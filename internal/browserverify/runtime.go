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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const browserRuntimeVersion = "1.61.1-r29"
const browserRuntimeProtocolProbeVersion = 1

// BrowserRuntimeVersion is the immutable Playwright runtime version bundled by
// desktop release artifacts. Packaging and runtime discovery must agree on it.
const BrowserRuntimeVersion = browserRuntimeVersion

const (
	browserRuntimeDependencyInstallTimeout = 5 * time.Minute
	browserRuntimeDownloadTimeout          = 90 * time.Minute
	browserRuntimeProbeTimeout             = time.Minute
)

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
	bundledRuntimeDir string
	browserCacheRoot  string
	runner            CommandRunner
	installMu         sync.Mutex
	statusMu          sync.RWMutex
	status            RuntimeStatus
	beforeInstallLock func()
	dependencyTimeout time.Duration
	downloadTimeout   time.Duration
	probeTimeout      time.Duration
}

// SetPlaywrightBrowserCache lets release tooling seed the isolated runtime
// from Playwright's verified local cache before running `playwright install`.
// The install command and real probe still run and remain authoritative.
func (m *RuntimeManager) SetPlaywrightBrowserCache(root string) {
	m.browserCacheRoot = strings.TrimSpace(root)
}

type execCommandRunner struct {
	attachOutputs commandOutputAttacher
}

func (runner execCommandRunner) Run(ctx context.Context, executable string, args, env []string, dir string, stdin io.Reader, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	command := exec.CommandContext(ctx, executable, args...)
	processController, err := configureWorkerProcess(ctx, command)
	if err != nil {
		return err
	}
	attachOutputs := runner.attachOutputs
	if attachOutputs == nil {
		attachOutputs = attachOwnedCommandOutputs
	}
	outputs, err := attachOutputs(command)
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
	afterStartErr := processController.afterStart(command)
	outputCloseErr := outputs.childStarted()
	if err := errors.Join(afterStartErr, outputCloseErr); err != nil {
		_ = processController.kill(command)
		waitErr := processController.wait(command)
		return errors.Join(err, waitErr, processController.finish())
	}
	stdoutDone, stderrDone := outputs.copyTo(stdout, stderr)
	waitErr := processController.wait(command)
	cleanupErr := processController.finish()
	copyErr := outputs.waitCopies(stdoutDone, stderrDone)
	return errors.Join(waitErr, cleanupErr, copyErr)
}

func NewRuntimeManager(managementRoot string, runner CommandRunner) *RuntimeManager {
	return NewRuntimeManagerWithBundle(managementRoot, "", runner)
}

// NewRuntimeManagerWithBundle configures an optional, release-provided runtime
// directory. Ensure imports a valid bundle into the per-user management root
// before considering any network installation.
func NewRuntimeManagerWithBundle(managementRoot, bundledRuntimeDir string, runner CommandRunner) *RuntimeManager {
	if runner == nil {
		runner = execCommandRunner{}
	}
	manager := &RuntimeManager{
		managementRoot:    managementRoot,
		bundledRuntimeDir: strings.TrimSpace(bundledRuntimeDir),
		runner:            runner,
		dependencyTimeout: browserRuntimeDependencyInstallTimeout,
		downloadTimeout:   browserRuntimeDownloadTimeout,
		probeTimeout:      browserRuntimeProbeTimeout,
		status: RuntimeStatus{
			State:     RuntimeBroken,
			Version:   browserRuntimeVersion,
			ErrorCode: "browser_runtime_missing",
			Message:   "browser runtime is not installed",
		},
	}
	paths := manager.pathsFor(manager.currentDir())
	if info, err := os.Lstat(paths.Root); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if validatePublishedRuntime(paths) == nil {
			manager.status = RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion}
		} else {
			manager.status = RuntimeStatus{State: RuntimeBroken, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_broken", Message: "browser runtime validation failed during Studio startup"}
		}
	}
	return manager
}

