// Package userconfig 管理桌面 app 跨会话的用户偏好,存 ~/.tshoot/config.json。
// 只放"全局设置"类数据,跟 bot / yaml 无关 —— 比如默认 clone 目录、UI 偏好等。
//
// 为什么不走 keychain:这些不是 secrets,纯配置项,明文 JSON 更方便用户自己
// 查看 / git ignore / 备份。API key 才走 keychain(见 cmd/tshoot-desktop/
// bindings_keystore.go)。
package userconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome 把 ~ 或 ~/subdir 前缀展开成绝对路径。Go 的 os / filepath / exec
// 都不会自动展开 ~,前端 UI 里让用户输入 ~/foo 然后直接传给后端会 os.Stat 失败。
// 拿不到 $HOME 时原样返回(不崩),其它路径原样返回(已经是绝对或相对)。
func ExpandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	// ~user/xxx 这种 POSIX 展开不处理(罕见,Go 标准库也没),原样返回。
	return p
}

// Config 是用户级偏好设置的完整 schema。
// 新字段加这里,默认值在 Load 的 zero-value 就是 ""——前端要自己兜 fallback。
type Config struct {
	// DefaultReposRoot 是 InitPage Step 4 扫描仓库时,"远程 clone" 模式下的
	// 默认父目录(clone 到 <here>/<repo.name>/)。空表示用内置 fallback:
	// ~/.tshoot/repos/。
	DefaultReposRoot string `json:"default_repos_root,omitempty"`

	// RepoPathsBySystem 是"仓库本地路径"映射:<system.id> → <repo.name> → 本机绝对路径。
	//
	// 设计:troubleshooter.yaml 必须保持可分享(团队私库 / 私密频道),不含任何本机路径;
	// 部署时 generator 需要把仓库本地路径烤进 repo-path-map.yaml,这份映射就走这里。
	// 流程:
	//   - wizard 一键部署:每次 ImportAndDeploy 把 repoPaths upsert 进来。
	//   - BotsPage 重新部署同一 system.id 的机器人:ApplyBot 自动按 system.id 读这份。
	//   - 团队成员拿到同一 yaml 但本机路径不一样:他们自己跑 wizard 后这份就有了。
	RepoPathsBySystem map[string]map[string]string `json:"repo_paths_by_system,omitempty"`

	// 历史:曾有 CustomInstallRoots 字段(IDE 自定义安装根目录),后来发现 IDE 扩展
	// 目录都是 hardcoded ~/.<target>(Claude Code / Cursor / Codex 都不读别处),功能
	// 没意义已砍。老 config.json 里如果有 custom_install_roots 字段,Unmarshal 时会
	// 被忽略(目标 struct 没这个字段),写回时就自动消失,无需 migrate。
}

// configPath 返回 ~/.tshoot/config.json 的绝对路径。
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tshoot", "config.json"), nil
}

// Load 读 config.json,文件不存在返回零值 Config (不是 error)。
// 解析失败才返回 error(让调用方决定是否展示给用户)。
func Load() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save 序列化写回 ~/.tshoot/config.json(目录不存在自动建)。
// 整份 Config 覆盖写,调用方读 → 改 → 写,不用处理 merge。
func Save(cfg *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// GetRepoPathsForSystem 返回某个 system.id 下"仓库名 → 本机绝对路径"映射。
// 文件不存在 / 没存过 / system.id 不在映射里都返回 nil(调用方按"空 map"处理)。
func GetRepoPathsForSystem(systemID string) map[string]string {
	if systemID == "" {
		return nil
	}
	cfg, err := Load()
	if err != nil || cfg == nil || cfg.RepoPathsBySystem == nil {
		return nil
	}
	return cfg.RepoPathsBySystem[systemID]
}

// SetRepoPathsForSystem upsert 某个 system.id 下的仓库路径映射。
// 传空 map 视为"清掉这个 system.id 的所有路径";其他 system.id 不动。
// 读 → 改 → 写,内部 merge 已处理。
func SetRepoPathsForSystem(systemID string, paths map[string]string) error {
	if systemID == "" {
		return nil
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.RepoPathsBySystem == nil {
		cfg.RepoPathsBySystem = map[string]map[string]string{}
	}
	// 过滤空值,避免持久化 "" 路径
	filtered := map[string]string{}
	for k, v := range paths {
		if v = strings.TrimSpace(v); v != "" {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		delete(cfg.RepoPathsBySystem, systemID)
	} else {
		cfg.RepoPathsBySystem[systemID] = filtered
	}
	return Save(cfg)
}

// DefaultReposRootOrFallback 给 UI 一个保证非空的路径:优先 cfg 里存的,
// 没存过就用 ~/.tshoot/repos/。UI 用这个避免写 "" 到输入框 placeholder 里。
func DefaultReposRootOrFallback() string {
	cfg, err := Load()
	if err == nil && cfg.DefaultReposRoot != "" {
		return cfg.DefaultReposRoot
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tshoot", "repos")
}
