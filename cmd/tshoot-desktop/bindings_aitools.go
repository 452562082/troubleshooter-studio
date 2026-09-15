// bindings_aitools.go —— 探测本机 Claude Code CLI / Cursor IDE 安装状态。
// wizard Step 2 target 卡片用来显示"✓ 已装 v2.1.118 / ⚠ 未检测到"徽标。
// 仅做"装了没"展示,帮用户避免"勾了 claude-code 但本机根本没装" 的尴尬。
package main

import (
	"github.com/xiaolong/troubleshooter-studio/internal/aitools"
)

// AIToolsDetectResult 给前端的合并结构。一次请求拿两家状态,UI 两张卡片各自读。
type AIToolsDetectResult struct {
	OpenCode   *aitools.Result `json:"opencode"`
	ClaudeCode *aitools.Result `json:"claude_code"`
	Cursor     *aitools.Result `json:"cursor"`
	Codex      *aitools.Result `json:"codex"`
}

func (a *App) DetectAITools() *AIToolsDetectResult {
	return &AIToolsDetectResult{
		OpenCode:   aitools.DetectOpenCode(),
		ClaudeCode: aitools.DetectClaudeCode(),
		Cursor:     aitools.DetectCursor(),
		Codex:      aitools.DetectCodex(),
	}
}
