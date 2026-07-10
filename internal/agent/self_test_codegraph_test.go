package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestProbeCodeGraphIndexes_CompleteIndexPasses(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	workspaceDir := makeCodeGraphSelfTestWorkspace(t, map[string]string{"orders": repoPath})
	cfg := codeGraphSelfTestConfig("orders")

	setCodeGraphRunnerForTest(t, func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "/managed/codegraph" {
			t.Fatalf("binary = %q, want registered absolute command", binary)
		}
		want := []string{"status", repoPath, "--json"}
		if fmt.Sprint(args) != fmt.Sprint(want) {
			t.Fatalf("args = %v, want %v", args, want)
		}
		return codeGraphStatusJSON(repoPath, true, 12, 84, 103, "complete"), nil
	})

	checks := collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
	assertSingleCodeGraphCheck(t, checks, "PASS", "files=12", "nodes=84", "edges=103")
}

func TestProbeCodeGraphIndexes_MissingIndexWarnsSeparatelyFromMCP(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	workspaceDir := makeCodeGraphSelfTestWorkspace(t, map[string]string{"orders": repoPath})
	cfg := codeGraphSelfTestConfig("orders")

	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return codeGraphStatusJSON(repoPath, false, 0, 0, 0, "missing"), nil
	})

	checks := collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
	assertSingleCodeGraphCheck(t, checks, "WARN", "未初始化", "重新索引")
	if checks[0].Name == "mcp probe shop-codegraph" {
		t.Fatalf("index health must be a separate row from MCP health: %#v", checks[0])
	}
}

