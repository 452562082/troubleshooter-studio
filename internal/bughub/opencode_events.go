package bughub

import (
	"encoding/json"
	"strings"
)

// OpenCode 1.2 JSON events: text is provisional, step_finish/stop confirms it.
// Tool payloads and reasoning never enter Studio's progress feed.
func newOpenCodeStreamJSONParser() investigationEventParser {
	var text string
	var inputTokens, outputTokens int64
	finished := false
	return func(line []byte) (InvestigationEvent, string, string) {
		var payload map[string]any
		if json.Unmarshal(line, &payload) != nil {
			return InvestigationEvent{}, "", ""
		}
		part, _ := payload["part"].(map[string]any)
		switch stringFromAny(payload["type"]) {
		case "error":
			message := firstNonEmpty(stringFromAny(nestedAny(payload, "error", "data", "message")), stringFromAny(nestedAny(payload, "error", "name")), "OpenCode 执行失败")
			return InvestigationEvent{Type: "turn_failed", Message: message}, "", message
		case "step_start":
			if finished {
				return InvestigationEvent{Type: "turn_failed"}, "", "OpenCode 终态后出现新步骤"
			}
			text = ""
			return InvestigationEvent{Type: "thread_started", Message: "OpenCode 正在处理"}, "", ""
		case "text":
			text = stringFromAny(part["text"])
			if step, ok := parsePhaseStepMessage(text); ok {
				return step, "", ""
			}
			return InvestigationEvent{Type: "agent_message", Message: text}, "", ""
		case "tool_use":
			name := stringFromAny(part["tool"])
			kind := "mcp_tool_call"
			if name == "bash" {
				kind = "command_execution"
			}
			return InvestigationEvent{Type: kind, Message: name, Meta: map[string]any{"state": stringFromAny(nestedAny(part, "state", "status"))}}, "", ""
		case "step_finish":
			inputTokens += int64FromAny(nestedAny(part, "tokens", "input"))
			outputTokens += int64FromAny(nestedAny(part, "tokens", "output"))
			usage := map[string]any{"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}}
			reason := stringFromAny(part["reason"])
			if reason == "tool-calls" {
				text = ""
				return InvestigationEvent{Type: "usage", Raw: usage}, "", ""
			}
			if reason != "stop" || strings.TrimSpace(text) == "" {
				return InvestigationEvent{Type: "turn_failed"}, "", "OpenCode 未返回完整成功结果（" + reason + "）"
			}
			finished = true
			return InvestigationEvent{Type: "result", Message: "OpenCode 阶段执行完成", Raw: usage}, text, ""
		}
		return InvestigationEvent{}, "", ""
	}
}
