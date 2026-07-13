// ide_prompt.go —— claude-code / cursor / codex 三家 IDE agent .md 的"原生 prompt 拼装"。
//
// 原则:每个平台的 agent .md 是为该平台原生写的提示词,**不**机械拼贴 OpenClaw workspace
// 的 5 个 .md 文件。具体:
//
//  1. workspace 里 SOUL.md / IDENTITY.md / AGENTS.md 第一行是 `# SOUL.md - X` /
//     `# IDENTITY.md` / `# AGENTS.md - X 工作区` 这种"文件名标识 heading",对 OpenClaw
//     workspace 内部寻址有意义,但写到 IDE agent .md 里就变成了奇怪的"# IDENTITY.md"
//     段标题。统一脱掉,内容直接接进 agent 主标题下。
//
//  2. AGENTS.md 里有 OpenClaw 专属段(`## 工作区路径` 给 ~/.openclaw/workspace/<id>/ 路径
//     和 6 份 .md 布局;`## 行为硬规则` 假设 OpenClaw 的进度条 / ctx 首行约定;
//     `## 出错应对` 提 `tshoot agent self-test` 命令)。这些段不进 IDE agent .md,
//     IDE 的对应内容在各自 generator 的 intro 里以平台原生措辞写。
//
//  3. "Cursor 模式限制" / "Codex 模式限制" 这种 warning section 不再出现。每个平台的
//     运行环境(有/没 Bash、MCP 怎么注册、脚本路径前缀)以**自然介绍**的形式由调用方传入
//     intro 字符串,作为 agent 自我介绍的一部分。
//
//  4. CHECKLIST.md(全是 OpenClaw 自动执行步骤)+ TOOLS.md(OpenClaw 权限边界)整体不拼。
//
// OpenClaw 自己的 workspace 仍按原样保留 5 个 .md 文件,本文件只影响 IDE 三家产物。
package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IDEPlatform 描述一个 IDE 平台的原生属性,生成器据此拼出"为该平台原生写的"agent .md。
type IDEPlatform struct {
	// Intro:本平台 agent 是如何被调用 + 有哪些可用能力的"自然介绍"。
	// 由调用方传入,会作为 agent 头部的引导段,代替老版本的"# 平台 模式限制"warning section。
	// 例:"在 Claude Code 通过 @<name> 调用本 subagent。可直接使用 Bash / Read / WebFetch /
	//      TodoWrite 等工具,MCP 已在 ~/.claude.json 自动注册..."
	Intro string

	// SkillsScriptPathPrefix:本平台 skills 目录在用户机器上的绝对路径前缀(不带 trailing /)。
	// 用于 agent 内部告知"调脚本 / Read 引用文件用 <prefix>/<skill>/..." 路径。
	// 例:"~/.claude/skills/<agent-name>" / "~/.cursor/skills/<agent-name>" / "~/.codex/skills/<agent-name>"
	SkillsScriptPathPrefix string

	// SkillsHeader:Skills 索引段的标题(如 "## Skills 索引" / "## 可用 Skills")。各家 IDE 文档
	// 风格不同 —— Claude Code 偏正式 "Skills 索引",Cursor / Codex 用 "可用 Skills"。
	SkillsHeader string
}

// readMDStripHeading 读 workspace 根下某 .md 文件,去掉第一行的 `# *.md*` 标识 heading。
// 这样 SOUL.md 头上的 "# SOUL.md - X排障机器人" 不会变成 IDE agent .md 里的怪段标题。
// 文件不存在 / 无 # heading → 全文返回(让调用方决定是否拼)。
func readMDStripHeading(wsRoot, name string) string {
	data, err := os.ReadFile(filepath.Join(wsRoot, name))
	if err != nil {
		return ""
	}
	body := string(data)
	// 找第一行;若以 `# ` 开头 + 含 ".md" 字样,认作"文件名标识 heading",剥掉。
	nl := strings.IndexByte(body, '\n')
	if nl < 0 {
		return body
	}
	first := strings.TrimSpace(body[:nl])
	if strings.HasPrefix(first, "# ") && strings.Contains(first, ".md") {
		body = body[nl+1:]
	}
	return strings.TrimLeft(body, "\n")
}

// extractMDSection 从 markdown 字符串里抽指定 H2 标题(`## <title>`)开始、到下一个 H2 或文件
// 末尾结束的整段。title 用前缀匹配(防"## 故障快报模板(排障类输出用)"括号注释扰动)。
//
// 返回值含原 ## 标题行;不含末尾换行余量。找不到时返空串。
func extractMDSection(md, titlePrefix string) string {
	lines := strings.Split(md, "\n")
	startIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "## ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
		if strings.HasPrefix(title, titlePrefix) {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return ""
	}
	endIdx := len(lines)
	for j := startIdx + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "## ") {
			endIdx = j
			break
		}
	}
	return strings.TrimRight(strings.Join(lines[startIdx:endIdx], "\n"), "\n")
}

