package bughub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

const (
	codexStudioAgentNetworkConfig = "permissions.studio_agent.network.enabled=true"
	claudeStudioAgentSettings     = `{"sandbox":{"enabled":false}}`
	codexGoSandboxDirectory       = ".tshoot-go"
)

type codexGoSandbox struct {
	goRoot      string
	buildCache  string
	goPath      string
	moduleCache string
	telemetry   string
}

type InvestigationEventSink func(run InvestigationRun, event InvestigationEvent)

type CodexInvestigator struct {
	store     *InvestigationStore
	codexBin  string
	binaries  map[string]string
	mu        sync.Mutex
	active    map[string]*activeCodexRun
	eventSink InvestigationEventSink
}

type activeCodexRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	errMu   sync.Mutex
	err     error
	process *os.Process
}

func NewCodexInvestigator(store *InvestigationStore, codexBin string) *CodexInvestigator {
	if strings.TrimSpace(codexBin) == "" {
		codexBin = "codex"
	}
	return &CodexInvestigator{
		store:    store,
		codexBin: codexBin,
		binaries: map[string]string{
			"codex":       codexBin,
			"claude-code": "claude",
		},
		active: make(map[string]*activeCodexRun),
	}
}

func (i *CodexInvestigator) SetBinaryForTarget(target, bin string) {
	if i == nil {
		return
	}
	target = strings.TrimSpace(target)
	bin = strings.TrimSpace(bin)
	if target == "" || bin == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.binaries == nil {
		i.binaries = make(map[string]string)
	}
	i.binaries[target] = bin
	if target == "codex" {
		i.codexBin = bin
	}
}

func (i *CodexInvestigator) SetEventSink(sink InvestigationEventSink) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.eventSink = sink
}

func BuildCodexInvestigationPrompt(b Bug, bot BotRef) string {
	return "请作为选定排障机器人执行只读根因分析，遵循 incident-investigator/SKILL.md 的 7 步排障图谱。Read `incident-investigator/SKILL.md`。从工单、附件、日志、链路、配置、数据和源码收集实际证据；没有自动复现或业务验证阶段。证据不足时明确缺口，不猜测根因。不修改代码或执行写操作。\n" + GenerateContext(b, bot) + investigationOutputContract()
}

func BuildCodexContinuePrompt(b Bug, bot BotRef, userInput string, prevRun InvestigationRun) string {
	return BuildCodexInvestigationPrompt(b, bot) + "\n## 前轮排障输出（作为证据参考）\n" + prevRun.FinalMessage + "\n## 用户补充信息\n" + strings.TrimSpace(userInput)
}

func BuildCodexFixPrompt(b Bug, bot BotRef, prevRun InvestigationRun, userInput string) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 修复 Agent。\n")
	sb.WriteString("目标：基于 Bug 工单、验证证据和排障结论落地修复。只有用户明确触发修复后才执行。\n\n")
	sb.WriteString("第一步：如果当前机器人 workspace 中存在 `bug-fixer/SKILL.md`，必须先 Read 它，并按其中流程执行。\n")
	sb.WriteString("如果旧安装还没有该 skill，使用下面内置流程作为兼容兜底。\n\n")
	sb.WriteString("## 强制流程\n\n")
	sb.WriteString("1. Studio 会同时提供用户批准的开发基线和 routing 确定的目标环境分支；两者是独立概念，当前 checkout 和环境分支都不得擅自替代开发基线。\n")
	sb.WriteString("2. 必须只在 Studio locked fix workspace 中，从锁定的开发基线 commit 创建独立修复分支。缺少 locked workspace、仓库或开发基线绑定时停止并报告配置缺口；禁止自行从当前 HEAD 或环境分支兜底创建。\n")
	sb.WriteString("3. 修复 Bug，只做最小必要改动，不做无关重构，不改无关文件。\n")
	sb.WriteString("4. 运行相关测试、构建或最小验证命令；无法运行时说明具体原因。\n")
	sb.WriteString("5. `git status` 确认只包含本次修复文件后提交，并推送修复分支。\n")
	sb.WriteString("6. 最终输出分支名、commit、push 结果和测试结果，等待 Studio 单独授权后把修复提交分别合入开发基线与环境分支。\n\n")
	sb.WriteString("## 停止条件\n\n")
	sb.WriteString("- 如果工作区已有用户未提交改动或无法确认这些改动属于本次修复，停止并说明，不要覆盖。\n")
	sb.WriteString("- 如果无法确认开发基线或环境分支、无法创建分支、无法提交或无法推送，停止并说明阻塞点。\n")
	sb.WriteString("- 不自行部署，不修改生产配置，不执行破坏性命令。\n\n")
	if strings.TrimSpace(userInput) != "" {
		sb.WriteString("## 用户补充修复要求\n\n")
		sb.WriteString(strings.TrimSpace(userInput))
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Bug 上下文\n\n")
	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString("\n")
	appendPreviousRunForFix(&sb, prevRun)
	sb.WriteString(fixOutputContract())
	return sb.String()
}

func appendPreviousRunForFix(sb *strings.Builder, prevRun InvestigationRun) {
	if sb == nil {
		return
	}
	var validationParts []string
	var investigationParts []string
	for _, e := range prevRun.Events {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		phase, _ := e.Meta["phase"].(string)
		switch phase {
		case "validation":
			validationParts = append(validationParts, msg)
		case "investigation":
			investigationParts = append(investigationParts, msg)
		}
	}
	if len(validationParts) > 0 {
		sb.WriteString("## 历史工单证据\n\n")
		for _, p := range validationParts {
			sb.WriteString(p)
			sb.WriteString("\n\n")
		}
	}
	if len(investigationParts) > 0 || strings.TrimSpace(prevRun.FinalMessage) != "" {
		sb.WriteString("## 排障 Agent 结论\n\n")
		for _, p := range investigationParts {
			sb.WriteString(p)
			sb.WriteString("\n\n")
		}
		if strings.TrimSpace(prevRun.FinalMessage) != "" {
			sb.WriteString(strings.TrimSpace(prevRun.FinalMessage))
			sb.WriteString("\n\n")
		}
	}
}

func investigationOutputContract() string {
	var sb strings.Builder
	sb.WriteString("\n## 最终输出契约（必须遵守）\n\n")
	sb.WriteString("最终回答必须使用下面的故障快报模板，不要改成普通列表或过程总结。证据不足时也要输出故障快报，但把置信度标为中/低，并在“需补信息”里列明缺口。\n\n")
	sb.WriteString("```text\n")
	sb.WriteString("🚨 故障快报 | <环境> | <服务/模块>\n")
	sb.WriteString("🕒 时间: <故障窗口，使用绝对时间和时区>\n")
	sb.WriteString("📌 结论: <一句话根因或低置信度疑似结论>\n")
	sb.WriteString("1) 影响范围  <用户影响 / 页面或接口 / 错误量或复现范围>\n")
	sb.WriteString("2) 关键信号  <TOP 3 信号，包含时间、trace_id/request_id/日志关键词/指标结论>\n")
	sb.WriteString("3) 已查证据  [已查] trace/log/metric/code/config/data 中实际查过的证据；[未查+原因] 不可用或用户未提供的关键证据\n")
	sb.WriteString("4) 根因      <直接根因 + 深层根因 + 置信度: 高/中/低 + 维度自检>\n")
	sb.WriteString("5) 处置      <P0 止血 / P1 修复 / P2 预防；低置信度不得给生产修改命令>\n")
	sb.WriteString("6) 验证      <如何确认恢复或如何复查修复>\n")
	sb.WriteString("7) 需补信息  <无则写 []；有则列最小阻塞项及获取方式>\n")
	sb.WriteString("```\n\n")
	sb.WriteString("如果 confidence=high，按 `incident-investigator/SKILL.md` 的步骤 7 追加 known-errors.local.yaml 沉淀草稿；confidence=medium/low 时不要追加沉淀草稿。\n")
	return sb.String()
}

