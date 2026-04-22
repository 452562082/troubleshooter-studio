// bindings_deploy.go —— 跑 install.sh / 写凭证到 .env 的 binding:
// ScanInstallPrompts / ReadEnv / RunInstall / CancelInstall / RevealInFinder。
//
// 这些是"部署 openclaw 产物时的凭证表单闭环"：解析 install.sh 的 read_var 提示 →
// 预填已存的 .env → 前端让用户补/改 → 回写 .env → shell-out 跑 install.sh →
// 流式推日志到前端。独立成档是因为涉及 shell exec，权限与错误处理需单独审。
package main

import (
	"context"
	"errors"
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
//
// 取消支持:前端点"取消安装"会调 CancelInstall(),cancel 当前 ctx,内部
// exec.CommandContext 发 SIGKILL 给 bash 进程组(见 deploy/process_unix.go)。
// Cancel 后此函数仍返回 Result(不是 error);res.ExitCode == -2 表示用户主动取消。
func (a *App) RunInstall(outputDir string, creds map[string]string) (*RunInstallResult, error) {
	if err := deploy.WriteEnvFile(outputDir, creds); err != nil {
		return nil, err
	}

	// 每次 install 建独立 cancellable ctx(父 = Wails runtime ctx,app 退出顺带 cancel),
	// 存进 App 让 CancelInstall 能触发。
	runCtx, cancel := context.WithCancel(a.ctx)
	a.installMu.Lock()
	if a.installCancel != nil {
		// 理论上前端会禁用"部署"按钮防并发,走到这里说明 UI 有 bug;先干掉上一个
		// 避免日志流相互串扰。
		a.installCancel()
	}
	a.installCancel = cancel
	a.installMu.Unlock()

	defer func() {
		a.installMu.Lock()
		a.installCancel = nil
		a.installMu.Unlock()
		cancel() // 保证 ctx 被 cancel,避免 goroutine 泄漏
	}()

	// 流式把每行输出推到前端 "install:log" 事件。前端 BotsPage 挂监听,
	// 追加到 installLog 让用户实时看到进度。
	log, err := deploy.RunInstallStreaming(runCtx, outputDir, func(line string) {
		wailsruntime.EventsEmit(a.ctx, "install:log", line)
	})
	res := &RunInstallResult{Log: log, OK: err == nil}
	if err != nil {
		// 用户 cancel:ExitCode = -2,UI 展示"已取消"而不是"失败"。
		if errors.Is(err, context.Canceled) {
			res.ExitCode = -2
			res.OK = false
			return res, nil
		}
		// exit code 提取（bash 脚本的非零退出）
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		// 不包装成 error:前端要看到日志,即使 install 失败
		return res, nil
	}
	return res, nil
}

// CancelInstall 前端"取消安装"按钮调:触发当前 install ctx cancel,
// 底层 exec.CommandContext 会 SIGKILL bash + 整个进程组。没有 install 在跑时
// 返回 false(UI 忽略),有的话返回 true。
func (a *App) CancelInstall() bool {
	a.installMu.Lock()
	defer a.installMu.Unlock()
	if a.installCancel == nil {
		return false
	}
	a.installCancel()
	a.installCancel = nil
	return true
}

// RevealInFinder 在 Finder / Explorer 里打开 path（不是打开文件，是打开所在目录并高亮）。
// macOS 用 `open -R <path>`；Windows 未来需要分支。
func (a *App) RevealInFinder(path string) error {
	return exec.Command("open", "-R", path).Run()
}
