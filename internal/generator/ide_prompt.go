// ide_prompt.go —— claude-code / cursor / codex 三家 IDE agent .md 共用的"prompt 拼装"逻辑。
//
// 历史问题:三家 generator 早期都把 workspace 里 5 个 .md 文件(SOUL / IDENTITY / AGENTS /
// CHECKLIST / TOOLS)整段塞进 IDE agent 文件。但这 5 个文件是 OpenClaw workspace 视角写的,
// 大量内容跟 IDE 不匹配:
//
//   - AGENTS.md 里"工作区路径"段列三平台路径 + 提"调脚本用绝对路径 python3 ~/.claude/..."
//     ——IDE 默认不会跑 Python(Cursor 没 Bash、Claude Code/Codex 也不直接执行)
//   - AGENTS.md "行为硬规则" 引用 `TodoWrite` 工具 + 假设并发工具调用 —— IDE 各家工具集不同
//   - AGENTS.md "出错应对" 提 `tshoot agent self-test ...` 命令 —— IDE 用户没法跑
//   - CHECKLIST.md 全文是 OpenClaw 自动执行步骤(git fetch / config-executor / k8s-runtime-query 等)
//     ——IDE 用户根本没法执行
//   - TOOLS.md 是 OpenClaw 权限边界声明 —— IDE 没这个抽象层
//
// IDE agent .md 应该只带跨平台通用内容:SOUL.md(行为风格 + 最高原则)+ IDENTITY.md(身份 +
// 典型示例)+ AGENTS.md 里的"首次打招呼"段(开场介绍)+ "故障快报模板"段(输出格式约束)+
// skills 索引。其余 OpenClaw 专属段一律不拼。
package generator

import (
	"os"
	"path/filepath"
	"strings"
)

// readWorkspaceMD 读 workspace 根下某 .md 文件,失败返空串(让调用方决定要不要写空白)。
func readWorkspaceMD(wsRoot, name string) string {
	data, err := os.ReadFile(filepath.Join(wsRoot, name))
	if err != nil {
		return ""
	}
	return string(data)
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

// writeIDEPromptBody 把跨平台通用内容(SOUL / IDENTITY / AGENTS 抽段 + skills 索引)写到 sb。
// 三家 IDE generator 复用本函数,确保未来加新模板时三家同步,不会再飘开。
//
// 入参 wsRoot 是 OpenClaw workspace 根(generator 临时解出的);ctx 用来取 system.id 当备用。
// pre 由调用方传入,用于在主体之前插入 IDE 专属说明(如 Cursor 的"⚠ Cursor 模式限制"段);
// 想要只走默认主体的传 ""。
func writeIDEPromptBody(sb *strings.Builder, wsRoot string, ctx *Context, pre string) {
	if pre != "" {
		sb.WriteString(pre)
		if !strings.HasSuffix(pre, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// SOUL —— 行为风格 + 最高原则,跨平台通用
	if data := readWorkspaceMD(wsRoot, "SOUL.md"); data != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(data)
		if !strings.HasSuffix(data, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// IDENTITY —— 身份 + 典型示例,跨平台通用
	if data := readWorkspaceMD(wsRoot, "IDENTITY.md"); data != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(data)
		if !strings.HasSuffix(data, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 从 AGENTS.md 抽两段跨平台通用内容(其余段是 OpenClaw 专属,不拼):
	//   - "首次打招呼" —— 开场介绍 + 能力清单,IDE 用户也需要看到
	//   - "故障快报模板" —— 排障类回复的输出格式约束,跨平台通用
	if agents := readWorkspaceMD(wsRoot, "AGENTS.md"); agents != "" {
		// 排障入口段也保留:它告诉 agent "看到排障关键词先 Read incident-investigator/SKILL.md"。
		// IDE 平台用户问问题时这条流程一样适用。
		if section := extractMDSection(agents, "排障入口"); section != "" {
			sb.WriteString("---\n\n")
			sb.WriteString(section)
			sb.WriteString("\n\n")
		}
		if section := extractMDSection(agents, "输出形态"); section != "" {
			sb.WriteString("---\n\n")
			sb.WriteString(section)
			sb.WriteString("\n\n")
		}
		if section := extractMDSection(agents, "首次打招呼"); section != "" {
			sb.WriteString("---\n\n")
			sb.WriteString(section)
			sb.WriteString("\n\n")
		}
		if section := extractMDSection(agents, "故障快报模板"); section != "" {
			sb.WriteString("---\n\n")
			sb.WriteString(section)
			sb.WriteString("\n\n")
		}
	}

	// 不拼 CHECKLIST.md(全是 OpenClaw 自动执行步骤,IDE 用户没法跑)
	// 不拼 TOOLS.md(OpenClaw 权限边界声明)
	// 不拼 AGENTS.md 的"工作区路径"/"行为硬规则"/"出错应对"段(OpenClaw 专属)
	_ = ctx // 保留参数:未来可能按 ctx.System / ctx.Agent 字段裁剪
}

// writeSkillsIndex 拼跨平台通用的 skills 索引段(列每个 skill 的名字 + description)。
// 三家 IDE 都需要这段,让 agent 知道哪些 skill 可用。
//
// header 由调用方传(如"## Skills 索引" / "## 可用 Skills"),提示性的描述行也由调用方写,
// 因为各家提示用户怎么访问 skill 文件的路径不同(~/.claude/ vs ~/.cursor/ vs ~/.codex/)。
func writeSkillsIndex(sb *strings.Builder, wsRoot, header, hint string) {
	sb.WriteString("---\n\n")
	sb.WriteString(header)
	if !strings.HasSuffix(header, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	if hint != "" {
		sb.WriteString(hint)
		if !strings.HasSuffix(hint, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
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
		sb.WriteString("- **")
		sb.WriteString(e.Name())
		sb.WriteString("** — ")
		sb.WriteString(desc)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}
