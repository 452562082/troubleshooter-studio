package bughub

import (
	"encoding/json"
	"strings"
)

func ParseCodexJSONLEvent(line []byte) (InvestigationEvent, string, string) {
	rawLine := strings.TrimSpace(string(line))
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawLine), &payload); err != nil {
		return InvestigationEvent{Type: "raw", Message: rawLine}, "", ""
	}

	eventType := stringFromAny(payload["type"])
	event := InvestigationEvent{
		Type:    eventType,
		Message: firstNonEmpty(stringFromAny(payload["message"]), eventType),
		Raw:     payload,
	}

	switch eventType {
	case "thread.started":
		event.Type = "thread_started"
		event.Message = "Codex 线程已启动"
	case "turn.started":
		event.Type = "turn_started"
		event.Message = "开始排障"
	case "turn.completed":
		event.Type = "turn_completed"
		event.Message = "排障完成"
	case "turn.failed":
		event.Type = "turn_failed"
		event.Message = codexErrorMessage(payload)
		return event, "", event.Message
	case "error":
		event.Type = "error"
		event.Message = codexErrorMessage(payload)
		return event, "", event.Message
	case "item.started", "item.completed":
		item, _ := payload["item"].(map[string]any)
		itemType := stringFromAny(item["type"])
		switch itemType {
		case "agent_message":
			event.Type = "agent_message"
			event.Message = stringFromAny(item["text"])
			return event, event.Message, ""
		case "command_execution":
			event.Type = "command_execution"
			event.Message = stringFromAny(item["command"])
		case "mcp_tool_call":
			event.Type = "mcp_tool_call"
			event.Message = firstNonEmpty(stringFromAny(item["name"]), "MCP tool call")
		default:
			event.Type = firstNonEmpty(itemType, eventType)
			event.Message = firstNonEmpty(stringFromAny(item["text"]), event.Message)
		}
	}

	return event, "", ""
}

func codexErrorMessage(payload map[string]any) string {
	errPayload, _ := payload["error"].(map[string]any)
	return firstNonEmpty(
		stringFromAny(errPayload["message"]),
		stringFromAny(errPayload["code"]),
		stringFromAny(payload["message"]),
		stringFromAny(payload["code"]),
		"Codex 运行失败",
	)
}
