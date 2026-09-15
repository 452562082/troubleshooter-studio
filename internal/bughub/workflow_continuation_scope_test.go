package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestFixRetryKeepsApprovedScopeAndReplaysWithoutAnotherAttempt(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "retry", BugID: "bug", Status: CaseFixFailed, CycleNumber: 1, Version: 1, CurrentAttemptID: "failed", SelectedBotKey: "fixer", Environment: "test"}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	previous := PhaseAttempt{ID: "failed", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusFailed, BotKey: "fixer", AgentTarget: "codex", ParentAttemptID: "approved-root", InputJSON: []byte(`{"source_baselines":{"api":"develop"},"required_fix_branch_suffix":"approved-suffix"}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, previous); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil)
	cmd := ContinueWithEvidenceCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "retry-once", ActorID: "alice", Phase: PhaseFix, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"unapproved":"main"},"user_input":"retry after tool recovery"}`)}
	first, err := o.ContinueWithEvidence(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != CaseFixing || runner.startCount() != 1 {
		t.Fatalf("first=%+v starts=%d", first, runner.startCount())
	}
	attempt := runner.starts[0]
	var input map[string]any
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	if attempt.ParentAttemptID != "approved-root" || !reflect.DeepEqual(input["source_baselines"], map[string]any{"api": "develop"}) || input["required_fix_branch_suffix"] != "approved-suffix" || input["user_input"] != "retry after tool recovery" {
		t.Fatalf("scope=%s parent=%s", attempt.InputJSON, attempt.ParentAttemptID)
	}
	replay, err := o.ContinueWithEvidence(ctx, cmd)
	if err != nil || replay.Version != first.Version || replay.CurrentAttemptID != first.CurrentAttemptID || runner.startCount() != 1 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	cmd.InputJSON = []byte(`{"user_input":"different request"}`)
	if _, err := o.ContinueWithEvidence(ctx, cmd); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay=%v", err)
	}
}
