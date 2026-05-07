// bindings_deploy.go —— openclaw 部署 binding:解析需要的凭证字段、写 .env、原生
// Go 跑安装、流式日志推送。
//
// 历史:之前 RunInstall 是 shell-out `bash scripts/install.sh`,~500 行 bash + 嵌入
// Python。现在改成原生 Go(internal/agent.InstallNativeOpenclaw),好处:
//   - 桌面端不再依赖 bash / Python 进程,跨平台行为一致
//   - 取消逻辑统一走 ctx.cancel,不用 Setpgid + SIGKILL 进程组那套
//   - 凭证 prompts 直接照 troubleshooter.yaml 派生,跟模板的 read_var 1:1 对齐(由
//     agent.DerivePrompts 生成,不再扫脚本)
//
// 接口形态保留:UI 还是先 ScanInstallPrompts → 渲染表单 → ReadEnv 预填 →
// RunInstall 收集后跑;ScanInstallPrompts 改读 tshoot.json 里的 troubleshooter.yaml。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// ScanInstallPrompts 推导 outputDir 这份 openclaw 产物需要的凭证字段。
// 数据源:<outputDir>/tshoot.json 的 SystemYAML(install.sh 没了不能再扫脚本)。
// 非 openclaw target / 没 tshoot.json → 返回 nil(无 install 步骤)。
func (a *App) ScanInstallPrompts(outputDir string) ([]deploy.Prompt, error) {
	cfg, target, err := loadStagingConfig(outputDir)
	if err != nil {
		// tshoot.json 不存在 → 没装步骤,返回空列表;别的错往上抛
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// 只有 openclaw 走凭证表单 install 流程;其它 target 在 ImportAndApply 内部
	// 已经 native install 完,UI 不需要再问一次。
	if target != "openclaw" {
		return nil, nil
	}
	return agent.DerivePrompts(cfg), nil
}

// loadStagingConfig 读 staging 目录的 tshoot.json,返回 cfg + target。
// 两个候选位置(谁先存在用谁):
//   - <outputDir>/tshoot.json                              ← claude-code / cursor staging
//   - <outputDir>/templates/workspace-template/tshoot.json ← openclaw staging
//
// openclaw 之所以放子目录:那个子树会被 cp 到 ~/.openclaw/workspace/<name>/
// 当成 discover 锚点,如果在 staging 根再写一份会被 discover 扫成重复卡。
func loadStagingConfig(outputDir string) (*config.SystemConfig, string, error) {
	candidates := []string{
		filepath.Join(outputDir, discover.MetaFilename),
		filepath.Join(outputDir, "templates", "workspace-template", discover.MetaFilename),
	}
	var (
		data []byte
		err  error
	)
	for _, p := range candidates {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	if err != nil {
		return nil, "", err // 最后一个 not-exist
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, "", fmt.Errorf("parse tshoot.json: %w", err)
	}
	cfg, err := config.LoadFromBytes([]byte(meta.SystemYAML))
	if err != nil {
		return nil, meta.Target, fmt.Errorf("troubleshooter.yaml in tshoot.json invalid: %w", err)
	}
	return cfg, meta.Target, nil
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

// RunInstallResult 等同之前 shell-out 时代的结构,UI 字段不变。
type RunInstallResult struct {
	Log      string `json:"log"`
	ExitCode int    `json:"exit_code"`
	OK       bool   `json:"ok"`
}

// RunInstall 跑 openclaw 原生 install:
//  1. 把 creds 写进 outputDir/scripts/.env(每次都覆盖,UI 表单值是权威源)
//  2. 调 agent.InstallNativeOpenclaw 完成 workspace 安装 + openclaw.json 注入 +
//     creds.json 写入 + gateway 重启
//  3. 流式日志通过 "install:log" event 推 UI(跟 shell-out 时代行为一致)
//
// 取消支持:ctx 被 CancelInstall 触发时,InstallNativeOpenclaw 内只在调用
// `openclaw gateway restart` 时受 ctx 影响;其它纯文件操作快速返回。语义跟
// 老版"SIGKILL bash 进程组"略弱(纯 IO 步骤不可中断),但实际跑完只要几秒,
// 用户体验差异不大。
func (a *App) RunInstall(outputDir string, creds map[string]string) (*RunInstallResult, error) {
	// 从 outputDir/tshoot.json 读出嵌入的 system_yaml,抽 prefill 默认值,作为 user creds 的 fallback。
	// 用户在 GUI 表单填的 creds 始终优先(MergeCredsWithPrefill 内部 user wins),
	// 这里只兜底"用户没在表单填但 yaml 里写过"的字段(常见场景:Editor / BotsPage 导入路径)。
	if cfg, _, err := loadStagingConfig(outputDir); err == nil && cfg != nil {
		creds = agent.MergeCredsWithPrefill(creds, agent.PrefillCredsFromYAML(cfg))
	}
	if err := deploy.WriteEnvFile(outputDir, creds); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(a.ctx)
	a.installMu.Lock()
	if a.installCancel != nil {
		// 上一个还没结束,先取消(UI 应该禁了按钮防并发,走到这里是 UI bug)
		a.installCancel()
	}
	a.installCancel = cancel
	a.installMu.Unlock()

	defer func() {
		a.installMu.Lock()
		a.installCancel = nil
		a.installMu.Unlock()
		cancel()
	}()

	var logBuf strings.Builder
	onLog := func(line string) {
		logBuf.WriteString(line)
		logBuf.WriteByte('\n')
		wailsruntime.EventsEmit(a.ctx, "install:log", line)
	}

	err := agent.InstallNativeOpenclaw(runCtx, outputDir, agent.InstallOpenclawOptions{
		Creds: creds,
		OnLog: onLog,
	})
	// 装完一个 bot,assertBotWorkspacePath 的 5 分钟 TTL cache 立即失效,
	// 用户切到 BotsPage 浏览新装的 workspace 不必等 cache 过期。
	invalidateBotPathsCache()
	res := &RunInstallResult{Log: logBuf.String(), OK: err == nil}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			res.ExitCode = -2
			res.OK = false
			return res, nil
		}
		res.ExitCode = -1
		// 错误同时进 log,UI 上能看到完整原因
		logBuf.WriteString("[error] " + err.Error() + "\n")
		res.Log = logBuf.String()
		return res, nil
	}
	return res, nil
}

// CancelInstall 前端"取消安装"按钮调:目前只能在 gateway restart 阶段生效,
// 其它纯 Go IO 步骤不可中断。UI 上仍然显示"已取消"避免用户困惑。
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

// SelfTestAgent 跑 openclaw 自检(替代 scripts/self-test.sh)。dir 是 staging
// 或已部署 workspace 都行;返回各项检查结果给 UI 渲染。
func (a *App) SelfTestAgent(dir string) (*agent.SelfTestResult, error) {
	return agent.SelfTestOpenclaw(a.ctx, dir)
}

// UninstallAgent 卸载 openclaw agent(替代 scripts/uninstall.sh)。dir 同上,
// 移走 workspace + 从 openclaw.json 摘 agent + 清 creds.json。
func (a *App) UninstallAgent(dir string) (*agent.UninstallOpenclawResult, error) {
	return agent.UninstallNativeOpenclaw(dir)
}

// UninstallBotResult 给前端的统一形状,涵盖 openclaw / claude-code / cursor 三种 target。
// openclaw 字段(WorkspaceMovedTo / OpenclawJSONClean / CredsRemoved)只在 target=openclaw 时填。
// native 字段(StagingMovedTo / UserAgentMD / UserSkillsDir / UserScriptsDir)在 claude-code / cursor 时填。
type UninstallBotResult struct {
	Target            string   `json:"target"`
	WorkspaceMovedTo  string   `json:"workspace_moved_to,omitempty"`  // openclaw
	OpenclawJSONClean bool     `json:"openclaw_json_clean,omitempty"` // openclaw
	CredsRemoved      bool     `json:"creds_removed,omitempty"`       // openclaw
	StagingMovedTo    string   `json:"staging_moved_to,omitempty"`    // claude-code / cursor / codex
	UserAgentMD       string   `json:"user_agent_md,omitempty"`
	UserSkillsDir     string   `json:"user_skills_dir,omitempty"`
	UserScriptsDir    string   `json:"user_scripts_dir,omitempty"`
	MCPRemoved        []string `json:"mcp_removed,omitempty"` // 从 IDE 配置清掉的 MCP server keys
	Log               []string `json:"log,omitempty"`
}

// UninstallBot 按 target 分派到对应卸载实现。BotsPage 的"卸载"按钮调这个,
// dir 一律传 BotsPage 列表里那条机器人的 path(中间包 / workspace 都接受 —— 各 target
// 实现内部会自己解析 tshoot.json 找真实位置)。
//
// 同步:成功卸载后从 ~/.tshoot/config.json deployed_bots 里也清掉对应记录,
// 否则 BotsPage 下次刷新会把它标 ghost(disk 已删但 deployed_bots 还在)。
func (a *App) UninstallBot(dir, target string) (*UninstallBotResult, error) {
	// 从 dir 反查 system_id —— 卸载成功后用来同步 RemoveDeployedBot。
	// 失败容忍:扫不到就 systemID="",RemoveDeployedBot 自己 nil-safe。
	var systemID string
	if found, _ := discover.Scan([]string{dir}); len(found) > 0 {
		systemID = found[0].Meta.SystemID
	}
	switch target {
	case "openclaw":
		r, err := agent.UninstallNativeOpenclaw(dir)
		if err != nil {
			return nil, err
		}
		invalidateBotPathsCache() // bot 已卸,失效 5 分钟 cache
		_ = userconfig.RemoveDeployedBot(systemID, target)
		return &UninstallBotResult{
			Target:            target,
			WorkspaceMovedTo:  r.WorkspaceMovedTo,
			OpenclawJSONClean: r.OpenclawJSONClean,
			CredsRemoved:      r.CredsRemoved,
			Log:               r.Log,
		}, nil
	case "claude-code", "cursor", "codex":
		r, err := agent.UninstallNative(dir, target)
		if err != nil {
			return nil, err
		}
		invalidateBotPathsCache() // bot 已卸,失效 5 分钟 cache
		_ = userconfig.RemoveDeployedBot(systemID, target)
		return &UninstallBotResult{
			Target:         target,
			StagingMovedTo: r.StagingMovedTo,
			UserAgentMD:    r.UserAgentMD,
			UserSkillsDir:  r.UserSkillsDir,
			UserScriptsDir: r.UserScriptsDir,
			MCPRemoved:     r.MCPRemoved,
			Log:            r.Log,
		}, nil
	default:
		return nil, fmt.Errorf("UninstallBot: unsupported target %q", target)
	}
}

// ForgetGhostBot 把 ~/.tshoot/config.json deployed_bots 里某条记录删掉。
// BotsPage 卡片对 ghost(disk 已不在的)bot 提供"忘掉它"入口,调这个;disk 上
// 没东西可删,只清 ghost 元数据。disk 还在的 bot 应走 UninstallBot,**不要**
// 通过这条绕过(否则会留下没追踪的 disk 残留)。
func (a *App) ForgetGhostBot(systemID, target string) error {
	return userconfig.RemoveDeployedBot(systemID, target)
}
