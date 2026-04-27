// bindings_core.go —— 核心只读 / 干跑类 binding：
// Version / DiscoverBots / Validate / Gen / Plan / Diff / Analyze / Doctor。
//
// 凡是不改装机器人活 workspace、也不 shell-out 跑 install.sh 的 binding 都在这里。
// 改装已装机器人的走 bindings_apply.go；跑 install.sh / 写 .env 的走 bindings_deploy.go。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// Version 前端可调：window.go.main.App.Version()
func (a *App) Version() string {
	if commit != "" {
		return fmt.Sprintf("%s (%s)", version, commit)
	}
	return version
}

// DiscoverBots 扫描本机已安装的排障机器人（tshoot.json 锚点）。
// 默认只扫 ~/.openclaw/workspace/（桌面 app 的 CWD 没意义，不同于 CLI 的 DefaultRoots）。
// extraRoots 是 UI 侧让用户追加的项目根（用于找 claude-code / cursor 装进去的机器人）。
// 前端调用：window.go.main.App.DiscoverBots([]) or DiscoverBots(["/path/to/project"]).
func (a *App) DiscoverBots(extraRoots []string) ([]discover.DiscoveredAgent, error) {
	roots := append([]string{"~/.openclaw/workspace"}, extraRoots...)
	return discover.Scan(roots)
}

// ValidateResult 与 /api/validate 返回形状对齐，前端已有依赖。
type ValidateResult struct {
	Valid  bool   `json:"valid"`
	System string `json:"system"`
	Name   string `json:"name"`
	Envs   int    `json:"envs"`
	Repos  int    `json:"repos"`
}

// Validate 校验 system.yaml 内容，解析失败返回 error；成功返回概要。
func (a *App) Validate(yamlText string) (*ValidateResult, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return &ValidateResult{
		Valid:  true,
		System: cfg.System.ID,
		Name:   cfg.System.Name,
		Envs:   len(cfg.Environments),
		Repos:  len(cfg.Repos),
	}, nil
}

// Gen 按 system.yaml 实际落盘生成机器人产物（写到 outputDir；后续要部署还得走
// ImportAndDeploy 或 RunInstall 把产物装到 AI 平台）。
// outputDir 为空时用 yaml 里的 generation.output_dir；相对路径解析成绝对路径，
// 让 UI 能稳定展示"产物在 /abs/path/xxx"。
func (a *App) Gen(yamlText, outputDir string) (*generator.GenSummary, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if outputDir == "" {
		outputDir = cfg.Generation.OutputDir
	}
	if outputDir == "" {
		outputDir = "./dist"
	}
	if !filepath.IsAbs(outputDir) {
		abs, _ := filepath.Abs(outputDir)
		outputDir = abs
	}
	g := generator.New(cfg, a.templateRoot, outputDir)
	g.TshootVersion = version
	g.SystemYAMLSource = []byte(yamlText)
	if err := g.Generate(); err != nil {
		return nil, err
	}
	return g.Summary, nil
}

// Plan 干跑一次 gen,返回 Plan 结构(skills / files 分布 / config-map 投影)。
// 等价 POST /api/plan。不落盘(generator 写到临时目录后清掉)。
func (a *App) Plan(yamlText string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-plan-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan("")
}

// Diff 跟 Plan 用同一个底层(generator.BuildPlan),区别是传入 existingDir,
// 让底层 diffFileSets 给出 create / modify / remove 三类文件差异,用于新产物
// vs 现有产物的对比预览。existingDir 为空时等价 Plan。
func (a *App) Diff(yamlText, existingDir string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan(existingDir)
}

// Analyze 扫描 reposRoot 下的所有仓库(按 repos[].name 匹配子目录),抽 service_names
// 和配置中心线索,返回完整 report + 每仓库摘要。
// autoClone=true 时缺失的仓库会自动 shallow clone(需要 git + 凭证);默认 false,
// 缺失的仓库标 "skipped"。进度日志通过 Wails EventsEmit "analyze:log" 推到前端。
func (a *App) Analyze(yamlText, reposRoot string, autoClone bool) (*analyzerpipe.Result, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return analyzerpipe.Run(cfg, analyzerpipe.Options{
		ReposRoot: userconfig.ExpandHome(reposRoot),
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
// 所有来自前端的路径过 ExpandHome,支持用户输入 ~/foo 这种写法。
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
// 用户可能输入 ~/code/foo(手输场景),展开后再交给 git CLI。
func (a *App) GetRemoteURL(repoPath string) string {
	return analyzer.GetRemoteURL(userconfig.ExpandHome(repoPath))
}

// Doctor 对比声明 vs 代码实态,返回漂移报告。等价 POST /api/doctor?repos_root=...
// reposRoot 留空则只校验声明的一致性,不扫代码。
func (a *App) Doctor(yamlText, reposRoot string) (*doctor.Report, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return doctor.Check(cfg, reposRoot)
}
