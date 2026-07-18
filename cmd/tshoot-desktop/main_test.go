package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestResolveBundledBrowserRuntimeDirFromAppResources(t *testing.T) {
	app := t.TempDir()
	executable := filepath.Join(app, "TroubleshooterStudio.app", "Contents", "MacOS", "TroubleshooterStudio")
	runtimeRoot := filepath.Join(app, "TroubleshooterStudio.app", "Contents", "Resources", "browser-runtime", browserverify.BrowserRuntimeVersion)
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := desktopExecutablePath
	desktopExecutablePath = func() (string, error) { return executable, nil }
	t.Cleanup(func() { desktopExecutablePath = previous })
	if got := resolveBundledBrowserRuntimeDir(); got != runtimeRoot {
		t.Fatalf("runtime dir = %q, want %q", got, runtimeRoot)
	}
}

func TestResolveBundledBrowserRuntimeDirFallsBackWhenBundleIsMissing(t *testing.T) {
	previous := desktopExecutablePath
	desktopExecutablePath = func() (string, error) { return filepath.Join(t.TempDir(), "tshoot-desktop"), nil }
	t.Cleanup(func() { desktopExecutablePath = previous })
	if got := resolveBundledBrowserRuntimeDir(); got != "" {
		t.Fatalf("runtime dir = %q, want empty fallback", got)
	}
}

func TestNewDesktopOptionsRunsInBackgroundOnClose(t *testing.T) {
	appState := &App{workflowRoot: t.TempDir()}
	opts := newDesktopOptions(appState, http.NewServeMux())

	if opts == nil {
		t.Fatal("newDesktopOptions returned nil")
	}
	if !opts.HideWindowOnClose {
		t.Fatal("HideWindowOnClose = false, want true")
	}
	if opts.Title != "Troubleshooter Studio" {
		t.Fatalf("Title = %q, want Troubleshooter Studio", opts.Title)
	}
	if opts.Width != 1280 || opts.Height != 860 {
		t.Fatalf("size = %dx%d, want 1280x860", opts.Width, opts.Height)
	}
	if len(opts.Bind) != 1 || opts.Bind[0] != appState {
		t.Fatalf("Bind = %#v, want appState only", opts.Bind)
	}
	if opts.OnStartup == nil {
		t.Fatal("OnStartup is nil")
	}
}

func TestStartupStartsTrayAfterContextIsSet(t *testing.T) {
	var called bool
	var pollerCalled bool
	prev := startDesktopTray
	prevPoller := startDesktopBugPoller
	startDesktopTray = func(appState *App) {
		called = true
		if appState.ctx == nil {
			t.Fatal("tray started before Wails context was set")
		}
	}
	startDesktopBugPoller = func(ctx context.Context, appState *App) {
		pollerCalled = true
		if ctx == nil || appState.ctx == nil {
			t.Fatal("bug poller started before Wails context was set")
		}
	}
	t.Cleanup(func() {
		startDesktopTray = prev
		startDesktopBugPoller = prevPoller
	})

	prepared := make(chan struct{})
	appState := &App{
		workflowRoot: t.TempDir(),
		workflowEmit: func(string, any) {},
		workflowBrowserPrepare: func(context.Context, func(bughub.BrowserProgress)) error {
			close(prepared)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = appState.closeIncidentWorkflow() })
	appState.startup(ctx)

	if appState.ctx == nil {
		t.Fatal("startup did not store Wails context")
	}
	if !called {
		t.Fatal("startup did not start tray")
	}
	if !pollerCalled {
		t.Fatal("startup did not start bug poller")
	}
	select {
	case <-prepared:
	case <-time.After(time.Second):
		t.Fatal("startup did not prepare the browser runtime outside a Case")
	}
}
