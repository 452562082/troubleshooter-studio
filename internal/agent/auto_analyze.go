// auto_analyze.go —— 部署期自动跑 analyzer
//
// 用户走 InitPage Step 4 跑过分析能拿到 findings,但走 BotsPage 导入 / Editor 部署
// 路径 yaml 没带 findings,产物里:
//   - service-dependency-map.upstream / downstream 全是空
//   - data-schema-map.tables / collections / cache_prefixes 全是空
//
// 这两条原本要让用户手填,体验差。
//
// 解决方案:部署阶段拿"用户在 wizard / 之前部署时存过的本地仓库路径"
// (~/.tshoot/config.json -> repo_paths_by_system),自动跑一遍 analyzer,
// 把 findings 折进 generator,产物里两份 map 自动填齐。
//
// 用户没存过路径时:
//   - UI 调 CheckMissingRepoPaths() 拿 missing 列表
//   - 弹 prompt 让用户填(类似 InitPage Step 4),保存后再跑 RunAutoAnalyze
package agent

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// autoAnalyzeTimeout 是 auto-analyze 在部署期单次扫所有 repo 的硬上限。analyzer 内部
// 没 ctx 不可 cancel,这里走 channel + select:超时仍让上层主流程返回,goroutine 在后台
// 自然死透(进程退出时 GC)。
//
// 60s 选取:已知主流大仓 dependency_scan + schema_scan 实测 5-30s;极大 monorepo 偶尔
// 50s+。再多就是用户体感"卡死"——宁可产物两份 map 留空(可后续 BotsPage 重 gen 拿到)
// 也不让部署 UI 永远转。
const autoAnalyzeTimeout = 60 * time.Second

// CheckMissingRepoPaths 返回 yaml 里"应该 / 可以扫"但目前 savedPaths 没覆盖的 repo 名。
//   - savedPaths 通常来自 userconfig.GetRepoPathsForSystem(cfg.System.ID)
//   - "应该扫"的判断:repo.Stack 非空(有 stack 才能扫;不含 docs / 配置仓库)
//
// UI 用法:返回非空时弹路径选择对话框,跟 InitPage Step 4 同款体验。
func CheckMissingRepoPaths(cfg *config.SystemConfig, savedPaths map[string]string) []string {
	if cfg == nil {
		return nil
	}
	var missing []string
	for _, r := range cfg.Repos {
		if r.Stack == "" {
			continue // 没 stack 的仓库扫不出有用东西,不要求路径
		}
		if p, ok := savedPaths[r.Name]; ok && p != "" {
			continue
		}
		missing = append(missing, r.Name)
	}
	return missing
}

// RunAutoAnalyzeOptions 给 RunAutoAnalyze 的入参。
type RunAutoAnalyzeOptions struct {
	Cfg       *config.SystemConfig
	RepoPaths map[string]string // repo.name → 本机绝对路径
	OnLog     func(string)      // 流式日志(可选)
}

// RunAutoAnalyze 跑一遍 analyzerpipe.Run。返回 Result 给调用方塞进 generator
// (g.LoadAnalysisResult 或写到临时 analysis.json 再 LoadAnalysis)。
//
// 路径全空时返回 (nil, nil),调用方按"无 findings"继续走;有任一路径就跑 ——
// analyzer 内部处理 partial(没路径的仓库 skip)。
//
// 60s timeout:analyzer 没 ctx 不可 cancel,这里走 channel + select。超时返回 (nil, nil)
// 让主流程当"无 findings"继续(产物里两份 map 留空,可后续 BotsPage 重 gen 拿到);
// 不阻断 install 主流程。详见 autoAnalyzeTimeout 注释。
func RunAutoAnalyze(opts RunAutoAnalyzeOptions) (*analyzerpipe.Result, error) {
	if opts.Cfg == nil {
		return nil, nil
	}
	expanded := make(map[string]string, len(opts.RepoPaths))
	for k, v := range opts.RepoPaths {
		if v == "" {
			continue
		}
		expanded[k] = userconfig.ExpandHome(v)
	}
	if len(expanded) == 0 {
		return nil, nil
	}
	// 推导一个 ReposRoot(用第一条路径的父目录),让 analyzer 接受 partial 路径
	// (其它仓库走 ReposRoot+Name 默认拼法,虽然多半不存在但 analyzer 会 skip 不中断)
	var reposRoot string
	for _, p := range expanded {
		reposRoot = filepath.Dir(p)
		break
	}
	if opts.OnLog != nil {
		opts.OnLog(fmt.Sprintf("[info] auto-analyze 开始扫 %d 个 repo 的依赖 + schema(上限 %ds,超时自动放弃)", len(expanded), int(autoAnalyzeTimeout.Seconds())))
	}
	type chRes struct {
		r   *analyzerpipe.Result
		err error
	}
	ch := make(chan chRes, 1)
	go func() {
		r, err := analyzerpipe.Run(opts.Cfg, analyzerpipe.Options{
			ReposRoot:  reposRoot,
			RepoPaths:  expanded,
			AutoClone:  false,
			OnProgress: opts.OnLog,
		})
		ch <- chRes{r, err}
	}()
	select {
	case res := <-ch:
		if opts.OnLog != nil {
			if res.err != nil {
				opts.OnLog(fmt.Sprintf("[warn] auto-analyze 失败: %v(产物里两份 map 留空,可后续重 gen)", res.err))
			} else {
				opts.OnLog("[ok] auto-analyze 完成")
			}
		}
		return res.r, res.err
	case <-time.After(autoAnalyzeTimeout):
		if opts.OnLog != nil {
			opts.OnLog(fmt.Sprintf("[warn] auto-analyze 超过 %ds 未完成,放弃等待(产物里 service-dependency-map / data-schema-map 字段留空,可后续 BotsPage 重新生成拿到)", int(autoAnalyzeTimeout.Seconds())))
		}
		return nil, nil
	}
}
