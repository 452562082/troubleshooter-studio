package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func createRunningPhase(t *testing.T, store *CaseStore, id string, from, running CaseStatus, phase Phase, mode AttemptMode, input json.RawMessage) (IncidentCase, PhaseAttempt) {
	t.Helper()
	ctx := context.Background()
	incident := createWorkflowCase(t, store, id, from)
	attempt := PhaseAttempt{ID: id + "-attempt", CaseID: id, CycleNumber: 1, Phase: phase, Mode: mode, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: input, OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	updated, _, err := store.TransitionWithUpdate(ctx, id, incident.Version, running, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(attempt.ID), SelectedBotKey: workflowStringPointer("bot")}, TransitionEvent{ID: id + "-event", IdempotencyKey: id + ":start", EventType: "phase_started", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return updated, attempt
}

func TestRecoverInterruptedReadOnlyPhaseRetriesAtMostOnceAndIsDeterministic(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, old := createRunningPhase(t, store, "recover-validation", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{"mode":"reproduce"}`))
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseValidating || got.CurrentAttemptID == old.ID {
		t.Fatalf("case=%+v", got)
	}
	finished, err := store.GetAttempt(ctx, old.ID)
	if err != nil || finished.Status != AttemptStatusInterrupted {
		t.Fatalf("attempt=%+v err=%v", finished, err)
	}
	if runner.startCount() != 1 {
		t.Fatalf("starts=%d", runner.startCount())
	}
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if runner.startCount() != 1 {
		t.Fatalf("duplicate recovery starts=%d", runner.startCount())
	}
}

func TestRecoverPreparedAttemptAfterCrashBeforeTransition(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "recover-prepared", CasePendingValidation)
	attempt := PhaseAttempt{ID: "recover-prepared-attempt", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseValidating || got.CurrentAttemptID != attempt.ID || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d", got, runner.startCount())
	}
}

func TestRecoverPreparedQueuedAttemptAfterReservationCrash(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "recover-queued", CasePendingValidation)
	attempt := PhaseAttempt{ID: "recover-queued-attempt", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	if got.Status != CaseValidating || got.CurrentAttemptID != attempt.ID || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d", got, runner.startCount())
	}
}

func TestRecoverInterruptedReadOnlyPhaseStopsAfterOneRetry(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, first := createRunningPhase(t, store, "recover-limit", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{}`))
	first.Status = AttemptStatusInterrupted
	if err := store.FinishAttempt(ctx, first); err != nil {
		t.Fatal(err)
	}
	retry := PhaseAttempt{ID: "recover-limit-retry", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), ParentAttemptID: first.ID}
	if err := store.CreateAttempt(ctx, retry); err != nil {
		t.Fatal(err)
	}
	// Model the snapshot a prior recovery committed before Studio stopped again.
	waiting, _, err := store.Transition(ctx, incident.ID, incident.Version, CaseWaitingEvidence, TransitionEvent{ID: "limit-wait", IdempotencyKey: "limit-wait", EventType: "interrupted", ActorType: "studio", ActorID: "recovery", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	incident, _, err = store.TransitionWithUpdate(ctx, waiting.ID, waiting.Version, CaseValidating, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(retry.ID)}, TransitionEvent{ID: "limit-retry", IdempotencyKey: "limit-retry", EventType: "retry", ActorType: "studio", ActorID: "recovery", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseWaitingEvidence || runner.startCount() != 0 {
		t.Fatalf("case=%+v starts=%d", got, runner.startCount())
	}
}

func TestRecoverInterruptedFixInspectsExternalStateAndNeverBlindlyRetries(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, _ := createRunningPhase(t, store, "recover-fix", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{"fix_branch":"fix/bug","workspace":"/repo"}`))
	runner := &recordingPhaseRunner{}
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{{Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}}}, result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}}
	o := NewCaseOrchestrator(store, runner, git, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v", got)
	}
	if runner.startCount() != 0 || len(git.inspections) != 1 {
		t.Fatalf("starts=%d inspections=%d", runner.startCount(), len(git.inspections))
	}
	merged, mergeErr := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: got.ID, ExpectedVersion: got.Version, IdempotencyKey: "recover-fix-merge", ActorID: "alice"})
	if mergeErr != nil || merged.Status != CaseWaitingDeployment {
		t.Fatalf("merge after recovered fix=%+v err=%v", merged, mergeErr)
	}
}

func TestRecoverInterruptedMergeInspectsRemoteBeforeAdvancing(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	request := MergeRequest{CaseID: "recover-merge", FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": "test"}}
	incident := createWorkflowCase(t, store, request.CaseID, CaseWaitingMergeApproval)
	now := time.Now().UTC()
	fixAttempt := PhaseAttempt{ID: "recover-merge-fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
	if err := store.CreateAttempt(ctx, fixAttempt); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCodeChange(ctx, CodeChange{ID: "recover-merge-change", CaseID: incident.ID, AttemptID: fixAttempt.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}); err != nil {
		t.Fatal(err)
	}
	scope, _ := json.Marshal(MergeApprovalScope{CycleNumber: 1, FixAttemptID: fixAttempt.ID, CodeChanges: []ApprovedCodeChange{{ID: "recover-merge-change", Repo: "repo", FixCommit: "fix-1", TargetBranch: "test"}}})
	approval := Approval{ID: "recover-merge-approval", CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: incident.Version, ScopeJSON: scope, FixCommits: request.FixCommits, TargetBranches: request.TargetBranches}
	if err := store.RecordApproval(ctx, approval, "recover-merge-approval"); err != nil {
		t.Fatal(err)
	}
	incident, _, _ = store.TransitionWithUpdate(ctx, incident.ID, incident.Version, CaseMerging, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(fixAttempt.ID)}, TransitionEvent{ID: "recover-merge-start", IdempotencyKey: "recover-merge-start", EventType: "merge_started", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)})
	git := &recordingGitIntegration{inspection: MergeInspection{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: false}}}, result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseWaitingDeployment || len(git.inspections) != 1 {
		t.Fatalf("case=%+v inspections=%d", got, len(git.inspections))
	}
	if git.mergeCalls != 0 || git.resumeCalls != 1 {
		t.Fatalf("merge calls=%d resume calls=%d", git.mergeCalls, git.resumeCalls)
	}
}

func TestRecoverInterruptedRegressionRequiresLatestMatchedDeployment(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, _ := createRunningPhase(t, store, "recover-regression", CaseDeploymentVerified, CaseRegressionValidating, PhaseRegression, AttemptRegression, []byte(`{}`))
	now := time.Now().UTC()
	observation := DeploymentObservation{ID: "obs", CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: map[string]string{"repo": "fix-1"}, UserNotifiedAt: &now, VerifiedAt: &now, VerificationSource: "manual", ObservedCommits: map[string]string{"repo": "fix-1"}, Result: DeploymentResultMatched}
	if err := store.RecordDeploymentObservation(ctx, observation, "obs-key"); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d", got, runner.startCount())
	}
}

func TestRecoverInterruptedReconcilesTerminalCurrentAttempt(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "recover-terminal", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{}`))
	attempt.Status = AttemptStatusFailed
	attempt.OutputJSON = []byte(`{"error":"crash-after-finish"}`)
	if err := store.FinishAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	if got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v", got)
	}
}

func TestRecoverDeploymentUsesPersistedReservationContextAndDoesNotRerunResult(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "recover-deploy-reservation", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	reserveKey := fmt.Sprintf("deployment-reserve:%s:v%d", incident.ID, incident.Version)
	request := DeploymentVerificationRequest{CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: map[string]string{"repo": "merge-1"}, ObservedVersion: "persisted-proof", ObservedCommits: map[string]string{"repo": "merge-1"}}
	reservation := DeploymentReservation{ReservationID: "reservation", ReservationKey: reserveKey, OriginalExpectedVersion: incident.Version, CycleNumber: 1, Environment: incident.Environment, ExpectedCommits: request.ExpectedCommits, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, VerifierInput: request}
	payload := mustJSON(reservation)
	reserved, err := store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: reserveKey, RequestJSON: payload, Steps: []CaseMutationStep{{To: CaseDeploymentUnverified, Event: TransitionEvent{ID: "reserve-event", EventType: "deployment_verification_reserved", ActorType: "user", ActorID: "alice", PayloadJSON: payload}}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMatched, VerifiedAt: &now, ObservedCommits: map[string]string{"repo": "merge-1"}}}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, verifier)
	if err := o.recoverDeploymentVerification(ctx, reserved.Case); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverDeploymentVerification(ctx, reserved.Case); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	if got.Status != CaseRegressionValidating || len(verifier.requests) != 1 || verifier.requests[0].ObservedVersion != "persisted-proof" || runner.startCount() != 1 {
		t.Fatalf("case=%+v requests=%+v starts=%d", got, verifier.requests, runner.startCount())
	}
}
