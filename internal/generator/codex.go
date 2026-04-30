package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateCodex 输出 OpenAI Codex CLI 用户级 Custom Agent 格式:
//   - agents/<workspace_name>.md  (Codex agent 定义,带 frontmatter:name/description)
//   - skills/                      (映射表 + 脚本,跟 Cursor 同款)
//   - scripts/                     (辅助脚本)
//
// 装载位置:`~/.codex/{agents,skills,scripts}/`,跟 Claude Code/Cursor 同款命名空间约定。
// 这是"中间包"产物,装到 ~/.codex/ 由 InstallNative() 完成。
//
// frontmatter.name 用 ASCII kebab-case (workspace_name);description 可中文(system.name)。
// model 字段仅在用户显式给 codex 配 target_models.codex 时写出 —— 否则 Codex CLI 用
// 它自己的 ~/.codex/config.toml 里的默认模型(常见 gpt-5 / o3 之类),Studio 不强行覆盖。
func (g *Generator) GenerateCodex() error {
	outDir := g.OutputDir + "-codex"
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

	agentName := agentSlug(g.Ctx)
	agentMD, err := buildCodexAgentMD(wsRoot, g.Ctx, agentName)
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

	skillsDir := filepath.Join(wsRoot, "skills")
	if err := copyDirRecursive(skillsDir, filepath.Join(outDir, "skills")); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	if err := g.writeTshootMeta(outDir, "codex"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

// buildCodexAgentMD —— 跟 buildClaudeAgentMD / buildCursorAgentMD 同套思路。
// model frontmatter 仅在 target_models.codex 显式给出时写,否则用户走 Codex CLI 自己的默认。
func buildCodexAgentMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", ctx.System.Name)
	if m := strings.TrimSpace(ctx.Agent.TargetModels["codex"]); m != "" {
		fmt.Fprintf(&sb, "model: %s\n", m)
	}
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("# 由 troubleshooter-studio 生成,目标平台:OpenAI Codex CLI\n\n")

	// 读取各 MD 文件合并
	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(wsRoot, name)); err == nil {
			sb.WriteString("---\n\n")
			sb.Write(data)
			sb.WriteString("\n\n")
		}
	}

	// Skills 索引(跟 Cursor 同款,Codex 也是用户在终端内手动调 skill)
	sb.WriteString("---\n\n## 可用 Skills\n\n")
	sb.WriteString("详细规则见 `~/.codex/skills/<agent-name>/<skill>/SKILL.md`。\n\n")

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
