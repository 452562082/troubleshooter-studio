// bindings_deploy.go —— 跑 install.sh / 写凭证到 .env 的 binding:
// ScanInstallPrompts / ReadEnv / RunInstall / RevealInFinder。
//
// 这些是"部署 openclaw 产物时的凭证表单闭环"：解析 install.sh 的 read_var 提示 →
// 预填已存的 .env → 前端让用户补/改 → 回写 .env → shell-out 跑 install.sh →
// 流式推日志到前端。独立成档是因为涉及 shell exec，权限与错误处理需单独审。
package main

import (
	"os/exec"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
)

// ScanInstallPrompts 解析 outputDir/scripts/install.sh，把所有 read_var 调用列出来，
// UI 拿去渲染凭证表单。
func (a *App) ScanInstallPrompts(outputDir string) ([]deploy.Prompt, error) {
	return deploy.ParseInstallPrompts(outputDir)
}

// ReadEnv 把 outputDir/scripts/.env 读回（如果存在）。UI 做"已有凭证预填"用。
func (a *App) ReadEnv(outputDir string) (map[string]string, error) {
	m, err := deploy.ReadEnvFile(outputDir)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]string{}, nil
	}
	return m, nil
}

// RunInstallResult 等同 deploy 层的 install 结果，额外含 ExitCode 便于 UI 区分失败类型。
type RunInstallResult struct {
	Log      string `json:"log"`
	ExitCode int    `json:"exit_code"`
	OK       bool   `json:"ok"`
}

// RunInstall 先把 creds 写进 outputDir/scripts/.env，再 shell-out bash install.sh，
// 返回合并日志。前端拿去展示在模态框里。
func (a *App) RunInstall(outputDir string, creds map[string]string) (*RunInstallResult, error) {
	if err := deploy.WriteEnvFile(outputDir, creds); err != nil {
		return nil, err
	}
	// 流式把每行输出推到前端 "install:log" 事件。前端 BotsPage 挂着监听,
	// 追加到 installLog 让用户实时看到进度,不用盯静默黑屏。
	log, err := deploy.RunInstallStreaming(outputDir, func(line string) {
		wailsruntime.EventsEmit(a.ctx, "install:log", line)
	})
	res := &RunInstallResult{Log: log, OK: err == nil}
	if err != nil {
		// exit code 提取（bash 脚本的非零退出）
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		// 不包装成 error 返回：前端要看到日志，即使 install 失败
		return res, nil
	}
	return res, nil
}

// RevealInFinder 在 Finder / Explorer 里打开 path（不是打开文件，是打开所在目录并高亮）。
// macOS 用 `open -R <path>`；Windows 未来需要分支。
func (a *App) RevealInFinder(path string) error {
	return exec.Command("open", "-R", path).Run()
}
