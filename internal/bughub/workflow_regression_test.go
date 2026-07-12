package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRegressionStartBuildsDeterministicCurrentCycleInput(t *testing.T) {
	store, incident, original, observation := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)

	started, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase != PhaseRegression || started.Mode != AttemptRegression || started.CycleNumber != incident.CycleNumber {
		t.Fatalf("attempt=%+v", started)
	}
	var input RegressionValidationInput
	if err := json.Unmarshal(started.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	if input.OriginalValidationAttemptID != original.ID || input.OriginalReproduction == "" || input.ExpectedBehavior != "healthy response" || input.OriginalScenarioHash == "" {
		t.Fatalf("input=%+v", input)
	}
	if input.CycleNumber != incident.CycleNumber || input.DeploymentObservationID != observation.ID || input.DeploymentReservationID == "" || input.ObservedDeploymentVersion != observation.ObservedVersion || input.TargetEnvironment != incident.Environment {
		t.Fatalf("deployment binding=%+v", input)
	}
	if input.ExpectedFixCommits["repo"] != "merge-1" || len(input.OriginalEvidenceReferences) != 1 || runner.startCount() != 1 {
		t.Fatalf("input=%+v starts=%d", input, runner.startCount())
	}
	if _, err := o.StartRegression(context.Background(), incident.ID, startedCaseVersion(t, store, incident.ID)); !errors.Is(err, ErrRegressionDuplicate) {
		t.Fatalf("duplicate error=%v", err)
	}
}

func TestRegressionStartRejectsUnverifiedOrDriftedDeployment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *CaseStore, *IncidentCase, *DeploymentObservation)
	}{
		{name: "unverified", mutate: func(t *testing.T, store *CaseStore, incident *IncidentCase, _ *DeploymentObservation) {
			if _, err := store.db.ExecContext(context.Background(), `UPDATE incident_cases SET status=? WHERE id=?`, CaseDeploymentUnverified, incident.ID); err != nil {
				t.Fatal(err)
			}
			incident.Status = CaseDeploymentUnverified
		}},
		{name: "environment mismatch", mutate: func(_ *testing.T, _ *CaseStore, _ *IncidentCase, observation *DeploymentObservation) {
			observation.Environment = "prod"
		}},
		{name: "commit drift", mutate: func(_ *testing.T, _ *CaseStore, _ *IncidentCase, observation *DeploymentObservation) {
			observation.ExpectedCommits = map[string]string{"repo": "old"}
			observation.ObservedCommits = map[string]string{"repo": "old"}
		}},
		{name: "old cycle reservation", mutate: func(t *testing.T, store *CaseStore, incident *IncidentCase, _ *DeploymentObservation) {
			incident.CycleNumber++
			if _, err := store.db.ExecContext(context.Background(), `UPDATE incident_cases SET cycle_number=? WHERE id=?`, incident.CycleNumber, incident.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, incident, _, observation := prepareRegressionCase(t, 1)
			test.mutate(t, store, &incident, &observation)
			if test.name != "unverified" {
				expectedJSON, _ := json.Marshal(observation.ExpectedCommits)
				observedJSON, _ := json.Marshal(observation.ObservedCommits)
				if _, err := store.db.ExecContext(context.Background(), `UPDATE deployment_observations SET environment=?,expected_commits_json=?,observed_commits_json=? WHERE id=?`, observation.Environment, string(expectedJSON), string(observedJSON), observation.ID); err != nil {
					t.Fatal(err)
				}
			}
			o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
			if _, err := o.StartRegression(context.Background(), incident.ID, incident.Version); err == nil {
				t.Fatal("started regression with invalid deployment binding")
			}
		})
	}
}

func TestRegressionFixedVerifiedRequiresFreshCurrentAttemptEvidence(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	output := regressionOutput(t, attempt, "fixed_verified", "")
	cmd := CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-fixed", ActorID: "validator", Outcome: PhaseOutcomeFixedVerified, OutputJSON: output}
	failedButFixed := cmd
	failedButFixed.IdempotencyKey = "regression-failed-but-fixed"
	failedButFixed.ErrorCode = "agent_process_failed"
	if _, err := o.CompleteAttempt(context.Background(), failedButFixed); err == nil {
		t.Fatal("failed regression command bypassed fresh evidence with fixed_verified outcome")
	}
	if _, err := o.CompleteAttempt(context.Background(), cmd); !errors.Is(err, ErrRegressionFreshEvidence) {
		t.Fatalf("old evidence alone closed Case: %v", err)
	}
	recordRegressionArtifact(t, store, attempt, "request-new", time.Now().UTC().Add(time.Second))
	closed, err := o.CompleteAttempt(context.Background(), cmd)
	if err != nil || closed.Status != CaseFixedVerified || closed.ClosedAt == nil || closed.CycleNumber != 1 {
		t.Fatalf("closed=%+v err=%v", closed, err)
	}
}

