package agent

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

const codeGraphVersion = "v1.3.1"

const maxCodeGraphDownloadBytes int64 = 512 << 20

type codeGraphArtifact struct {
	Asset  string
	SHA256 string
	Format string
	Target string
}

var codeGraphArtifacts = map[string]codeGraphArtifact{
	"darwin/arm64":  {Asset: "codegraph-darwin-arm64.tar.gz", SHA256: "d4931334e2497a4861b214ec077d78e5e38702a258fe4e05c33ed3bc1d144a90", Format: "tar.gz", Target: "darwin-arm64"},
	"darwin/amd64":  {Asset: "codegraph-darwin-x64.tar.gz", SHA256: "e9364cf8b104cf290c7c96ef1ed3dcd30d17af56583cdf0091efa0b001e3669e", Format: "tar.gz", Target: "darwin-x64"},
	"linux/arm64":   {Asset: "codegraph-linux-arm64.tar.gz", SHA256: "28130da6f6c7087d293337737dfca1040f0694996b0252c9528a7706a5721d8b", Format: "tar.gz", Target: "linux-arm64"},
	"linux/amd64":   {Asset: "codegraph-linux-x64.tar.gz", SHA256: "e605073f6eb170fe161e986c2350b6a0681e68018ed844ce57f72814c09fea1d", Format: "tar.gz", Target: "linux-x64"},
	"windows/arm64": {Asset: "codegraph-win32-arm64.zip", SHA256: "45f13d13dc7fd3dacc4c083fadec5ffa86f3e645dea7e4ca54fa057d135becef", Format: "zip", Target: "win32-arm64"},
	"windows/amd64": {Asset: "codegraph-win32-x64.zip", SHA256: "ffe76e64670f51c3335da8691174278446bd4b4af853e08c545564f4781629dd", Format: "zip", Target: "win32-x64"},
}

var codeGraphUserHomeDir = os.UserHomeDir
var codeGraphGOOS = runtime.GOOS
var codeGraphGOARCH = runtime.GOARCH
var codeGraphHTTPClient = &http.Client{Timeout: 90 * time.Second}

type codeGraphInstallPaths struct {
	cacheRoot      string
	bundleLauncher string
	marker         string
	stableCommand  string
}

func CfgUsesCodeGraph(cfg *config.SystemConfig) bool {
	return cfg != nil && cfg.CodeIntelligence.UsesCodeGraph()
}

func codeGraphArtifactForPlatform(goos, goarch string) (codeGraphArtifact, error) {
	platform := goos + "/" + goarch
	artifact, ok := codeGraphArtifacts[platform]
	if !ok {
		return codeGraphArtifact{}, fmt.Errorf("unsupported CodeGraph platform %s; supported platforms are darwin, linux, and windows on amd64 or arm64", platform)
	}
	return artifact, nil
}

func codeGraphManagedCommandPath() (string, error) {
	home, err := codeGraphAbsoluteHome()
	if err != nil {
		return "", err
	}
	name := "codegraph"
	if codeGraphGOOS == "windows" {
		name += ".cmd"
	}
	return filepath.Join(home, ".tshoot", "bin", name), nil
}

