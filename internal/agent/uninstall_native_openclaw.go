// uninstall_native_openclaw.go —— 把 ~/.openclaw/workspace/<name>/ 退役 +
// 从 openclaw.json 摘掉 agents.list 里的对应条目。原生 Go 实现,替代之前
// templates/scripts/uninstall.sh.tmpl 的 ~15 行 bash。
//
// 边界:
//   - MCP servers 不主动清理 —— 同一 cc 类型的 MCP(如 nacos-mcp-server-dev)
//     可能被多 agent 共享,清掉会断别人。让它留着,无害。
//   - workspace 移到 ~/.Trash 而不是直接 rm,方便误删找回
//   - <agent_id>-creds.json 主动清掉(per-agent,无共享)
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UninstallOpenclawResult 给 UI 展示"动了哪些资源"。
type UninstallOpenclawResult struct {
	WorkspaceMovedTo  string // 移到 ~/.Trash/<...>;空 = workspace 不存在,跳过
	OpenclawJSONClean bool   // true = agents.list 里成功摘了一条
	CredsRemoved      bool   // true = <agent_id>-creds.json 被清
	Log               []string
}

// UninstallNativeOpenclaw 卸载一个已部署的 openclaw agent。
// installedDir 是 ~/.openclaw/workspace/<name>/(tshoot.json 在那里);也接受
// staging dir(从 tshoot.json 读 workspace_name 后定位真正的 workspace)。
func UninstallNativeOpenclaw(installedDir string) (*UninstallOpenclawResult, error) {
	cfg, _, err := loadCfgFromTshoot(installedDir)
	if err != nil {
		return nil, fmt.Errorf("read tshoot.json: %w", err)
	}
	wsName := strings.TrimSpace(cfg.ResolveWorkspaceName())
	if wsName == "" {
		return nil, fmt.Errorf("无法确定 workspace 目录名:agent.id / agent.workspace_name 至少要有一个非空")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	res := &UninstallOpenclawResult{}
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }

	agentID := cfg.ResolveID()
	wsDir := filepath.Join(home, ".openclaw", "workspace", wsName)
	if _, err := os.Stat(wsDir); err == nil {
		// nanoTimestamp 防 1 秒内连点两次卸载撞 bk(秒精度时第二次 Rename 失败可能丢 workspace)。
		bk := filepath.Join(home, ".Trash", agentID+"-workspace-uninstall-"+nanoTimestamp())
		if err := os.MkdirAll(filepath.Dir(bk), 0o755); err != nil {
			return res, err
		}
		if err := os.Rename(wsDir, bk); err != nil {
			// 跨盘 rename 失败 → 退而 RemoveAll
			if rmErr := os.RemoveAll(wsDir); rmErr != nil {
				return res, fmt.Errorf("rename to Trash failed: %v; remove also failed: %v", err, rmErr)
			}
			logf("[ok] workspace 删除(rename to Trash 失败,直接 rm)")
		} else {
			res.WorkspaceMovedTo = bk
			logf("[ok] workspace 移到 %s", bk)
		}
	} else {
		logf("[skip] workspace %s 不存在", wsDir)
	}

	// 摘 agents.list + 清自家 MCP servers
	cfgPath := filepath.Join(home, ".openclaw", "openclaw.json")
	if data, err := readJSONOrEmpty(cfgPath); err == nil {
		dirty := false
		if removeAgentEntry(data, agentID) {
			res.OpenclawJSONClean = true
			dirty = true
			logf("[ok] %s 里 agents.list 已摘掉 %s", cfgPath, agentID)
		} else {
			logf("[skip] openclaw.json 里没找到 %s,无需清理", agentID)
		}
		// 删自家 MCP server keys。从 cfg 推 key 集合,跟 install 时 inject 的同款规则,
		// 用 system.id 当短前缀,不会跟别的 agent 撞名。改了 yaml 再装时残留的废 key
		// 也会被这一步清掉(不止本次注册的那批,前缀模糊匹配)。
		mcpPrefix := cfg.MCPKeyPrefix()
		mcp, _ := data["mcp"].(map[string]any)
		if mcp != nil {
			servers, _ := mcp["servers"].(map[string]any)
			if servers != nil {
				removed := []string{}
				for k := range servers {
					// 严格匹配:必须是 "<system.id>-..." 形式才删,避免误伤别人。
					// 老旧产物可能没改名,如果 prefix 跟 system.id 完全相等也删(罕见 corner case)。
					if strings.HasPrefix(k, mcpPrefix+"-") || k == mcpPrefix {
						delete(servers, k)
						removed = append(removed, k)
					}
				}
				if len(removed) > 0 {
					sort.Strings(removed)
					dirty = true
					logf("[ok] mcp.servers 摘掉 %d 项: %s", len(removed), strings.Join(removed, ", "))
				}
			}
		}
		if dirty {
			if err := writeJSONFile(cfgPath, data, 0o644); err != nil {
				return res, fmt.Errorf("write %s: %w", cfgPath, err)
			}
		}
	}

	// 清 creds.json
	credsPath := filepath.Join(home, ".openclaw", agentID+"-creds.json")
	if err := os.Remove(credsPath); err == nil {
		res.CredsRemoved = true
		logf("[ok] %s 已删除", credsPath)
	} else if !os.IsNotExist(err) {
		logf("[warn] 删 %s 失败:%v", credsPath, err)
	}

	logf("[done] uninstall 完成。本 agent 的 MCP servers 已从 openclaw.json 摘掉,不留垃圾条目。")
	return res, nil
}

// removeAgentEntry 从 root["agents"]["list"] 摘掉 id == agentID 的条目;
// 命中返回 true。结构不存在视为没命中。
func removeAgentEntry(root map[string]any, agentID string) bool {
	agents, _ := root["agents"].(map[string]any)
	if agents == nil {
		return false
	}
	list, _ := agents["list"].([]any)
	if len(list) == 0 {
		return false
	}
	out := make([]any, 0, len(list))
	matched := false
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if id, _ := m["id"].(string); id == agentID {
				matched = true
				continue
			}
		}
		out = append(out, item)
	}
	if !matched {
		return false
	}
	agents["list"] = out
	return true
}
