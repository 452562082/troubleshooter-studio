package browserverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const browserRuntimeVersion = "1.61.1"

const browserRuntimePackageJSON = "{\n  \"name\": \"tshoot-browser-runtime\",\n  \"private\": true,\n  \"version\": \"1.61.1\",\n  \"dependencies\": { \"playwright\": \"1.61.1\" }\n}\n"

//go:embed worker/browser_worker.mjs
var embeddedBrowserWorker []byte

//go:embed worker/sanitize.mjs
var embeddedBrowserSanitizer []byte

type RuntimeState string

const (
	RuntimeReady      RuntimeState = "ready"
	RuntimeInstalling RuntimeState = "installing"
	RuntimeBroken     RuntimeState = "broken"
)

type RuntimeStatus struct {
	State     RuntimeState `json:"state"`
	Version   string       `json:"version"`
	ErrorCode string       `json:"error_code,omitempty"`
	Message   string       `json:"message,omitempty"`
}

type RuntimePaths struct {
	Root         string
	NodeModules  string
	BrowsersPath string
	WorkerPath   string
	Version      string
}

type CommandRunner interface {
	Run(context.Context, string, []string, []string, string, io.Reader, io.Writer, io.Writer) error
}

type RuntimeManager struct {
	managementRoot    string
	runner            CommandRunner
	mu                sync.Mutex
	status            RuntimeStatus
	beforeInstallLock func()
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, executable string, args, env []string, dir string, stdin io.Reader, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	command := exec.CommandContext(ctx, executable, args...)
	processController, err := configureWorkerProcess(command)
	if err != nil {
		return err
	}
	outputs, err := attachOwnedCommandOutputs(command)
	if err != nil {
		return errors.Join(err, processController.finish())
	}
	defer outputs.closeAll()
	command.Dir = dir
	command.Env = mergeCommandEnvironment(os.Environ(), env)
	command.Stdin = stdin
	if err := command.Start(); err != nil {
		return errors.Join(err, processController.finish())
	}
	if err := outputs.childStarted(); err != nil {
		_ = processController.kill(command)
		_ = processController.wait(command)
		return errors.Join(err, processController.finish())
	}
	stdoutDone, stderrDone := outputs.copyTo(stdout, stderr)
	if err := processController.afterStart(command); err != nil {
		_ = processController.kill(command)
		waitErr := processController.wait(command)
		cleanupErr := processController.finish()
		copyErr := outputs.waitCopies(stdoutDone, stderrDone)
		return errors.Join(err, waitErr, cleanupErr, copyErr)
	}
	waitErr := processController.wait(command)
	cleanupErr := processController.finish()
	copyErr := outputs.waitCopies(stdoutDone, stderrDone)
	return errors.Join(waitErr, cleanupErr, copyErr)
}

func NewRuntimeManager(managementRoot string, runner CommandRunner) *RuntimeManager {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &RuntimeManager{
		managementRoot: managementRoot,
		runner:         runner,
		status: RuntimeStatus{
			State:     RuntimeBroken,
			Version:   browserRuntimeVersion,
			ErrorCode: "browser_runtime_missing",
			Message:   "browser runtime is not installed",
		},
	}
}

func (m *RuntimeManager) Ensure(ctx context.Context, emit func(bughub.BrowserProgress)) (RuntimePaths, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked(ctx, emit)
}

