// Package analyzerpipe 把 analyze 子命令的主流水线(遍历 repos / 可选 auto-clone /
// stack-specific scanner / 汇总 findings)抽出来给 CLI (cmd/tshoot) 和桌面 app
// (cmd/tshoot-desktop) 共用。
//
// 实现刻意不做 I/O(不读 yaml 文件 / 不写 analysis.json),调用方自己负责:
//   - CLI: config.Load → Run → json.Marshal → WriteFile + stdout 摘要
//   - 桌面 app: config.LoadFromBytes → Run → 把 Result 送到前端展示
package analyzerpipe

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/gitclone"
)

// Options 控制 Run 行为。
type Options struct {
	// ReposRoot 是仓库 checkout 的根目录,每个仓库按 repos[].name 在下面成子目录。
	// 留空时每个 repo 都得有 RepoPaths[name] 指定自己路径;两者都空直接 skip。
	ReposRoot string
	// RepoPaths 可选:按 repo.name 指定该仓库的绝对路径,覆盖 ReposRoot+Name 默认拼法。
	// 给 InitPage Step 4 的"混合来源"场景用:有的 repo 用户本地已存在(直接指路径),
	// 有的要远程 clone(落到 ReposRoot/Name 下)。填了这里的走这里,否则回落到 ReposRoot+Name。
	RepoPaths map[string]string
	// AutoClone 为 true 时仓库不在 ReposRoot 下会触发 gitclone。CLI 默认关,桌面 app
	// 也默认关(需要用户主动勾选,避免在 GUI 里突然占网络拉 git)。
	AutoClone bool
	// RepoName 非空时只跑匹配的仓库,其它仓库跳过(不 skip 进 PerRepo,直接忽略)。
	// 桌面 app 单仓库 inline 扫描用:用户选完本地目录 / 按"同步"后,只扫这一个仓库,
	// 避免在 yaml 里还没填完的其它 repo 上浪费时间或报错。
	RepoName string
	// CloneBranch 留空则取 repos[i].EnvBranches 里第一个环境对应的分支;都没则用仓库默认分支。
	CloneBranch string
	// OnProgress 如果非 nil,会对每个 repo 的处理节点调一次(clone 开始 / 成功 / 跳过 / 失败)。
	// CLI 的 text 模式可以把它打印到 stderr;桌面 app 可以把它当 toast / 状态行。
	OnProgress func(msg string)
}

// RepoSummary 是 Run 返回每仓库一条的简略状态。前端展示卡片用。
type RepoSummary struct {
	Name string `json:"name"`
	// analyzed / cloned-then-analyzed / skipped / clone-failed
	Status           string `json:"status"`
	ServiceNameCount int    `json:"service_name_count"`
	FindingCount     int    `json:"finding_count"`
	Error            string `json:"error,omitempty"`
	// DetectedStack / DetectedFramework 是两个启发式 detector 对仓库的探测结果。
	// InitPage Step 4 把 stack / framework 做成只读 badge,这两个字段是唯一数据源,
	// 用户只能看不能改(想改就手动编辑 yaml 或重写 repo URL 重新扫描)。
	// 都可能是空字符串 —— 仓库不在本地 / 不是 git / manifest 不认识。
	DetectedStack     string `json:"detected_stack,omitempty"`
	DetectedFramework string `json:"detected_framework,omitempty"`
	// Branches 是仓库的所有分支名(本地 + 远端,去重 + 字母序)。
	// InitPage Step 4 的 env_branches input 用 <datalist> 挂上去,用户点
	// 下拉就能从真实分支里选,不用手敲。仓库不存在 / 不是 git repo 时为空。
	Branches []string `json:"branches,omitempty"`
}

// Result 是 Run 的完整结果:analyzer.Report(可以 Marshal 成 analysis.json) + 每仓库摘要。
type Result struct {
	Report  analyzer.Report `json:"report"`
	PerRepo []RepoSummary   `json:"per_repo"`
}

