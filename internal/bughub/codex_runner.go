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

type CodexInvestigator struct {
	store    *InvestigationStore
	codexBin string
	mu       sync.Mutex
	active   map[string]*activeCodexRun
}

type activeCodexRun struct {
	cancel context.CancelFunc
	done   chan struct{}
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
	gitPath := filepath.Join(workspace, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("workspace %q is not a git repository", workspace)
		}
		return nil, fmt.Errorf("check git repository %q: %w", workspace, err)
	}
	cmd := exec.Command(codexBin, "exec", "--json", "--sandbox", "workspace-write", "--cd", workspace, prompt)
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
	if active, ok, err := i.store.ActiveRunForBug(bug.ID); err != nil {
		return InvestigationRun{}, err
	} else if ok {
		return active, nil
	}

	prompt := BuildCodexInvestigationPrompt(bug, bot)
	cmd, err := BuildCodexExecCommand(i.codexBin, bot.Path, prompt)
	if err != nil {
		return InvestigationRun{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = strings.TrimSpace(bot.Path)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return InvestigationRun{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
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
		cancel()
		return InvestigationRun{}, err
	}
	active := &activeCodexRun{cancel: cancel, done: make(chan struct{})}
	i.mu.Lock()
	i.active[run.ID] = active
	i.mu.Unlock()

	if err := cmd.Start(); err != nil {
		_ = i.store.Finish(run.ID, InvestigationFailed, "", err.Error())
		i.removeActive(run.ID)
		cancel()
		close(active.done)
		return run, err
	}

	go i.collectRun(ctx, run.ID, cmd, stdout, stderr, active.done)
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
	<-active.done
	i.removeActive(runID)
	return nil
}

func (i *CodexInvestigator) Wait(runID string) (InvestigationRun, error) {
	i.mu.Lock()
	active, ok := i.active[runID]
	i.mu.Unlock()
	if ok {
		<-active.done
		i.removeActive(runID)
	}
	return i.store.Get(runID)
}

func (i *CodexInvestigator) collectRun(ctx context.Context, runID string, cmd *exec.Cmd, stdout io.Reader, stderr io.Reader, done chan<- struct{}) {
	defer close(done)
	defer i.removeActive(runID)

	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrDone <- strings.TrimSpace(string(data))
	}()

	var finalMessage string
	var failure string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, final, failed := ParseCodexJSONLEvent([]byte(line))
		if strings.TrimSpace(event.Message) != "" {
			_ = i.store.AppendEvent(runID, event)
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
		_ = i.store.Finish(runID, InvestigationCancelled, finalMessage, ctx.Err().Error())
	case strings.TrimSpace(failure) != "":
		_ = i.store.Finish(runID, InvestigationFailed, finalMessage, failure)
	case waitErr != nil:
		errorText := strings.TrimSpace(stderrText)
		if errorText == "" {
			errorText = waitErr.Error()
		}
		_ = i.store.Finish(runID, InvestigationFailed, finalMessage, errorText)
	default:
		_ = i.store.Finish(runID, InvestigationSucceeded, finalMessage, "")
	}
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