// writeIDEAgentBody 把"原生写给该 IDE 平台"的 agent prompt 主体写到 sb。
// 调用方传入 wsRoot(OpenClaw workspace 临时目录,内有 SOUL/IDENTITY/AGENTS 等)+ ctx +
// 平台属性 profile。本函数负责:
//
//  1. 写平台 intro(自然介绍,不是 warning section)
//  2. 写 SOUL 主体(去掉文件名 heading)
//  3. 写 IDENTITY 主体(去掉文件名 heading)
//  4. 抽 AGENTS 跨平台通用四段(排障入口 / 输出形态 / 首次打招呼 / 故障快报模板)
//  5. 写 Skills 索引(用 profile.SkillsScriptPathPrefix 提示路径前缀)
//
// 不拼 CHECKLIST.md / TOOLS.md / AGENTS.md 的 OpenClaw 专属段。
func writeIDEAgentBody(sb *strings.Builder, wsRoot string, ctx *Context, profile IDEPlatform) {
	_ = ctx // 保留:未来按 ctx.Agent / ctx.System 字段动态裁剪 intro 时用

	// 1) 平台 intro —— 自然介绍,不是 warning,直接接在主标题下
	if intro := strings.TrimSpace(profile.Intro); intro != "" {
		sb.WriteString(intro)
		sb.WriteString("\n\n")
	}

	// 2) SOUL 主体(去掉 "# SOUL.md - X" 第一行 heading)
	if soul := readMDStripHeading(wsRoot, "SOUL.md"); soul != "" {
		sb.WriteString(soul)
		if !strings.HasSuffix(soul, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 3) IDENTITY 主体(去掉 "# IDENTITY.md" 第一行 heading);整段以 "## 身份" 重命名,
	// 避免裸的姓名 + 典型示例段没有自己的标题在 SOUL 之后突兀贴着
	if id := readMDStripHeading(wsRoot, "IDENTITY.md"); id != "" {
		sb.WriteString("## 身份\n\n")
		sb.WriteString(id)
		if !strings.HasSuffix(id, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 4) AGENTS 跨平台通用四段:排障入口 / 输出形态 / 首次打招呼 / 故障快报模板。
	// "工作区路径" / "行为硬规则" / "出错应对" / "角色" 段是 OpenClaw 视角,IDE 不要。
	if agents, _ := os.ReadFile(filepath.Join(wsRoot, "AGENTS.md")); len(agents) > 0 {
		md := string(agents)
		for _, title := range []string{"排障入口", "输出形态", "首次打招呼", "故障快报模板"} {
			if section := extractMDSection(md, title); section != "" {
				sb.WriteString(section)
				sb.WriteString("\n\n")
			}
		}
	}

	// 5) Skills 索引 —— 标题 + 路径提示(平台特定)+ 列表
	header := strings.TrimSpace(profile.SkillsHeader)
	if header == "" {
		header = "## Skills 索引"
	}
	sb.WriteString(header)
	sb.WriteString("\n\n")
	if profile.SkillsScriptPathPrefix != "" {
		fmt.Fprintf(sb, "skill 文件路径前缀:`%s`,排障时按各 SKILL.md 流程操作,不要跳步骤。\n\n",
			profile.SkillsScriptPathPrefix)
	}

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
		fmt.Fprintf(sb, "- **%s** — %s\n", e.Name(), desc)
	}
	sb.WriteString("\n")
}

// writeIDEValidatorAgentBody 写验证 agent 的精简提示词。它不复用 SOUL / IDENTITY /
// AGENTS.md 的排障段，避免把排查主线、故障快报模板等内容带进验证角色。
func writeIDEValidatorAgentBody(sb *strings.Builder, profile IDEPlatform) {
	if intro := strings.TrimSpace(profile.Intro); intro != "" {
		sb.WriteString(intro)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## 角色边界\n\n")
	sb.WriteString("- 只处理 bug 验证、主动复现、修复后复查和证据收集。\n")
	sb.WriteString("- 第一入口固定为 `bug-verifier/SKILL.md`;按它的状态枚举和输出结构返回验证报告。\n")
	sb.WriteString("- 可按验证流程读取 attachment-evidence-verifier、api-verifier、routing、frontend-repro-investigator、日志 / trace / 配置相关 skill 来补证据。\n")
	sb.WriteString("- 不读取业务源码定位函数/文件行号/补丁点；不输出故障定位报告,不排序候选原因,不把验证结论扩展成处置建议。\n\n")

	sb.WriteString("## 输出要求\n\n")
	sb.WriteString("- 输出必须围绕 `verification_status`、环境、复现步骤、实际表现、期望表现、证据和缺口。\n")
	sb.WriteString("- 结论必须绑定截图、Network、console、API 响应、request id、trace id、日志或命令输出等可复查证据。\n")
	sb.WriteString("- 如果已复现,输出 `handoff_to_troubleshooter` 事实摘要后停止；不要继续做代码分析或修复建议。\n")
	sb.WriteString("- 信息不足时使用 `insufficient_info`,列出最小阻塞项,不要猜测。\n\n")

	sb.WriteString("## 验证入口\n\n")
	if profile.SkillsScriptPathPrefix != "" {
		fmt.Fprintf(sb, "第一步 Read `%s/bug-verifier/SKILL.md`。\n\n", profile.SkillsScriptPathPrefix)
	} else {
		sb.WriteString("第一步 Read `bug-verifier/SKILL.md`。\n\n")
	}
	sb.WriteString("常用辅助 skill:\n\n")
	sb.WriteString("- **attachment-evidence-verifier** — Bug 工单附件、截图、录屏、HAR、console 文件证据归一化。\n")
	sb.WriteString("- **bug-verifier** — 验证 agent 统一入口,负责复现、回归、证据收集和验证报告。\n")
	sb.WriteString("- **api-verifier** — API / webhook / job 场景的请求重放、响应对比和修复验证。\n")
	sb.WriteString("- **frontend-repro-investigator** — 浏览器场景的 HAR / screenshot / console / Network / RUM 证据整理。\n")
	sb.WriteString("- **routing** — 查询环境、域名、服务、仓库和配置映射。\n")

	sb.WriteString("\n")
}
