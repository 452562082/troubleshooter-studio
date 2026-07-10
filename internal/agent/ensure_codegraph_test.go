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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestCodeGraphArtifactForPlatform_AllSupported(t *testing.T) {
	want := map[string]codeGraphArtifact{
		"darwin/arm64":  {Asset: "codegraph-darwin-arm64.tar.gz", SHA256: "d4931334e2497a4861b214ec077d78e5e38702a258fe4e05c33ed3bc1d144a90", Format: "tar.gz", Target: "darwin-arm64"},
		"darwin/amd64":  {Asset: "codegraph-darwin-x64.tar.gz", SHA256: "e9364cf8b104cf290c7c96ef1ed3dcd30d17af56583cdf0091efa0b001e3669e", Format: "tar.gz", Target: "darwin-x64"},
		"linux/arm64":   {Asset: "codegraph-linux-arm64.tar.gz", SHA256: "28130da6f6c7087d293337737dfca1040f0694996b0252c9528a7706a5721d8b", Format: "tar.gz", Target: "linux-arm64"},
		"linux/amd64":   {Asset: "codegraph-linux-x64.tar.gz", SHA256: "e605073f6eb170fe161e986c2350b6a0681e68018ed844ce57f72814c09fea1d", Format: "tar.gz", Target: "linux-x64"},
		"windows/arm64": {Asset: "codegraph-win32-arm64.zip", SHA256: "45f13d13dc7fd3dacc4c083fadec5ffa86f3e645dea7e4ca54fa057d135becef", Format: "zip", Target: "win32-arm64"},
		"windows/amd64": {Asset: "codegraph-win32-x64.zip", SHA256: "ffe76e64670f51c3335da8691174278446bd4b4af853e08c545564f4781629dd", Format: "zip", Target: "win32-x64"},
	}

	if len(codeGraphArtifacts) != len(want) {
		t.Fatalf("codeGraphArtifacts has %d entries, want %d", len(codeGraphArtifacts), len(want))
	}
	for platform, expected := range want {
		parts := strings.Split(platform, "/")
		got, err := codeGraphArtifactForPlatform(parts[0], parts[1])
		if err != nil {
			t.Fatalf("codeGraphArtifactForPlatform(%q) error = %v", platform, err)
		}
		if got != expected {
			t.Errorf("codeGraphArtifactForPlatform(%q) = %#v, want %#v", platform, got, expected)
		}
	}
}

func TestCfgUsesCodeGraph(t *testing.T) {
	if CfgUsesCodeGraph(nil) {
		t.Fatal("nil config must not use CodeGraph")
	}
	cfg := &config.SystemConfig{}
	if CfgUsesCodeGraph(cfg) {
		t.Fatal("zero config must not use CodeGraph")
	}
	cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: config.CodeIntelligenceProviderCodeGraph}
	if !CfgUsesCodeGraph(cfg) {
		t.Fatal("enabled codegraph config must use CodeGraph")
	}
}

