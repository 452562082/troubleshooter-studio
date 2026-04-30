package config

// health.go —— yaml 健康检查(配置完整度 / 一致性 / 可生成性)。
// 跟 Validate() 的区别:
//   - Validate 拦"yaml 格式上跑不下去"的错(必填字段缺、引用未知 env、类型枚举非法等),
//     一旦报错 generator 直接拒生。
//   - HealthCheck 出"配置上能跑、但语义上有缺口"的提示(severity = warn/info/error),
//     用户可以选择忽略,但前端会展示出来,提醒"agent 实际跑的时候这一块会拿不到数据"。
//
// 价值定位:wizard 默认填的字段不够全,新手一路 Next 出来的 yaml 极容易在某些 env / 数据源
// 上"看起来配齐了实际上空着"。HealthCheck 把这些缺口聚成一份清单。

import (
	"fmt"
	"slices"
	"strings"
)

// HealthIssue 单条健康检查问题。
type HealthIssue struct {
	Severity string `json:"severity"` // error / warn / info
	Category string `json:"category"` // env / observability / generation / repo / config_center / data_stores / messaging
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// 已知 skill 名清单(跟 templates/workspace/skills/ 同步,加新 skill 时要更新这里)
// 用 map 加速 lookup;前端展示时排序后给出
var knownSkills = map[string]bool{
	"routing":                  true,
	"config-executor":          true,
	"incident-investigator":    true,
	"recent-changes":           true,
	"diagram-generator":        true,
	"k8s-runtime-query":        true,
	"redis-runtime-query":      true,
	"mysql-runtime-query":      true,
	"mongodb-runtime-query":    true,
	"es-runtime-query":         true,
	"postgresql-runtime-query": true,
	"clickhouse-runtime-query": true,
	"kafka-runtime-query":      true,
	"rocketmq-runtime-query":   true,
	"rabbitmq-runtime-query":   true,
	"elk-log-query":            true,
	"tracing-query":            true,
	"skywalking-query":         true,
	"tempo-query":              true,
}

// HealthCheck 在 yaml 已经通过 Validate() 之后再跑一组语义检查。返回零长度 slice 表示无问题。
func HealthCheck(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	out = append(out, checkEnvRepo(c)...)
	out = append(out, checkObservability(c)...)
	out = append(out, checkGeneration(c)...)
	out = append(out, checkConfigCenter(c)...)
	out = append(out, checkDataStoresMessaging(c)...)
	return out
}

func envIDList(c *SystemConfig) []string {
	out := make([]string, 0, len(c.Environments))
	for _, e := range c.Environments {
		out = append(out, e.ID)
	}
	return out
}

// 1. 环境-仓库关系
//
// "repo 在 env X 没填 env_branches" 既可能是"漏填了",也可能是"这个 repo 不在 env X 部署"。
// 没足够信号区分,所以只在**整张 env_branches 完全为空**时报(明显漏配),否则不打扰。
// service_names 空 → info 提示一下 analyzer 可以自动抽。
func checkEnvRepo(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	for _, r := range c.Repos {
		if len(r.EnvBranches) == 0 {
			out = append(out, HealthIssue{
				Severity: "warn",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].env_branches", r.Name),
				Message:  fmt.Sprintf("仓库 %q 完全没填 env_branches,routing 不知道该 checkout 哪个分支", r.Name),
				Hint:     "至少填一个 env→branch 映射(typical: dev: develop, prod: main)",
			})
		}
		if len(r.ServiceNames) == 0 {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].service_names", r.Name),
				Message:  fmt.Sprintf("仓库 %q 没填 service_names,routing 用 repo.name 当服务名,跟实际可能不一致", r.Name),
				Hint:     "跑一遍仓库分析让 analyzer 自动抽 service_names,或手填进 yaml",
			})
		}
	}
	return out
}

