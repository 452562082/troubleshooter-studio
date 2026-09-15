package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestPrepareCodeGraphIndexes_InitializesMissingIndex(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	systemID := "codegraph-index-init"
	InvalidateCodeGraphIndexCache(systemID)

	var calls [][]string
	var statusCalls int
	setCodeGraphRunnerForTest(t, func(_ context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{binary}, args...))
		switch args[0] {
		case "status":
			statusCalls++
			if statusCalls == 1 {
				return codeGraphStatusJSON(repoPath, false, 0, 0, 0, "missing"), nil
			}
			return codeGraphStatusJSON(repoPath, true, 12, 84, 103, "complete"), nil
		case "init":
			return nil, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	})

	var progressMu sync.Mutex
	var progress []string
	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{{Name: "orders", Path: repoPath, Head: "abc123"}},
		OnProgress: func(line string) {
			progressMu.Lock()
			defer progressMu.Unlock()
			progress = append(progress, line)
		},
	})

	wantCalls := [][]string{
		{"/managed/codegraph", "status", repoPath, "--json"},
		{"/managed/codegraph", "init", repoPath},
		{"/managed/codegraph", "status", repoPath, "--json"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("commands = %#v, want %#v", calls, wantCalls)
	}
	if report.Ready != 1 || report.Total != 1 || len(report.Repos) != 1 {
		t.Fatalf("report summary = %#v, want one ready repository", report)
	}
	got := report.Repos[0]
	if got.Action != "initialized" || got.Status != "ready" || got.FileCount != 12 || got.NodeCount != 84 || got.EdgeCount != 103 || got.IndexState != "complete" {
		t.Errorf("repository result = %#v", got)
	}
	if len(progress) != 1 || !strings.HasPrefix(progress[0], "[codegraph] orders: initialized (12 files, 84 nodes, ") || !strings.HasSuffix(progress[0], "s)") {
		t.Errorf("progress = %#v", progress)
	}
}

func TestPrepareCodeGraphIndexes_SyncsExistingIndex(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "app.ts")
	systemID := "codegraph-index-sync"
	InvalidateCodeGraphIndexCache(systemID)

	var calls [][]string
	var statusCalls int
	setCodeGraphRunnerForTest(t, func(_ context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{binary}, args...))
		switch args[0] {
		case "status":
			statusCalls++
			if statusCalls == 1 {
				return codeGraphStatusJSON(repoPath, true, 12, 84, 103, "complete"), nil
			}
			return codeGraphStatusJSON(repoPath, true, 13, 90, 111, "complete"), nil
		case "sync":
			return nil, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	})

	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{{Name: "users", Path: repoPath}},
	})

	wantCalls := [][]string{
		{"/managed/codegraph", "status", repoPath, "--json"},
		{"/managed/codegraph", "sync", repoPath},
		{"/managed/codegraph", "status", repoPath, "--json"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("commands = %#v, want %#v", calls, wantCalls)
	}
	got := report.Repos[0]
	if report.Ready != 1 || got.Action != "synced" || got.Status != "ready" || got.FileCount != 13 || got.NodeCount != 90 || got.EdgeCount != 111 {
		t.Fatalf("report = %#v", report)
	}
}

func TestPrepareCodeGraphIndexes_SkipsMissingPathAndNoSource(t *testing.T) {
	noSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(noSource, "README.md"), []byte("docs only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(noSource, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noSource, ".git", "ignored.go"), []byte("package ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	systemID := "codegraph-index-skips"
	InvalidateCodeGraphIndexCache(systemID)

	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		t.Fatalf("runner called for skipped repository: %v", args)
		return nil, nil
	})

	var skipProgressMu sync.Mutex
	var progress []string
	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos: []CodeGraphRepoTarget{
			{Name: "missing", Path: ""},
			{Name: "docs", Path: noSource},
		},
		OnProgress: func(line string) {
			skipProgressMu.Lock()
			defer skipProgressMu.Unlock()
			progress = append(progress, line)
		},
	})

	if report.Ready != 0 || report.Total != 2 || len(report.Repos) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if got := report.Repos[0]; got.Action != "skipped" || got.Status != "skipped" || !strings.Contains(got.Detail, "path") {
		t.Errorf("missing path result = %#v", got)
	}
	if got := report.Repos[1]; got.Action != "skipped" || got.Status != "skipped" || got.Detail != "no supported source files" {
		t.Errorf("no-source result = %#v", got)
	}
	wantProgress := []string{
		"[codegraph] missing: skipped (repository path missing)",
		"[codegraph] docs: skipped (no supported source files)",
	}
	sort.Strings(progress)
	sort.Strings(wantProgress)
	if !reflect.DeepEqual(progress, wantProgress) {
		t.Errorf("progress = %#v, want %#v", progress, wantProgress)
	}
}

