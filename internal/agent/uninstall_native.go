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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// readSystemIDFromMeta 从 tshoot.json 读 system_id 字段。读不到返回空串(调用方 fallback)。
func readSystemIDFromMeta(metaPath string) string {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var m struct {
		SystemID string `json:"system_id"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m.SystemID
}

// UninstallNativeResult 通用卸载结果(claude-code / cursor 共用一个)。
type UninstallNativeResult struct {
	StagingMovedTo  string   // ~/.tshoot/<target>/<id>/ 移到 ~/.Trash/<...> 的路径;空 = 不存在或未动
	UserAgentMD     string   // 删掉的 ~/.claude|cursor/agents/<name>.md(或空)
	UserSkillsDir   string   // 删掉的 ~/.claude|cursor/skills/<name>/
	UserScriptsDir  string   // 删掉的 ~/.claude|cursor/scripts/<name>/
	Log             []string
}

// UninstallNative 卸载 claude-code / cursor 装的机器人。
// installedDir = ag.Path(BotsPage 扫到的真实部署目录,2026-04-30 起 = ~/.claude/skills/<name>/
//                或 ~/.cursor/skills/<name>/,跟 OpenClaw 一样指向真实位置)。
// target 必须是 "claude-code" 或 "cursor"。
//
// 流程:
//  1. 真实部署目录(skills/<name>/)→ ~/.Trash(失败回退 RemoveAll)
//  2. agent name = installedDir basename;system_id 从 installedDir 里 tshoot.json 读
//  3. 清 ~/.claude|cursor/agents/<name>.md(及 .bak)
//  4. 清 ~/.claude|cursor/scripts/<name>/(skills/<name>/ 已在步骤 1 移走)
//  5. 清 staging 中间包 ~/.tshoot/<target>/<system_id>/(deploy 完已无用,残留干扰)
func UninstallNative(installedDir, target string) (*UninstallNativeResult, error) {
	var rootName string
	switch target {
	case "claude-code":
		rootName = ".claude"
	case "cursor":
		rootName = ".cursor"
	case "codex":
		rootName = ".codex"
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

	// agent name = 真实部署目录的 basename(InstallNative 装的时候就是用这个名字)。
	// system_id 单独从 tshoot.json 读 —— staging 用 system_id 命名,跟 agent name 不同
	// (常见:agent name=truss-troubleshooter,system_id=truss)。
	agentName := filepath.Base(installedDir)
	systemID := readSystemIDFromMeta(filepath.Join(installedDir, discover.MetaFilename))

	// 1) 真实部署目录 → ~/.Trash(出错回退 RemoveAll)。从 Trash 走避免误删后无法恢复。
	if _, err := os.Stat(installedDir); err == nil {
		ts := time.Now().Format("20060102-150405")
		bk := filepath.Join(home, ".Trash", agentName+"-"+target+"-uninstall-"+ts)
		if mkErr := os.MkdirAll(filepath.Dir(bk), 0o755); mkErr == nil {
			if err := os.Rename(installedDir, bk); err == nil {
				res.StagingMovedTo = bk
				logf("[ok] 已装目录移到 %s", bk)
			} else if rmErr := os.RemoveAll(installedDir); rmErr == nil {
				logf("[ok] 已装目录删除(rename to Trash 失败:%v,直接 rm)", err)
			} else {
				return res, fmt.Errorf("rename to Trash failed: %v; remove also failed: %v", err, rmErr)
			}
		}
	} else {
		logf("[skip] 已装目录 %s 不存在", installedDir)
	}

	// 1b) staging 中间包(~/.tshoot/<target>/<system_id>/)— 部署中途临时落盘,装完已无用。
	if systemID != "" {
		stagingDir := filepath.Join(home, ".tshoot", target, systemID)
		if _, err := os.Stat(stagingDir); err == nil {
			if rmErr := os.RemoveAll(stagingDir); rmErr == nil {
				logf("[ok] staging 中间包 %s 清掉", stagingDir)
			} else {
				logf("[warn] 清 staging %s 失败:%v", stagingDir, rmErr)
			}
		}
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

// findInstalledAgentName 老逻辑:从 staging 中间包 agents/ 下抽 agent 名。2026-04-30 起
// uninstall 路径接收的是真实部署目录(~/.claude|cursor/skills/<name>/),agent 名直接 = basename,
// 不再走这个 helper。保留代码以备 OpenClaw 卸载或 staging 路径走 fallback 时调用。
func findInstalledAgentName(installedDir string) (string, error) { //nolint:unused
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
