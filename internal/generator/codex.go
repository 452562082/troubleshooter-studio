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

// 给 OpenAI Codex CLI 写的原生 prompt。在终端通过 `@<name>` 调用本 agent;CLI 提供 Bash
// 能直接跑 Python 脚本,MCP 集成走 ~/.codex/config.toml(由 troubleshooter-studio 装机时通过
// `codex mcp add` 自动注册)。
//
// model frontmatter 仅在 target_models["codex"] 显式给出时写,否则用 Codex CLI 自身默认。
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

	intro := "本 agent 在 OpenAI Codex CLI 终端内通过 `@" + agentName + "` 调用,做 **只读** 排障(日志 / 指标 / trace / 配置 / 代码),**不**直接落地修改。\n\n" +
		"运行环境:\n" +
		"- 可使用 Bash 跑 Python 脚本与 shell 命令\n" +
		"- MCP 已通过 `codex mcp add` 自动注册到 `~/.codex/config.toml`(由 troubleshooter-studio 装机时写入),排障时直接调对应 mcp_server\n" +
		"- skills 脚本用 **绝对路径**调用:`python3 ~/.codex/skills/" + agentName + "/<skill>/scripts/<file>.py ...`\n" +
		"- 多步排障流程在回复开头打 `[已完成: routing ✓ / 当前: 拉指标]` 进度条让用户跟得上"

	writeIDEAgentBody(&sb, wsRoot, ctx, IDEPlatform{
		Intro:                  intro,
		SkillsScriptPathPrefix: "~/.codex/skills/" + agentName,
		SkillsHeader:           "## 可用 Skills",
	})
	return sb.String(), nil
}
