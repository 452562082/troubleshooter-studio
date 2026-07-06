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
	var sb strings.Builder
	sb.WriteString("请作为选定的 AI 排障机器人开始排障。\n")
	sb.WriteString("目标：基于下面 Bug 工单上下文，完成只读根因分析，输出可执行结论和下一步建议。\n")
	sb.WriteString("约束：默认不要修改代码，不要执行破坏性命令；如必须写入或重启服务，先在结论中说明需要人工确认。\n\n")
	sb.WriteString(GenerateContext(b, bot))
	sb.WriteString("\n请按以下结构输出：\n")
	sb.WriteString("1. 现象复述\n2. 已验证事实\n3. 最可能根因\n4. 建议排查命令或证据\n5. 需要用户补充的信息\n")
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
	cmd := exec.Command(claudeBin, "-p", "--output-format", "stream-json", "--agent", agentName, prompt)
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
	cmdDir := cmd.Dir
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = cmdDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		i.mu.Unlock()
		cancel()
		return InvestigationRun{}, err
	}

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
	setCodexProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		if finishErr := i.store.Finish(run.ID, InvestigationFailed, "", err.Error()); finishErr != nil {
			active.setError(finishErr)
		}
		delete(i.active, run.ID)
		i.mu.Unlock()
		cancel()
		close(active.done)
		return run, err
	}
	active.process = cmd.Process

	i.mu.Unlock()
	go i.collectRun(ctx, run.ID, cmd, stdout, stderr, active, parser)
	return run, nil
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

func (i *CodexInvestigator) collectRun(ctx context.Context, runID string, cmd *exec.Cmd, stdout io.Reader, stderr io.Reader, active *activeCodexRun, parser investigationEventParser) {
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
	stderrText := <-stderrDone
	var finishStatus InvestigationStatus
	var finishError string
	switch {
	case ctx.Err() != nil:
		finishStatus = InvestigationCancelled
		finishError = ctx.Err().Error()
	case strings.TrimSpace(failure) != "":
		finishStatus = InvestigationFailed
		finishError = failure
	case waitErr != nil:
		errorText := strings.TrimSpace(stderrText)
		if errorText == "" {
			errorText = waitErr.Error()
		}
		finishStatus = InvestigationFailed
		finishError = errorText
	default:
		finishStatus = InvestigationSucceeded
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
	pathBase := strings.TrimSuffix(filepath.Base(strings.TrimSpace(bot.Path)), filepath.Ext(strings.TrimSpace(bot.Path)))
	return firstNonEmpty(pathBase, bot.SystemID, bot.Name)
}
