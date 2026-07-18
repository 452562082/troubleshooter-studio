package agent

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const projectRouterSkillName = "tshoot-router"

//go:embed assets/project-router/SKILL.md
var projectRouterSkillTemplate string

//go:embed assets/project-router/resolve.py
var projectRouterResolver []byte

// installProjectRouter installs one shared router per IDE. It is intentionally
// outside every business agent namespace and has no tshoot.json anchor, so the
// Studio still discovers one card per business system rather than a fake bot.
func installProjectRouter(root string, target IDETarget) error {
	dir := filepath.Join(root, "skills", projectRouterSkillName)
	scriptPath := filepath.Join(dir, "scripts", "resolve.py")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return fmt.Errorf("mkdir project router: %w", err)
	}
	if err := os.WriteFile(scriptPath, projectRouterResolver, 0o755); err != nil {
		return fmt.Errorf("install project router resolver: %w", err)
	}
	skill := strings.ReplaceAll(projectRouterSkillTemplate, "{{ROUTER_SCRIPT}}", filepath.ToSlash(scriptPath))
	skill = strings.ReplaceAll(skill, "{{TARGET}}", string(target))
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		return fmt.Errorf("install project router skill: %w", err)
	}
	return nil
}
