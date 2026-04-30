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
	"sort"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
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
// 默认根:
//   - ~/.openclaw/workspace/                — OpenClaw workspace
//   - ~/.tshoot/openclaw/, ~/.tshoot/claude-code/, ~/.tshoot/cursor/ — wizard 一键部署落地的中间包
//
// 桌面 app 不扫 CWD（CLI 才有意义）。extraRoots 是 UI 侧让用户追加的项目根（用于找
// claude-code / cursor 直接装进项目里的机器人）。
func (a *App) DiscoverBots(extraRoots []string) ([]discover.DiscoveredAgent, error) {
	// 跟 discover.DefaultRoots() 对齐(那边给 CLI 用,这里给桌面 app 用)。
	// 三个默认位置都是"真实部署位置";staging 中间包(~/.tshoot/<target>/)不再扫。
	// 详见 DefaultRoots 注释。
	//
	// 一次性迁移:老用户的 Claude Code/Cursor 机器人锚点只在 staging 里(2026-04-30 之前
	// 部署的),新版 discover 扫不到。MigrateLegacyAnchors 把 staging 的 tshoot.json 拷一份
	// 到真实位置,迁移完老机器人重新出现在 BotsPage。幂等 —— 已经迁移过 / 真实位置没真部署
	// 都安全 no-op。每次扫描跑一遍代价 ms 级,简单直接。
	_ = agent.MigrateLegacyAnchors()
	roots := []string{
		"~/.openclaw/workspace",
		"~/.claude/skills",
		"~/.cursor/skills",
		"~/.codex/skills",
	}
	// 把用户在 wizard 里选过的"自定义安装根目录"也加进扫描列表 —— 否则装到非默认
	// 位置的机器人在 BotsPage 是隐形的。target 决定子目录(openclaw → workspace/,
	// 其它 → skills/),跟默认 ~/.<target> 下的布局保持一致。
	for target, dir := range userconfig.GetCustomInstallRoots() {
		if dir = strings.TrimSpace(dir); dir == "" {
			continue
		}
		switch target {
		case "openclaw":
			roots = append(roots, filepath.Join(dir, "workspace"))
		case "claude-code", "cursor", "codex":
			roots = append(roots, filepath.Join(dir, "skills"))
		}
	}
	roots = append(roots, extraRoots...)
	return discover.Scan(roots)
}

// ValidateResult 与 /api/validate 返回形状对齐，前端已有依赖。
// Issues 是健康检查的额外发现:Validate 通过(yaml 能跑)后再跑 HealthCheck,
// 把"语义层"的缺口(可观测性 wiring 不全 / 仓库分支缺 env / 多源没指定 source 等)
// 一并返给前端。Issues 仅供展示提醒,不阻断生成。
type ValidateResult struct {
	Valid  bool                  `json:"valid"`
	System string                `json:"system"`
	Name   string                `json:"name"`
	Envs   int                   `json:"envs"`
	Repos  int                   `json:"repos"`
	Issues []config.HealthIssue  `json:"issues,omitempty"`
}

// PrefillCreds 从 yaml 抽 install 阶段需要的环境变量默认值(KUBOARD_URL_DEV / GRAFANA_URL_DEV
// 等),让 UI 表单做"已经在 yaml 里写过的字段自动填上"。
// yaml 解析失败返回 error;成功返回 env var key → value(value 为空的 key 不返)。
// UI 用法:导入 yaml 时调一次,把返回值 merge 到 toolInputs / form state,用户再编辑。
// 注:install 时(RunInstall)也会内部再 merge 一次 prefill 兜底,即使 UI 没显式调本接口
// 也能保证 yaml 写过的字段不丢。
func (a *App) PrefillCreds(yamlText string) (map[string]string, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return agent.PrefillCredsFromYAML(cfg), nil
}

// Validate 校验 system.yaml 内容,解析失败返回 error;成功后再跑健康检查,
// 把语义层缺口(severity warn/info/error)一并放进 Issues。
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
		Issues: config.HealthCheck(cfg),
	}, nil
}

