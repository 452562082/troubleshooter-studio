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
	if strings.TrimSpace(input.Bot.Target) != "codex" {
		return bughub.InvestigationRun{}, errors.New("当前只支持 Codex 机器人直接排障")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return bughub.InvestigationRun{}, errors.New("未检测到 codex CLI")
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