func fixOutputContract() string {
	var sb strings.Builder
	sb.WriteString("\n## 最终输出契约（必须遵守）\n\n")
	sb.WriteString("修复完成、阻塞或失败时，最终回答必须只输出下面的 YAML 结构，不要改成普通列表：\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("fix_status: fixed_pushed | blocked | failed\n")
	sb.WriteString("environment: \"<env>\"\n")
	sb.WriteString("branches:\n")
	sb.WriteString("  - repo: \"<repo>\"\n")
	sb.WriteString("    base_branch: \"<user-approved-source-baseline-branch>\"\n")
	sb.WriteString("    fix_branch: \"<fix-branch>\"\n")
	sb.WriteString("    commit: \"<sha-or-empty>\"\n")
	sb.WriteString("    pushed: true\n")
	sb.WriteString("    target_environment_branch: \"<env-branch>\"\n")
	sb.WriteString("    push_remote: \"<remote>\"\n")
	sb.WriteString("changes:\n")
	sb.WriteString("  - repo: \"<repo>\"\n")
	sb.WriteString("    summary: \"<file-or-module>: <what changed>\"\n")
	sb.WriteString("tests:\n")
	sb.WriteString("  - repo: \"<repo>\"\n")
	sb.WriteString("    commit: \"<tested-fix-commit>\"\n")
	sb.WriteString("    command: \"<command>\"\n")
	sb.WriteString("    result: passed | failed | skipped\n")
	sb.WriteString("    note: \"<short evidence>\"\n")
	sb.WriteString("    skipped_reason: \"<required only when result=skipped>\"\n")
	sb.WriteString("deployment_notice: \"修复分支已推送；等待用户独立授权，由 Studio 将修复提交合入开发基线和环境分支，之后由用户人工验收。\"\n")
	sb.WriteString("risks:\n")
	sb.WriteString("  - \"<remaining-risk-or-empty>\"\n")
	sb.WriteString("blocked_reason: \"<only when blocked/failed>\"\n")
	sb.WriteString("evidence: []\n")
	sb.WriteString("```\n")
	sb.WriteString("修复阶段通常不需要另写总结文件作为 evidence，阻塞或失败时保持 `evidence: []`。只有确有可注册的运行时或测试制品时才添加条目，而且每项必须同时包含非空 `kind`、staging 相对 `path` 和 `environment`。\n")
	sb.WriteString("每个仓库的 base_branch 必须是 Studio 锁定的用户确认开发基线；target_environment_branch 是后续独立集成目标，两者允许不同。fix_branch 必须是不同的专用修复分支，禁止直接在基线分支或环境分支修复、提交或推送；Agent 也不得自行合并这两个目标分支。\n")
	return sb.String()
}

func BuildCodexExecCommand(codexBin, workspace, prompt string) (*exec.Cmd, error) {
	return buildCodexExecCommandWithProfile(codexBin, workspace, prompt, nil, "")
}

func buildCodexExecCommand(codexBin, workspace, prompt string, imagePaths []string) (*exec.Cmd, error) {
	return buildCodexExecCommandWithProfile(codexBin, workspace, prompt, imagePaths, "")
}

func buildCodexExecCommandWithProfile(codexBin, workspace, prompt string, imagePaths []string, profile string) (*exec.Cmd, error) {
	codexBin = strings.TrimSpace(codexBin)
	if codexBin == "" {
		codexBin = "codex"
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("workspace is required")
	}
	info, err := os.Stat(workspace)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("workspace %q does not exist", workspace)
		}
		return nil, fmt.Errorf("check workspace %q: %w", workspace, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %q is not a directory", workspace)
	}
	goSandbox, err := prepareCodexGoSandbox(workspace, prompt)
	if err != nil {
		return nil, err
	}
	filesystemConfig, err := codexFilesystemPermissionConfig(workspace, prompt, imagePaths, goSandbox)
	if err != nil {
		return nil, err
	}
	args := []string{
		"exec", "--json",
		"--enable", "respect_system_proxy",
		"-c", "suppress_unstable_features_warning=true",
		// Studio background Agents cannot answer an interactive approval dialog.
		// More importantly, the named filesystem profile below gives Codex a
		// host-generated allowlist, so a model command cannot traverse $HOME and
		// trigger macOS App Data / Documents / Photos privacy prompts.
		"-c", `approval_policy="never"`,
		// Current Codex CLI requires default_permissions when named permission
		// profiles are defined. permission_profile was accepted by older builds
		// but now leaves the profile table without an active default and makes
		// `codex exec` fail before the Agent turn starts.
		"-c", `default_permissions="studio_agent"`,
		// Every workflow phase may need runtime evidence, dependency downloads,
		// MCP/API access, or Git transport. A named Codex permission profile has
		// networking disabled by default, and respect_system_proxy only controls
		// proxy behavior; it does not grant network access by itself.
		"-c", codexStudioAgentNetworkConfig,
		"-c", filesystemConfig,
	}
	if profile = strings.TrimSpace(profile); profile != "" {
		args = append(args, "--profile", profile)
	}
	args = append(args, "--cd", workspace, "--skip-git-repo-check")
	for _, path := range imagePaths {
		args = append(args, "--image", path)
	}
	// Codex defines --image as a variadic option (`--image <FILE>...`). Without
	// the option terminator, the initial prompt is consumed as another image
	// path and `codex exec` exits without starting the agent.
	if len(imagePaths) != 0 {
		args = append(args, "--")
	}
	args = append(args, prompt)
	cmd := exec.Command(codexBin, args...)
	cmd.Dir = workspace
	cmd.Env = codexAgentProcessEnvironment(prompt, goSandbox)
	return cmd, nil
}

func buildCodexBotExecCommand(codexBin string, bot BotRef, prompt string, imagePaths []string) (*exec.Cmd, error) {
	profile, codexHome, err := ensureCodexAgentRuntimeHome(bot)
	if err != nil {
		return nil, err
	}
	cmd, err := buildCodexExecCommandWithProfile(codexBin, bot.Path, prompt, imagePaths, profile)
	if err != nil {
		return nil, err
	}
	if codexHome != "" {
		cmd.Env = setProcessEnv(cmd.Env, "CODEX_HOME", codexHome)
	}
	return cmd, nil
}

