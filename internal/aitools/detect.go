// Package aitools 探测本机是否装了 Claude Code / Cursor IDE,用于向导 Step 2 target
// 卡片的"已安装 ✓ / 未安装 ⚠"状态标。跟 OpenClaw 不一样:这俩 target 不从它们的
// 配置读模型(claude-code 靠 --model CLI flag,cursor 由自家 subscription 管),
// 所以这里只做"装了没 + 什么版本"的信息展示,不抓 model 列表。
//
// 探测策略:
//   - Claude Code CLI:在 PATH 里找 claude 二进制;能跑 `claude --version` 就认
//     装了,版本号从输出抓(eg "2.1.118 (Claude Code)")。
//   - Claude 桌面 app:/Applications/Claude.app 里 Info.plist 的版本字段。
//     跟 Claude Code CLI 不是一回事(桌面 app 是独立的 chat UI);wizard target 是 claude-code,
//     所以优先看 CLI,Desktop 只在 CLI 不在时做 fallback 信息。
//   - Cursor IDE:macOS /Applications/Cursor.app 的 Info.plist
//     (Linux / Windows 路径约定不同,当前只处理 macOS —— 其它平台返回 installed=false
//     + 一句 note 说"本检测只覆盖 macOS")。
package aitools

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Result 单一工具的探测结果。Installed=false 时其它字段可能空。
type Result struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Path 能定位到的"权威位置":
	//   Claude Code → claude 二进制绝对路径
	//   Claude Desktop → Claude.app 绝对路径
	//   Cursor → Cursor.app 绝对路径
	Path string `json:"path,omitempty"`
	// Note 给 UI 的附加提示:比如"只覆盖 macOS"、"检测到但版本读取失败"等,非致命
	Note string `json:"note,omitempty"`
}

// DetectClaudeCode 查 PATH 里的 claude CLI(Claude Code 的入口)。
// 版本从 `claude --version` stdout 抓;抓不到也没关系,installed=true 即可。
func DetectClaudeCode() *Result {
	res := &Result{}
	p, err := exec.LookPath("claude")
	if err != nil {
		// fallback:~/.claude/settings.json 存在也算装了(用户可能用别名/手动装脚本)
		home, herr := os.UserHomeDir()
		if herr == nil {
			if _, serr := os.Stat(filepath.Join(home, ".claude", "settings.json")); serr == nil {
				res.Installed = true
				res.Note = "PATH 里没找到 claude 二进制,但 ~/.claude/settings.json 在,按已装处理"
				return res
			}
		}
		return res
	}
	res.Installed = true
	res.Path = p
	// 拿版本;超时 / 失败就不强求,installed 已经 true
	cmd := exec.Command(p, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		line := strings.TrimSpace(out.String())
		// 典型输出 "2.1.118 (Claude Code)"
		if line != "" {
			// 只取 "X.Y.Z" 那段,避免把 "(Claude Code)" 后缀塞进 version 字段
			fields := strings.Fields(line)
			if len(fields) > 0 {
				res.Version = fields[0]
			}
		}
	}
	return res
}

// DetectCursor 查 Cursor IDE 安装。当前只做 macOS 的 /Applications/Cursor.app。
// 版本从 Info.plist 的 CFBundleShortVersionString 读(不依赖 PlistBuddy,自己扫 xml 找键值对)。
func DetectCursor() *Result {
	res := &Result{}
	if runtime.GOOS != "darwin" {
		res.Note = "当前只实现了 macOS 的 Cursor 探测(Linux/Windows 请手填)"
		return res
	}
	appPath := "/Applications/Cursor.app"
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	raw, err := os.ReadFile(plist)
	if err != nil {
		return res // Installed=false
	}
	res.Installed = true
	res.Path = appPath
	res.Version = readPlistStringKey(raw, "CFBundleShortVersionString")
	return res
}

// readPlistStringKey 从一个 Info.plist 字节串里抠指定 key 的 string 值。
// Info.plist 是 xml 格式:
//
//	<key>CFBundleShortVersionString</key>
//	<string>2.6.20</string>
//
// 简单字符串扫描而非正则/xml 解析 —— 够用且零依赖。匹配不到返 ""。
func readPlistStringKey(raw []byte, key string) string {
	s := string(raw)
	needle := fmt.Sprintf("<key>%s</key>", key)
	idx := strings.Index(s, needle)
	if idx < 0 {
		return ""
	}
	after := s[idx+len(needle):]
	// 找下一个 <string>...</string>
	open := strings.Index(after, "<string>")
	if open < 0 {
		return ""
	}
	start := open + len("<string>")
	close := strings.Index(after[start:], "</string>")
	if close < 0 {
		return ""
	}
	return strings.TrimSpace(after[start : start+close])
}