func TestPrepareCodeGraphIndexes_TimeoutAndPartialFailureDoNotStopPeers(t *testing.T) {
	slowPath := makeCodeGraphSourceRepo(t, "slow.go")
	peerPath := makeCodeGraphSourceRepo(t, "peer.go")
	systemID := "codegraph-index-timeout"
	InvalidateCodeGraphIndexCache(systemID)

	var mu sync.Mutex
	statusCalls := map[string]int{}
	setCodeGraphRunnerForTest(t, func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		path := args[1]
		switch args[0] {
		case "status":
			mu.Lock()
			statusCalls[path]++
			call := statusCalls[path]
			mu.Unlock()
			if call == 1 {
				return codeGraphStatusJSON(path, true, 12, 84, 103, "complete"), nil
			}
			return codeGraphStatusJSON(path, true, 14, 99, 120, "complete"), nil
		case "sync":
			if path == slowPath {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	})

	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath:  "/managed/codegraph",
		SystemID:    systemID,
		Repos:       []CodeGraphRepoTarget{{Name: "legacy", Path: slowPath}, {Name: "peer", Path: peerPath}},
		SyncTimeout: 25 * time.Millisecond,
	})

	if report.Total != 2 || report.Ready != 1 {
		t.Fatalf("report summary = %#v", report)
	}
	if got := report.Repos[0]; got.Name != "legacy" || got.Status != "warn" || got.Action != "failed" || !strings.Contains(got.Detail, "sync timeout") || !strings.Contains(got.Detail, "fallback enabled") {
		t.Errorf("timed-out result = %#v", got)
	} else if got.DurationMS < 20 {
		t.Errorf("timed-out duration_ms = %d, want evidence of the real context timeout", got.DurationMS)
	}
	if got := report.Repos[1]; got.Name != "peer" || got.Status != "ready" || got.Action != "synced" || got.FileCount != 14 {
		t.Errorf("peer result = %#v", got)
	}
}

func TestPrepareCodeGraphIndexes_MaxConcurrencyTwo(t *testing.T) {
	systemID := "codegraph-index-concurrency"
	InvalidateCodeGraphIndexCache(systemID)
	targets := make([]CodeGraphRepoTarget, 4)
	for i := range targets {
		targets[i] = CodeGraphRepoTarget{Name: fmt.Sprintf("repo-%d", i), Path: makeCodeGraphSourceRepo(t, "main.go")}
	}

	var active atomic.Int32
	var maximum atomic.Int32
	var mu sync.Mutex
	statusCalls := map[string]int{}
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release

		path := args[1]
		switch args[0] {
		case "status":
			mu.Lock()
			statusCalls[path]++
			call := statusCalls[path]
			mu.Unlock()
			return codeGraphStatusJSON(path, call > 1, 1, 1, 1, map[bool]string{true: "complete", false: "missing"}[call > 1]), nil
		case "init":
			return nil, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	})

	done := make(chan CodeGraphIndexReport, 1)
	go func() {
		done <- PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
			BinaryPath: "/managed/codegraph",
			SystemID:   systemID,
			Repos:      targets,
		})
	}()

	waitForCodeGraphFakeCall(t, entered)
	waitForCodeGraphFakeCall(t, entered)
	select {
	case <-entered:
		t.Fatal("third command entered while default two-command limit was saturated")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)

	select {
	case report := <-done:
		if report.Ready != len(targets) {
			t.Fatalf("ready = %d, want %d; report = %#v", report.Ready, len(targets), report)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PrepareCodeGraphIndexes did not finish")
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum fake-runner concurrency = %d, want 2", got)
	}
}

