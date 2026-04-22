// bindings_apply.go —— 改装已装机器人 workspace 的 binding：ApplyBot / ImportAndDeploy。
//
// 跟 bindings_core.go 的区别是这些方法会真写盘到机器人活 workspace（rsync 产物 /
// 更新 tshoot.json），所以单独归一档方便 reviewer 检查对权限与副作用的处理。
package main

import (
	"fmt"

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
// target: openclaw / claude-code / cursor / standalone
// destPath: 部署目标路径。openclaw 下是产物目录（含 install.sh）；其它 target 下是目标项目根。
func (a *App) ImportAndDeploy(yamlText, target, destPath string) (*agent.Result, error) {
	return agent.ImportAndApply([]byte(yamlText), target, destPath, agent.ApplyOptions{
		TemplateRoot:  a.templateRoot,
		TshootVersion: version,
	})
}
