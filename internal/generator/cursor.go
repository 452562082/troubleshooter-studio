package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateCursor 输出 Cursor IDE 用户级 Custom Agent 格式：
//   - agents/<workspace_name>.md  (Cursor agent 定义,带 frontmatter:name/description)
//   - skills/                      (映射表 + 脚本)
//   - scripts/                     (辅助脚本)
//   - install.sh                   (把 agent .md 装到 ~/.cursor/agents/,skills 装到 ~/.cursor/skills/)
//
// frontmatter.name 用 ASCII kebab-case (workspace_name);description 可中文(system.name)。
func (g *Generator) GenerateCursor() error {
	outDir := g.OutputDir + "-cursor"
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	wsRoot, cleanup, err := g.resolveWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()

	// 1) 生成 agents/<workspace_name>.md (Cursor agent 定义)
	agentName := agentSlug(g.Ctx)
	agentMD, err := buildCursorAgentMD(wsRoot, g.Ctx, agentName)
	if err != nil {
		return fmt.Errorf("build agent .md: %w", err)
	}
	agentsDir := filepath.Join(outDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(agentsDir, agentName+".md"), []byte(agentMD), 0o644); err != nil {
		return err
	}

	// 2) 拷贝 skills/(含 references 映射表)
	skillsDir := filepath.Join(wsRoot, "skills")
	if err := copyDirRecursive(skillsDir, filepath.Join(outDir, "skills")); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	// 3) 拷贝辅助脚本
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// 4) install.sh —— 装到用户级 ~/.cursor/agents/
	installSrc := filepath.Join(g.TemplateRoot, "cursor", "install.sh.tmpl")
	if err := g.renderFile(installSrc, filepath.Join(outDir, "install.sh")); err != nil {
		return fmt.Errorf("install.sh: %w", err)
	}
	if err := os.Chmod(filepath.Join(outDir, "install.sh"), 0o755); err != nil {
		return err
	}

	if err := g.writeTshootMeta(outDir, "cursor"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

// buildCursorAgentMD —— 跟 buildClaudeAgentMD 同套思路,只是 frontmatter 形态略不同
// (Cursor agent 不强制要 model 字段,留空让用户在 Cursor 里挑)。
func buildCursorAgentMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", ctx.System.Name)
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("# 由 troubleshooter-studio 生成,目标平台:Cursor Custom Agent\n\n")

	// 读取各 MD 文件合并
	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(wsRoot, name)); err == nil {
			sb.WriteString("---\n\n")
			sb.Write(data)
			sb.WriteString("\n\n")
		}
	}

	// Skills 索引
	sb.WriteString("---\n\n## 可用 Skills\n\n")
	sb.WriteString("详细规则见 `.cursor/rules/` 目录下的 .mdc 文件，映射表和脚本见 `skills/` 目录。\n\n")

	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		desc := ""
		if data, err := os.ReadFile(skillMD); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if rest, ok := strings.CutPrefix(line, "description:"); ok {
					desc = strings.TrimSpace(rest)
					break
				}
			}
		}
		fmt.Fprintf(&sb, "- **%s** — %s\n", e.Name(), desc)
	}

	return sb.String(), nil
}
