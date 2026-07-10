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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
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
// 重复扫同一份 repo —— 纯浪费。加 process-level cache,按完整配置摘要 + topology schema
// + sorted repo path/HEAD 生成 key,首次跑写 cache,后续 target 命中直接复用 result。
// 5min TTL 覆盖一次完整部署流程(<2min),配置或 checkout 变化则立即换 key 重扫。
const autoAnalyzeCacheTTL = 5 * time.Minute

// autoAnalyzeCache 是 RunAutoAnalyze 的 process-level 缓存。key 见 autoAnalyzeCacheKey;
// autoAnalyzeFlights 合并并发 miss,二者共用同一把短临界区 mutex,扫描期间不持锁。
// 短 TTL,每次 lookup/publication 顺手清过期项;一次 wizard 最多 ~1-2 个 system.id。
var (
	autoAnalyzeCacheMu sync.Mutex
	autoAnalyzeCache   = make(map[string]*autoAnalyzeCacheEntry)
	autoAnalyzeFlights = make(map[string]*autoAnalyzeFlight)
)

type autoAnalyzeCacheEntry struct {
	result *analyzerpipe.Result
	at     time.Time
}

type autoAnalyzeFlight struct {
	done      chan struct{}
	result    *analyzerpipe.Result
	err       error
	cancel    context.CancelFunc
	waiters   int
	abandoned bool
}

type autoAnalyzeCacheRepository struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Head string `json:"head"`
}

type autoAnalyzeCacheMaterial struct {
	SystemID      string                       `json:"system_id"`
	ConfigDigest  string                       `json:"config_digest"`
	SchemaVersion string                       `json:"schema_version"`
	Repositories  []autoAnalyzeCacheRepository `json:"repositories"`
}

