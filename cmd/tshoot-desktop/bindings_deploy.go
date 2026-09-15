// 原生机器人卸载与工作目录操作。
package main

import (
	"fmt"
	"os/exec"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// RevealInFinder 在 Finder / Explorer 里打开 path（不是打开文件，是打开所在目录并高亮）。
// macOS 用 `open -R <path>`；Windows 未来需要分支。
func (a *App) RevealInFinder(path string) error {
	return exec.Command("open", "-R", path).Run()
}

// native 字段(StagingMovedTo / UserAgentMD / UserSkillsDir / UserScriptsDir)在 claude-code / cursor 时填。
type UninstallBotResult struct {
	Target         string   `json:"target"`
	StagingMovedTo string   `json:"staging_moved_to,omitempty"` // claude-code / cursor / codex
	UserAgentMD    string   `json:"user_agent_md,omitempty"`
	UserSkillsDir  string   `json:"user_skills_dir,omitempty"`
	UserScriptsDir string   `json:"user_scripts_dir,omitempty"`
	MCPRemoved     []string `json:"mcp_removed,omitempty"` // 从 IDE 配置清掉的 MCP server keys
	Log            []string `json:"log,omitempty"`
}

// UninstallBot 按 target 分派到对应卸载实现。BotsPage 的"卸载"按钮调这个,
// dir 一律传 BotsPage 列表里那条机器人的 path(中间包 / workspace 都接受 —— 各 target
// 实现内部会自己解析 tshoot.json 找真实位置)。
//
// 同步:成功卸载后从 ~/.tshoot/config.json deployed_bots 里也清掉对应记录,
// 否则 BotsPage 下次刷新会把它标 ghost(disk 已删但 deployed_bots 还在)。
func (a *App) UninstallBot(dir, target string) (*UninstallBotResult, error) {
	// 从 dir 反查 system_id —— 卸载成功后用来同步 RemoveDeployedBot。
	// 失败容忍:扫不到就 systemID="",RemoveDeployedBot 自己 nil-safe。
	var systemID string
	if found, _ := discover.Scan([]string{dir}); len(found) > 0 {
		systemID = found[0].Meta.SystemID
	}
	switch target {
	case "claude-code", "cursor", "codex", "opencode":
		r, err := agent.UninstallNative(dir, target)
		if err != nil {
			return nil, err
		}
		invalidateBotPathsCache() // bot 已卸,失效 5 分钟 cache
		_ = userconfig.RemoveDeployedBot(systemID, target)
		return &UninstallBotResult{
			Target:         target,
			StagingMovedTo: r.StagingMovedTo,
			UserAgentMD:    r.UserAgentMD,
			UserSkillsDir:  r.UserSkillsDir,
			UserScriptsDir: r.UserScriptsDir,
			MCPRemoved:     r.MCPRemoved,
			Log:            r.Log,
		}, nil
	default:
		return nil, fmt.Errorf("UninstallBot: unsupported target %q", target)
	}
}

// ForgetGhostBot 把 ~/.tshoot/config.json deployed_bots 里某条记录删掉。
// BotsPage 卡片对 ghost(disk 已不在的)bot 提供"忘掉它"入口,调这个;disk 上
// 没东西可删,只清 ghost 元数据。disk 还在的 bot 应走 UninstallBot,**不要**
// 通过这条绕过(否则会留下没追踪的 disk 残留)。
func (a *App) ForgetGhostBot(systemID, target string) error {
	return userconfig.RemoveDeployedBot(systemID, target)
}