func TestProbeCodeGraphIndexes_StaleExtractionWarnsRebuild(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	workspaceDir := makeCodeGraphSelfTestWorkspace(t, map[string]string{"orders": repoPath})
	cfg := codeGraphSelfTestConfig("orders")

	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"initialized":true,"version":"1.3.1","projectPath":%q,"fileCount":12,"nodeCount":84,"edgeCount":103,"index":{"builtWithVersion":"1.3.1","builtWithExtractionVersion":50,"currentExtractionVersion":51,"reindexRecommended":true,"state":"complete","pendingRefs":0}}`, repoPath)), nil
	})

	checks := collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
	assertSingleCodeGraphCheck(t, checks, "WARN", "50", "51", "重新索引")
}

func TestProbeCodeGraphIndexes_NoSupportedSourceSkipsZeroNodes(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("docs only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceDir := makeCodeGraphSelfTestWorkspace(t, map[string]string{"docs": repoPath})
	cfg := codeGraphSelfTestConfig("docs")

	calls := 0
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls++
		return codeGraphStatusJSON(repoPath, true, 1, 0, 0, "complete"), nil
	})

	checks := collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
	assertSingleCodeGraphCheck(t, checks, "SKIP", "无受支持源码")
	if calls != 0 {
		t.Fatalf("source-less repository executed %d status commands, want 0", calls)
	}
}

func TestProbeCodeGraphIndexes_OptionalFailuresWarn(t *testing.T) {
	tests := []struct {
		name       string
		paths      map[string]string
		runner     codeGraphCommandRunner
		wantDetail string
	}{
		{name: "missing local path", paths: map[string]string{}, wantDetail: "local_path"},
		{name: "status command failure", paths: map[string]string{"orders": makeCodeGraphSourceRepo(t, "failure.go")}, runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("broken status"), errors.New("exit 1")
		}, wantDetail: "status 查询失败"},
		{name: "non-complete state", paths: map[string]string{"orders": makeCodeGraphSourceRepo(t, "partial.go")}, runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			return codeGraphStatusJSON(args[1], true, 12, 84, 103, "building"), nil
		}, wantDetail: "state=building"},
		{name: "zero nodes", paths: map[string]string{"orders": makeCodeGraphSourceRepo(t, "empty.go")}, runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			return codeGraphStatusJSON(args[1], true, 12, 0, 0, "complete"), nil
		}, wantDetail: "nodes=0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceDir := makeCodeGraphSelfTestWorkspace(t, tt.paths)
			cfg := codeGraphSelfTestConfig("orders")
			if tt.runner != nil {
				setCodeGraphRunnerForTest(t, tt.runner)
			} else {
				setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
					t.Fatal("missing-path repository must not execute CodeGraph")
					return nil, nil
				})
			}
			checks := collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
			assertSingleCodeGraphCheck(t, checks, "WARN", tt.wantDetail)
		})
	}
}

func TestProbeCodeGraphIndexes_MaxConcurrencyTwoAndStableOrder(t *testing.T) {
	repoPaths := map[string]string{
		"first":  makeCodeGraphSourceRepo(t, "first.go"),
		"second": makeCodeGraphSourceRepo(t, "second.go"),
		"third":  makeCodeGraphSourceRepo(t, "third.go"),
	}
	workspaceDir := makeCodeGraphSelfTestWorkspace(t, repoPaths)
	cfg := codeGraphSelfTestConfig("first", "second", "third")

	var active, maximum atomic.Int32
	entered := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		current := active.Add(1)
		recordCodeGraphMaximum(&maximum, current)
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return codeGraphStatusJSON(args[1], true, 12, 84, 103, "complete"), nil
	})

	done := make(chan []SelfTestCheck, 1)
	go func() {
		done <- collectCodeGraphIndexChecks(context.Background(), cfg, workspaceDir, "/managed/codegraph")
	}()
	waitForCodeGraphFakeCall(t, entered)
	waitForCodeGraphFakeCall(t, entered)
	select {
	case <-entered:
		t.Fatal("third CodeGraph status started before a concurrency slot was released")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	release <- struct{}{}
	waitForCodeGraphFakeCall(t, entered)
	release <- struct{}{}

	select {
	case checks := <-done:
		if maximum.Load() != 2 {
			t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
		}
		wantNames := []string{"CodeGraph 索引 first", "CodeGraph 索引 second", "CodeGraph 索引 third"}
		for i, want := range wantNames {
			if checks[i].Name != want || checks[i].Status != "PASS" {
				t.Fatalf("checks[%d] = %#v, want %q PASS", i, checks[i], want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent CodeGraph index probes did not finish")
	}
}

func codeGraphSelfTestConfig(repoNames ...string) *config.SystemConfig {
	cfg := &config.SystemConfig{
		System:           config.System{ID: "shop"},
		CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: config.CodeIntelligenceProviderCodeGraph},
	}
	for _, name := range repoNames {
		cfg.Repos = append(cfg.Repos, config.Repo{Name: name, Analysis: config.RepoAnalysis{Enabled: true}})
	}
	return cfg
}

func makeCodeGraphSelfTestWorkspace(t *testing.T, repoPaths map[string]string) string {
	t.Helper()
	workspaceDir := t.TempDir()
	mapPath := filepath.Join(workspaceDir, "skills", "routing", "references", "repo-path-map.yaml")
	if err := os.MkdirAll(filepath.Dir(mapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString("repos:\n")
	for name, localPath := range repoPaths {
		fmt.Fprintf(&body, "  %s:\n    local_path: %q\n", name, localPath)
	}
	if err := os.WriteFile(mapPath, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspaceDir
}

func collectCodeGraphIndexChecks(ctx context.Context, cfg *config.SystemConfig, workspaceDir, binary string) []SelfTestCheck {
	var checks []SelfTestCheck
	probeCodeGraphIndexes(ctx, cfg, workspaceDir, binary, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})
	return checks
}

func assertSingleCodeGraphCheck(t *testing.T, checks []SelfTestCheck, status string, detailSubstrings ...string) {
	t.Helper()
	if len(checks) != 1 {
		t.Fatalf("checks = %#v, want exactly one", checks)
	}
	if checks[0].Status != status {
		t.Fatalf("status = %q, want %q: %#v", checks[0].Status, status, checks[0])
	}
	for _, substring := range detailSubstrings {
		if !strings.Contains(checks[0].Detail, substring) {
			t.Fatalf("detail %q does not contain %q", checks[0].Detail, substring)
		}
	}
}
