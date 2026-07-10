package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestRunAutoAnalyzeExplicitPartialPathsDoNotDiscoverSiblingRepositories(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	beta := filepath.Join(root, "beta")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(alpha, "go.mod"), "module example.test/alpha\n\ngo 1.22\n")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(beta, "go.mod"), "module example.test/beta\n\ngo 1.22\n")

	cfg := &config.SystemConfig{
		System: config.System{ID: "auto-analyze-explicit-partial"},
		Repos: []config.Repo{
			{Name: "alpha", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"alpha"}},
			{Name: "beta", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"beta"}},
		},
		Infrastructure: config.Infrastructure{ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}}},
	}

	result, err := RunAutoAnalyze(RunAutoAnalyzeOptions{
		Cfg:       cfg,
		RepoPaths: map[string]string{"alpha": alpha},
	})
	if err != nil {
		t.Fatalf("RunAutoAnalyze: %v", err)
	}
	if result == nil {
		t.Fatal("RunAutoAnalyze returned nil result")
	}
	assertAutoAnalyzeRepoStatus(t, result, "alpha", "analyzed", "")
	assertAutoAnalyzeRepoStatus(t, result, "beta", "skipped", "not-found")
	for _, status := range result.Topology.Repositories {
		if status.Repo == "beta" && status.State != "failed" {
			t.Fatalf("omitted beta topology status=%#v, want failed", status)
		}
	}
}

func TestRunAutoAnalyzeConcurrentSameKeyCallersShareOneScan(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	api := filepath.Join(root, "api")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(web, "package.json"), `{"name":"web"}`)
	writeAutoAnalyzeFixtureFile(t, filepath.Join(web, "client.ts"), `fetch("http://api/orders")`)
	writeAutoAnalyzeFixtureFile(t, filepath.Join(api, "go.mod"), "module example.test/api\n\ngo 1.22\n")
	for index := 0; index < 40; index++ {
		writeAutoAnalyzeFixtureFile(t, filepath.Join(api, "routes", fmt.Sprintf("route_%02d.go", index)), fmt.Sprintf(`
package routes

func route%d(r *Router) { r.GET("/orders/%d", handler) }
`, index, index))
	}
	initAutoAnalyzeGitRepo(t, web)
	initAutoAnalyzeGitRepo(t, api)

	cfg := &config.SystemConfig{
		System: config.System{ID: "auto-analyze-concurrent-singleflight"},
		Repos: []config.Repo{
			{Name: "web", Stack: "node", Role: config.RoleFrontend, ServiceNames: []string{"web"}},
			{Name: "api", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"api"}},
		},
		Infrastructure: config.Infrastructure{ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}}},
	}
	repoPaths := map[string]string{"web": web, "api": api}

	const callers = 12
	start := make(chan struct{})
	results := make([]*analyzerpipe.Result, callers)
	errs := make([]error, callers)
	var scanStarts atomic.Int32
	var wg sync.WaitGroup
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = RunAutoAnalyze(RunAutoAnalyzeOptions{
				Ctx:       context.Background(),
				Cfg:       cfg,
				RepoPaths: repoPaths,
				OnLog: func(message string) {
					if strings.Contains(message, "auto-analyze 开始扫") {
						scanStarts.Add(1)
					}
				},
			})
		}(index)
	}
	close(start)
	wg.Wait()

	if got := scanStarts.Load(); got != 1 {
		t.Fatalf("%d concurrent callers started %d scans, want 1", callers, got)
	}
	for index := range results {
		if errs[index] != nil {
			t.Fatalf("caller %d error: %v", index, errs[index])
		}
		if results[index] == nil {
			t.Fatalf("caller %d returned nil result", index)
		}
		if index > 0 && results[index] != results[0] {
			t.Fatalf("caller %d received a distinct result pointer", index)
		}
	}
}

func TestRunAutoAnalyzeCanceledWaiterDoesNotCancelSharedScan(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "api")
	writeAutoAnalyzeFixtureFile(t, filepath.Join(repo, "go.mod"), "module example.test/api\n\ngo 1.22\n")
	initAutoAnalyzeGitRepo(t, repo)
	cfg := &config.SystemConfig{
		System: config.System{ID: "auto-analyze-canceled-waiter"},
		Repos:  []config.Repo{{Name: "api", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"api"}}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
		},
	}
	repoPaths := map[string]string{"api": repo}

	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	ownerDone := make(chan struct{})
	var scanStarts atomic.Int32
	var ownerResult *analyzerpipe.Result
	var ownerErr error
	go func() {
		defer close(ownerDone)
		ownerResult, ownerErr = RunAutoAnalyze(RunAutoAnalyzeOptions{
			Cfg:       cfg,
			RepoPaths: repoPaths,
			OnLog: func(message string) {
				if strings.Contains(message, "auto-analyze 开始扫") {
					scanStarts.Add(1)
					close(scanStarted)
					<-releaseScan
				}
			},
		})
	}()
	<-scanStarted

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledResult, canceledErr := RunAutoAnalyze(RunAutoAnalyzeOptions{
		Ctx:       canceledCtx,
		Cfg:       cfg,
		RepoPaths: repoPaths,
		OnLog: func(message string) {
			if strings.Contains(message, "auto-analyze 开始扫") {
				scanStarts.Add(1)
			}
		},
	})
	if canceledErr != nil || canceledResult != nil {
		t.Fatalf("canceled waiter result=%#v err=%v, want nil/nil", canceledResult, canceledErr)
	}
	close(releaseScan)
	<-ownerDone

	if ownerErr != nil || ownerResult == nil {
		t.Fatalf("shared scan result=%#v err=%v", ownerResult, ownerErr)
	}
	if got := scanStarts.Load(); got != 1 {
		t.Fatalf("canceled waiter caused %d scans, want 1", got)
	}
}

func assertAutoAnalyzeRepoStatus(t *testing.T, result *analyzerpipe.Result, repo, status, repoErr string) {
	t.Helper()
	for _, summary := range result.PerRepo {
		if summary.Name == repo {
			if summary.Status != status || summary.Error != repoErr {
				t.Fatalf("repo %q summary=%#v, want status=%q error=%q", repo, summary, status, repoErr)
			}
			return
		}
	}
	t.Fatalf("missing repo %q summary in %#v", repo, result.PerRepo)
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
