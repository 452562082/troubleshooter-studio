package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

func TestOriginalValidationSkipsLegacyAttemptWithoutEvidence(t *testing.T) {
	store, incident, original, _ := prepareRegressionCase(t, 1)
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM evidence_artifacts WHERE attempt_id=?`, original.ID); err != nil {
		t.Fatal(err)
	}
	finishedAt := original.StartedAt.Add(2 * time.Minute)
	replacement := PhaseAttempt{ID: "replacement-validation", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, AgentTarget: original.AgentTarget, BotKey: original.BotKey, InputJSON: []byte(`{"mode":"reproduce","step":"replacement"}`), OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"healthy response","evidence":[{"kind":"api","path":"response.json","environment":"test","redaction_status":"not_required"}],"gaps":[]}`), StartedAt: original.StartedAt.Add(time.Minute), FinishedAt: &finishedAt}
	if err := store.CreateAttempt(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	recordRegressionArtifact(t, store, replacement, "replacement-request", finishedAt)
	selected, _, refs, err := NewCaseOrchestrator(store, nil, nil, nil).originalValidation(context.Background(), incident.ID)
	if err != nil || selected.ID != replacement.ID || len(refs) != 1 {
		t.Fatalf("selected=%+v refs=%v err=%v", selected, refs, err)
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

func TestRegressionStillReproducesCompletionExactReplaySurvivesNextCycle(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	newOrchestrator := func() *CaseOrchestrator {
		o := NewCaseOrchestrator(store, runner, nil, nil)
		o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
			return Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: "/workspace"}, nil
		}))
		return o
	}
	o := newOrchestrator()
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	recordRegressionArtifact(t, store, attempt, "request-replay", time.Now().UTC().Add(time.Second))
	current, _ := store.GetCase(context.Background(), incident.ID)
	command := CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-still-exact", ActorID: "validator", Outcome: PhaseOutcomeStillReproduces, OutputJSON: regressionOutput(t, attempt, "still_reproduces", "timeout remains")}
	completed, err := o.CompleteAttempt(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := newOrchestrator().CompleteAttempt(context.Background(), command)
	if err != nil || replayed != completed {
		t.Fatalf("replayed=%+v completed=%+v err=%v", replayed, completed, err)
	}
	changed := command
	changed.OutputJSON = regressionOutput(t, attempt, "still_reproduces", "different failure")
	if _, err := newOrchestrator().CompleteAttempt(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	attempts, _ := store.ListAttempts(context.Background(), AttemptFilter{CaseID: incident.ID})
	next := 0
	for _, candidate := range attempts {
		if candidate.Phase == PhaseInvestigation && candidate.CycleNumber == 2 {
			next++
		}
	}
	if next != 1 || runner.startCount() != 2 {
		t.Fatalf("next investigations=%d starts=%d attempts=%+v", next, runner.startCount(), attempts)
	}
}

func TestRegressionStillReproducesConcurrentCompletionCreatesOneNextAttempt(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	newOrchestrator := func() *CaseOrchestrator {
		o := NewCaseOrchestrator(store, runner, nil, nil)
		o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
			return Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: "/workspace"}, nil
		}))
		return o
	}
	attempt, err := newOrchestrator().StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	recordRegressionArtifact(t, store, attempt, "request-concurrent", time.Now().UTC().Add(time.Second))
	current, _ := store.GetCase(context.Background(), incident.ID)
	command := CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-still-concurrent", ActorID: "validator", Outcome: PhaseOutcomeStillReproduces, OutputJSON: regressionOutput(t, attempt, "still_reproduces", "timeout remains")}
	const workers = 8
	errs := make(chan error, workers)
	for range workers {
		go func() { _, err := newOrchestrator().CompleteAttempt(context.Background(), command); errs <- err }()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Errorf("completion err=%v", err)
		}
	}
	attempts, _ := store.ListAttempts(context.Background(), AttemptFilter{CaseID: incident.ID})
	next := 0
	for _, candidate := range attempts {
		if candidate.Phase == PhaseInvestigation && candidate.CycleNumber == 2 {
			next++
		}
	}
	if next != 1 {
		t.Fatalf("next investigations=%d attempts=%+v", next, attempts)
	}
}

