// generator.go —— 类型定义 + Generate 主流程。
//
// 子文件分工:
//   render_walk.go    walkAndRender / shouldSkipDir / renderFile / copyFile
//   readme.go         writeReadme + 三个 section helpers
//   clawhub_lock.go   writeClawhubLock + count* 摘要计数
//   funcs.go          模板 funcMap
//   tshoot_meta.go    writeTshootMeta(产物锚点 tshoot.json)
//   plan.go / preserve.go / diff.go  二次生成的计划 / 保留 / diff
//   claude_code.go / cursor.go / codex.go  各 target 产物适配
package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type Context struct {
	*config.SystemConfig
	AgentID string
	// MCPKeyPrefix 用于 MCP server key 的前缀(短形式,通常 = system.id),跟
	// install_native_mcp.go::buildMCPServersForCfg 用的同一前缀对齐 ——
	// config-map.yaml 模板里 mcp_server 字段拼出来的名字必须跟实际 mcp.json/config.toml
	// 里注册的 server key 一致,机器人才调得到对应 MCP。
	MCPKeyPrefix string
	// Findings[service][env] -> Finding；env 为空串表示对所有环境生效
	Findings map[string]map[string]analyzer.Finding
	// PriorOverrides[service][env] -> Finding；来自上次生成产物中的人工 verified 行
	PriorOverrides map[string]map[string]analyzer.Finding
	// RepoLocalPaths 仓库名 → 本机绝对路径,生成 repo-path-map.yaml 用。
	// 系统 yaml 不含路径(跨机器不可分享),部署时由 wizard/CLI 注入到产物里。
	// 键必须匹配 cfg.Repos[i].Name。
	RepoLocalPaths map[string]string
	// DownstreamCallsByRepo[repoName] / DataStoreUsagesByRepo[repoName]
	// dependency_scan.go 扫到的"本仓库的下游调用 + 数据层使用"种子值,
	// 给 service-dependency-map.yaml.tmpl 渲染时自动填 downstream/data_stores 列表用。
	// 键必须匹配 cfg.Repos[i].Name(跟 RepoLocalPaths 一样);没扫到的 repo 缺键即可。
	DownstreamCallsByRepo map[string][]analyzer.DownstreamCall
	DataStoreUsagesByRepo map[string][]analyzer.DataStoreUsage
	// SchemaTablesByRepo 跟上面平行,给 data-schema-map.yaml 模板渲染用。
	// agent 排障时 "order #xxx" → 直接知道查哪张表/collection/redis prefix。
	SchemaTablesByRepo map[string][]analyzer.SchemaTable
}

type Generator struct {
	TemplateRoot string
	OutputDir    string
	Ctx          *Context
	Summary      *GenSummary
	// SharedStaging 当非空时，GenerateClaudeCode/Cursor/Embedded 跳过各自
	// 内部的 workspace 临时渲染，直接复用该目录下已渲染好的
	// templates/workspace-template/ 作为 wsRoot。调用方（cmd/tshoot/main.go）
	// 负责在多 target 生成时先跑一次 Generate() 到此目录、或把 openclaw 产物挪过来。
	SharedStaging string

	// TshootVersion 写入产物 tshoot.json 的 tshoot_version 字段
	// （由 cmd/tshoot / cmd/tshoot-desktop 的 main.version 注入）。留空即空字符串。
	TshootVersion string

	// SystemYAMLSource 是原始 system.yaml 内容（含注释），塞进 tshoot.json.system_yaml。
	// 这是 discover / agent apply 的"真源"：二次修改时从这里读。
	// 调用方应在 Generate 前设好；为空时 tshoot.json 里该字段为空串。
	SystemYAMLSource []byte

	// RepoLocalPaths 仓库名 → 本机绝对路径,部署时由调用方(桌面端向导 / CLI --repo-path
	// 参数)填进来。system.yaml 故意不含此信息(路径跟机器绑定,不可分享),但部署后
	// 的机器人需要知道"仓库在我这台机器上 checkout 到哪了"才能做代码分析 ——
	// 所以把这份数据烤进 routing skill 的 references/repo-path-map.yaml(只存在于产物里,
	// 不进 system.yaml)。键必须匹配 cfg.Repos[i].Name;未匹配的仓库不写 local_path 行。
	// 为空 map 时模板会生成 "# 无本地路径"的占位 yaml 提示用户补齐。
	RepoLocalPaths map[string]string
}

