// Command tshoot-desktop 是 tshoot 的真桌面 app 入口（Wails v2）。
//
// 设计策略：Wails 负责原生窗口 + WebView + 打包；所有 HTTP 请求（前端 fetch /api/*
// 和 SPA 静态资源）全部 forward 到现有 api.NewRouter —— 所以 Vue 前端代码完全不用改，
// 跟 `tshoot serve` 的 Web UI 行为一致，只是换了个宿主。
//
// 跟 `tshoot serve`（路线 B）的区别：
//   - 路线 B：用户系统浏览器打开 http://localhost:<port>/；占端口、需要浏览器
//   - 路线 A（本文件）：原生 WKWebView / WebView2 窗口；不占公网端口、没浏览器地址栏
//
// 后端逻辑（discover / agent apply / gen / serve）100% 复用 internal/ 各包。
//
// 文件组织（跟 cmd/tshoot/ 的拆分风格一致）：
//   - main.go             入口 + App struct + wails.Run + 模板解析
//   - bindings_core.go    Version / Validate / Gen / Plan / Diff / Analyze / Doctor / DiscoverBots
//   - bindings_apply.go   ApplyBot / ImportAndDeploy
//   - bindings_deploy.go  ScanInstallPrompts / ReadEnv / RunInstall / RevealInFinder
//   - dialogs.go          OpenYAML / OpenDir / SaveYAML + 原生对话框 helpers
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	tshoot "github.com/xiaolong/troubleshooter-studio"
	"github.com/xiaolong/troubleshooter-studio/api"
	"github.com/xiaolong/troubleshooter-studio/internal/webui"
)

// 版本信息跟 cmd/tshoot 同样由 -ldflags 注入（make desktop 会传）
var (
	version = "dev"
	commit  = ""
)

// App 是暴露给前端的对象。每个导出方法自动成为一个 Wails binding，
// 前端可通过 window.go.main.App.* 调用。绑定方法散落在 bindings_*.go 和 dialogs.go。
type App struct {
	// templateRoot 是 gen 流水线用的模板源（templates/ 所在路径）；
	// 每个涉及 gen 的 binding（Gen / Plan / Diff / Apply*）都要它。
	templateRoot string

	// ctx 是 Wails 运行时 ctx，在 startup 阶段注入。所有需要原生能力（SaveFileDialog /
	// OpenDirectoryDialog / WindowShow / EventsEmit 等）的 binding 都用这个。
	ctx context.Context

	// installMu 保护 installCancel 字段；install 和 cancel 是不同 Wails goroutine
	// 过来的,没锁会 race。
	installMu sync.Mutex
	// installCancel 是当前正在跑的 install.sh 的 cancel 函数,nil=没有 install 在跑。
	// RunInstall 赋值并 defer 清空;CancelInstall 读取并调用。同一时刻只允许一个
	// install 跑,前端 UI 会禁用"部署"按钮避免并发。
	installCancel context.CancelFunc
}

// startup 由 Wails 在窗口创建完成时调用，注入 runtime ctx。私有也能被 Wails 识别。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func main() {
	tr := resolveTemplateDir()
	srv := &api.Server{TemplateRoot: tr}
	router := api.NewRouter(srv, webui.Distribution())

	appState := &App{
		templateRoot: tr,
	}

	err := wails.Run(&options.App{
		Title:  "Troubleshooter Studio",
		Width:  1280,
		Height: 860,

		// AssetServer.Handler = 所有 HTTP 请求（静态 SPA 资源 + /api/*）走这里。
		// 不设 Assets，让 router 一肩挑（NewRouter 里已经做了 SPA fallback + CORS）。
		AssetServer: &assetserver.Options{
			Handler: router,
		},

		// 背景色:跟左侧 sidebar 同 deep slate(#1e293b)。配合 TitleBarHiddenInsetUnified
		// 让顶部 macOS title bar 跟 sidebar 融为一体,traffic lights 浮在 sidebar 上,
		// 不再有那条灰色独立标题栏的视觉割裂。
		BackgroundColour: &options.RGBA{R: 30, G: 41, B: 59, A: 255},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "Troubleshooter Studio",
				Message: fmt.Sprintf("AI 排障机器人工作台 (桌面端入口)\n版本: %s", appState.Version()),
			},
		},

		Bind:      []any{appState},
		OnStartup: appState.startup,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "wails run:", err)
		os.Exit(1)
	}
}

// resolveTemplateDir 按优先级找 templates/：
//  1. 可执行文件旁（dev 模式：`wails dev` / 手动 `go run`）
//  2. macOS .app 里的 Contents/Resources/templates/（`wails build` 的产物布局）
//  3. CWD/templates/（从仓库根跑）
//  4. embed fallback：解压 tshoot.TemplatesFS 到 ~/.tshoot/templates/
//
// 桌面 app 场景下通常走 (4)，因为 app 被拷到 /Applications/ 后 CWD = /。
func resolveTemplateDir() string {
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		for _, rel := range []string{"templates", "../Resources/templates"} {
			p := filepath.Clean(filepath.Join(base, rel))
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, "templates")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// embed extract —— 桌面 app 里最常走的路径
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dst := filepath.Join(home, ".tshoot", "templates")
	_ = os.RemoveAll(dst) // 每次启动重建，确保跟当前 app 内嵌的一致
	if err := extractEmbedded(tshoot.TemplatesFS, "templates", dst); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 解压 embed templates 失败: %v\n", err)
		return ""
	}
	return dst
}

// extractEmbedded 把 embed.FS 里 rootSub 下的内容平铺到 dst（跳过 .DS_Store，
// .sh/.py 自动给 0755 执行位）。跟 cmd/tshoot/main.go 的同名逻辑一致。
func extractEmbedded(src fs.FS, rootSub, dst string) error {
	return fs.WalkDir(src, rootSub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootSub, p)
		if rel == "." {
			return nil
		}
		if filepath.Base(rel) == ".DS_Store" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if ext := filepath.Ext(rel); ext == ".sh" || ext == ".py" {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}
