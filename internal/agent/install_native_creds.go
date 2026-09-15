// install_native_creds.go —— 给 Claude Code / Cursor / Codex 这三个 IDE 平台
// 写"通用 creds.json"。
//
// IDE 平台部署时**没有这个文件**,导致这些脚本在 Claude Code / Cursor / Codex 上跑
// 都报 "creds file missing"。
//
// 解决:IDE 平台部署时镜像写一份到 ~/.tshoot/<agent_id>-creds.json(平台无关位置),
//
// 派生,直接复用那段代码),保证两个文件 schema 一致,脚本只读不挑路径。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// WriteIDECredsFile 把 creds 按类型分 section 写到 ~/.tshoot/<agent_id>-creds.json
// (mode 0600)。creds=nil / 全 nacos 时直接 return(不动盘,避免空覆盖)。
//
// tshoot 兜底"的回退就能两端通吃。实质转发到 WriteCredsFileToHome("./tshoot", ...)。
func WriteIDECredsFile(cfg *config.SystemConfig, creds map[string]string) error {
	if creds == nil {
		return nil
	}
	get := func(k string) string { return creds[k] }
	return WriteCredsFileToHome(".tshoot", cfg, get)
}

// 调用方不关心。已存在的 creds.json 会 merge 而非覆盖(允许多次部署 / 多 agent 共存)。
//
// 全 nacos 的 cfg 不写(脚本不需要),避免噪音文件。
func WriteCredsFileToHome(homeSubdir string, cfg *config.SystemConfig, get func(string) string) error {
	// 任一源是 apollo/consul/env-vars/kuboard 才真有"非 MCP 读 creds.json"的需求。
	needs := cfg.Infrastructure.Observability.SkyWalking.Enabled
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if needsCreds(cc.Type) {
			needs = true
			break
		}
	}
	if cfg.Infrastructure.Observability.K8sRuntime.Enabled &&
		!strings.EqualFold(strings.TrimSpace(cfg.Infrastructure.Observability.K8sRuntime.Provider), "one2all") {
		needs = true
	}
	if !needs {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	dir := filepath.Join(home, homeSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	credsPath := filepath.Join(dir, cfg.ResolveID()+"-creds.json")
	// 已有就 merge(允许多次部署 / 不同 target 同 agent_id 共存)
	credsData, _ := readJSONOrEmpty(credsPath)
	writeCredsByType(credsData, cfg, get)
	if err := writeJSONFile(credsPath, credsData, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", credsPath, err)
	}
	return nil
}
