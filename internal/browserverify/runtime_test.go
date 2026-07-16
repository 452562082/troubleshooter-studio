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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	assertRuntimeInstallLockAvailable(t, manager)
	entries, err := os.ReadDir(manager.runtimeRoot())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".install-") && entry.Name() != filepath.Base(manager.lockPath()) && entry.Name() != filepath.Base(manager.legacyLockPath()) {
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
	assertRuntimeInstallLockAvailable(t, manager)
}

func TestRuntimeManagerLiveInstallLockFailsClosed(t *testing.T) {
	root := t.TempDir()
	owner := NewRuntimeManager(root, &recordingCommandRunner{})
	if err := os.MkdirAll(owner.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := owner.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release owner lock: %v", err)
		}
	}()

	contender := NewRuntimeManager(root, &recordingCommandRunner{})
	if _, err := contender.Ensure(context.Background(), nil); err == nil {
		t.Fatal("expected concurrent install error")
	}
	if status := contender.Status(); status.State != RuntimeInstalling || status.ErrorCode != "browser_runtime_install_in_progress" {
		t.Fatalf("status = %+v", status)
	}
}

func TestRuntimeManagerLegacyInstallLockRequiresManualRecovery(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("old-studio-random-owner-token\n")
	if err := os.WriteFile(manager.legacyLockPath(), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background(), nil); !errors.Is(err, errLegacyRuntimeInstallLock) {
		t.Fatalf("legacy lock error = %v, want errLegacyRuntimeInstallLock", err)
	}
	if status := manager.Status(); status.State != RuntimeInstalling || status.ErrorCode != "browser_runtime_legacy_install_lock" || !strings.Contains(status.Message, "manual") {
		t.Fatalf("legacy lock status = %+v", status)
	}
	got, err := os.ReadFile(manager.legacyLockPath())
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy lock changed: got=%q err=%v", got, err)
	}
}

func TestRuntimeManagerEmptyOEXCLInstallLockRequiresManualRecovery(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.legacyLockPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(manager.legacyLockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acquireInstallLock(); !errors.Is(err, errLegacyRuntimeInstallLock) {
		t.Fatalf("empty O_EXCL lock error = %v, want errLegacyRuntimeInstallLock", err)
	}
	after, err := os.Stat(manager.legacyLockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Size() != 0 {
		t.Fatalf("empty O_EXCL lock was replaced or changed: before=%+v after=%+v", before, after)
	}
}

func TestRuntimeManagerBlocksLiveHistoricalAdvisoryOwner(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishRuntimeInstallCompatibilityMarker(manager.legacyLockPath()); err != nil {
		t.Fatal(err)
	}
	historicalRelease, err := acquireRuntimeAdvisoryLock(manager.legacyLockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer historicalRelease()
	if _, err := manager.acquireInstallLock(); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("new manager error with live historical advisory owner = %v, want fs.ErrExist", err)
	}
}

func TestRuntimeInstallLockSerializesHistoricalV2AndCurrentContenders(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishRuntimeInstallCompatibilityMarker(manager.legacyLockPath()); err != nil {
		t.Fatal(err)
	}
	historicalRelease, err := acquireRuntimeAdvisoryLock(manager.legacyLockPath())
	if err != nil {
		t.Fatal(err)
	}
	v2Release, err := acquireRuntimeAdvisoryLock(manager.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acquireInstallLock(); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("current contender bypassed live v2 gate: %v", err)
	}
	if err := v2Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acquireInstallLock(); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("current contender bypassed historical advisory owner: %v", err)
	}
	if err := historicalRelease(); err != nil {
		t.Fatal(err)
	}
	currentRelease, err := manager.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	defer currentRelease()
	if release, err := acquireRuntimeAdvisoryLock(manager.lockPath()); !errors.Is(err, fs.ErrExist) {
		if err == nil {
			_ = release()
		}
		t.Fatalf("current owner did not retain v2 gate: %v", err)
	}
	if release, err := acquireRuntimeAdvisoryLock(manager.legacyLockPath()); !errors.Is(err, fs.ErrExist) {
		if err == nil {
			_ = release()
		}
		t.Fatalf("current owner did not retain historical advisory lock: %v", err)
	}
}

func TestRuntimeManagerMigratesAfterLegacyLockManualRemoval(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.legacyLockPath(), []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acquireInstallLock(); !errors.Is(err, errLegacyRuntimeInstallLock) {
		t.Fatalf("legacy lock error = %v", err)
	}
	if err := os.Remove(manager.legacyLockPath()); err != nil {
		t.Fatal(err)
	}
	release, err := manager.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(manager.legacyLockPath())
	if err != nil || string(marker) != runtimeInstallLockV2Marker {
		t.Fatalf("compatibility marker=%q err=%v", marker, err)
	}
	assertRuntimeInstallLockAvailable(t, manager)
}

func TestRuntimeManagerDoesNotOverwriteReplacedLegacyLock(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	ownerRelease, err := manager.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.legacyLockPath()); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement-legacy-owner\n")
	if err := os.WriteFile(manager.legacyLockPath(), replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	contender := NewRuntimeManager(manager.managementRoot, &recordingCommandRunner{})
	if _, err := contender.acquireInstallLock(); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("live advisory contender error=%v, want fs.ErrExist", err)
	}
	if err := ownerRelease(); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.acquireInstallLock(); !errors.Is(err, errLegacyRuntimeInstallLock) {
		t.Fatalf("replacement legacy error=%v", err)
	}
	got, err := os.ReadFile(manager.legacyLockPath())
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement legacy lock changed: got=%q err=%v", got, err)
	}
}

