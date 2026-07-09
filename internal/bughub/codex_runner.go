package bughub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

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
			"openclaw":    "openclaw",
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
	return BuildCodexInvestigationPromptWithValidation(b, bot, "")
}

func BuildCodexValidationPrompt(b Bug, bot BotRef) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 验证 Agent。\n")
	sb.WriteString("目标：先做取证验证，不做根因判断，不给修复方案；修复后也可复用同一流程做回归复查。\n")
	sb.WriteString("请读取 Bug 工单的复现步骤、附件、环境、前端 URL/API 线索；能实际打开页面或请求接口时优先执行，只读取证。\n")
	sb.WriteString("边界：只复现场景和收集证据；不要读取业务源码定位函数/行号，不要输出\"代码根因/最可能原因/修复建议/候选原因\"。如需代码分析，交给后续排障 Agent。\n")
	sb.WriteString("如果缺少账号、入口、测试数据或可重放请求，请明确标记 insufficient_info；如果用于修复后复查，请明确 fixed_verified 或 still_reproduces。\n")
	sb.WriteString(validationAgentExecutionGuidance())
	sb.WriteString("\n")
	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString(validationOutputContract())
	return sb.String()
}

func BuildCodexInvestigationPromptWithValidation(b Bug, bot BotRef, validationReport string) string {
	var sb strings.Builder
	sb.WriteString("请作为选定的 AI 排障机器人开始排障。\n")
	sb.WriteString("目标：基于下面 Bug 工单上下文和验证 Agent 已确认的复现证据，完成只读根因分析，输出可执行结论。\n\n")
	sb.WriteString("## 强制流程（不可跳过）\n\n")
	sb.WriteString("1. **第一步**：Read `incident-investigator/SKILL.md`，严格按 7 步排障图谱执行。\n")
	sb.WriteString("2. 从步骤 2 timeline 开始：查最近变更（git log / K8s rollout / 配置 history）。\n")
	sb.WriteString("3. 步骤 5 多向交叉：根据问题类型选维度，**至少覆盖 3 个维度**（日志 + 代码 + 数据 中至少 2 个），不要只靠验证 Agent 的截图。\n")
	sb.WriteString("4. 用 trace_id / request_id 查 Jaeger 链路和 Loki/ELK 日志，不要跳过。\n")
	sb.WriteString("5. 步骤 6 输出故障快报，步骤 7 沉淀。\n\n")
	sb.WriteString("## 关键约束\n\n")
	sb.WriteString("- **验证报告仅供复现参考**，不能替代你自己的排障证据链。你必须独立查询日志/指标/链路/代码/配置来确认根因。\n")
	sb.WriteString("- 禁止只引用验证报告就下结论。没有自己查到的 trace span、日志行、代码片段、配置 diff 之前，不要写\"最可能根因\"。\n")
	sb.WriteString("- 默认只读，不要修改代码或执行破坏性命令。\n\n")
	sb.WriteString(GenerateContext(b, bot))
	if strings.TrimSpace(validationReport) != "" {
		sb.WriteString("\n## 验证 Agent 报告（复现参考，不是排障结论）\n")
		sb.WriteString(strings.TrimSpace(validationReport))
		sb.WriteString("\n")
	}
	sb.WriteString("\n## 输出结构\n\n")
	sb.WriteString("1. 排障过程：列出你实际执行的查询（trace/log/metric/code/config），附上关键证据。每一项前面标记 [已查] 或 [未查+原因]。\n")
	sb.WriteString("2. 最可能根因：基于你自己查到的证据得出的结论，不要复述验证报告。\n")
	sb.WriteString("3. 建议处置和验证方法\n")
	sb.WriteString("4. 需要用户补充的信息\n")
	return sb.String()
}

