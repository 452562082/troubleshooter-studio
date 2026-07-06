package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type BugInvestigationInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

type BugInvestigationCancelInput struct {
	RunID string `json:"run_id"`
}

func (a *App) StartBugInvestigation(input BugInvestigationInput) (bughub.InvestigationRun, error) {
	bug, ok, err := bugStore().Get(input.BugID)
	if err != nil {
		return bughub.InvestigationRun{}, err
	}
	if !ok {
		return bughub.InvestigationRun{}, os.ErrNotExist
	}
	target := strings.TrimSpace(input.Bot.Target)
	switch target {
	case "codex":
		if _, err := exec.LookPath("codex"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 codex CLI")
		}
	case "claude-code":
		if _, err := exec.LookPath("claude"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 claude CLI")
		}
	case "openclaw":
		if _, err := exec.LookPath("openclaw"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 openclaw CLI")
		}
	case "cursor":
		return bughub.InvestigationRun{}, errors.New("暂不支持 Cursor 后台直启，请复制上下文后在 Cursor Custom Agent 中发起")
	default:
		return bughub.InvestigationRun{}, errors.New("暂不支持该机器人后台直启")
	}
	ctx := a.getRuntimeContext()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.codexInvestigator().Start(ctx, bug, input.Bot)
}

func (a *App) CancelBugInvestigation(input BugInvestigationCancelInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("run id is required")
	}
	return a.codexInvestigator().Cancel(input.RunID)
}

func (a *App) ListBugInvestigationRuns(bugID string) ([]bughub.InvestigationRun, error) {
	return bugInvestigationStore().ListByBug(bugID)
}

func (a *App) codexInvestigator() *bughub.CodexInvestigator {
	a.bugInvestigationMu.Lock()
	defer a.bugInvestigationMu.Unlock()
	if a.bugInvestigator == nil {
		a.bugInvestigator = bughub.NewCodexInvestigator(bugInvestigationStore(), "codex")
		a.bugInvestigator.SetEventSink(func(run bughub.InvestigationRun, event bughub.InvestigationEvent) {
			if ctx := a.getRuntimeContext(); ctx != nil {
				wailsruntime.EventsEmit(ctx, "bug-investigation:event", map[string]any{
					"run":   run,
					"event": event,
				})
			}
		})
	}
	return a.bugInvestigator
}

func bugInvestigationStore() *bughub.InvestigationStore {
	return bughub.NewInvestigationStore(bughub.DefaultRoot())
}
