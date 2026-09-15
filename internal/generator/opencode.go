package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const OpenCodeSkillsRoot = "__TSHOOT_OPENCODE_SKILLS_ROOT__"

func (g *Generator) GenerateOpenCode() error {
	out := g.OutputDir + "-opencode"
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "agents"), 0755); err != nil {
		return err
	}
	ws, cleanup, err := g.resolveWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()
	for _, role := range internalAgentRoles() {
		name := agentIDForRole(g.Ctx, role)
		front := map[string]any{"description": projectBoundAgentDescription(g.Ctx, name, role), "mode": "all", "permission": map[string]string{"external_directory": "allow"}}
		if model := strings.TrimSpace(g.Ctx.Agent.TargetModels["opencode"]); model != "" {
			front["model"] = model
		}
		header, err := yaml.Marshal(front)
		if err != nil {
			return err
		}
		var body strings.Builder
		body.WriteString("---\n" + string(header) + "---\n\n")
		fmt.Fprintf(&body, "# %s\n\n", roleDisplayName(g.Ctx, role))
		root := OpenCodeSkillsRoot + "/" + name
		intro := fmt.Sprintf("你是 %s 的专属 OpenCode Agent。先读取 `%s/routing/SKILL.md` 确认项目和环境。\n", g.Ctx.System.Name, root)
		if role == AgentRoleFixer {
			intro += "只有获得用户明确修复授权才修改代码；Studio 任务必须遵守锁定工作区与基线，提交与合并由 Studio 独立授权。首先读取 `" + root + "/bug-fixer/SKILL.md`。\n"
		} else {
			intro += "按 `" + root + "/incident-investigator/SKILL.md` 执行只读排障，不自行修改代码或配置。\n"
		}
		writeIDEAgentBody(&body, ws, g.Ctx, IDEPlatform{Intro: intro, SkillsScriptPathPrefix: root, SkillsHeader: "## Skills 索引"})
		if err := os.WriteFile(filepath.Join(out, "agents", name+".md"), []byte(body.String()), 0644); err != nil {
			return err
		}
	}
	if err := copyDirRecursive(filepath.Join(ws, "skills"), filepath.Join(out, "skills")); err != nil {
		return err
	}
	scripts := filepath.Join(ws, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scripts); err == nil {
		if err := copyDirRecursive(scripts, filepath.Join(out, "scripts")); err != nil {
			return err
		}
	}
	return g.writeIDEAgentMetas(out, "opencode")
}

// GenerateTarget is the shared CLI, desktop and re-deployment dispatch point.
func (g *Generator) GenerateTarget(target string) error {
	switch target {
	case "claude-code":
		return g.GenerateClaudeCode()
	case "cursor":
		return g.GenerateCursor()
	case "codex":
		return g.GenerateCodex()
	case "opencode":
		return g.GenerateOpenCode()
	default:
		return fmt.Errorf("unsupported target: %q", target)
	}
}
