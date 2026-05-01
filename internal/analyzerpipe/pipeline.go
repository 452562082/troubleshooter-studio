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
	"strings"

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
	// Role 是 yaml 里声明的仓库角色(空时按 backend 兜底)。前端卡片用来区分
	// "业务服务找不到 = 红色 not-found 警告"vs"非业务服务找不到 = 静默跳过"。
	Role string `json:"role,omitempty"`
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

	// urlClonedTo 跟踪"同一 URL 已经 clone 到的目录",同 url 多 repo 条目(monorepo
	// 拆分成多 service 时常见)只 clone 一次,后续 repo 直接复用同一 clone root,
	// 各自 sub_path 决定 scan 子目录。key=URL,val=clone 落点(绝对路径)。
	urlClonedTo := map[string]string{}

	for _, repo := range cfg.Repos {
		// RepoName filter:桌面 app 单仓库扫描用,其它仓库直接跳(不进 PerRepo)。
		if opts.RepoName != "" && repo.Name != opts.RepoName {
			continue
		}
		// 优先用 RepoPaths 显式指定的绝对路径(本地已有的仓库),回落到 ReposRoot/Name,
		// 再回落到"同 URL 之前已 clone 到的根"(monorepo 多 service 共用 clone)。
		var repoPath string
		if p, ok := opts.RepoPaths[repo.Name]; ok && p != "" {
			repoPath = p
		} else if cloned, ok := urlClonedTo[repo.URL]; ok && cloned != "" {
			repoPath = cloned
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
		// Fallback:reposRoot 下找不到 → 往下扒一层 <reposRoot>/<X>/<repo.name>。
		// 典型场景:用户 git clone --recurse-submodules <umbrella>,各子模块代码落在
		// <umbrella>/<sub>/,而不是直接落在 reposRoot。为防误中:被找到的目录必须看
		// 着像真代码(.git 或常见 manifest 文件),且**唯一**命中(多个 ambiguous 时退回 skip,
		// 让用户显式提供路径)。
		if pathMissing && opts.ReposRoot != "" {
			if guess := findInSiblingDirs(opts.ReposRoot, repo.Name); guess != "" {
				progress(fmt.Sprintf("[fallback] repo %s 在 %s 找不到,降级到 %s(可能是 git submodule)", repo.Name, repoPath, guess))
				repoPath = guess
				pathMissing = false
			}
		}
		if pathMissing {
			if !opts.AutoClone {
				perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped", Error: "not-found", Role: string(repo.EffectiveRole())})
				progress(fmt.Sprintf("[skip] repo %s not found at %s", repo.Name, repoPath))
				continue
			}
			// autoClone 但 repoPath 空(没指定路径):需要 fallback 到 ReposRoot+Name,
			// 否则 Clone 不知道往哪落盘
			if repoPath == "" {
				if opts.ReposRoot == "" {
					perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped",
						Error: "no path hint and no repos_root to auto-clone into", Role: string(repo.EffectiveRole())})
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
				perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "clone-failed", Error: err.Error(), Role: string(repo.EffectiveRole())})
				progress(fmt.Sprintf("[skip] clone %s failed: %v", repo.Name, err))
				continue
			}
			status = "cloned-then-analyzed"
		}
		// 记下"这个 URL 的 clone root",同 url 后续 repo 条目复用(monorepo 多 service)
		urlClonedTo[repo.URL] = repoPath

		// monorepo:repoPath 当前是 clone root;sub_path 非空时把 scan 路径定到子目录。
		// 这样 stack 探测 / role-hint / dep-scan / schema-scan / Analyze 看到的都是
		// 单服务的代码,不会被仓库根的"多服务混合"信号干扰(多个 main.go / 多个 package.json 等)。
		// 留着 cloneRoot 给修复层(EnsureSubmodules/Branches 仍走 root level git 命令)。
		cloneRoot := repoPath
		if repo.SubPath != "" {
			joined := filepath.Join(cloneRoot, repo.SubPath)
			if _, err := os.Stat(joined); err == nil {
				repoPath = joined
			} else {
				progress(fmt.Sprintf("[warn] repo %s sub_path=%q not found under %s, fallback 到 clone root", repo.Name, repo.SubPath, cloneRoot))
			}
		}

		// 现有 clone 的修复层(兜底老版本留下的不完整状态):
		//   - submodule umbrella(.gitmodules):之前没 recurse 的,服务目录是空的
		//   - single-branch shallow clone:老 Clone() 没带 --no-single-branch,
		//     shallow 下 git 只抓 HEAD 分支,ListBranches 只出 main 一条
		// 两个都 idempotent,已 OK 的秒回,有问题的补齐。失败不中断整体扫描,记 warning。
		// 这两个修复要在 git 仓库根上跑,不是子目录(SubPath 模式下 repoPath 已被改成子目录)
		if subErr := gitclone.EnsureSubmodules(cloneRoot); subErr != nil {
			progress(fmt.Sprintf("[warn] submodule init %s: %v", repo.Name, subErr))
		}
		if brErr := gitclone.EnsureAllBranches(cloneRoot); brErr != nil {
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
				Role:              string(repo.EffectiveRole()),
			})
			progress(fmt.Sprintf("[skip] %s: %v", repo.Name, err))
			continue
		}
		ra, err := a.Analyze(repoPath, repo.Analysis.IncludePaths)
		if err != nil {
			return nil, fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		ra.Name = repo.Name
		// 把"同仓多入口"信号展开成服务名,跟 wizard mergeMonorepoIntoServices /
		// doctor 的 service-drift 检测共享同一份命名规则(`<repo>-<entry>`)。
		// 实现见 internal/analyzer/expand.go::ExpandCmdEntriesAsServiceNames。
		analyzer.ExpandCmdEntriesAsServiceNames(ra, repo.Name, repoPath)
		// 没有 cmd 入口信号:保留 ra.ServiceNames 原值(单仓单服务场景,go.mod 的 module 名就是服务名)
		// 顺带扫"下游调用 + 数据层使用"——给 service-dependency-map.yaml 自动填种子值,
		// 用户拿到种子改比从空白起强 10 倍。即使扫漏 50% 也比 0% 强,保守 best-effort。
		ra.DownstreamCalls, ra.DataStoreUsages = analyzer.ScanDependencies(effectiveStack, repoPath, repo.Analysis.IncludePaths)
		// 扫"业务表 / collection / 缓存 prefix"——给 data-schema-map.yaml 自动填种子值,
		// 多策略叠加(orm_annotation > orm_api_call > sql_literal > file_name)同 (name,kind) 去重保最高优先级。
		ra.SchemaTables = analyzer.ScanSchema(effectiveStack, repoPath, repo.Analysis.IncludePaths)
		// role 推荐:基于仓库名 + 顶层目录 + stack 专属文件(package.json/pom.xml/go.mod/...)。
		// 用户在 yaml 显式 set 了 role 时不覆盖(只是产物里仍记录 hint,UI 给"📍 推荐 vs 实际"对比)。
		hint := analyzer.RecommendRole(effectiveStack, repo.Name, repoPath)
		ra.RoleHint = &hint
		// 非业务服务角色(infra / common-lib / docs / frontend / mobile)本来就不要求"是个 go/maven module";
		// 比如 mongodb-configs(role=infra)只是一堆 mongo 配置文件,扫"go.mod 没找到"是预期行为不是异常。
		// 把这些 stack-missing-manifest 类警告降级,搬到 Notes 里 FYI,不再出现在异常提示里吓人。
		if !repo.RequiresServiceNames() {
			ra.Warnings, ra.Notes = filterStackManifestWarnings(ra.Warnings, ra.Notes)
		}
		report.Repos = append(report.Repos, *ra)
		perRepo = append(perRepo, RepoSummary{
			Name:              repo.Name,
			Status:            status,
			ServiceNameCount:  len(ra.ServiceNames),
			FindingCount:      len(ra.Findings),
			DetectedStack:     detectedStack,
			DetectedFramework: detectedFramework,
			Branches:          branches,
			Role:              string(repo.EffectiveRole()),
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

// filterStackManifestWarnings 把"manifest 文件没找到"类警告从 Warnings 降级到 Notes。
// 给非业务服务角色(infra / common-lib / docs / frontend / mobile)用 —— 它们本来就不必须
// 是个 module(mongodb-configs 是一堆 yaml,docs 是一堆 .md),没 go.mod / package.json 不是 bug。
// 命中条件:子串匹配预设关键字片段(不写完整提示,只匹核心关键词,避免分析器后续改文案就漏掉)。
func filterStackManifestWarnings(warns, notes []string) (newWarns, newNotes []string) {
	keywords := []string{
		"go.mod not found",
		"package.json not found",
		"pom.xml not found",
		"requirements.txt not found",
		"build.gradle not found",
	}
	for _, w := range warns {
		matched := false
		for _, k := range keywords {
			if strings.Contains(w, k) {
				matched = true
				break
			}
		}
		if matched {
			notes = append(notes, "(role 非业务服务,以下不算异常) "+w)
		} else {
			newWarns = append(newWarns, w)
		}
	}
	return newWarns, notes
}

// findInSiblingDirs 在 reposRoot 一层子目录里搜 <reposRoot>/<sibling>/<name>,
// 用于 git submodule 场景:用户 `git clone --recurse-submodules <umbrella>` 后,各子模块
// 实际代码落在 <umbrella>/<sub>/,而不是直接落在 reposRoot 顶层。
//
// 命中条件(防误中):
//   - 路径必须存在且是目录
//   - 目录非空(有任意可见文件 / 子目录)—— 避免捡到 .gitmodules 声明但还没初始化的空目录;
//     不再要求"代码信号"(.git / manifest),因为 infra 类配置仓(mongodb-configs / k8s-yaml /
//     terraform 等)里只有 yaml/json/conf 文件,严要求会误漏。
//   - 整个 reposRoot 范围内**唯一**命中(多个 ambiguous 时返空让上层 skip,避免误用)
//
// reposRoot 子目录里的 `.` 隐藏目录、node_modules、vendor 等通用排除项跳过。
func findInSiblingDirs(reposRoot, name string) string {
	entries, err := os.ReadDir(reposRoot)
	if err != nil {
		return ""
	}
	excluded := map[string]bool{
		"node_modules": true, "vendor": true, "target": true, "build": true, "dist": true,
		".git": true, ".svn": true, ".hg": true,
	}
	// 目录非空判定:有任意可见 entry 即视作"已初始化的真目录"。
	// `.git` / `.gitkeep` 这种隐藏文件不算"内容",避免空 submodule 误命中。
	dirNonEmpty := func(dir string) bool {
		es, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range es {
			if !strings.HasPrefix(e.Name(), ".") {
				return true
			}
		}
		return false
	}
	var matches []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		eName := e.Name()
		if strings.HasPrefix(eName, ".") || excluded[eName] {
			continue
		}
		candidate := filepath.Join(reposRoot, eName, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		if !dirNonEmpty(candidate) {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
