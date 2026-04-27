// Package openclaw 读本机 OpenClaw 安装的 openclaw.json,抽出"可用模型"给向导用。
//
// 真实 schema(~/.openclaw/openclaw.json,JSON 不是 YAML):
//
//	{
//	  "meta": {"lastTouchedVersion": "2026.4.9", ...},
//	  "agents": {
//	    "defaults": {
//	      "model":  {"primary": "openai-codex/gpt-5.4"},  // 默认模型
//	      "models": {"openai-codex/gpt-5.4": {}}          // 已配置的模型 map (权威)
//	    },
//	    "list": [{"id": "...", "model": "openai-codex/gpt-5.3-codex"}]  // 历史在用
//	  },
//	  "auth":     {"profiles": {"<provider>:<name>": {...}}},  // 已配置的认证 (仅信息性)
//	  "gateway":  {...},
//	  "mcp":      {...}
//	}
//
// 模型 id 格式:"<provider>/<model>"(如 openai-codex/gpt-5.4),
// provider 是 openclaw 内部的 provider 命名(不等于 Studio 的 providers.go 注册表),
// Studio 只需记录完整 id 回写 yaml,让 openclaw gateway 自己路由。
//
// 探测流程:
//  1. installDir(默认 ~/.openclaw)必须是目录
//  2. 必须有 openclaw.json 且能 JSON 解析,否则认定"不是 openclaw 安装"
//  3. 模型从三处去重聚合:defaults.model.primary / defaults.models / list[].model
//  4. 全部三处都空 → Models=nil + InstalledButEmpty=true,UI 让用户去配 openclaw
package openclaw

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrNotInstalled = errors.New("not an openclaw installation (openclaw.json missing)")

// ModelEntry 给前端的单条模型记录。
type ModelEntry struct {
	ID       string `json:"id"`                 // 完整 model id,例:"openai-codex/gpt-5.4"
	Provider string `json:"provider,omitempty"` // 从 id 前缀抠出,display 用
	Label    string `json:"label,omitempty"`    // 等于 ID 或加 "(默认)" 等修饰
	Source   string `json:"source,omitempty"`   // 来自 openclaw.json 哪个字段
	Primary  bool   `json:"primary,omitempty"`  // 是 defaults.model.primary 标记的首选
}

// DetectResult openclaw 探测结果。
type DetectResult struct {
	InstallDir        string       `json:"install_dir"`
	ConfigPath        string       `json:"config_path"`
	Version           string       `json:"version,omitempty"` // meta.lastTouchedVersion
	Models            []ModelEntry `json:"models"`
	InstalledButEmpty bool         `json:"installed_but_empty"` // openclaw.json 存在但无任何 model 信息
	// AuthProviders 是 openclaw.json 的 auth.profiles 里出现过的 provider 名字(去重)。
	// 仅供 UI 作"模型 provider 有没有对应 auth 配置"的 sanity check 提示,不强制约束。
	AuthProviders []string `json:"auth_providers,omitempty"`
}

// Detect 读 installDir 下的 openclaw.json 并抽可用模型。
// installDir 为空 → 默认 ~/.openclaw。
func Detect(installDir string) (*DetectResult, error) {
	dir := strings.TrimSpace(installDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("read home: %w", err)
		}
		dir = filepath.Join(home, ".openclaw")
	}
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, ErrNotInstalled
	}
	cfgPath := filepath.Join(dir, "openclaw.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, ErrNotInstalled
	}

	// 只按需抠几块;openclaw.json 里有很多其它字段(gateway/auth/mcp/...),不用全解
	var cfg struct {
		Meta struct {
			LastTouchedVersion string `json:"lastTouchedVersion"`
		} `json:"meta"`
		Agents struct {
			Defaults struct {
				Model struct {
					Primary string `json:"primary"`
				} `json:"model"`
				Models map[string]json.RawMessage `json:"models"`
			} `json:"defaults"`
			List []struct {
				Model string `json:"model"`
			} `json:"list"`
		} `json:"agents"`
		Auth struct {
			Profiles map[string]struct {
				Provider string `json:"provider"`
			} `json:"profiles"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("openclaw.json 解析失败: %w", err)
	}

	res := &DetectResult{
		InstallDir: dir,
		ConfigPath: cfgPath,
		Version:    cfg.Meta.LastTouchedVersion,
	}

	seen := map[string]bool{}
	addModel := func(id, source string, primary bool) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		provider := ""
		if idx := strings.Index(id, "/"); idx > 0 {
			provider = id[:idx]
		}
		label := id
		if primary {
			label = id + " (默认)"
		}
		res.Models = append(res.Models, ModelEntry{
			ID:       id,
			Provider: provider,
			Label:    label,
			Source:   source,
			Primary:  primary,
		})
	}

	// 顺序决定下拉里的显示顺序 + label("(默认)" 加在 primary 上):
	//   1. defaults.model.primary(权威"开箱即用")
	//   2. defaults.models map 的 key(用户主动配的其它可用模型)
	//   3. agents.list[].model(已有 agent 的历史记录 —— 至少这个 id 在本机跑过)
	addModel(cfg.Agents.Defaults.Model.Primary, "agents.defaults.model.primary", true)
	for k := range cfg.Agents.Defaults.Models {
		addModel(k, "agents.defaults.models", false)
	}
	for _, a := range cfg.Agents.List {
		addModel(a.Model, "agents.list[]", false)
	}

	// 抽 auth.profiles 涉及的 provider 名字,去重
	authSeen := map[string]bool{}
	for _, p := range cfg.Auth.Profiles {
		if p.Provider == "" || authSeen[p.Provider] {
			continue
		}
		authSeen[p.Provider] = true
		res.AuthProviders = append(res.AuthProviders, p.Provider)
	}

	res.InstalledButEmpty = len(res.Models) == 0
	return res, nil
}
