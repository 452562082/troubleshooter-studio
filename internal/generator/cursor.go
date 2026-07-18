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

	agentsDir := filepath.Join(outDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	// 1) 生成两个 agent 定义:排障 agent + 验证 agent。两者共享下面同一份 skills/scripts。
	for _, role := range internalAgentRoles() {
		agentName := agentIDForRole(g.Ctx, role)
		agentMD, err := buildCursorAgentMD(wsRoot, g.Ctx, agentName, role)
		if err != nil {
			return fmt.Errorf("build %s agent .md: %w", role, err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, agentName+".md"), []byte(agentMD), 0o644); err != nil {
			return err
		}
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

	if err := g.writeIDEAgentMetas(outDir, "cursor"); err != nil {
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
func buildCursorAgentMD(wsRoot string, ctx *Context, agentName string, role AgentRole) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", projectBoundAgentDescription(ctx, agentName, role))
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s\n\n", roleDisplayName(ctx, role))
	sb.WriteString(cursorProjectOwnershipGate(ctx, agentName))

	if role == AgentRoleValidator {
		intro := "本 agent 在 Cursor IDE 内作为 Custom Agent 调用,负责 **验证 / 主动复现 / 修复后复查**,只输出验证报告,不做原因定位。\n\n" +
			"运行环境:\n" +
			"- chat 工具集默认无 Bash,工作区的 Python 脚本不能直接执行 —— 需要执行命令时**输出完整命令模板让用户粘贴执行**,等用户贴回结果再继续验证\n" +
			"- MCP server 已写入 `~/.cursor/mcp.json`,但需用户在 Cursor Settings → MCP Servers 手动启用每个 server 才能调用\n" +
			"- 给用户的命令模板用**绝对路径**(如 `python3 ~/.cursor/skills/" + agentName + "/<skill>/scripts/<file>.py ...`),Cursor 当前工作区不一定是本 agent 的 skills 目录\n" +
			"- 不读取业务源码定位函数/文件行号/补丁点;代码分析和原因判断交给排障 Agent\n" +
			"- 第一动作是 Read `~/.cursor/skills/" + agentName + "/bug-verifier/SKILL.md`,按其中流程复现、回归并输出验证报告"

		writeIDEValidatorAgentBody(&sb, IDEPlatform{
			Intro:                  intro,
			SkillsScriptPathPrefix: "~/.cursor/skills/" + agentName,
		})
		return sb.String(), nil
	}
	if role == AgentRoleFixer {
		fmt.Fprintf(&sb, "本 agent 在 Cursor IDE 内作为 Custom Agent 调用,负责 **修复 Bug / 创建修复分支 / 提交并推送**。只在用户明确触发修复后执行。\n\n")
		sb.WriteString("第一动作是 Read `~/.cursor/skills/" + agentName + "/bug-fixer/SKILL.md`,按其中流程执行。\n\n")
		sb.WriteString("## 强制流程\n\n")
		sb.WriteString("1. 确认当前工作区已经在目标环境分支；无法确认就停止。\n")
		sb.WriteString("2. 从当前环境分支创建独立修复分支，分支名使用 `fix/bug-<source>-<id>-<short>` 风格。\n")
		sb.WriteString("3. 最小改动修复，运行相关测试或给出无法运行原因。\n")
		sb.WriteString("4. 提交并推送修复分支；最后通知用户部署该分支，不自行部署。\n\n")
		sb.WriteString("如果工作区已有用户未提交改动，不要覆盖，先停止并说明。\n")
		return sb.String(), nil
	}

	intro := "本 agent 在 Cursor IDE 内作为 Custom Agent 调用,做 **只读** 排障(日志 / 指标 / trace / 配置 / 代码),**不**直接落地修改。\n\n" +
		"运行环境:\n" +
		"- chat 工具集默认无 Bash,工作区的 Python 脚本不能直接执行 —— 需要执行命令时**输出完整命令模板让用户粘贴执行**,等用户贴回结果再继续分析\n" +
		"- MCP server 已写入 `~/.cursor/mcp.json`,但需用户在 Cursor Settings → MCP Servers 手动启用每个 server 才能调用\n" +
		"- 给用户的命令模板用**绝对路径**(如 `python3 ~/.cursor/skills/" + agentName + "/<skill>/scripts/<file>.py ...`),Cursor 当前工作区不一定是本 agent 的 skills 目录\n" +
		"- 当前模式拉不到数据时,**只描述阻塞点 + 给手动命令**,不要替用户揣测结论"

	writeIDEAgentBody(&sb, wsRoot, ctx, IDEPlatform{
		Intro:                  intro,
		SkillsScriptPathPrefix: "~/.cursor/skills/" + agentName,
		SkillsHeader:           "## 可用 Skills",
	})
	return sb.String(), nil
}