func TestPrepareCodeGraphIndexes_SerializesProgressCallbacks(t *testing.T) {
	systemID := "codegraph-index-progress-serialization"
	InvalidateCodeGraphIndexCache(systemID)
	targets := []CodeGraphRepoTarget{
		{Name: "first", Path: makeCodeGraphSourceRepo(t, "main.go")},
		{Name: "second", Path: makeCodeGraphSourceRepo(t, "main.go")},
	}

	var workActive atomic.Int32
	var workMaximum atomic.Int32
	var callbackActive atomic.Int32
	var callbackMaximum atomic.Int32
	var statusMu sync.Mutex
	statusCalls := map[string]int{}
	finalStatuses := make(chan struct{})
	var finalStatusCount atomic.Int32
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		active := workActive.Add(1)
		defer workActive.Add(-1)
		recordCodeGraphMaximum(&workMaximum, active)

		path := args[1]
		switch args[0] {
		case "status":
			statusMu.Lock()
			statusCalls[path]++
			call := statusCalls[path]
			statusMu.Unlock()
			if call == 2 {
				if finalStatusCount.Add(1) == int32(len(targets)) {
					close(finalStatuses)
				}
				<-finalStatuses
			}
			return codeGraphStatusJSON(path, true, 2, 3, 4, "complete"), nil
		case "sync":
			return nil, nil
		default:
			t.Errorf("unexpected command: %v", args)
			return nil, errors.New("unexpected command")
		}
	})

	report := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      targets,
		OnProgress: func(string) {
			active := callbackActive.Add(1)
			recordCodeGraphMaximum(&callbackMaximum, active)
			time.Sleep(20 * time.Millisecond)
			callbackActive.Add(-1)
		},
	})

	if report.Ready != len(targets) {
		t.Fatalf("report = %#v, want both repositories ready", report)
	}
	if got := workMaximum.Load(); got != 2 {
		t.Fatalf("repository work maximum concurrency = %d, want 2", got)
	}
	if got := callbackMaximum.Load(); got != 1 {
		t.Fatalf("progress callback maximum concurrency = %d, want 1", got)
	}
}

func TestPrepareCodeGraphIndexes_ProcessCacheReusesReport(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	systemID := "codegraph-index-cache-warning"
	InvalidateCodeGraphIndexCache(systemID)

	var calls atomic.Int32
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
		switch args[0] {
		case "status":
			return codeGraphStatusJSON(repoPath, true, 12, 84, 103, "complete"), nil
		case "sync":
			return nil, errors.New("disk is read-only")
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	})
	opts := CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{{Name: "cached-warning", Path: repoPath, Head: "abc123"}},
	}

	first := PrepareCodeGraphIndexes(context.Background(), opts)
	if got := calls.Load(); got != 2 {
		t.Fatalf("first call executed %d commands, want 2", got)
	}
	if first.Repos[0].Status != "warn" {
		t.Fatalf("first report = %#v, want cached warning", first)
	}
	expected := first.Repos[0]
	first.Repos[0].Name = "mutated by caller"
	first.Repos = append(first.Repos, CodeGraphRepoResult{Name: "also mutated"})

	second := PrepareCodeGraphIndexes(context.Background(), opts)
	if got := calls.Load(); got != 2 {
		t.Fatalf("cache hit executed commands; total = %d, want 2", got)
	}
	if len(second.Repos) != 1 || second.Repos[0] != expected {
		t.Fatalf("cached report was not deep-copied: %#v, want %#v", second, expected)
	}

	InvalidateCodeGraphIndexCache(systemID)
	_ = PrepareCodeGraphIndexes(context.Background(), opts)
	if got := calls.Load(); got != 4 {
		t.Fatalf("invalidated call count = %d, want 4", got)
	}
}

func TestPrepareCodeGraphIndexes_ProcessCachePreservesCurrentInputOrder(t *testing.T) {
	firstPath := makeCodeGraphSourceRepo(t, "first.go")
	secondPath := makeCodeGraphSourceRepo(t, "second.go")
	systemID := "codegraph-index-cache-order"
	InvalidateCodeGraphIndexCache(systemID)

	var calls atomic.Int32
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
		path := args[1]
		switch args[0] {
		case "status":
			if path == firstPath {
				return codeGraphStatusJSON(path, true, 11, 12, 13, "complete"), nil
			}
			return codeGraphStatusJSON(path, true, 21, 22, 23, "complete"), nil
		case "sync":
			return nil, nil
		default:
			t.Errorf("unexpected command: %v", args)
			return nil, errors.New("unexpected command")
		}
	})
	firstTarget := CodeGraphRepoTarget{Name: "first", Path: firstPath, Head: "head-first"}
	secondTarget := CodeGraphRepoTarget{Name: "second", Path: secondPath, Head: "head-second"}

	first := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{firstTarget, secondTarget},
	})
	if got := []string{first.Repos[0].Name, first.Repos[1].Name}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("initial report order = %v", got)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("initial command count = %d, want 6", got)
	}

	reversed := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{secondTarget, firstTarget},
	})
	if got := calls.Load(); got != 6 {
		t.Fatalf("reversed cache hit executed commands; total = %d, want 6", got)
	}
	if got := []string{reversed.Repos[0].Name, reversed.Repos[1].Name}; !reflect.DeepEqual(got, []string{"second", "first"}) {
		t.Fatalf("reversed cache report order = %v, want [second first]", got)
	}
	if reversed.Repos[0].FileCount != 21 || reversed.Repos[1].FileCount != 11 {
		t.Fatalf("reversed cache report mismatched repositories: %#v", reversed.Repos)
	}

	reversed.Repos[0].Name = "caller mutation"
	again := PrepareCodeGraphIndexes(context.Background(), CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{secondTarget, firstTarget},
	})
	if again.Repos[0].Name != "second" || calls.Load() != 6 {
		t.Fatalf("cached reordered report did not preserve deep-copy isolation: %#v", again)
	}
}