func ensureCodexAgentRuntimeHome(bot BotRef) (string, string, error) {
	agentID := strings.TrimSpace(bot.AgentID)
	if agentID == "" {
		return "", "", nil
	}
	workspace := filepath.Clean(strings.TrimSpace(bot.Path))
	skillsDir := filepath.Dir(workspace)
	if filepath.Base(skillsDir) != "skills" || filepath.Base(workspace) != agentID {
		// Tests and embedding callers may inject a standalone workspace rather
		// than a native ~/.codex/skills/<agent> installation. There is no safe
		// runtime root to infer in that case, so preserve the legacy command.
		return "", "", nil
	}
	if strings.ContainsAny(agentID, "/\\\x00") || agentID == "." || agentID == ".." {
		return "", "", errors.New("Codex agent id is invalid")
	}
	codexHome := filepath.Dir(skillsDir)
	runtimeHome := filepath.Join(codexHome, "tshoot-runtimes", agentID)
	if err := ensureCodexRuntimeDirectory(runtimeHome); err != nil {
		return "", "", err
	}

	// Codex profile layers currently ignore mcp_servers. Give each background
	// agent an isolated CODEX_HOME whose standard config.toml contains exactly the
	// managed MCP region. This keeps business MCPs out of the user's global main
	// session while using a configuration path that codex exec actually loads.
	agentPath := filepath.Join(codexHome, "agents", agentID+".toml")
	raw, err := os.ReadFile(agentPath)
	if err != nil {
		return "", "", fmt.Errorf("Codex runtime config source is missing; re-apply agent %q: %w", agentID, err)
	}
	text := string(raw)
	begin := strings.Index(text, generator.CodexMCPRegionBegin)
	end := strings.Index(text, generator.CodexMCPRegionEnd)
	if begin < 0 || end < begin {
		return "", "", fmt.Errorf("Codex runtime config source is invalid and agent %q has no managed MCP region; re-apply the robot", agentID)
	}
	bodyStart := begin + len(generator.CodexMCPRegionBegin)
	body := strings.TrimSpace(text[bodyStart:end])
	content := "# Managed by tshoot. Used by Studio background codex exec.\n" + body + "\n"
	configPath := filepath.Join(runtimeHome, "config.toml")
	if info, err := os.Lstat(configPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("Codex runtime config %s is not a regular file", configPath)
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if current, readErr := os.ReadFile(configPath); readErr == nil && string(current) == content {
		if err := os.Chmod(configPath, 0o600); err != nil {
			return "", "", fmt.Errorf("secure Codex runtime config %s: %w", configPath, err)
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("read Codex runtime config %s: %w", configPath, readErr)
	} else if err := replaceCodexRuntimeConfig(configPath, []byte(content)); err != nil {
		return "", "", err
	}
	for _, shared := range []string{"skills", "auth.json"} {
		target := filepath.Join(codexHome, shared)
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", "", fmt.Errorf("inspect shared Codex %s: %w", shared, err)
		}
		if err := ensureCodexRuntimeLink(filepath.Join(runtimeHome, shared), target); err != nil {
			return "", "", err
		}
	}
	return "", runtimeHome, nil
}

func ensureCodexRuntimeDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Codex runtime home %s is not a directory", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Codex runtime home %s: %w", path, err)
	} else if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create Codex runtime home %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure Codex runtime home %s: %w", path, err)
	}
	return nil
}

func ensureCodexRuntimeLink(path, target string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("Codex runtime shared path %s is not a symlink", path)
		}
		linked, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("read Codex runtime link %s: %w", path, err)
		}
		if !filepath.IsAbs(linked) {
			linked = filepath.Join(filepath.Dir(path), linked)
		}
		if filepath.Clean(linked) != filepath.Clean(target) {
			return fmt.Errorf("Codex runtime link %s points outside the managed target", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Codex runtime link %s: %w", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("create Codex runtime link %s: %w", path, err)
	}
	return nil
}

func replaceCodexRuntimeConfig(path string, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tshoot-codex-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Codex runtime config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary Codex runtime config: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary Codex runtime config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary Codex runtime config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Codex runtime config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Codex runtime config %s: %w", path, err)
	}
	return nil
}

func setProcessEnv(values []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(values)+1)
	for _, item := range values {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		result = append(result, item)
	}
	return append(result, prefix+value)
}

var (
	codexGitBinDirOnce sync.Once
	codexGitBinDir     string
)

func codexAgentProcessEnvironment(prompt string, goSandbox *codexGoSandbox) []string {
	values := os.Environ()
	staging := codexStagingPathFromPrompt(prompt)
	if staging == "" || codexRepositoryAccessPhase(staging) != PhaseFix {
		return values
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"TMPDIR":              staging,
		"TMP":                 staging,
		"TEMP":                staging,
	} {
		values = setProcessEnv(values, key, value)
	}
	if goSandbox != nil {
		for key, value := range map[string]string{
			"GOROOT":         goSandbox.goRoot,
			"GOCACHE":        goSandbox.buildCache,
			"GOPATH":         goSandbox.goPath,
			"GOMODCACHE":     goSandbox.moduleCache,
			"GOTELEMETRY":    "off",
			"GOTELEMETRYDIR": goSandbox.telemetry,
			"GOENV":          "off",
			"GOTOOLCHAIN":    "auto",
		} {
			values = setProcessEnv(values, key, value)
		}
		goFlags := strings.TrimSpace(processEnvValue(values, "GOFLAGS"))
		if !strings.Contains(" "+goFlags+" ", " -modcacherw ") {
			goFlags = strings.TrimSpace(goFlags + " -modcacherw")
		}
		values = setProcessEnv(values, "GOFLAGS", goFlags)
	}
	if directory := codexHostGitBinDirectory(); directory != "" {
		path := processEnvValue(values, "PATH")
		if path == "" {
			path = directory
		} else {
			path = directory + string(os.PathListSeparator) + path
		}
		values = setProcessEnv(values, "PATH", path)
	}
	if goSandbox != nil {
		directory := filepath.Join(goSandbox.goRoot, "bin")
		path := processEnvValue(values, "PATH")
		if path == "" {
			path = directory
		} else {
			path = directory + string(os.PathListSeparator) + path
		}
		values = setProcessEnv(values, "PATH", path)
	}
	return values
}

func prepareCodexGoSandbox(workspace, prompt string) (*codexGoSandbox, error) {
	staging := codexStagingPathFromPrompt(prompt)
	if staging == "" || codexRepositoryAccessPhase(staging) != PhaseFix {
		return nil, nil
	}
	goRoot := resolveCodexGoRoot(workspace)
	if goRoot == "" {
		// A fix may target JavaScript, Python, or another stack. Absence of a
		// host Go toolchain must not prevent those Agents from starting.
		return nil, nil
	}
	root := filepath.Join(filepath.Clean(staging), codexGoSandboxDirectory)
	sandbox := &codexGoSandbox{
		goRoot:      goRoot,
		buildCache:  filepath.Join(root, "build-cache"),
		goPath:      filepath.Join(root, "path"),
		moduleCache: filepath.Join(root, "path", "pkg", "mod"),
		telemetry:   filepath.Join(root, "telemetry"),
	}
	for _, directory := range []string{root, sandbox.buildCache, sandbox.goPath, sandbox.moduleCache, sandbox.telemetry} {
		if err := ensureCodexRuntimeDirectory(directory); err != nil {
			return nil, fmt.Errorf("prepare isolated Codex Go directory: %w", err)
		}
	}
	return sandbox, nil
}

