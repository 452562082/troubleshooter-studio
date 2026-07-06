package bughub

import (
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
