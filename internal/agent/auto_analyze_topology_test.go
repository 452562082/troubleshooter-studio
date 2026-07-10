package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestRunAutoAnalyzeCacheReusesTopologyAcrossFourTargetsAndInvalidatesChangedHead(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "mall-web")
	order := filepath.Join(root, "mall-order")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(web, "package.json"), `{"name":"mall-web","dependencies":{"axios":"1.0.0"}}`)
	writeAutoAnalyzeFixtureFile(t, filepath.Join(web, "src", "orders.ts"), `axios.get("http://mall-order/internal/orders")`)
	writeAutoAnalyzeFixtureFile(t, filepath.Join(order, "go.mod"), "module example.test/mall-order\n\ngo 1.22\n")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(order, "handler.go"), `
package main

func routes(r *Router) {
	r.GET("/internal/orders", handler)
}
`)
	initAutoAnalyzeGitRepo(t, web)
	initAutoAnalyzeGitRepo(t, order)

	cfg := &config.SystemConfig{
		System:       config.System{ID: "auto-analyze-topology"},
		Environments: []config.Environment{{ID: "dev", APIDomain: "api.mall.test", WebDomain: "mall.test"}},
		Repos: []config.Repo{
			{Name: "mall-web", Stack: "node", Role: config.RoleFrontend, ServiceNames: []string{"mall-web"}},
			{Name: "mall-order", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"mall-order"}},
		},
		Infrastructure: config.Infrastructure{ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}}},
	}
	repoPaths := map[string]string{"mall-web": web, "mall-order": order}
	var scanStarts atomic.Int32
	var cacheHits atomic.Int32
	log := func(message string) {
		if strings.Contains(message, "auto-analyze 开始扫") {
			scanStarts.Add(1)
		}
		if strings.Contains(message, "auto-analyze cache 命中") {
			cacheHits.Add(1)
		}
	}

	var first *analyzerpipe.Result
	for target := 0; target < 4; target++ {
		result, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths, OnLog: log})
		if err != nil {
			t.Fatalf("target %d RunAutoAnalyze: %v", target, err)
		}
		if len(result.Topology.Edges) != 1 {
			t.Fatalf("target %d topology=%#v", target, result.Topology)
		}
		if target == 0 {
			first = result
		} else if result != first {
			t.Fatalf("target %d did not reuse cached analysis", target)
		}
	}
	if got := scanStarts.Load(); got != 1 {
		t.Fatalf("four targets started %d scans, want 1", got)
	}
	if got := cacheHits.Load(); got != 3 {
		t.Fatalf("four targets produced %d cache hits, want 3", got)
	}

	commitAutoAnalyzeRepo(t, web, "head-change.txt", "new commit")
	changed, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths, OnLog: log})
	if err != nil {
		t.Fatalf("changed HEAD RunAutoAnalyze: %v", err)
	}
	if changed == first {
		t.Fatal("changed HEAD reused cached result")
	}
	if got := scanStarts.Load(); got != 2 {
		t.Fatalf("changed HEAD left scan starts at %d, want 2", got)
	}
}

func writeAutoAnalyzeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
