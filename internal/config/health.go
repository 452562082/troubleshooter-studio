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
//
// 各类 check 已按域拆到子文件:
//   health_repo.go             checkEnvRepo
//   health_observability.go    checkObservability
//   health_generation.go       checkGeneration + dataStoreEnabled helper
//   health_config_center.go    checkConfigCenter + joinConfigCenterIDs helper
//   health_data_stores.go      checkDataStoresMessaging

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
	"routing":                      true,
	"code-intelligence-query":      true,
	"config-executor":              true,
	"incident-investigator":        true,
	"api-verifier":                 true,
	"attachment-evidence-verifier": true,
	"bug-verifier":                 true,
	"frontend-repro-investigator":  true,
	"grafana-observability-query":  true,
	"recent-changes":               true,
	"diagram-generator":            true,
	"k8s-runtime-query":            true,
	"redis-runtime-query":          true,
	"mysql-runtime-query":          true,
	"doris-runtime-query":          true,
	"mongodb-runtime-query":        true,
	"es-runtime-query":             true,
	"postgresql-runtime-query":     true,
	"clickhouse-runtime-query":     true,
	"kafka-runtime-query":          true,
	"rabbitmq-runtime-query":       true,
	"elk-log-query":                true,
	"tracing-query":                true,
	"skywalking-query":             true,
	"tempo-query":                  true,
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

func sortedSkillNames() []string {
	return sortedKeys(knownSkills)
}