func TestRuntimeInstallLockAllowsOnlyOneOfThreeContenders(t *testing.T) {
	root := t.TempDir()
	manager := NewRuntimeManager(root, &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	type lockResult struct {
		err error
	}
	start := make(chan struct{})
	releaseWinner := make(chan struct{})
	results := make(chan lockResult, 3)
	var wait sync.WaitGroup
	for range 3 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			contender := NewRuntimeManager(root, &recordingCommandRunner{})
			release, err := contender.acquireInstallLock()
			results <- lockResult{err: err}
			if err == nil {
				<-releaseWinner
				_ = release()
			}
		}()
	}
	close(start)
	winners := 0
	busy := 0
	for range 3 {
		result := <-results
		switch {
		case result.err == nil:
			winners++
		case errors.Is(result.err, fs.ErrExist):
			busy++
		default:
			t.Fatalf("contender error=%v", result.err)
		}
	}
	if winners != 1 || busy != 2 {
		t.Fatalf("winners=%d busy=%d", winners, busy)
	}
	close(releaseWinner)
	wait.Wait()
	assertRuntimeInstallLockAvailable(t, manager)
}

func TestRuntimeFileLockRegistryUsesOpenedFileIdentity(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "lock")
	alias := filepath.Join(temporary, "lock-alias")
	if err := os.WriteFile(path, []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	registry := &runtimeFileLockRegistry{}
	releaseOriginal, acquired := registry.tryAcquire(originalInfo)
	if !acquired {
		t.Fatal("original file identity was not acquired")
	}
	if releaseAlias, acquired := registry.tryAcquire(aliasInfo); acquired {
		releaseAlias()
		t.Fatal("hardlink alias bypassed same-process file identity guard")
	}
	releaseOriginal()
	if releaseAlias, acquired := registry.tryAcquire(aliasInfo); !acquired {
		t.Fatal("alias identity remained locked after release")
	} else {
		releaseAlias()
	}
}

func TestRuntimeManagerRecoversExistingUnlockedInstallLockFile(t *testing.T) {
	manager := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("stale-owner-metadata\n")
	if err := os.WriteFile(manager.lockPath(), want, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Ensure(context.Background(), nil); err != nil {
		t.Fatalf("stale lock file blocked install: %v", err)
	}
	got, err := os.ReadFile(manager.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("advisory lock file changed: got %q want %q", got, want)
	}
}

func TestRuntimeInstallLockReleasedWhenOwnerProcessExits(t *testing.T) {
	root := t.TempDir()
	readyPath := filepath.Join(root, "lock-ready")
	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeInstallLockOwnerProcess$")
	command.Env = append(os.Environ(),
		"TSHOOT_RUNTIME_LOCK_HELPER=1",
		"TSHOOT_RUNTIME_LOCK_ROOT="+root,
		"TSHOOT_RUNTIME_LOCK_READY="+readyPath,
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})

	contender := NewRuntimeManager(root, &recordingCommandRunner{})
	unexpectedRelease, err := contender.acquireInstallLock()
	if err == nil {
		_ = unexpectedRelease()
	}
	if !errors.Is(err, fs.ErrExist) {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatalf("live owner lock error = %v, want fs.ErrExist; helper stderr=%q", err, stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	state, err := command.Process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if state.Success() {
		t.Fatal("crashed lock owner unexpectedly exited successfully")
	}

	assertRuntimeInstallLockAvailable(t, contender)
}

func TestRuntimeInstallLockOwnerProcess(t *testing.T) {
	if os.Getenv("TSHOOT_RUNTIME_LOCK_HELPER") != "1" {
		return
	}
	manager := NewRuntimeManager(os.Getenv("TSHOOT_RUNTIME_LOCK_ROOT"), &recordingCommandRunner{})
	if err := os.MkdirAll(manager.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := manager.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_LOCK_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
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
	assertRuntimeInstallLockAvailable(t, manager)
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

func assertRuntimeInstallLockAvailable(t *testing.T, manager *RuntimeManager) {
	t.Helper()
	release, err := manager.acquireInstallLock()
	if err != nil {
		t.Fatalf("install lock is not recoverable: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release recovered install lock: %v", err)
	}
}

func waitForRuntimeTestFile(t *testing.T, path string, timeout time.Duration, cleanup func()) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanup()
			t.Fatalf("inspect runtime test file: %v", err)
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("timed out waiting for %s", filepath.Base(path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