// Gen 按 system.yaml 实际落盘生成机器人产物(写到 outputDir;后续要部署还得走
// ImportAndDeploy 或 RunInstall 把产物装到 AI 平台)。
// outputDir 为空时默认 ./dist;相对路径解析成绝对路径,让 UI 能稳定展示"产物在 /abs/path/xxx"。
//
// 自动 analyzer:Gen 内部从 ~/.tshoot/config.json 读 system 对应的本地仓库路径,
// 命中即跑一遍 analyzerpipe.Run,把 findings / dependency_scan / schema_scan 折进
// generator,产物里 service-dependency-map.upstream/downstream + data-schema-map.tables
// 自动填齐(用户走 BotsPage / Editor 部署路径不再"两份 map 全空白手填")。
// 缺失路径用 GetMissingRepoPaths 取,UI 应在 Gen 之前弹 prompt 让用户补,
// 然后调 SaveRepoPaths 落进 ~/.tshoot/config.json,再 Gen 就能命中。
func (a *App) Gen(yamlText, outputDir string) (*generator.GenSummary, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
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

	// auto-analyze:从 userconfig 读已存的本地仓库路径,命中即跑一遍 analyzer。
	// 跑失败 / 路径全空 都不阻塞 gen,只是产物里两份 map 走 fallback(yaml 反推 / 留空)。
	saved := userconfig.GetRepoPathsForSystem(cfg.System.ID)
	if result, aerr := agent.RunAutoAnalyze(agent.RunAutoAnalyzeOptions{
		Cfg:       cfg,
		RepoPaths: saved,
		OnLog: func(msg string) {
			wailsruntime.EventsEmit(a.ctx, "gen:log", msg)
		},
	}); aerr == nil && result != nil {
		g.LoadAnalysisReport(result.Report)
	} else if aerr != nil {
		// 不中断 gen;只是日志告诉用户"分析没跑成,产物某些字段会空"
		wailsruntime.EventsEmit(a.ctx, "gen:log", "[warn] auto-analyze failed: "+aerr.Error())
	}

	if err := g.Generate(); err != nil {
		return nil, err
	}
	return g.Summary, nil
}

// MissingRepoPathsResult 给 UI 决定"要不要弹仓库路径补全对话框"。
type MissingRepoPathsResult struct {
	SystemID string            `json:"system_id"`
	Saved    map[string]string `json:"saved"`              // 已存的(repo.name → 本机绝对路径)
	Missing  []string          `json:"missing"`            // 还没存的(repo.name 列表;按 yaml 顺序)
	Suggest  string            `json:"suggest_repos_root"` // UI placeholder:用户全局默认 repos root
}

// GetMissingRepoPaths 给 UI 在调 Gen / RunInstall 前预检:
// 返回当前 system 的已存路径 + 仍缺的仓库名。UI 命中 Missing 非空就弹对话框收路径,
// 用户填完调 SaveRepoPaths 落进 ~/.tshoot/config.json,再走正常 Gen / Deploy 路径。
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

// 注:写路径用 App.SaveRepoPathsForSystem(bindings_config.go),这里不再重复定义。

// ListBranchesForRepo 给 wizard Step 4 用 —— 仅列分支,比 AnalyzeV2 / scanSingleRepo
// 轻量得多(不跑 stack 检测,不扫 dependency,不 clone)。
// 主用法:monorepo .gitmodules 拆分后,每个子模块行需要拿到分支列表喂下拉,但又不想
// 跑完整 analyze(已经在父仓 clone 过,子模块只是子目录里另一个 git repo)。空路径
// 或非 git 仓库返回空数组(UI 端 fallback 到 text input,跟原行为一致)。
func (a *App) ListBranchesForRepo(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	return analyzer.ListBranches(userconfig.ExpandHome(repoPath))
}

