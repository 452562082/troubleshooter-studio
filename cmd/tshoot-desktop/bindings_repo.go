// bindings_repo.go —— 仓库 / 机器人扫描 / Analyze 类 binding。
//
// 跟 bindings_core.go 同样属于"只读 / 干跑"类(不改 workspace、不 shell-out),
// 但聚焦在仓库探测和扫描:
//   - DiscoverBots:扫已装机器人(tshoot.json 锚点)
//   - GetMissingRepoPaths:Gen/Deploy 前预检本机仓库路径覆盖
//   - ListBranchesForRepo / DetectSubmodulesForRepo / RecommendRoleForRepo:wizard Step 4 仓库填写辅助
//   - Analyze / AnalyzeV2:扫源码反推 yaml(配置中心 / service_names / 数据层 / 依赖图)
//   - GetRemoteURL:本地仓库目录反查 origin url(本地模式反填 yaml)

package main

import (
	"os"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/aitools"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// DiscoverBots 扫描本机已安装的排障机器人(tshoot.json 锚点)。
// 默认根:
//   - ~/.openclaw/workspace/                — OpenClaw workspace
//   - ~/.claude/skills/, ~/.cursor/skills/, ~/.codex/skills/ — IDE 平台 skills
//
// 桌面 app 不扫 CWD(CLI 才有意义)。extraRoots 是 UI 侧让用户追加的项目根。
//
// 返回结果做两步 enrichment(scan 完跑):
//  1. IDEAvailable —— 探测对应 IDE 二进制还在不在(用户卸载 IDE 但 ~/.<target>/
//     还在的常见场景),BotsPage 卡片标 "⚠ IDE 已卸载,机器人不可用"
//  2. Ghost —— ~/.tshoot/config.json deployed_bots 里有但 disk 上 tshoot.json
//     不在的(用户外部 rm -rf 清掉),append 一条 ghost=true 的占位让 UI 能引导
//     "重新部署 / 忘掉它"。Ghost 条目 Meta 字段从 deployed_bots 元数据回填,
//     但 TroubleshooterYAML 缺(disk 没了),前端要把"诊断/重 gen/编辑"按钮 disable。
func (a *App) DiscoverBots(extraRoots []string) ([]discover.DiscoveredAgent, error) {
	// 一次性迁移:老用户的 Claude Code/Cursor 机器人锚点只在 staging 里(2026-04-30 之前
	// 部署的),新版 discover 扫不到。MigrateLegacyAnchors 把 staging 的 tshoot.json
	// 拷一份到真实位置,迁移完老机器人重新出现在 BotsPage。幂等。
	_ = agent.MigrateLegacyAnchors()
	roots := []string{
		"~/.openclaw/workspace",
		"~/.claude/skills",
		"~/.cursor/skills",
		"~/.codex/skills",
	}
	roots = append(roots, extraRoots...)
	bots, err := discover.Scan(roots)
	if err != nil {
		return nil, err
	}

	// Step 1: enrich IDEAvailable —— 一次性探测三家 IDE,for 每个 bot 按 target 查表。
	// openclaw 直接 true(产品自带,不靠探测三方 IDE)。cache 在本次调用内,避免 N×detect。
	ideInstalled := map[string]bool{
		"openclaw":    true,
		"claude-code": aitools.DetectClaudeCode().Installed,
		"cursor":      aitools.DetectCursor().Installed,
		"codex":       aitools.DetectCodex().Installed,
	}
	for i := range bots {
		bots[i].IDEAvailable = ideInstalled[bots[i].Meta.Target]
	}

	// Step 2: ghost bot 合并 —— deployed_bots 里有但 scan 没找到的,append 一条
	// ghost=true 占位。key="<system_id>|<target>" 跟 disk 上的 bot 比对去重。
	seen := map[string]bool{}
	for _, b := range bots {
		seen[userconfig.DeployedBotKey(b.Meta.SystemID, b.Meta.Target)] = true
	}
	for key, entry := range userconfig.ListDeployedBots() {
		if seen[key] {
			continue
		}
		ghost := discover.DiscoveredAgent{
			Meta: discover.Meta{
				SystemID:   entry.SystemID,
				SystemName: entry.SystemName,
				Target:     entry.Target,
			},
			Path:         entry.Path,
			IDEAvailable: ideInstalled[entry.Target],
			Ghost:        true,
		}
		bots = append(bots, ghost)
	}
	return bots, nil
}

// MissingRepoPathsResult 给 UI 决定"要不要弹仓库路径补全对话框"。
type MissingRepoPathsResult struct {
	SystemID string            `json:"system_id"`
	Saved    map[string]string `json:"saved"`              // 已存的(repo.name → 本机绝对路径)
	Missing  []string          `json:"missing"`            // 还没存的(repo.name 列表;按 yaml 顺序)
	Suggest  string            `json:"suggest_repos_root"` // UI placeholder:用户全局默认 repos root
}

// GetMissingRepoPaths 给 UI 在调 Gen / RunInstall 前预检:返回当前 system 的已存
// 路径 + 仍缺的仓库名。UI 命中 Missing 非空就弹对话框收路径,落 SaveRepoPaths
// 后再走正常 Gen / Deploy 路径。
func (a *App) GetMissingRepoPaths(yamlText string) (*MissingRepoPathsResult, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	saved := userconfig.GetRepoPathsForSystem(cfg.System.ID)
	if saved == nil {
		saved = map[string]string{}
	}
	return &MissingRepoPathsResult{
		SystemID: cfg.System.ID,
		Saved:    saved,
		Missing:  agent.CheckMissingRepoPaths(cfg, saved),
		Suggest:  userconfig.DefaultReposRootOrFallback(),
	}, nil
}

// PathExists 给 wizard 做"扫描前预检"用 —— 子模块代码可能被用户手动 rm 了,
// 前端先 check 一下,提示明确的"先去 umbrella 行点同步扫描拉子模块"指引,
// 比让 scanSingleRepo 真跑下去拿到 backend 的"path missing skipped"模糊错误友好。
// 空路径返 false。
func (a *App) PathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(userconfig.ExpandHome(p))
	return err == nil
}