func TestRegressionEvidenceContinuationExactReplayKeepsImmutableParent(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	original, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	waiting, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: original.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-continuation-wait", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: regressionOutput(t, original, "insufficient_info", "")})
	if err != nil {
		t.Fatal(err)
	}
	command := ContinueWithEvidenceCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "regression-continuation-exact", ActorID: "alice", Phase: PhaseRegression, Bug: Bug{ID: waiting.BugID}, Bot: BotRef{Key: "validator", Target: "codex", Path: "/workspace"}, InputJSON: []byte(`{"account":"fresh"}`)}
	first, err := o.ContinueWithEvidence(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(context.Background(), command)
	if err != nil || replayed != first {
		t.Fatalf("replayed=%+v first=%+v err=%v", replayed, first, err)
	}
	retry, err := store.GetAttempt(context.Background(), first.CurrentAttemptID)
	if err != nil || retry.ParentAttemptID != original.ID || retry.ParentAttemptID == retry.ID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	changed := command
	changed.InputJSON = []byte(`{"account":"different"}`)
	if _, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	attempts, _ := store.ListAttempts(context.Background(), AttemptFilter{CaseID: incident.ID})
	regressions := 0
	for _, candidate := range attempts {
		if candidate.Phase == PhaseRegression {
			regressions++
		}
	}
	if regressions != 2 || runner.startCount() != 2 {
		t.Fatalf("regressions=%d starts=%d", regressions, runner.startCount())
	}
}

func TestRegressionEvidenceContinuationConcurrentReplayCreatesOneRetry(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	original, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	waiting, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: original.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-concurrent-wait", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: regressionOutput(t, original, "insufficient_info", "")})
	if err != nil {
		t.Fatal(err)
	}
	command := ContinueWithEvidenceCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "regression-continuation-concurrent", ActorID: "alice", Phase: PhaseRegression, Bug: Bug{ID: waiting.BugID}, Bot: BotRef{Key: "validator", Target: "codex", Path: "/workspace"}, InputJSON: []byte(`{"account":"fresh"}`)}
	const workers = 8
	errs := make(chan error, workers)
	for range workers {
		go func() {
			_, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(context.Background(), command)
			errs <- err
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Errorf("continuation err=%v", err)
		}
	}
	attempts, _ := store.ListAttempts(context.Background(), AttemptFilter{CaseID: incident.ID})
	regressions := 0
	for _, candidate := range attempts {
		if candidate.Phase == PhaseRegression {
			regressions++
			if candidate.ID != original.ID && candidate.ParentAttemptID != original.ID {
				t.Fatalf("retry parent=%q original=%q", candidate.ParentAttemptID, original.ID)
			}
		}
	}
	if regressions != 2 || runner.startCount() != 2 {
		t.Fatalf("regressions=%d starts=%d", regressions, runner.startCount())
	}
}

