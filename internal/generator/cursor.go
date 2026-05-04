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
//
// 这是"中间包"产物,对应的"装到 ~/.cursor/{agents,skills,scripts}/" 步骤已挪到
// internal/agent.InstallNative()(原生 Go,Apply / ImportAndApply 内部自动调一次,
// 替代之前的 install.sh shell-out)。
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

	if err := g.writeTshootMeta(outDir, "cursor"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

// buildCursorAgentMD —— 跟 buildClaudeAgentMD 同套思路,只是 frontmatter 形态略不同
// (Cursor agent 不强制要 model 字段,留空让用户在 Cursor 里挑)。
//
// 主体内容(SOUL / IDENTITY / 跨平台通用段 + skills 索引)走 writeIDEPromptBody +
// writeSkillsIndex 共用 helper,跟 claude-code / codex 三家保持一致。Cursor 专属的
// "⚠ Cursor 模式限制"段作为 pre 传入,放在主体之前。
func buildCursorAgentMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", ctx.System.Name)
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("# 由 troubleshooter-studio 生成,目标平台:Cursor Custom Agent\n\n")

	// Cursor 模式限制说明:Cursor Custom Agent 默认 chat 工具集只有 codebase_search /
	// read_file / web_fetch / edit_file,**没 Bash**(除非启用 background agent),
	// 所以无法执行本工作区的 Python 脚本(k8s_query.py / resolve_runtime_*.py 等);
	// MCP 也要去 Cursor Settings 手填,不会自动加载 .cursor/skills 目录。
	// 这段告诉 Cursor 用户清晰预期:"问题分析助手"模式,不是"自动化排障执行器"。
	pre := "---\n\n## ⚠ Cursor 模式限制\n\n" +
		"Cursor Custom Agent 默认 chat 工具集**没 Bash**(除非启用 background agent),**不会自动加载** `.cursor/skills/` 目录,本工作区里的 Python 脚本(`k8s_query.py` / `resolve_runtime_*.py` 等)**无法直接执行**。\n\n" +
		"**Cursor 模式下你能做的**:\n" +
		"- 按 SKILL.md 里的执行流程**指导用户手动跑命令**(给完整命令模板,等用户粘贴回执)\n" +
		"- 解读用户贴过来的日志 / trace / pod 状态片段,按故障快报模板输出归因\n" +
		"- 查 routing 映射(读 `~/.cursor/skills/<agent-name>/routing/references/*.yaml`)给出 哪个 env / 哪个服务 / 哪个 mcp_server\n\n" +
		"**Cursor 模式下你做不了**:\n" +
		"- 直接调 MCP 工具(Cursor 的 MCP 集成靠 `~/.cursor/mcp.json`,需用户手动启用每个 server)\n" +
		"- 跑 Bash 脚本拉日志 / pod 状态(必须改用 OpenClaw 或 Claude Code 部署)\n\n" +
		"> 想要全自动排障,请用 OpenClaw 或 Claude Code 部署的同名 agent。Cursor 主要用于 IDE 内随手问。\n"
	writeIDEPromptBody(&sb, wsRoot, ctx, pre)
	writeSkillsIndex(&sb, wsRoot, "## 可用 Skills",
		"详细规则见 `.cursor/rules/` 目录下的 .mdc 文件，映射表和脚本见 `skills/` 目录。")
	return sb.String(), nil
}