func EnsureCodeGraphInstalled(onLog func(string)) (string, error) {
	log := onLog
	if log == nil {
		log = func(string) {}
	}

	artifact, err := codeGraphArtifactForPlatform(codeGraphGOOS, codeGraphGOARCH)
	if err != nil {
		return "", err
	}
	paths, err := codeGraphPaths(artifact)
	if err != nil {
		return "", err
	}
	if codeGraphCacheValid(paths, artifact) {
		if err := refreshCodeGraphStableCommand(paths); err != nil {
			return "", fmt.Errorf("refresh CodeGraph command: %w", err)
		}
		log(fmt.Sprintf("[ok] CodeGraph %s cache hit: %s", codeGraphVersion, paths.cacheRoot))
		return paths.stableCommand, nil
	}

	parent := filepath.Dir(paths.cacheRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create CodeGraph cache parent: %w", err)
	}
	log(fmt.Sprintf("[info] downloading CodeGraph %s for %s/%s", codeGraphVersion, codeGraphGOOS, codeGraphGOARCH))
	archivePath, err := downloadCodeGraphArchive(parent, artifact)
	if err != nil {
		return "", err
	}
	defer os.Remove(archivePath)

	extractRoot, err := os.MkdirTemp(parent, "."+artifact.Target+".extract-")
	if err != nil {
		return "", fmt.Errorf("create CodeGraph extraction directory: %w", err)
	}
	defer os.RemoveAll(extractRoot)

	if err := extractCodeGraphArchive(archivePath, extractRoot, artifact); err != nil {
		return "", err
	}
	tempLauncher := filepath.Join(extractRoot, "bin", codeGraphLauncherName())
	if err := validateCodeGraphLauncher(tempLauncher); err != nil {
		return "", fmt.Errorf("validate extracted CodeGraph launcher: %w", err)
	}
	if err := os.WriteFile(filepath.Join(extractRoot, ".installed-sha256"), []byte(artifact.SHA256+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write CodeGraph digest marker: %w", err)
	}

	// Another install may have completed while this process downloaded. Reuse that
	// valid cache instead of replacing it.
	if !codeGraphCacheValid(paths, artifact) {
		if err := promoteCodeGraphDirectory(extractRoot, paths.cacheRoot); err != nil {
			return "", fmt.Errorf("promote CodeGraph cache: %w", err)
		}
	}
	if !codeGraphCacheValid(paths, artifact) {
		return "", fmt.Errorf("CodeGraph cache failed post-install validation at %s", paths.cacheRoot)
	}
	if err := refreshCodeGraphStableCommand(paths); err != nil {
		return "", fmt.Errorf("create CodeGraph command: %w", err)
	}
	log(fmt.Sprintf("[ok] CodeGraph %s installed: %s", codeGraphVersion, paths.stableCommand))
	return paths.stableCommand, nil
}

func codeGraphAbsoluteHome() (string, error) {
	home, err := codeGraphUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home for CodeGraph: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("find user home for CodeGraph: empty path")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("make CodeGraph home absolute: %w", err)
	}
	return filepath.Clean(home), nil
}

func codeGraphPaths(artifact codeGraphArtifact) (codeGraphInstallPaths, error) {
	home, err := codeGraphAbsoluteHome()
	if err != nil {
		return codeGraphInstallPaths{}, err
	}
	cacheRoot := filepath.Join(home, ".tshoot", "tools", "codegraph", codeGraphVersion, artifact.Target)
	stableCommand, err := codeGraphManagedCommandPath()
	if err != nil {
		return codeGraphInstallPaths{}, err
	}
	return codeGraphInstallPaths{
		cacheRoot:      cacheRoot,
		bundleLauncher: filepath.Join(cacheRoot, "bin", codeGraphLauncherName()),
		marker:         filepath.Join(cacheRoot, ".installed-sha256"),
		stableCommand:  stableCommand,
	}, nil
}

func codeGraphLauncherName() string {
	if codeGraphGOOS == "windows" {
		return "codegraph.cmd"
	}
	return "codegraph"
}

func codeGraphCacheValid(paths codeGraphInstallPaths, artifact codeGraphArtifact) bool {
	markerInfo, err := os.Lstat(paths.marker)
	if err != nil || !markerInfo.Mode().IsRegular() {
		return false
	}
	marker, err := os.ReadFile(paths.marker)
	if err != nil || strings.TrimSpace(string(marker)) != artifact.SHA256 {
		return false
	}
	return validateCodeGraphLauncher(paths.bundleLauncher) == nil
}

func validateCodeGraphLauncher(launcher string) error {
	info, err := os.Lstat(launcher)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", launcher)
	}
	if codeGraphGOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", launcher)
	}
	return nil
}