func resolveCodexGoRoot(workspace string) string {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	cmd := exec.Command(goBinary, "env", "GOROOT")
	cmd.Dir = workspace
	cmd.Env = setProcessEnv(setProcessEnv(os.Environ(), "GOTELEMETRY", "off"), "GOENV", "off")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	goRoot := filepath.Clean(strings.TrimSpace(string(output)))
	if goRoot == "." || !filepath.IsAbs(goRoot) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(goRoot); err == nil {
		goRoot = filepath.Clean(resolved)
	}
	info, err := os.Stat(goRoot)
	if err != nil || !info.IsDir() {
		return ""
	}
	goExecutable := "go"
	if runtime.GOOS == "windows" {
		goExecutable += ".exe"
	}
	info, err = os.Stat(filepath.Join(goRoot, "bin", goExecutable))
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return goRoot
}

func codexRepositoryAccessPhase(staging string) Phase {
	data, err := os.ReadFile(filepath.Join(staging, repositoryAccessManifestName))
	if err != nil {
		return ""
	}
	var manifest repositoryAccessManifest
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return manifest.Phase
}

func codexHostGitBinDirectory() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	codexGitBinDirOnce.Do(func() {
		output, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output()
		if err != nil {
			return
		}
		path := filepath.Clean(strings.TrimSpace(string(output)))
		if filepath.IsAbs(path) {
			codexGitBinDir = filepath.Dir(path)
		}
	})
	return codexGitBinDir
}

func processEnvValue(values []string, key string) string {
	prefix := key + "="
	for index := len(values) - 1; index >= 0; index-- {
		if strings.HasPrefix(values[index], prefix) {
			return strings.TrimPrefix(values[index], prefix)
		}
	}
	return ""
}

func codexFilesystemPermissionConfig(workspace, prompt string, imagePaths []string, goSandbox *codexGoSandbox) (string, error) {
	roots := map[string]string{filepath.Clean(workspace): "write"}
	addCodexSSHHostVerificationFiles(roots)
	if goSandbox != nil && roots[goSandbox.goRoot] != "write" {
		// Grant the exact selected SDK read-only. Go caches and downloaded
		// modules stay inside the attempt staging directory below.
		roots[goSandbox.goRoot] = "read"
	}
	staging := codexStagingPathFromPrompt(prompt)
	if staging != "" {
		if !filepath.IsAbs(staging) {
			return "", errors.New("Studio evidence staging path must be absolute")
		}
		staging = filepath.Clean(staging)
		info, err := os.Stat(staging)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("Studio evidence staging path is unavailable: %s", staging)
		}
		roots[staging] = "write"
		manifestPath := filepath.Join(staging, repositoryAccessManifestName)
		data, readErr := os.ReadFile(manifestPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", fmt.Errorf("read repository access manifest: %w", readErr)
		}
		if readErr == nil {
			var manifest repositoryAccessManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return "", fmt.Errorf("parse repository access manifest: %w", err)
			}
			for _, root := range manifest.Roots {
				path := filepath.Clean(strings.TrimSpace(root.Path))
				if path == "." || !filepath.IsAbs(path) {
					return "", fmt.Errorf("repository access path for %s must be absolute", root.Repo)
				}
				access := strings.TrimSpace(root.Access)
				if access != "read" && access != "write" {
					return "", fmt.Errorf("repository access mode for %s is invalid", root.Repo)
				}
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					return "", fmt.Errorf("repository access path for %s is unavailable", root.Repo)
				}
				roots[path] = access
				if manifest.Phase == PhaseFix && access == "write" {
					gitMetadata := filepath.Join(path, ".git")
					gitInfo, gitErr := os.Lstat(gitMetadata)
					if gitErr != nil || !gitInfo.IsDir() {
						return "", fmt.Errorf("standalone fix repository Git metadata for %s is unavailable", root.Repo)
					}
					// Codex protects .git recursively even when its workspace root is
					// writable. Reopen only the metadata owned by Studio's standalone
					// fix clone so the Agent can create, commit, and push its fix branch.
					roots[gitMetadata] = "write"
				}
			}
		}
	}
	for _, imagePath := range imagePaths {
		path := filepath.Clean(strings.TrimSpace(imagePath))
		if path == "." || !filepath.IsAbs(path) {
			return "", errors.New("Codex attachment path must be absolute")
		}
		roots[path] = "read"
	}

	paths := make([]string, 0, len(roots))
	for path := range roots {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var config strings.Builder
	config.WriteString(`permissions.studio_agent.filesystem={":minimal"="read"`)
	for _, path := range paths {
		config.WriteByte(',')
		config.WriteString(strconv.Quote(path))
		config.WriteByte('=')
		config.WriteString(strconv.Quote(roots[path]))
	}
	config.WriteByte('}')
	return config.String(), nil
}

func addCodexSSHHostVerificationFiles(roots map[string]string) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return
	}
	sshDir := filepath.Join(filepath.Clean(home), ".ssh")
	for _, name := range []string{"known_hosts", "known_hosts2"} {
		path := filepath.Join(sshDir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if roots[path] != "write" {
			// Host verification data is public server identity metadata. Grant the
			// exact regular file only; never expose ~/.ssh, config, or private keys.
			roots[path] = "read"
		}
	}
}

func codexStagingPathFromPrompt(prompt string) string {
	const marker = "STUDIO_EVIDENCE_STAGING_DIR="
	var staging string
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, marker) {
			continue
		}
		if candidate := strings.TrimSpace(strings.TrimPrefix(line, marker)); candidate != "" {
			staging = candidate
		}
	}
	return staging
}

func BuildClaudeInvestigationCommand(claudeBin, workspace, agentPath, prompt string) (*exec.Cmd, error) {
	return buildClaudeInvestigationCommand(claudeBin, workspace, agentPath, prompt, nil)
}