func TestEnsureCodeGraphInstalled_InstallsTarBundleAndRelativeStableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink semantics")
	}
	archive := makeCodeGraphTarGz(t,
		tarTestEntry{Name: "codegraph-linux-x64/bin/codegraph", Body: []byte("#!/bin/sh\nexit 0\n"), Mode: 0o755, Type: tar.TypeReg},
		tarTestEntry{Name: "codegraph-linux-x64/README.md", Body: []byte("fixture\n"), Mode: 0o644, Type: tar.TypeReg},
	)
	home, requests := setCodeGraphTestEnvironment(t, "linux", "amd64", archive, http.StatusOK, true)

	got, err := EnsureCodeGraphInstalled(nil)
	if err != nil {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v", err)
	}
	wantStable := filepath.Join(home, ".tshoot", "bin", "codegraph")
	if got != wantStable || !filepath.IsAbs(got) {
		t.Fatalf("EnsureCodeGraphInstalled() = %q, want absolute %q", got, wantStable)
	}
	if *requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", *requests)
	}
	wantURL := "https://github.com/colbymchenry/codegraph/releases/download/v1.3.1/codegraph-linux-x64.tar.gz"
	if codeGraphTestLastRequestURL != wantURL {
		t.Errorf("download URL = %q, want %q", codeGraphTestLastRequestURL, wantURL)
	}
	linkTarget, err := os.Readlink(got)
	if err != nil {
		t.Fatalf("stable command is not a symlink: %v", err)
	}
	if filepath.IsAbs(linkTarget) {
		t.Errorf("stable symlink target must be relative, got %q", linkTarget)
	}
	bundleLauncher := codeGraphTestBundleLauncher(home, "linux-x64", "linux")
	resolved := filepath.Clean(filepath.Join(filepath.Dir(got), linkTarget))
	if resolved != bundleLauncher {
		t.Errorf("stable symlink resolves to %q, want %q", resolved, bundleLauncher)
	}
	info, err := os.Lstat(bundleLauncher)
	if err != nil {
		t.Fatalf("bundle launcher missing: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("bundle launcher mode = %v, want executable regular file", info.Mode())
	}
	marker := filepath.Join(filepath.Dir(filepath.Dir(bundleLauncher)), ".installed-sha256")
	assertFileContents(t, marker, codeGraphArtifacts["linux/amd64"].SHA256+"\n")
	assertNoCodeGraphTemps(t, filepath.Dir(filepath.Dir(filepath.Dir(bundleLauncher))))
}

func TestEnsureCodeGraphInstalled_InstallsZipBundleAndWindowsStableCommand(t *testing.T) {
	archive := makeCodeGraphZip(t,
		zipTestEntry{Name: "codegraph-win32-x64/bin/codegraph.cmd", Body: []byte("@echo off\r\n"), Mode: 0o644},
		zipTestEntry{Name: "codegraph-win32-x64/README.md", Body: []byte("fixture\r\n"), Mode: 0o644},
	)
	home, requests := setCodeGraphTestEnvironment(t, "windows", "amd64", archive, http.StatusOK, true)

	got, err := EnsureCodeGraphInstalled(nil)
	if err != nil {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v", err)
	}
	want := filepath.Join(home, ".tshoot", "bin", "codegraph.cmd")
	if got != want || !filepath.IsAbs(got) {
		t.Fatalf("EnsureCodeGraphInstalled() = %q, want absolute %q", got, want)
	}
	if *requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", *requests)
	}
	bundleLauncher := codeGraphTestBundleLauncher(home, "win32-x64", "windows")
	assertFileContents(t, got, fmt.Sprintf("@call %q %%*\r\n", bundleLauncher))
	assertFileContents(t, filepath.Join(filepath.Dir(filepath.Dir(bundleLauncher)), ".installed-sha256"), codeGraphArtifacts["windows/amd64"].SHA256+"\n")
}

func TestEnsureCodeGraphInstalled_CacheHitDoesNotDownload(t *testing.T) {
	goos := "linux"
	target := "linux-x64"
	if runtime.GOOS == "windows" {
		goos = "windows"
		target = "win32-x64"
	}
	home, requests := setCodeGraphTestEnvironment(t, goos, "amd64", nil, http.StatusInternalServerError, false)
	artifact := codeGraphArtifacts[goos+"/amd64"]
	launcher := codeGraphTestBundleLauncher(home, target, goos)
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if goos != "windows" {
		mode = 0o755
	}
	if err := os.WriteFile(launcher, []byte("cached"), mode); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(filepath.Dir(launcher)), ".installed-sha256")
	if err := os.WriteFile(marker, []byte(artifact.SHA256+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := EnsureCodeGraphInstalled(nil)
	if err != nil {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v", err)
	}
	if *requests != 0 {
		t.Fatalf("cache hit made %d HTTP requests, want 0", *requests)
	}
	want, err := codeGraphManagedCommandPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("EnsureCodeGraphInstalled() = %q, want %q", got, want)
	}
}