func TestRegressionStillReproducesCreatesNextCycleInvestigationWithDelta(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 2)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: "/workspace"}, nil
	}))
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	recordRegressionArtifact(t, store, attempt, "request-still", time.Now().UTC().Add(time.Second))
	current, _ := store.GetCase(context.Background(), incident.ID)
	output := regressionOutput(t, attempt, "still_reproduces", "original timeout remains after deployed fix")
	nextCase, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-still", ActorID: "validator", Outcome: PhaseOutcomeStillReproduces, OutputJSON: output})
	if err != nil || nextCase.Status != CaseInvestigating || nextCase.CycleNumber != 3 {
		t.Fatalf("case=%+v err=%v", nextCase, err)
	}
	next, err := store.GetAttempt(context.Background(), nextCase.CurrentAttemptID)
	if err != nil || next.Phase != PhaseInvestigation || next.CycleNumber != 3 || next.ParentAttemptID != attempt.ID {
		t.Fatalf("attempt=%+v err=%v", next, err)
	}
	var input NextCycleInvestigationInput
	if err := json.Unmarshal(next.InputJSON, &input); err != nil || input.PreviousCycle != 2 || !strings.Contains(input.Delta, "timeout") || !strings.Contains(input.Delta, "original timeout remains") || len(input.RegressionEvidenceReferences) != 1 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
}

func TestRegressionInsufficientInfoWaitsWithoutIncrementingCycle(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 4)
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	output := regressionOutput(t, attempt, "insufficient_info", "")
	waiting, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-gaps", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: output})
	if err != nil || waiting.Status != CaseWaitingEvidence || waiting.CycleNumber != 4 {
		t.Fatalf("case=%+v err=%v", waiting, err)
	}
}

func TestRegressionInsufficientInfoContinuationPreservesBinding(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	waiting, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-needs-account", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: regressionOutput(t, attempt, "insufficient_info", "")})
	if err != nil {
		t.Fatal(err)
	}
	bot := BotRef{Key: "validator", Target: "codex", Path: "/workspace"}
	continued, err := o.ContinueWithEvidence(context.Background(), ContinueWithEvidenceCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "regression-account-provided", ActorID: "alice", Phase: PhaseRegression, Bug: Bug{ID: waiting.BugID}, Bot: bot, InputJSON: []byte(`{"account":"fresh"}`)})
	if err != nil || continued.Status != CaseRegressionValidating || continued.CycleNumber != 1 {
		t.Fatalf("case=%+v err=%v", continued, err)
	}
	retry, err := store.GetAttempt(context.Background(), continued.CurrentAttemptID)
	if err != nil || retry.ParentAttemptID != attempt.ID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	var before, after RegressionValidationInput
	if json.Unmarshal(attempt.InputJSON, &before) != nil || json.Unmarshal(retry.InputJSON, &after) != nil || before.OriginalScenarioHash != after.OriginalScenarioHash || before.DeploymentObservationID != after.DeploymentObservationID || before.DeploymentReservationID != after.DeploymentReservationID || string(after.SupplementalEvidence) != `{"account":"fresh"}` {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
	phaseRunner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	prompt, err := phaseRunner.promptForAttempt(retry, Bug{ID: waiting.BugID}, bot)
	if err != nil || !strings.Contains(prompt, `"account":"fresh"`) {
		t.Fatalf("prompt=%q err=%v", prompt, err)
	}
}

func TestRegressionRecoveryAppliesStillReproducesIntentWithoutRerunningValidation(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	initialRunner := &recordingPhaseRunner{}
	first := NewCaseOrchestrator(store, initialRunner, nil, nil)
	attempt, err := first.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	recordRegressionArtifact(t, store, attempt, "request-recovery", time.Now().UTC().Add(time.Second))
	current, _ := store.GetCase(context.Background(), incident.ID)
	command := CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-recovered-intent", ActorID: "validator", Outcome: PhaseOutcomeStillReproduces, OutputJSON: regressionOutput(t, attempt, "still_reproduces", "timeout persists")}
	if err := store.SaveCompletionIntentIfRunning(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	recoveryRunner := &recordingPhaseRunner{}
	restarted := NewCaseOrchestrator(store, recoveryRunner, nil, nil)
	restarted.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: "/workspace"}, nil
	}))
	if err := restarted.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(context.Background(), incident.ID)
	if got.Status != CaseInvestigating || got.CycleNumber != 2 || recoveryRunner.startCount() != 1 {
		t.Fatalf("case=%+v recovery starts=%d", got, recoveryRunner.startCount())
	}
	next, err := store.GetAttempt(context.Background(), got.CurrentAttemptID)
	if err != nil || next.Phase != PhaseInvestigation || next.ParentAttemptID != attempt.ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func startedCaseVersion(t *testing.T, store *CaseStore, caseID string) int64 {
	t.Helper()
	incident, err := store.GetCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	return incident.Version
}