func BuildCodexContinuePrompt(b Bug, bot BotRef, userInput string, prevRun InvestigationRun) string {
	var sb strings.Builder
	sb.WriteString("## 用户补充信息（请优先根据此信息调整排障方向）\n\n")
	sb.WriteString(strings.TrimSpace(userInput))
	sb.WriteString("\n\n")

	sb.WriteString("以上是用户针对前一轮排障中缺失的信息提供的补充说明。请基于这些补充信息重新排障，重点关注新的线索。\n\n")

	// Get previous validation events
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
		default:
			investigationParts = append(investigationParts, msg)
		}
	}

	// Validation report (from previous run)
	if len(validationParts) > 0 {
		sb.WriteString("## 前一轮验证报告（复现参考）\n\n")
		for _, p := range validationParts {
			sb.WriteString(p)
			sb.WriteString("\n\n")
		}
	}

	// Previous investigation output
	if len(investigationParts) > 0 || strings.TrimSpace(prevRun.FinalMessage) != "" {
		sb.WriteString("## 前一轮排障输出\n\n")
		for _, p := range investigationParts {
			sb.WriteString(p)
			sb.WriteString("\n\n")
		}
		if strings.TrimSpace(prevRun.FinalMessage) != "" {
			sb.WriteString(strings.TrimSpace(prevRun.FinalMessage))
			sb.WriteString("\n\n")
		}
	}

	// Bug context
	sb.WriteString(GenerateContext(b, bot))

	sb.WriteString("\n## 输出结构\n\n")
	sb.WriteString("1. 排障过程：列出你实际执行的查询（trace/log/metric/code/config），附上关键证据。每一项前面标记 [已查] 或 [未查+原因]。\n")
	sb.WriteString("2. 最可能根因：基于你自己查到的证据得出的结论。\n")
	sb.WriteString("3. 建议处置和验证方法\n")
	sb.WriteString("4. 需要用户补充的信息\n")
	return sb.String()
}