// ListBranchesForRepo 给 wizard Step 4 用 —— 仅列分支,比 AnalyzeV2 / scanSingleRepo
// 轻量得多(不跑 stack 检测,不扫 dependency,不 clone)。空路径或非 git 仓库返回空。
func (a *App) ListBranchesForRepo(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	return analyzer.ListBranches(userconfig.ExpandHome(repoPath))
}

// DetectSubmodulesForRepo 自动检测仓库是不是 monorepo + 列出每个子模块。
// 命中 0 → 不是 monorepo,UI 静默;命中 N>1 → UI 弹"检测到 N 个子模块,一键拆分"。
func (a *App) DetectSubmodulesForRepo(repoPath string) []analyzer.SubmoduleHint {
	if repoPath == "" {
		return nil
	}
	return analyzer.DetectSubmodules(userconfig.ExpandHome(repoPath))
}

// RecommendRoleForRepo 给 (stack, name, optionalLocalPath) 推荐 role + 理由。
// 空路径只看名字 + stack 兜底,有路径时进一步读 package.json / pom.xml / go.mod 等。
func (a *App) RecommendRoleForRepo(stack, name, path string) analyzer.RoleHint {
	expanded := path
	if path != "" {
		expanded = userconfig.ExpandHome(path)
	}
	return analyzer.RecommendRole(stack, name, expanded)
}

// Analyze 扫描仓库,抽 service_names / 配置中心线索 / 数据表 / 依赖图等。
//
// 路径来源优先级:
//  1. saved per-repo paths(~/.tshoot/config.json[system.id])—— 部署/Step 4 已记录;
//     按 repo.name 直接命中绝对路径,**优先于 reposRoot**。
//  2. reposRoot 非空 —— saved 没记的 repo 用 <reposRoot>/<repo.name> 拼。
//  3. 都没有 —— analyzerpipe 报错 "ReposRoot or RepoPaths required",前端引导补路径。
//
// autoClone=true 时缺失的仓库会自动 shallow clone(需要 git + 凭证 + reposRoot 兜底)。
// 默认 false,缺失的仓库标 "skipped"。进度日志通过 Wails EventsEmit "analyze:log" 推到前端。
func (a *App) Analyze(yamlText, reposRoot string, autoClone bool) (*analyzerpipe.Result, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	expanded := map[string]string{}
	for k, v := range userconfig.GetRepoPathsForSystem(cfg.System.ID) {
		if strings.TrimSpace(v) != "" {
			expanded[k] = userconfig.ExpandHome(v)
		}
	}
	return analyzerpipe.Run(cfg, analyzerpipe.Options{
		ReposRoot: userconfig.ExpandHome(reposRoot),
		RepoPaths: expanded,
		AutoClone: autoClone,
		OnProgress: func(msg string) {
			wailsruntime.EventsEmit(a.ctx, "analyze:log", msg)
		},
	})
}

// AnalyzeInput 给 AnalyzeV2 的入参:比 Analyze 多了 RepoPaths 允许 per-repo 指定
// 本地绝对路径(InitPage Step 4 的"本地 vs 远程"混合模式)。
type AnalyzeInput struct {
	YAMLText  string            `json:"yaml_text"`
	ReposRoot string            `json:"repos_root"`           // 远程 clone 的默认父目录
	RepoPaths map[string]string `json:"repo_paths,omitempty"` // key=repo.name, value=本地绝对路径(本地模式)
	AutoClone bool              `json:"auto_clone"`
	RepoName  string            `json:"repo_name,omitempty"` // 非空则只扫这一个仓库(单仓库 inline 扫描)
}

// AnalyzeV2 混合来源版:允许给部分仓库指定本地路径(不走 ReposRoot/Name 默认拼法)。
// 前端 InitPage Step 4 用这个;CLI 的 analyze 继续用 Analyze。
func (a *App) AnalyzeV2(in AnalyzeInput) (*analyzerpipe.Result, error) {
	cfg, err := config.LoadFromBytes([]byte(in.YAMLText))
	if err != nil {
		return nil, err
	}
	// 路径里可能夹带 ~(用户手输 / 从 UI displayPath 保留的写法),统一展开
	expandedPaths := make(map[string]string, len(in.RepoPaths))
	for k, v := range in.RepoPaths {
		expandedPaths[k] = userconfig.ExpandHome(v)
	}
	return analyzerpipe.Run(cfg, analyzerpipe.Options{
		ReposRoot: userconfig.ExpandHome(in.ReposRoot),
		RepoPaths: expandedPaths,
		AutoClone: in.AutoClone,
		RepoName:  in.RepoName,
		OnProgress: func(msg string) {
			wailsruntime.EventsEmit(a.ctx, "analyze:log", msg)
		},
	})
}

// GetRemoteURL 本地模式下,前端选了一个仓库目录,想反填 yaml.repos[].url 字段时用。
// 返回 `git -C <path> remote get-url origin` 的结果;不是 git 仓库 / 没 origin 返回空串。
func (a *App) GetRemoteURL(repoPath string) string {
	return analyzer.GetRemoteURL(userconfig.ExpandHome(repoPath))
}
