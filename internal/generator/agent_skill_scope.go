package generator

// SkillAllowedForAgentRole is the single source of truth for which generated
// skill directories belong to each internal agent role.
func SkillAllowedForAgentRole(skillName string, role AgentRole) bool {
	switch role {
	case AgentRoleValidator:
		return false
	case AgentRoleFixer:
		return skillName != "bug-verifier" &&
			skillName != "api-verifier" &&
			skillName != "attachment-evidence-verifier"
	case AgentRoleTroubleshooter:
		return skillName != "bug-fixer" &&
			skillName != "bug-verifier" &&
			skillName != "api-verifier" &&
			skillName != "attachment-evidence-verifier"
	default:
		return true
	}
}
