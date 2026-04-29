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
	"strings"
	"time"
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
		ts := time.Now().Format("20060102-150405")
		bk := filepath.Join(home, ".Trash", agentID+"-workspace-uninstall-"+ts)
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

	// 摘 agents.list
	cfgPath := filepath.Join(home, ".openclaw", "openclaw.json")
	if data, err := readJSONOrEmpty(cfgPath); err == nil {
		if removeAgentEntry(data, agentID) {
			if err := writeJSONFile(cfgPath, data, 0o644); err != nil {
				return res, fmt.Errorf("write %s: %w", cfgPath, err)
			}
			res.OpenclawJSONClean = true
			logf("[ok] %s 里 agents.list 已摘掉 %s", cfgPath, agentID)
		} else {
			logf("[skip] openclaw.json 里没找到 %s,无需清理", agentID)
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

	logf("[done] uninstall 完成。MCP servers (nacos-mcp-server-* 等) 保留,可能被其它 agent 共享。")
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