func TestPrepareCodeGraphIndexes_StatusUsesSyncTimeout(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	systemID := "codegraph-index-status-timeout"
	InvalidateCodeGraphIndexCache(systemID)

	setCodeGraphRunnerForTest(t, func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "status" {
			t.Errorf("unexpected command: %v", args)
			return nil, errors.New("unexpected command")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	parentCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	report := PrepareCodeGraphIndexes(parentCtx, CodeGraphIndexOptions{
		BinaryPath:  "/managed/codegraph",
		SystemID:    systemID,
		Repos:       []CodeGraphRepoTarget{{Name: "bounded-status", Path: repoPath}},
		SyncTimeout: 20 * time.Millisecond,
	})

	got := report.Repos[0]
	if got.Status != "warn" || got.Action != "failed" || got.Detail != "status timeout, fallback enabled" {
		t.Fatalf("status timeout result = %#v", got)
	}
	if got.DurationMS < 15 || got.DurationMS >= 100 {
		t.Fatalf("status timeout duration_ms = %d, want sync-timeout bound near 20ms", got.DurationMS)
	}
}

func TestPrepareCodeGraphIndexes_CanceledContextStopsSourceWalk(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	systemID := "codegraph-index-canceled-walk"
	InvalidateCodeGraphIndexCache(systemID)

	var calls atomic.Int32
	setCodeGraphRunnerForTest(t, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := PrepareCodeGraphIndexes(ctx, CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{{Name: "canceled-walk", Path: repoPath}},
	})

	if got := calls.Load(); got != 0 {
		t.Fatalf("canceled source walk executed %d CodeGraph commands, want 0", got)
	}
	if got := report.Repos[0]; got.Status != "warn" || got.Detail != "source scan failed: context canceled, fallback enabled" {
		t.Fatalf("canceled source walk result = %#v", got)
	}
}

func TestPrepareCodeGraphIndexes_ConcurrentInvalidationAndCacheStore(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	systemID := "codegraph-index-concurrent-invalidation"
	InvalidateCodeGraphIndexCache(systemID)

	var calls atomic.Int32
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls.Add(1)
		switch args[0] {
		case "status":
			return codeGraphStatusJSON(repoPath, true, 3, 4, 5, "complete"), nil
		case "sync":
			time.Sleep(time.Millisecond)
			return nil, nil
		default:
			t.Errorf("unexpected command: %v", args)
			return nil, errors.New("unexpected command")
		}
	})
	opts := CodeGraphIndexOptions{
		BinaryPath: "/managed/codegraph",
		SystemID:   systemID,
		Repos:      []CodeGraphRepoTarget{{Name: "concurrent", Path: repoPath, Head: "head"}},
	}

	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report := PrepareCodeGraphIndexes(context.Background(), opts)
			if report.Ready != 1 {
				t.Errorf("concurrent report = %#v", report)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			InvalidateCodeGraphIndexCache(systemID)
		}
	}()
	wg.Wait()

	InvalidateCodeGraphIndexCache(systemID)
	before := calls.Load()
	report := PrepareCodeGraphIndexes(context.Background(), opts)
	if report.Ready != 1 || calls.Load() <= before {
		t.Fatalf("final invalidation did not force fresh commands: before=%d after=%d report=%#v", before, calls.Load(), report)
	}
}

