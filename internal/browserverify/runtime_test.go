package browserverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type commandRecord struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
}

type recordingCommandRunner struct {
	mu              sync.Mutex
	Records         []commandRecord
	FailContaining  string
	BlockContaining string
	ProbeSHA        string
}

type runtimeEnsureResult struct {
	paths RuntimePaths
	err   error
}

func (r *recordingCommandRunner) Run(ctx context.Context, executable string, args, env []string, dir string, _ io.Reader, stdout, _ io.Writer) error {
	record := commandRecord{Executable: executable, Args: append([]string(nil), args...), Env: append([]string(nil), env...), Dir: dir}
	r.mu.Lock()
	r.Records = append(r.Records, record)
	r.mu.Unlock()
	summary := executable + " " + strings.Join(args, " ")
	if r.BlockContaining != "" && strings.Contains(summary, r.BlockContaining) {
		<-ctx.Done()
		return ctx.Err()
	}
	if r.FailContaining != "" && strings.Contains(summary, r.FailContaining) {
		return fmt.Errorf("forced command failure: %s", r.FailContaining)
	}
	if filepath.Base(executable) == "npm" {
		if err := os.MkdirAll(filepath.Join(dir, "node_modules", "playwright"), 0o700); err != nil {
			return err
		}
	}
	if strings.Contains(summary, "install chromium") {
		for _, entry := range env {
			if strings.HasPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH=") {
				if err := os.MkdirAll(strings.TrimPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH="), 0o700); err != nil {
					return err
				}
			}
		}
	}
	if containsArg(args, "--mode") && containsArg(args, "probe") {
		output := argValue(args, "--output")
		content := []byte("\x89PNG\r\n\x1a\nprobe")
		if err := os.WriteFile(output, content, 0o600); err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		probeSHA := hex.EncodeToString(digest[:])
		if r.ProbeSHA != "" {
			probeSHA = r.ProbeSHA
		}
		return json.NewEncoder(stdout).Encode(map[string]string{
			"status": "ready",
			"sha256": probeSHA,
		})
	}
	return nil
}

func (r *recordingCommandRunner) CommandSummaries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, 0, len(r.Records))
	for _, record := range r.Records {
		result = append(result, record.Executable+" "+strings.Join(record.Args, " "))
	}
	return result
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func argValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func TestRuntimeManagerInstallsPinnedVersionAndRunsRealProbeCommand(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := NewRuntimeManager(t.TempDir(), runner)
	paths, err := manager.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Version != "1.61.1" {
		t.Fatalf("version = %q", paths.Version)
	}
	got := strings.Join(runner.CommandSummaries(), "\n")
	for _, want := range []string{"npm install", "playwright install chromium", "browser_worker.mjs --mode probe"} {
		if !strings.Contains(got, want) {
			t.Fatalf("commands %q do not contain %q", got, want)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(paths.Root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantManifest := "{\n  \"name\": \"tshoot-browser-runtime\",\n  \"private\": true,\n  \"version\": \"1.61.1\",\n  \"dependencies\": { \"playwright\": \"1.61.1\" }\n}\n"
	if string(manifest) != wantManifest {
		t.Fatalf("package.json = %q", manifest)
	}
	if status := manager.Status(); status.State != RuntimeReady || status.Version != "1.61.1" {
		t.Fatalf("status = %+v", status)
	}
}

func TestRuntimeManagerInstallsBrowsersInsideTemporaryVersionBeforePublish(t *testing.T) {
	runner := &recordingCommandRunner{}
	root := t.TempDir()
	manager := NewRuntimeManager(root, runner)
	paths, err := manager.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var install commandRecord
	for _, record := range runner.Records {
		if strings.Contains(strings.Join(record.Args, " "), "install chromium") {
			install = record
			break
		}
	}
	var browsersPath string
	for _, entry := range install.Env {
		if strings.HasPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH=") {
			browsersPath = strings.TrimPrefix(entry, "PLAYWRIGHT_BROWSERS_PATH=")
		}
	}
	if browsersPath == "" || !strings.Contains(browsersPath, string(filepath.Separator)+".install-1.61.1-") {
		t.Fatalf("install env did not use temporary browser path: %+v", install.Env)
	}
	if browsersPath == paths.BrowsersPath {
		t.Fatalf("install pre-created final browser path %q", browsersPath)
	}
	if paths.BrowsersPath != filepath.Join(paths.Root, "browsers") {
		t.Fatalf("published browsers path = %q", paths.BrowsersPath)
	}
}

func TestRuntimeManagerDoesNotPublishFailedInstall(t *testing.T) {
	runner := &recordingCommandRunner{FailContaining: "playwright install chromium"}
	manager := NewRuntimeManager(t.TempDir(), runner)
	if _, err := manager.Ensure(context.Background(), nil); err == nil {
		t.Fatal("expected install failure")
	}
	if status := manager.Status(); status.State != RuntimeBroken || status.ErrorCode != "browser_runtime_install_failed" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(manager.currentDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published failed runtime: %v", err)
	}
	if _, err := os.Stat(manager.lockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install lock remains: %v", err)
	}
	entries, err := os.ReadDir(manager.runtimeRoot())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".install-") {
			t.Fatalf("temporary install remains: %s", entry.Name())
		}
	}
}