func buildClaudeInvestigationCommand(claudeBin, workspace, agentPath, prompt string, attachmentDirs []string) (*exec.Cmd, error) {
	claudeBin = strings.TrimSpace(claudeBin)
	if claudeBin == "" {
		claudeBin = "claude"
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("workspace is required")
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return nil, fmt.Errorf("check workspace %q: %w", workspace, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %q is not a directory", workspace)
	}
	agentName := claudeAgentName(agentPath)
	if agentName == "" {
		return nil, errors.New("claude agent is required")
	}
	// Claude's permission mode and OS sandbox are independent. Disable the
	// per-invocation sandbox explicitly so a user-level domain allowlist cannot
	// silently block runtime queries, dependency downloads, or Git transport.
	args := []string{"-p", "--dangerously-skip-permissions", "--permission-mode", "bypassPermissions", "--settings", claudeStudioAgentSettings, "--output-format", "stream-json", "--verbose", "--agent", agentName}
	for _, directory := range attachmentDirs {
		args = append(args, "--add-dir", directory)
	}
	// Claude defines --add-dir as a variadic option. Without the option
	// terminator, the non-interactive prompt is consumed as one more directory
	// and Claude exits before the phase agent starts. This affects every
	// screenshot-assisted browser step: planning, locator repair, and final
	// evidence evaluation.
	if len(attachmentDirs) != 0 {
		args = append(args, "--")
	}
	args = append(args, prompt)
	cmd := exec.Command(claudeBin, args...)
	cmd.Dir = workspace
	return cmd, nil
}

func (i *CodexInvestigator) Start(parent context.Context, bug Bug, bot BotRef) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
	}
	target := strings.TrimSpace(bot.Target)
	i.mu.Lock()
	if active, ok, err := i.store.ActiveRunForBug(bug.ID); err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	} else if ok {
		if _, inMemory := i.active[active.ID]; inMemory {
			i.mu.Unlock()
			return active, nil
		}
		if err := i.store.Finish(active.ID, InvestigationFailed, active.FinalMessage, "investigation process is not running"); err != nil {
			i.mu.Unlock()
			return InvestigationRun{}, err
		}
	}

	prompt := BuildCodexInvestigationPrompt(bug, bot)
	cmd, parser, err := i.buildCommandLocked(target, bot, prompt)
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(parent)

	run := InvestigationRun{
		ID:            randomRunID(),
		BugID:         bug.ID,
		BotKey:        bot.Key,
		Status:        InvestigationRunning,
		StartedAt:     time.Now().UTC(),
		PromptPreview: promptPreview(prompt),
	}
	if err := i.store.Upsert(run); err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.active[run.ID] = active

	i.mu.Unlock()
	go i.collectContinueRun(ctx, run.ID, cmd, parser, active, "investigation")
	return run, nil
}

func (i *CodexInvestigator) Continue(ctx context.Context, bug Bug, bot BotRef, userInput string, previousRunID string, phase string) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
	}
	if phase != "" && phase != "investigation" && phase != "fix" {
		return InvestigationRun{}, errors.New("unsupported continuation phase")
	}
	phase = normalizeContinuationPhase(phase)

	i.mu.Lock()
	if active, ok, err := i.store.ActiveRunForBug(bug.ID); err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	} else if ok {
		if _, inMemory := i.active[active.ID]; inMemory {
			i.mu.Unlock()
			return InvestigationRun{}, fmt.Errorf("bug %s has an active investigation (%s), cannot start continuation", bug.ID, active.ID)
		}
	}

	prevRun, err := i.store.Get(previousRunID)
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, fmt.Errorf("previous run %s not found: %w", previousRunID, err)
	}

	continueBot := bot
	if phase == "fix" {
		continueBot = FixerBotFor(bot)
	}
	target := strings.TrimSpace(continueBot.Target)
	prompt := BuildCodexContinuePrompt(bug, continueBot, userInput, prevRun)
	if phase == "fix" {
		prompt = BuildCodexFixPrompt(bug, continueBot, prevRun, userInput)
	}
	continueCmd, parser, err := i.buildCommandLocked(target, continueBot, prompt)
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(ctx)

	run := InvestigationRun{
		ID:             randomRunID(),
		BugID:          bug.ID,
		BotKey:         bot.Key,
		Status:         InvestigationRunning,
		StartedAt:      time.Now().UTC(),
		PromptPreview:  promptPreview(prompt),
		ContinuationOf: strings.TrimSpace(previousRunID),
	}
	if err := i.store.Upsert(run); err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.active[run.ID] = active

	i.mu.Unlock()
	go i.collectContinueRun(ctx, run.ID, continueCmd, parser, active, phase)
	return run, nil
}

func (i *CodexInvestigator) StartFix(ctx context.Context, bug Bug, bot BotRef, previousRunID string) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
	}
	i.mu.Lock()
	if active, ok, err := i.store.ActiveRunForBug(bug.ID); err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	} else if ok {
		if _, inMemory := i.active[active.ID]; inMemory {
			i.mu.Unlock()
			return InvestigationRun{}, fmt.Errorf("bug %s has an active investigation (%s), cannot start fix", bug.ID, active.ID)
		}
	}

	prevRun, err := i.store.Get(strings.TrimSpace(previousRunID))
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, fmt.Errorf("previous run %s not found: %w", previousRunID, err)
	}
	fixBot := FixerBotFor(bot)
	target := strings.TrimSpace(fixBot.Target)
	prompt := BuildCodexFixPrompt(bug, fixBot, prevRun, "")
	cmd, parser, err := i.buildCommandLocked(target, fixBot, prompt)
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	run := InvestigationRun{
		ID:             randomRunID(),
		BugID:          bug.ID,
		BotKey:         bot.Key,
		Status:         InvestigationRunning,
		StartedAt:      time.Now().UTC(),
		PromptPreview:  promptPreview(prompt),
		ContinuationOf: strings.TrimSpace(previousRunID),
	}
	if err := i.store.Upsert(run); err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.active[run.ID] = active
	i.mu.Unlock()
	go i.collectContinueRun(ctx, run.ID, cmd, parser, active, "fix")
	return run, nil
}

func normalizeContinuationPhase(phase string) string {
	switch strings.TrimSpace(strings.ToLower(phase)) {
	case "fix":
		return "fix"
	default:
		return "investigation"
	}
}

func (i *CodexInvestigator) collectContinueRun(ctx context.Context, runID string, cmd *exec.Cmd, parser investigationEventParser, active *activeCodexRun, phase string) {
	defer close(active.done)
	defer i.removeActive(runID)
	stopKillWatcher := make(chan struct{})
	defer close(stopKillWatcher)
	go func() {
		select {
		case <-ctx.Done():
			active.kill()
		case <-stopKillWatcher:
		}
	}()

	stageMessage := "排障 Agent 继续执行（基于用户补充信息）"
	if phase == "fix" {
		stageMessage = "修复 Agent 开始修复（基于排障结论）"
	}
	i.emitStageEvent(runID, phase, stageMessage)
	finalMessage, finishStatus, runErr := i.runCommandStage(ctx, runID, cmd, parser, active, phase)
	if runErr != nil && finishStatus != InvestigationCancelled {
		active.setError(runErr)
	}
	finishError := ""
	if finishStatus != InvestigationSucceeded {
		finishError = runErrorText(ctx, runErr)
	}
	if phase == "fix" && isStructuredFixReport(finalMessage) {
		finalMessage = formatFixFinalReport(finalMessage)
	}
	if err := i.store.Finish(runID, finishStatus, finalMessage, finishError); err != nil {
		active.setError(err)
		return
	}
	i.emitEvent(runID, InvestigationEvent{
		At:      time.Now().UTC(),
		Type:    "status",
		Message: string(finishStatus),
	})
}