func TestEnsureCodeGraphInstalled_RejectsSHA256Mismatch(t *testing.T) {
	archive := makeCodeGraphTarGz(t,
		tarTestEntry{Name: "codegraph-linux-x64/bin/codegraph", Body: []byte("valid archive, untrusted bytes"), Mode: 0o755, Type: tar.TypeReg},
		tarTestEntry{Name: "codegraph-linux-x64/README.md", Body: []byte("fixture"), Mode: 0o644, Type: tar.TypeReg},
	)
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", archive, http.StatusOK, false)

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want SHA-256 mismatch", err)
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_RejectsArchiveTraversal(t *testing.T) {
	archive := makeCodeGraphTarGz(t,
		tarTestEntry{Name: "codegraph-linux-x64/bin/codegraph", Body: []byte("partial"), Mode: 0o755, Type: tar.TypeReg},
		tarTestEntry{Name: "../../escaped", Body: []byte("escape"), Mode: 0o644, Type: tar.TypeReg},
	)
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", archive, http.StatusOK, true)

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "archive") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want unsafe archive path", err)
	}
	for _, escaped := range []string{
		filepath.Join(home, "escaped"),
		filepath.Join(home, ".tshoot", "tools", "codegraph", "escaped"),
	} {
		if _, statErr := os.Stat(escaped); !os.IsNotExist(statErr) {
			t.Fatalf("archive escaped extraction root to %s: stat error = %v", escaped, statErr)
		}
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_RejectsArchiveLinks(t *testing.T) {
	archive := makeCodeGraphTarGz(t,
		tarTestEntry{Name: "codegraph-linux-x64/bin/codegraph", Body: []byte("partial"), Mode: 0o755, Type: tar.TypeReg},
		tarTestEntry{Name: "codegraph-linux-x64/bin/alias", Mode: 0o755, Type: tar.TypeSymlink, Linkname: "codegraph"},
	)
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", archive, http.StatusOK, true)

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "link") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want archive link rejection", err)
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_DownloadFailureLeavesNoExecutable(t *testing.T) {
	home, requests := setCodeGraphTestEnvironment(t, "linux", "amd64", []byte("unavailable"), http.StatusServiceUnavailable, false)

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want HTTP 503", err)
	}
	if *requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", *requests)
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_RejectsAdvertisedBodyOverLimit(t *testing.T) {
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", nil, http.StatusOK, false)
	codeGraphHTTPClient = &http.Client{Transport: codeGraphRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			ContentLength: maxCodeGraphDownloadBytes + 1,
			Body:          io.NopCloser(bytes.NewReader(nil)),
			Request:       req,
		}, nil
	})}

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "larger than") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want advertised-size rejection", err)
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_RejectsStreamedBodyOverLimit(t *testing.T) {
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", nil, http.StatusOK, false)
	const unreadTail = int64(4096)
	body := &sizedZeroReadCloser{remaining: maxCodeGraphDownloadBytes + 1 + unreadTail}
	codeGraphHTTPClient = &http.Client{Transport: codeGraphRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			ContentLength: -1,
			Body:          body,
			Request:       req,
		}, nil
	})}

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "exceeds") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want streamed-size rejection", err)
	}
	if body.remaining != unreadTail {
		t.Errorf("stream reader has %d bytes remaining, want %d after early stop at the over-limit sentinel", body.remaining, unreadTail)
	}
	assertCodeGraphNotPromoted(t, home, "linux-x64", "linux")
}

