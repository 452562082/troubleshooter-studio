// bindings_openclaw.go —— 读本机 OpenClaw 安装目录里的模型配置,供向导 Step 2
// "部署到 OpenClaw" 卡片使用。用户勾 OpenClaw 后,前端调这个 binding 拉可用模型列表;
// 默认探测 ~/.openclaw,探测失败时前端弹"选目录"让用户指路。
//
// 跟向导里"embedded / 其它 target"的硬编码 modelGroups 并存:
//   - openclaw target:列表来自 openclaw 自己的配置(动态)
//   - embedded / claude-code / cursor:用 modelGroups 硬编码预设(静态)
package main

import (
	"errors"

	"github.com/xiaolong/troubleshooter-studio/internal/openclaw"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// OpenClawDetectResult 是给前端的形状;ok=false 时 Err 装人类可读的原因
// (installed=false = ErrNotInstalled,其它错误装完整 message)。
// 独立包一层不直接返 *openclaw.DetectResult 是为了给 "not-installed" / "installed-but-empty"
// 三种状态一组稳定的字段信号,前端按它切 UI 分支。
type OpenClawDetectResult struct {
	OK                bool                  `json:"ok"`
	Installed         bool                  `json:"installed"`
	InstalledButEmpty bool                  `json:"installed_but_empty"` // openclaw.json 在但无任何 model 信息
	InstallDir        string                `json:"install_dir,omitempty"`
	ConfigPath        string                `json:"config_path,omitempty"`
	Version           string                `json:"version,omitempty"`
	Models            []openclaw.ModelEntry `json:"models,omitempty"`
	AuthProviders     []string              `json:"auth_providers,omitempty"`
	Err               string                `json:"err,omitempty"`
}

// DetectOpenClawModels 探测本机 OpenClaw 安装并列可用模型。
// installDir 为空 = 用 ~/.openclaw 默认路径。
// 返回的 Result 永远非 nil;OK=false 时看 Err 和 Installed 分辨失败原因。
func (a *App) DetectOpenClawModels(installDir string) *OpenClawDetectResult {
	expanded := userconfig.ExpandHome(installDir)
	r, err := openclaw.Detect(expanded)
	if err != nil {
		if errors.Is(err, openclaw.ErrNotInstalled) {
			return &OpenClawDetectResult{OK: false, Installed: false, Err: "未检测到 OpenClaw 安装(openclaw.json 缺失)"}
		}
		return &OpenClawDetectResult{OK: false, Installed: false, Err: err.Error()}
	}
	return &OpenClawDetectResult{
		OK:                true,
		Installed:         true,
		InstalledButEmpty: r.InstalledButEmpty,
		InstallDir:        r.InstallDir,
		ConfigPath:        r.ConfigPath,
		Version:           r.Version,
		Models:            r.Models,
		AuthProviders:     r.AuthProviders,
	}
}
