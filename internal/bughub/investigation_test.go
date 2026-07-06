package bughub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvestigationStoreCreateAppendAndList(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	run := InvestigationRun{
		ID:            "run-1",
		BugID:         "zentao-577",
		BotKey:        "/Users/me/.codex/agents/base.toml|codex",
		Status:        InvestigationRunning,
		StartedAt:     time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		PromptPreview: "Investigate bug",
	}
	if err := store.Upsert(run); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.AppendEvent("run-1", InvestigationEvent{Type: "agent_message", Message: "checking logs"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.Finish("run-1", InvestigationSucceeded, "root cause", ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	runs, err := store.ListByBug("zentao-577")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got := runs[0]
	if got.Status != InvestigationSucceeded || got.FinalMessage != "root cause" {
		t.Fatalf("run = %+v", got)
	}
	if got.FinishedAt == nil {
		t.Fatalf("FinishedAt is nil")
	}
	if len(got.Events) != 1 || got.Events[0].Message != "checking logs" {
		t.Fatalf("events = %+v", got.Events)
	}
}

func TestInvestigationStoreActiveRunForBug(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "done", BugID: "b1", Status: InvestigationSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "running", BugID: "b1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.ActiveRunForBug("b1")
	if err != nil {
		t.Fatalf("ActiveRunForBug: %v", err)
	}
	if !ok || got.ID != "running" {
		t.Fatalf("active ok=%v run=%+v", ok, got)
	}
}

func TestInvestigationStoreListByBugFiltersAndSortsNewestFirst(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	older := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Upsert(InvestigationRun{ID: "old", BugID: "b1", StartedAt: older}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "other", BugID: "b2", StartedAt: newer}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "new", BugID: "b1", StartedAt: newer}); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs len = %d", len(runs))
	}
	if runs[0].ID != "new" || runs[1].ID != "old" {
		t.Fatalf("runs order = %+v", runs)
	}
}

func TestInvestigationStoreUpsertValidationAndDefaults(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{BugID: "b1"}); err == nil {
		t.Fatal("expected empty ID error")
	}
	if err := store.Upsert(InvestigationRun{ID: "run-1"}); err == nil {
		t.Fatal("expected empty BugID error")
	}

	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got := runs[0]
	if got.Status != InvestigationQueued {
		t.Fatalf("status = %q", got.Status)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero")
	}
	if got.StartedAt.Location() != time.UTC {
		t.Fatalf("StartedAt location = %v", got.StartedAt.Location())
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "finished_at") {
		t.Fatalf("unfinished run serialized finished_at: %s", data)
	}
}

func TestInvestigationStoreMissingRunErrors(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.AppendEvent("missing", InvestigationEvent{Type: "agent_message"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AppendEvent err = %v", err)
	}
	if err := store.Finish("missing", InvestigationFailed, "", "failed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Finish err = %v", err)
	}
}

func TestInvestigationStoreMissingAndEmptyFile(t *testing.T) {
	root := t.TempDir()
	store := NewInvestigationStore(root)
	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug missing file: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got, ok, err := store.ActiveRunForBug("b1")
	if err != nil {
		t.Fatalf("ActiveRunForBug missing file: %v", err)
	}
	if ok || got.ID != "" {
		t.Fatalf("active ok=%v run=%+v", ok, got)
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err = store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug empty file: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs len = %d", len(runs))
	}
}

func TestInvestigationStoreWriteNewlineAndMode(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("runs.json missing trailing newline: %q", data)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestInvestigationStoreWriteTightensExistingFileMode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestParseCodexJSONLEvent(t *testing.T) {
	event, final, failed := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"root cause found"}}`))
	if event.Type != "agent_message" || event.Message != "root cause found" {
		t.Fatalf("event = %+v", event)
	}
	if final != "root cause found" || failed != "" {
		t.Fatalf("final=%q failed=%q", final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`{"type":"turn.failed","error":{"message":"auth missing"}}`))
	if event.Type != "turn_failed" || failed != "auth missing" || final != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`not-json`))
	if event.Type != "raw" || event.Message != "not-json" || final != "" || failed != "" {
		t.Fatalf("malformed event=%+v final=%q failed=%q", event, final, failed)
	}
}

func TestBuildCodexInvestigationPromptIncludesBugAndBot(t *testing.T) {
	bug := Bug{ID: "zentao-577", Source: "zentao", SourceID: "577", Title: "搜索结果错误", Steps: "1. 搜索电影"}
	bot := BotRef{Key: "/tmp/base.toml|codex", SystemID: "base", Target: "codex", Path: "/tmp/base.toml"}
	prompt := BuildCodexInvestigationPrompt(bug, bot)
	for _, want := range []string{
		"请作为选定的 Codex 排障机器人开始排障",
		"搜索结果错误",
		"zentao:577",
		"target: codex",
		"不要修改代码",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