// DetectSubmodulesForRepo 给 wizard Step 4 在用户选完本地路径 / clone 完成后调一次,
// 自动检测仓库是不是 monorepo + 列出每个子模块。命中 0 → 不是 monorepo,UI 静默;
// 命中 N>1 → UI 弹"检测到 N 个子模块,一键拆分"。
// 见 internal/analyzer/monorepo_scan.go 支持的 monorepo 模式。
func (a *App) DetectSubmodulesForRepo(repoPath string) []analyzer.SubmoduleHint {
	if repoPath == "" {
		return nil
	}
	return analyzer.DetectSubmodules(userconfig.ExpandHome(repoPath))
}

// RecommendRoleForRepo 给 wizard Step 4 实时推荐 role(用户改 stack / repo name 时立即调)。
//   stack:   yaml 里的 stack(go/java/node/python/php)
//   name:    仓库名(用于子串匹配 admin/gateway/web 等约定)
//   path:    本机仓库路径(可选)。空时仅做名字匹配 + stack 兜底;非空时进一步看
//            package.json / pom.xml / go.mod / composer.json 等关键依赖。
// 返回 {role, reason}:role 一定是合法枚举,reason 给 UI 显示推荐理由(用户能一眼看出"为啥推这个")。
func (a *App) RecommendRoleForRepo(stack, name, path string) analyzer.RoleHint {
	expanded := path
	if path != "" {
		expanded = userconfig.ExpandHome(path)
	}
	return analyzer.RecommendRole(stack, name, expanded)
}

// GenPreviewFile 一份产物文件的预览条目。Binary=true 时 Content 留空,前端显示
// "二进制文件,无法预览";Truncated=true 时 Content 是截断版本,展示头部即可。
//
// Path 是"用户视角真实部署后的路径",已带 target 前缀:
//   openclaw     openclaw/SOUL.md(实际落到 ~/.openclaw/workspace/<id>/SOUL.md)
//   claude-code  claude-code/agents/<name>.md(实际落到 ~/.claude/agents/<name>.md)
//   cursor       cursor/agents/<name>.md(实际落到 ~/.cursor/agents/<name>.md)
// staging 中的 templates/workspace-template/ 前缀已被剥掉,跟用户最终看到的目录结构对齐。
type GenPreviewFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content,omitempty"`
}

// GenPreviewResult 给前端的产物预览数据。复用 Plan 的 skill 决策,加全文件树。
type GenPreviewResult struct {
	System         string                     `json:"system"`
	ConfigCenter   string                     `json:"config_center"`
	Targets        []string                   `json:"targets"`
	SkillsIncluded []generator.SkillDecision  `json:"skills_included"`
	SkillsSkipped  []generator.SkillDecision  `json:"skills_skipped"`
	Files          []GenPreviewFile           `json:"files"`
}

