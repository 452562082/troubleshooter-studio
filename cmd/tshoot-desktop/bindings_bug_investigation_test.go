package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	repo := filepath.Join(root, "base-troubleshooter")
	validatorRepo := filepath.Join(root, "base-validator")
	for _, dir := range []string{repo, validatorRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
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
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
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

func TestStartBugInvestigationMaterializesZentaoAttachmentsForAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "base-troubleshooter")
	validatorRepo := filepath.Join(root, "base-validator")
	for _, dir := range []string{repo, validatorRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\nprintf '%s\\n' \"$last\" >> \"$CALLS_PATH\"\nprintf '%s\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CALLS_PATH", callsPath)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		if r.URL.Path != "/api.php/v1/files/101/download" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nagent-readable"))
	}))
	defer srv.Close()
	if _, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID:      "zentao-main",
		Name:    "禅道",
		Type:    "zentao",
		BaseURL: srv.URL,
		Token:   "secret",
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID:     "zentao-718",
		Source: "zentao",
		Title:  "Bug 718",
		Attachments: []bughub.Attachment{{
			ID:        "101",
			Name:      "screen.png",
			Type:      "image/png",
			RemoteURL: "/data/upload/1/202607/screen",
		}},
	}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-718",
		Bot: bughub.BotRef{
			Key:      repo + "|codex",
			Target:   "codex",
			Path:     repo,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if _, err := app.codexInvestigator().Wait(run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	prompts, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	if !strings.Contains(string(prompts), "id=101") {
		t.Fatalf("prompt missing attachment id:\n%s", prompts)
	}
	localMarker := filepath.Join(".tshoot", "bugs", "attachments", "zentao-718", "101-screen.png")
	if !strings.Contains(string(prompts), localMarker) {
		t.Fatalf("prompt missing materialized attachment path %q:\n%s", localMarker, prompts)
	}
	materialized := filepath.Join(root, localMarker)
	data, err := os.ReadFile(materialized)
	if err != nil {
		t.Fatalf("materialized attachment missing: %v", err)
	}
	if !strings.Contains(string(data), "agent-readable") {
		t.Fatalf("materialized data = %q", data)
	}
}

func TestStartBugInvestigationRunsClaudeBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	agentPath := filepath.Join(root, "skills", "base-troubleshooter")
	validatorPath := filepath.Join(root, "skills", "base-validator")
	for _, dir := range []string{agentPath, validatorPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "claude")
	script := "#!/bin/sh\nprintf '%s\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"claude done\"}'\n"
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
			Key:      agentPath + "|claude-code",
			Target:   "claude-code",
			Path:     agentPath,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	waited, err := app.codexInvestigator().Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != bughub.InvestigationSucceeded || waited.FinalMessage != "claude done" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestStartBugInvestigationRejectsCursorBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot:   bughub.BotRef{Key: "repo|cursor", Target: "cursor", Path: root, SystemID: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "暂不支持 Cursor") {
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
