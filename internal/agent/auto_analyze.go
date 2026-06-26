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
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// autoAnalyzeCacheTTL:一次部署 wizard 里 4 个 target(openclaw/claude-code/cursor/codex)
// 串行调 ImportAndDeploy,每个内部各跑一次 RunAutoAnalyze,实测每个 ~4-5s = 总共 ~20s
// 重复扫同一份 repo —— 纯浪费。加 process-level cache 按 (system.id + sorted repo paths)
// key,首次跑写 cache,后续 target 命中直接复用 result。5min TTL 覆盖一次完整部署流程(<2min)
// 且让用户改 yaml 后重新部署时强制重扫(避免 stale findings)。
const autoAnalyzeCacheTTL = 5 * time.Minute

// autoAnalyzeCache 是 RunAutoAnalyze 的 process-level 缓存。key 见 autoAnalyzeCacheKey。
// 短 TTL,无 LRU 上限:一次 wizard 最多 ~1-2 个 system.id,内存可忽略。重启 .app 自动清。
var (
	autoAnalyzeCacheMu sync.Mutex
	autoAnalyzeCache   = make(map[string]*autoAnalyzeCacheEntry)
)

type autoAnalyzeCacheEntry struct {
	result *analyzerpipe.Result
	at     time.Time
}

// autoAnalyzeCacheKey 构造 cache key:system.id + 按 repo name 排序后的 path 列表。
// 用 \x1f (US, Unit Separator) 当字段分隔符——路径里不可能出现,避免误命中。
func autoAnalyzeCacheKey(systemID string, expanded map[string]string) string {
	names := make([]string, 0, len(expanded))
	for n := range expanded {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(systemID)
	for _, n := range names {
		b.WriteByte('\x1f')
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(expanded[n])
	}
	return b.String()
}

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
	// Ctx 是给 analyzerpipe.Run 用的取消上下文(可选,nil 时用 background)。
	// 注:Go 惯例 ctx 作为第一个参数而非 struct 字段,但 RunAutoAnalyzeOptions 现有 4+
	// 调用方都用 opts struct,把 ctx 塞这里避免改全部签名 — 这是 minimal-invasive 妥协。
	Ctx context.Context
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
	// cache 命中检查:同一 wizard 部署多 target 时,后 3 个 target 复用首次扫的结果
	cacheKey := autoAnalyzeCacheKey(opts.Cfg.System.ID, expanded)
	autoAnalyzeCacheMu.Lock()
	if entry, ok := autoAnalyzeCache[cacheKey]; ok && time.Since(entry.at) < autoAnalyzeCacheTTL {
		hit := entry
		autoAnalyzeCacheMu.Unlock()
		if opts.OnLog != nil {
			opts.OnLog(fmt.Sprintf("[info] auto-analyze cache 命中(%ds 前的结果),复用 dependency / schema findings",
				int(time.Since(hit.at).Seconds())))
		}
		return hit.result, nil
	}
	autoAnalyzeCacheMu.Unlock()

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
	// 内部 ctx:caller ctx(opts.Ctx)+ 我们自己的 60s timeout 包一层。
	// caller 取消(用户点 stop / 桌面关 app)或本地 60s 时间到,都让 analyzerpipe.Run
	// 从 step 之间退出,不再"goroutine 后台跑到死"。
	parentCtx := opts.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	runCtx, cancelRun := context.WithTimeout(parentCtx, autoAnalyzeTimeout)
	defer cancelRun()
	go func() {
		r, err := analyzerpipe.Run(runCtx, opts.Cfg, analyzerpipe.Options{
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
		// 成功才 cache(失败 / nil result 不缓存,下次 retry 有机会)
		if res.err == nil && res.r != nil {
			autoAnalyzeCacheMu.Lock()
			autoAnalyzeCache[cacheKey] = &autoAnalyzeCacheEntry{result: res.r, at: time.Now()}
			autoAnalyzeCacheMu.Unlock()
		}
		return res.r, res.err
	case <-runCtx.Done():
		// 复用 runCtx 的 deadline(= autoAnalyzeTimeout)而非另起 time.After:既不泄露定时器,
		// 又让 caller 传进来的 parentCtx 取消(关 app / 取消部署)能真正中断这里的等待。
		if opts.OnLog != nil {
			opts.OnLog(fmt.Sprintf("[warn] auto-analyze 超过 %ds 未完成或被取消,放弃等待(产物里 service-dependency-map / data-schema-map 字段留空,可后续 BotsPage 重新生成拿到)", int(autoAnalyzeTimeout.Seconds())))
		}
		return nil, nil
	}
}
