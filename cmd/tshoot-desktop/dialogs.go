// dialogs.go —— 原生文件/目录对话框 binding 与 helper：OpenYAML / OpenDir / SaveYAML。
//
// macOS 26 上 Wails v2.12 的 NSOpenPanel 出现即关（Wails runtime 里对 panel 的
// 封装与新系统的生命周期假设冲突），所以 darwin 下全部绕过 Wails 直接用
// `osascript -e 'choose file ...'` 的 Apple Events 写法。其他平台（未来跨平台
// 真出现时）保留 Wails 的 OpenFileDialog / SaveFileDialog fallback。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// OpenYAMLResult 用户取消返回两个空串 + nil error，前端据此驱动'导入 yaml 部署机器人'流程。
type OpenYAMLResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// OpenYAML 弹原生打开对话框让用户选一个 yaml 文件，返回 {path, content}。
func (a *App) OpenYAML() (*OpenYAMLResult, error) {
	path, err := pickFileNative("选择 system.yaml")
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &OpenYAMLResult{}, nil // 用户取消
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &OpenYAMLResult{Path: path, Content: string(data)}, nil
}

// OpenDir 弹原生目录选择对话框（用于选部署目标路径 destPath）。用户取消返回 ""。
func (a *App) OpenDir(title string) (string, error) {
	return pickDirNative(title, a.ctx)
}

// SaveYAML 弹原生保存对话框让用户选路径，把 yamlText 写到那里。
// defaultFilename 是对话框里预填的文件名（"shop.yaml" 之类）。
// 返回值：
//   - ok 时返回真实保存路径（含用户改过名字的情况）
//   - 用户取消时返回空字符串 + nil error
func (a *App) SaveYAML(defaultFilename, yamlText string) (string, error) {
	path, err := saveFileNative("导出 system.yaml", defaultFilename, a.ctx)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil // user canceled
	}
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// pickFileNative 选文件对话框。macOS 用 osascript（Wails v2.12 在 macOS 26 上
// NSOpenPanel 出现即关，绕过 Wails 的封装直接用 Apple Events 的 choose file）；
// 其他平台走 Wails 的 OpenFileDialog。取消返回 ("", nil)。
func pickFileNative(title string) (string, error) {
	if runtime.GOOS == "darwin" {
		return osaChoose(fmt.Sprintf(`POSIX path of (choose file with prompt "%s")`, escapeApple(title)))
	}
	// 其他平台先保留 Wails 的实现（需要 ctx，这里返回 error 让调用方处理；
	// 实际上目前只有 darwin 构建，留给未来跨平台时再补 Wails 路径）。
	return "", fmt.Errorf("file picker not wired for %s yet", runtime.GOOS)
}

// pickDirNative 同理的目录选择。ctx 非 darwin 下走 Wails 需要。
func pickDirNative(title string, ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		return osaChoose(fmt.Sprintf(`POSIX path of (choose folder with prompt "%s")`, escapeApple(title)))
	}
	return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{Title: title})
}

// saveFileNative 保存对话框。macOS 用 osascript（choose file name），
// 其他平台 fallback 到 Wails 的 SaveFileDialog。
func saveFileNative(title, defaultName string, ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`POSIX path of (choose file name with prompt "%s" default name "%s")`,
			escapeApple(title), escapeApple(defaultName))
		return osaChoose(script)
	}
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: defaultName,
	})
}

// osaChoose 跑 osascript -e <AppleScript>，返回 stdout（去首尾空白）。
// 用户点 Cancel 时 osascript 会以 exit 1 结束并在 stderr 打印 "User canceled."，
// 我们把它当作用户取消处理，返回 ("", nil)。
func osaChoose(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		// 用户取消或 AppleScript 错误。区分用户取消（不算 error，返回空串）与其它错误。
		if ee, ok := err.(*exec.ExitError); ok {
			msg := string(ee.Stderr)
			if strings.Contains(msg, "User canceled") || strings.Contains(msg, "-128") {
				return "", nil
			}
			return "", fmt.Errorf("osascript: %s", strings.TrimSpace(msg))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// escapeApple 转义 AppleScript 字符串里的双引号和反斜杠，避免 prompt 里带特殊字符破坏脚本。
func escapeApple(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
