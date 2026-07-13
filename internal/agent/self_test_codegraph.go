package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"gopkg.in/yaml.v3"
)

type generatedRepoPathMap struct {
	Repos map[string]struct {
		LocalPath string `yaml:"local_path"`
	} `yaml:"repos"`
}

// probeCodeGraphIndexes reports repository index health separately from the
// generic MCP process/tool-surface probe. CodeGraph is an optional enhancement,
// so every unhealthy repository is WARN/SKIP and never a global FAIL.
func probeCodeGraphIndexes(ctx context.Context, cfg *config.SystemConfig, workspaceDir string, binary string, add func(name, status, detail string)) {
	if cfg == nil || !cfg.CodeIntelligence.UsesCodeGraph() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pathMapFile := filepath.Join(workspaceDir, "skills", "routing", "references", "repo-path-map.yaml")
	contents, err := os.ReadFile(pathMapFile)
	if err != nil {
		add("CodeGraph 索引配置", "WARN", fmt.Sprintf("读取 repo-path-map.yaml 失败:%v", err))
		return
	}
	var pathMap generatedRepoPathMap
	if err := yaml.Unmarshal(contents, &pathMap); err != nil {
		add("CodeGraph 索引配置", "WARN", fmt.Sprintf("解析 repo-path-map.yaml 失败:%v", err))
		return
	}

	var repos []config.Repo
	for _, repo := range cfg.Repos {
		if !repo.Analysis.Enabled {
			continue
		}
		repos = append(repos, repo)
	}

	results := make([]SelfTestCheck, len(repos))
	semaphore := make(chan struct{}, defaultCodeGraphMaxConcurrency)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func(index int, repo config.Repo) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
				results[index] = probeCodeGraphIndex(ctx, repo, pathMap, binary)
			case <-ctx.Done():
				results[index] = SelfTestCheck{
					Name: "CodeGraph 索引 " + repo.Name, Status: "WARN",
					Detail: fmt.Sprintf("索引检查取消:%v", ctx.Err()),
				}
			}
		}(i, repo)
	}
	wg.Wait()
	for _, result := range results {
		add(result.Name, result.Status, result.Detail)
	}
}

func probeCodeGraphIndex(ctx context.Context, repo config.Repo, pathMap generatedRepoPathMap, binary string) SelfTestCheck {
	check := SelfTestCheck{Name: "CodeGraph 索引 " + repo.Name}
	set := func(status, detail string) SelfTestCheck {
		check.Status = status
		check.Detail = detail
		return check
	}

	repoPath := strings.TrimSpace(pathMap.Repos[repo.Name].LocalPath)
	if repoPath == "" {
		return set("WARN", "repo-path-map.yaml 缺少 local_path,请补齐仓库路径后重新索引")
	}
	repoPath = filepath.Clean(repoPath)
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return set("WARN", fmt.Sprintf("local_path 不可用:%s", repoPath))
	}

	hasSource, err := codeGraphRepoHasSource(ctx, repoPath)
	if err != nil {
		return set("WARN", fmt.Sprintf("源码扫描失败:%v", err))
	}
	if !hasSource {
		return set("SKIP", "无受支持源码,跳过 CodeGraph 索引检查")
	}

	status, err := queryCodeGraphStatus(ctx, binary, repoPath, defaultCodeGraphSyncTimeout)
	if err != nil {
		return set("WARN", fmt.Sprintf("status 查询失败:%v", err))
	}
	if !status.Initialized {
		return set("WARN", "索引未初始化,请重新索引")
	}
	if status.Index.ReindexRecommended || status.Index.BuiltWithExtractionVersion < status.Index.CurrentExtractionVersion {
		return set("WARN", fmt.Sprintf("extraction version %d -> %d,请重新索引",
			status.Index.BuiltWithExtractionVersion, status.Index.CurrentExtractionVersion))
	}
	if !codeGraphStatusReady(status) {
		return set("WARN", fmt.Sprintf("索引未就绪:state=%s, files=%d, nodes=%d;请重新索引",
			status.Index.State, status.FileCount, status.NodeCount))
	}
	return set("PASS", fmt.Sprintf("files=%d, nodes=%d, edges=%d",
		status.FileCount, status.NodeCount, status.EdgeCount))
}