// GenSummary 描述一次 Generate 的实际产出结构，便于 CLI 以 text / json 等格式展示
type GenSummary struct {
	System              string `json:"system"`
	ConfigCenter        string `json:"config_center"`
	OutputDir           string `json:"output_dir"`
	SkillsIncludedCount int    `json:"skills_included_count"`
	FilesWritten        int    `json:"files_written"`
	PreservedCount      int    `json:"preserved_count"`
	PriorOverridesCount int    `json:"prior_overrides_count"`
	AnalyzerHitsCount   int    `json:"analyzer_hits_count"`
}

func New(cfg *config.SystemConfig, templateRoot, outputDir string) *Generator {
	return &Generator{
		TemplateRoot: templateRoot,
		OutputDir:    outputDir,
		Ctx: &Context{
			SystemConfig:          cfg,
			AgentID:               cfg.ResolveID(),
			MCPKeyPrefix:          cfg.MCPKeyPrefix(),
			Findings:              map[string]map[string]analyzer.Finding{},
			PriorOverrides:        map[string]map[string]analyzer.Finding{},
			DownstreamCallsByRepo: map[string][]analyzer.DownstreamCall{},
			DataStoreUsagesByRepo: map[string][]analyzer.DataStoreUsage{},
			SchemaTablesByRepo:    map[string][]analyzer.SchemaTable{},
		},
	}
}

// LoadAnalysisReport 合并已经在内存里的 analyzer.Report 到 Context。
// 跟 LoadAnalysis 区别:跳过磁盘 IO(部署期 auto-analyze 拿的 Result 直接传进来,
// 不必先写 analysis.json 再读)。LoadAnalysis 走文件路径时复用本函数。
func (g *Generator) LoadAnalysisReport(report analyzer.Report) {
	for _, ra := range report.Repos {
		// findings → ctx(老路径,只填有 findings 的)
		if len(ra.Findings) > 0 && len(ra.ServiceNames) > 0 {
			for _, svc := range ra.ServiceNames {
				if g.Ctx.Findings[svc] == nil {
					g.Ctx.Findings[svc] = map[string]analyzer.Finding{}
				}
				for _, nf := range ra.Findings {
					env := nf.EnvProfile
					if _, dup := g.Ctx.Findings[svc][env]; !dup {
						g.Ctx.Findings[svc][env] = nf
					}
				}
			}
		}
		// dependency scan → ctx,**给所有 repo**(没扫到的就空,模板侧 fallback)
		if ra.Name != "" {
			if len(ra.DownstreamCalls) > 0 {
				g.Ctx.DownstreamCallsByRepo[ra.Name] = ra.DownstreamCalls
			}
			if len(ra.DataStoreUsages) > 0 {
				g.Ctx.DataStoreUsagesByRepo[ra.Name] = ra.DataStoreUsages
			}
			if len(ra.SchemaTables) > 0 {
				g.Ctx.SchemaTablesByRepo[ra.Name] = ra.SchemaTables
			}
		}
	}
}

// LoadAnalysis 合并 analyzer 产出的 findings 到 Context
func (g *Generator) LoadAnalysis(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read analysis: %w", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("parse analysis: %w", err)
	}
	g.LoadAnalysisReport(report)
	return nil
}

