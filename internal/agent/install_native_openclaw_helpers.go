// install_native_openclaw_helpers.go —— Openclaw 安装的辅助函数集。
//
//	loadCfgFromTshoot   —— 从 staging / 已部署 dir 找 tshoot.json 反读 cfg + meta
//	injectAgent         —— openclaw.json 的 agents.list upsert
//	copyDirAll          —— 整目录拷贝(走 install_native.go 的 copyFileSimple,保留 mode + exec 位)
//	readEnvFileSimple / writeEnvFileSimple —— scripts/.env 读写,跟 deploy 同格式但内联避免循环依赖
//
// 主流程 InstallNativeOpenclaw 在 install_native_openclaw.go;MCP / creds 注入分别在
// install_native_openclaw_mcp.go / install_native_openclaw_creds.go。
package agent

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// loadCfgFromTshoot 从 dir 找 tshoot.json(两个候选位置)反读出 cfg + meta。
// 两个位置:
//   - <dir>/tshoot.json                              ← claude-code/cursor staging,或已部署的 openclaw workspace
//   - <dir>/templates/workspace-template/tshoot.json ← openclaw staging
//
// openclaw 故意在子目录写,避免 discover.Scan 扫到 staging 时跟已装 workspace
// 重复。所以查询时也分两路。
func loadCfgFromTshoot(dir string) (*config.SystemConfig, discover.Meta, error) {
	candidates := []string{
		filepath.Join(dir, discover.MetaFilename),
		filepath.Join(dir, "templates", "workspace-template", discover.MetaFilename),
	}
	var (
		data []byte
		err  error
	)
	for _, p := range candidates {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, discover.Meta{}, fmt.Errorf("read tshoot.json under %s: %w", dir, err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, meta, fmt.Errorf("parse tshoot.json: %w", err)
	}
	cfg, err := config.LoadFromBytes([]byte(meta.TroubleshooterYAML))
	if err != nil {
		return nil, meta, fmt.Errorf("troubleshooter.yaml in tshoot.json invalid: %w", err)
	}
	return cfg, meta, nil
}

// injectAgent 把 agent 注入 openclaw.json 的 agents.list,已存在(按 id 匹配)就不重复加。
func injectAgent(root map[string]any, id, name, model, workspace string) error {
	agents, _ := root["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
		root["agents"] = agents
	}
	listAny := agents["list"]
	list, _ := listAny.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if existID, _ := m["id"].(string); existID == id {
				setOpenClawAgentNetworkMode(m)
				return nil
			}
		}
	}
	agent := map[string]any{
		"id":        id,
		"name":      name,
		"model":     model,
		"workspace": workspace,
	}
	setOpenClawAgentNetworkMode(agent)
	list = append(list, agent)
	agents["list"] = list
	return nil
}

func setOpenClawAgentNetworkMode(agent map[string]any) {
	sandbox, _ := agent["sandbox"].(map[string]any)
	if sandbox == nil {
		sandbox = map[string]any{}
		agent["sandbox"] = sandbox
	}
	// Studio's workflow Agent needs host network access for runtime evidence,
	// MCP/API calls, dependency downloads, and Git push. Pin this per agent so a
	// restrictive agents.defaults.sandbox setting cannot silently break it.
	sandbox["mode"] = "off"
}

// copyDirAll:整目录拷贝,保留 mode。dst 必须不存在(由调用方保证)。
func copyDirAll(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if installShouldSkipGeneratedArtifact(d.Name()) {
				return fs.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if installShouldSkipGeneratedArtifact(d.Name()) {
			return nil
		}
		if err := copyFileSimple(p, target); err != nil {
			return err
		}
		// 保留 exec 位:Linux 上某些 skill 脚本要可执行
		if info, err := d.Info(); err == nil {
			_ = os.Chmod(target, info.Mode())
		}
		return nil
	})
}

// readEnvFileSimple 跟 deploy.ReadEnvFile 一样的格式,内联避免循环依赖。
func readEnvFileSimple(stagingDir string) (map[string]string, error) {
	envPath := filepath.Join(stagingDir, "scripts", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// 去外层引号 + bash '\'' 转回 '
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			quote := val[0]
			val = val[1 : len(val)-1]
			if quote == '\'' {
				val = strings.ReplaceAll(val, `'\''`, `'`)
			}
		}
		out[key] = val
	}
	return out, nil
}

// writeEnvFileSimple 跟 deploy.WriteEnvFile 同格式;空 map 等同 no-op。
func writeEnvFileSimple(stagingDir string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	envDir := filepath.Join(stagingDir, "scripts")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(envDir, ".env")
	var sb strings.Builder
	sb.WriteString("# 由 tshoot 桌面端写入。编辑前先备份。\n")
	sb.WriteString("# 删除此文件 = 下次 import 不再预填,需重新输入凭证。\n\n")
	for k, v := range kv {
		if k == "" {
			continue
		}
		escaped := strings.ReplaceAll(v, "'", `'\''`)
		fmt.Fprintf(&sb, "%s='%s'\n", k, escaped)
	}
	return os.WriteFile(envPath, []byte(sb.String()), 0o600)
}
