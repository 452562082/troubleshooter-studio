// bindings_config.go —— 用户级全局配置的读写 binding(~/.tshoot/config.json)。
// 跟 bindings_keystore.go(API key → 系统钥匙串)分开:keystore 管 secrets,
// 这里管普通偏好(默认 clone 目录、未来的 UI 主题等)。
package main

import (
	"os"

	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// UserConfigResult 给前端的统一形状。不用直接 return *userconfig.Config 是因为
// Wails 生成的 TS 类型会跟 Go 的 json tag 有一些习惯性差异,包一层显式简单点,
// 也让未来加字段时不破坏已存在的前端代码(加 optional 字段即可)。
type UserConfigResult struct {
	DefaultReposRoot  string `json:"default_repos_root"`  // 用户实际存的值(可能为空)
	ResolvedReposRoot string `json:"resolved_repos_root"` // 前端展示用:空时回落到内置 fallback(~/.tshoot/repos)
	HomeDir           string `json:"home_dir"`            // 当前用户 $HOME;前端据此把绝对路径折回成 ~/... 展示,避免把 /Users/xxx 硬编码到 UI copy
}

// GetUserConfig 让前端读配置 + 计算好的 fallback。UI 一般两个字段都会用:
//   - DefaultReposRoot 空时,placeholder 展示 "(未设置,将使用默认)"
//   - ResolvedReposRoot 作为 "就算用户没设也知道 clone 会去哪" 的实际路径
//   - HomeDir 用来把 ResolvedReposRoot 的 /Users/xxx 前缀折成 ~ 展示,不替代实际操作路径
func (a *App) GetUserConfig() (*UserConfigResult, error) {
	cfg, err := userconfig.Load()
	if err != nil {
		return nil, err
	}
	// os.UserHomeDir 失败时返回空串,前端逻辑会回落"不折叠"展示,不致命。
	home, _ := os.UserHomeDir()
	return &UserConfigResult{
		DefaultReposRoot:  cfg.DefaultReposRoot,
		ResolvedReposRoot: userconfig.DefaultReposRootOrFallback(),
		HomeDir:           home,
	}, nil
}

// SetDefaultReposRoot 保存用户选择的默认 clone 目录。
// 空串 = 清掉用户设置,回落到 fallback。
// 用户输入 ~/foo 会展开成绝对路径再存,避免下次 Load 出来的还是 ~/... 被 os.Stat 打回。
func (a *App) SetDefaultReposRoot(path string) error {
	cfg, err := userconfig.Load()
	if err != nil {
		return err
	}
	cfg.DefaultReposRoot = userconfig.ExpandHome(path)
	return userconfig.Save(cfg)
}