func BuildCodexValidationContinuePrompt(b Bug, bot BotRef, userInput string, prevRun InvestigationRun) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 验证 Agent。\n")
	sb.WriteString("目标：基于用户补充信息继续取证验证，不做根因判断，不给修复方案。\n\n")
	sb.WriteString(validationAgentExecutionGuidance())
	sb.WriteString("\n")
	sb.WriteString("## 用户补充信息\n\n")
	sb.WriteString(strings.TrimSpace(userInput))
	sb.WriteString("\n\n")

	var previousValidation []string
	for _, e := range prevRun.Events {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		phase, _ := e.Meta["phase"].(string)
		if phase == "validation" {
			previousValidation = append(previousValidation, msg)
		}
	}
	if len(previousValidation) > 0 {
		sb.WriteString("## 前一轮验证证据\n\n")
		for _, p := range previousValidation {
			sb.WriteString(p)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString(validationOutputContract())
	return sb.String()
}

func BuildCodexFixPrompt(b Bug, bot BotRef, prevRun InvestigationRun, userInput string) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 修复 Agent。\n")
	sb.WriteString("目标：基于 Bug 工单、验证证据和排障结论落地修复。只有用户明确触发修复后才执行。\n\n")
	sb.WriteString("第一步：如果当前机器人 workspace 中存在 `bug-fixer/SKILL.md`，必须先 Read 它，并按其中流程执行。\n")
	sb.WriteString("如果旧安装还没有该 skill，使用下面内置流程作为兼容兜底。\n\n")
	sb.WriteString("## 强制流程\n\n")
	sb.WriteString("1. 先确认当前代码仓库已经切到目标环境对应分支；不要从错误分支修复。\n")
	sb.WriteString("2. 基于当前环境分支创建独立修复分支，分支名使用 `fix/bug-<source>-<id>-<short>` 风格。\n")
	sb.WriteString("3. 修复 Bug，只做最小必要改动，不做无关重构，不改无关文件。\n")
	sb.WriteString("4. 运行相关测试、构建或最小验证命令；无法运行时说明具体原因。\n")
	sb.WriteString("5. `git status` 确认只包含本次修复文件后提交，并推送修复分支。\n")
	sb.WriteString("6. 最终输出分支名、commit、push 结果、测试结果，并明确通知用户部署该修复分支。\n\n")
	sb.WriteString("## 停止条件\n\n")
	sb.WriteString("- 如果工作区已有用户未提交改动或无法确认这些改动属于本次修复，停止并说明，不要覆盖。\n")
	sb.WriteString("- 如果无法确认环境分支、无法创建分支、无法提交或无法推送，停止并说明阻塞点。\n")
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
	sb.WriteString("\n## 输出结构\n\n")
	sb.WriteString("1. 修复分支\n")
	sb.WriteString("2. 修改摘要\n")
	sb.WriteString("3. 测试/验证结果\n")
	sb.WriteString("4. 提交与推送结果\n")
	sb.WriteString("5. 部署提示\n")
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
		sb.WriteString("## 验证 Agent 证据\n\n")
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

func validationAgentExecutionGuidance() string {
	var sb strings.Builder
	sb.WriteString("\n## 执行环境说明\n")
	sb.WriteString("- 当前验证 Agent 由桌面应用后台启动，不保证拥有 in-app browser / iab / 可视化浏览器控制能力。\n")
	sb.WriteString("- 如果浏览器工具返回 unavailable，不要把它本身写入 gaps，也不要要求用户提供浏览器能力；应改用工单附件、本地附件路径、HAR/Network 导出、curl/API 请求、trace/request id、日志或已有截图取证。\n")
	sb.WriteString("- 只有缺少业务验证必需资料时才写入 gaps，例如后台登录态/测试账号、受影响 URL/route/API、测试数据、HAR/Network 导出、request id 或 trace id。\n")
	sb.WriteString("- 工具能力限制但不阻塞已有证据判断时，写入 handoff_to_troubleshooter.unchecked_scopes，不要写入 gaps。\n")
	return sb.String()
}

func validationOutputContract() string {
	var sb strings.Builder
	sb.WriteString("\n请只输出结构化验证报告，格式如下：\n")
	sb.WriteString("verification_status: reproduced | not_reproduced | insufficient_info | fixed_verified | still_reproduces\n")
	sb.WriteString("environment: <bug env / bot env>\n")
	sb.WriteString("entry:\n  frontend_url: <实际入口或 ->\n  api_url: <实际接口或 ->\n")
	sb.WriteString("observed_behavior: <实际看到的现象>\n")
	sb.WriteString("expected_behavior: <工单期望>\n")
	sb.WriteString("evidence:\n  attachments: []\n  screenshots: []\n  network: []\n  console_errors: []\n  trace_ids: []\n  request_ids: []\n")
	sb.WriteString("handoff_to_troubleshooter:\n  evidence_summary: <只写事实>\n  unchecked_scopes: []\n")
	sb.WriteString("gaps: []\n")
	sb.WriteString("\n只有当用户需要补充的阻塞资料已经清空时，gaps 才能输出 []。\n")
	return sb.String()
}

func BuildCodexExecCommand(codexBin, workspace, prompt string) (*exec.Cmd, error) {
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
	cmd := exec.Command(codexBin, "exec", "--json", "--sandbox", "workspace-write", "--cd", workspace, "--skip-git-repo-check", prompt)
	cmd.Dir = workspace
	return cmd, nil
}

func BuildClaudeInvestigationCommand(claudeBin, workspace, agentPath, prompt string) (*exec.Cmd, error) {
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
	cmd := exec.Command(claudeBin, "-p", "--dangerously-skip-permissions", "--permission-mode", "bypassPermissions", "--output-format", "stream-json", "--verbose", "--agent", agentName, prompt)
	cmd.Dir = workspace
	return cmd, nil
}

func BuildOpenClawInvestigationCommand(openclawBin, agentID, prompt string) (*exec.Cmd, error) {
	openclawBin = strings.TrimSpace(openclawBin)
	if openclawBin == "" {
		openclawBin = "openclaw"
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, errors.New("openclaw agent is required")
	}
	return exec.Command(openclawBin, "agent", "--agent", agentID, "--message", prompt, "--json"), nil
}

func (i *CodexInvestigator) Start(parent context.Context, bug Bug, bot BotRef) (InvestigationRun, error) {
	return i.StartWithValidator(parent, bug, bot, ValidatorBotFor(bot))
}

func (i *CodexInvestigator) StartWithValidator(parent context.Context, bug Bug, bot BotRef, validator BotRef) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
	}
	target := strings.TrimSpace(bot.Target)
	validationBot := validator
	if strings.TrimSpace(validationBot.Key) == "" {
		validationBot = bot
	}
	validationTarget := strings.TrimSpace(validationBot.Target)
	if validationTarget == "" {
		validationTarget = target
	}

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

	validationPrompt := BuildCodexValidationPrompt(bug, validationBot)
	validationCmd, validationParser, err := i.buildCommandLocked(validationTarget, validationBot, validationPrompt)
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
		PromptPreview: promptPreview(validationPrompt),
	}
	if err := i.store.Upsert(run); err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.active[run.ID] = active

	i.mu.Unlock()
	go i.collectRun(ctx, run.ID, bug, bot, target, validationCmd, validationParser, active)
	return run, nil
}