func (i *CodexInvestigator) buildCommandLocked(target string, bot BotRef, prompt string) (*exec.Cmd, investigationEventParser, error) {
	if i.binaries == nil {
		i.binaries = make(map[string]string)
	}
	switch target {
	case "codex":
		cmd, err := buildCodexBotExecCommand(firstNonEmpty(i.binaries["codex"], i.codexBin, "codex"), bot, prompt, nil)
		return cmd, newCodexStreamJSONParser(), err
	case "claude-code":
		workspace := claudeWorkspace(bot.Path)
		cmd, err := BuildClaudeInvestigationCommand(firstNonEmpty(i.binaries["claude-code"], "claude"), workspace, bot.Path, prompt)
		return cmd, ParseClaudeStreamJSONEvent, err
	case "opencode":
		cmd, err := BuildOpenCodeInvestigationCommand(i.binaries["opencode"], bot, prompt, nil)
		return cmd, newOpenCodeStreamJSONParser(), err
	case "cursor":
		cmd, err := BuildCursorInvestigationCommand(i.binaries["cursor"], bot.Path, prompt)
		return cmd, newCursorStreamJSONParser(), err
	default:
		return nil, nil, fmt.Errorf("暂不支持 %s 后台直启", firstNonEmpty(target, "unknown"))
	}
}

func (i *CodexInvestigator) Cancel(runID string) error {
	i.mu.Lock()
	active, ok := i.active[runID]
	i.mu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	active.cancel()
	active.kill()
	<-active.done
	i.removeActive(runID)
	return active.getError()
}

func (i *CodexInvestigator) Wait(runID string) (InvestigationRun, error) {
	i.mu.Lock()
	active, ok := i.active[runID]
	i.mu.Unlock()
	if ok {
		<-active.done
		i.removeActive(runID)
		if err := active.getError(); err != nil {
			return InvestigationRun{}, err
		}
	}
	return i.store.Get(runID)
}

func fieldFromLooseEnvLabel(text, key string) string {
	lower := strings.ToLower(text)
	key = strings.ToLower(key)
	idx := strings.Index(lower, key)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[idx+len(key):])
	rest = strings.TrimLeft(rest, " :=：")
	for _, sep := range []string{",", "，", "|", ";", "；"} {
		if cut := strings.Index(rest, sep); cut >= 0 {
			rest = rest[:cut]
		}
	}
	return strings.TrimSpace(rest)
}

func nonDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return ""
	}
	return value
}

func formatFixFinalReport(report string) string {
	report = strings.TrimSpace(strings.ReplaceAll(report, `\n`, "\n"))
	if report == "" {
		return ""
	}
	if strings.Contains(report, "修复报告 |") {
		return report
	}
	status := yamlScalar(report, "fix_status")
	env := firstNonEmpty(yamlScalar(report, "environment"), "-")
	deploymentNotice := firstNonEmpty(yamlScalar(report, "deployment_notice"), "-")
	blockedReason := yamlScalar(report, "blocked_reason")
	branches := yamlBlockSummary(report, "branches")
	changes := yamlBlockSummary(report, "changes")
	tests := yamlBlockSummary(report, "tests")
	risks := yamlBlockSummary(report, "risks")

	var sb strings.Builder
	fmt.Fprintf(&sb, "### 修复报告 | %s | %s\n\n", env, fixStatusLabel(status))
	fmt.Fprintf(&sb, "- 结论: %s\n", fixConclusion(status))
	if branches != "" {
		fmt.Fprintf(&sb, "- 分支/提交: %s\n", branches)
	}
	if changes != "" {
		fmt.Fprintf(&sb, "- 改动: %s\n", changes)
	}
	if tests != "" {
		fmt.Fprintf(&sb, "- 测试: %s\n", tests)
	}
	fmt.Fprintf(&sb, "- 部署提示: %s\n", deploymentNotice)
	if risks != "" {
		fmt.Fprintf(&sb, "- 风险: %s\n", risks)
	}
	if strings.TrimSpace(blockedReason) != "" {
		fmt.Fprintf(&sb, "- 阻塞原因: %s\n", blockedReason)
	}
	sb.WriteString("\n#### 原始结构化结果\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString(report)
	sb.WriteString("\n```\n")
	return sb.String()
}

func isStructuredFixReport(report string) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(report, `\n`, "\n")), "fix_status:")
}

func fixStatusLabel(status string) string {
	switch strings.TrimSpace(status) {
	case "fixed_pushed":
		return "已提交推送"
	case "blocked":
		return "已阻塞"
	case "failed":
		return "失败"
	default:
		return "结论不完整"
	}
}

func fixConclusion(status string) string {
	switch strings.TrimSpace(status) {
	case "fixed_pushed":
		return "修复分支已生成、提交并推送，等待独立合并授权与人工验收。"
	case "blocked":
		return "修复 Agent 遇到阻塞，用户补充信息后可继续修复。"
	case "failed":
		return "修复执行失败，需要查看失败原因后重试或人工介入。"
	default:
		return "修复 Agent 未输出完整结构化结论。"
	}
}

func yamlScalar(report, key string) string {
	report = strings.ReplaceAll(report, `\n`, "\n")
	prefix := strings.TrimSpace(key) + ":"
	for _, line := range strings.Split(report, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		return strings.Trim(value, "`\"'")
	}
	return ""
}

func yamlNestedScalar(report, parent, key string) string {
	block := yamlRawBlock(report, parent)
	if block == "" {
		return ""
	}
	return yamlScalar(block, key)
}

func yamlBlockSummary(report, key string) string {
	block := strings.TrimSpace(yamlRawBlock(report, key))
	if block == "" {
		value := strings.TrimSpace(yamlScalar(report, key))
		if value == "" || value == "[]" || strings.EqualFold(value, "null") {
			return value
		}
		return value
	}
	block = strings.ReplaceAll(block, "\n", " ")
	block = strings.Join(strings.Fields(block), " ")
	if block == "[]" || strings.EqualFold(block, "null") {
		return block
	}
	const maxLen = 420
	if utf8.RuneCountInString(block) > maxLen {
		runes := []rune(block)
		block = string(runes[:maxLen]) + "..."
	}
	return block
}

func yamlRawBlock(report, key string) string {
	report = strings.ReplaceAll(report, `\n`, "\n")
	lines := strings.Split(report, "\n")
	prefix := strings.TrimSpace(key) + ":"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		_, value, _ := strings.Cut(trimmed, ":")
		value = strings.TrimSpace(value)
		if value != "" {
			return strings.Trim(value, "`\"'")
		}
		var block []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if isTopLevelYAMLKey(next) {
				break
			}
			block = append(block, strings.TrimSpace(next))
		}
		return strings.Join(block, "\n")
	}
	return ""
}

func isTopLevelYAMLKey(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "-") {
		return false
	}
	key, _, ok := strings.Cut(line, ":")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	return key != ""
}