func (m *RuntimeManager) Ensure(ctx context.Context, emit func(bughub.BrowserProgress)) (RuntimePaths, error) {
	m.installMu.Lock()
	defer m.installMu.Unlock()
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
		m.setStatus(RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion})
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
			m.setStatus(RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_install_in_progress", Message: "another Studio process is installing the browser runtime"})
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
		m.setStatus(RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion})
		return paths, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime cannot be inspected after install lock", err)
	}
	if m.bundledRuntimeDir != "" {
		paths, err := m.importBundledRuntimeLocked(emit)
		if err != nil {
			return RuntimePaths{}, err
		}
		publishedByThisCall = true
		return paths, nil
	}

	m.setStatus(RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion})
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
	emitRuntimeProgress(emit, "browser_runtime_dependencies_installing", "Installing pinned Playwright dependencies")
	if err := m.runCommandWithTimeout(ctx, m.dependencyTimeout, "npm", []string{"install", "--ignore-scripts", "--no-audit", "--no-fund"}, nil, temporary, io.Discard, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "npm install failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}
	temporaryBrowsers := filepath.Join(temporary, "browsers")
	if err := os.MkdirAll(temporaryBrowsers, 0o700); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "create temporary browser directory", err)
	}
	if m.browserCacheRoot != "" {
		if err := seedPlaywrightBrowserCache(filepath.Join(temporary, "node_modules", "playwright-core", "browsers.json"), m.browserCacheRoot, temporaryBrowsers); err != nil {
			return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "seed Playwright browser cache", err)
		}
	}
	playwright := filepath.Join(temporary, "node_modules", ".bin", "playwright")
	stderr.Reset()
	emitRuntimeProgressStep(emit, "browser_runtime_downloading", "Downloading pinned Chromium browser", 0, 100)
	downloadProgress := newPlaywrightDownloadProgressWriter(emit)
	if err := m.runCommandWithTimeout(ctx, m.downloadTimeout, playwright, []string{"install", "chromium"}, []string{"PLAYWRIGHT_BROWSERS_PATH=" + temporaryBrowsers}, temporary, downloadProgress, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_install_failed", "Playwright Chromium install failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}

	probePNG := filepath.Join(temporary, "probe.png")
	var probeOutput bytes.Buffer
	stderr.Reset()
	emitRuntimeProgress(emit, "browser_runtime_probing", "Starting Chromium runtime probe")
	if err := m.runCommandWithTimeout(ctx, m.probeTimeout, "node", []string{filepath.Join(temporary, "browser_worker.mjs"), "--mode", "probe", "--output", probePNG}, []string{"PLAYWRIGHT_BROWSERS_PATH=" + temporaryBrowsers}, temporary, &probeOutput, &stderr); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_probe_failed", "browser runtime probe failed", errors.Join(err, boundedRuntimeStderr(&stderr)))
	}
	probe, err := validateRuntimeProbe(probeOutput.Bytes(), probePNG)
	if err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_probe_failed", "browser runtime probe output is invalid", err)
	}
	ready, err := json.Marshal(struct {
		Version         string `json:"version"`
		SHA256          string `json:"probe_sha256"`
		ProtocolVersion int    `json:"protocol_probe_version"`
	}{Version: browserRuntimeVersion, SHA256: probe.SHA256, ProtocolVersion: probe.ProtocolVersion})
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
	m.setStatus(RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion})
	emitRuntimeProgress(emit, "browser_runtime_ready", "Browser runtime is ready")
	return paths, nil
}

func (m *RuntimeManager) importBundledRuntimeLocked(emit func(bughub.BrowserProgress)) (RuntimePaths, error) {
	source := m.pathsFor(filepath.Clean(m.bundledRuntimeDir))
	if err := validatePublishedRuntime(source); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_bundle_invalid", "bundled browser runtime validation failed", err)
	}
	m.setStatus(RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion})
	emitRuntimeProgress(emit, "browser_runtime_importing", "Importing bundled Chromium runtime")
	temporary, err := os.MkdirTemp(m.runtimeRoot(), ".import-"+browserRuntimeVersion+"-")
	if err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "create temporary bundled runtime directory", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "secure temporary bundled runtime directory", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := copyRuntimeTree(source.Root, temporary); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "copy bundled browser runtime", err)
	}
	imported := m.pathsFor(temporary)
	if err := validatePublishedRuntime(imported); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "validate imported browser runtime", err)
	}
	if err := syncRuntimeTree(temporary); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "sync imported browser runtime", err)
	}
	if err := os.Rename(temporary, m.currentDir()); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "publish imported browser runtime", err)
	}
	if err := syncRuntimeDirectory(m.runtimeRoot()); err != nil {
		_ = os.RemoveAll(m.currentDir())
		_ = syncRuntimeDirectory(m.runtimeRoot())
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_import_failed", "sync published bundled runtime", err)
	}
	published = true
	paths := m.pathsFor(m.currentDir())
	m.setStatus(RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion})
	emitRuntimeProgress(emit, "browser_runtime_ready", "Browser runtime is ready")
	return paths, nil
}

