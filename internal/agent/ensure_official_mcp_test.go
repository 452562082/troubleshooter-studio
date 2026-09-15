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
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type officialRoundTrip func(*http.Request) (*http.Response, error)

func (f officialRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func officialArchive(t *testing.T, format, name string, link bool) []byte {
	t.Helper()
	var b bytes.Buffer
	if format == "zip" {
		z := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		if link {
			h.SetMode(os.ModeSymlink | 0o777)
		}
		f, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte("fixture-executable")); err != nil {
			t.Fatal(err)
		}
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		h := &tar.Header{Name: name, Mode: 0o755, Size: 18, Typeflag: tar.TypeReg}
		if link {
			h.Typeflag = tar.TypeSymlink
			h.Linkname = "/tmp/other"
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !link {
			if _, err := tw.Write([]byte("fixture-executable")); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

func TestInstallOfficialMCPVerifiesArchiveAndCache(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			archive := officialArchive(t, format, "fixture", false)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write(archive) }))
			defer server.Close()
			client := server.Client()
			transport := client.Transport
			client.Transport = officialRoundTrip(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme = "http"
				r.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return transport.RoundTrip(r)
			})
			release := officialMCPRelease{name: "fixture", version: "1", format: format, hashes: map[string]string{"linux/amd64": fmt.Sprintf("%x", sha256.Sum256(archive))}}
			home := t.TempDir()
			dest, err := installOfficialMCP(release, "linux", "amd64", home, client, nil)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(dest); err != nil || string(b) != "fixture-executable" {
				t.Fatalf("bad installed executable: %v", err)
			}
			if _, err := installOfficialMCP(release, "linux", "amd64", home, client, nil); err != nil || calls != 1 {
				t.Fatalf("cache not reused: %v calls=%d", err, calls)
			}
			if err := os.WriteFile(dest, []byte("corrupt"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := installOfficialMCP(release, "linux", "amd64", home, client, nil); err != nil || calls != 2 {
				t.Fatalf("corrupt cache reused: %v", err)
			}
			release.hashes["linux/amd64"] = strings.Repeat("0", 64)
			if _, err := installOfficialMCP(release, "linux", "amd64", t.TempDir(), client, nil); err == nil || !strings.Contains(err.Error(), "SHA256") {
				t.Fatalf("bad digest accepted: %v", err)
			}
			before := calls
			if _, err := installOfficialMCP(release, "unknown", "amd64", t.TempDir(), client, nil); err == nil || calls != before {
				t.Fatal("unsupported platform must not download")
			}
		})
	}
}

func TestExtractOfficialMCPRejectsMissingOrLinkedExecutable(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		for _, name := range []string{"../fixture", "other"} {
			if _, err := extractOfficialMCP(officialArchive(t, format, name, false), format, "fixture"); err == nil {
				t.Fatal("invalid archive name accepted")
			}
		}
		if _, err := extractOfficialMCP(officialArchive(t, format, "fixture", true), format, "fixture"); err == nil {
			t.Fatal("linked executable accepted")
		}
		if _, err := extractOfficialMCP([]byte("broken archive"), format, "fixture"); err == nil {
			t.Fatal("corrupt archive accepted")
		}
	}
	if _, err := extractOfficialMCP(nil, "other", "fixture"); err == nil {
		t.Fatal("unsupported format accepted")
	}
}

func TestInstallOfficialMCPHTTPFailure(t *testing.T) {
	client := &http.Client{Transport: officialRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("secret-response"))}, nil
	})}
	_, err := installOfficialMCP(consulMCPRelease, "darwin", "arm64", t.TempDir(), client, nil)
	if err == nil || strings.Contains(err.Error(), "secret-response") {
		t.Fatalf("invalid error %v", err)
	}
}
