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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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

// UninstallNativeResult 通用卸载结果(claude-code / cursor / codex 共用一个)。
type UninstallNativeResult struct {
	StagingMovedTo  string   // ~/.tshoot/<target>/<id>/ 移到 ~/.Trash/<...> 的路径;空 = 不存在或未动
	UserAgentMD     string   // 删掉的 agent 人格文件(claude/cursor:~/.<target>/agents/<name>.md;codex:~/.codex/AGENTS.md)
	UserSkillsDir   string   // 删掉的 ~/.<target>/skills/<name>/
	UserScriptsDir  string   // 删掉的 ~/.<target>/scripts/<name>/
	MCPRemoved      []string // 从 IDE 配置(settings.json/mcp.json/config.toml)清掉的 MCP server keys
	Log             []string
}

// UninstallNative 卸载 claude-code / cursor / codex 装的机器人。
// installedDir = ag.Path,即 <root>/skills/<name>/(BotsPage 扫到的真实部署目录)。
// <root> 通常是 ~/.claude / ~/.cursor / ~/.codex,但 wizard 里选过自定义目录时 <root>
// 就是那个 custom dir。target 必须是 "claude-code" / "cursor" / "codex"。
//
// 流程:
//  1. 真实部署目录(skills/<name>/)→ ~/.Trash(失败回退 RemoveAll)
//  2. agent name = installedDir basename;system_id 从 installedDir 里 tshoot.json 读
//  3. 清 <root>/agents/<name>.md(及 .bak)
//  4. 清 <root>/scripts/<name>/(skills/<name>/ 已在步骤 1 移走)
//  5. 清 staging 中间包 ~/.tshoot/<target>/<system_id>/(deploy 完已无用,残留干扰)
//
// <root> 的推导:从 installedDir 反推(installedDir = <root>/skills/<name>),
// 这样 default ~/.<target> 和 custom install root 都自动覆盖,不必额外传 customRoot。
func UninstallNative(installedDir, target string) (*UninstallNativeResult, error) {
	t, err := ParseIDETarget(target)
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("read $HOME: %w", err)
	}
	// <root> = installedDir 的祖父(installedDir = <root>/skills/<name>/);
	// 兜底:如果反推出来路径异常(没有 skills 段,或为空),回退到 ~/.<target>。
	root := deriveInstallRoot(installedDir, t, home)

	res := &UninstallNativeResult{}
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }

	// agent name = 真实部署目录的 basename(InstallNative 装的时候就是用这个名字)。
	// system_id 单独从 tshoot.json 读 —— staging 用 system_id 命名,跟 agent name 不同
	// (常见:agent name=truss-troubleshooter,system_id=truss)。
	agentName := filepath.Base(installedDir)
	systemID := readSystemIDFromMeta(filepath.Join(installedDir, discover.MetaFilename))

	// 1) 真实部署目录 → ~/.Trash(出错回退 RemoveAll)。从 Trash 走避免误删后无法恢复。
	// nanoTimestamp 防 1 秒内连点两次卸载撞 bk(秒精度时第二次 Rename 失败会 fallthrough 删数据)。
	bk := filepath.Join(home, ".Trash", agentName+"-"+target+"-uninstall-"+nanoTimestamp())
	movedTo, existed, mvErr := moveOutOrRemove(installedDir, bk)
	if mvErr != nil {
		return res, fmt.Errorf("uninstall installedDir: %w", mvErr)
	}
	switch {
	case !existed:
		logf("[skip] 已装目录 %s 不存在", installedDir)
	case movedTo != "":
		res.StagingMovedTo = movedTo
		logf("[ok] 已装目录移到 %s", movedTo)
	default:
		logf("[ok] 已装目录已删除(rename to Trash 失败,直接 rm)")
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

	// 2) agent 文件(及备份):
	//    claude / cursor: ~/.<root>/agents/<name>.md
	//    codex        : ~/.codex/agents/<name>.toml
	agentFile := filepath.Join(root, "agents", agentName+t.UserAgentExt())
	if err := os.Remove(agentFile); err == nil {
		res.UserAgentMD = agentFile
		logf("[ok] %s 已删除", agentFile)
	} else if !os.IsNotExist(err) {
		logf("[warn] 删 %s 失败:%v", agentFile, err)
	}
	// 顺手清备份(install_native 生成的 .bak.YYYYMMDD-HHMMSS)
	bakMatches, _ := filepath.Glob(agentFile + ".bak.*")
	for _, bak := range bakMatches {
		if err := os.Remove(bak); err == nil {
			logf("[ok] %s 已删除", bak)
		}
	}

	// 2b) codex 老版本残留清理:troubleshooter-studio 早期版本会另外往 ~/.codex/mcp.json
	// 写一份 Claude Code 风格的 mcpServers JSON(孤儿,codex 实际不读但还是会启动崩溃)。
	// 卸载时顺手清掉本机器人前缀的 server keys,避免下次装机叠加旧条目。
	if t == TargetCodex && systemID != "" {
		legacyMCPJSON := filepath.Join(root, "mcp.json")
		if data, rerr := os.ReadFile(legacyMCPJSON); rerr == nil {
			var obj map[string]any
			if json.Unmarshal(data, &obj) == nil {
				if servers, ok := obj["mcpServers"].(map[string]any); ok {
					removed := 0
					for k := range servers {
						if k == systemID || strings.HasPrefix(k, systemID+"-") {
							delete(servers, k)
							removed++
						}
					}
					if removed > 0 {
						obj["mcpServers"] = servers
						_ = writeJSONFile(legacyMCPJSON, obj, 0o644)
						logf("[ok] 清掉老版本 %s 里 %d 项遗留 MCP", legacyMCPJSON, removed)
					}
				}
			}
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

	// 4) IDE 配置里的 MCP server keys —— 清自家前缀("<system.id>-...")。
	// 老产物 ResolveID()+"-mcp-server-..." 形态以 systemID 开头也能命中,迁移期顺手清。
	if systemID != "" {
		removed := cleanIDEMCPServers(t, home, root, systemID, logf)
		res.MCPRemoved = removed
	}

	// 老产物清理:之前版本会下 mcp-grafana go 二进制到 <root>/bin/mcp-grafana
	// (Windows 加 .exe)。现已改走 npx mcp-grafana-npx,旧二进制留着没人用,遇到就清掉。
	// 老 codex agent 共用 + 其它 IDE 各一份的判断已不再适用 — 反正只是个 30MiB 的孤儿。
	for _, name := range []string{"mcp-grafana", "mcp-grafana.exe"} {
		legacy := filepath.Join(root, "bin", name)
		if _, err := os.Stat(legacy); err == nil {
			if rmErr := os.Remove(legacy); rmErr == nil {
				logf("[ok] 老 %s 二进制已删(改走 npx mcp-grafana-npx)", legacy)
			}
		}
	}
	_ = os.Remove(filepath.Join(root, "bin")) // 空目录顺带清

	logf("[done] uninstall(%s) 完成", target)
	return res, nil
}

// deriveInstallRoot 从 installedDir(= "<root>/skills/<name>/")反推 <root> 祖父目录。
// 路径异常(空 / 没 skills 段 / 非绝对路径)→ 回退默认 ~/.<target>/。
func deriveInstallRoot(installedDir string, t IDETarget, home string) string {
	defaultRoot := t.RootDir(home)
	if strings.TrimSpace(installedDir) == "" {
		return defaultRoot
	}
	abs, err := filepath.Abs(installedDir)
	if err != nil {
		return defaultRoot
	}
	parent := filepath.Dir(abs) // <root>/skills
	if filepath.Base(parent) != "skills" {
		return defaultRoot
	}
	return filepath.Dir(parent)
}

// cleanIDEMCPServers 从对应 IDE 配置里删本 system 名下的 MCP server keys。
//   - claude-code:~/.claude.json(dotfile)按 prefix 删 + 迁移期顺手清 ~/.claude/settings.json 残留
//   - cursor:<root>/mcp.json 按 prefix 删
//   - codex:codex mcp list → 匹配前缀逐个 codex mcp remove(不能手 marshal TOML 破坏其它段)
//
// 匹配 "<systemID>" 和 "<systemID>-..."(老产物 ResolveID 派生的 "<systemID>-troubleshooter-..."
// 也命中,迁移期顺手清)。返回真删了哪些 keys 给 UI 展示。
func cleanIDEMCPServers(t IDETarget, home, root, systemID string, logf func(format string, a ...any)) []string {
	prefix := systemID + "-"
	if t == TargetCodex {
		codexBin, err := exec.LookPath("codex")
		if err != nil {
			logf("[warn] 找不到 codex CLI,跳过清 ~/.codex/config.toml 里的 MCP servers")
			return nil
		}
		out, err := exec.Command(codexBin, "mcp", "list").Output()
		if err != nil {
			logf("[warn] codex mcp list 失败: %v(skip 清理)", err)
			return nil
		}
		var removed []string
		for line := range strings.SplitSeq(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] == "Name" {
				continue
			}
			name := fields[0]
			if name == systemID || strings.HasPrefix(name, prefix) {
				if err := exec.Command(codexBin, "mcp", "remove", name).Run(); err == nil {
					removed = append(removed, name)
				}
			}
		}
		if len(removed) > 0 {
			sort.Strings(removed)
			logf("[ok] codex config.toml 摘掉 %d 项 MCP: %s", len(removed), strings.Join(removed, ", "))
		}
		return removed
	}

	// MCPConfigPath 始终在 $HOME 下(claude-code → ~/.claude.json,cursor → ~/.cursor/mcp.json,
	// codex → 嵌入 agent toml)。IDE 的扩展目录都是 hardcoded,不存在"自定义位置"概念。
	cfgFile := t.MCPConfigPath(home)
	removed := pruneMCPKeysFromJSON(cfgFile, systemID, prefix, logf)

	// claude-code 迁移期:老版本写到 ~/.claude/settings.json 的残留也清掉。
	if t == TargetClaudeCode {
		legacyPath := filepath.Join(root, "settings.json")
		legacyRemoved := pruneMCPKeysFromJSON(legacyPath, systemID, prefix, logf)
		// 老残留摘出来的 key 不一定跟新位置一样,合并去重
		removed = mergeUnique(removed, legacyRemoved)
	}

	return removed
}

// pruneMCPKeysFromJSON 从 cfgFile 里 mcpServers map 删掉 key == systemID 或前缀 prefix 的项,
// 写回原文件(其它顶层字段保留)。文件不存在 / 无 mcpServers / 没命中 → 返回 nil。
func pruneMCPKeysFromJSON(cfgFile, systemID, prefix string, logf func(format string, a ...any)) []string {
	if cfgFile == "" {
		return nil
	}
	if _, err := os.Stat(cfgFile); err != nil {
		return nil
	}
	settings, err := readJSONOrEmpty(cfgFile)
	if err != nil {
		logf("[warn] %s 解析失败:%v(skip 清理)", cfgFile, err)
		return nil
	}
	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		return nil
	}
	var removed []string
	for k := range servers {
		if k == systemID || strings.HasPrefix(k, prefix) {
			delete(servers, k)
			removed = append(removed, k)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	sort.Strings(removed)
	if len(servers) == 0 {
		delete(settings, "mcpServers")
	} else {
		settings["mcpServers"] = servers
	}
	if err := writeJSONFile(cfgFile, settings, 0o644); err != nil {
		logf("[warn] 写 %s 失败:%v", cfgFile, err)
		return nil
	}
	logf("[ok] %s 摘掉 %d 项 MCP: %s", cfgFile, len(removed), strings.Join(removed, ", "))
	return removed
}

func mergeUnique(a, b []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