func copyRuntimeTree(source, destination string) error {
	source = filepath.Clean(source)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				return fmt.Errorf("bundled runtime contains absolute symlink: %s", relative)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), link))
			inside, err := filepath.Rel(source, resolved)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("bundled runtime symlink escapes source: %s", relative)
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundled runtime contains unsupported file: %s", relative)
		}
		mode := fs.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Close(), input.Close())
	})
}

func seedPlaywrightBrowserCache(manifestPath, cacheRoot, destination string) error {
	var manifest struct {
		Browsers []struct {
			Name              string            `json:"name"`
			Revision          string            `json:"revision"`
			RevisionOverrides map[string]string `json:"revisionOverrides"`
		} `json:"browsers"`
	}
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return err
	}
	prefixes := map[string]string{
		"chromium":                "chromium",
		"chromium-headless-shell": "chromium_headless_shell",
		"ffmpeg":                  "ffmpeg",
	}
	for _, browser := range manifest.Browsers {
		prefix, wanted := prefixes[browser.Name]
		if !wanted {
			continue
		}
		revisions := map[string]struct{}{browser.Revision: {}}
		for _, revision := range browser.RevisionOverrides {
			revisions[revision] = struct{}{}
		}
		for revision := range revisions {
			if strings.TrimSpace(revision) == "" {
				continue
			}
			name := prefix + "-" + revision
			source := filepath.Join(cacheRoot, name)
			info, err := os.Lstat(source)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe Playwright cache entry: %s", name)
			}
			marker, err := os.Lstat(filepath.Join(source, "INSTALLATION_COMPLETE"))
			if err != nil || !marker.Mode().IsRegular() || marker.Mode()&os.ModeSymlink != 0 {
				continue
			}
			target := filepath.Join(destination, name)
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			if err := copyRuntimeTree(source, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *RuntimeManager) runCommandWithTimeout(ctx context.Context, timeout time.Duration, executable string, args, env []string, dir string, stdout, stderr io.Writer) error {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := m.runner.Run(commandContext, executable, args, env, dir, nil, stdout, stderr)
	if commandContextErr := commandContext.Err(); commandContextErr != nil {
		return errors.Join(commandContextErr, err)
	}
	return err
}

type playwrightDownloadProgressWriter struct {
	mu      sync.Mutex
	pending []byte
	last    int
	emit    func(bughub.BrowserProgress)
}

func newPlaywrightDownloadProgressWriter(emit func(bughub.BrowserProgress)) *playwrightDownloadProgressWriter {
	return &playwrightDownloadProgressWriter{last: 0, emit: emit}
}

func (w *playwrightDownloadProgressWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, content...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := string(w.pending[:newline])
		w.pending = w.pending[newline+1:]
		percentage, ok := parsePlaywrightDownloadPercentage(line)
		if !ok || percentage <= w.last {
			continue
		}
		w.last = percentage
		emitRuntimeProgressStep(w.emit, "browser_runtime_downloading", "Downloading pinned Chromium browser", percentage, 100)
	}
	return len(content), nil
}

func parsePlaywrightDownloadPercentage(line string) (int, bool) {
	if !strings.Contains(line, "% of ") || !strings.Contains(line, "|") {
		return 0, false
	}
	for _, field := range strings.Fields(line) {
		if !strings.HasSuffix(field, "%") {
			continue
		}
		percentage, err := strconv.Atoi(strings.TrimSuffix(field, "%"))
		if err != nil || percentage < 0 || percentage > 100 {
			return 0, false
		}
		return percentage, true
	}
	return 0, false
}

