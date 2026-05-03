// bindings_core.go —— 元数据 + 验证类 binding:Version / Validate / PrefillCreds / Doctor。
//
// 按域分文件:
//   bindings_core.go  Version / Validate / PrefillCreds / Doctor
//   bindings_repo.go  DiscoverBots / Analyze / 仓库辅助(branches / submodules / role)
//   bindings_gen.go   Gen / Plan / Diff / GenPreview
//   bindings_apply.go 改已装机器人的活 workspace
//   bindings_deploy.go 走 install.sh / 写 .env
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// Version 前端可调：window.go.main.App.Version()
func (a *App) Version() string {
	if commit != "" {
		return fmt.Sprintf("%s (%s)", version, commit)
	}
	return version
}


// ValidateResult 与 /api/validate 返回形状对齐，前端已有依赖。
// Issues 是健康检查的额外发现:Validate 通过(yaml 能跑)后再跑 HealthCheck,
// 把"语义层"的缺口(可观测性 wiring 不全 / 仓库分支缺 env / 多源没指定 source 等)
// 一并返给前端。Issues 仅供展示提醒,不阻断生成。
type ValidateResult struct {
	Valid  bool                  `json:"valid"`
	System string                `json:"system"`
	Name   string                `json:"name"`
	Envs   int                   `json:"envs"`
	Repos  int                   `json:"repos"`
	Issues []config.HealthIssue  `json:"issues,omitempty"`
}

// PrefillCreds 从 yaml 抽 install 阶段需要的环境变量默认值(KUBOARD_URL_DEV / GRAFANA_URL_DEV
// 等),让 UI 表单做"已经在 yaml 里写过的字段自动填上"。
// yaml 解析失败返回 error;成功返回 env var key → value(value 为空的 key 不返)。
// UI 用法:导入 yaml 时调一次,把返回值 merge 到 toolInputs / form state,用户再编辑。
// 注:install 时(RunInstall)也会内部再 merge 一次 prefill 兜底,即使 UI 没显式调本接口
// 也能保证 yaml 写过的字段不丢。
func (a *App) PrefillCreds(yamlText string) (map[string]string, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return agent.PrefillCredsFromYAML(cfg), nil
}

// Validate 校验 system.yaml 内容,解析失败返回 error;成功后再跑健康检查,
// 把语义层缺口(severity warn/info/error)一并放进 Issues。
func (a *App) Validate(yamlText string) (*ValidateResult, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return &ValidateResult{
		Valid:  true,
		System: cfg.System.ID,
		Name:   cfg.System.Name,
		Envs:   len(cfg.Environments),
		Repos:  len(cfg.Repos),
		Issues: config.HealthCheck(cfg),
	}, nil
}

// Doctor 对比声明 vs 代码实态,返回漂移报告。等价 POST /api/doctor?repos_root=...
//
// 路径来源优先级(从高到低):
//  1. reposRoot 非空(用户在 UI 选了同根目录):每个 repo.Name 拼成 <reposRoot>/<repo.Name>;
//     **同时**会跟 userconfig 里的 saved 路径合并 —— 用户选了根但有些子模块单独存,
//     已保存的路径会优先(reposRoot 路径只填 saved 没记的那部分)。
//  2. 本地保存的 ~/.tshoot/config.json[system.id]:部署时记下的"每个 repo → 本机绝对路径"
//     表(给 Gen / auto-analyzer 用)→ doctor 自动复用,用户不必再选父目录。
//  3. 都没有(新机器人 / 第一次诊断):跳过深度扫,只跑静态检查。
//
// 这样用户在 BotsPage 点"诊断"默认就能拿到深度扫结果(部署时已记录路径),
// 不必每次手选 reposRoot;只在子模块路径漂移 / 默认路径不对的时候才需要手动覆盖。
func (a *App) Doctor(yamlText, reposRoot string) (*doctor.Report, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	// 先取 saved per-repo paths(部署阶段已记录)
	merged := map[string]string{}
	for k, v := range userconfig.GetRepoPathsForSystem(cfg.System.ID) {
		if strings.TrimSpace(v) != "" {
			merged[k] = userconfig.ExpandHome(v)
		}
	}
	// reposRoot 仅填补 saved 没记的 repo;saved 优先
	if strings.TrimSpace(reposRoot) != "" {
		root := userconfig.ExpandHome(reposRoot)
		for _, repo := range cfg.Repos {
			if _, ok := merged[repo.Name]; ok {
				continue
			}
			merged[repo.Name] = filepath.Join(root, repo.Name)
		}
	}
	return doctor.CheckWithPaths(cfg, merged)
}
