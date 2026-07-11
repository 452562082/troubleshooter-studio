package bughub

import (
	"fmt"
	"strings"
)

func GenerateContext(b Bug, bot BotRef) string {
	var sb strings.Builder
	line := func(format string, args ...any) {
		sb.WriteString(fmt.Sprintf(format, args...))
		sb.WriteByte('\n')
	}

	line("# Bug 排障上下文")
	line("")
	line("## Bug")
	line("- 来源: %s:%s", emptyDash(b.Source), emptyDash(b.SourceID))
	line("- 标题: %s", emptyDash(b.Title))
	line("- 状态: %s", emptyDash(b.Status))
	line("- 严重级别: %s", emptyDash(b.Severity))
	line("- 优先级: %s", emptyDash(b.Priority))
	botEnv := strings.TrimSpace(bot.Env)
	if botEnv == "" {
		botEnv = matchEnv(b)
	}
	line("- 环境: %s", emptyDash(effectiveBugEnv(b, bot)))
	line("- 排障机器人环境: %s", emptyDash(botEnv))
	line("- 系统: %s", emptyDash(b.SystemID))
	line("- 前端仓库: %s", emptyDash(b.FrontendRepo))
	line("- 前端 URL: %s", emptyDash(b.FrontendURL))
	line("- 指派: %s", emptyDash(b.Assignee))
	line("- 提交: %s", emptyDash(b.Reporter))
	line("")
	line("## 复现")
	line("%s", emptyDash(b.Steps))
	if b.Description != "" {
		line("")
		line("## 描述")
		line("%s", b.Description)
	}
	if b.Expected != "" || b.Actual != "" {
		line("")
		line("## 预期/实际")
		line("- 预期: %s", emptyDash(b.Expected))
		line("- 实际: %s", emptyDash(b.Actual))
	}
	writeList(&sb, "服务线索", b.ServiceHints)
	writeList(&sb, "API 路径", b.APIPaths)
	writeList(&sb, "Trace IDs", b.TraceIDs)
	writeList(&sb, "Request IDs", b.RequestIDs)
	if len(b.Attachments) > 0 {
		line("")
		line("## 附件")
		for _, a := range b.Attachments {
			parts := []string{emptyDash(a.Name)}
			if strings.TrimSpace(a.ID) != "" {
				parts = append(parts, "id="+strings.TrimSpace(a.ID))
			}
			if strings.TrimSpace(a.Type) != "" {
				parts = append(parts, "type="+strings.TrimSpace(a.Type))
			}
			if strings.TrimSpace(a.LocalPath) != "" {
				parts = append(parts, "local_path="+strings.TrimSpace(a.LocalPath))
			}
			if strings.TrimSpace(a.RemoteURL) != "" {
				parts = append(parts, "remote_url="+strings.TrimSpace(a.RemoteURL))
			}
			line("- %s", strings.Join(parts, " "))
		}
	}
	line("")
	line("## 选定机器人")
	line("- key: %s", emptyDash(bot.Key))
	line("- system_id: %s", emptyDash(bot.SystemID))
	line("- target: %s", emptyDash(bot.Target))
	line("- path: %s", emptyDash(bot.Path))
	line("")
	line("## 建议首轮排查")
	line("1. 优先根据 trace/request id 查链路与日志。")
	line("2. 若没有 trace id,用 API 路径和前端入口映射候选后端服务。")
	line("3. 对照最近变更、K8s 状态、配置中心和下游依赖定位根因。")
	return strings.TrimSpace(sb.String()) + "\n"
}

func effectiveBugEnv(b Bug, bot BotRef) string {
	return firstNonEmpty(strings.TrimSpace(b.Env), strings.TrimSpace(b.BotEnv), strings.TrimSpace(bot.Env))
}

func writeList(sb *strings.Builder, title string, items []string) {
	cleaned := cleanStrings(items)
	if len(cleaned) == 0 {
		return
	}
	sb.WriteString("\n## " + title + "\n")
	for _, item := range cleaned {
		sb.WriteString("- " + item + "\n")
	}
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func cleanStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}
