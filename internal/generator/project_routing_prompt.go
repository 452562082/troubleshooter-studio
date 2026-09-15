package generator

import "fmt"

func projectBoundAgentDescription(ctx *Context, agentName string, role AgentRole) string {
	return fmt.Sprintf(
		"%s。仅当 tshoot-router 已将当前项目解析到 %s，或用户明确点名系统 %s / Agent %s 时调用；不得按通用故障关键词自动选择。",
		roleDisplayName(ctx, role), ctx.System.Name, ctx.System.ID, agentName,
	)
}

func codexProjectOwnershipGate(ctx *Context, agentName string) string {
	return fmt.Sprintf(`## 项目归属门禁（任何业务动作之前执行）

本 Agent 是系统 **%s**（system_id=%s）的专属执行器，不是全局通用机器人。调用 MCP、读取业务 routing/代码或修改文件前，先运行：

`+"```bash"+`
python3 ~/.codex/skills/tshoot-router/scripts/resolve.py --cwd "$PWD" --expect-agent %q
`+"```"+`

只有返回 `+"`status=matched`"+` 且 `+"`allowed=true`"+` 才能继续。`+"`unmatched/ambiguous/allowed=false`"+` 时立即停止并返回路由结果，不得改选名称相似的机器人。

若用户明确点名 **%s / %s / %s**，或 Studio 任务上下文的“选定机器人”明确给出匹配的 `+"`system_id/agent_id`"+`，在命令末尾追加 `+"`--system %q`"+`；这属于显式绑定，可以不依赖 cwd。除此之外不得追加 `+"`--system`"+` 绕过项目判断。

`, ctx.System.Name, ctx.System.ID, agentName, ctx.System.Name, ctx.System.ID, agentName, ctx.System.ID)
}

func claudeProjectOwnershipGate(ctx *Context, agentName string) string {
	return fmt.Sprintf(`## 项目归属门禁

本 Agent 只属于 **%s**（system_id=%s）。任何 MCP、业务代码或配置查询前先运行：

`+"```bash"+`
python3 ~/.claude/skills/tshoot-router/scripts/resolve.py --cwd "$PWD" --expect-agent %q
`+"```"+`

仅 `+"`status=matched`"+` 且 `+"`allowed=true`"+` 可继续。用户明确点名 %s / %s / %s，或 Studio“选定机器人”上下文与本 Agent 一致时，可追加 `+"`--system %q`"+`。其他 unmatched、ambiguous 或归属不符必须停止，不得猜选。

`, ctx.System.Name, ctx.System.ID, agentName, ctx.System.Name, ctx.System.ID, agentName, ctx.System.ID)
}

func cursorProjectOwnershipGate(ctx *Context, agentName string) string {
	return fmt.Sprintf(`## 项目归属门禁

本 Agent 只属于 **%s**（system_id=%s）。仅在以下任一条件成立时继续：用户在 Cursor 明确选择/点名本 Agent；或 tshoot-router 已返回本 Agent ID；或 Studio“选定机器人”上下文明确匹配本系统。普通“报错/慢/失败/修复”等词不构成归属证据。

若当前 Cursor 会话可用终端，先执行 `+"`python3 ~/.cursor/skills/tshoot-router/scripts/resolve.py --cwd \"$PWD\" --expect-agent %q`"+`。结果不是 `+"`status=matched, allowed=true`"+` 时停止；无终端且用户也未显式选择本 Agent 时同样停止，不得猜选另一个机器人。

`, ctx.System.Name, ctx.System.ID, agentName)
}