func (m *RuntimeManager) ensureLocked(ctx context.Context, emit func(bughub.BrowserProgress)) (paths RuntimePaths, returnedErr error) {
	paths = m.pathsFor(m.currentDir())
	if info, err := os.Lstat(paths.Root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime path is not a regular directory", errors.New("browser runtime path is unsafe"))
		}
		if err := validatePublishedRuntime(paths); err != nil {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime validation failed", err)
		}
		m.status = RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion}
		return paths, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime cannot be inspected", err)
	}

	if err := os.MkdirAll(m.runtimeRoot(), 0o700); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "create browser runtime root", err)
	}
	if err := os.Chmod(m.runtimeRoot(), 0o700); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "secure browser runtime root", err)
	}
	if m.beforeInstallLock != nil {
		m.beforeInstallLock()
	}
	releaseLock, err := m.acquireInstallLock()
	if err != nil {
		if errors.Is(err, errLegacyRuntimeInstallLock) {
			m.setLegacyInstallLockStatusLocked()
			return RuntimePaths{}, fmt.Errorf("browser runtime install blocked by a legacy lock: %w", err)
		}
		if errors.Is(err, fs.ErrExist) {
			m.status = RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_install_in_progress", Message: "another Studio process is installing the browser runtime"}
			return RuntimePaths{}, fmt.Errorf("browser runtime install is already in progress: %w", err)
		}
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "acquire browser runtime install lock", err)
	}
	publishedByThisCall := false
	defer func() {
		if err := releaseLock(); err != nil && returnedErr == nil {
			if publishedByThisCall {
				_ = os.RemoveAll(m.currentDir())
				_ = syncRuntimeDirectory(m.runtimeRoot())
			}
			returnedErr = m.setBrokenLocked("browser_runtime_install_failed", "release browser runtime install lock", err)
			paths = RuntimePaths{}
		}
	}()

	if info, err := os.Lstat(paths.Root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime path is not a regular directory", errors.New("browser runtime path is unsafe"))
		}
		if err := validatePublishedRuntime(paths); err != nil {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime validation failed after install lock", err)
		}
		m.status = RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion}
		return paths, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime cannot be inspected after install lock", err)
	}

	m.status = RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion}
	emitRuntimeProgress(emit, "browser_runtime_installing", "Installing pinned Playwright browser runtime")
	temporary, err := os.MkdirTemp(m.runtimeRoot(), ".install-"+browserRuntimeVersion+"-")
	if err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "create temporary browser runtime", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "secure temporary browser runtime", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()

	for name, content := range map[string][]byte{
		"package.json":       []byte(browserRuntimePackageJSON),
		"browser_worker.mjs": embeddedBrowserWorker,
		"sanitize.mjs":       embeddedBrowserSanitizer,
	} {
		if err := writeSyncedRuntimeFile(filepath.Join(temporary, name), content, 0o600); err != nil {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "write browser runtime files", err)
		}
	}

	var stderr bytes.Buffer
	if err := m.runner.Run(ctx, "npm", []string{"install", "--ignore-scripts", "--no-audit", "--no-fund"}, nil, temporary, nil, io.Discard, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "npm install failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}
	temporaryBrowsers := filepath.Join(temporary, "browsers")
	if err := os.MkdirAll(temporaryBrowsers, 0o700); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "create temporary browser directory", err)
	}
	playwright := filepath.Join(temporary, "node_modules", ".bin", "playwright")
	stderr.Reset()
	if err := m.runner.Run(ctx, playwright, []string{"install", "chromium"}, []string{"PLAYWRIGHT_BROWSERS_PATH=" + temporaryBrowsers}, temporary, nil, io.Discard, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "Playwright Chromium install failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}

	probePNG := filepath.Join(temporary, "probe.png")
	var probeOutput bytes.Buffer
	stderr.Reset()
	if err := m.runner.Run(ctx, "node", []string{filepath.Join(temporary, "browser_worker.mjs"), "--mode", "probe", "--output", probePNG}, []string{"PLAYWRIGHT_BROWSERS_PATH=" + temporaryBrowsers}, temporary, nil, &probeOutput, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_probe_failed", "browser runtime probe failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}
	probe, err := validateRuntimeProbe(probeOutput.Bytes(), probePNG)
	if err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_probe_failed", "browser runtime probe output is invalid", err)
	}
	ready, err := json.Marshal(struct {
		Version string `json:"version"`
		SHA256  string `json:"probe_sha256"`
	}{Version: browserRuntimeVersion, SHA256: probe.SHA256})
	if err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "encode browser runtime marker", err)
	}
	if err := writeSyncedRuntimeFile(filepath.Join(temporary, ".runtime-ready.json"), append(ready, '\n'), 0o600); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "write browser runtime marker", err)
	}
	if err := syncRuntimeTree(temporary); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "sync browser runtime", err)
	}
	if err := os.Rename(temporary, m.currentDir()); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "publish browser runtime", err)
	}
	if err := syncRuntimeDirectory(m.runtimeRoot()); err != nil {
		_ = os.RemoveAll(m.currentDir())
		_ = syncRuntimeDirectory(m.runtimeRoot())
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "sync published browser runtime", err)
	}
	published = true
	publishedByThisCall = true
	paths = m.pathsFor(m.currentDir())
	m.status = RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion}
	emitRuntimeProgress(emit, "browser_runtime_ready", "Browser runtime is ready")
	return paths, nil
}