// GenPreview 按 yaml 的 generation.targets 真跑一遍 generator,把每个 target 的产物
// 都读回来给 UI 预览。跟 cmd/tshoot/gen.go 保持同一逻辑(openclaw staging 复用 / 单
// claude-code 时建临时 staging 等),用户在 EditorPage 看到的就是"真实部署到 AI 平台
// 的内容",不是 staging 中间形态。
//
// payload 控制:单文件 200KB 上限(超出截断),含 NUL 字节视为二进制(只标记不返内容)。
func (a *App) GenPreview(yamlText string) (*GenPreviewResult, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "tshoot-preview-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// 主 outDir(openclaw 落地)+ 兄弟目录(<outDir>-claude-code / <outDir>-cursor)
	outDir := filepath.Join(tmp, "openclaw")
	g := generator.New(cfg, a.templateRoot, outDir)
	g.TshootVersion = version
	g.SystemYAMLSource = []byte(yamlText)

	targets := cfg.Generation.ResolvedTargets()
	hasOpenclaw, hasOther := false, false
	for _, t := range targets {
		switch t {
		case "openclaw":
			hasOpenclaw = true
		case "claude-code", "cursor", "codex":
			hasOther = true
		}
	}
	// 没勾 openclaw 但有 claude-code / cursor:跟 cmd/tshoot/gen.go 同样套路,先建临时
	// staging 让 GenerateClaudeCode/Cursor 复用 workspace 渲染,完事即清。
	if hasOther && !hasOpenclaw {
		stagingDir, err := os.MkdirTemp("", "tshoot-preview-staging-*")
		if err != nil {
			return nil, fmt.Errorf("create staging: %w", err)
		}
		defer os.RemoveAll(stagingDir)
		origOut := g.OutputDir
		g.OutputDir = stagingDir
		if err := g.Generate(); err != nil {
			g.OutputDir = origOut
			return nil, fmt.Errorf("stage workspace: %w", err)
		}
		g.OutputDir = origOut
		g.SharedStaging = stagingDir
	}

	// 按 target 各跑一遍,失败任一直接返错(让用户看到 yaml 里某个 target 渲染哪儿挂了)
	for _, target := range targets {
		switch target {
		case "openclaw":
			if err := g.Generate(); err != nil {
				return nil, fmt.Errorf("gen openclaw: %w", err)
			}
			if hasOther {
				g.SharedStaging = g.OutputDir
			}
		case "claude-code":
			if err := g.GenerateClaudeCode(); err != nil {
				return nil, fmt.Errorf("gen claude-code: %w", err)
			}
		case "cursor":
			if err := g.GenerateCursor(); err != nil {
				return nil, fmt.Errorf("gen cursor: %w", err)
			}
		case "codex":
			if err := g.GenerateCodex(); err != nil {
				return nil, fmt.Errorf("gen codex: %w", err)
			}
		}
	}

	res := &GenPreviewResult{
		System:       cfg.System.ID,
		ConfigCenter: cfg.Infrastructure.PrimaryConfigCenter().Type,
		Targets:      targets,
	}
	if plan, err := g.BuildPlan(""); err == nil && plan != nil {
		res.SkillsIncluded = plan.SkillsIncluded
		res.SkillsSkipped = plan.SkillsSkipped
	}

	// 每个 target 对应一个产物根目录;walk 时给每条文件加 target/ 前缀,
	// openclaw 还要剥掉 staging 里的 "templates/workspace-template/" 前缀,
	// 让用户看到的路径就是 ~/.openclaw/workspace/<id>/ 下的真实结构。
	type targetRoot struct {
		target string
		root   string
		strip  string // 走 staging 的需要剥前缀
	}
	var roots []targetRoot
	for _, t := range targets {
		switch t {
		case "openclaw":
			roots = append(roots, targetRoot{target: "openclaw", root: outDir, strip: filepath.Join("templates", "workspace-template")})
		case "claude-code":
			roots = append(roots, targetRoot{target: "claude-code", root: outDir + "-claude-code"})
		case "cursor":
			roots = append(roots, targetRoot{target: "cursor", root: outDir + "-cursor"})
		case "codex":
			roots = append(roots, targetRoot{target: "codex", root: outDir + "-codex"})
		}
	}

	const previewMax = 200 * 1024
	for _, r := range roots {
		if _, err := os.Stat(r.root); err != nil {
			continue // target 没生成成功就跳
		}
		_ = filepath.Walk(r.root, func(p string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(r.root, p)
			if err != nil {
				return nil
			}
			// openclaw:剥 templates/workspace-template/ 前缀;不在那个子树下的(比如根级
			// scripts/install.sh)就照原样保留,加 target 前缀以区分。
			if r.strip != "" && strings.HasPrefix(rel, r.strip+string(filepath.Separator)) {
				rel = rel[len(r.strip)+1:]
			}
			displayPath := filepath.ToSlash(filepath.Join(r.target, rel))

			f := GenPreviewFile{Path: displayPath, Size: info.Size()}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				res.Files = append(res.Files, f)
				return nil
			}
			for _, b := range data {
				if b == 0 {
					f.Binary = true
					break
				}
			}
			if !f.Binary {
				if int64(len(data)) > previewMax {
					f.Content = string(data[:previewMax])
					f.Truncated = true
				} else {
					f.Content = string(data)
				}
			}
			res.Files = append(res.Files, f)
			return nil
		})
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return res, nil
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
