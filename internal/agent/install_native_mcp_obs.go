// install_native_mcp_obs.go —— 可观测性(grafana / jaeger / elk)三家 MCP 的 builder。
// 2026-05-15 从 install_native_mcp_common.go 拆出,纯重构。
package agent

import "strings"

// Grafana uses the official platform wheel through uvx. Loki and Prometheus
// share this process; existing tool selection and token/basic auth are preserved.
func (b *mcpBuilder) buildGrafana(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.Grafana.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		servers[b.keyFor("grafana", "", e.ID)] = map[string]any{
			"command": "uvx",
			"args": []any{grafanaMCPPackage,
				"--disable-incident", "--disable-alerting", "--disable-oncall",
				"--disable-admin", "--disable-sift", "--disable-pyroscope",
			},
			"env": b.envBlock(b.grafanaAuthEnv(up)),
		}
	}
}

// grafanaAuthEnv 二选一:有 API key/SAT 时走 GRAFANA_SERVICE_ACCOUNT_TOKEN,空则回落 basic auth。
func (b *mcpBuilder) grafanaAuthEnv(up string) map[string]any {
	if k := b.get("GRAFANA_API_KEY_" + up); k != "" {
		return map[string]any{
			"GRAFANA_URL":                   b.get("GRAFANA_URL_" + up),
			"GRAFANA_SERVICE_ACCOUNT_TOKEN": k,
		}
	}
	return map[string]any{
		"GRAFANA_URL":      b.get("GRAFANA_URL_" + up),
		"GRAFANA_USERNAME": b.get("GRAFANA_USER_" + up),
		"GRAFANA_PASSWORD": b.get("GRAFANA_PASS_" + up),
	}
}

// 历史上这里有过单独的 loki MCP(同款 mcp-grafana 二进制只是多 --disable-search/dashboard/
// datasource 把工具集瘦身)。但本质是 grafana MCP 的严格子集 — query_loki_logs/patterns/stats 等
// 工具 grafana MCP 都已暴露,起两份相同进程纯属浪费 spawn + zod schema 注册时间。
// 已删,保持 loki/prom 永远走 grafana MCP 单一路径。yaml 里 observability.loki.enabled
// 仅决定 routing skill 模板里的 LOKI_URL_<env> CLI fallback 提示(当 mcp 不可用时)。
// 同款理由:prometheus 一直没独立 MCP(社区无成熟 prom-only mcp 包),也走 grafana MCP。
// validate 阶段强制 Loki/Prom 启用 ⇒ Grafana 必启用,见 validate_observability_grafana_required.go。

// buildJaeger:用 traceloop/opentelemetry-mcp(uvx)真 mcp,4 家平台都注册(跟数据层 mcp 同款思路 —
// 让 AI 直接 tool_use 调,不用让 AI 自己拼 jaeger /api/traces HTTP curl)。
// 老路径(opts.IncludeRawObsCurl 控制 jaeger 走 curl 占位)被替换。
// stdio 干净,BACKEND_TYPE=jaeger / BACKEND_URL=<JAEGER_URL_<env>> 指向 jaeger query 端口(默认 16686)。
// PruneEmpty 模式下:JAEGER_URL_<env> 没填则 BACKEND_URL 空 → 整个 env block 被剔 → mcp 启动失败被 IDE 自动跳。
//
// 2026-05-15 runtime probe 后已知:实际暴露 11 个工具(README 写了 5 个,多 6 个 LLM-tracing 专用)。
// LLM 排障 filter `gen_ai_system / gen_ai_model` 留空即可不影响,常规微服务排障 search_traces /
// get_trace / list_services / find_errors 4 个就够。
func (b *mcpBuilder) buildJaeger(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.Jaeger.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		jurl := b.get("JAEGER_URL_" + up)
		if jurl == "" && b.opts.PruneEmpty {
			continue
		}
		servers[b.keyFor("jaeger", "", e.ID)] = map[string]any{
			"command": "uvx",
			"args":    []any{"opentelemetry-mcp"},
			"env": b.envBlock(map[string]any{
				"BACKEND_TYPE": "jaeger",
				"BACKEND_URL":  jurl,
			}),
		}
	}
}

// buildELK 走 Elastic 官方 @elastic/mcp-server-elasticsearch(跟数据层 elasticsearch 同款,
// 区别只在 env vars 命名空间:ELK_* 防跟数据层 ES 字段串)。Kibana UI 由 agent 通过
// SKILL.md 拼 deeplink,不进 MCP env(本 MCP 只接 ES API)。
// OTEL_SDK_DISABLED=true 防 elastic-otel-node 自动注入往 stdout 打 banner JSON 污染
// stdio JSON-RPC(同数据层 ES 那条注释)。
//
// v0.1.1 使用 Elasticsearch 8.x client,可连接 ES 7/8 集群;v0.2.0+ 改用 9.x
// client 并发送 compatible-with=9,会被现有 ES 7/8 环境拒绝。
func (b *mcpBuilder) buildELK(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.ELK.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		esURL := b.get("ELK_ES_URL_" + up)
		if esURL == "" && b.opts.PruneEmpty {
			continue // 没填 ES URL → 跳过(避免注册一条永远启动失败的 mcp)
		}
		servers[b.keyFor("elk", "", e.ID)] = map[string]any{
			"command": "npx",
			"args":    []any{"-y", elasticsearchMCPPackage},
			"env": b.envBlock(map[string]any{
				"ES_URL":            esURL,
				"ES_USERNAME":       b.get("ELK_USERNAME"),
				"ES_PASSWORD":       b.get("ELK_PASSWORD"),
				"OTEL_SDK_DISABLED": "true",
			}),
		}
	}
}
