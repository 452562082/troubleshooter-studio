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

// buildCursorAgentMD —— 给 Cursor IDE Custom Agent 写的原生 prompt。
//
// Cursor 默认 chat 工具集没 Bash(除非启用 background agent),MCP 集成靠 ~/.cursor/mcp.json
// 但需用户在 Cursor Settings 手动启用每个 server。所以本平台的 agent 主要做 流程指导 /
// 日志分析 / 路由查询;需要执行命令时给完整模板让用户粘贴执行,然后等用户贴回结果再分析。
//
// 这些环境约束作为 intro 自然介绍写在 agent 身份介绍里,不再单独开 "## ⚠ Cursor 模式限制"
// warning section。frontmatter.model 字段留空,让用户在 Cursor 里自选模型。
func buildCursorAgentMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", ctx.System.Name)
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)

	intro := "本 agent 在 Cursor IDE 内作为 Custom Agent 调用,做 **只读** 排障(日志 / 指标 / trace / 配置 / 代码),**不**直接落地修改。\n\n" +
		"运行环境:\n" +
		"- chat 工具集默认无 Bash,本工作区的 Python 脚本不能直接执行 —— 需要执行命令时,**输出完整命令模板让用户粘贴执行**,等用户贴回结果再继续分析\n" +
		"- MCP server 在 `~/.cursor/mcp.json` 已注册(由 troubleshooter-studio 装机时写入),但需用户在 Cursor Settings → MCP Servers 启用每个 server 才能调用\n" +
		"- 给用户的命令模板用 **绝对路径**(如 `python3 ~/.cursor/skills/" + agentName + "/<skill>/scripts/<file>.py ...`),Cursor 当前工作区不一定是本 agent 的 skills 目录\n" +
		"- 需要全自动调 MCP / Bash 拉数据时,提示用户改用 OpenClaw 或 Claude Code 部署的同名 agent"

	writeIDEAgentBody(&sb, wsRoot, ctx, IDEPlatform{
		Intro:                  intro,
		SkillsScriptPathPrefix: "~/.cursor/skills/" + agentName,
		SkillsHeader:           "## 可用 Skills",
	})
	return sb.String(), nil
}