func downloadCodeGraphArchive(parent string, artifact codeGraphArtifact) (archivePath string, err error) {
	url := "https://github.com/colbymchenry/codegraph/releases/download/" + codeGraphVersion + "/" + artifact.Asset
	resp, err := codeGraphHTTPClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("download CodeGraph %s: %w", codeGraphVersion, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download CodeGraph %s: HTTP %d from %s", codeGraphVersion, resp.StatusCode, url)
	}
	if resp.ContentLength > maxCodeGraphDownloadBytes {
		return "", fmt.Errorf("download CodeGraph %s: compressed archive is larger than %d bytes", codeGraphVersion, maxCodeGraphDownloadBytes)
	}

	archive, err := os.CreateTemp(parent, "."+artifact.Target+".download-")
	if err != nil {
		return "", fmt.Errorf("create CodeGraph temporary archive: %w", err)
	}
	archivePath = archive.Name()
	tempArchivePath := archivePath
	keep := false
	defer func() {
		if closeErr := archive.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close CodeGraph temporary archive: %w", closeErr)
		}
		if !keep || err != nil {
			_ = os.Remove(tempArchivePath)
		}
	}()

	hash := sha256.New()
	limited := io.LimitReader(resp.Body, maxCodeGraphDownloadBytes+1)
	n, err := io.Copy(io.MultiWriter(archive, hash), limited)
	if err != nil {
		return "", fmt.Errorf("stream CodeGraph archive: %w", err)
	}
	if n > maxCodeGraphDownloadBytes {
		return "", fmt.Errorf("download CodeGraph %s: compressed archive exceeds %d bytes", codeGraphVersion, maxCodeGraphDownloadBytes)
	}
	gotDigest := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(gotDigest, artifact.SHA256) {
		return "", fmt.Errorf("CodeGraph archive SHA256 mismatch: got %s, want %s", gotDigest, artifact.SHA256)
	}
	keep = true
	return archivePath, nil
}

func extractCodeGraphArchive(archivePath, extractRoot string, artifact codeGraphArtifact) error {
	switch artifact.Format {
	case "tar.gz":
		return extractCodeGraphTarGz(archivePath, extractRoot, artifact)
	case "zip":
		return extractCodeGraphZip(archivePath, extractRoot, artifact)
	default:
		return fmt.Errorf("unsupported CodeGraph archive format %q", artifact.Format)
	}
}

func extractCodeGraphTarGz(archivePath, extractRoot string, artifact codeGraphArtifact) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open CodeGraph tar archive: %w", err)
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open CodeGraph gzip stream: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read CodeGraph tar archive: %w", err)
		}
		destination, err := codeGraphArchiveDestination(extractRoot, artifact, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return fmt.Errorf("create CodeGraph archive directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeCodeGraphArchiveFile(destination, mode, reader); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("CodeGraph archive link entry %q is not allowed", header.Name)
		default:
			return fmt.Errorf("CodeGraph archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}
}

func extractCodeGraphZip(archivePath, extractRoot string, artifact codeGraphArtifact) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open CodeGraph zip archive: %w", err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		destination, err := codeGraphArchiveDestination(extractRoot, artifact, entry.Name)
		if err != nil {
			return err
		}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("CodeGraph archive link entry %q is not allowed", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return fmt.Errorf("create CodeGraph archive directory: %w", err)
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("CodeGraph archive entry %q is not a regular file", entry.Name)
		}
		source, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open CodeGraph zip entry %q: %w", entry.Name, err)
		}
		fileErr := writeCodeGraphArchiveFile(destination, mode.Perm(), source)
		closeErr := source.Close()
		if fileErr != nil {
			return fileErr
		}
		if closeErr != nil {
			return fmt.Errorf("close CodeGraph zip entry %q: %w", entry.Name, closeErr)
		}
	}
	return nil
}

