package generator

// SkillAllowedForAgentRole is the single source of truth for which generated
// skill directories belong to each internal agent role.
func SkillAllowedForAgentRole(skillName string, role AgentRole) bool {
	switch role {
	case AgentRoleValidator:
		return validatorSkillSet[skillName]
	case AgentRoleTroubleshooter:
		return skillName != "bug-verifier" &&
			skillName != "api-verifier" &&
			skillName != "attachment-evidence-verifier"
	default:
		return true
	}
}

var validatorSkillSet = map[string]bool{
	"bug-verifier":                 true,
	"api-verifier":                 true,
	"attachment-evidence-verifier": true,
	"frontend-repro-investigator":  true,
	"routing":                      true,
	"config-executor":              true,
	"grafana-observability-query":  true,
	"k8s-runtime-query":            true,
	"elk-log-query":                true,
	"tracing-query":                true,
	"tempo-query":                  true,
	"skywalking-query":             true,
	"mongodb-runtime-query":        true,
	"mysql-runtime-query":          true,
	"postgresql-runtime-query":     true,
	"redis-runtime-query":          true,
	"es-runtime-query":             true,
	"doris-runtime-query":          true,
	"clickhouse-runtime-query":     true,
	"kafka-runtime-query":          true,
	"rabbitmq-runtime-query":       true,
}