func (i *CodexInvestigator) Continue(ctx context.Context, bug Bug, bot BotRef, userInput string, previousRunID string, phase string) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
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
	if phase == "validation" {
		continueBot = ValidatorBotFor(bot)
	} else if phase == "fix" {
		continueBot = FixerBotFor(bot)
	}
	target := strings.TrimSpace(continueBot.Target)
	prompt := BuildCodexContinuePrompt(bug, continueBot, userInput, prevRun)
	if phase == "validation" {
		prompt = BuildCodexValidationContinuePrompt(bug, continueBot, userInput, prevRun)
	} else if phase == "fix" {
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
	if phase == "validation" {
		go i.collectValidationContinueRun(ctx, run.ID, bug, bot, continueCmd, parser, active)
	} else {
		go i.collectContinueRun(ctx, run.ID, continueCmd, parser, active, phase)
	}
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
	case "validation":
		return "validation"
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
	if phase == "validation" {
		stageMessage = "验证 Agent 继续取证（基于用户补充信息）"
	} else if phase == "fix" {
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

func (i *CodexInvestigator) collectValidationContinueRun(ctx context.Context, runID string, bug Bug, bot BotRef, validationCmd *exec.Cmd, validationParser investigationEventParser, active *activeCodexRun) {
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

	i.emitStageEvent(runID, "validation", "验证 Agent 继续取证（基于用户补充信息）")
	validationReport, validationStatus, validationError := i.runCommandStage(ctx, runID, validationCmd, validationParser, active, "validation")
	if validationError != nil && validationStatus != InvestigationCancelled {
		active.setError(validationError)
	}
	if validationStatus != InvestigationSucceeded {
		_ = i.store.Finish(runID, validationStatus, "", runErrorText(ctx, validationError))
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(validationStatus),
		})
		return
	}
	if !validationReportReadyForInvestigation(validationReport) {
		i.emitStageEvent(runID, "validation", "验证 Agent 尚未给出可进入排障的完整验证结论，已暂停进入排障 Agent")
		if err := i.store.Finish(runID, InvestigationSucceeded, "", ""); err != nil {
			active.setError(err)
			return
		}
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(InvestigationSucceeded),
		})
		return
	}
	i.emitStageEvent(runID, "validation", "验证 Agent 完成，已将证据交给排障 Agent")

	prompt := BuildCodexInvestigationPromptWithValidation(bug, bot, validationReport)
	investigationCmd, parser, err := i.buildCommand(strings.TrimSpace(bot.Target), bot, prompt)
	if err != nil {
		active.setError(err)
		_ = i.store.Finish(runID, InvestigationFailed, validationReport, err.Error())
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(InvestigationFailed),
		})
		return
	}
	finalMessage, finishStatus, runErr := i.runCommandStage(ctx, runID, investigationCmd, parser, active, "investigation")
	if runErr != nil && finishStatus != InvestigationCancelled {
		active.setError(runErr)
	}
	finishError := ""
	if finishStatus != InvestigationSucceeded {
		finishError = runErrorText(ctx, runErr)
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
		cmd, err := BuildCodexExecCommand(firstNonEmpty(i.binaries["codex"], i.codexBin, "codex"), bot.Path, prompt)
		return cmd, ParseCodexJSONLEvent, err
	case "claude-code":
		workspace := claudeWorkspace(bot.Path)
		cmd, err := BuildClaudeInvestigationCommand(firstNonEmpty(i.binaries["claude-code"], "claude"), workspace, bot.Path, prompt)
		return cmd, ParseClaudeStreamJSONEvent, err
	case "openclaw":
		cmd, err := BuildOpenClawInvestigationCommand(firstNonEmpty(i.binaries["openclaw"], "openclaw"), openClawAgentID(bot), prompt)
		return cmd, ParseOpenClawJSONEvent, err
	case "cursor":
		return nil, nil, errors.New("暂不支持 Cursor 后台直启，请复制上下文后在 Cursor Custom Agent 中发起")
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

func (i *CodexInvestigator) collectRun(ctx context.Context, runID string, bug Bug, bot BotRef, target string, validationCmd *exec.Cmd, validationParser investigationEventParser, active *activeCodexRun) {
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

	i.emitStageEvent(runID, "validation", "验证 Agent 开始取证验证")
	validationReport, validationStatus, validationError := i.runCommandStage(ctx, runID, validationCmd, validationParser, active, "validation")
	if validationError != nil && validationStatus != InvestigationCancelled {
		active.setError(validationError)
	}
	if validationStatus != InvestigationSucceeded {
		_ = i.store.Finish(runID, validationStatus, "", runErrorText(ctx, validationError))
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(validationStatus),
		})
		return
	}
	if !validationReportReadyForInvestigation(validationReport) {
		i.emitStageEvent(runID, "validation", "验证 Agent 尚未给出可进入排障的完整验证结论，已暂停进入排障 Agent")
		if err := i.store.Finish(runID, InvestigationSucceeded, "", ""); err != nil {
			active.setError(err)
			return
		}
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(InvestigationSucceeded),
		})
		return
	}
	i.emitStageEvent(runID, "validation", "验证 Agent 完成，已将证据交给排障 Agent")

	prompt := BuildCodexInvestigationPromptWithValidation(bug, bot, validationReport)
	investigationCmd, parser, err := i.buildCommand(target, bot, prompt)
	if err != nil {
		active.setError(err)
		_ = i.store.Finish(runID, InvestigationFailed, validationReport, err.Error())
		i.emitEvent(runID, InvestigationEvent{
			At:      time.Now().UTC(),
			Type:    "status",
			Message: string(InvestigationFailed),
		})
		return
	}
	finalMessage, finishStatus, runErr := i.runCommandStage(ctx, runID, investigationCmd, parser, active, "investigation")
	if runErr != nil && finishStatus != InvestigationCancelled {
		active.setError(runErr)
	}
	finishError := ""
	if finishStatus != InvestigationSucceeded {
		finishError = runErrorText(ctx, runErr)
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

func validationReportReadyForInvestigation(report string) bool {
	text := strings.TrimSpace(report)
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "insufficient_info") {
		return false
	}
	gapsPresent, gapsEmpty := validationGapsState(text)
	if !gapsPresent || !gapsEmpty {
		return false
	}
	// Flow control is allowlist-based: validation must emit a structured terminal
	// status and explicitly empty blocking gaps before investigation can start.
	status := validationStatus(text)
	switch status {
	case "reproduced", "not_reproduced", "fixed_verified", "still_reproduces":
		return true
	default:
		return false
	}
}

