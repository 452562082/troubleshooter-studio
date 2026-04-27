// Package mcpcfg 封装各 AI 平台的 MCP server 配置文件路径和 JSON schema 差异,
// 让生成器 / 部署流程有一个权威的地方决定"把这个 MCP server 写到哪、长什么样"。
//
// 4 个 target 的真实配置位置(从本机 ~/.openclaw/ / ~/.claude.json /
// ~/.cursor/mcp.json / ~/Library/Application Support/Claude/ 反推):
//
//	openclaw      ~/.openclaw/openclaw.json           键名 "mcp.servers.<name>"(嵌套)
//	claude-code   <project>/.mcp.json                 键名 "mcpServers.<name>"(顶层)
//	              (或用户级 ~/.claude.json)
//	cursor        <project>/.cursor/mcp.json          键名 "mcpServers.<name>"
//	              (或全局 ~/.cursor/mcp.json)
//	embedded      Studio 直读 system.yaml,无独立 MCP 文件
//
// 每条 MCP server 的 value 结构在 4 个平台都一致:
//
//	{ "command": "uvx", "args": ["nacos-mcp-router@latest"],
//	  "env": {"NACOS_ADDR": "...", "NACOS_USERNAME": "..."} }
//
// 差别只在外层包装键(mcpServers vs mcp.servers)和文件位置。
package mcpcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target 对应 wizard 里的 target enum 值。
type Target string

const (
	TargetOpenClaw   Target = "openclaw"
	TargetClaudeCode Target = "claude-code"
	TargetCursor     Target = "cursor"
)

// Scope 配置文件作用范围。
type Scope string

const (
	ScopeUser    Scope = "user"    // 用户级:影响该用户所有项目
	ScopeProject Scope = "project" // 项目级:仅影响此项目根
)

// Resolved 表示"该 target + 该 projectRoot 组合下"MCP 配置要写到哪个文件。
// Schema:
//   NestedUnderMCP=true  → 顶层 {"mcp": {"servers": {<name>: ...}}}(openclaw)
//   NestedUnderMCP=false → 顶层 {"mcpServers": {<name>: ...}}(claude-code/cursor/claude-desktop)
type Resolved struct {
	Target         Target
	Scope          Scope
	Path           string // 绝对路径(展开 ~ 后)
	NestedUnderMCP bool
}

// ResolvePath 按 target + 可选 projectRoot 返 MCP 配置文件绝对路径及 schema 线索。
//
// 规则:
//   - openclaw:永远用户级 ~/.openclaw/openclaw.json;projectRoot 忽略
//   - claude-code / cursor:projectRoot 非空 → 项目级;空 → 用户级 fallback
//   - embedded:无独立文件,返 EmbeddedTarget=true
//
// 调用方通常在 wizard 部署流程里传 destPath(claude-code / cursor 的产物根)作 projectRoot。
func ResolvePath(target Target, projectRoot string) (*Resolved, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("read home: %w", err)
	}
	// 允许用户传 ~/foo:展开
	if strings.HasPrefix(projectRoot, "~/") {
		projectRoot = filepath.Join(home, projectRoot[2:])
	}
	switch target {
	case TargetOpenClaw:
		return &Resolved{
			Target:         target,
			Scope:          ScopeUser,
			Path:           filepath.Join(home, ".openclaw", "openclaw.json"),
			NestedUnderMCP: true, // {"mcp":{"servers":...}}
		}, nil
	case TargetClaudeCode:
		if projectRoot != "" {
			return &Resolved{
				Target:         target,
				Scope:          ScopeProject,
				Path:           filepath.Join(projectRoot, ".mcp.json"),
				NestedUnderMCP: false,
			}, nil
		}
		// 用户级 fallback:~/.claude.json(注意不是 ~/.claude/config.json —
		// 后者在本机是空的 {};MCP 真正住在 ~/.claude.json 的 mcpServers 键里)
		return &Resolved{
			Target:         target,
			Scope:          ScopeUser,
			Path:           filepath.Join(home, ".claude.json"),
			NestedUnderMCP: false,
		}, nil
	case TargetCursor:
		if projectRoot != "" {
			return &Resolved{
				Target:         target,
				Scope:          ScopeProject,
				Path:           filepath.Join(projectRoot, ".cursor", "mcp.json"),
				NestedUnderMCP: false,
			}, nil
		}
		return &Resolved{
			Target:         target,
			Scope:          ScopeUser,
			Path:           filepath.Join(home, ".cursor", "mcp.json"),
			NestedUnderMCP: false,
		}, nil
	default:
		return nil, fmt.Errorf("unknown target: %q", target)
	}
}