// autoAnalyzeCacheKey hashes canonical JSON containing the system/config digest,
// topology contract version, and each sorted repository path plus HEAD state.
// Hashing both config and final material invalidates topology-relevant aliases,
// roles, domains, and overrides without exposing configuration values or secrets.
func autoAnalyzeCacheKey(cfg *config.SystemConfig, expanded map[string]string) string {
	material := autoAnalyzeCacheMaterialFor(cfg, expanded)
	encoded, err := json.Marshal(material)
	if err != nil {
		encoded = []byte(fmt.Sprintf("cache-material-json-error:%T:%v", material, err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func autoAnalyzeCacheMaterialFor(cfg *config.SystemConfig, expanded map[string]string) autoAnalyzeCacheMaterial {
	names := make([]string, 0, len(expanded))
	for n := range expanded {
		names = append(names, n)
	}
	sort.Strings(names)
	material := autoAnalyzeCacheMaterial{
		ConfigDigest:  canonicalJSONDigest(cfg),
		SchemaVersion: topology.SchemaVersion,
		Repositories:  make([]autoAnalyzeCacheRepository, 0, len(names)),
	}
	if cfg != nil {
		material.SystemID = cfg.System.ID
	}
	for _, n := range names {
		material.Repositories = append(material.Repositories, autoAnalyzeCacheRepository{
			Name: n,
			Path: expanded[n],
			Head: autoAnalyzeRepoHead(expanded[n]),
		})
	}
	return material
}

func canonicalJSONDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("config-json-error:%T:%v", value, err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func autoAnalyzeRepoHead(path string) string {
	if _, err := os.Stat(path); err != nil {
		return "missing"
	}
	output, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return "not-git"
	}
	head := strings.TrimSpace(string(output))
	if head == "" {
		return "not-git"
	}
	return head
}

// pruneExpiredAutoAnalyzeCacheLocked removes stale entries while the caller
// already holds autoAnalyzeCacheMu. Entries exactly at the TTL are expired.
func pruneExpiredAutoAnalyzeCacheLocked(now time.Time) {
	for key, entry := range autoAnalyzeCache {
		if entry == nil || now.Sub(entry.at) >= autoAnalyzeCacheTTL {
			delete(autoAnalyzeCache, key)
		}
	}
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
	cacheKey := autoAnalyzeCacheKey(opts.Cfg, expanded)
	callerCtx := opts.Ctx
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	autoAnalyzeCacheMu.Lock()
	pruneExpiredAutoAnalyzeCacheLocked(time.Now())
	if entry, ok := autoAnalyzeCache[cacheKey]; ok {
		hit := entry
		autoAnalyzeCacheMu.Unlock()
		if opts.OnLog != nil {
			opts.OnLog(fmt.Sprintf("[info] auto-analyze cache 命中(%ds 前的结果),复用 dependency / schema / topology findings",
				int(time.Since(hit.at).Seconds())))
		}
		return hit.result, nil
	}
	if flight, ok := autoAnalyzeFlights[cacheKey]; ok {
		flight.waiters++
		autoAnalyzeCacheMu.Unlock()
		if opts.OnLog != nil {
			opts.OnLog("[info] auto-analyze 同 key 扫描进行中,等待共享结果")
		}
		return waitAutoAnalyzeFlight(callerCtx, cacheKey, flight, opts.OnLog)
	}
	sharedCtx, cancelShared := context.WithTimeout(context.Background(), autoAnalyzeTimeout)
	flight := &autoAnalyzeFlight{done: make(chan struct{}), cancel: cancelShared, waiters: 1}
	autoAnalyzeFlights[cacheKey] = flight
	autoAnalyzeCacheMu.Unlock()

	scanOpts := opts
	scanOpts.Ctx = sharedCtx
	go func() {
		result, err := runAutoAnalyzeScan(scanOpts, expanded)

		autoAnalyzeCacheMu.Lock()
		flight.result = result
		flight.err = err
		if current, ok := autoAnalyzeFlights[cacheKey]; ok && current == flight && !flight.abandoned {
			now := time.Now()
			pruneExpiredAutoAnalyzeCacheLocked(now)
			if err == nil && result != nil {
				autoAnalyzeCache[cacheKey] = &autoAnalyzeCacheEntry{result: result, at: now}
			}
			delete(autoAnalyzeFlights, cacheKey)
		}
		close(flight.done)
		flight.cancel()
		autoAnalyzeCacheMu.Unlock()
	}()

	return waitAutoAnalyzeFlight(callerCtx, cacheKey, flight, opts.OnLog)
}

func waitAutoAnalyzeFlight(ctx context.Context, cacheKey string, flight *autoAnalyzeFlight, onLog func(string)) (*analyzerpipe.Result, error) {
	select {
	case <-flight.done:
		return flight.result, flight.err
	case <-ctx.Done():
		autoAnalyzeCacheMu.Lock()
		if current, ok := autoAnalyzeFlights[cacheKey]; ok && current == flight {
			flight.waiters--
			if flight.waiters == 0 {
				flight.abandoned = true
				delete(autoAnalyzeFlights, cacheKey)
				flight.cancel()
			}
		}
		autoAnalyzeCacheMu.Unlock()
		if onLog != nil {
			onLog("[warn] auto-analyze 调用已取消,放弃等待共享扫描结果")
		}
		return nil, nil
	}
}

func runAutoAnalyzeScan(opts RunAutoAnalyzeOptions, expanded map[string]string) (*analyzerpipe.Result, error) {
	if opts.OnLog != nil {
		opts.OnLog(fmt.Sprintf("[info] auto-analyze 开始扫 %d 个 repo 的依赖 + schema(上限 %ds,超时自动放弃)", len(expanded), int(autoAnalyzeTimeout.Seconds())))
	}
	type chRes struct {
		r   *analyzerpipe.Result
		err error
	}
	ch := make(chan chRes, 1)
	runCtx := opts.Ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	go func() {
		r, err := analyzerpipe.Run(runCtx, opts.Cfg, analyzerpipe.Options{
			ReposRoot:  "",
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
	case <-runCtx.Done():
		// 复用 shared runCtx 的 deadline(= autoAnalyzeTimeout)而非另起 time.After,
		// 既不泄露定时器,又能在所有 caller 都取消时中断底层扫描。
		if opts.OnLog != nil {
			opts.OnLog(fmt.Sprintf("[warn] auto-analyze 超过 %ds 未完成或被取消,放弃等待(产物里 service-dependency-map / data-schema-map 字段留空,可后续 BotsPage 重新生成拿到)", int(autoAnalyzeTimeout.Seconds())))
		}
		return nil, nil
	}
}
