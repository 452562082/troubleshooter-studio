package generator

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type AgentRole string

const (
	AgentRoleTroubleshooter AgentRole = discover.RoleTroubleshooter
	AgentRoleValidator      AgentRole = discover.RoleValidator
	AgentRoleFixer          AgentRole = discover.RoleFixer
)

func internalAgentRoles() []AgentRole {
	return []AgentRole{AgentRoleTroubleshooter, AgentRoleValidator, AgentRoleFixer}
}

func agentIDForRole(ctx *Context, role AgentRole) string {
	if role == AgentRoleTroubleshooter {
		return agentSlug(ctx)
	}
	base := strings.TrimSpace(ctx.System.ID)
	if base == "" {
		base = strings.TrimSuffix(agentSlug(ctx), "-troubleshooter")
	}
	if role == AgentRoleValidator {
		return base + "-validator"
	}
	if role == AgentRoleFixer {
		return base + "-fixer"
	}
	return agentSlug(ctx)
}

func roleDisplayName(ctx *Context, role AgentRole) string {
	name := strings.TrimSpace(ctx.System.Name)
	if name == "" {
		name = strings.TrimSpace(ctx.System.ID)
	}
	if role == AgentRoleValidator {
		return name + " 验证"
	}
	if role == AgentRoleFixer {
		return name + " 修复"
	}
	return name + " 排障"
}
