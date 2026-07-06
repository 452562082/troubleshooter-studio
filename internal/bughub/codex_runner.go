package bughub

import "strings"

func BuildCodexInvestigationPrompt(b Bug, bot BotRef) string {
	var sb strings.Builder
	sb.WriteString("请作为选定的 Codex 排障机器人开始排障。\n")
	sb.WriteString("目标：基于下面 Bug 工单上下文，完成只读根因分析，输出可执行结论和下一步建议。\n")
	sb.WriteString("约束：默认不要修改代码，不要执行破坏性命令；如必须写入或重启服务，先在结论中说明需要人工确认。\n\n")
	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString("\n请按以下结构输出：\n")
	sb.WriteString("1. 现象复述\n2. 已验证事实\n3. 最可能根因\n4. 建议排查命令或证据\n5. 需要用户补充的信息\n")
	return sb.String()
}
