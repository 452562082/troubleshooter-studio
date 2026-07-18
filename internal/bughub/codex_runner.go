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

func buildCodexDurableValidationContinuePrompt(b Bug, bot BotRef, userInputs []string, structuredInput, previousResult string) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 验证 Agent。\n")
	sb.WriteString("目标：基于用户持续补充的信息继续实际取证验证，不做根因判断，不给修复方案。\n")
	sb.WriteString("最新一条用户补充是本轮优先执行指令；如果用户明确要求通过 Web 页面或接口复现，必须实际尝试该路径，不能只复述历史附件。\n")
	sb.WriteString("请重新核对上一轮 gaps：已经被本轮或历史补充满足的项目不得继续原样索要；只有实际执行仍被阻塞时才能保留。\n")
	sb.WriteString("边界：只复现场景和收集证据；不要读取业务源码定位函数/行号，不要输出\"代码根因/最可能原因/修复建议/候选原因\"。如需代码分析，交给后续排障 Agent。\n")
	sb.WriteString(validationAgentExecutionGuidance())
	sb.WriteString("\n")
	if len(userInputs) > 0 {
		sb.WriteString("## 用户补充信息（按提交顺序，最后一条优先）\n\n")
		for index, input := range userInputs {
			sb.WriteString(fmt.Sprintf("%d. %s\n", index+1, strings.TrimSpace(input)))
		}
		sb.WriteString("\n")
	}
	if strings.TrimSpace(structuredInput) != "" {
		sb.WriteString("## 本轮结构化验证信息\n\n```json\n")
		sb.WriteString(strings.TrimSpace(structuredInput))
		sb.WriteString("\n```\n\n")
	}
	if strings.TrimSpace(previousResult) != "" {
		sb.WriteString("## 上一轮验证结果（仅作为待复核上下文）\n\n```json\n")
		sb.WriteString(strings.TrimSpace(previousResult))
		sb.WriteString("\n```\n\n")
	}
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
	sb.WriteString(investigationOutputContract())
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

	sb.WriteString(investigationOutputContract())
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
	return validationOutputContractFor("reproduced | not_reproduced | insufficient_info | fixed_verified | still_reproduces")
}

func validationOutputContractFor(statuses string) string {
	var sb strings.Builder
	sb.WriteString("\n请只输出下面的严格 YAML，不得增加字段或解释性段落：\n")
	sb.WriteString("verification_status: ")
	sb.WriteString(statuses)
	sb.WriteByte('\n')
	sb.WriteString("environment: \"<有效目标环境>\"\n")
	sb.WriteString("observed_behavior: \"<what-was-observed-during-verification>\"\n")
	sb.WriteString("expected_behavior: \"<expected>\"\n")
	sb.WriteString("scenario_hash: \"<原始场景哈希；首次验证可为空，回归必须原样返回>\"\n")
	sb.WriteString("evidence:\n  - kind: \"<har|screenshot|network|console|api|trace|log|command>\"\n    path: \"<Studio staging 目录内的相对路径>\"\n    captured_at: \"<RFC3339；仅兼容输出，Studio 以 fstat 为准>\"\n    environment: \"<env>\"\n    version: \"<运行版本；回归必填>\"\n    request_id: \"<可空>\"\n    trace_id: \"<可空>\"\n    redaction_status: redacted | not_required # 仅兼容输出，Studio 总会重新扫描\n")
	sb.WriteString("gaps: []\n")
	sb.WriteString("只有当阻塞资料已经清空时 gaps 才能输出 []。证据必须通过 path 引用常规文件；不得内联密钥、cookie 或 Authorization。\n")
	sb.WriteString("最终回答不得输出该结构之外的解释性段落。\n")
	return sb.String()
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
	sb.WriteString("    base_branch: \"<env-branch>\"\n")
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
	sb.WriteString("deployment_notice: \"请部署 <repo>/<fix_branch> 到 <env> 后再触发验证 Agent 回归。\"\n")
	sb.WriteString("risks:\n")
	sb.WriteString("  - \"<remaining-risk-or-empty>\"\n")
	sb.WriteString("blocked_reason: \"<only when blocked/failed>\"\n")
	sb.WriteString("evidence: []\n")
	sb.WriteString("```\n")
	sb.WriteString("每个仓库的 base_branch 必须与 target_environment_branch 完全一致；fix_branch 必须是不同的专用修复分支，禁止直接在环境分支修复或推送。\n")
	return sb.String()
}

