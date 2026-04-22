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
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	tsf "github.com/xiaolong/troubleshooter-studio"
	"github.com/xiaolong/troubleshooter-studio/api"
	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/webui"
)

// 版本信息跟 cmd/tshoot 同样由 -ldflags 注入（make desktop 会传）
var (
	version = "dev"
	commit  = ""
)

// App 是暴露给前端的对象。每个导出方法自动成为一个 Wails binding，
// 前端可通过 window.go.main.App.* 调用。
type App struct {
	// templateRoot 是 gen 流水线用的模板源（templates/ 所在路径）；
	// 每个涉及 gen 的 binding（Gen / 未来的 Plan）都要它。
	templateRoot string

	// ctx 是 Wails 运行时 ctx，在 startup 阶段注入。所有需要原生能力（SaveFileDialog /
	// OpenDirectoryDialog / WindowShow 等）的 binding 都用这个。
	ctx context.Context
}

// startup 由 Wails 在窗口创建完成时调用，注入 runtime ctx。私有也能被 Wails 识别。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// Version 前端可调：window.go.main.App.Version()
func (a *App) Version() string {
	if commit != "" {
		return fmt.Sprintf("%s (%s)", version, commit)
	}
	return version
}

// DiscoverBots 扫描本机已安装的排障机器人（tshoot.json 锚点）。
// 默认只扫 ~/.openclaw/workspace/（桌面 app 的 CWD 没意义，不同于 CLI 的 DefaultRoots）。
// extraRoots 是 UI 侧让用户追加的项目根（用于找 claude-code / cursor 装进去的机器人）。
// 前端调用：window.go.main.App.DiscoverBots([]) or DiscoverBots(["/path/to/project"]).
func (a *App) DiscoverBots(extraRoots []string) ([]discover.DiscoveredAgent, error) {
	roots := append([]string{"~/.openclaw/workspace"}, extraRoots...)
	return discover.Scan(roots)
}

// ValidateResult 与 /api/validate 返回形状对齐，前端已有依赖。
type ValidateResult struct {
	Valid  bool   `json:"valid"`
	System string `json:"system"`
	Name   string `json:"name"`
	Envs   int    `json:"envs"`
	Repos  int    `json:"repos"`
}

// Validate 校验 system.yaml 内容，解析失败返回 error；成功返回概要。
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
	}, nil
}

// Gen 按 system.yaml 实际落盘生成机器人产物（写到 outputDir；后续要部署还得走
// ImportAndDeploy 或 RunInstall 把产物装到 AI 平台）。
// outputDir 为空时用 yaml 里的 generation.output_dir；相对路径解析成绝对路径，
// 让 UI 能稳定展示"产物在 /abs/path/xxx"。
func (a *App) Gen(yamlText, outputDir string) (*generator.GenSummary, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if outputDir == "" {
		outputDir = cfg.Generation.OutputDir
	}
	if outputDir == "" {
		outputDir = "./dist"
	}
	if !filepath.IsAbs(outputDir) {
		abs, _ := filepath.Abs(outputDir)
		outputDir = abs
	}
	g := generator.New(cfg, a.templateRoot, outputDir)
	g.TshootVersion = version
	g.SystemYAMLSource = []byte(yamlText)
	if err := g.Generate(); err != nil {
		return nil, err
	}
	return g.Summary, nil
}

// Plan 干跑一次 gen,返回 Plan 结构(skills / files 分布 / config-map 投影)。
// 等价 POST /api/plan。不落盘(generator 写到临时目录后清掉)。
func (a *App) Plan(yamlText string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-plan-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan("")
}

// Diff 跟 Plan 用同一个底层(generator.BuildPlan),区别是传入 existingDir,
// 让底层 diffFileSets 给出 create / modify / remove 三类文件差异,用于新产物
// vs 现有产物的对比预览。existingDir 为空时等价 Plan。
func (a *App) Diff(yamlText, existingDir string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan(existingDir)
}

// Analyze 扫描 reposRoot 下的所有仓库(按 repos[].name 匹配子目录),抽 service_names
// 和配置中心线索,返回完整 report + 每仓库摘要。
// autoClone=true 时缺失的仓库会自动 shallow clone(需要 git + 凭证);默认 false,
// 缺失的仓库标 "skipped"。进度日志通过 Wails EventsEmit "analyze:log" 推到前端。
func (a *App) Analyze(yamlText, reposRoot string, autoClone bool) (*analyzerpipe.Result, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return analyzerpipe.Run(cfg, analyzerpipe.Options{
		ReposRoot: reposRoot,
		AutoClone: autoClone,
		OnProgress: func(msg string) {
			wailsruntime.EventsEmit(a.ctx, "analyze:log", msg)
		},
	})
}

// Doctor 对比声明 vs 代码实态,返回漂移报告。等价 POST /api/doctor?repos_root=...
// reposRoot 留空则只校验声明的一致性,不扫代码。
func (a *App) Doctor(yamlText, reposRoot string) (*doctor.Report, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	return doctor.Check(cfg, reposRoot)
}

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

// OpenYAML 弹原生打开对话框让用户选一个 yaml 文件，返回 {path, content}。
// 用户取消返回 ({"", "", nil)。前端据此驱动'导入 yaml 部署机器人'流程。
type OpenYAMLResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

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

// ImportAndDeploy 把 yaml 直接部署成新机器人（agent.ImportAndApply 的 UI 封装）。
// target: openclaw / claude-code / cursor / standalone
// destPath: 部署目标路径。openclaw 下是产物目录（含 install.sh）；其它 target 下是目标项目根。
func (a *App) ImportAndDeploy(yamlText, target, destPath string) (*agent.Result, error) {
	return agent.ImportAndApply([]byte(yamlText), target, destPath, agent.ApplyOptions{
		TemplateRoot:  a.templateRoot,
		TshootVersion: version,
	})
}

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

// RunInstall 先把 creds 写进 outputDir/scripts/.env，再 shell-out bash install.sh，
// 返回合并日志。前端拿去展示在模态框里。
type RunInstallResult struct {
	Log      string `json:"log"`
	ExitCode int    `json:"exit_code"`
	OK       bool   `json:"ok"`
}

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

func main() {
	tr := resolveTemplateDir()
	srv := &api.Server{TemplateRoot: tr}
	router := api.NewRouter(srv, webui.Distribution())

	appState := &App{templateRoot: tr}

	err := wails.Run(&options.App{
		Title:  "Troubleshooter Studio",
		Width:  1280,
		Height: 860,

		// AssetServer.Handler = 所有 HTTP 请求（静态 SPA 资源 + /api/*）走这里。
		// 不设 Assets，让 router 一肩挑（NewRouter 里已经做了 SPA fallback + CORS）。
		AssetServer: &assetserver.Options{
			Handler: router,
		},

		Mac: &mac.Options{
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
//  4. embed fallback：解压 tsf.TemplatesFS 到 ~/.tshoot/templates/
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
	if err := extractEmbedded(tsf.TemplatesFS, "templates", dst); err != nil {
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