func validationGapsState(report string) (bool, bool) {
	report = strings.ReplaceAll(report, `\n`, "\n")
	lines := strings.Split(report, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "gaps") {
			continue
		}
		_, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return true, false
		}
		value = strings.TrimSpace(value)
		if value == "[]" || strings.EqualFold(value, "null") {
			return true, true
		}
		if value != "" {
			return true, false
		}
		for _, next := range lines[i+1:] {
			nextTrimmed := strings.TrimSpace(next)
			if nextTrimmed == "" {
				continue
			}
			if isTopLevelYAMLKey(next) {
				break
			}
			if nextTrimmed != "[]" {
				return true, false
			}
		}
		return true, true
	}
	return false, false
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

func validationStatus(report string) string {
	report = strings.ReplaceAll(report, `\n`, "\n")
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if !strings.HasPrefix(line, "verification_status") {
			continue
		}
		_, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if idx := strings.IndexAny(value, " \t,;|"); idx >= 0 {
			value = value[:idx]
		}
		return strings.Trim(value, "`\"'")
	}
	return ""
}

func (i *CodexInvestigator) buildCommand(target string, bot BotRef, prompt string) (*exec.Cmd, investigationEventParser, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.buildCommandLocked(target, bot, prompt)
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
	var failure string
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
		if strings.TrimSpace(failed) != "" {
			failure = failed
		}
	}
	if err := scanner.Err(); err != nil && failure == "" {
		failure = err.Error()
	}

	waitErr := cmd.Wait()
	active.setProcess(nil)
	stderrText := <-stderrDone
	switch {
	case ctx.Err() != nil:
		return finalMessage, InvestigationCancelled, ctx.Err()
	case strings.TrimSpace(failure) != "":
		return finalMessage, InvestigationFailed, errors.New(failure)
	case waitErr != nil:
		errorText := strings.TrimSpace(stderrText)
		if errorText == "" {
			errorText = waitErr.Error()
		}
		return finalMessage, InvestigationFailed, errors.New(errorText)
	default:
		return finalMessage, InvestigationSucceeded, nil
	}
}

func normalizePhaseEvent(event *InvestigationEvent, phase string) {
	if event == nil || phase != "validation" {
		return
	}
	switch event.Type {
	case "turn_started":
		event.Message = "开始验证"
	case "turn_completed":
		event.Message = "验证完成"
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

func openClawAgentID(bot BotRef) string {
	if strings.TrimSpace(bot.AgentID) != "" {
		return strings.TrimSpace(bot.AgentID)
	}
	pathBase := strings.TrimSuffix(filepath.Base(strings.TrimSpace(bot.Path)), filepath.Ext(strings.TrimSpace(bot.Path)))
	return firstNonEmpty(pathBase, bot.SystemID, bot.Name)
}