func TestEnsureCodeGraphInstalled_InvalidMarkerDoesNotReuseCache(t *testing.T) {
	home, requests := setCodeGraphTestEnvironment(t, "linux", "amd64", []byte("unavailable"), http.StatusServiceUnavailable, false)
	launcher := codeGraphTestBundleLauncher(home, "linux-x64", "linux")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("old cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(filepath.Dir(launcher)), ".installed-sha256")
	if err := os.WriteFile(marker, []byte("wrong digest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil {
		t.Fatal("EnsureCodeGraphInstalled() unexpectedly reused invalid cache")
	}
	if *requests != 1 {
		t.Fatalf("invalid cache made %d HTTP requests, want 1", *requests)
	}
	assertFileContents(t, launcher, "old cache")
	if _, statErr := os.Lstat(filepath.Join(home, ".tshoot", "bin", "codegraph")); !os.IsNotExist(statErr) {
		t.Fatalf("stable command exists after failed refresh: %v", statErr)
	}
}

func TestEnsureCodeGraphInstalled_AtomicallyReplacesInvalidCacheAfterCompleteExtraction(t *testing.T) {
	archive := makeCodeGraphTarGz(t,
		tarTestEntry{Name: "codegraph-linux-x64/bin/codegraph", Body: []byte("new launcher"), Mode: 0o755, Type: tar.TypeReg},
		tarTestEntry{Name: "codegraph-linux-x64/README.md", Body: []byte("complete"), Mode: 0o644, Type: tar.TypeReg},
	)
	home, _ := setCodeGraphTestEnvironment(t, "linux", "amd64", archive, http.StatusOK, true)
	launcher := codeGraphTestBundleLauncher(home, "linux-x64", "linux")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("old launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(filepath.Dir(launcher)), ".installed-sha256")
	if err := os.WriteFile(marker, []byte("old digest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureCodeGraphInstalled(nil); err != nil {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v", err)
	}
	assertFileContents(t, launcher, "new launcher")
	assertFileContents(t, marker, codeGraphArtifacts["linux/amd64"].SHA256+"\n")
	assertNoCodeGraphTemps(t, filepath.Dir(filepath.Dir(filepath.Dir(launcher))))
}

func TestEnsureCodeGraphInstalled_ConcurrentPromotionsAcceptValidWinner(t *testing.T) {
	parent := t.TempDir()
	artifact := codeGraphArtifacts["linux/amd64"]
	cacheRoot := filepath.Join(parent, artifact.Target)
	paths := codeGraphInstallPaths{
		cacheRoot:      cacheRoot,
		bundleLauncher: filepath.Join(cacheRoot, "bin", "codegraph"),
		marker:         filepath.Join(cacheRoot, ".installed-sha256"),
		stableCommand:  filepath.Join(parent, "bin", "codegraph"),
	}
	writeCodeGraphTestCache(t, cacheRoot, "invalid launcher", "invalid digest")
	extractOne := filepath.Join(parent, ".linux-x64.extract-one")
	extractTwo := filepath.Join(parent, ".linux-x64.extract-two")
	writeCodeGraphTestCache(t, extractOne, "winner one", artifact.SHA256)
	writeCodeGraphTestCache(t, extractTwo, "winner two", artifact.SHA256)

	oldRename := codeGraphRename
	var backupAttempts atomic.Int32
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	codeGraphRename = func(oldPath, newPath string) error {
		if oldPath == cacheRoot && strings.Contains(filepath.Base(newPath), ".backup-") && backupAttempts.Add(1) <= 2 {
			ready <- struct{}{}
			<-release
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { codeGraphRename = oldRename })

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, extractRoot := range []string{extractOne, extractTwo} {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			errs <- promoteCodeGraphDirectory(root, paths, artifact)
		}(extractRoot)
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent promotion error = %v", err)
		}
	}
	if !codeGraphCacheValid(paths, artifact) {
		t.Fatal("concurrent promotion did not leave a valid cache winner")
	}
	assertNoCodeGraphTemps(t, parent)
}

func TestEnsureCodeGraphInstalled_UnsupportedPlatform(t *testing.T) {
	home, requests := setCodeGraphTestEnvironment(t, "freebsd", "amd64", nil, http.StatusOK, false)

	_, err := EnsureCodeGraphInstalled(nil)
	if err == nil || !strings.Contains(err.Error(), "freebsd/amd64") || !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Fatalf("EnsureCodeGraphInstalled() error = %v, want actionable unsupported-platform error", err)
	}
	if *requests != 0 {
		t.Fatalf("unsupported platform made %d HTTP requests, want 0", *requests)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".tshoot")); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported platform changed filesystem: %v", statErr)
	}
}

type codeGraphRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codeGraphRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type sizedZeroReadCloser struct {
	remaining int64
}

func (r *sizedZeroReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	return int(n), nil
}

func (*sizedZeroReadCloser) Close() error { return nil }

var codeGraphTestLastRequestURL string

func setCodeGraphTestEnvironment(t *testing.T, goos, goarch string, body []byte, status int, useBodyDigest bool) (string, *int) {
	t.Helper()
	oldHome := codeGraphUserHomeDir
	oldGOOS := codeGraphGOOS
	oldGOARCH := codeGraphGOARCH
	oldClient := codeGraphHTTPClient
	oldArtifacts := codeGraphArtifacts
	oldLastURL := codeGraphTestLastRequestURL
	t.Cleanup(func() {
		codeGraphUserHomeDir = oldHome
		codeGraphGOOS = oldGOOS
		codeGraphGOARCH = oldGOARCH
		codeGraphHTTPClient = oldClient
		codeGraphArtifacts = oldArtifacts
		codeGraphTestLastRequestURL = oldLastURL
	})

	home := t.TempDir()
	codeGraphUserHomeDir = func() (string, error) { return home, nil }
	codeGraphGOOS = goos
	codeGraphGOARCH = goarch
	codeGraphTestLastRequestURL = ""
	requests := 0
	codeGraphHTTPClient = &http.Client{Transport: codeGraphRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		codeGraphTestLastRequestURL = req.URL.String()
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})}
	if useBodyDigest {
		artifacts := cloneCodeGraphArtifacts(oldArtifacts)
		artifact, ok := artifacts[goos+"/"+goarch]
		if !ok {
			t.Fatalf("test platform %s/%s is not supported", goos, goarch)
		}
		digest := sha256.Sum256(body)
		artifact.SHA256 = fmt.Sprintf("%x", digest)
		artifacts[goos+"/"+goarch] = artifact
		codeGraphArtifacts = artifacts
	}
	return home, &requests
}

func cloneCodeGraphArtifacts(src map[string]codeGraphArtifact) map[string]codeGraphArtifact {
	dst := make(map[string]codeGraphArtifact, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func codeGraphTestBundleLauncher(home, target, goos string) string {
	name := "codegraph"
	if goos == "windows" {
		name += ".cmd"
	}
	return filepath.Join(home, ".tshoot", "tools", "codegraph", codeGraphVersion, target, "bin", name)
}

func assertCodeGraphNotPromoted(t *testing.T, home, target, goos string) {
	t.Helper()
	launcher := codeGraphTestBundleLauncher(home, target, goos)
	cacheRoot := filepath.Dir(filepath.Dir(launcher))
	stableName := "codegraph"
	if goos == "windows" {
		stableName += ".cmd"
	}
	for _, path := range []string{cacheRoot, filepath.Join(home, ".tshoot", "bin", stableName)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("failed install left %s behind: %v", path, err)
		}
	}
	assertNoCodeGraphTemps(t, filepath.Dir(cacheRoot))
}

func assertNoCodeGraphTemps(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ".download-") || strings.Contains(name, ".extract-") || strings.Contains(name, ".backup-") {
			t.Errorf("temporary installer artifact was not cleaned: %s", filepath.Join(parent, name))
		}
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s contents = %q, want %q", path, got, want)
	}
}

func writeCodeGraphTestCache(t *testing.T, root, launcherContents, digest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "codegraph"), []byte(launcherContents), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".installed-sha256"), []byte(digest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

type tarTestEntry struct {
	Name     string
	Body     []byte
	Mode     int64
	Type     byte
	Linkname string
}

func makeCodeGraphTarGz(t *testing.T, entries ...tarTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     entry.Mode,
			Size:     int64(len(entry.Body)),
			Typeflag: entry.Type,
			Linkname: entry.Linkname,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.Body) > 0 {
			if _, err := tw.Write(entry.Body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type zipTestEntry struct {
	Name string
	Body []byte
	Mode os.FileMode
}

func makeCodeGraphZip(t *testing.T, entries ...zipTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(entry.Mode)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