// 2. 可观测性 wiring
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
	if obs.Loki.Enabled && obs.Loki.ViaGrafana && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.loki.via_grafana",
			Message:  "Loki via_grafana=true 但 Grafana 未启用,矛盾",
			Hint:     "改 loki.via_grafana=false 直连 Loki,或启用 Grafana",
		})
	}
	if obs.Prometheus.Enabled && obs.Prometheus.ViaGrafana && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.prometheus.via_grafana",
			Message:  "Prometheus via_grafana=true 但 Grafana 未启用,矛盾",
		})
	}
	if obs.Tempo.Enabled && obs.Tempo.ViaGrafana && !obs.Grafana.Enabled {
		out = append(out, HealthIssue{
			Severity: "error",
			Category: "observability",
			Field:    "infrastructure.observability.tempo.via_grafana",
			Message:  "Tempo via_grafana=true 但 Grafana 未启用,矛盾",
		})
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

// 3. 生成可行性
func checkGeneration(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	targets := c.Generation.ResolvedTargets()

	// 未知 skill
	for _, s := range c.Generation.SkillsWhitelist {
		if !knownSkills[s] {
			out = append(out, HealthIssue{
				Severity: "warn",
				Category: "generation",
				Field:    "generation.skills_whitelist",
				Message:  fmt.Sprintf("skills_whitelist 含未知 skill 名 %q,生成时会被忽略", s),
				Hint:     "已知 skill:" + strings.Join(sortedSkillNames(), ", "),
			})
		}
	}

	// 已 whitelist 的 *-runtime-query 但对应 data_store 没启用 → 会被静默跳过
	dsCheck := func(skill, dsType string) {
		for _, w := range c.Generation.SkillsWhitelist {
			if w == skill && !dataStoreEnabled(c, dsType) {
				out = append(out, HealthIssue{
					Severity: "info",
					Category: "generation",
					Field:    "generation.skills_whitelist",
					Message:  fmt.Sprintf("%s 在白名单但 data_stores.%s 未启用,该 skill 会被跳过", skill, dsType),
				})
			}
		}
	}
	dsCheck("redis-runtime-query", "redis")
	dsCheck("mysql-runtime-query", "mysql")
	dsCheck("mongodb-runtime-query", "mongodb")
	dsCheck("es-runtime-query", "elasticsearch")
	dsCheck("postgresql-runtime-query", "postgresql")
	dsCheck("clickhouse-runtime-query", "clickhouse")

	// preserve_on_regenerate 含越狱路径
	for _, p := range c.Generation.PreserveOnRegenerate {
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "generation",
				Field:    "generation.preserve_on_regenerate",
				Message:  fmt.Sprintf("preserve_on_regenerate 项 %q 含绝对路径或 .. 跳出 workspace,不安全", p),
			})
		}
	}

	// targets 不含 openclaw 但配了 agent.model:模型字段对 claude-code/cursor 不消费
	hasOpenclaw := false
	hasOther := false
	for _, t := range targets {
		switch t {
		case "openclaw":
			hasOpenclaw = true
		case "claude-code", "cursor":
			hasOther = true
		}
	}
	if !hasOpenclaw && hasOther && c.Agent.Model != "" {
		out = append(out, HealthIssue{
			Severity: "info",
			Category: "generation",
			Field:    "agent.model",
			Message:  "agent.model 仅 openclaw 消费,目前 targets 不含 openclaw,该字段不生效",
		})
	}

	return out
}

// 4. 配置中心
//
// 同样的 shared-instance 取舍:典型上每 env 一套 nacos 集群,但小团队也可能共享一套。
// 所以只在 endpoints 完全为空时报(明显配漏),partial 不报(可能是共享场景)。
func checkConfigCenter(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	cc := c.Infrastructure.ConfigCenters
	if len(cc) == 0 {
		return nil
	}

	// 多源时 repo 必须显式 config_source
	if len(cc) > 1 {
		for _, r := range c.Repos {
			if r.ConfigSource == "" {
				out = append(out, HealthIssue{
					Severity: "warn",
					Category: "config_center",
					Field:    fmt.Sprintf("repos[%s].config_source", r.Name),
					Message:  fmt.Sprintf("多源场景下仓库 %q 没显式 config_source,会回落到第一个源 %q,可能不对", r.Name, cc[0].ID),
					Hint:     "可选源:" + joinConfigCenterIDs(cc),
				})
			}
		}
	}

	// 类型需要 endpoints 但完全空 → error
	for _, c1 := range cc {
		if c1.Type == "none" || c1.Type == "env-vars" || c1.Type == "kuboard" {
			continue
		}
		if len(c1.Endpoints) == 0 {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "config_center",
				Field:    fmt.Sprintf("infrastructure.config_centers[%s].endpoints", c1.ID),
				Message:  fmt.Sprintf("配置中心 %q (type=%s) endpoints 完全为空,无法连接", c1.ID, c1.Type),
			})
		}
	}
	return out
}

// 5. data_stores / messaging 全 disabled
func checkDataStoresMessaging(c *SystemConfig) []HealthIssue {
	var out []HealthIssue

	if len(c.Infrastructure.DataStores) > 0 {
		anyEnabled := false
		for _, ds := range c.Infrastructure.DataStores {
			if ds.Enabled {
				anyEnabled = true
				break
			}
		}
		if !anyEnabled {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "data_stores",
				Field:    "infrastructure.data_stores",
				Message:  "data_stores 都 disabled,所有 *-runtime-query skill 会被跳过",
			})
		}
	}

	if len(c.Infrastructure.Messaging) > 0 {
		anyEnabled := false
		for _, m := range c.Infrastructure.Messaging {
			if m.Enabled {
				anyEnabled = true
				break
			}
		}
		if !anyEnabled {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "messaging",
				Field:    "infrastructure.messaging",
				Message:  "messaging 都 disabled,机器人不会主动通知",
			})
		}
	}
	return out
}

// helper

func dataStoreEnabled(c *SystemConfig, t string) bool {
	for _, ds := range c.Infrastructure.DataStores {
		if ds.Type == t && ds.Enabled {
			return true
		}
	}
	return false
}

func joinConfigCenterIDs(cc []ConfigCenter) string {
	out := []string{}
	for _, c := range cc {
		out = append(out, c.ID)
	}
	return strings.Join(out, ", ")
}

func sortedSkillNames() []string {
	return sortedKeys(knownSkills)
}