func TestPrepareCodeGraphIndexes_UsesPinnedSourceExtensionAndSkipSets(t *testing.T) {
	wantExtensions := strings.Fields(".ts .tsx .mts .cts .ets .js .mjs .cjs .xsjs .xsjslib .jsx .py .pyw .go .rs .java .c .h .cpp .cc .cxx .hpp .hxx .cs .cshtml .razor .php .module .install .theme .inc .yml .yaml .twig .rb .rake .swift .kt .kts .dart .liquid .svelte .vue .astro .r .pas .dpr .dpk .lpr .dfm .fmx .scala .sc .lua .luau .m .mm .sol .cfc .cfm .cfs .metal .cu .cuh .nix .xml .cbl .cob .cobol .cpy .vb .erl .hrl .escript .properties .tf .tfvars .tofu")
	gotExtensions := make([]string, 0, len(codeGraphSourceExtensions))
	for extension := range codeGraphSourceExtensions {
		gotExtensions = append(gotExtensions, extension)
	}
	sort.Strings(gotExtensions)
	sort.Strings(wantExtensions)
	if !reflect.DeepEqual(gotExtensions, wantExtensions) {
		t.Fatalf("source extensions = %v, want pinned v1.3.1 set %v", gotExtensions, wantExtensions)
	}

	wantSkipped := strings.Fields(".git .codegraph node_modules vendor dist build")
	gotSkipped := make([]string, 0, len(codeGraphSkippedDirectories))
	for directory := range codeGraphSkippedDirectories {
		gotSkipped = append(gotSkipped, directory)
	}
	sort.Strings(gotSkipped)
	sort.Strings(wantSkipped)
	if !reflect.DeepEqual(gotSkipped, wantSkipped) {
		t.Fatalf("skipped directories = %v, want %v", gotSkipped, wantSkipped)
	}
}

func TestBuildCodeGraphRepoTargets_AnalysisDefaultsEnabledAndUsesAbsolutePaths(t *testing.T) {
	repoRoot := t.TempDir()
	nonGitRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCodeGraphTest(t, "init", "-q", repoRoot)
	runGitForCodeGraphTest(t, "-C", repoRoot, "add", "main.go")
	runGitForCodeGraphTest(t, "-C", repoRoot, "-c", "user.name=CodeGraph Test", "-c", "user.email=codegraph@example.invalid", "commit", "-qm", "initial")
	head := strings.TrimSpace(runGitForCodeGraphTest(t, "-C", repoRoot, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runGitForCodeGraphTest(t, "-C", repoRoot, "branch", "--show-current"))
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relRoot, err := filepath.Rel(workingDir, repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.SystemConfig{Repos: []config.Repo{
		{Name: "disabled", Analysis: config.RepoAnalysis{Enabled: boolPointer(false)}},
		{Name: "default-enabled"},
		{Name: "monorepo-first", SubPath: "services/orders", Analysis: config.RepoAnalysis{Enabled: boolPointer(true)}},
		{Name: "monorepo-duplicate", SubPath: "services/users", Analysis: config.RepoAnalysis{Enabled: boolPointer(true)}},
		{Name: "not-a-checkout", Analysis: config.RepoAnalysis{Enabled: boolPointer(true)}},
		{Name: "missing-visible", Analysis: config.RepoAnalysis{Enabled: boolPointer(true)}},
	}}
	targets := BuildCodeGraphRepoTargets(cfg, map[string]string{
		"disabled":           repoRoot,
		"default-enabled":    repoRoot,
		"monorepo-first":     "  " + relRoot + "  ",
		"monorepo-duplicate": repoRoot,
		"not-a-checkout":     nonGitRoot,
	})

	want := []CodeGraphRepoTarget{
		{Name: "default-enabled", Path: repoRoot, Branch: branch, Head: head},
		{Name: "not-a-checkout", Path: nonGitRoot, Head: ""},
		{Name: "missing-visible", Path: ""},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func setCodeGraphRunnerForTest(t *testing.T, runner codeGraphCommandRunner) {
	t.Helper()
	previous := runCodeGraphCommand
	runCodeGraphCommand = runner
	t.Cleanup(func() { runCodeGraphCommand = previous })
}

func makeCodeGraphSourceRepo(t *testing.T, sourceName string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, sourceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func codeGraphStatusJSON(projectPath string, initialized bool, fileCount, nodeCount, edgeCount int, state string) []byte {
	return []byte(fmt.Sprintf(`{"initialized":%t,"version":"1.3.1","projectPath":%q,"fileCount":%d,"nodeCount":%d,"edgeCount":%d,"index":{"builtWithVersion":"1.3.1","builtWithExtractionVersion":51,"currentExtractionVersion":51,"reindexRecommended":false,"state":%q,"pendingRefs":0}}`, initialized, projectPath, fileCount, nodeCount, edgeCount, state))
}

func waitForCodeGraphFakeCall(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fake CodeGraph command")
	}
}

func recordCodeGraphMaximum(maximum *atomic.Int32, current int32) {
	for {
		seen := maximum.Load()
		if current <= seen || maximum.CompareAndSwap(seen, current) {
			return
		}
	}
}

func runGitForCodeGraphTest(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
