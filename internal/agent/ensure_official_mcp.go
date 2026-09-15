package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Only reviewed releases are installed. PATH binaries are deliberately not used:
// an older Consul MCP may still contain the backend/token isolation defects.
type officialMCPRelease struct {
	name, version, format string
	hashes                map[string]string
}

var consulMCPRelease = officialMCPRelease{"consul-mcp-server", "0.1.4", "zip", map[string]string{
	"darwin/amd64":  "10d4c6f3bc170f269538c0e10ffe19812c18034a7b7550a3c01283a825c8aa7f",
	"darwin/arm64":  "6acfaadba2434c1b534ad7f0d6e6dc5f75199f806bde06e5b8a36e87c62be814",
	"linux/amd64":   "8ed2e7401868b452672b28f33eaf856e3773255b52f3421e6ffa29f43c8461b5",
	"linux/arm64":   "44bc9128a2cbead861469dd28a15048b45754c7ff331c8a2170362087c632699",
	"windows/amd64": "7989eeb2b76f2f146b084d7214ef82da0ecb5cf9441abb7e55cdc599ea539f68",
}}
var skywalkingMCPRelease = officialMCPRelease{"swmcp", "0.2.0", "tar.gz", map[string]string{
	"darwin/amd64": "346877c67fd8c7bf54bd130ed0ab1243ad1b90a331eeaea1b0b42e5089f565d4",
	"darwin/arm64": "e7eb0471546b8b0e468ef345f02eefcedcd6c48a3eb08b872eb557529fe8bc4e",
	"linux/amd64":  "f60e51c6a57be46b59e931dc286a81007d73ba00926f6454ddad248ae0609b07",
	"linux/arm64":  "c3b73a7e7310300824e034f8bfecb323dbb5a3709505634c3fa44d4c4e67632c",
}}

func (r officialMCPRelease) url(goos, arch string) string {
	if r.name == "consul-mcp-server" {
		return fmt.Sprintf("https://releases.hashicorp.com/consul-mcp-server/%s/consul-mcp-server_%s_%s_%s.zip", r.version, r.version, goos, arch)
	}
	return fmt.Sprintf("https://github.com/apache/skywalking-mcp/releases/download/v%s/swmcp-v%s-%s-%s.tar.gz", r.version, r.version, goos, arch)
}

var ensureOfficialMCP = func(r officialMCPRelease, log func(string)) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return installOfficialMCP(r, runtime.GOOS, runtime.GOARCH, home, &http.Client{Timeout: 5 * time.Minute}, log)
}

const maxOfficialMCPBytes = 128 << 20

func installOfficialMCP(r officialMCPRelease, goos, arch, home string, client *http.Client, log func(string)) (string, error) {
	expected := r.hashes[goos+"/"+arch]
	if expected == "" {
		return "", fmt.Errorf("%s %s 暂无 %s/%s 发布包，继续使用 HTTP/API", r.name, r.version, goos, arch)
	}
	binary := r.name
	if goos == "windows" {
		binary += ".exe"
	}
	dest := filepath.Join(home, ".tshoot", "bin", r.name+"-"+r.version+"-"+goos+"-"+arch, binary)
	marker := dest + ".sha256"
	if info, err := os.Lstat(dest); err == nil && info.Mode().IsRegular() && (goos == "windows" || info.Mode().Perm()&0o111 != 0) {
		data, err := os.ReadFile(dest)
		saved, _ := os.ReadFile(marker)
		if err == nil && string(saved) == fmt.Sprintf("%s:%x", expected, sha256.Sum256(data)) {
			return dest, nil
		}
	}
	if log != nil {
		log(fmt.Sprintf("[info] 安装官方 %s %s (%s/%s)", r.name, r.version, goos, arch))
	}
	resp, err := client.Get(r.url(goos, arch))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", r.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", r.name, resp.StatusCode)
	}
	archive, err := io.ReadAll(io.LimitReader(resp.Body, maxOfficialMCPBytes+1))
	if err != nil {
		return "", err
	}
	if len(archive) > maxOfficialMCPBytes {
		return "", fmt.Errorf("%s archive too large", r.name)
	}
	if fmt.Sprintf("%x", sha256.Sum256(archive)) != expected {
		return "", fmt.Errorf("%s SHA256 mismatch", r.name)
	}
	data, err := extractOfficialMCP(archive, r.format, binary)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := writeOfficialMCPFile(dest, data, 0o755); err != nil {
		return "", err
	}
	if err := writeOfficialMCPFile(marker, []byte(fmt.Sprintf("%s:%x", expected, sha256.Sum256(data))), 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func writeOfficialMCPFile(dest string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".mcp-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// Extract only the exact executable; archive paths, links and other files never
// become filesystem paths. Both compressed input and executable size are bounded.
func extractOfficialMCP(archive []byte, format, binary string) ([]byte, error) {
	var found []byte
	read := func(r io.Reader) error {
		if found != nil {
			return fmt.Errorf("duplicate executable %s", binary)
		}
		b, err := io.ReadAll(io.LimitReader(r, maxOfficialMCPBytes+1))
		if err != nil {
			return err
		}
		if len(b) == 0 || len(b) > maxOfficialMCPBytes {
			return fmt.Errorf("invalid executable size")
		}
		found = b
		return nil
	}
	switch format {
	case "zip":
		z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if f.Name != binary {
				continue
			}
			if !f.Mode().IsRegular() {
				return nil, fmt.Errorf("executable must be a regular file")
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = read(rc)
			closeErr := rc.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
	case "tar.gz":
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		tr := tar.NewReader(io.LimitReader(gz, maxOfficialMCPBytes+1))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if strings.TrimPrefix(h.Name, "./") != binary {
				continue
			}
			if h.Typeflag != tar.TypeReg {
				return nil, fmt.Errorf("executable must be a regular file")
			}
			if err := read(tr); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported archive format %s", format)
	}
	if found == nil {
		return nil, fmt.Errorf("executable %s missing", binary)
	}
	return found, nil
}
