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
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type InvestigationEventSink func(run InvestigationRun, event InvestigationEvent)

type CodexInvestigator struct {
	store     *InvestigationStore
	codexBin  string
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
		active:   make(map[string]*activeCodexRun),
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
	sb.WriteString("请作为选定的 Codex 排障机器人开始排障。\n")
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

func (i *CodexInvestigator) Start(parent context.Context, bug Bug, bot BotRef) (InvestigationRun, error) {
	if i == nil || i.store == nil {
		return InvestigationRun{}, errors.New("investigation store is required")
	}
	if strings.TrimSpace(bot.Target) != "codex" {
		return InvestigationRun{}, fmt.Errorf("bot target %q is not codex", bot.Target)
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

	prompt := BuildCodexInvestigationPrompt(bug, bot)
	cmd, err := BuildCodexExecCommand(i.codexBin, bot.Path, prompt)
	if err != nil {
		i.mu.Unlock()
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = strings.TrimSpace(bot.Path)

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
	go i.collectRun(ctx, run.ID, cmd, stdout, stderr, active)
	return run, nil
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

func (i *CodexInvestigator) collectRun(ctx context.Context, runID string, cmd *exec.Cmd, stdout io.Reader, stderr io.Reader, active *activeCodexRun) {
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
		event, final, failed := ParseCodexJSONLEvent([]byte(line))
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
	switch {
	case ctx.Err() != nil:
		active.setError(i.store.Finish(runID, InvestigationCancelled, finalMessage, ctx.Err().Error()))
	case strings.TrimSpace(failure) != "":
		active.setError(i.store.Finish(runID, InvestigationFailed, finalMessage, failure))
	case waitErr != nil:
		errorText := strings.TrimSpace(stderrText)
		if errorText == "" {
			errorText = waitErr.Error()
		}
		active.setError(i.store.Finish(runID, InvestigationFailed, finalMessage, errorText))
	default:
		active.setError(i.store.Finish(runID, InvestigationSucceeded, finalMessage, ""))
	}
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
