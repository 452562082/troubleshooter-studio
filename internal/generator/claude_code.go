package generator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GenerateClaudeCode 输出 Claude Code 用户级 subagent 格式：
//   - agents/<workspace_name>.md  (subagent 定义文件,带 frontmatter:name/description/tools/model)
//   - skills/                      (原样保留所有 skill 目录,subagent 内会引用)
//   - scripts/                     (辅助脚本)
//
// 这是"中间包"产物,对应的"装到 ~/.claude/{agents,skills,scripts}/" 步骤已挪到
// internal/agent.InstallNative()(原生 Go,Apply / ImportAndApply 内部自动调一次,
// 替代之前的 install.sh shell-out)。
//
// frontmatter.name = workspace_name(ASCII kebab-case),用户在 Claude Code 里用 @<name> 调用;
// frontmatter.description = system.name (中文友好,IDE 列表里显示)。
func (g *Generator) GenerateClaudeCode() error {
	outDir := g.OutputDir + "-claude-code"
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

	// 1) 生成 agents/<workspace_name>.md (subagent 定义)
	agentName := agentSlug(g.Ctx)
	agentMD, err := buildClaudeAgentMD(wsRoot, g.Ctx, agentName)
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

	// 2) 拷贝 skills/ (subagent 内通过路径引用)
	skillsSrc := filepath.Join(wsRoot, "skills")
	skillsDst := filepath.Join(outDir, "skills")
	if err := copyDirRecursive(skillsSrc, skillsDst); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	// 3) 拷贝辅助脚本到 scripts/(resolve_runtime 等)
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	scriptsDst := filepath.Join(outDir, "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, scriptsDst); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	if err := g.writeTshootMeta(outDir, "claude-code"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

// agentSlug 取 ctx.AgentID 作 subagent 文件名 / @name slug。
// 这个值由 cfg.ResolveID() 算出来:优先 agent.id,空时 <system.id>-troubleshooter。
// 跟 OpenClaw agents.list[*].id 完全对齐 —— 一份标识贯穿所有 AI 平台。
// (老代码用 workspace_name 兜底,但 wizard 已经不再 emit workspace_name,
// 那条路径会回落到 system.id 而不带 -troubleshooter 后缀,跟其他 target 不一致。)
func agentSlug(ctx *Context) string {
	if s := strings.TrimSpace(ctx.AgentID); s != "" {
		return s
	}
	if s := strings.TrimSpace(ctx.Agent.WorkspaceName); s != "" {
		return s
	}
	if s := strings.TrimSpace(ctx.System.ID); s != "" {
		return s
	}
	return "tshoot-agent"
}

// buildClaudeAgentMD 拼一份 Claude Code subagent 定义。带 YAML frontmatter:
//   name: <slug>
//   description: <中文显示名>
//   tools: 不限制(默认全工具)
//   model: 仅当用户显式给 claude-code 配了 target_models.claude-code 才写 ——
//          否则 Claude Code 用 IDE 当前选的模型(用户偏好)。
//
// 历史 bug:之前直接写 ctx.Agent.Model,但 Agent.Model 是 OpenClaw gateway 专属的
// LLM 路由 id(可能是 openai-codex/gpt-5.4 之类的非 Claude 模型)。Claude Code 拿到
// 那个值会把"你以为它会用的 Claude 模型"替换成奇怪字符串(或者忽略 / 报错),用户
// 体感是"OpenClaw 选什么 Claude Code 也跟着"。修法:Claude Code 只认 target_models.claude-code,
// 没显式配就不写 model frontmatter。
//
// 主体走 writeIDEPromptBody + writeSkillsIndex 共用 helper(SOUL / IDENTITY / 跨平台通用段 +
// skills 索引),不再吞 AGENTS / CHECKLIST / TOOLS 全文。AGENTS.md 里"工作区路径"段列三平台
// 路径 + "行为硬规则"提 TodoWrite + "出错应对"提 self-test 命令,都是 OpenClaw workspace
// 视角的;CHECKLIST.md 是 OpenClaw 自动执行步骤;TOOLS.md 是 OpenClaw 权限边界 —— 跨平台
// 通用的"首次打招呼"+ "故障快报模板" + "排障入口" + "输出形态"四段由 helper 抽出来拼。
func buildClaudeAgentMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	var sb strings.Builder
	// frontmatter
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", ctx.System.Name)
	if m := strings.TrimSpace(ctx.Agent.TargetModels["claude-code"]); m != "" {
		fmt.Fprintf(&sb, "model: %s\n", m)
	}
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("> 由 troubleshooter-studio 生成,目标平台:Claude Code subagent\n\n")

	writeIDEPromptBody(&sb, wsRoot, ctx, "")
	writeSkillsIndex(&sb, wsRoot, "## Skills 索引",
		"以下 skills 目录包含排障所需的映射表、脚本和执行流程文档。\n排障时请按 SKILL.md 中的执行流程操作，不要跳过步骤。")
	sb.WriteString("详细用法见各 skill 目录下的 `SKILL.md`。\n")
	return sb.String(), nil
}

func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, _ := d.Info()
		mode := os.FileMode(0o644)
		if info != nil {
			mode = info.Mode()
		}
		return os.WriteFile(target, data, mode)
	})
}
