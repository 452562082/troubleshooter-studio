package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
)

func TestReindexCodeGraph_DisabledRejected(t *testing.T) {
	yamlText := desktopCodeGraphYAML(t, false)
	previousEnsure := ensureCodeGraphForRetry
	previousPrepare := prepareCodeGraphForRetry
	previousInvalidate := invalidateCodeGraphForRetry
	ensureCodeGraphForRetry = func(func(string)) (string, error) {
		t.Fatal("disabled retry called installer")
		return "", nil
	}
	prepareCodeGraphForRetry = func(context.Context, agent.CodeGraphIndexOptions) agent.CodeGraphIndexReport {
		t.Fatal("disabled retry called index manager")
		return agent.CodeGraphIndexReport{}
	}
	invalidateCodeGraphForRetry = func(string) { t.Fatal("disabled retry invalidated cache") }
	t.Cleanup(func() {
		ensureCodeGraphForRetry = previousEnsure
		prepareCodeGraphForRetry = previousPrepare
		invalidateCodeGraphForRetry = previousInvalidate
	})

	result, err := (&App{}).ReindexCodeGraph(yamlText, nil)
	if err == nil || !strings.Contains(err.Error(), "CodeGraph is not enabled") {
		t.Fatalf("ReindexCodeGraph() result = %#v, error = %v", result, err)
	}
}

func TestReindexCodeGraph_ExpandsPathsInvalidatesCacheAndSerializesReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	yamlText := desktopCodeGraphYAML(t, true)

	previousEnsure := ensureCodeGraphForRetry
	previousPrepare := prepareCodeGraphForRetry
	previousInvalidate := invalidateCodeGraphForRetry
	previousEmit := emitCodeGraphRetryLog
	ensureCodeGraphForRetry = func(onLog func(string)) (string, error) {
		onLog("installer ready")
		return "/fake/codegraph", nil
	}
	var invalidated string
	invalidateCodeGraphForRetry = func(systemID string) { invalidated = systemID }
	var gotOpts agent.CodeGraphIndexOptions
	prepareCodeGraphForRetry = func(_ context.Context, opts agent.CodeGraphIndexOptions) agent.CodeGraphIndexReport {
		gotOpts = opts
		opts.OnProgress("repository ready")
		return agent.CodeGraphIndexReport{
			Ready: 1,
			Total: 1,
			Repos: []agent.CodeGraphRepoResult{{
				Name: "order-service", Path: opts.Repos[0].Path, Status: "ready",
				FileCount: 10, NodeCount: 20, EdgeCount: 30,
			}},
		}
	}
	var logs []string
	emitCodeGraphRetryLog = func(_ context.Context, line string) { logs = append(logs, line) }
	t.Cleanup(func() {
		ensureCodeGraphForRetry = previousEnsure
		prepareCodeGraphForRetry = previousPrepare
		invalidateCodeGraphForRetry = previousInvalidate
		emitCodeGraphRetryLog = previousEmit
	})

	app := &App{}
	app.setRuntimeContext(context.Background())
	report, err := app.ReindexCodeGraph(yamlText, map[string]string{
		"order-service": "~/src/order-service",
	})
	if err != nil {
		t.Fatalf("ReindexCodeGraph() error = %v", err)
	}
	if invalidated != "shop" {
		t.Fatalf("invalidated system = %q, want shop", invalidated)
	}
	wantPath := filepath.Join(home, "src", "order-service")
	if len(gotOpts.Repos) != 1 || gotOpts.Repos[0].Path != wantPath {
		t.Fatalf("repo targets = %#v, want path %q", gotOpts.Repos, wantPath)
	}
	if gotOpts.BinaryPath != "/fake/codegraph" || gotOpts.SystemID != "shop" {
		t.Fatalf("index options = %#v", gotOpts)
	}
	if gotOpts.InitTimeout != 120*time.Second || gotOpts.SyncTimeout != 30*time.Second || gotOpts.MaxConcurrency != 2 {
		t.Fatalf("index limits = init %v sync %v concurrency %d", gotOpts.InitTimeout, gotOpts.SyncTimeout, gotOpts.MaxConcurrency)
	}
	if strings.Join(logs, "\n") != "[codegraph-retry] installer ready\n[codegraph-retry] repository ready" {
		t.Fatalf("retry logs = %#v", logs)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["ready"] != float64(1) || fields["total"] != float64(1) {
		t.Fatalf("serialized report = %s", data)
	}
	repos := fields["repos"].([]any)
	repo := repos[0].(map[string]any)
	if repo["file_count"] != float64(10) || repo["node_count"] != float64(20) || repo["edge_count"] != float64(30) {
		t.Fatalf("serialized repo = %#v", repo)
	}
}

func TestReindexCodeGraph_InstallerErrorReturned(t *testing.T) {
	yamlText := desktopCodeGraphYAML(t, true)
	previousEnsure := ensureCodeGraphForRetry
	previousPrepare := prepareCodeGraphForRetry
	previousInvalidate := invalidateCodeGraphForRetry
	previousEmit := emitCodeGraphRetryLog
	ensureCodeGraphForRetry = func(func(string)) (string, error) {
		return "", errors.New("checksum mismatch")
	}
	prepareCodeGraphForRetry = func(context.Context, agent.CodeGraphIndexOptions) agent.CodeGraphIndexReport {
		t.Fatal("installer failure still called index manager")
		return agent.CodeGraphIndexReport{}
	}
	invalidateCodeGraphForRetry = func(string) {}
	emitCodeGraphRetryLog = func(context.Context, string) {}
	t.Cleanup(func() {
		ensureCodeGraphForRetry = previousEnsure
		prepareCodeGraphForRetry = previousPrepare
		invalidateCodeGraphForRetry = previousInvalidate
		emitCodeGraphRetryLog = previousEmit
	})

	report, err := (&App{}).ReindexCodeGraph(yamlText, nil)
	if report != nil || err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("ReindexCodeGraph() report = %#v, error = %v", report, err)
	}
}

func desktopCodeGraphYAML(t *testing.T, enabled bool) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "shop-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		data = []byte(strings.Replace(string(data), "code_intelligence:\n  enabled: false", "code_intelligence:\n  enabled: true", 1))
	}
	return string(data)
}
