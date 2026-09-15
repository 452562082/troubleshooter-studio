package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func nonCodeRootCauseOutput(rootCauseType RootCauseType, mode RemediationMode) []byte {
	return mustJSON(InvestigationResult{
		InvestigationStatus: "root_cause_ready",
		Environment:         "test",
		RootCause:           "runtime state is inconsistent",
		Confidence:          "high",
		RootCauseType:       rootCauseType,
		Remediation: RemediationPlan{
			Mode:         mode,
			Target:       "test/order-service",
			Summary:      "restore the affected runtime state",
			Rollback:     "restore the previous snapshot",
			Verification: "rerun the original business scenario",
		},
		Evidence: []ArtifactReference{},
		Gaps:     []string{},
	})
}

func prepareRemediationCase(t *testing.T, rootCauseType RootCauseType, mode RemediationMode) (*CaseStore, IncidentCase, PhaseAttempt) {
	t.Helper()
	ctx := context.Background()
	store := newOrchestratorStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	incident := IncidentCase{ID: "case-remediation", BugID: "bug-remediation", Source: "test", SystemID: "shop", Environment: "test", Status: CaseWaitingRemediation, CycleNumber: 1, CurrentAttemptID: "investigation-remediation", SelectedBotKey: "validator", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	original := PhaseAttempt{ID: "validation-original", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Mode: "", Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{"reproduction_steps":["submit order"],"expected_behavior":"order succeeds"}`), OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"order failed","expected_behavior":"order succeeds","evidence":[],"gaps":[]}`), StartedAt: now, FinishedAt: &now}
	rootTime := now.Add(time.Second)
	root := PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator", InputJSON: []byte(`{}`), OutputJSON: nonCodeRootCauseOutput(rootCauseType, mode), StartedAt: rootTime, FinishedAt: &rootTime}
	for _, attempt := range []PhaseAttempt{original, root} {
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	artifact := EvidenceArtifact{ID: "original-evidence", CaseID: incident.ID, AttemptID: original.ID, Kind: "api", PathOrReference: "/artifacts/original", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CapturedAt: now, Environment: incident.Environment, Version: "before", RequestID: "request-original", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, artifact, nil); err != nil {
		t.Fatal(err)
	}
	incident, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, incident, root
}

func TestCompleteRemediationRecordsAuditWithoutStartingAnotherAgent(t *testing.T) {
	store, incident, root := prepareRemediationCase(t, RootCauseData, RemediationOperatorAction)
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil)
	command := CompleteRemediationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: CompleteRemediationKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID,
		Summary: "restored order 42 to pending", Evidence: "change-ticket DATA-42",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"},
	}
	completed, err := orchestrator.CompleteRemediation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CaseRemediationRecorded || completed.ClosedAt == nil || runner.startCount() != 0 {
		t.Fatalf("case=%+v starts=%d", completed, runner.startCount())
	}
	if changes, err := store.ListCodeChanges(context.Background(), incident.ID); err != nil || len(changes) != 0 {
		t.Fatalf("code changes=%+v err=%v", changes, err)
	}
	approvals, err := store.ListApprovals(context.Background(), incident.ID)
	if err != nil || len(approvals) != 1 || approvals[0].Kind != ApprovalCompleteRemediation {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	var scope RemediationApprovalScope
	if err := json.Unmarshal(approvals[0].ScopeJSON, &scope); err != nil || scope.RootCauseType != RootCauseData || scope.Summary != command.Summary || scope.BindingID == "" {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	replayed, err := orchestrator.CompleteRemediation(context.Background(), command)
	if err != nil || replayed.ID != completed.ID || runner.startCount() != 0 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
	changed := command
	changed.Summary = "changed replay must not be accepted"
	if _, err := orchestrator.CompleteRemediation(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
}

func TestCompleteRemediationRejectsCodeRootCauseAndStaleScope(t *testing.T) {
	store, incident, root := prepareRemediationCase(t, RootCauseCode, RemediationCodeChange)
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil)
	base := CompleteRemediationCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, ActorID: "alice", RootCauseAttemptID: root.ID, Summary: "patched code", Evidence: "commit abc", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}}
	base.IdempotencyKey = CompleteRemediationKey(base.CaseID, base.RootCauseAttemptID, base.ExpectedVersion)
	if _, err := orchestrator.CompleteRemediation(context.Background(), base); !errors.Is(err, ErrRemediationNotApplicable) {
		t.Fatalf("code root cause err=%v", err)
	}
	base.ExpectedVersion++
	base.IdempotencyKey = CompleteRemediationKey(base.CaseID, base.RootCauseAttemptID, base.ExpectedVersion)
	if _, err := orchestrator.CompleteRemediation(context.Background(), base); !errors.Is(err, ErrCaseVersionConflict) {
		t.Fatalf("stale version err=%v", err)
	}
}

func TestRootCauseOutcomeRoutesNonCodeCauseAwayFromFixApproval(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-root-routing", CaseInvestigating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil)
	completed, err := orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "root-routing", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: nonCodeRootCauseOutput(RootCauseInfrastructure, RemediationOperatorAction)})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CaseWaitingRemediation || completed.CurrentAttemptID != attempt.ID {
		t.Fatalf("completed=%+v", completed)
	}
	events, err := store.ListEvents(ctx, incident.ID)
	if err != nil || events[len(events)-1].EventType != "remediation_confirmation_requested" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