func BuildCodexExecCommand(codexBin, workspace, prompt string) (*exec.Cmd, error) {
	return buildCodexExecCommand(codexBin, workspace, prompt, nil)
}

func buildCodexExecCommand(codexBin, workspace, prompt string, imagePaths []string) (*exec.Cmd, error) {
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
	args := []string{
		"exec", "--json",
		"--enable", "respect_system_proxy",
		"-c", "suppress_unstable_features_warning=true",
		"--sandbox", "workspace-write",
		"--cd", workspace,
		"--skip-git-repo-check",
	}
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
	return cmd, nil
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
	args := []string{"-p", "--dangerously-skip-permissions", "--permission-mode", "bypassPermissions", "--output-format", "stream-json", "--verbose", "--agent", agentName}
	for _, directory := range attachmentDirs {
		args = append(args, "--add-dir", directory)
	}
	args = append(args, prompt)
	cmd := exec.Command(claudeBin, args...)
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
		i.emitStageEvent(runID, "validation", validationPauseMessage(validationReport))
		if err := i.store.Finish(runID, InvestigationSucceeded, formatValidationFinalReport(validationReport, bug, bot), ""); err != nil {
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
		i.emitStageEvent(runID, "validation", validationPauseMessage(validationReport))
		if err := i.store.Finish(runID, InvestigationSucceeded, formatValidationFinalReport(validationReport, bug, bot), ""); err != nil {
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
	case "reproduced", "still_reproduces":
		return true
	default:
		return false
	}
}

func validationPauseMessage(report string) string {
	switch validationStatus(report) {
	case "not_reproduced":
		return "验证 Agent 未复现原始 Bug，已暂停进入排障 Agent"
	case "fixed_verified":
		return "验证 Agent 确认修复已通过，已暂停进入排障 Agent"
	case "insufficient_info":
		return "验证 Agent 信息不足，已暂停进入排障 Agent"
	default:
		return "验证 Agent 尚未给出可进入排障的复现结论，已暂停进入排障 Agent"
	}
}

func formatValidationFinalReport(report string, bug Bug, bot BotRef) string {
	report = strings.TrimSpace(strings.ReplaceAll(report, `\n`, "\n"))
	if report == "" {
		return ""
	}
	if strings.Contains(report, "验证报告 |") {
		return report
	}
	status := validationStatus(report)
	statusLabel := validationStatusLabel(status)
	env := normalizeValidationReportEnv(yamlScalar(report, "environment"), bug, bot)
	frontendURL := firstNonEmpty(yamlNestedScalar(report, "entry", "frontend_url"), "-")
	apiURL := firstNonEmpty(yamlNestedScalar(report, "entry", "api_url"), "-")
	observed := firstNonEmpty(yamlScalar(report, "observed_behavior"), "-")
	expected := firstNonEmpty(yamlScalar(report, "expected_behavior"), "-")
	evidenceSummary := firstNonEmpty(yamlNestedScalar(report, "handoff_to_troubleshooter", "evidence_summary"), "-")
	gaps := yamlBlockSummary(report, "gaps")
	if gaps == "" {
		gaps = "[]"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### 验证报告 | %s | %s\n\n", env, statusLabel)
	fmt.Fprintf(&sb, "- 结论: %s\n", validationConclusion(status))
	fmt.Fprintf(&sb, "- 入口: frontend=%s; api=%s\n", frontendURL, apiURL)
	fmt.Fprintf(&sb, "- 实际现象: %s\n", observed)
	fmt.Fprintf(&sb, "- 期望表现: %s\n", expected)
	fmt.Fprintf(&sb, "- 关键证据: %s\n", evidenceSummary)
	fmt.Fprintf(&sb, "- 需补信息: %s\n\n", gaps)
	sb.WriteString("#### 原始结构化结果\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString(report)
	sb.WriteString("\n```\n")
	return sb.String()
}

func normalizeValidationReportEnv(env string, bug Bug, bot BotRef) string {
	env = strings.TrimSpace(strings.Trim(env, "`\"'"))
	fallback := firstNonEmpty(effectiveBugEnv(bug, bot), "-")
	if env == "" || env == "-" {
		return fallback
	}
	lower := strings.ToLower(env)
	if strings.Contains(lower, "bug env") || strings.Contains(lower, "bot env") {
		bugEnv := fieldFromLooseEnvLabel(env, "bug env")
		botEnv := fieldFromLooseEnvLabel(env, "bot env")
		return firstNonEmpty(nonDash(bugEnv), nonDash(botEnv), fallback)
	}
	return env
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

func validationStatusLabel(status string) string {
	switch status {
	case "reproduced":
		return "已复现"
	case "not_reproduced":
		return "未复现"
	case "insufficient_info":
		return "信息不足"
	case "fixed_verified":
		return "修复已验证"
	case "still_reproduces":
		return "修复后仍复现"
	default:
		return "结论不完整"
	}
}

func validationConclusion(status string) string {
	switch status {
	case "reproduced":
		return "已复现原始 Bug，可以进入排障 Agent。"
	case "not_reproduced":
		return "未复现原始 Bug，已暂停进入排障 Agent。"
	case "insufficient_info":
		return "验证所需信息不足，用户补充后应继续验证。"
	case "fixed_verified":
		return "修复验证通过，已暂停进入排障 Agent。"
	case "still_reproduces":
		return "修复后仍可复现，需要进入排障 Agent。"
	default:
		return "验证 Agent 未输出可进入排障的完整结构化结论。"
	}
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
		return "修复分支已生成、提交并推送，等待部署后回归验证。"
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
	return i.executePreparedPhase(parent, attemptID, cmd, parser, emit)
}

// ExecutePhaseWithAttachments transports trusted host evidence through the
// target-specific mechanism: Codex receives --image, Claude receives a
// read-authorized directory, and OpenClaw receives a short-lived file inside
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
	openclawBin := firstNonEmpty(i.binaries["openclaw"], "openclaw")
	i.mu.Unlock()
	switch target {
	case "codex":
		cmd, err = buildCodexExecCommand(codexBin, bot.Path, prompt+phaseAttachmentPrompt(nil), paths)
		parser = ParseCodexJSONLEvent
	case "claude-code":
		directories := make([]string, 0, len(paths))
		for _, path := range paths {
			directories = append(directories, filepath.Dir(path))
		}
		cmd, err = buildClaudeInvestigationCommand(claudeBin, claudeWorkspace(bot.Path), bot.Path, prompt+phaseAttachmentPrompt(paths), directories)
		parser = ParseClaudeStreamJSONEvent
	case "openclaw":
		if len(validated) != 1 {
			return PhaseExecutionResult{}, errors.New("OpenClaw browser evaluation requires exactly one screenshot")
		}
		workspacePath, removeView, viewErr := createBrowserEvaluatorScreenshotViewAt(bot.Path, validated[0].Content)
		if viewErr != nil {
			return PhaseExecutionResult{}, viewErr
		}
		cleanup = removeView
		paths = append(paths, workspacePath)
		cmd, err = BuildOpenClawInvestigationCommand(openclawBin, openClawAgentID(bot), prompt+phaseAttachmentPrompt([]string{workspacePath}))
		parser = ParseOpenClawJSONEvent
	default:
		return PhaseExecutionResult{}, fmt.Errorf("暂不支持 %s 后台直启", firstNonEmpty(target, "unknown"))
	}
	if err != nil {
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
		return result, errors.New("agent returned no final structured result")
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