func (m *RuntimeManager) Repair(ctx context.Context, emit func(bughub.BrowserProgress)) (RuntimePaths, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.runtimeRoot(), 0o700); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "create browser runtime root before repair", err)
	}
	releaseLock, err := m.acquireInstallLock()
	if err != nil {
		if errors.Is(err, errLegacyRuntimeInstallLock) {
			m.setLegacyInstallLockStatusLocked()
			return RuntimePaths{}, KnownFailedRecoveryEffect(fmt.Errorf("browser runtime repair blocked by a legacy lock: %w", err))
		}
		if errors.Is(err, fs.ErrExist) {
			m.status = RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_install_in_progress", Message: "another Studio process is installing or repairing the browser runtime"}
			return RuntimePaths{}, KnownFailedRecoveryEffect(fmt.Errorf("browser runtime repair is already in progress: %w", err))
		}
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "acquire browser runtime repair lock", err)
	}
	releaseForEnsure := func() error {
		if err := releaseLock(); err != nil {
			return m.setBrokenLocked("browser_runtime_repair_failed", "release browser runtime repair lock", err)
		}
		return nil
	}
	paths := m.pathsFor(m.currentDir())
	info, err := os.Lstat(paths.Root)
	if errors.Is(err, os.ErrNotExist) {
		if err := releaseForEnsure(); err != nil {
			return RuntimePaths{}, err
		}
		return m.ensureLocked(ctx, emit)
	}
	if err != nil {
		_ = releaseLock()
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "inspect browser runtime before repair", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		_ = releaseLock()
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "refuse to remove an unsafe runtime path", errors.New("runtime path is not a regular directory"))
	}
	if err := validatePublishedRuntime(paths); err == nil {
		if err := releaseForEnsure(); err != nil {
			return RuntimePaths{}, err
		}
		m.status = RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion}
		return paths, nil
	}
	if err := os.RemoveAll(paths.Root); err != nil {
		_ = releaseLock()
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "remove broken browser runtime", err)
	}
	if err := syncRuntimeDirectory(m.runtimeRoot()); err != nil {
		_ = releaseLock()
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_repair_failed", "sync repaired browser runtime root", err)
	}
	if err := releaseForEnsure(); err != nil {
		return RuntimePaths{}, err
	}
	return m.ensureLocked(ctx, emit)
}

func (m *RuntimeManager) Status() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *RuntimeManager) runtimeRoot() string {
	return filepath.Join(m.managementRoot, "browser-runtime")
}

func (m *RuntimeManager) currentDir() string {
	return filepath.Join(m.runtimeRoot(), browserRuntimeVersion)
}

func (m *RuntimeManager) lockPath() string {
	return filepath.Join(m.runtimeRoot(), ".install-"+browserRuntimeVersion+".advisory-v2.lock")
}

func (m *RuntimeManager) legacyLockPath() string {
	return filepath.Join(m.runtimeRoot(), ".install-"+browserRuntimeVersion+".lock")
}

func (m *RuntimeManager) pathsFor(root string) RuntimePaths {
	return RuntimePaths{
		Root:         root,
		NodeModules:  filepath.Join(root, "node_modules"),
		BrowsersPath: filepath.Join(root, "browsers"),
		WorkerPath:   filepath.Join(root, "browser_worker.mjs"),
		Version:      browserRuntimeVersion,
	}
}

func (m *RuntimeManager) acquireInstallLock() (func() error, error) {
	releaseV2, err := acquireRuntimeAdvisoryLock(m.lockPath())
	if err != nil {
		return nil, err
	}
	releaseHistorical, err := acquireRuntimeInstallCompatibilityLock(m.legacyLockPath())
	if err != nil {
		return nil, errors.Join(err, releaseV2())
	}
	return func() error {
		return errors.Join(releaseHistorical(), releaseV2())
	}, nil
}

func (m *RuntimeManager) setLegacyInstallLockStatusLocked() {
	m.status = RuntimeStatus{
		State:     RuntimeInstalling,
		Version:   browserRuntimeVersion,
		ErrorCode: "browser_runtime_legacy_install_lock",
		Message:   "a legacy browser runtime install lock is present; manual removal is required after confirming no older Studio is running",
	}
}

func mergeCommandEnvironment(base, overrides []string) []string {
	overridden := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		if name, _, found := strings.Cut(entry, "="); found {
			overridden[commandEnvironmentKey(name)] = struct{}{}
		}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replace := overridden[commandEnvironmentKey(name)]; replace {
				continue
			}
		}
		merged = append(merged, entry)
	}
	return append(merged, overrides...)
}

func commandEnvironmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func (m *RuntimeManager) setBrokenLocked(code, message string, err error) error {
	m.status = RuntimeStatus{State: RuntimeBroken, Version: browserRuntimeVersion, ErrorCode: code, Message: message}
	return fmt.Errorf("%s: %w", message, err)
}

type runtimeProbeResult struct {
	Status string `json:"status"`
	SHA256 string `json:"sha256"`
}

func validateRuntimeProbe(encoded []byte, screenshotPath string) (runtimeProbeResult, error) {
	var probe runtimeProbeResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&probe); err != nil {
		return runtimeProbeResult{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return runtimeProbeResult{}, err
	}
	if probe.Status != "ready" || len(probe.SHA256) != sha256.Size*2 {
		return runtimeProbeResult{}, errors.New("probe did not report ready with a SHA256")
	}
	content, err := os.ReadFile(screenshotPath)
	if err != nil {
		return runtimeProbeResult{}, fmt.Errorf("read probe screenshot: %w", err)
	}
	if len(content) <= 8 || !bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")) {
		return runtimeProbeResult{}, errors.New("probe screenshot is empty or not PNG")
	}
	digest := sha256.Sum256(content)
	if !strings.EqualFold(probe.SHA256, hex.EncodeToString(digest[:])) {
		return runtimeProbeResult{}, errors.New("probe screenshot SHA256 mismatch")
	}
	return probe, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validatePublishedRuntime(paths RuntimePaths) error {
	manifest, err := os.ReadFile(filepath.Join(paths.Root, "package.json"))
	if err != nil || string(manifest) != browserRuntimePackageJSON {
		return errors.New("pinned package manifest is missing or changed")
	}
	for _, path := range []string{paths.WorkerPath, filepath.Join(paths.Root, "sanitize.mjs"), filepath.Join(paths.Root, ".runtime-ready.json"), filepath.Join(paths.Root, "probe.png")} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required runtime file is unsafe or missing: %s", filepath.Base(path))
		}
	}
	worker, err := os.ReadFile(paths.WorkerPath)
	if err != nil || !bytes.Equal(worker, embeddedBrowserWorker) {
		return errors.New("embedded browser worker is missing or changed")
	}
	sanitizer, err := os.ReadFile(filepath.Join(paths.Root, "sanitize.mjs"))
	if err != nil || !bytes.Equal(sanitizer, embeddedBrowserSanitizer) {
		return errors.New("embedded browser sanitizer is missing or changed")
	}
	for _, path := range []string{paths.NodeModules, filepath.Join(paths.NodeModules, "playwright"), paths.BrowsersPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required runtime directory is unsafe or missing: %s", filepath.Base(path))
		}
	}
	var marker struct {
		Version string `json:"version"`
		SHA256  string `json:"probe_sha256"`
	}
	encoded, err := os.ReadFile(filepath.Join(paths.Root, ".runtime-ready.json"))
	if err != nil || json.Unmarshal(encoded, &marker) != nil || marker.Version != browserRuntimeVersion || len(marker.SHA256) != sha256.Size*2 {
		return errors.New("runtime readiness marker is invalid")
	}
	probe, err := os.ReadFile(filepath.Join(paths.Root, "probe.png"))
	if err != nil || len(probe) <= 8 || !bytes.HasPrefix(probe, []byte("\x89PNG\r\n\x1a\n")) {
		return errors.New("runtime probe screenshot is missing or invalid")
	}
	digest := sha256.Sum256(probe)
	if !strings.EqualFold(marker.SHA256, hex.EncodeToString(digest[:])) {
		return errors.New("runtime probe screenshot SHA256 changed")
	}
	return nil
}

func writeSyncedRuntimeFile(path string, content []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func syncRuntimeTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = errors.Join(file.Sync(), file.Close())
		return err
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncRuntimeDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

func syncRuntimeDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if runtime.GOOS == "windows" {
		return closeErr
	}
	return errors.Join(syncErr, closeErr)
}

func boundedRuntimeStderr(stderr *bytes.Buffer) error {
	if stderr == nil || stderr.Len() == 0 {
		return nil
	}
	message := stderr.String()
	if len(message) > 4096 {
		message = message[:4096]
	}
	return errors.New(strings.TrimSpace(message))
}

func emitRuntimeProgress(emit func(bughub.BrowserProgress), code, message string) {
	if emit != nil {
		emit(bughub.BrowserProgress{Code: code, Message: message})
	}
}
