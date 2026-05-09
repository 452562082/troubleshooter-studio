package config

import (
	"fmt"
	"slices"
)

// checkObservability:可观测性 wiring 检查。
//
// 设计取舍:很多团队"一套 Grafana 服所有 env"(env 区分由 LogQL/PromQL 标签决定),
// 这种场景下 url_by_env / datasource_uid_by_env 只填一条就够。所以**不做"逐 env 缺哪个就报哪个"
// 的检查**(伪报太多)。只在以下情况报:
//   1. 整张 map 完全空 → warn / info(看是不是必填)
//   2. 字段语义矛盾 → error (e.g. via_grafana=true 但 grafana 没启用)
//   3. service_map 引用了不存在的 env → warn
func checkObservability(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	obs := c.Infrastructure.Observability
	envIDs := envIDList(c)

	mapEmpty := func(m map[string]string) bool { return len(m) == 0 }

	// Grafana 启用但 url_by_env 完全空
	if obs.Grafana.Enabled && mapEmpty(obs.Grafana.URLByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.grafana.url_by_env",
			Message:  "Grafana 启用了但 url_by_env 完全为空,所有环境都拿不到 dashboard",
			Hint:     "至少填一条(共享一套 Grafana 时填一条即可),否则关掉 grafana.enabled",
		})
	}

	// 矛盾:via_grafana=true 但 grafana.enabled=false
	// Loki / Prometheus / Tempo 当前**只**支持 via Grafana 代理 — mcp-grafana-npx 内置
	// query_loki_logs / query_prometheus / 等工具,社区没有成熟的"独立 Loki/Prom MCP"
	// npm 包,自己写不划算。yaml 里 via_grafana 字段保留(向后兼容),但只接受 true。
	// 启 Loki/Prom/Tempo 但 Grafana 未启 ⇒ 强制报错(老逻辑只在 via_grafana=true 时报,
	// 但用户改 via_grafana=false 也是错配 — 我们根本不支持直连)。
	if obs.Loki.Enabled && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.loki",
			Message:  "Loki 启用但 Grafana 未启用 — 本系统的 Loki 查询通过 grafana MCP 的 query_loki_* 工具实现(没有独立 Loki MCP)",
			Hint:     "启用 grafana 并填 URL/凭据,或关 loki.enabled",
		})
	}
	if obs.Prometheus.Enabled && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.prometheus",
			Message:  "Prometheus 启用但 Grafana 未启用 — 本系统的 Prometheus 查询通过 grafana MCP 的 query_prometheus 工具实现(没有独立 Prom MCP)",
			Hint:     "启用 grafana 并填 URL/凭据,或关 prometheus.enabled",
		})
	}
	if obs.Tempo.Enabled && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.tempo",
			Message:  "Tempo 启用但 Grafana 未启用 — 本系统的 Tempo 查询通过 grafana MCP 实现(没有独立 Tempo MCP)",
			Hint:     "启用 grafana 并填 URL/凭据,或关 tempo.enabled",
		})
	}
	// 反向:via_grafana=false 也提示一下 — 当前不支持直连模式
	for _, c := range []struct {
		enabled, via bool
		field, name  string
	}{
		{obs.Loki.Enabled, obs.Loki.ViaGrafana, "infrastructure.observability.loki.via_grafana", "Loki"},
		{obs.Prometheus.Enabled, obs.Prometheus.ViaGrafana, "infrastructure.observability.prometheus.via_grafana", "Prometheus"},
		{obs.Tempo.Enabled, obs.Tempo.ViaGrafana, "infrastructure.observability.tempo.via_grafana", "Tempo"},
	} {
		if c.enabled && !c.via {
			out = append(out, HealthIssue{
				Severity: "warn",
				Category: "observability",
				Field:    c.field,
				Message:  c.name + " via_grafana=false,但本系统目前只通过 grafana MCP 查 " + c.name + "(无独立 MCP),via_grafana 字段实际不影响生成结果",
				Hint:     "建议改 via_grafana=true(语义对齐实际行为)",
			})
		}
	}

	// Loki / Prometheus 走 Grafana 代理但 datasource_uid_by_env 完全空
	if obs.Loki.Enabled && obs.Loki.ViaGrafana && obs.Grafana.Enabled && mapEmpty(obs.Loki.DatasourceUIDByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.loki.datasource_uid_by_env",
			Message:  "Loki 走 Grafana 代理但 datasource_uid_by_env 完全空,查日志找不到数据源",
			Hint:     "在 Grafana 后台数据源页面找到 Loki 的 UID(URL 末段),至少填一条",
		})
	}
	if obs.Prometheus.Enabled && obs.Prometheus.ViaGrafana && obs.Grafana.Enabled && mapEmpty(obs.Prometheus.DatasourceUIDByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.prometheus.datasource_uid_by_env",
			Message:  "Prometheus 走 Grafana 代理但 datasource_uid_by_env 完全空",
		})
	}

	// Jaeger 启用但 url + ds uid 全空
	if obs.Jaeger.Enabled && mapEmpty(obs.Jaeger.URLByEnv) && mapEmpty(obs.Jaeger.DatasourceUIDByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.jaeger",
			Message:  "Jaeger 启用了但 url_by_env / datasource_uid_by_env 都为空,trace 查询无法发起",
			Hint:     "或者关掉 jaeger.enabled",
		})
	}

	// ELK 启用但三类入口都空
	if obs.ELK.Enabled && mapEmpty(obs.ELK.ESByEnv) && mapEmpty(obs.ELK.KibanaByEnv) && mapEmpty(obs.ELK.DatasourceUIDByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.elk",
			Message:  "ELK 启用了但 es_by_env / kibana_by_env / datasource_uid_by_env 全空,日志查询会失败",
		})
	}

	// SkyWalking / Tempo:整张 map 空才报
	if obs.SkyWalking.Enabled && mapEmpty(obs.SkyWalking.URLByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.skywalking.url_by_env",
			Message:  "SkyWalking 启用了但 url_by_env 完全为空",
		})
	}
	if obs.Tempo.Enabled && !obs.Tempo.ViaGrafana && mapEmpty(obs.Tempo.URLByEnv) {
		out = append(out, HealthIssue{
			Severity: "warn",
			Category: "observability",
			Field:    "infrastructure.observability.tempo.url_by_env",
			Message:  "Tempo 启用了(未走 Grafana 代理)但 url_by_env 完全为空",
		})
	}

	// K8sRuntime:url_by_env 完全空必报(skill 跑不起来),非空时不再逐 env 比对(同 Grafana 共享逻辑)
	if obs.K8sRuntime.Enabled {
		if mapEmpty(obs.K8sRuntime.URLByEnv) {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "observability",
				Field:    "infrastructure.observability.k8s_runtime.url_by_env",
				Message:  "K8s Runtime (Kuboard) 启用了但 url_by_env 完全为空,无法发请求",
				Hint:     "至少填一条 Kuboard URL,或者关掉 k8s_runtime.enabled",
			})
		}
		if len(obs.K8sRuntime.ServiceMap) == 0 {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "observability",
				Field:    "infrastructure.observability.k8s_runtime.service_map",
				Message:  "K8s Runtime 启用但 service_map 为空,routing 退化到通用 namespace+app 标签匹配,准确度下降",
				Hint:     "建议跑仓库分析让 analyzer 自动反填,或在向导里手挑 Deployment",
			})
		} else {
			// service_map 引用了未知 env(这条是真问题,该映射不生效)
			for i, sm := range obs.K8sRuntime.ServiceMap {
				if !slices.Contains(envIDs, sm.Env) {
					out = append(out, HealthIssue{
						Severity: "warn",
						Category: "observability",
						Field:    fmt.Sprintf("infrastructure.observability.k8s_runtime.service_map[%d].env", i),
						Message:  fmt.Sprintf("k8s_runtime.service_map[%d].env=%q 不在 environments 列表里,该映射不生效", i, sm.Env),
					})
				}
			}
		}
	}

	// 一个都没启用
	anyEnabled := obs.Grafana.Enabled || obs.Loki.Enabled || obs.Prometheus.Enabled ||
		obs.Jaeger.Enabled || obs.ELK.Enabled || obs.SkyWalking.Enabled || obs.Tempo.Enabled ||
		obs.K8sRuntime.Enabled
	if !anyEnabled {
		out = append(out, HealthIssue{
			Severity: "info",
			Category: "observability",
			Field:    "infrastructure.observability",
			Message:  "完全没启用任何可观测性组件,机器人只能基于代码 + 配置中心做静态推断",
			Hint:     "建议至少启用一项指标(prometheus)或日志(loki/elk),否则排障准确度有限",
		})
	}

	return out
}
