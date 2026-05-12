// readme.go —— 产物根 README.md 生成。
// Generate() 跑完模板后调一次 writeReadme(),按 troubleshooter.yaml 推断出
//   - 启用了哪些 skill(skillsSection)
//   - 部署前要准备哪些凭证(credentialsSection)
//   - 常见 FAQ(按启用组件浮现条目)
//
// 这份 README 是装完之后用户/团队成员第一眼看到的"机器人介绍",决定能不能快速上手。
package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (g *Generator) writeReadme() error {
	ctx := g.Ctx
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s Troubleshooter Agent\n\n", ctx.System.Name)
	fmt.Fprintf(&sb, "由 troubleshooter-studio 生成。系统：%s (`%s`)\n\n", ctx.System.Name, ctx.System.ID)
	if ctx.System.Description != "" {
		fmt.Fprintf(&sb, "> %s\n\n", ctx.System.Description)
	}

	sb.WriteString("## 这个机器人能干什么\n\n")
	sb.WriteString(readmeSkillsSection(ctx))
	sb.WriteString("\n")

	sb.WriteString("## 部署前你需要准备\n\n")
	sb.WriteString(readmeCredentialsSection(ctx))
	sb.WriteString("\n")

	sb.WriteString("## 快速开始\n\n")
	sb.WriteString("Studio 桌面端打开本目录:点 **部署** 即可(原生 Go,跑完会装 workspace + 注入 MCP + 重启 gateway,无 bash 依赖)。\n")
	sb.WriteString("CLI 用户可走同一份逻辑(由 `tshoot` 桌面端的 `RunInstall` binding 调 `agent.InstallNativeOpenclaw`)。\n\n")
	sb.WriteString("凭证持久化在 `scripts/.env`,删它即等同重置(下次部署不再预填)。\n\n")

	sb.WriteString("## 常见问题\n\n")
	sb.WriteString(readmeFAQSection(ctx))
	sb.WriteString("\n")

	sb.WriteString("## 升级与卸载\n\n")
	sb.WriteString("- **升级**（tshoot 或 troubleshooter.yaml 改过后）：在 tshoot 仓库里跑 `tshoot upgrade -i troubleshooter.yaml`，会自动备份旧产物到 `<output_dir>.bak.<ts>/` 再重 gen，最后打印 diff。\n")
	sb.WriteString("- **卸载**:Studio 桌面端 BotsPage 上点对应卡的卸载按钮(走 `agent.UninstallNativeOpenclaw`,移走 workspace + 从 openclaw.json 摘 agent)。\n")
	sb.WriteString("- **回滚**：`mv <output_dir>.bak.<ts> <output_dir>` 然后再点一次部署。\n\n")

	sb.WriteString("## 安装位置\n\n")
	fmt.Fprintf(&sb, "- Agent 工作区：`~/.openclaw/workspace/%s`\n", ctx.Agent.WorkspaceName)
	sb.WriteString("- OpenClaw 全局配置：`~/.openclaw/openclaw.json`\n")
	sb.WriteString("- 本次凭证（0600）：`scripts/.env`\n")
	if ctx.Infrastructure.PrimaryConfigCenter().Type == "apollo" || ctx.Infrastructure.PrimaryConfigCenter().Type == "consul" ||
		ctx.Infrastructure.PrimaryConfigCenter().Type == "env-vars" || ctx.Infrastructure.PrimaryConfigCenter().Type == "kuboard" {
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

	cc := ctx.Infrastructure.PrimaryConfigCenter().Type
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

	sb.WriteString("Studio 部署时会问下面这些值（按 troubleshooter.yaml 自动派生），准备好可以加快流程：\n\n")
	switch cc {
	case "nacos":
		sb.WriteString("- **Nacos**：每个 env 的 `host:port` + 用户名 + 密码\n")
	case "apollo":
		sb.WriteString("- **Apollo**：每个 env 的 meta URL + Open API token（若无鉴权可留空）\n")
	case "consul":
		sb.WriteString("- **Consul**：每个 env 的 host + ACL token（若无 ACL 可留空）\n")
	case "kuboard":
		sb.WriteString("- **Kuboard**：每个 env 的 Kuboard URL / 用户名 / 密码（cluster / namespace / ConfigMap 由 service_map 决定，无需 install 时输入）\n")
	case "env-vars":
		sb.WriteString("- **静态连接串**：每个 env 下每个数据层组件的地址（host:port 或 URI）\n")
	}

	if ctx.Infrastructure.Observability.Grafana.Enabled {
		sb.WriteString("- **Grafana**：每个 env 的 URL + 用户名 + 密码\n")
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

	if ctx.Infrastructure.PrimaryConfigCenter().Type != "" && ctx.Infrastructure.PrimaryConfigCenter().Type != "none" {
		sb.WriteString("**Q: 某个 env 的配置查不到？**\n")
		sb.WriteString("A: (1) 检查 `scripts/.env` 里该 env 的地址/凭证；(2) 对比 `templates/workspace-template/skills/routing/references/config-map.yaml` 里的 namespace/dataId/group 是否对；(3) 在 tshoot 仓库跑 `tshoot doctor -i troubleshooter.yaml --repos-root <dir>` 看声明与实态是否漂移。\n\n")
	}

	sb.WriteString("**Q: 改了 troubleshooter.yaml，怎么更新部署？**\n")
	sb.WriteString("A: 在 tshoot 仓库里跑 `tshoot upgrade -i troubleshooter.yaml` —— 自动备份 + 重 gen + 打印 diff。然后回 BotsPage 重新部署(走 InstallNativeOpenclaw)应用到 OpenClaw。\n\n")

	sb.WriteString("**Q: 想把机器人部署到别的平台（Claude Code / Cursor / Embedded 内嵌对话）？**\n")
	sb.WriteString("A: 在 `troubleshooter.yaml` 的 `generation.targets` 里加上对应名字再 `tshoot gen`，会生成 `<output_dir>-claude-code/` / `-cursor/` 兄弟目录；Studio 部署 → 自动装到 `~/.claude/agents/` 或 `~/.cursor/agents/`(走 agent.InstallNative,无 bash)。\n")
	return sb.String()
}