func codeGraphArchiveDestination(extractRoot string, artifact codeGraphArtifact, archiveName string) (string, error) {
	if archiveName == "" || strings.ContainsRune(archiveName, '\x00') {
		return "", fmt.Errorf("CodeGraph archive contains an invalid empty path")
	}
	cleanName := path.Clean(archiveName)
	if path.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", fmt.Errorf("CodeGraph archive path %q escapes the extraction root", archiveName)
	}
	bundleRoot := "codegraph-" + artifact.Target
	var relative string
	switch {
	case cleanName == bundleRoot:
		relative = "."
	case strings.HasPrefix(cleanName, bundleRoot+"/"):
		relative = strings.TrimPrefix(cleanName, bundleRoot+"/")
	default:
		return "", fmt.Errorf("CodeGraph archive path %q is outside expected bundle root %q", archiveName, bundleRoot)
	}

	converted := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" {
		return "", fmt.Errorf("CodeGraph archive path %q is absolute", archiveName)
	}
	destination := filepath.Join(extractRoot, converted)
	relToRoot, err := filepath.Rel(extractRoot, destination)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("CodeGraph archive path %q escapes the extraction root", archiveName)
	}
	return destination, nil
}

func writeCodeGraphArchiveFile(destination string, mode os.FileMode, source io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create CodeGraph archive parent: %w", err)
	}
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create CodeGraph archive file %q: %w", destination, err)
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("extract CodeGraph archive file %q: %w", destination, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("close CodeGraph archive file %q: %w", destination, closeErr)
	}
	if err := os.Chmod(destination, mode.Perm()); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("set CodeGraph archive file mode %q: %w", destination, err)
	}
	return nil
}

func promoteCodeGraphDirectory(extractRoot, cacheRoot string) error {
	if _, err := os.Lstat(cacheRoot); os.IsNotExist(err) {
		return os.Rename(extractRoot, cacheRoot)
	} else if err != nil {
		return err
	}

	backupRoot, err := unusedCodeGraphPath(filepath.Dir(cacheRoot), "."+filepath.Base(cacheRoot)+".backup-")
	if err != nil {
		return err
	}
	if err := os.Rename(cacheRoot, backupRoot); err != nil {
		return err
	}
	if err := os.Rename(extractRoot, cacheRoot); err != nil {
		if rollbackErr := os.Rename(backupRoot, cacheRoot); rollbackErr != nil {
			return fmt.Errorf("rename new cache: %v; rollback old cache: %w", err, rollbackErr)
		}
		return err
	}
	if err := os.RemoveAll(backupRoot); err != nil {
		return fmt.Errorf("remove replaced CodeGraph cache backup: %w", err)
	}
	return nil
}

func refreshCodeGraphStableCommand(paths codeGraphInstallPaths) error {
	if err := os.MkdirAll(filepath.Dir(paths.stableCommand), 0o755); err != nil {
		return err
	}
	if codeGraphGOOS == "windows" {
		contents := fmt.Sprintf("@call %q %%*\r\n", paths.bundleLauncher)
		return writeCodeGraphStableFile(paths.stableCommand, []byte(contents), 0o644)
	}

	relativeTarget, err := filepath.Rel(filepath.Dir(paths.stableCommand), paths.bundleLauncher)
	if err != nil {
		return err
	}
	tempPath, err := unusedCodeGraphPath(filepath.Dir(paths.stableCommand), ".codegraph-link-")
	if err != nil {
		return err
	}
	if err := os.Symlink(relativeTarget, tempPath); err != nil {
		return err
	}
	if err := replaceCodeGraphPath(tempPath, paths.stableCommand); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func writeCodeGraphStableFile(destination string, contents []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(destination), ".codegraph-command-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceCodeGraphPath(tempPath, destination)
}

func replaceCodeGraphPath(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if _, err := os.Lstat(destination); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil {
		return err
	}
	return os.Rename(source, destination)
}

func unusedCodeGraphPath(parent, pattern string) (string, error) {
	temp, err := os.CreateTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	name := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}