func prepareRegressionCase(t *testing.T, cycle int) (*CaseStore, IncidentCase, PhaseAttempt, DeploymentObservation) {
	t.Helper()
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "regression-case-" + time.Now().Format("150405.000000000"), BugID: "bug-regression", Source: "test", SystemID: "shop", Environment: "test", Status: CaseDeploymentVerified, CycleNumber: cycle, CurrentAttemptID: "fix-current", SelectedBotKey: "validator", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	incident, _ = store.GetCase(ctx, incident.ID)
	now := time.Now().UTC().Add(-time.Minute)
	original := PhaseAttempt{ID: incident.ID + "-validation", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{"reproduction_steps":["open checkout","submit order"],"expected_behavior":"healthy response"}`), OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"healthy response","evidence":[],"gaps":[]}`), StartedAt: now, FinishedAt: &now}
	fix := PhaseAttempt{ID: "fix-current", CaseID: incident.ID, CycleNumber: cycle, Phase: PhaseFix, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now, FinishedAt: &now}
	for _, attempt := range []PhaseAttempt{original, fix} {
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	artifact := EvidenceArtifact{ID: incident.ID + "-original-artifact", CaseID: incident.ID, AttemptID: original.ID, Kind: "api", PathOrReference: "/artifacts/original", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CapturedAt: now, Environment: "test", Version: "before", RequestID: "request-old", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, artifact, nil); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: incident.ID + "-change", CaseID: incident.ID, AttemptID: fix.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeCommit: "merge-1", PushStatus: "pushed"}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	scope := MergeApprovalScope{CycleNumber: cycle, FixAttemptID: fix.ID, CodeChanges: []ApprovedCodeChange{{ID: change.ID, Repo: "repo", FixCommit: "fix-1", TargetBranch: "test"}}}
	if err := store.RecordApproval(ctx, Approval{ID: incident.ID + "-approval", CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: incident.Version, ScopeJSON: mustJSON(scope), FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": "test"}}, incident.ID+"-approval-key"); err != nil {
		t.Fatal(err)
	}
	reservationKey := incident.ID + "-reserve"
	reservation := DeploymentReservation{ReservationID: stableID("deployment-reservation", reservationKey), ReservationKey: reservationKey, CallerIdempotencyKey: "notify", ActorID: "alice", OriginalExpectedVersion: incident.Version, CycleNumber: cycle, Environment: "test", ExpectedCommits: map[string]string{"repo": "merge-1"}, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex", Path: "/workspace"}, VerifierInput: DeploymentVerificationRequest{CaseID: incident.ID, Environment: "test", ExpectedCommits: map[string]string{"repo": "merge-1"}}}
	verified := time.Now().UTC()
	observation := DeploymentObservation{ID: stableID("deployment", reservationKey), CaseID: incident.ID, Environment: "test", ExpectedCommits: map[string]string{"repo": "merge-1"}, VerifiedAt: &verified, ObservedAt: verified, VerificationSource: "manual", ObservedVersion: "build-42", ObservedCommits: map[string]string{"repo": "merge-1"}, Result: DeploymentResultMatched}
	if err := store.RecordDeploymentObservation(ctx, observation, incident.ID+"-observation-key"); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: reservation.ReservationKey, RequestJSON: mustJSON(reservation), Steps: []CaseMutationStep{{To: CaseDeploymentVerified, AuditOnly: true, Event: TransitionEvent{ID: incident.ID + "-reserve-event", EventType: "deployment_verification_reserved", ActorType: "user", ActorID: "alice", PayloadJSON: mustJSON(reservation)}}}})
	if err != nil {
		t.Fatal(err)
	}
	incident, _ = store.GetCase(ctx, incident.ID)
	return store, incident, original, observation
}

func regressionOutput(t *testing.T, attempt PhaseAttempt, status, observed string) []byte {
	t.Helper()
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	return mustJSON(ValidationResult{VerificationStatus: status, Environment: input.TargetEnvironment, ObservedBehavior: observed, ExpectedBehavior: input.ExpectedBehavior, ScenarioHash: input.OriginalScenarioHash, Evidence: []ArtifactReference{}, Gaps: func() []string {
		if status == "insufficient_info" {
			return []string{"fresh request id"}
		}
		return []string{}
	}()})
}

func recordRegressionArtifact(t *testing.T, store *CaseStore, attempt PhaseAttempt, requestID string, capturedAt time.Time) {
	t.Helper()
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	artifact := EvidenceArtifact{ID: attempt.ID + "-artifact", CaseID: attempt.CaseID, AttemptID: attempt.ID, Kind: "api", PathOrReference: "/artifacts/" + attempt.ID, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CapturedAt: capturedAt, Environment: input.TargetEnvironment, Version: input.ObservedDeploymentVersion, RequestID: requestID, RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(context.Background(), artifact, nil); err != nil {
		t.Fatal(err)
	}
}
