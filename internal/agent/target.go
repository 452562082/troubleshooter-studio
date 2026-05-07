// target.go —— IDE 平台 target 抽象。把"target string"散落在 install / uninstall /
// merge MCP 多个 switch 里的 ~/.<name>/ 根目录、settings 文件名、合法性判断收口到一处,
// 改一家行为只改这里。
//
// openclaw 不在本枚举里 —— 它走自己的 ~/.openclaw/ 全套逻辑(install_native_openclaw.go),
// 跟"用户级 IDE 装机"模型差太远,强行套同一抽象只会让接口形状奇怪。
package agent

import (
	"fmt"
	"path/filepath"
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
	dirName string // ~/.<dirName>/  e.g. ".claude"
	// mcpHomeRel:相对 $HOME 的 MCP 配置 JSON 文件路径(顶层 "mcpServers" 字段)。
	//   - claude-code → ".claude.json"  (dotfile,**不**在 ~/.claude/ 下;
	//                                    Claude Code CLI 启动时强绑死从 $HOME 读这个文件,
	//                                    ~/.claude/settings.json 是 hooks/permissions/env 用的,
	//                                    不读 mcpServers 字段)
	//   - cursor      → ".cursor/mcp.json"
	//   - codex       → ""              (MCP 嵌入 agent toml 内联段,无独立 JSON 配置)
	mcpHomeRel string
	// agentExt 是 <root>/agents/<name>.<这里> 的扩展名(含点)。
	//   - claude-code / cursor → ".md"  (markdown subagent profile, frontmatter name+description+...)
	//   - codex            → ".toml" (TOML subagent definition; 见 https://developers.openai.com/codex/subagents)
	agentExt string
}

var ideSpecs = map[IDETarget]ideSpec{
	TargetClaudeCode: {dirName: ".claude", mcpHomeRel: ".claude.json", agentExt: ".md"},
	TargetCursor:     {dirName: ".cursor", mcpHomeRel: ".cursor/mcp.json", agentExt: ".md"},
	TargetCodex:      {dirName: ".codex", mcpHomeRel: "", agentExt: ".toml"},
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

// RootDir 返回 IDE 用户级安装根目录的绝对路径(始终 $HOME/<DirName>)。
// home 由调用方传(通常是 os.UserHomeDir() 的结果),便于测试时注入 fakeHome。
//
// 收口的小 helper:install / uninstall / merge MCP 三家都要"home + DirName" 的拼,
// 之前各自 filepath.Join 现在统一走这里,改路径形态(比如未来加 XDG_CONFIG_HOME 兜底)
// 一处即生效。
func (t IDETarget) RootDir(home string) string {
	return filepath.Join(home, t.DirName())
}

// MCPConfigPath 返回 IDE 用户级 MCP 配置 JSON 文件的绝对路径(顶层 "mcpServers" 字段)。
//
//   - claude-code → $HOME/.claude.json。Claude Code CLI 启动时固定从 $HOME 读这个 dotfile。
//     早期实现写到 ~/.claude/settings.json 是 bug —— 那个文件用于 hooks/permissions/env,
//     Claude Code 不在那里读 mcpServers,装好的 MCP 在 `claude mcp list` 永远看不到。
//   - cursor      → $HOME/.cursor/mcp.json。
//   - codex       → 空串。codex MCP 嵌入 agent toml 内联段,没有独立 JSON 配置。
//     调用方对 codex 走专门分支,不该走 JSON 路径。
func (t IDETarget) MCPConfigPath(home string) string {
	rel := ideSpecs[t].mcpHomeRel
	if rel == "" {
		return ""
	}
	return filepath.Join(home, rel)
}

// MCPConfigDisplay 返回展示用的"~/路径"形式,给 install 末尾提示文案用。
// 不存在配置文件时(codex)返回空串。
func (t IDETarget) MCPConfigDisplay() string {
	rel := ideSpecs[t].mcpHomeRel
	if rel == "" {
		return ""
	}
	return "~/" + rel
}

// UserAgentExt 返回 <root>/agents/<name>.<这里> 的扩展名(含点)。
// 例:claude-code / cursor → ".md";codex → ".toml"。
func (t IDETarget) UserAgentExt() string { return ideSpecs[t].agentExt }

// listIDETargets 给错误信息用。
func listIDETargets() string {
	parts := make([]string, len(allIDETargets))
	for i, t := range allIDETargets {
		parts[i] = string(t)
	}
	return strings.Join(parts, " / ")
}