func (m *RuntimeManager) Repair(ctx context.Context, emit func(bughub.BrowserProgress)) (RuntimePaths, error) {
	m.installMu.Lock()
	defer m.installMu.Unlock()
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
			m.setStatus(RuntimeStatus{State: RuntimeInstalling, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_install_in_progress", Message: "another Studio process is installing or repairing the browser runtime"})
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
		m.setStatus(RuntimeStatus{State: RuntimeReady, Version: browserRuntimeVersion})
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
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

// RequireReady returns the already prepared runtime without installing or
// repairing anything. Validation and regression attempts use this boundary so
// infrastructure setup never becomes part of Case execution.
func (m *RuntimeManager) RequireReady() (RuntimePaths, error) {
	status := m.Status()
	if status.State != RuntimeReady {
		code := strings.TrimSpace(status.ErrorCode)
		if code == "" {
			code = "browser_runtime_missing"
		}
		return RuntimePaths{}, fmt.Errorf("%s: browser runtime is not ready", code)
	}
	paths := m.pathsFor(m.currentDir())
	if err := validatePublishedRuntime(paths); err != nil {
		return RuntimePaths{}, m.setBrokenLocked("browser_runtime_broken", "browser runtime validation failed before use", err)
	}
	return paths, nil
}

func (m *RuntimeManager) setStatus(status RuntimeStatus) {
	m.statusMu.Lock()
	m.status = status
	m.statusMu.Unlock()
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
	m.setStatus(RuntimeStatus{
		State:     RuntimeInstalling,
		Version:   browserRuntimeVersion,
		ErrorCode: "browser_runtime_legacy_install_lock",
		Message:   "a legacy browser runtime install lock is present; manual removal is required after confirming no older Studio is running",
	})
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
	m.setStatus(RuntimeStatus{State: RuntimeBroken, Version: browserRuntimeVersion, ErrorCode: code, Message: message})
	return fmt.Errorf("%s: %s: %w", code, message, err)
}

type runtimeProbeResult struct {
	Status          string       `json:"status"`
	SHA256          string       `json:"sha256"`
	ProtocolVersion int          `json:"protocol_version"`
	WorkerResult    workerResult `json:"worker_result"`
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
	if probe.ProtocolVersion != browserRuntimeProtocolProbeVersion {
		return runtimeProbeResult{}, errors.New("probe protocol version is incompatible")
	}
	if err := validateRuntimeProbeWorkerResult(probe.WorkerResult); err != nil {
		return runtimeProbeResult{}, fmt.Errorf("probe worker result is incompatible: %w", err)
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

func validateRuntimeProbeWorkerResult(result workerResult) error {
	if err := validateWorkerResultBounds(result); err != nil {
		return err
	}
	if result.Status != "completed" || result.ErrorCode != "" || result.FinalURL == "" || result.Title != "tshoot browser runtime probe" || len(result.Artifacts) != 0 {
		return errors.New("probe worker result shape is invalid")
	}
	documentFound := false
	searchFound := false
	for _, node := range result.AccessibilitySummary {
		switch {
		case node.Role == "document" && node.Visible && strings.Contains(node.Name, "中文页面") && len(node.Name) >= 1024:
			documentFound = true
		case (node.Role == "textbox" || node.Role == "searchbox") && node.Name == "请输入搜索关键字" && node.LocatorKind == "placeholder" && node.Visible && !node.Disabled:
			searchFound = true
		}
	}
	if !documentFound || !searchFound {
		return errors.New("probe worker result lacks multilingual document or search control semantics")
	}
	return nil
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
		Version         string `json:"version"`
		SHA256          string `json:"probe_sha256"`
		ProtocolVersion int    `json:"protocol_probe_version"`
	}
	encoded, err := os.ReadFile(filepath.Join(paths.Root, ".runtime-ready.json"))
	if err != nil || json.Unmarshal(encoded, &marker) != nil || marker.Version != browserRuntimeVersion || marker.ProtocolVersion != browserRuntimeProtocolProbeVersion || len(marker.SHA256) != sha256.Size*2 {
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

func emitRuntimeProgressStep(emit func(bughub.BrowserProgress), code, message string, current, total int) {
	if emit != nil {
		emit(bughub.BrowserProgress{Code: code, Message: message, Current: current, Total: total})
	}
}
