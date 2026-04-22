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
	// ReposRoot 是仓库 checkout 的根目录,每个仓库按 repos[].name 在下面成子目录。必填。
	ReposRoot string
	// AutoClone 为 true 时仓库不在 ReposRoot 下会触发 gitclone。CLI 默认关,桌面 app
	// 也默认关(需要用户主动勾选,避免在 GUI 里突然占网络拉 git)。
	AutoClone bool
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
	if opts.ReposRoot == "" {
		return nil, fmt.Errorf("ReposRoot required")
	}
	if opts.AutoClone {
		if err := os.MkdirAll(opts.ReposRoot, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir repos-root: %w", err)
		}
	}

	progress := opts.OnProgress
	if progress == nil {
		progress = func(string) {}
	}

	reg := analyzer.NewRegistry(cfg.Infrastructure.ConfigCenter.Type)
	report := analyzer.Report{
		SchemaVersion: "0.1",
		ConfigCenter:  cfg.Infrastructure.ConfigCenter.Type,
	}
	perRepo := []RepoSummary{}

	for _, repo := range cfg.Repos {
		repoPath := filepath.Join(opts.ReposRoot, repo.Name)
		status := "analyzed"

		if _, err := os.Stat(repoPath); err != nil {
			if !opts.AutoClone {
				perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped", Error: "not-found"})
				progress(fmt.Sprintf("[skip] repo %s not found at %s", repo.Name, repoPath))
				continue
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

		a, err := reg.Get(repo.Stack)
		if err != nil {
			perRepo = append(perRepo, RepoSummary{Name: repo.Name, Status: "skipped", Error: err.Error()})
			progress(fmt.Sprintf("[skip] %s: %v", repo.Name, err))
			continue
		}
		ra, err := a.Analyze(repoPath, repo.Analysis.IncludePaths)
		if err != nil {
			return nil, fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		ra.Name = repo.Name
		report.Repos = append(report.Repos, *ra)
		perRepo = append(perRepo, RepoSummary{
			Name:             repo.Name,
			Status:           status,
			ServiceNameCount: len(ra.ServiceNames),
			FindingCount:     len(ra.Findings),
		})
		progress(fmt.Sprintf("[ok] analyzed %s: %d service_names, %d findings",
			repo.Name, len(ra.ServiceNames), len(ra.Findings)))
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
