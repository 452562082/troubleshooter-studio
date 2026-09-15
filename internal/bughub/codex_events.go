package bughub

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type investigationEventParser func([]byte) (InvestigationEvent, string, string)

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
		event.Meta = map[string]any{"state": strings.TrimPrefix(eventType, "item.")}
		switch itemType {
		case "agent_message":
			text := stringFromAny(item["text"])
			if step, ok := parsePhaseStepMessage(text); ok {
				step.Raw = payload
				return step, "", ""
			}
			event.Type = "agent_message"
			event.Message = text
			return event, event.Message, ""
		case "command_execution":
			event.Type = "command_execution"
			event.Message = stringFromAny(item["command"])
			if status := stringFromAny(item["status"]); status != "" {
				event.Meta["status"] = status
			}
			if exitCode, ok := item["exit_code"].(float64); ok {
				event.Meta["exit_code"] = int(exitCode)
			}
		case "mcp_tool_call":
			event.Type = "mcp_tool_call"
			event.Message = firstNonEmpty(stringFromAny(item["name"]), stringFromAny(item["tool"]), "MCP tool call")
		default:
			event.Type = firstNonEmpty(itemType, eventType)
			event.Message = firstNonEmpty(stringFromAny(item["text"]), event.Message)
		}
	}

	return event, "", ""
}

func ParseClaudeStreamJSONEvent(line []byte) (InvestigationEvent, string, string) {
	rawLine := strings.TrimSpace(string(line))
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawLine), &payload); err != nil {
		return InvestigationEvent{Type: "raw", Message: rawLine}, "", ""
	}

	eventType := stringFromAny(payload["type"])
	event := InvestigationEvent{
		Type:    firstNonEmpty(eventType, "event"),
		Message: stringFromAny(payload["message"]),
		Raw:     payload,
	}

	switch eventType {
	case "system", "user":
		event.Type = eventType
		event.Message = ""
		if eventType == "user" {
			if tool, ok := claudeToolEvent(payload, "tool_result"); ok {
				return tool, "", ""
			}
		}
	case "assistant":
		text := claudeMessageText(payload)
		if strings.TrimSpace(text) == "" {
			if tool, ok := claudeToolEvent(payload, "tool_use"); ok {
				return tool, "", ""
			}
		}
		if step, ok := parsePhaseStepMessage(text); ok {
			step.Raw = payload
			return step, "", ""
		}
		event.Type = "agent_message"
		event.Message = text
	case "result":
		final := firstNonEmpty(stringFromAny(payload["result"]), stringFromAny(payload["message"]))
		subtype := stringFromAny(payload["subtype"])
		event.Type = "result"
		event.Message = firstNonEmpty(final, subtype, "Claude Code 完成")
		isError, _ := payload["is_error"].(bool)
		if isError || strings.Contains(strings.ToLower(subtype), "error") || strings.Contains(strings.ToLower(subtype), "fail") {
			return event, "", event.Message
		}
		return event, final, ""
	case "error":
		event.Type = "error"
		event.Message = firstNonEmpty(stringFromAny(payload["error"]), stringFromAny(payload["message"]), "Claude Code 运行失败")
		return event, "", event.Message
	}
	return event, "", ""
}

var phaseStepPattern = regexp.MustCompile(`^\[\[TSHOOT_STEP phase=(investigation) index=([1-7]) key=([a-z_]+)\]\]$`)

var investigationPhaseSteps = []struct {
	Key   string
	Label string
}{
	{Key: "evidence_handoff", Label: "接收验证证据"},
	{Key: "timeline", Label: "时间轴与最近变更"},
	{Key: "runtime_scope", Label: "横向运行时检查"},
	{Key: "dependency_chain", Label: "依赖与调用链"},
	{Key: "evidence_correlation", Label: "多维证据交叉"},
	{Key: "root_cause", Label: "根因收敛"},
	{Key: "knowledge_sink", Label: "沉淀与结果"},
}

// parsePhaseStepMessage accepts only the fixed Studio progress protocol. The
// label is resolved locally instead of trusting arbitrary Agent output, so the
// UI can safely render a stable seven-step workflow.
func parsePhaseStepMessage(message string) (InvestigationEvent, bool) {
	match := phaseStepPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 4 {
		return InvestigationEvent{}, false
	}
	index, err := strconv.Atoi(match[2])
	if err != nil || index < 1 || index > len(investigationPhaseSteps) {
		return InvestigationEvent{}, false
	}
	step := investigationPhaseSteps[index-1]
	if match[3] != step.Key {
		return InvestigationEvent{}, false
	}
	return InvestigationEvent{
		Type:    "phase_step",
		Message: step.Label,
		Meta: map[string]any{
			"phase":      match[1],
			"step_key":   step.Key,
			"step_index": index,
			"step_total": len(investigationPhaseSteps),
			"state":      "running",
		},
	}, true
}

func claudeMessageText(payload map[string]any) string {
	message, _ := payload["message"].(map[string]any)
	content, _ := message["content"].([]any)
	var parts []string
	for _, item := range content {
		m, _ := item.(map[string]any)
		if stringFromAny(m["type"]) == "text" {
			parts = append(parts, stringFromAny(m["text"]))
		}
	}
	return strings.Join(parts, "\n")
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

// Codex agent_message is also used for intermediate text. Only a completed turn
// can authorize its last message as a final result.
func newCodexStreamJSONParser() investigationEventParser {
	var lastMessage string
	return func(line []byte) (InvestigationEvent, string, string) {
		event, message, failed := ParseCodexJSONLEvent(line)
		if event.Type == "agent_message" && event.Meta["state"] == "completed" && message != "" {
			lastMessage = message
		}
		if event.Type == "turn_started" {
			lastMessage = ""
		}
		if event.Type == "turn_completed" && failed == "" {
			return event, lastMessage, ""
		}
		return event, "", failed
	}
}

// Surface tool lifecycle without copying arguments, results or tool output to
// progress events. The phase runner separately validates all evidence artifacts.
func claudeToolEvent(payload map[string]any, blockType string) (InvestigationEvent, bool) {
	message, _ := payload["message"].(map[string]any)
	content, _ := message["content"].([]any)
	for _, block := range content {
		item, _ := block.(map[string]any)
		if stringFromAny(item["type"]) != blockType {
			continue
		}
		event := InvestigationEvent{Type: "mcp_tool_call", Message: "Claude 工具", Meta: map[string]any{"state": "completed"}}
		if blockType == "tool_use" {
			event.Message = firstNonEmpty(stringFromAny(item["name"]), event.Message)
			event.Meta["state"] = "started"
			if event.Message == "Bash" {
				event.Type = "command_execution"
			}
		}
		return event, true
	}
	return InvestigationEvent{}, false
}
