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
	original := PhaseAttempt{ID: "validation-original", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{"reproduction_steps":["submit order"],"expected_behavior":"order succeeds"}`), OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"order failed","expected_behavior":"order succeeds","evidence":[],"gaps":[]}`), StartedAt: now, FinishedAt: &now}
	root := PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator", InputJSON: []byte(`{}`), OutputJSON: nonCodeRootCauseOutput(rootCauseType, mode), StartedAt: now, FinishedAt: &now}
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

func TestCompleteRemediationRecordsAuditAndStartsFreshRegressionWithoutCodeChange(t *testing.T) {
	store, incident, root := prepareRemediationCase(t, RootCauseData, RemediationOperatorAction)
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
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
	if completed.Status != CaseRegressionValidating || runner.startCount() != 1 {
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
	var input RegressionValidationInput
	if err := json.Unmarshal(runner.starts[0].InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	if input.RemediationBindingID != scope.BindingID || input.RemediationType != RootCauseData || input.RemediationSummary != command.Summary || len(input.ExpectedFixCommits) != 0 {
		t.Fatalf("regression input=%+v scope=%+v", input, scope)
	}

	replayed, err := orchestrator.CompleteRemediation(context.Background(), command)
	if err != nil || replayed.ID != completed.ID || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
	changed := command
	changed.Summary = "changed replay must not be accepted"
	if _, err := orchestrator.CompleteRemediation(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
}

func TestRecoverInterruptedRemediationStartsRegressionExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store, incident, root := prepareRemediationCase(t, RootCauseInfrastructure, RemediationOperatorAction)
	command := CompleteRemediationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: CompleteRemediationKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID,
		Summary: "restarted the unhealthy workload", Evidence: "change-ticket OPS-42",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"},
	}
	result, err := validatedRootCauseResult(ctx, store, incident, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindingID := stableID("remediation-binding", command.IdempotencyKey)
	scope := RemediationApprovalScope{
		RootCauseAttemptID: root.ID,
		CycleNumber:        incident.CycleNumber,
		RootCauseType:      result.RootCauseType,
		Mode:               result.Remediation.Mode,
		Target:             result.Remediation.Target,
		RecommendedAction:  result.Remediation.Summary,
		Rollback:           result.Remediation.Rollback,
		Verification:       result.Remediation.Verification,
		Summary:            command.Summary,
		Evidence:           command.Evidence,
		BindingID:          bindingID,
	}
	approval := Approval{
		ID: stableID("approval", command.IdempotencyKey), CaseID: incident.ID,
		Kind: ApprovalCompleteRemediation, Actor: command.ActorID,
		CaseVersion: incident.Version, ScopeJSON: mustJSON(scope),
	}
	reservation := DeploymentReservation{
		ReservationID:  stableID("deployment-reservation", command.IdempotencyKey),
		ReservationKey: command.IdempotencyKey, CallerIdempotencyKey: command.IdempotencyKey,
		ActorID: command.ActorID, OriginalExpectedVersion: incident.Version,
		CycleNumber: incident.CycleNumber, Environment: incident.Environment,
		ExpectedCommits: map[string]string{}, RemediationBindingID: bindingID,
		RemediationType: result.RootCauseType, RemediationSummary: command.Summary,
		Bug: command.Bug, Bot: command.Bot,
		VerifierInput: DeploymentVerificationRequest{
			CaseID: incident.ID, Environment: incident.Environment,
			ExpectedCommits: map[string]string{}, Source: "manual-remediation",
		},
	}
	now := time.Now().UTC()
	observation := DeploymentObservation{
		ID: stableID("deployment", reservation.ReservationKey), CaseID: incident.ID,
		Environment: incident.Environment, ExpectedCommits: map[string]string{},
		UserNotifiedAt: &now, VerificationSource: "manual-remediation", ObservedAt: now,
		DiagnosticCode: "remediation_completed", DiagnosticMessage: "operator confirmed remediation",
		Result: DeploymentResultUnavailable,
	}
	payload := mustJSON(reservation)
	committed, err := store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: command.IdempotencyKey, RequestJSON: mustJSON(command),
		Approvals: []Approval{approval}, Observations: []DeploymentObservation{observation},
		Steps: []CaseMutationStep{{To: CaseRemediationApplied, Event: TransitionEvent{
			ID: stableID("event", command.IdempotencyKey), EventType: "remediation_regression_reserved",
			ActorType: "user", ActorID: command.ActorID, PayloadJSON: payload,
		}}},
	})
	if err != nil || committed.Case.Status != CaseRemediationApplied {
		t.Fatalf("committed=%+v err=%v", committed, err)
	}

	runner := &recordingPhaseRunner{}
	restarted := NewCaseOrchestrator(store, runner, nil, nil)
	if err := restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil || current.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d err=%v", current, runner.startCount(), err)
	}
	if err := restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if runner.startCount() != 1 {
		t.Fatalf("recovery replay started regression %d times", runner.startCount())
	}
}

func TestCompleteRemediationRejectsCodeRootCauseAndStaleScope(t *testing.T) {
	store, incident, root := prepareRemediationCase(t, RootCauseCode, RemediationCodeChange)
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
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
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
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