// Run 执行 analyze 流水线。入参已解析过的 config,不读文件;出参 Result 可序列化。
// 任一 repo 的 a.Analyze 实际失败(不是 skip 类)会中断并返回 error;skip / clone-failed
// 都会变成 RepoSummary 里的 Error 字段,不中断整体。
func Run(cfg *config.SystemConfig, opts Options) (*Result, error) {
	if opts.ReposRoot == "" && len(opts.RepoPaths) == 0 {
		return nil, fmt.Errorf("ReposRoot or RepoPaths required")
	}
	// ReposRoot 非空才 mkdir(单仓库模式下可能只传 RepoPaths,没 ReposRoot)
	if opts.AutoClone && opts.ReposRoot != "" {
		if err := os.MkdirAll(opts.ReposRoot, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir repos-root: %w", err)
		}
	}

	progress := opts.OnProgress
	if progress == nil {
		progress = func(string) {}
	}

	// analyzer 目前是单源视角:多源场景下用 PrimaryConfigCenter() 作为"主源"扫,
	// stage 2 再做 per-repo 按 config_source 路由的多源版本。
	primaryCC := cfg.Infrastructure.PrimaryConfigCenter().Type
	reg := analyzer.NewRegistry(primaryCC)
	report := analyzer.Report{
		SchemaVersion: "0.1",
		ConfigCenter:  primaryCC,
	}
	perRepo := []RepoSummary{}

	for _, repo := range cfg.Repos {
		// RepoName filter:桌面 app 单仓库扫描用,其它仓库直接跳(不进 PerRepo)。
		if opts.RepoName != "" && repo.Name != opts.RepoName {
			continue
		}
		// 优先用 RepoPaths 显式指定的绝对路径(本地已有的仓库),回落到 ReposRoot/Name。
		// 都没有就 skip,走下面 not-found 分支。
		var repoPath string
		if p, ok := opts.RepoPaths[repo.Name]; ok && p != "" {
			repoPath = p
		} else if opts.ReposRoot != "" {
			repoPath = filepath.Join(opts.ReposRoot, repo.Name)
		}
		status := "analyzed"

		// repoPath 空(既没 RepoPaths 也没 ReposRoot)或路径不存在都走 skip/clone 分支
		pathMissing := repoPath == ""
		if !pathMissing {
			if _, err := os.Stat(repoPath); err != nil {
				pathMissing = true
			}
		}
		if pathMissing {
			if !opts.AutoClone {
				perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped", Error: "not-found"})
				progress(fmt.Sprintf("[skip] repo %s not found at %s", repo.Name, repoPath))
				continue
			}
			// autoClone 但 repoPath 空(没指定路径):需要 fallback 到 ReposRoot+Name,
			// 否则 Clone 不知道往哪落盘
			if repoPath == "" {
				if opts.ReposRoot == "" {
					perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped",
						Error: "no path hint and no repos_root to auto-clone into"})
					continue
				}
				repoPath = filepath.Join(opts.ReposRoot, repo.Name)
			}
			branch := pickCloneBranch(opts.CloneBranch, repo, cfg.Environments)
			progress(fmt.Sprintf("[clone] %s → %s (branch=%s, depth=%d)",
				repo.URL, repoPath, orDefault(branch, "<default>"), repo.Analysis.ShallowDepth))
			if err := gitclone.Clone(gitclone.Options{
				URL:    repo.URL,
				Dest:   repoPath,
				Branch: branch,
				Depth:  repo.Analysis.ShallowDepth,
			}); err != nil {
				perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "clone-failed", Error: err.Error()})
				progress(fmt.Sprintf("[skip] clone %s failed: %v", repo.Name, err))
				continue
			}
			status = "cloned-then-analyzed"
		}

		// 现有 clone 的修复层(兜底老版本留下的不完整状态):
		//   - submodule umbrella(.gitmodules):之前没 recurse 的,服务目录是空的
		//   - single-branch shallow clone:老 Clone() 没带 --no-single-branch,
		//     shallow 下 git 只抓 HEAD 分支,ListBranches 只出 main 一条
		// 两个都 idempotent,已 OK 的秒回,有问题的补齐。失败不中断整体扫描,记 warning。
		if subErr := gitclone.EnsureSubmodules(repoPath); subErr != nil {
			progress(fmt.Sprintf("[warn] submodule init %s: %v", repo.Name, subErr))
		}
		if brErr := gitclone.EnsureAllBranches(repoPath); brErr != nil {
			progress(fmt.Sprintf("[warn] expand branches %s: %v", repo.Name, brErr))
		}

		// 三项仓库元信息探测,跟 yaml 里声明值独立;Step 4 UI 以探测值为准。
		// 都轻量(只读根文件 / git for-each-ref),不会显著拖慢 analyze 流程。
		// (role 启发式误报率太高,已下线;repo 角色由用户口头描述给机器人就行,不必入 yaml)
		detectedStack := analyzer.DetectStack(repoPath)
		detectedFramework := analyzer.DetectFramework(repoPath, detectedStack)
		branches := analyzer.ListBranches(repoPath)

		// 跑实际 analyzer 选哪个 stack:
		//   - 单仓库 inline 扫描模式(opts.RepoName != ""):yaml 里的 stack 多半
		//     是前端 generateYAML 的兜底默认 "go",不代表用户意图 —— 所以探测到
		//     的 stack 优先于 yaml。这样扫 api-truss 这种 PHP 仓库时不会被
		//     wizard 的 go 默认拖去跑 goAnalyzer,跑出空 service_names。
		//   - 批量模式(CLI tshoot analyze):yaml 是用户手填或者已经扫过改过
		//     的,yaml 的 stack 代表用户 override,优先级高于 detected。
		//   - 两个都空就没法分析了,标 skipped。
		effectiveStack := repo.Stack
		if opts.RepoName != "" && detectedStack != "" {
			// inline 单仓库扫描:探测到啥就用啥
			effectiveStack = detectedStack
		}
		if effectiveStack == "" {
			effectiveStack = detectedStack
		}

		a, err := reg.Get(effectiveStack)
		if err != nil {
			perRepo = append(perRepo, RepoSummary{
				Name:              repo.Name,
				Status:            "skipped",
				Error:             err.Error(),
				DetectedStack:     detectedStack,
				DetectedFramework: detectedFramework,
				Branches:          branches,
			})
			progress(fmt.Sprintf("[skip] %s: %v", repo.Name, err))
			continue
		}
		ra, err := a.Analyze(repoPath, repo.Analysis.IncludePaths)
		if err != nil {
			return nil, fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		ra.Name = repo.Name
		// 顺带扫"下游调用 + 数据层使用"——给 service-dependency-map.yaml 自动填种子值,
		// 用户拿到种子改比从空白起强 10 倍。即使扫漏 50% 也比 0% 强,保守 best-effort。
		ra.DownstreamCalls, ra.DataStoreUsages = analyzer.ScanDependencies(effectiveStack, repoPath, repo.Analysis.IncludePaths)
		report.Repos = append(report.Repos, *ra)
		perRepo = append(perRepo, RepoSummary{
			Name:              repo.Name,
			Status:            status,
			ServiceNameCount:  len(ra.ServiceNames),
			FindingCount:      len(ra.Findings),
			DetectedStack:     detectedStack,
			DetectedFramework: detectedFramework,
			Branches:          branches,
		})
		progress(fmt.Sprintf("[ok] analyzed %s (stack=%s): %d service_names, %d findings",
			repo.Name, effectiveStack, len(ra.ServiceNames), len(ra.Findings)))
	}

	return &Result{Report: report, PerRepo: perRepo}, nil
}

// pickCloneBranch 选择 auto-clone 时的分支:CLI 显式指定 > env 对应分支 > 仓库默认。
func pickCloneBranch(cliBranch string, repo config.Repo, envs []config.Environment) string {
	if cliBranch != "" {
		return cliBranch
	}
	for _, env := range envs {
		if b, ok := repo.EnvBranches[env.ID]; ok && b != "" {
			return b
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
