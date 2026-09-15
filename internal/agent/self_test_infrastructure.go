// 配置所需的 MCP 清单；三平台共用。
package agent

import "github.com/xiaolong/troubleshooter-studio/internal/config"

func requiredMCPKeys(cfg *config.SystemConfig, agentID string) []string {
	withAgent := func(name string) string {
		if agentID == "" {
			return name
		}
		return agentID + "-" + name
	}
	var out []string
	if cfg.CodeIntelligence.UsesCodeGraph() {
		out = append(out, withAgent("codegraph"))
	}
	// nacos(plan D):自研本地 MCP 脚本,每 source × env 一个实例,跟 buildNacos 镜像。
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range cfg.Environments {
			out = append(out, withAgent(mcpKey("nacos", cc.ID, e.ID)))
		}
	}
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("grafana-"+e.ID))
		}
	}
	// loki MCP 已合并进 grafana MCP(同款 mcp-grafana-npx 二进制本就含 query_loki_*),
	// 不再单独注册 loki-<env>。validate 阶段强制 Loki.Enabled ⇒ Grafana.Enabled,
	// 这里也就没"独立 loki" 期望了。
	// jaeger / elk:2026-05 都从 curl 占位升级到真 MCP(uvx opentelemetry-mcp /
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("jaeger-"+e.ID))
		}
	}
	if cfg.Infrastructure.Observability.ELK.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("elk-"+e.ID))
		}
	}
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			out = append(out, withAgent("lark-openapi"))
			break
		}
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		if !ds.Enabled || !dataStoreRegistersMCP(ds.Type) {
			continue
		}
		for _, e := range cfg.Environments {
			unique := dsEndpointsUnique(ds, e.ID)
			if len(unique) == 0 {
				out = append(out, withAgent(mcpKey(ds.Type, "", e.ID)))
				continue
			}
			single := len(unique) <= 1
			for _, ep := range unique {
				sourceID := ""
				if !single {
					sourceID = ep.sourceID
				}
				out = append(out, withAgent(mcpKey(ds.Type, sourceID, e.ID)))
			}
		}
	}
	// 注:feishu_project 不在 requiredMCPKeys —— 2026-05-15 审计后暂时禁用 mcp 注册
	// (@lark-project/mcp v0.0.1 是字节内部 prototype),buildFeishuProject 仅打 warn。
	// yaml 仍合法,字节发正式版后翻 buildFeishuProject 即可重启用,届时这里也补回 FeishuProjectMcp。
	return out
}

func dataStoreRegistersMCP(typ string) bool {
	switch typ {
	case "mongodb", "postgresql", "elasticsearch", "redis", "mysql", "doris", "clickhouse", "kafka":
		return true
	case "rabbitmq":
		return false // 方案 B:凭据仍收,但 MCP 不注册,SKILL 走 HTTP Management API
	default:
		return false
	}
}
