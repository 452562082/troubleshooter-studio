package generator

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type Context struct {
	*config.SystemConfig
	AgentID string
	// Findings[service][env] -> Finding；env 为空串表示对所有环境生效
	Findings map[string]map[string]analyzer.Finding
	// PriorOverrides[service][env] -> Finding；来自上次生成产物中的人工 verified 行
	PriorOverrides map[string]map[string]analyzer.Finding
	// RepoLocalPaths 仓库名 → 本机绝对路径,生成 repo-path-map.yaml 用。
	// 系统 yaml 不含路径(跨机器不可分享),部署时由 wizard/CLI 注入到产物里。
	// 键必须匹配 cfg.Repos[i].Name。
	RepoLocalPaths map[string]string
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
			SystemConfig:   cfg,
			AgentID:        cfg.System.ID + "-troubleshooter",
			Findings:       map[string]map[string]analyzer.Finding{},
			PriorOverrides: map[string]map[string]analyzer.Finding{},
		},
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
	for _, ra := range report.Repos {
		if len(ra.Findings) == 0 || len(ra.ServiceNames) == 0 {
			continue
		}
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
	// 若 config_center 切换（nacos→apollo 等），不继承 prior overrides，避免字段错配
	if snap.OriginalCenter == "" || snap.OriginalCenter == g.Ctx.Infrastructure.ConfigCenter.Type {
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
	for env, svcMap := range g.Ctx.Infrastructure.ConfigCenter.ServiceMap {
		for svc, rec := range svcMap {
			if g.Ctx.PriorOverrides[svc] == nil {
				g.Ctx.PriorOverrides[svc] = map[string]analyzer.Finding{}
			}
			if _, exists := g.Ctx.PriorOverrides[svc][env]; exists {
				continue // 产物 snapshot 已有同 env 的人工行,尊重它
			}
			g.Ctx.PriorOverrides[svc][env] = analyzer.Finding{
				ConfigCenter: g.Ctx.Infrastructure.ConfigCenter.Type,
				DataID:       rec.DataID,
				Group:        rec.Group,
				NamespaceID:  rec.Namespace,
				AppID:        rec.AppID,
				SourceFile:   "wizard:service_map",
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
		ConfigCenter:        g.Ctx.Infrastructure.ConfigCenter.Type,
		OutputDir:           g.OutputDir,
		SkillsIncludedCount: countSkills(g.OutputDir),
		FilesWritten:        countFiles(g.OutputDir),
		PreservedCount:      len(snap.PreservedFiles),
		PriorOverridesCount: countOverrides(g.Ctx.PriorOverrides),
		AnalyzerHitsCount:   countOverrides(g.Ctx.Findings),
	}
	return nil
}

func countSkills(outputDir string) int {
	skillsDir := filepath.Join(outputDir, "templates", "workspace-template", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func countFiles(outputDir string) int {
	n := 0
	_ = filepath.WalkDir(outputDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// writeClawhubLock 生成 ~/.openclaw 工作区识别用的 .clawhub/lock.json
// 按输出目录下实际存在的 skills/*/ 目录来列举，避免与模板过滤逻辑重复判断
func (g *Generator) writeClawhubLock() error {
	wsRoot := filepath.Join(g.OutputDir, "templates", "workspace-template")
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		// 没有 skills 目录也写一份空 lock，保证 OpenClaw 能识别工作区
		entries = nil
	}
	now := time.Now().UnixMilli()
	type skillEntry struct {
		Version     string `json:"version"`
		InstalledAt int64  `json:"installedAt"`
	}
	skills := map[string]skillEntry{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skills[e.Name()] = skillEntry{Version: "0.0.0-tshoot", InstalledAt: now}
	}
	lock := struct {
		Version int                   `json:"version"`
		Skills  map[string]skillEntry `json:"skills"`
	}{
		Version: 1,
		Skills:  skills,
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(wsRoot, ".clawhub", "lock.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, append(data, '\n'), 0o644)
}

func countOverrides(m map[string]map[string]analyzer.Finding) int {
	n := 0
	for _, byEnv := range m {
		n += len(byEnv)
	}
	return n
}

func (g *Generator) walkAndRender(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			if g.shouldSkipDir(rel) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
		}

		outPath := filepath.Join(dstRoot, rel)
		if strings.HasSuffix(path, ".tmpl") {
			outPath = strings.TrimSuffix(outPath, ".tmpl")
			return g.renderFile(path, outPath)
		}
		return copyFile(path, outPath)
	})
}

func (g *Generator) shouldSkipDir(rel string) bool {
	// skills whitelist filtering
	const skillsPrefix = "skills" + string(filepath.Separator)
	if !strings.HasPrefix(rel, skillsPrefix) {
		return false
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 3)
	if len(parts) < 2 {
		return false
	}
	skillName := parts[1]

	whitelist := g.Ctx.Generation.SkillsWhitelist
	if len(whitelist) > 0 {
		found := false
		for _, w := range whitelist {
			if w == skillName {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}

	// infrastructure-driven: skip runtime-query skills for disabled data stores
	for _, ds := range g.Ctx.Infrastructure.DataStores {
		if !ds.Enabled && skillName == dataStoreSkillName(ds.Type) {
			return true
		}
	}
	// skip config-executor if no config center
	if skillName == "config-executor" {
		t := g.Ctx.Infrastructure.ConfigCenter.Type
		if t == "" || t == "none" {
			return true
		}
	}
	return false
}

func (g *Generator) renderFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tpl, err := template.New(filepath.Base(src)).Funcs(funcMap()).Parse(string(data))
	if err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tpl.Execute(f, g.Ctx); err != nil {
		return fmt.Errorf("render %s: %w", src, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (g *Generator) writeReadme() error {
	ctx := g.Ctx
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s Troubleshooter Agent\n\n", ctx.System.Name)
	fmt.Fprintf(&sb, "由 troubleshooter-studio 生成。系统：%s (`%s`)\n\n", ctx.System.Name, ctx.System.ID)
	if ctx.System.Description != "" {
		fmt.Fprintf(&sb, "> %s\n\n", ctx.System.Description)
	}

	// ── 能干什么 ──
	sb.WriteString("## 这个机器人能干什么\n\n")
	sb.WriteString(readmeSkillsSection(ctx))
	sb.WriteString("\n")

	// ── 你需要准备什么 ──
	sb.WriteString("## 部署前你需要准备\n\n")
	sb.WriteString(readmeCredentialsSection(ctx))
	sb.WriteString("\n")

	// ── 快速开始 ──
	sb.WriteString("## 快速开始\n\n")
	sb.WriteString("Studio 桌面端打开本目录:点 **部署** 即可(原生 Go,跑完会装 workspace + 注入 MCP + 重启 gateway,无 bash 依赖)。\n")
	sb.WriteString("CLI 用户可走同一份逻辑(由 `tshoot` 桌面端的 `RunInstall` binding 调 `agent.InstallNativeOpenclaw`)。\n\n")
	sb.WriteString("凭证持久化在 `scripts/.env`,删它即等同重置(下次部署不再预填)。\n\n")

	// ── FAQ ──
	sb.WriteString("## 常见问题\n\n")
	sb.WriteString(readmeFAQSection(ctx))
	sb.WriteString("\n")

	// ── 升级 / 卸载 ──
	sb.WriteString("## 升级与卸载\n\n")
	sb.WriteString("- **升级**（tshoot 或 system.yaml 改过后）：在 tshoot 仓库里跑 `tshoot upgrade -i system.yaml`，会自动备份旧产物到 `<output_dir>.bak.<ts>/` 再重 gen，最后打印 diff。\n")
	sb.WriteString("- **卸载**:Studio 桌面端 BotsPage 上点对应卡的卸载按钮(走 `agent.UninstallNativeOpenclaw`,移走 workspace + 从 openclaw.json 摘 agent)。\n")
	sb.WriteString("- **回滚**：`mv <output_dir>.bak.<ts> <output_dir>` 然后再点一次部署。\n\n")

	// ── 安装位置 ──
	sb.WriteString("## 安装位置\n\n")
	fmt.Fprintf(&sb, "- Agent 工作区：`~/.openclaw/workspace/%s`\n", ctx.Agent.WorkspaceName)
	sb.WriteString("- OpenClaw 全局配置：`~/.openclaw/openclaw.json`\n")
	sb.WriteString("- 本次凭证（0600）：`scripts/.env`\n")
	if ctx.Infrastructure.ConfigCenter.Type == "apollo" || ctx.Infrastructure.ConfigCenter.Type == "consul" ||
		ctx.Infrastructure.ConfigCenter.Type == "env-vars" || ctx.Infrastructure.ConfigCenter.Type == "kubernetes" {
		fmt.Fprintf(&sb, "- 运行时凭证（0600）：`~/.openclaw/%s-troubleshooter-creds.json`\n", ctx.System.ID)
	}

	return os.WriteFile(filepath.Join(g.OutputDir, "README.md"), []byte(sb.String()), 0o644)
}

// readmeSkillsSection 从 skills_whitelist + infra 推断出机器人的能力清单
func readmeSkillsSection(ctx *Context) string {
	var sb strings.Builder
	if len(ctx.Generation.SkillsWhitelist) == 0 {
		sb.WriteString("（未列白名单，所有 skill 默认启用）\n")
		return sb.String()
	}
	skillDesc := map[string]string{
		"routing":                  "根据 service + env 定位代码路径、域名、分支、配置标识",
		"config-executor":          "从配置中心读取/对比配置值，支持历史版本",
		"redis-runtime-query":      "查询 Redis key / TTL / 值（只读）",
		"mongodb-runtime-query":    "MongoDB query / aggregate / count（只读）",
		"es-runtime-query":         "Elasticsearch _search（只读）",
		"mysql-runtime-query":      "MySQL 只读 SELECT（数据一致性 / 慢查询）",
		"postgresql-runtime-query": "PostgreSQL 只读查询（pg_stat / 连接数 / 表大小）",
		"kafka-runtime-query":      "Kafka topic / 消费积压 / 死信",
		"rocketmq-runtime-query":   "RocketMQ topic / consumer / 积压 / DLQ",
		"rabbitmq-runtime-query":   "RabbitMQ queue / exchange / 消息数",
		"clickhouse-runtime-query": "ClickHouse 只读 OLAP 查询 / 分区 / 慢查询日志",
		"diagram-generator":        "生成架构图 / 流程图 / 链路拓扑",
		"tracing-query":            "Jaeger trace_id → span 树 / 耗时 TOP / 错误 span",
		"tempo-query":              "Tempo trace 查询（Grafana 生态）",
		"skywalking-query":         "SkyWalking APM：服务拓扑 + trace + 慢端点",
		"elk-log-query":            "ELK 日志搜索（ES _search + Kibana）",
	}
	for _, s := range ctx.Generation.SkillsWhitelist {
		desc, ok := skillDesc[s]
		if !ok {
			desc = "（自定义 skill，见 templates/workspace-template/skills/" + s + "/SKILL.md）"
		}
		fmt.Fprintf(&sb, "- **%s** — %s\n", s, desc)
	}
	return sb.String()
}

// readmeCredentialsSection 按 infrastructure 给出"必备凭证清单"
func readmeCredentialsSection(ctx *Context) string {
	var sb strings.Builder

	cc := ctx.Infrastructure.ConfigCenter.Type
	hasCreds := (cc != "" && cc != "none") ||
		ctx.Infrastructure.Observability.Grafana.Enabled ||
		ctx.Infrastructure.Observability.Jaeger.Enabled ||
		ctx.Infrastructure.Observability.ELK.Enabled
	for _, m := range ctx.Infrastructure.Messaging {
		if m.Enabled {
			hasCreds = true
			break
		}
	}
	for _, pt := range ctx.Infrastructure.ProjectTracking {
		if pt.Enabled {
			hasCreds = true
			break
		}
	}
	if !hasCreds {
		sb.WriteString("本系统未启用任何需要凭证的外部组件（配置中心 / 可观测性 / 消息 / 项目管理），点部署即跑完。\n")
		return sb.String()
	}

	sb.WriteString("Studio 部署时会问下面这些值（按 system.yaml 自动派生），准备好可以加快流程：\n\n")
	perEnvCC := ctx.Infrastructure.ConfigCenter.PerEnvCredentials
	switch cc {
	case "nacos":
		sb.WriteString("- **Nacos**：每个 env 的 `host:port`")
		if perEnvCC {
			sb.WriteString("；每个 env 独立用户名 + 密码\n")
		} else {
			sb.WriteString("；共用一对用户名 + 密码\n")
		}
	case "apollo":
		sb.WriteString("- **Apollo**：每个 env 的 meta URL")
		if perEnvCC {
			sb.WriteString("；每个 env 独立 Open API token\n")
		} else {
			sb.WriteString("；共用 Open API token（若无鉴权可留空）\n")
		}
	case "consul":
		sb.WriteString("- **Consul**：每个 env 的 host")
		if perEnvCC {
			sb.WriteString("；每个 env 独立 ACL token\n")
		} else {
			sb.WriteString("；共用 ACL token（若无 ACL 可留空）\n")
		}
	case "kubernetes":
		sb.WriteString("- **Kubernetes**：每个 env 的 context / namespace / ConfigMap / Secret 名\n")
	case "env-vars":
		sb.WriteString("- **静态连接串**：每个 env 下每个数据层组件的地址（host:port 或 URI）\n")
	}

	if ctx.Infrastructure.Observability.Grafana.Enabled {
		sb.WriteString("- **Grafana**：每个 env 的 URL")
		if ctx.Infrastructure.Observability.Grafana.PerEnvCredentials {
			sb.WriteString("；每个 env 独立用户名 + 密码\n")
		} else {
			sb.WriteString("；共用用户名 + 密码\n")
		}
	}
	if ctx.Infrastructure.Observability.Jaeger.Enabled {
		sb.WriteString("- **Jaeger**：每个 env 的 URL（如 `http://jaeger-xxx:16686`）\n")
	}
	if ctx.Infrastructure.Observability.ELK.Enabled {
		sb.WriteString("- **ELK**：每个 env 的 Kibana URL / ES URL + 共用用户名密码（若无鉴权可留空）\n")
	}
	for _, m := range ctx.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			sb.WriteString("- **Lark**：APP_ID + APP_SECRET\n")
		}
	}
	for _, pt := range ctx.Infrastructure.ProjectTracking {
		if pt.Enabled && pt.Platform == "feishu_project" {
			sb.WriteString("- **Feishu Project**：MCP User Token\n")
		}
	}
	sb.WriteString("\n> 凭证会被写入 `scripts/.env`（权限 0600），以及配置中心的 `~/.openclaw/<agent-id>-creds.json`（若使用 Apollo/Consul/env-vars/K8s）。**两个文件都是本机私有，不要提交到 git**。\n")
	return sb.String()
}

// readmeFAQSection 根据启用的组件拼"常见问题"
func readmeFAQSection(ctx *Context) string {
	var sb strings.Builder
	sb.WriteString("**Q: 机器人回答里说 MCP 连不上 / timeout？**\n")
	sb.WriteString("A: 凭证过期或网络不通。改 `scripts/.env` 里对应 env 的变量,或回 BotsPage 重新填表 → 再点部署(走 InstallNativeOpenclaw,已设的不重问)。\n\n")

	sb.WriteString("**Q: 装完后没看到 agent？**\n")
	sb.WriteString("A: 检查 `~/.openclaw/openclaw.json` 里有没有 `agents.list[...]` 包含 `" + ctx.AgentID + "`；没有就回 BotsPage 重新部署。OpenClaw 客户端可能也需要重启 gateway：`openclaw gateway restart`。\n\n")

	if ctx.Infrastructure.ConfigCenter.Type != "" && ctx.Infrastructure.ConfigCenter.Type != "none" {
		sb.WriteString("**Q: 某个 env 的配置查不到？**\n")
		sb.WriteString("A: (1) 检查 `scripts/.env` 里该 env 的地址/凭证；(2) 对比 `templates/workspace-template/skills/routing/references/config-map.yaml` 里的 namespace/dataId/group 是否对；(3) 在 tshoot 仓库跑 `tshoot doctor -i system.yaml --repos-root <dir>` 看声明与实态是否漂移。\n\n")
	}

	sb.WriteString("**Q: 改了 system.yaml，怎么更新部署？**\n")
	sb.WriteString("A: 在 tshoot 仓库里跑 `tshoot upgrade -i system.yaml` —— 自动备份 + 重 gen + 打印 diff。然后回 BotsPage 重新部署(走 InstallNativeOpenclaw)应用到 OpenClaw。\n\n")

	sb.WriteString("**Q: 想把机器人部署到别的平台（Claude Code / Cursor / Embedded 内嵌对话）？**\n")
	sb.WriteString("A: 在 `system.yaml` 的 `generation.targets` 里加上对应名字再 `tshoot gen`，会生成 `<output_dir>-claude-code/` / `-cursor/` 兄弟目录；Studio 部署 → 自动装到 `~/.claude/agents/` 或 `~/.cursor/agents/`(走 agent.InstallNative,无 bash)。\n")
	return sb.String()
}