func (i *CodexInvestigator) buildCommand(target string, bot BotRef, prompt string) (*exec.Cmd, investigationEventParser, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.buildCommandLocked(target, bot, prompt)
}

// ExecutePhase reuses the established target-specific CLI adapters without
// mutating CaseStore. AgentPhaseRunner owns the compatibility projection and
// the orchestrator remains the only workflow state writer.
func (i *CodexInvestigator) ExecutePhase(parent context.Context, attemptID string, bot BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	if i == nil {
		return PhaseExecutionResult{}, errors.New("agent executor is required")
	}
	cmd, parser, err := i.buildCommand(strings.TrimSpace(bot.Target), bot, prompt)
	if err != nil {
		return PhaseExecutionResult{}, err
	}
	if err := applyPhaseStagingEnvironment(cmd, prompt); err != nil {
		return PhaseExecutionResult{}, err
	}
	return i.executePreparedPhase(parent, attemptID, cmd, parser, emit)
}

func applyPhaseStagingEnvironment(cmd *exec.Cmd, prompt string) error {
	staging := codexStagingPathFromPrompt(prompt)
	if staging == "" {
		return nil
	}
	if !filepath.IsAbs(staging) {
		return errors.New("Studio evidence staging path must be absolute")
	}
	info, err := os.Stat(staging)
	if err != nil || !info.IsDir() {
		return errors.New("Studio evidence staging directory is unavailable")
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = setProcessEnv(cmd.Env, "STUDIO_EVIDENCE_STAGING_DIR", staging)
	return nil
}

// ExecutePhaseWithAttachments transports trusted host evidence through the
// target-specific mechanism: Codex receives --image, Claude receives a
// its configured workspace so its Read tool can load the rendered PNG.
func (i *CodexInvestigator) ExecutePhaseWithAttachments(parent context.Context, attemptID string, bot BotRef, prompt string, attachments []PhaseAttachment, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	if i == nil {
		return PhaseExecutionResult{}, errors.New("agent executor is required")
	}
	validated, err := validatePhaseAttachments(attachments)
	if err != nil {
		return PhaseExecutionResult{}, err
	}
	target := strings.TrimSpace(bot.Target)
	paths := make([]string, 0, len(validated))
	for _, attachment := range validated {
		paths = append(paths, attachment.Path)
	}
	var cmd *exec.Cmd
	var parser investigationEventParser
	cleanup := func() error { return nil }
	i.mu.Lock()
	codexBin := firstNonEmpty(i.binaries["codex"], i.codexBin, "codex")
	claudeBin := firstNonEmpty(i.binaries["claude-code"], "claude")
	cursorBin := i.binaries["cursor"]
	openCodeBin := i.binaries["opencode"]
	i.mu.Unlock()
	switch target {
	case "opencode":
		cmd, err = BuildOpenCodeInvestigationCommand(openCodeBin, bot, prompt+phaseAttachmentPrompt(nil), paths)
		parser = newOpenCodeStreamJSONParser()
	case "cursor":
		cmd, err = BuildCursorInvestigationCommand(cursorBin, bot.Path, prompt+phaseAttachmentPrompt(paths))
		parser = newCursorStreamJSONParser()
	case "codex":
		cmd, err = buildCodexBotExecCommand(codexBin, bot, prompt+phaseAttachmentPrompt(nil), paths)
		parser = newCodexStreamJSONParser()
	case "claude-code":
		directories := make([]string, 0, len(paths))
		for _, path := range paths {
			directories = append(directories, filepath.Dir(path))
		}
		cmd, err = buildClaudeInvestigationCommand(claudeBin, claudeWorkspace(bot.Path), bot.Path, prompt+phaseAttachmentPrompt(paths), directories)
		parser = ParseClaudeStreamJSONEvent
	default:
		return PhaseExecutionResult{}, fmt.Errorf("暂不支持 %s 后台直启", firstNonEmpty(target, "unknown"))
	}
	if err != nil {
		_ = cleanup()
		return PhaseExecutionResult{}, err
	}
	if err := applyPhaseStagingEnvironment(cmd, prompt); err != nil {
		_ = cleanup()
		return PhaseExecutionResult{}, err
	}
	result, executeErr := i.executePreparedPhase(parent, attemptID, cmd, parser, emit)
	cleanupErr := cleanup()
	if cleanupErr != nil {
		return PhaseExecutionResult{}, cleanupErr
	}
	attachmentTokens := make([]PhaseAttachment, 0, len(paths))
	for _, path := range paths {
		attachmentTokens = append(attachmentTokens, PhaseAttachment{Path: path})
	}
	if phaseResultContainsAttachmentPath(result.FinalYAML, attachmentTokens) {
		return PhaseExecutionResult{}, errPhaseAttachmentPathEcho
	}
	return result, executeErr
}

func (i *CodexInvestigator) executePreparedPhase(parent context.Context, attemptID string, cmd *exec.Cmd, parser investigationEventParser, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	ctx, cancel := context.WithCancel(parent)
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.mu.Lock()
	if _, exists := i.active[attemptID]; exists {
		i.mu.Unlock()
		cancel()
		return PhaseExecutionResult{}, fmt.Errorf("phase attempt %s is already running", attemptID)
	}
	i.active[attemptID] = active
	i.mu.Unlock()
	defer func() {
		close(active.done)
		i.removeActive(attemptID)
		cancel()
	}()
	result, err := executePhaseCommand(ctx, cmd, parser, active, emit)
	if err != nil && !errors.Is(err, context.Canceled) {
		active.setError(err)
	}
	return result, err
}

func (i *CodexInvestigator) CancelPhase(ctx context.Context, attemptID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return i.Cancel(attemptID)
}

func executePhaseCommand(ctx context.Context, command *exec.Cmd, parser investigationEventParser, active *activeCodexRun, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	cmd.Dir = command.Dir
	cmd.Stdin = command.Stdin
	if command.Env != nil {
		cmd.Env = append([]string{}, command.Env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return PhaseExecutionResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return PhaseExecutionResult{}, err
	}
	setCodexProcessGroup(cmd)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return PhaseExecutionResult{}, ctx.Err()
		}
		return PhaseExecutionResult{}, err
	}
	active.setProcess(cmd.Process)
	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- strings.TrimSpace(string(data))
	}()
	var result PhaseExecutionResult
	var terminalFailure string
	var recoverableFailure string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, final, failed := parser([]byte(line))
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		if strings.TrimSpace(event.Message) != "" && emit != nil {
			emit(event)
		}
		mergeUsageFromRaw(&result.Usage, event.Raw)
		if strings.TrimSpace(final) != "" {
			result.FinalYAML = final
		}
		updateAgentCommandFailure(event, failed, strings.TrimSpace(result.FinalYAML) != "", &terminalFailure, &recoverableFailure)
	}
	if err := scanner.Err(); err != nil && terminalFailure == "" {
		terminalFailure = err.Error()
	}
	waitErr := cmd.Wait()
	active.setProcess(nil)
	stderrText := <-stderrDone

	result.Usage.Duration = time.Since(started)
	switch {
	case ctx.Err() != nil:
		return result, ctx.Err()
	case terminalFailure != "":
		return result, errors.New(terminalFailure)
	case recoverableFailure != "":
		return result, errors.New(recoverableFailure)
	case waitErr != nil:
		if stderrText == "" {
			stderrText = waitErr.Error()
		}
		return result, errors.New(stderrText)
	case strings.TrimSpace(result.FinalYAML) == "":
		return result, missingAgentResultError(stderrText)
	default:
		return result, nil
	}
}