// resolveWorkspace 返回一个可读的 workspace-template 目录路径。
// 若 SharedStaging 已设置则直接返回它下面的 workspace-template；
// 否则创建临时目录、跑一次 Generate() 生成完整产物，并返回临时 wsRoot
// 以及 cleanup 回调（调用方 defer 调用）。
func (g *Generator) resolveWorkspace() (wsRoot string, cleanup func(), err error) {
	if g.SharedStaging != "" {
		wsRoot = filepath.Join(g.SharedStaging, "templates", "workspace-template")
		if _, statErr := os.Stat(wsRoot); statErr != nil {
			return "", func() {}, fmt.Errorf("shared staging missing workspace: %w", statErr)
		}
		return wsRoot, func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "tshoot-stage-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }

	origOut := g.OutputDir
	g.OutputDir = tmpDir
	if genErr := g.Generate(); genErr != nil {
		g.OutputDir = origOut
		cleanup()
		return "", func() {}, fmt.Errorf("render templates: %w", genErr)
	}
	g.OutputDir = origOut

	return filepath.Join(tmpDir, "templates", "workspace-template"), cleanup, nil
}

func (g *Generator) Generate() error {
	// 把 Generator 上的 RepoLocalPaths 同步到 Context(模板只从 Ctx 取;
	// 保持 Generator 字段为"外部可配",Context 为"模板可见"的分层)
	g.Ctx.RepoLocalPaths = g.RepoLocalPaths

	// 1) 从现有产物提取 preserved 文件内容 + config-map 人工行
	snap, err := SnapshotExisting(g.OutputDir, g.Ctx.Generation.PreserveOnRegenerate)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	// 若主源 config_center 切换（nacos→apollo 等），不继承 prior overrides，避免字段错配。
	// 多源场景下用主源 type 做这个判断(stage 1 简化;stage 2 可改 per-source 判断)。
	primaryCCType := g.Ctx.Infrastructure.PrimaryConfigCenter().Type
	if snap.OriginalCenter == "" || snap.OriginalCenter == primaryCCType {
		for svc, byEnv := range snap.ConfigOverrides {
			if g.Ctx.PriorOverrides[svc] == nil {
				g.Ctx.PriorOverrides[svc] = map[string]analyzer.Finding{}
			}
			for env, f := range byEnv {
				g.Ctx.PriorOverrides[svc][env] = f
			}
		}
	}
	// 向导 Step 5 里用户通过下拉挑的 "env → service → (namespace, group, data_id)" 映射,
	// 以 prior override 的形式注入(优先级介于 analyzer finding 与 inferred 之间 —
	// 在模板里 findPrior 会命中这一层)。不覆盖 snapshot 里已有的人工行(用户在
	// 产物里手改过就更权威)。
	// 多源:遍历所有源的 ServiceMap;同一服务出现在多源里以最后一个为准(罕见,生成时警告)。
	for _, cc := range g.Ctx.Infrastructure.ConfigCenters {
		for env, svcMap := range cc.ServiceMap {
			for svc, rec := range svcMap {
				if g.Ctx.PriorOverrides[svc] == nil {
					g.Ctx.PriorOverrides[svc] = map[string]analyzer.Finding{}
				}
				if _, exists := g.Ctx.PriorOverrides[svc][env]; exists {
					continue // 产物 snapshot 已有同 env 的人工行,尊重它
				}
				g.Ctx.PriorOverrides[svc][env] = analyzer.Finding{
					ConfigCenter: cc.Type,
					DataID:       rec.DataID,
					Group:        rec.Group,
					NamespaceID:  rec.Namespace,
					AppID:        rec.AppID,
					SourceFile:   "wizard:service_map",
				}
			}
		}
	}

	if err := os.RemoveAll(g.OutputDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(g.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	// workspace → templates/workspace-template/
	wsSrc := filepath.Join(g.TemplateRoot, "workspace")
	wsDst := filepath.Join(g.OutputDir, "templates", "workspace-template")
	if err := g.walkAndRender(wsSrc, wsDst); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}

	// scripts/ 已不再生成 —— install / self-test / uninstall 全部由
	// internal/agent.{InstallNativeOpenclaw, SelfTestOpenclaw, UninstallNativeOpenclaw}
	// 原生 Go 实现,不再用 bash + 嵌入式 Python。staging 目录瘦身。

	if err := g.writeReadme(); err != nil {
		return fmt.Errorf("readme: %w", err)
	}

	// 写 .clawhub/lock.json：列出本次生成的 skills（OpenClaw 工作区元数据）
	if err := g.writeClawhubLock(); err != nil {
		return fmt.Errorf("clawhub lock: %w", err)
	}

	// 还原 preserved 文件（覆盖刚渲染的默认版本）
	if err := snap.Restore(g.OutputDir); err != nil {
		return fmt.Errorf("restore preserved: %w", err)
	}

	// 写 tshoot.json 到真正的 workspace 根（agent.InstallNativeOpenclaw cp 它到
	// ~/.openclaw/workspace/<name>/,被 discover.Scan 反向识别）
	// 注意:刻意不在 staging 根再写一份 —— 否则 discover 会扫到两份,UI 出重复
	// 卡片。ScanInstallPrompts 直接去 templates/workspace-template/tshoot.json 找。
	wsDir := filepath.Join(g.OutputDir, "templates", "workspace-template")
	if err := g.writeTshootMeta(wsDir, "openclaw"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}

	// 填 Summary：供 CLI 按 text/json 渲染；不再直接 Printf
	g.Summary = &GenSummary{
		System:              g.Ctx.System.ID,
		ConfigCenter:        g.Ctx.Infrastructure.PrimaryConfigCenter().Type,
		OutputDir:           g.OutputDir,
		SkillsIncludedCount: countSkills(g.OutputDir),
		FilesWritten:        countFiles(g.OutputDir),
		PreservedCount:      len(snap.PreservedFiles),
		PriorOverridesCount: countOverrides(g.Ctx.PriorOverrides),
		AnalyzerHitsCount:   countOverrides(g.Ctx.Findings),
	}
	return nil
}
