// uninstall_native.go —— claude-code / cursor 的卸载流程(对应 install_native.go)。
//
// 职责:从 ~/.claude/{agents,skills,scripts}/(或 ~/.cursor/...)摘掉已装机器人,
// 同时清掉中间包 ~/.tshoot/<target>/<id>/。两端都得清:中间包不清,BotsPage 仍能扫到。
//
// 跟 uninstall_native_openclaw.go 的区别:
//   - openclaw 装在 ~/.openclaw/workspace/<name>/ + ~/.openclaw/openclaw.json agents.list
//   - claude-code / cursor 装在用户级 ~/.claude|.cursor/{agents,skills,scripts}/<name>
//   - 共同:都需要清中间包 ~/.tshoot/<target>/<system_id>/
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UninstallNativeResult 通用卸载结果(claude-code / cursor 共用一个)。
type UninstallNativeResult struct {
	StagingMovedTo  string   // ~/.tshoot/<target>/<id>/ 移到 ~/.Trash/<...> 的路径;空 = 不存在或未动
	UserAgentMD     string   // 删掉的 ~/.claude|cursor/agents/<name>.md(或空)
	UserSkillsDir   string   // 删掉的 ~/.claude|cursor/skills/<name>/
	UserScriptsDir  string   // 删掉的 ~/.claude|cursor/scripts/<name>/
	Log             []string
}

// UninstallNative 卸载 claude-code / cursor 装的机器人。
// installedDir = ~/.tshoot/<target>/<system_id>/(BotsPage 扫到的中间包路径)。
// target 必须是 "claude-code" 或 "cursor"。
//
// 流程:
//  1. 中间包 → ~/.Trash(失败回退 RemoveAll)
//  2. 推断出 agent name(读中间包 agents/*.md 文件名),清 ~/.claude|cursor/agents/<name>.md(及 .bak)
//  3. 清 ~/.claude|cursor/skills/<name>/ 和 scripts/<name>/(都是命名空间隔离的,不会误删别 agent 的)
func UninstallNative(installedDir, target string) (*UninstallNativeResult, error) {
	var rootName string
	switch target {
	case "claude-code":
		rootName = ".claude"
	case "cursor":
		rootName = ".cursor"
	default:
		return nil, fmt.Errorf("uninstall_native: unsupported target %q", target)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("read $HOME: %w", err)
	}
	root := filepath.Join(home, rootName)

	res := &UninstallNativeResult{}
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }

	// 推断 agent name:从中间包 agents/*.md 找,跟 install_native.go::findAgentMD 同逻辑。
	// 中间包不存在/没 .md 时降级用 installedDir 的 basename(<system_id>),让卸载流程不卡。
	var agentName string
	if name, err := findInstalledAgentName(installedDir); err == nil {
		agentName = name
	} else {
		agentName = filepath.Base(installedDir)
		logf("[warn] 中间包没找到 agents/*.md(%v),用目录名 %q 当 agent 名兜底", err, agentName)
	}

	// 1) 中间包 → ~/.Trash(出错回退 RemoveAll)
	if _, err := os.Stat(installedDir); err == nil {
		ts := time.Now().Format("20060102-150405")
		bk := filepath.Join(home, ".Trash", agentName+"-"+target+"-uninstall-"+ts)
		if mkErr := os.MkdirAll(filepath.Dir(bk), 0o755); mkErr == nil {
			if err := os.Rename(installedDir, bk); err == nil {
				res.StagingMovedTo = bk
				logf("[ok] 中间包移到 %s", bk)
			} else if rmErr := os.RemoveAll(installedDir); rmErr == nil {
				logf("[ok] 中间包删除(rename to Trash 失败:%v,直接 rm)", err)
			} else {
				return res, fmt.Errorf("rename to Trash failed: %v; remove also failed: %v", err, rmErr)
			}
		}
	} else {
		logf("[skip] 中间包 %s 不存在", installedDir)
	}

	// 2) ~/.claude|cursor/agents/<name>.md(及备份)
	agentMD := filepath.Join(root, "agents", agentName+".md")
	if err := os.Remove(agentMD); err == nil {
		res.UserAgentMD = agentMD
		logf("[ok] %s 已删除", agentMD)
	} else if !os.IsNotExist(err) {
		logf("[warn] 删 %s 失败:%v", agentMD, err)
	}
	// 顺手清备份(install_native 生成的 .bak.YYYYMMDD-HHMMSS)
	bakMatches, _ := filepath.Glob(agentMD + ".bak.*")
	for _, bak := range bakMatches {
		if err := os.Remove(bak); err == nil {
			logf("[ok] %s 已删除", bak)
		}
	}

	// 3) skills / scripts 整目录
	skillsDir := filepath.Join(root, "skills", agentName)
	if err := os.RemoveAll(skillsDir); err == nil {
		if _, statErr := os.Stat(skillsDir); os.IsNotExist(statErr) {
			res.UserSkillsDir = skillsDir
			logf("[ok] %s 已删除", skillsDir)
		}
	}
	scriptsDir := filepath.Join(root, "scripts", agentName)
	if err := os.RemoveAll(scriptsDir); err == nil {
		if _, statErr := os.Stat(scriptsDir); os.IsNotExist(statErr) {
			res.UserScriptsDir = scriptsDir
			logf("[ok] %s 已删除", scriptsDir)
		}
	}

	logf("[done] uninstall(%s) 完成", target)
	return res, nil
}

// findInstalledAgentName 取中间包 agents/ 下第一个非 .bak 的 .md 文件名(去后缀)。
// 跟 install_native.go::findAgentMD 同逻辑,提出来给 uninstall 复用,逻辑保持一致。
func findInstalledAgentName(installedDir string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(installedDir, "agents"))
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".md") || strings.Contains(n, ".bak.") {
			continue
		}
		return strings.TrimSuffix(n, ".md"), nil
	}
	return "", fmt.Errorf("no agents/*.md in %s", installedDir)
}
