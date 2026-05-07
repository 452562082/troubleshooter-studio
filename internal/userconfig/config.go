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

	// DeployedBots 是"曾经成功部署过的机器人"清单,key="<system_id>|<target>"。
	//
	// 用途(ghost bot):用户外部 `rm -rf ~/.<target>/skills/<name>/` 清掉机器人后,
	// discover.Scan 找不到 tshoot.json 锚点 → BotsPage 卡片直接消失,用户视角是
	// "刚才还在的机器人没了"。BotsPage 把这里有但实际 disk 没有的标 ghost 显示出来,
	// 并提供"重新部署"入口,让用户能恢复或主动清理。
	//
	// 维护:
	//   - ImportAndDeploy 成功后 upsert(覆盖 LastDeployedAt)
	//   - 用户在 BotsPage 主动卸载 → RemoveDeployedBot 清掉
	//   - 用户在 BotsPage 对 ghost 点"忘掉它" → 同上
	//
	// 历史:曾有 CustomInstallRoots 字段(IDE 自定义安装根目录),后来发现 IDE 扩展
	// 目录都是 hardcoded ~/.<target>(Claude Code / Cursor / Codex 都不读别处),功能
	// 没意义已砍。老 config.json 里如果有 custom_install_roots 字段,Unmarshal 时会
	// 被忽略,写回时就自动消失,无需 migrate。
	DeployedBots map[string]DeployedBotEntry `json:"deployed_bots,omitempty"`
}

// DeployedBotEntry 单条"曾部署"记录。字段最小化:够 BotsPage ghost 卡片渲染 +
// 重新部署入口能拿到 system_id 即可,详细 yaml 仍以 disk 上 tshoot.json 为准。
type DeployedBotEntry struct {
	SystemID       string `json:"system_id"`
	SystemName     string `json:"system_name"`
	Target         string `json:"target"`
	Path           string `json:"path"`             // 当时部署落地路径(disk 缺失时给"应该在哪"的线索)
	LastDeployedAt int64  `json:"last_deployed_at"` // unix seconds
}

// DeployedBotKey 拼出 DeployedBots map 的 key。集中一处,upsert / lookup / remove 一致。
func DeployedBotKey(systemID, target string) string {
	return systemID + "|" + target
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
//
// 写入策略:
//  1. tmp + rename 原子写。直接 os.WriteFile 写到一半 crash → 文件被截断,
//     用户下次启动 Load 解析失败,整份 RepoPathsBySystem / DeployedBots 全丢。
//     tmp + rename 让失败时原文件保持完整(rename 是 inode 级原子操作)。
//  2. 0o600 而不是 0o644。content 含 RepoPathsBySystem(本机仓库绝对路径)+
//     DeployedBots(部署元数据 + system_id),world-readable 等于让其他用户能
//     推断 home 结构 + 哪些项目在用工作台,跟 IDE settings 同种隐私性质。
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
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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

// UpsertDeployedBot 把一条"部署成功"记录写进 ~/.tshoot/config.json。
// LastDeployedAt 调用方填(time.Now().Unix());Path 是落地路径(同卡片显示路径)。
// 同 key 已存在 → 覆盖(LastDeployedAt 更新),不报错。
func UpsertDeployedBot(entry DeployedBotEntry) error {
	if entry.SystemID == "" || entry.Target == "" {
		return nil
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.DeployedBots == nil {
		cfg.DeployedBots = map[string]DeployedBotEntry{}
	}
	cfg.DeployedBots[DeployedBotKey(entry.SystemID, entry.Target)] = entry
	return Save(cfg)
}

// ListDeployedBots 返回 ~/.tshoot/config.json 里所有部署记录。
// 文件不存在 / 没存过返回空 map(非 nil,方便 range)。
func ListDeployedBots() map[string]DeployedBotEntry {
	cfg, err := Load()
	if err != nil || cfg == nil || cfg.DeployedBots == nil {
		return map[string]DeployedBotEntry{}
	}
	return cfg.DeployedBots
}

// RemoveDeployedBot 删一条记录。BotsPage 卸载机器人 / "忘掉 ghost" 都调这个。
// key 不存在 → no-op,不报错。
func RemoveDeployedBot(systemID, target string) error {
	if systemID == "" || target == "" {
		return nil
	}
	cfg, err := Load()
	if err != nil || cfg == nil || cfg.DeployedBots == nil {
		return nil
	}
	delete(cfg.DeployedBots, DeployedBotKey(systemID, target))
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
