// target.go —— IDE 平台 target 抽象。把"target string"散落在 install / uninstall /
// merge MCP 多个 switch 里的 ~/.<name>/ 根目录、settings 文件名、合法性判断收口到一处,
// 改一家行为只改这里。
//
// openclaw 不在本枚举里 —— 它走自己的 ~/.openclaw/ 全套逻辑(install_native_openclaw.go),
// 跟"用户级 IDE 装机"模型差太远,强行套同一抽象只会让接口形状奇怪。
package agent

import (
	"fmt"
	"strings"
)

// IDETarget 三家 IDE 的 target 标识。直接用 string 兼容现有调用方
// (CLI flag / wizard payload 都是字符串),不强转就能用。
type IDETarget string

const (
	TargetClaudeCode IDETarget = "claude-code"
	TargetCursor     IDETarget = "cursor"
	TargetCodex      IDETarget = "codex"
)

// allIDETargets 跟下面 spec 一一对齐;改一处必改另一处。
var allIDETargets = []IDETarget{TargetClaudeCode, TargetCursor, TargetCodex}

// ideSpec 单 target 的所有"路径/文件名/特性"参数,集中在一处声明。
type ideSpec struct {
	dirName      string // ~/.<dirName>/  e.g. ".claude"
	settingsFile string // 顶层 mcpServers JSON 文件名;codex 走 CLI 故为空
}

var ideSpecs = map[IDETarget]ideSpec{
	TargetClaudeCode: {dirName: ".claude", settingsFile: "settings.json"},
	TargetCursor:     {dirName: ".cursor", settingsFile: "mcp.json"},
	TargetCodex:      {dirName: ".codex", settingsFile: ""}, // 走 codex mcp add CLI,不读 JSON
}

// ParseIDETarget 校验并归一化 target 字符串。不在三家枚举内 → error。
// install / uninstall / merge MCP 的入口都该过一次这个函数,统一错误形态。
func ParseIDETarget(s string) (IDETarget, error) {
	t := IDETarget(s)
	if _, ok := ideSpecs[t]; !ok {
		return "", fmt.Errorf("unsupported IDE target %q (want one of: %s)",
			s, listIDETargets())
	}
	return t, nil
}

// DirName 返回 ~/.<这里>/ 的目录名。如 TargetClaudeCode → ".claude"。
func (t IDETarget) DirName() string { return ideSpecs[t].dirName }

// SettingsFilename 返回 IDE 写 mcpServers 的 JSON 文件名(顶层)。
// codex 走 CLI 注册不读 JSON,返回空串 —— 调用方对 codex 走专门分支,不该走 JSON 路径。
func (t IDETarget) SettingsFilename() string { return ideSpecs[t].settingsFile }

// listIDETargets 给错误信息用。
func listIDETargets() string {
	parts := make([]string, len(allIDETargets))
	for i, t := range allIDETargets {
		parts[i] = string(t)
	}
	return strings.Join(parts, " / ")
}