func TestRegressionEvidenceContinuationReplayBindsCompleteExecutionContext(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	original, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	waiting, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: original.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-context-wait", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: regressionOutput(t, original, "insufficient_info", "")})
	if err != nil {
		t.Fatal(err)
	}
	command := fullRegressionContinuationCommand(waiting)
	first, err := o.ContinueWithEvidence(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	event, found, err := store.GetEventByIdempotencyKey(context.Background(), command.IdempotencyKey)
	if err != nil || !found || !strings.Contains(string(event.PayloadJSON), "continuation_identity_sha256") {
		t.Fatalf("event=%+v found=%v err=%v", event, found, err)
	}
	for _, secret := range []string{"Bearer continuation-secret", "workspace-secret", "attachment-secret"} {
		if strings.Contains(string(event.PayloadJSON), secret) {
			t.Fatalf("event leaked %q: %s", secret, event.PayloadJSON)
		}
	}

	reordered := command
	reordered.InputJSON = []byte(`{"z":1,"account":"fresh"}`)
	reordered.Bug.ServiceHints = []string{"svc-a", "svc-b"}
	reordered.Bug.APIPaths = []string{"/a", "/z"}
	reordered.Bug.Attachments = []Attachment{command.Bug.Attachments[1], command.Bug.Attachments[0]}
	reordered.Bot.InternalAgents = []BotInternalAgent{command.Bot.InternalAgents[1], command.Bot.InternalAgents[0]}
	reordered.Bot.Envs = []string{"prod", "test"}
	reordered.Bug.CreatedAt = command.Bug.CreatedAt.UTC()
	reordered.Bug.UpdatedAt = command.Bug.UpdatedAt.UTC()
	reordered.Bug.LastContextAt = command.Bug.LastContextAt.UTC()
	replayed, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(context.Background(), reordered)
	if err != nil || replayed != first {
		t.Fatalf("canonical replay=%+v first=%+v err=%v", replayed, first, err)
	}

	tests := []struct {
		name   string
		mutate func(*ContinueWithEvidenceCommand)
	}{
		{"case_id", func(c *ContinueWithEvidenceCommand) { c.CaseID += "-other" }},
		{"expected_version", func(c *ContinueWithEvidenceCommand) { c.ExpectedVersion++ }},
		{"actor", func(c *ContinueWithEvidenceCommand) { c.ActorID = "bob" }},
		{"phase", func(c *ContinueWithEvidenceCommand) { c.Phase = PhaseValidation }},
		{"input", func(c *ContinueWithEvidenceCommand) { c.InputJSON = []byte(`{"account":"other","z":1}`) }},
		{"bug_id", func(c *ContinueWithEvidenceCommand) { c.Bug.ID += "x" }},
		{"bug_source", func(c *ContinueWithEvidenceCommand) { c.Bug.Source += "x" }},
		{"bug_source_id", func(c *ContinueWithEvidenceCommand) { c.Bug.SourceID += "x" }},
		{"bug_platform_id", func(c *ContinueWithEvidenceCommand) { c.Bug.PlatformID += "x" }},
		{"bug_title", func(c *ContinueWithEvidenceCommand) { c.Bug.Title += "x" }},
		{"bug_description", func(c *ContinueWithEvidenceCommand) { c.Bug.Description += "x" }},
		{"bug_steps", func(c *ContinueWithEvidenceCommand) { c.Bug.Steps += "x" }},
		{"bug_expected", func(c *ContinueWithEvidenceCommand) { c.Bug.Expected += "x" }},
		{"bug_actual", func(c *ContinueWithEvidenceCommand) { c.Bug.Actual += "x" }},
		{"bug_status", func(c *ContinueWithEvidenceCommand) { c.Bug.Status += "x" }},
		{"bug_severity", func(c *ContinueWithEvidenceCommand) { c.Bug.Severity += "x" }},
		{"bug_priority", func(c *ContinueWithEvidenceCommand) { c.Bug.Priority += "x" }},
		{"bug_product", func(c *ContinueWithEvidenceCommand) { c.Bug.Product += "x" }},
		{"bug_module", func(c *ContinueWithEvidenceCommand) { c.Bug.Module += "x" }},
		{"bug_type", func(c *ContinueWithEvidenceCommand) { c.Bug.BugType += "x" }},
		{"bug_os", func(c *ContinueWithEvidenceCommand) { c.Bug.OS += "x" }},
		{"bug_browser", func(c *ContinueWithEvidenceCommand) { c.Bug.Browser += "x" }},
		{"bug_keywords", func(c *ContinueWithEvidenceCommand) { c.Bug.Keywords += "x" }},
		{"bug_assignee", func(c *ContinueWithEvidenceCommand) { c.Bug.Assignee += "x" }},
		{"bug_reporter", func(c *ContinueWithEvidenceCommand) { c.Bug.Reporter += "x" }},
		{"bug_created", func(c *ContinueWithEvidenceCommand) { c.Bug.CreatedAt = c.Bug.CreatedAt.Add(time.Second) }},
		{"bug_updated", func(c *ContinueWithEvidenceCommand) { c.Bug.UpdatedAt = c.Bug.UpdatedAt.Add(time.Second) }},
		{"bug_env", func(c *ContinueWithEvidenceCommand) { c.Bug.Env += "x" }},
		{"bug_bot_env", func(c *ContinueWithEvidenceCommand) { c.Bug.BotEnv += "x" }},
		{"bug_system", func(c *ContinueWithEvidenceCommand) { c.Bug.SystemID += "x" }},
		{"bug_frontend_repo", func(c *ContinueWithEvidenceCommand) { c.Bug.FrontendRepo += "x" }},
		{"bug_service_hints", func(c *ContinueWithEvidenceCommand) { c.Bug.ServiceHints[0] += "x" }},
		{"bug_frontend_url", func(c *ContinueWithEvidenceCommand) { c.Bug.FrontendURL += "x" }},
		{"bug_api_paths", func(c *ContinueWithEvidenceCommand) { c.Bug.APIPaths[0] += "x" }},
		{"bug_trace_ids", func(c *ContinueWithEvidenceCommand) { c.Bug.TraceIDs[0] += "x" }},
		{"bug_request_ids", func(c *ContinueWithEvidenceCommand) { c.Bug.RequestIDs[0] += "x" }},
		{"bug_attachments", func(c *ContinueWithEvidenceCommand) { c.Bug.Attachments[0].Name += "x" }},
		{"bug_selected_bot", func(c *ContinueWithEvidenceCommand) { c.Bug.SelectedBotKey += "x" }},
		{"bug_last_context", func(c *ContinueWithEvidenceCommand) { c.Bug.LastContext += "x" }},
		{"bug_last_context_at", func(c *ContinueWithEvidenceCommand) { c.Bug.LastContextAt = c.Bug.LastContextAt.Add(time.Second) }},
		{"bug_raw_preview", func(c *ContinueWithEvidenceCommand) { c.Bug.RawPreview += "x" }},
		{"bot_key", func(c *ContinueWithEvidenceCommand) { c.Bot.Key += "x" }},
		{"bot_system", func(c *ContinueWithEvidenceCommand) { c.Bot.SystemID += "x" }},
		{"bot_target", func(c *ContinueWithEvidenceCommand) { c.Bot.Target += "x" }},
		{"bot_path", func(c *ContinueWithEvidenceCommand) { c.Bot.Path += "x" }},
		{"bot_name", func(c *ContinueWithEvidenceCommand) { c.Bot.Name += "x" }},
		{"bot_agent_id", func(c *ContinueWithEvidenceCommand) { c.Bot.AgentID += "x" }},
		{"bot_role", func(c *ContinueWithEvidenceCommand) { c.Bot.Role += "x" }},
		{"bot_internal_agents", func(c *ContinueWithEvidenceCommand) { c.Bot.InternalAgents[0].Role += "x" }},
		{"bot_env", func(c *ContinueWithEvidenceCommand) { c.Bot.Env += "x" }},
		{"bot_envs", func(c *ContinueWithEvidenceCommand) { c.Bot.Envs[0] += "x" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneContinueWithEvidenceCommand(command)
			test.mutate(&changed)
			if _, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRegressionContinuationIdentityCanonicalizationDoesNotMutateCommand(t *testing.T) {
	command := fullRegressionContinuationCommand(IncidentCase{ID: "case", Version: 2, BugID: "bug"})
	original := cloneContinueWithEvidenceCommand(command)
	first, err := regressionContinuationIdentityDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := regressionContinuationIdentityDigest(command)
	if err != nil || first != second || !reflect.DeepEqual(command, original) {
		t.Fatalf("first=%q second=%q mutated=%v err=%v", first, second, !reflect.DeepEqual(command, original), err)
	}
	changed := cloneContinueWithEvidenceCommand(command)
	changed.IdempotencyKey += "-different"
	different, err := regressionContinuationIdentityDigest(changed)
	if err != nil || different == first {
		t.Fatalf("idempotency digest=%q original=%q err=%v", different, first, err)
	}
}

func TestRegressionFreshEvidenceRejectsHistoricalRequestOrTraceIDs(t *testing.T) {
	for _, field := range []string{"request", "trace"} {
		t.Run(field, func(t *testing.T) {
			store, incident, _, _ := prepareRegressionCase(t, 1)
			now := time.Now().UTC().Add(-time.Minute)
			history := PhaseAttempt{ID: incident.ID + "-history", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now, FinishedAt: &now}
			if err := store.CreateAttempt(context.Background(), history); err != nil {
				t.Fatal(err)
			}
			historicalArtifact := EvidenceArtifact{ID: history.ID + "-artifact", CaseID: incident.ID, AttemptID: history.ID, Kind: "trace", PathOrReference: "/artifacts/history", SHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", CapturedAt: now, Environment: "test", Version: "before", TraceID: "trace-from-investigation", RedactionStatus: RedactionStatusNotRequired}
			if _, _, err := store.recordEvidenceArtifact(context.Background(), historicalArtifact, nil); err != nil {
				t.Fatal(err)
			}
			o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
			attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
			if err != nil {
				t.Fatal(err)
			}
			var input RegressionValidationInput
			if json.Unmarshal(attempt.InputJSON, &input) != nil {
				t.Fatal("input")
			}
			artifact := EvidenceArtifact{ID: attempt.ID + "-copied", CaseID: attempt.CaseID, AttemptID: attempt.ID, Kind: "api", PathOrReference: "/artifacts/copied", SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", CapturedAt: attempt.StartedAt.Add(time.Second), Environment: input.TargetEnvironment, Version: input.ObservedDeploymentVersion, RedactionStatus: RedactionStatusNotRequired}
			if field == "request" {
				artifact.RequestID = "request-old"
			} else {
				artifact.TraceID = "trace-from-investigation"
			}
			if _, _, err := store.recordEvidenceArtifact(context.Background(), artifact, nil); err != nil {
				t.Fatal(err)
			}
			current, _ := store.GetCase(context.Background(), incident.ID)
			cmd := CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-reused-" + field, ActorID: "validator", Outcome: PhaseOutcomeFixedVerified, OutputJSON: regressionOutput(t, attempt, "fixed_verified", "")}
			if _, err := o.CompleteAttempt(context.Background(), cmd); !errors.Is(err, ErrRegressionFreshEvidence) {
				t.Fatalf("reused %s ID err=%v", field, err)
			}
		})
	}
}

func TestRegressionFreshEvidenceAllowsSameNewRequestAcrossCurrentArtifacts(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	var input RegressionValidationInput
	if json.Unmarshal(attempt.InputJSON, &input) != nil {
		t.Fatal("input")
	}
	for index, digest := range []string{"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"} {
		artifact := EvidenceArtifact{ID: fmt.Sprintf("%s-new-%d", attempt.ID, index), CaseID: attempt.CaseID, AttemptID: attempt.ID, Kind: fmt.Sprintf("api-%d", index), PathOrReference: fmt.Sprintf("/artifacts/new-%d", index), SHA256: digest, CapturedAt: attempt.StartedAt.Add(time.Second), Environment: input.TargetEnvironment, Version: input.ObservedDeploymentVersion, RequestID: "request-brand-new", RedactionStatus: RedactionStatusNotRequired}
		if _, _, err := store.recordEvidenceArtifact(context.Background(), artifact, nil); err != nil {
			t.Fatal(err)
		}
	}
	if fresh, err := o.currentRegressionArtifacts(context.Background(), attempt, input); err != nil || len(fresh) != 2 {
		t.Fatalf("fresh=%+v err=%v started=%s", fresh, err, attempt.StartedAt)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	closed, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "regression-same-new-id", ActorID: "validator", Outcome: PhaseOutcomeFixedVerified, OutputJSON: regressionOutput(t, attempt, "fixed_verified", "")})
	if err != nil || closed.Status != CaseFixedVerified {
		t.Fatalf("closed=%+v err=%v", closed, err)
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

func fullRegressionContinuationCommand(incident IncidentCase) ContinueWithEvidenceCommand {
	fixed := time.Date(2026, 7, 12, 8, 0, 0, 123, time.FixedZone("CST", 8*60*60))
	return ContinueWithEvidenceCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "regression-context-exact", ActorID: "alice", Phase: PhaseRegression,
		InputJSON: []byte(`{"account":"fresh","z":1}`),
		Bug:       Bug{ID: incident.BugID, Source: "zentao", SourceID: "source-1", PlatformID: "platform-1", Title: "checkout", Description: "Authorization: Bearer continuation-secret", Steps: "submit", Expected: "ok", Actual: "timeout", Status: "open", Severity: "major", Priority: "high", Product: "shop", Module: "order", BugType: "code", OS: "linux", Browser: "chrome", Keywords: "payment", Assignee: "alice", Reporter: "bob", CreatedAt: fixed, UpdatedAt: fixed.Add(time.Minute), Env: "test", BotEnv: "test", SystemID: "shop", FrontendRepo: "web", ServiceHints: []string{"svc-b", "svc-a"}, FrontendURL: "https://example.test/checkout", APIPaths: []string{"/z", "/a"}, TraceIDs: []string{"trace-b", "trace-a"}, RequestIDs: []string{"request-b", "request-a"}, Attachments: []Attachment{{ID: "b", Name: "attachment-secret-b", Type: "har", LocalPath: "/tmp/workspace-secret-b", RemoteURL: "https://example.test/b"}, {ID: "a", Name: "a", Type: "png", LocalPath: "/tmp/a", RemoteURL: "https://example.test/a"}}, SelectedBotKey: "validator", LastContext: "context", LastContextAt: fixed.Add(2 * time.Minute), RawPreview: "preview"},
		Bot:       BotRef{Key: "validator", SystemID: "shop", Target: "codex", Path: "/tmp/workspace-secret", Name: "validator", AgentID: "agent-1", Role: "validator", InternalAgents: []BotInternalAgent{{ID: "z", Role: "verifier"}, {ID: "a", Role: "browser"}}, Env: "test", Envs: []string{"test", "prod"}},
	}
}

func cloneContinueWithEvidenceCommand(command ContinueWithEvidenceCommand) ContinueWithEvidenceCommand {
	cloned := command
	cloned.InputJSON = CloneRawMessage(command.InputJSON)
	cloned.Bug.ServiceHints = append([]string(nil), command.Bug.ServiceHints...)
	cloned.Bug.APIPaths = append([]string(nil), command.Bug.APIPaths...)
	cloned.Bug.TraceIDs = append([]string(nil), command.Bug.TraceIDs...)
	cloned.Bug.RequestIDs = append([]string(nil), command.Bug.RequestIDs...)
	cloned.Bug.Attachments = append([]Attachment(nil), command.Bug.Attachments...)
	cloned.Bot.InternalAgents = append([]BotInternalAgent(nil), command.Bot.InternalAgents...)
	cloned.Bot.Envs = append([]string(nil), command.Bot.Envs...)
	return cloned
}
