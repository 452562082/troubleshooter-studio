// bindings_apply.go —— 改装已装机器人 workspace 的 binding:ApplyBot / ImportAndDeploy /
// DefaultDestPath。这些方法会真写盘到机器人活 workspace,跟纯查询型 binding 分开放。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// ApplyBot 把新的 system.yaml 应用到已装机器人的活 workspace:
// 重新渲染产物 → rsync 到 agentPath → 更新 tshoot.json。
// preserve_on_regenerate 列表里的文件保留用户手改不覆盖。
//
// 仓库本地路径:不在 yaml 里(yaml 必须可分享),从 ~/.tshoot/config.json 按
// system.id 自动查出来,生成 repo-path-map.yaml 用。
//
// dryRun=true 时只预演,不真写盘,用于 UI 先给用户看"会变什么"。
func (a *App) ApplyBot(agentPath, newYamlText string, dryRun bool) (*agent.Result, error) {
	found, err := discover.Scan([]string{agentPath})
	if err != nil {
		return nil, fmt.Errorf("read agent at %s: %w", agentPath, err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no tshoot.json found under %s", agentPath)
	}
	// 同一目录下可能有多个 target 的 tshoot.json;选路径最短(最贴近 agentPath 根)那个
	ag := slices.MinFunc(found, func(a, b discover.DiscoveredAgent) int {
		return len(a.Path) - len(b.Path)
	})
	// 按 system.id 查仓库本地路径表(失败 → 空 map,不阻塞)
	var repoPaths map[string]string
	if cfg, perr := config.LoadFromBytes([]byte(newYamlText)); perr == nil && cfg.System.ID != "" {
		repoPaths = userconfig.GetRepoPathsForSystem(cfg.System.ID)
	}
	return agent.Apply(ag, agent.ApplyOptions{
		NewYAML:        []byte(newYamlText),
		TemplateRoot:   a.templateRoot,
		TshootVersion:  version,
		DryRun:         dryRun,
		RepoLocalPaths: repoPaths,
	})
}

// ImportAndDeploy 把 yaml 直接部署成新机器人（agent.ImportAndApply 的 UI 封装）。
// target: openclaw / claude-code / cursor / embedded
// destPath: 部署目标路径。openclaw 下是产物目录（含 install.sh）；其它 target 下是目标项目根。
// repoPaths: 仓库名 → 本机绝对路径,烤进产物 skills/routing/references/repo-path-map.yaml。
// 前端从 wizard 里抽出每个 repo 的 _localPath / _cloneTarget 传过来;system.yaml
// 里不含路径(故意的,保持可分享),这里是唯一的路径传入口。
//
// 副作用:把 repoPaths 按 system.id 持久化到 ~/.tshoot/config.json,后续 BotsPage
// 重新部署同一 system 时,ApplyBot 自动读这份,不必再跑一次 wizard。
func (a *App) ImportAndDeploy(yamlText, target, destPath string, repoPaths map[string]string, ideCreds map[string]string, customInstallRoot string) (*agent.Result, error) {
	// 用户可能传 ~/foo,统一展开成绝对路径
	expanded := make(map[string]string, len(repoPaths))
	for k, v := range repoPaths {
		if v != "" {
			expanded[k] = userconfig.ExpandHome(v)
		}
	}
	// 持久化到 ~/.tshoot/config.json(按 system.id 索引);失败不阻塞部署,只记日志
	if cfg, perr := config.LoadFromBytes([]byte(yamlText)); perr == nil && cfg.System.ID != "" && len(expanded) > 0 {
		_ = userconfig.SetRepoPathsForSystem(cfg.System.ID, expanded)
	}
	// customInstallRoot 用户在 wizard"我已自行安装→选目录"里指定的非默认安装位置;
	// 空字符串 → 走默认 ~/.<target>。展开 ~ 一致化处理 + 持久化到 ~/.tshoot/config.json,
	// 让 BotsPage 的 DiscoverBots 也能扫到这里、下次进 wizard 也能反填默认值。
	cir := ""
	if customInstallRoot != "" {
		cir = userconfig.ExpandHome(customInstallRoot)
		_ = userconfig.SetCustomInstallRoot(target, cir)
	}
	return agent.ImportAndApply([]byte(yamlText), target, destPath, agent.ApplyOptions{
		TemplateRoot:      a.templateRoot,
		TshootVersion:     version,
		RepoLocalPaths:    expanded,
		IDECreds:          ideCreds, // claude-code/cursor 装完直接注入 mcpServers 用
		CustomInstallRoot: cir,
	})
}

// DefaultDestPath 给不同 target 推荐默认部署路径,UI 据此决定要不要让用户手填。
//
// 设计:三种 target 都是 Studio 托管的中间包,装到 ~/.tshoot/<target>/<id>/。
// install.sh 跑完后再各自分发到用户级的真实位置:
//   - openclaw     ~/.openclaw/workspace/<workspace_name>/
//   - claude-code  ~/.claude/agents/<name>.md  + ~/.claude/skills/<name>/
//   - cursor       ~/.cursor/agents/<name>.md  + ~/.cursor/skills/<name>/
//
// 空 systemID 时回退到 "default"(UI 初始化时 system.id 可能还空,给个兜底)。
func (a *App) DefaultDestPath(target, systemID string) (string, error) {
	switch target {
	case "openclaw", "claude-code", "cursor", "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("read home: %w", err)
		}
		id := systemID
		if id == "" {
			id = "default"
		}
		return filepath.Join(home, ".tshoot", target, id), nil
	default:
		return "", fmt.Errorf("unknown target: %q", target)
	}
}