func TestRuntimeManagerCancellationCleansLockAndTemporaryInstall(t *testing.T) {
	runner := &recordingCommandRunner{BlockContaining: "npm install"}
	manager := NewRuntimeManager(t.TempDir(), runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Ensure(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(manager.currentDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published cancelled runtime: %v", err)
	}
	if _, err := os.Stat(manager.lockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install lock remains: %v", err)
	}
}

func TestRuntimeManagerExistingInstallLockFailsClosedWithoutDeletingOwnerLock(t *testing.T) {
	root := t.TempDir()
	manager := NewRuntimeManager(root, &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("other-process-lock")
	if err := os.WriteFile(manager.lockPath(), want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background(), nil); err == nil {
		t.Fatal("expected concurrent install error")
	}
	got, err := os.ReadFile(manager.lockPath())
	if err != nil {
		t.Fatalf("other process lock was removed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("other process lock changed to %q", got)
	}
	if status := manager.Status(); status.State != RuntimeInstalling || status.ErrorCode != "browser_runtime_install_in_progress" {
		t.Fatalf("status = %+v", status)
	}
}

func TestRuntimeManagerRejectsProbeSHAMismatchWithoutPublishing(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{ProbeSHA: strings.Repeat("0", 64)})
	if _, err := manager.Ensure(context.Background(), nil); err == nil {
		t.Fatal("expected probe mismatch")
	}
	if status := manager.Status(); status.State != RuntimeBroken || status.ErrorCode != "browser_runtime_probe_failed" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(manager.currentDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid probe runtime was published: %v", err)
	}
	if _, err := os.Stat(manager.lockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install lock remains: %v", err)
	}
}

func TestRuntimeManagerReusesReadyRuntimeWithoutCommands(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := NewRuntimeManager(t.TempDir(), runner)
	first, err := manager.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	commandCount := len(runner.CommandSummaries())
	second, err := manager.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(runner.CommandSummaries()) != commandCount {
		t.Fatalf("first=%+v second=%+v commands=%v", first, second, runner.CommandSummaries())
	}
}

func TestRuntimeManagerRevalidatesPublishedRuntimeAfterCrossProcessLockRace(t *testing.T) {
	root := t.TempDir()
	winnerRunner := &recordingCommandRunner{}
	loserRunner := &recordingCommandRunner{}
	winner := NewRuntimeManager(root, winnerRunner)
	loser := NewRuntimeManager(root, loserRunner)
	loserReachedLock := make(chan struct{})
	releaseLoser := make(chan struct{})
	loser.beforeInstallLock = func() {
		close(loserReachedLock)
		<-releaseLoser
	}
	loserResult := make(chan runtimeEnsureResult, 1)
	go func() {
		paths, err := loser.Ensure(context.Background(), nil)
		loserResult <- runtimeEnsureResult{paths: paths, err: err}
	}()
	<-loserReachedLock
	winnerPaths, err := winner.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	close(releaseLoser)
	result := <-loserResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.paths != winnerPaths {
		t.Fatalf("loser paths = %+v, winner paths = %+v", result.paths, winnerPaths)
	}
	if commands := loserRunner.CommandSummaries(); len(commands) != 0 {
		t.Fatalf("loser repeated installation commands: %v", commands)
	}
	if status := loser.Status(); status.State != RuntimeReady || status.ErrorCode != "" {
		t.Fatalf("loser status = %+v", status)
	}
}

func TestRuntimeManagerRepairReplacesOnlyVerifiedBrokenVersion(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := NewRuntimeManager(t.TempDir(), runner)
	paths, err := manager.Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Root, "package.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background(), nil); err == nil {
		t.Fatal("expected corrupted runtime to fail validation")
	}
	repaired, err := manager.Repair(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != paths {
		t.Fatalf("repaired paths = %+v, want %+v", repaired, paths)
	}
	if status := manager.Status(); status.State != RuntimeReady {
		t.Fatalf("status = %+v", status)
	}
	if len(runner.CommandSummaries()) != 6 {
		t.Fatalf("commands = %v, want two installs", runner.CommandSummaries())
	}
}

func TestRuntimeManagerRepairRefusesSymlinkVersion(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	proof := filepath.Join(victim, "proof.txt")
	if err := os.WriteFile(proof, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, manager.currentDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Repair(context.Background(), nil); err == nil {
		t.Fatal("expected symlink repair refusal")
	}
	if content, err := os.ReadFile(proof); err != nil || string(content) != "keep" {
		t.Fatalf("repair touched symlink target: content=%q err=%v", content, err)
	}
}

func TestMergeCommandEnvironmentReplacesInheritedBrowserPath(t *testing.T) {
	merged := mergeCommandEnvironment(
		[]string{"PATH=/bin", "PLAYWRIGHT_BROWSERS_PATH=/inherited", "HOME=/home/test"},
		[]string{"PLAYWRIGHT_BROWSERS_PATH=/temporary/browsers"},
	)
	joined := strings.Join(merged, "\n")
	if strings.Contains(joined, "/inherited") || strings.Count(joined, "PLAYWRIGHT_BROWSERS_PATH=") != 1 {
		t.Fatalf("merged environment = %v", merged)
	}
	if !strings.Contains(joined, "PLAYWRIGHT_BROWSERS_PATH=/temporary/browsers") {
		t.Fatalf("temporary browser path is missing: %v", merged)
	}
}
