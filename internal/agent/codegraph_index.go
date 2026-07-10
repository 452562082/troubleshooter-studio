package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

const (
	defaultCodeGraphInitTimeout    = 120 * time.Second
	defaultCodeGraphSyncTimeout    = 30 * time.Second
	defaultCodeGraphMaxConcurrency = 2
)

type CodeGraphRepoTarget struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Head string `json:"head,omitempty"`
}

type CodeGraphIndexOptions struct {
	BinaryPath     string
	SystemID       string
	Repos          []CodeGraphRepoTarget
	OnProgress     func(string)
	InitTimeout    time.Duration
	SyncTimeout    time.Duration
	MaxConcurrency int
}

type CodeGraphRepoResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	FileCount  int    `json:"file_count,omitempty"`
	NodeCount  int    `json:"node_count,omitempty"`
	EdgeCount  int    `json:"edge_count,omitempty"`
	IndexState string `json:"index_state,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type CodeGraphIndexReport struct {
	Ready int                   `json:"ready"`
	Total int                   `json:"total"`
	Repos []CodeGraphRepoResult `json:"repos"`
}

type codeGraphCommandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)

var runCodeGraphCommand codeGraphCommandRunner = runCodeGraphCommandExec

var (
	codeGraphIndexCacheMu sync.Mutex
	codeGraphIndexCache   = make(map[string]CodeGraphIndexReport)
)

var codeGraphSourceExtensions = map[string]struct{}{
	".ts": {}, ".tsx": {}, ".mts": {}, ".cts": {}, ".ets": {},
	".js": {}, ".mjs": {}, ".cjs": {}, ".xsjs": {}, ".xsjslib": {}, ".jsx": {},
	".py": {}, ".pyw": {}, ".go": {}, ".rs": {}, ".java": {},
	".c": {}, ".h": {}, ".cpp": {}, ".cc": {}, ".cxx": {}, ".hpp": {}, ".hxx": {},
	".cs": {}, ".cshtml": {}, ".razor": {},
	".php": {}, ".module": {}, ".install": {}, ".theme": {}, ".inc": {},
	".yml": {}, ".yaml": {}, ".twig": {}, ".rb": {}, ".rake": {},
	".swift": {}, ".kt": {}, ".kts": {}, ".dart": {}, ".liquid": {},
	".svelte": {}, ".vue": {}, ".astro": {}, ".r": {},
	".pas": {}, ".dpr": {}, ".dpk": {}, ".lpr": {}, ".dfm": {}, ".fmx": {},
	".scala": {}, ".sc": {}, ".lua": {}, ".luau": {}, ".m": {}, ".mm": {},
	".sol": {}, ".cfc": {}, ".cfm": {}, ".cfs": {}, ".metal": {}, ".cu": {}, ".cuh": {},
	".nix": {}, ".xml": {}, ".cbl": {}, ".cob": {}, ".cobol": {}, ".cpy": {},
	".vb": {}, ".erl": {}, ".hrl": {}, ".escript": {}, ".properties": {},
	".tf": {}, ".tfvars": {}, ".tofu": {},
}

var codeGraphSkippedDirectories = map[string]struct{}{
	".git": {}, ".codegraph": {}, "node_modules": {}, "vendor": {},
	"dist": {}, "build": {},
}

type codeGraphStatus struct {
	Initialized bool   `json:"initialized"`
	Version     string `json:"version"`
	ProjectPath string `json:"projectPath"`
	FileCount   int    `json:"fileCount"`
	NodeCount   int    `json:"nodeCount"`
	EdgeCount   int    `json:"edgeCount"`
	Index       struct {
		BuiltWithVersion           string `json:"builtWithVersion"`
		BuiltWithExtractionVersion int    `json:"builtWithExtractionVersion"`
		CurrentExtractionVersion   int    `json:"currentExtractionVersion"`
		ReindexRecommended         bool   `json:"reindexRecommended"`
		State                      string `json:"state"`
		PendingRefs                int    `json:"pendingRefs"`
	} `json:"index"`
}

func BuildCodeGraphRepoTargets(cfg *config.SystemConfig, repoPaths map[string]string) []CodeGraphRepoTarget {
	if cfg == nil {
		return nil
	}

	targets := make([]CodeGraphRepoTarget, 0, len(cfg.Repos))
	seenPaths := make(map[string]struct{}, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		if !repo.Analysis.Enabled {
			continue
		}

		localPath := strings.TrimSpace(repoPaths[repo.Name])
		if localPath == "" {
			targets = append(targets, CodeGraphRepoTarget{Name: repo.Name})
			continue
		}
		absolutePath, err := filepath.Abs(localPath)
		if err != nil {
			targets = append(targets, CodeGraphRepoTarget{Name: repo.Name})
			continue
		}
		absolutePath = filepath.Clean(absolutePath)
		if _, duplicate := seenPaths[absolutePath]; duplicate {
			continue
		}
		seenPaths[absolutePath] = struct{}{}

		target := CodeGraphRepoTarget{Name: repo.Name, Path: absolutePath}
		if output, err := exec.Command("git", "-C", absolutePath, "rev-parse", "HEAD").Output(); err == nil {
			target.Head = strings.TrimSpace(string(output))
		}
		targets = append(targets, target)
	}
	return targets
}

func PrepareCodeGraphIndexes(ctx context.Context, opts CodeGraphIndexOptions) CodeGraphIndexReport {
	if ctx == nil {
		ctx = context.Background()
	}
	cacheKey := codeGraphIndexCacheKey(opts.SystemID, opts.Repos)
	if cached, ok := loadCodeGraphIndexReport(cacheKey); ok {
		return cached
	}

	initTimeout := opts.InitTimeout
	if initTimeout == 0 {
		initTimeout = defaultCodeGraphInitTimeout
	}
	syncTimeout := opts.SyncTimeout
	if syncTimeout == 0 {
		syncTimeout = defaultCodeGraphSyncTimeout
	}
	maxConcurrency := opts.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = defaultCodeGraphMaxConcurrency
	}
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}

	report := CodeGraphIndexReport{
		Total: len(opts.Repos),
		Repos: make([]CodeGraphRepoResult, len(opts.Repos)),
	}
	semaphore := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i, target := range opts.Repos {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(index int, repo CodeGraphRepoTarget) {
			defer wg.Done()
			defer func() { <-semaphore }()
			result := prepareCodeGraphRepo(ctx, opts.BinaryPath, repo, initTimeout, syncTimeout)
			report.Repos[index] = result
			if opts.OnProgress != nil {
				opts.OnProgress(codeGraphProgressLine(result))
			}
		}(i, target)
	}
	wg.Wait()

	for _, result := range report.Repos {
		if result.Status == "ready" {
			report.Ready++
		}
	}
	storeCodeGraphIndexReport(cacheKey, report)
	return cloneCodeGraphIndexReport(report)
}

func InvalidateCodeGraphIndexCache(systemID string) {
	prefix := systemID + "\n"
	codeGraphIndexCacheMu.Lock()
	defer codeGraphIndexCacheMu.Unlock()
	for key := range codeGraphIndexCache {
		if strings.HasPrefix(key, prefix) {
			delete(codeGraphIndexCache, key)
		}
	}
}

func runCodeGraphCommandExec(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).CombinedOutput()
}

func prepareCodeGraphRepo(ctx context.Context, binary string, target CodeGraphRepoTarget, initTimeout, syncTimeout time.Duration) (result CodeGraphRepoResult) {
	started := time.Now()
	result = CodeGraphRepoResult{Name: target.Name, Path: target.Path}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
	}()

	if strings.TrimSpace(target.Path) == "" {
		result.Action = "skipped"
		result.Status = "skipped"
		result.Detail = "repository path missing"
		return result
	}
	info, err := os.Stat(target.Path)
	if err != nil || !info.IsDir() {
		result.Action = "skipped"
		result.Status = "skipped"
		result.Detail = "repository path unavailable"
		return result
	}
	hasSource, err := codeGraphRepoHasSource(target.Path)
	if err != nil {
		return codeGraphWarningResult(result, fmt.Sprintf("source scan failed: %v, fallback enabled", err))
	}
	if !hasSource {
		result.Action = "skipped"
		result.Status = "skipped"
		result.Detail = "no supported source files"
		return result
	}

	status, err := queryCodeGraphStatus(ctx, binary, target.Path)
	if err != nil {
		return codeGraphWarningResult(result, fmt.Sprintf("status failed: %v, fallback enabled", err))
	}

	command := "sync"
	action := "synced"
	timeout := syncTimeout
	if !status.Initialized {
		command = "init"
		action = "initialized"
		timeout = initTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	output, commandErr := runCodeGraphCommand(commandCtx, binary, command, target.Path)
	commandCtxErr := commandCtx.Err()
	cancel()
	if commandErr != nil {
		if errors.Is(commandCtxErr, context.DeadlineExceeded) {
			return codeGraphWarningResult(result, fmt.Sprintf("%s timeout, fallback enabled", command))
		}
		return codeGraphWarningResult(result, fmt.Sprintf("%s failed: %s, fallback enabled", command, codeGraphCommandError(commandErr, output)))
	}

	finalStatus, err := queryCodeGraphStatus(ctx, binary, target.Path)
	if err != nil {
		return codeGraphWarningResult(result, fmt.Sprintf("status after %s failed: %v, fallback enabled", command, err))
	}
	if !codeGraphStatusReady(finalStatus) {
		detail := fmt.Sprintf("index not ready after %s", command)
		if finalStatus.Index.State != "" {
			detail += ": state=" + finalStatus.Index.State
		}
		return codeGraphWarningResult(result, detail+", fallback enabled")
	}

	result.Action = action
	result.Status = "ready"
	result.FileCount = finalStatus.FileCount
	result.NodeCount = finalStatus.NodeCount
	result.EdgeCount = finalStatus.EdgeCount
	result.IndexState = finalStatus.Index.State
	return result
}

func codeGraphWarningResult(result CodeGraphRepoResult, detail string) CodeGraphRepoResult {
	result.Action = "failed"
	result.Status = "warn"
	result.Detail = detail
	return result
}

func queryCodeGraphStatus(ctx context.Context, binary, repoPath string) (codeGraphStatus, error) {
	output, err := runCodeGraphCommand(ctx, binary, "status", repoPath, "--json")
	if err != nil {
		return codeGraphStatus{}, errors.New(codeGraphCommandError(err, output))
	}
	var status codeGraphStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return codeGraphStatus{}, fmt.Errorf("decode status JSON: %w", err)
	}
	return status, nil
}

func codeGraphStatusReady(status codeGraphStatus) bool {
	return status.Initialized && status.Index.State == "complete" && status.FileCount > 0 && status.NodeCount > 0
}

func codeGraphCommandError(err error, output []byte) string {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, detail)
}

func codeGraphRepoHasSource(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				if _, skip := codeGraphSkippedDirectories[entry.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if _, supported := codeGraphSourceExtensions[filepath.Ext(entry.Name())]; supported {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

func codeGraphProgressLine(result CodeGraphRepoResult) string {
	durationSeconds := float64(result.DurationMS) / float64(time.Second/time.Millisecond)
	switch result.Status {
	case "ready":
		return fmt.Sprintf("[codegraph] %s: %s (%d files, %d nodes, %.1fs)", result.Name, result.Action, result.FileCount, result.NodeCount, durationSeconds)
	case "skipped":
		return fmt.Sprintf("[codegraph] %s: skipped (%s)", result.Name, result.Detail)
	default:
		return fmt.Sprintf("[codegraph] %s: warn (%s)", result.Name, result.Detail)
	}
}

func codeGraphIndexCacheKey(systemID string, repos []CodeGraphRepoTarget) string {
	parts := make([]string, 0, len(repos))
	for _, repo := range repos {
		path := strings.TrimSpace(repo.Path)
		if path != "" {
			if absolutePath, err := filepath.Abs(path); err == nil {
				path = filepath.Clean(absolutePath)
			}
		}
		parts = append(parts, repo.Name+"="+path+"@"+repo.Head)
	}
	sort.Strings(parts)
	return systemID + "\n" + strings.Join(parts, "\n")
}

func loadCodeGraphIndexReport(key string) (CodeGraphIndexReport, bool) {
	codeGraphIndexCacheMu.Lock()
	defer codeGraphIndexCacheMu.Unlock()
	report, ok := codeGraphIndexCache[key]
	if !ok {
		return CodeGraphIndexReport{}, false
	}
	return cloneCodeGraphIndexReport(report), true
}

func storeCodeGraphIndexReport(key string, report CodeGraphIndexReport) {
	codeGraphIndexCacheMu.Lock()
	defer codeGraphIndexCacheMu.Unlock()
	codeGraphIndexCache[key] = cloneCodeGraphIndexReport(report)
}

func cloneCodeGraphIndexReport(report CodeGraphIndexReport) CodeGraphIndexReport {
	cloned := report
	cloned.Repos = append([]CodeGraphRepoResult(nil), report.Repos...)
	return cloned
}
