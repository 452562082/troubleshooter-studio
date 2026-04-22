// bindings_apply.go —— 改装已装机器人 workspace 的 binding：ApplyBot / ImportAndDeploy /
// DefaultDestPath。
//
// 跟 bindings_core.go 的区别是这些方法会真写盘到机器人活 workspace（rsync 产物 /
// 更新 tshoot.json），所以单独归一档方便 reviewer 检查对权限与副作用的处理。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// ApplyBot 把新的 system.yaml 应用到已装机器人的活 workspace：
// 重新渲染产物 → rsync 到 agentPath → 更新 tshoot.json。
// preserve_on_regenerate 列表里的文件保留用户手改不覆盖。
//
// 前置：agentPath 下必须有可读的 tshoot.json（由 discover 识别到的路径）。
// dryRun=true 时只预演，不真写盘，用于 UI 先给用户看"会变什么"。
func (a *App) ApplyBot(agentPath, newYamlText string, dryRun bool) (*agent.Result, error) {
	// 从活 workspace 回读 DiscoveredAgent（避免 UI 传整个结构体过来，省序列化）
	found, err := discover.Scan([]string{agentPath})
	if err != nil {
		return nil, fmt.Errorf("read agent at %s: %w", agentPath, err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no tshoot.json found under %s", agentPath)
	}
	// 同一目录下可能有多个 target 的 tshoot.json；选路径最短（最贴近 agentPath 根）那个
	ag := found[0]
	for _, cand := range found[1:] {
		if len(cand.Path) < len(ag.Path) {
			ag = cand
		}
	}
	return agent.Apply(ag, agent.ApplyOptions{
		NewYAML:       []byte(newYamlText),
		TemplateRoot:  a.templateRoot,
		TshootVersion: version,
		DryRun:        dryRun,
	})
}

// ImportAndDeploy 把 yaml 直接部署成新机器人（agent.ImportAndApply 的 UI 封装）。
// target: openclaw / claude-code / cursor / embedded
// destPath: 部署目标路径。openclaw 下是产物目录（含 install.sh）；其它 target 下是目标项目根。
func (a *App) ImportAndDeploy(yamlText, target, destPath string) (*agent.Result, error) {
	return agent.ImportAndApply([]byte(yamlText), target, destPath, agent.ApplyOptions{
		TemplateRoot:  a.templateRoot,
		TshootVersion: version,
	})
}

// DefaultDestPath 给不同 target 推荐默认部署路径,UI 据此决定要不要让用户手填。
//
// 设计:
//   - embedded:产物只是"Studio 内嵌对话的素材"(system-prompt.md / skills),
//     用户从不直接 cd 进去。默认到 ~/.tshoot/embedded/<id>/,UI 隐藏路径输入。
//   - openclaw:产物是 install.sh 用的中间包,最终 rsync 到 workspace_name 目录,
//     中间位置对用户也没意义。默认到 ~/.tshoot/openclaw/<id>/,UI 隐藏输入。
//   - claude-code / cursor:装到"用户已有项目根"里(CLAUDE.md + skills/ 注进项目)。
//     Studio 不知道用户想装哪个项目,必须用户选。返回空串,UI 强制必填。
//
// 空 systemID 时回退到 "default"(UI 初始化时 system.id 可能还空,给个兜底)。
func (a *App) DefaultDestPath(target, systemID string) (string, error) {
	switch target {
	case "embedded", "openclaw":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("read home: %w", err)
		}
		id := systemID
		if id == "" {
			id = "default"
		}
		return filepath.Join(home, ".tshoot", target, id), nil
	case "claude-code", "cursor":
		// 空串 = 让用户必选,UI 会保持输入框可见
		return "", nil
	default:
		return "", fmt.Errorf("unknown target: %q", target)
	}
}
