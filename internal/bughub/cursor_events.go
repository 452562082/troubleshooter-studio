package bughub

import (
	"encoding/json"
	"sort"
	"strings"
)

// ParseCursorStreamJSONEvent accepts only the terminal result as a final report.
// Prompt echoes and tool arguments/results are intentionally not projected to UI.
func ParseCursorStreamJSONEvent(line []byte) (InvestigationEvent, string, string) {
	var payload map[string]any
	if json.Unmarshal(line, &payload) != nil {
		return InvestigationEvent{}, "", ""
	}
	switch stringFromAny(payload["type"]) {
	case "system":
		if stringFromAny(payload["subtype"]) == "init" {
			return InvestigationEvent{Type: "thread_started", Message: "Cursor 会话已启动"}, "", ""
		}
	case "assistant":
		message := claudeMessageText(payload)
		if step, ok := parsePhaseStepMessage(message); ok {
			return step, "", ""
		}
		return InvestigationEvent{Type: "agent_message", Message: message}, "", ""
	case "tool_call":
		state := stringFromAny(payload["subtype"])
		if state != "started" && state != "completed" {
			break
		}
		call, _ := payload["tool_call"].(map[string]any)
		names := make([]string, 0, len(call))
		for name, value := range call {
			// Cursor also includes timestamps, hook context and toolCallId here.
			if _, ok := value.(map[string]any); ok && strings.HasSuffix(name, "ToolCall") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		name := "Cursor 工具"
		if len(names) > 0 {
			name = names[0]
		}
		kind := "mcp_tool_call"
		if strings.Contains(strings.ToLower(name), "shell") {
			kind = "command_execution"
		}
		return InvestigationEvent{Type: kind, Message: name, Meta: map[string]any{"state": state}}, "", ""
	case "result":
		result := stringFromAny(payload["result"])
		subtype := strings.ToLower(stringFromAny(payload["subtype"]))
		failed, _ := payload["is_error"].(bool)
		event := InvestigationEvent{Type: "result", Message: "Cursor 阶段执行完成"}
		if failed || subtype != "success" {
			event.Message = firstNonEmpty(result, stringFromAny(payload["message"]), "Cursor 执行失败")
			return event, "", event.Message
		}
		return event, result, ""
	case "error":
		message := firstNonEmpty(stringFromAny(payload["message"]), stringFromAny(payload["error"]), "Cursor 执行失败")
		return InvestigationEvent{Type: "turn_failed", Message: message}, "", message
	}
	return InvestigationEvent{}, "", ""
}

// newCursorStreamJSONParser keeps per-process state. Cursor's terminal result
// concatenates all assistant messages (including pre-tool commentary) without
// separators. Only after terminal success, prefer the last complete message
// when the terminal payload confirms it as its suffix. Never promote a partial
// assistant response or replace a distinct terminal result.
func newCursorStreamJSONParser() investigationEventParser {
	var lastAssistant string
	return func(line []byte) (InvestigationEvent, string, string) {
		event, final, failed := ParseCursorStreamJSONEvent(line)
		if event.Type == "agent_message" && strings.TrimSpace(event.Message) != "" {
			lastAssistant = event.Message
		}
		if event.Type == "result" && failed == "" && strings.TrimSpace(final) != "" && lastAssistant != "" && strings.HasSuffix(strings.TrimSpace(final), strings.TrimSpace(lastAssistant)) {
			final = lastAssistant
		}
		return event, final, failed
	}
}
