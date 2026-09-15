package bughub

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRetiredWorkflowMigrationPreservesHistoryAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, c := range []IncidentCase{
		{ID: "retired", BugID: "old", Status: CaseValidating, CurrentAttemptID: "old-attempt", CycleNumber: 1, Version: 2},
		{ID: "ongoing", BugID: "current", Status: CaseInvestigating, CycleNumber: 1, Version: 1},
	} {
		if err := store.CreateCase(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	old := PhaseAttempt{ID: "old-attempt", CaseID: "retired", CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning, InputJSON: []byte(`{"historical":true}`), OutputJSON: []byte(`{"evidence":"retained"}`), StartedAt: now}
	if err := store.createAttempt(ctx, old, AttemptValidationOptions{AllowLegacyMigration: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO transition_events(id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES('old-event','retired','pending_validation','validating','validation_started','user','alice','old-start','{}',?,'','{}')`, formatStoreTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE key='workflow-investigation-fix-submission-v1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		store, err = OpenCaseStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.GetCase(ctx, "retired")
		if err != nil || got.Status != CaseLegacyArchived || got.Version != 3 || got.ClosedAt == nil {
			t.Fatalf("case=%+v err=%v", got, err)
		}
		current, err := store.GetCase(ctx, "ongoing")
		if err != nil || current.Status != CaseInvestigating || current.Version != 1 {
			t.Fatalf("ongoing=%+v err=%v", current, err)
		}
		attempt, err := store.GetAttempt(ctx, old.ID)
		if err != nil || attempt.Status != AttemptStatusCancelled || string(attempt.OutputJSON) != string(old.OutputJSON) {
			t.Fatalf("attempt=%+v err=%v", attempt, err)
		}
		events, err := store.ListEvents(ctx, "retired")
		if err != nil || len(events) != 2 {
			t.Fatalf("events=%+v err=%v", events, err)
		}
		if err := store.CreateAttempt(ctx, PhaseAttempt{ID: "forbidden", CaseID: "retired", CycleNumber: 1, Phase: PhaseRegression, Mode: AttemptRegression, Status: AttemptStatusQueued, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err == nil {
			t.Fatal("accepted retired executable phase")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
