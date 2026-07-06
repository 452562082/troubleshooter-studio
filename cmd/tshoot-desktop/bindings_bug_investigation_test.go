package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestStartBugInvestigationRunsCodexBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nprintf '%s\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{
			Key:      repo + "|codex",
			Target:   "codex",
			Path:     repo,
			SystemID: "base",
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if run.Status != bughub.InvestigationRunning {
		t.Fatalf("run = %+v", run)
	}
	waited, err := app.codexInvestigator().Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != bughub.InvestigationSucceeded || waited.FinalMessage != "done" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestStartBugInvestigationRejectsNonCodexBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot:   bughub.BotRef{Key: "repo|cursor", Target: "cursor", Path: root, SystemID: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "当前只支持 Codex 机器人直接排障") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartBugInvestigationRejectsMissingCodexCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", t.TempDir())
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot:   bughub.BotRef{Key: repo + "|codex", Target: "codex", Path: repo, SystemID: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "未检测到 codex CLI") {
		t.Fatalf("err = %v", err)
	}
}

func TestListBugInvestigationRunsReturnsStoredRunsForBug(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	store := bugInvestigationStore()
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-old",
		BugID:     "zentao-577",
		Status:    bughub.InvestigationSucceeded,
		StartedAt: time.Now().Add(-time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("Upsert old run: %v", err)
	}
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-new",
		BugID:     "zentao-577",
		Status:    bughub.InvestigationRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert new run: %v", err)
	}
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-other",
		BugID:     "zentao-999",
		Status:    bughub.InvestigationRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert other run: %v", err)
	}

	runs, err := (&App{}).ListBugInvestigationRuns("zentao-577")
	if err != nil {
		t.Fatalf("ListBugInvestigationRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run-new" || runs[1].ID != "run-old" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestCancelBugInvestigationRejectsBlankRunID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	err := (&App{}).CancelBugInvestigation(BugInvestigationCancelInput{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blank run should be validation error, got %v", err)
	}
}