// updateAgentCommandFailure keeps top-level stream errors provisional until the
// same command emits a terminal success with a final result. Codex emits these
// errors while reconnecting or falling back from WebSockets to HTTPS, so
// treating the first one as irrevocable discards a later valid turn.completed.
// Explicit terminal failures remain fatal even if malformed output follows.
func updateAgentCommandFailure(event InvestigationEvent, failed string, hasFinal bool, terminalFailure, recoverableFailure *string) {
	failed = strings.TrimSpace(failed)
	if failed != "" {
		switch event.Type {
		case "turn_failed", "result":
			*terminalFailure = failed
		default:
			*recoverableFailure = failed
		}
	}
	if *terminalFailure == "" && hasFinal && (event.Type == "turn_completed" || event.Type == "result") {
		*recoverableFailure = ""
	}
}

func mergeUsageFromRaw(usage *AgentUsage, raw any) {
	root, ok := raw.(map[string]any)
	if !ok || usage == nil {
		return
	}
	for _, candidate := range []any{root["usage"], root["token_usage"], nestedAny(root, "result", "usage")} {
		values, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		usage.InputTokens = maxInt64(usage.InputTokens, int64FromAny(firstAny(values["input_tokens"], values["inputTokens"], values["prompt_tokens"])))
		usage.OutputTokens = maxInt64(usage.OutputTokens, int64FromAny(firstAny(values["output_tokens"], values["outputTokens"], values["completion_tokens"])))
	}
}

func nestedAny(root map[string]any, keys ...string) any {
	var value any = root
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		result, _ := typed.Int64()
		return result
	default:
		return 0
	}
}

func maxInt64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

func (i *CodexInvestigator) emitStageEvent(runID string, phase string, message string) {
	event := InvestigationEvent{
		At:      time.Now().UTC(),
		Type:    "stage",
		Message: message,
		Meta:    map[string]any{"phase": phase},
	}
	if err := i.store.AppendEvent(runID, event); err != nil {
		return
	}
	i.emitEvent(runID, event)
}

func (i *CodexInvestigator) runCommandStage(ctx context.Context, runID string, cmd *exec.Cmd, parser investigationEventParser, active *activeCodexRun, phase string) (string, InvestigationStatus, error) {
	cmdDir := cmd.Dir
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = cmdDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", InvestigationFailed, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", InvestigationFailed, err
	}
	setCodexProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return "", InvestigationCancelled, ctx.Err()
		}
		return "", InvestigationFailed, err
	}
	active.setProcess(cmd.Process)

	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- strings.TrimSpace(string(data))
	}()

	var finalMessage string
	var terminalFailure string
	var recoverableFailure string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, final, failed := parser([]byte(line))
		if strings.TrimSpace(event.Message) != "" {
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			normalizePhaseEvent(&event, phase)
			if strings.TrimSpace(phase) != "" {
				if event.Meta == nil {
					event.Meta = make(map[string]any)
				}
				event.Meta["phase"] = phase
			}
			err := i.store.AppendEvent(runID, event)
			active.setError(err)
			if err == nil {
				i.emitEvent(runID, event)
			}
		}
		if strings.TrimSpace(final) != "" {
			finalMessage = final
		}
		updateAgentCommandFailure(event, failed, strings.TrimSpace(finalMessage) != "", &terminalFailure, &recoverableFailure)
	}
	if err := scanner.Err(); err != nil && terminalFailure == "" {
		terminalFailure = err.Error()
	}

	waitErr := cmd.Wait()
	active.setProcess(nil)
	stderrText := <-stderrDone

	switch {
	case ctx.Err() != nil:
		return finalMessage, InvestigationCancelled, ctx.Err()
	case strings.TrimSpace(terminalFailure) != "":
		return finalMessage, InvestigationFailed, errors.New(terminalFailure)
	case strings.TrimSpace(recoverableFailure) != "":
		return finalMessage, InvestigationFailed, errors.New(recoverableFailure)
	case waitErr != nil:
		errorText := strings.TrimSpace(stderrText)
		if errorText == "" {
			errorText = waitErr.Error()
		}
		return finalMessage, InvestigationFailed, errors.New(errorText)
	case strings.TrimSpace(finalMessage) == "":
		return finalMessage, InvestigationFailed, missingAgentResultError(stderrText)
	default:
		return finalMessage, InvestigationSucceeded, nil
	}
}

func normalizePhaseEvent(event *InvestigationEvent, phase string) {
	if event == nil {
		return
	}
	labels := map[string][2]string{
		"validation":    {"开始验证", "验证完成"},
		"investigation": {"开始排障", "排障完成"},
		"fix":           {"开始修复", "修复完成"},
		"regression":    {"开始回归", "回归完成"},
	}
	label, ok := labels[phase]
	if !ok {
		return
	}
	switch event.Type {
	case "turn_started":
		event.Message = label[0]
	case "turn_completed":
		event.Message = label[1]
	}
}

func runErrorText(ctx context.Context, err error) string {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err().Error()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func (i *CodexInvestigator) emitEvent(runID string, event InvestigationEvent) {
	i.mu.Lock()
	sink := i.eventSink
	i.mu.Unlock()
	if sink == nil {
		return
	}
	run, err := i.store.Get(runID)
	if err != nil {
		return
	}
	sink(run, event)
}

func (a *activeCodexRun) setError(err error) {
	if err == nil {
		return
	}
	a.errMu.Lock()
	defer a.errMu.Unlock()
	if a.err == nil {
		a.err = err
	}
}

func (a *activeCodexRun) getError() error {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	return a.err
}

func (a *activeCodexRun) setProcess(process *os.Process) {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	a.process = process
}

func (a *activeCodexRun) kill() {
	a.errMu.Lock()
	process := a.process
	a.errMu.Unlock()
	if process == nil {
		return
	}
	killCodexProcessGroup(process.Pid)
	_ = process.Kill()
}

func (i *CodexInvestigator) removeActive(runID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.active, runID)
}

func randomRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b[:])
}

func promptPreview(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if utf8.RuneCountInString(prompt) <= 240 {
		return prompt
	}
	runes := []rune(prompt)
	return string(runes[:240])
}

func claudeWorkspace(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

func claudeAgentName(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func missingAgentResultError(stderr string) error {
	message := "agent returned no final structured result"
	if stderr = strings.TrimSpace(stderr); stderr != "" && !containsSensitiveData([]byte(stderr)) && !resetURLUserinfoPattern.MatchString(stderr) {
		if len(stderr) > 2000 {
			stderr = stderr[:2000]
		}
		message += ": " + stderr
	}
	return errors.New(message)
}
