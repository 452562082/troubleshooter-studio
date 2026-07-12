package main

import (
	"context"
	"net/http"
	"testing"
)

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

	appState := &App{workflowRoot: t.TempDir()}
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
}
