// install_native_creds.go —— 给 Claude Code / Cursor / Codex 这三个 IDE 平台
// 写"通用 creds.json"。
//
// 背景:OpenClaw 的脚本(resolve_runtime_*.py / apollo_config.py / consul_config.py
// / kuboard 配套)固化读 ~/.openclaw/<agent_id>-creds.json,这是 OpenClaw 自家约定。
// IDE 平台部署时**没有这个文件**,导致这些脚本在 Claude Code / Cursor / Codex 上跑
// 都报 "creds file missing"。
//
// 解决:IDE 平台部署时镜像写一份到 ~/.tshoot/<agent_id>-creds.json(平台无关位置),
// 各脚本加双路径回退(openclaw 优先,tshoot 兜底)。
//
// 内容跟 install_native_openclaw.go::writeCredsByType 完全一致(同一个 cfg + creds
// 派生,直接复用那段代码),保证两个文件 schema 一致,脚本只读不挑路径。
package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// WriteIDECredsFile 把 creds 按类型分 section 写到 ~/.tshoot/<agent_id>-creds.json
// (mode 0600)。creds=nil / 全 nacos 时直接 return(不动盘,避免空覆盖)。
//
// 跟 OpenClaw 自家 ~/.openclaw/<id>-creds.json 同 schema,脚本走"openclaw 优先 +
// tshoot 兜底"的回退就能两端通吃。实质转发到 WriteCredsFileToHome("./tshoot", ...)。
func WriteIDECredsFile(cfg *config.SystemConfig, creds map[string]string) error {
	if creds == nil {
		return nil
	}
	get := func(k string) string { return creds[k] }
	return WriteCredsFileToHome(".tshoot", cfg, get)
}

// WriteCredsFileToHome 是 IDE / openclaw 共用的"写 <agent_id>-creds.json"实现。
// homeSubdir 是 $HOME 下的子目录("./openclaw" / ".tshoot" 二选一);路径由这层算,
// 调用方不关心。已存在的 creds.json 会 merge 而非覆盖(允许多次部署 / 多 agent 共存)。
//
// 全 nacos 的 cfg 不写(脚本不需要),避免噪音文件。
func WriteCredsFileToHome(homeSubdir string, cfg *config.SystemConfig, get func(string) string) error {
	// 任一源是 apollo/consul/env-vars/kuboard 才真有"非 MCP 读 creds.json"的需求。
	needs := false
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if needsCreds(cc.Type) {
			needs = true
			break
		}
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
