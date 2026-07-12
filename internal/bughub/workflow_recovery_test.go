package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

func validRecoveredFixChange(repo, commit string) CodeChange {
	tests := mustJSON([]FixTestResult{{Repo: repo, Commit: commit, Command: "test " + repo, Result: "passed"}})
	return CodeChange{Repo: repo, BaseBranch: "test", FixBranch: "fix/" + repo, FixCommit: commit, TestEvidence: tests, TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
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

func TestRecoverQueuedAttemptUsesResolvedWorkspaceContext(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "recover-context", CasePendingValidation)
	attempt := PhaseAttempt{ID: "recover-context-attempt", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, gotCase IncidentCase, gotAttempt PhaseAttempt) (Bug, BotRef, error) {
		if gotCase.ID != incident.ID || gotAttempt.ID != attempt.ID {
			t.Fatalf("resolver case=%s attempt=%s", gotCase.ID, gotAttempt.ID)
		}
		return Bug{ID: incident.BugID, Title: "loaded bug"}, BotRef{Key: "bot", Target: "codex", Path: "/workspace/base", Env: "test"}, nil
	}))

	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runner.bots) != 1 || runner.bots[0].Path != "/workspace/base" || len(runner.bugs) != 1 || runner.bugs[0].Title != "loaded bug" {
		t.Fatalf("bugs=%+v bots=%+v", runner.bugs, runner.bots)
	}
}

func TestRecoverInterruptedPreflightsAllContextsBeforeScheduling(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	createPrepared := func(id string) PhaseAttempt {
		incident := createWorkflowCase(t, store, id, CasePendingValidation)
		attempt := PhaseAttempt{ID: id + "-attempt", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	first := createPrepared("preflight-first")
	second := createPrepared("preflight-second")
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil, nil)
	failSecond := true
	o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		if failSecond && attempt.ID == second.ID {
			return Bug{}, BotRef{}, errors.New("workspace mapping unavailable")
		}
		return Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: "/workspace/" + incident.ID}, nil
	}))
	if err := o.RecoverInterrupted(ctx); err == nil || runner.startCount() != 0 {
		t.Fatalf("first recovery starts=%d err=%v", runner.startCount(), err)
	}
	firstCase, _ := store.GetCase(ctx, first.CaseID)
	secondCase, _ := store.GetCase(ctx, second.CaseID)
	if firstCase.Status != CasePendingValidation || secondCase.Status != CasePendingValidation {
		t.Fatalf("preflight mutated cases first=%+v second=%+v", firstCase, secondCase)
	}
	failSecond = false
	if err := o.RecoverInterrupted(ctx); err != nil || runner.startCount() != 2 {
		t.Fatalf("retry starts=%d err=%v", runner.startCount(), err)
	}
	firstCase, _ = store.GetCase(ctx, first.CaseID)
	startedAttempt, err := store.GetAttempt(ctx, firstCase.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := o.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: firstCase.ID, AttemptID: startedAttempt.ID, ExpectedVersion: firstCase.Version, IdempotencyKey: "complete:preflight-first", ActorID: "agent", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"gaps":["proof"]}`)})
	if err != nil || completed.Status != CaseWaitingEvidence {
		t.Fatalf("completion=%+v err=%v", completed, err)
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
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{validRecoveredFixChange("repo", "fix-1")}}, result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}}
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
	merged, mergeErr := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: got.ID, ExpectedVersion: got.Version, IdempotencyKey: "recover-fix-merge", ActorID: "alice", TargetHeads: map[string]string{"repo": "head-repo"}})
	if mergeErr != nil || merged.Status != CaseWaitingDeployment {
		t.Fatalf("merge after recovered fix=%+v err=%v", merged, mergeErr)
	}
}

func TestRecoverInterruptedFixReplaysPersistedInspectionReservationAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident, attempt := createRunningPhase(t, store, "recover-fix-reopen", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{"fix_branch":"fix/bug","workspace":"/repo"}`))
	first := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := first.reserveInspectionOnly(ctx, incident, attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{validRecoveredFixChange("repo", "fix-1")}}}
	reopened := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if err := reopened.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil || got.Status != CaseWaitingMergeApproval || len(git.inspections) != 1 {
		t.Fatalf("case=%+v inspections=%d err=%v", got, len(git.inspections), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if err := restarted.RecoverInterrupted(ctx); err != nil || len(git.inspections) != 1 {
		t.Fatalf("idempotent recovery inspections=%d err=%v", len(git.inspections), err)
	}
}

func TestRecoverInterruptedFixRejectsInvalidInspectionScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CodeChange)
	}{
		{name: "base target mismatch", mutate: func(change *CodeChange) { change.BaseBranch = "main" }},
		{name: "direct environment fix", mutate: func(change *CodeChange) { change.FixBranch = "test" }},
		{name: "missing tests", mutate: func(change *CodeChange) { change.TestEvidence = []byte(`[]`) }},
		{name: "skip without reason", mutate: func(change *CodeChange) {
			change.TestEvidence = mustJSON([]FixTestResult{{Repo: change.Repo, Commit: change.FixCommit, Command: "go test ./...", Result: "skipped"}})
		}},
		{name: "test repository mismatch", mutate: func(change *CodeChange) {
			change.TestEvidence = mustJSON([]FixTestResult{{Repo: "other", Commit: change.FixCommit, Command: "go test ./...", Result: "passed"}})
		}},
		{name: "test commit mismatch", mutate: func(change *CodeChange) {
			change.TestEvidence = mustJSON([]FixTestResult{{Repo: change.Repo, Commit: "stale", Command: "go test ./...", Result: "passed"}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident, _ := createRunningPhase(t, store, "recover-invalid-"+strings.ReplaceAll(test.name, " ", "-"), CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
			change := validRecoveredFixChange("api", "fix-api")
			test.mutate(&change)
			git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{change}}}
			orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
			if err := orchestrator.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetCase(ctx, incident.ID)
			if err != nil || got.Status != CaseFixFailed {
				t.Fatalf("case=%+v err=%v", got, err)
			}
			attempt, err := store.GetAttempt(ctx, got.CurrentAttemptID)
			if err != nil || attempt.ErrorCode != "fix_recovery_failed" || !strings.Contains(attempt.ErrorMessage, "inspection") {
				t.Fatalf("attempt=%+v err=%v", attempt, err)
			}
			events, err := store.ListEvents(ctx, got.ID)
			if err != nil || events[len(events)-1].EventType != "fix_recovery_failed" || !strings.Contains(string(events[len(events)-1].PayloadJSON), "inspection") {
				t.Fatalf("events=%+v err=%v", events, err)
			}
		})
	}
}

func TestRecoverInterruptedFixPersistsCanonicalMultiRepoResult(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, _ := createRunningPhase(t, store, "recover-fix-multi", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	api := validRecoveredFixChange("api", "fix-api")
	web := validRecoveredFixChange("web", "fix-web")
	web.TestEvidence = mustJSON([]FixTestResult{{Repo: "web", Commit: "fix-web", Command: "npm test", Result: "skipped", SkippedReason: "browser unavailable"}})
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{api, web}}}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if err := orchestrator.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil || got.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	attempt, err := store.GetAttempt(ctx, got.CurrentAttemptID)
	if err != nil || attempt.Status != AttemptStatusSucceeded {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	result, err := ParseFixResult(attempt.OutputJSON)
	if err != nil || result.FixStatus != "fixed_pushed" || len(result.Branches) != 2 || len(result.Changes) != 2 || len(result.Tests) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	changes, err := store.ListCodeChanges(ctx, incident.ID)
	if err != nil || len(changes) != 2 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
}

func TestRecoverInterruptedFixRejectsCrossRepositoryTestEvidence(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, _ := createRunningPhase(t, store, "recover-fix-cross-tests", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	api := validRecoveredFixChange("api", "fix-api")
	web := validRecoveredFixChange("web", "fix-web")
	api.TestEvidence, web.TestEvidence = web.TestEvidence, api.TestEvidence
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{api, web}}}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if err := orchestrator.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(ctx, incident.ID)
	if err != nil || got.Status != CaseFixFailed {
		t.Fatalf("case=%+v err=%v", got, err)
	}
}

func TestRecoverInterruptedMergeInspectsRemoteBeforeAdvancing(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	request := MergeRequest{CaseID: "recover-merge", FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": "test"}, TargetHeads: map[string]string{"repo": "head-repo"}}
	incident := createWorkflowCase(t, store, request.CaseID, CaseWaitingMergeApproval)
	now := time.Now().UTC()
	fixAttempt := PhaseAttempt{ID: "recover-merge-fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
	if err := store.CreateAttempt(ctx, fixAttempt); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCodeChange(ctx, CodeChange{ID: "recover-merge-change", CaseID: incident.ID, AttemptID: fixAttempt.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeBaseHead: "head-repo", PushStatus: "pushed"}); err != nil {
		t.Fatal(err)
	}
	scope, _ := json.Marshal(MergeApprovalScope{CycleNumber: 1, FixAttemptID: fixAttempt.ID, CodeChanges: []ApprovedCodeChange{{ID: "recover-merge-change", Repo: "repo", FixCommit: "fix-1", TargetBranch: "test", TargetHead: "head-repo", ApprovalKey: MergeApprovalKey(incident.ID, "repo", "fix-1", "test", "head-repo")}}})
	approval := Approval{ID: "recover-merge-approval", CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: incident.Version, ScopeJSON: scope, FixCommits: request.FixCommits, TargetBranches: request.TargetBranches}
	if err := store.RecordApproval(ctx, approval, "recover-merge-approval"); err != nil {
		t.Fatal(err)
	}
	incident, _, _ = store.TransitionWithUpdate(ctx, incident.ID, incident.Version, CaseMerging, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(fixAttempt.ID)}, TransitionEvent{ID: "recover-merge-start", IdempotencyKey: "recover-merge-start", EventType: "merge_started", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)})
	git := &recordingGitIntegration{inspection: MergeInspection{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", TargetHead: "head-repo", ApprovalKey: MergeApprovalKey(incident.ID, "repo", "fix-1", "test", "head-repo"), Pushed: false}}}, result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}}
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
	regressionInput := []byte(`{"scenario":"persisted-regression"}`)
	reservation := DeploymentReservation{ReservationID: stableID("deployment-reservation", reserveKey), ReservationKey: reserveKey, CallerIdempotencyKey: "notify-deployed", ActorID: "alice", OriginalExpectedVersion: incident.Version, CycleNumber: 1, Environment: incident.Environment, ExpectedCommits: request.ExpectedCommits, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, VerifierInput: request, RegressionInputJSON: regressionInput}
	payload := mustJSON(reservation)
	reserved, err := store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: reserveKey, RequestJSON: payload, Steps: []CaseMutationStep{{To: CaseDeploymentUnverified, Event: TransitionEvent{ID: "reserve-event", EventType: "deployment_verification_reserved", ActorType: "user", ActorID: "alice", PayloadJSON: payload}}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMatched, VerifiedAt: &now, ObservedVersion: "persisted-proof", ObservedCommits: map[string]string{"repo": "merge-1"}}}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, verifier)
	restarted := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, verifier)
	if err := restarted.recoverDeploymentVerification(ctx, reserved.Case); err != nil {
		t.Fatal(err)
	}
	if err := o.recoverDeploymentVerification(ctx, reserved.Case); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	if got.Status != CaseRegressionValidating || len(verifier.requests) != 1 || verifier.requests[0].ObservedVersion != "persisted-proof" || runner.startCount() != 1 {
		t.Fatalf("case=%+v requests=%+v starts=%d", got, verifier.requests, runner.startCount())
	}
	attempt, attemptErr := store.GetAttempt(ctx, got.CurrentAttemptID)
	var deterministic RegressionValidationInput
	if attemptErr != nil || json.Unmarshal(attempt.InputJSON, &deterministic) != nil || deterministic.ObservedDeploymentVersion != "persisted-proof" || deterministic.OriginalValidationAttemptID == "" {
		t.Fatalf("attempt=%+v err=%v", attempt, attemptErr)
	}
}

func TestRecoverDeploymentRejectsReservationWithoutDurableCallerIdentity(t *testing.T) {
	for name, fixture := range map[string]struct {
		mutate     func(*DeploymentReservation)
		eventActor string
	}{
		"missing caller key":     {mutate: func(r *DeploymentReservation) { r.CallerIdempotencyKey = "" }, eventActor: "alice"},
		"missing payload actor":  {mutate: func(r *DeploymentReservation) { r.ActorID = "" }, eventActor: "alice"},
		"missing event actor":    {mutate: func(*DeploymentReservation) {}, eventActor: ""},
		"wrong nonempty id":      {mutate: func(r *DeploymentReservation) { r.ReservationID = "forged-id" }, eventActor: "alice"},
		"malformed key":          {mutate: func(r *DeploymentReservation) { r.ReservationKey = "wrong-reservation" }, eventActor: "alice"},
		"event payload mismatch": {mutate: func(r *DeploymentReservation) { r.ActorID = "bob" }, eventActor: "alice"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "recover-invalid-"+strings.ReplaceAll(name, " ", "-"), CaseWaitingDeployment)
			incident = addPushedWorkflowChange(t, store, incident)
			reserveKey := fmt.Sprintf("deployment-reserve:%s:v%d", incident.ID, incident.Version)
			request := DeploymentVerificationRequest{CaseID: incident.ID, Environment: incident.Environment, Source: "manual", ExpectedCommits: map[string]string{"repo": "merge-1"}, ObservedVersion: "build", ObservedCommits: map[string]string{"repo": "merge-1"}}
			reservation := DeploymentReservation{ReservationID: stableID("deployment-reservation", reserveKey), ReservationKey: reserveKey, CallerIdempotencyKey: "notify", ActorID: "alice", OriginalExpectedVersion: incident.Version, CycleNumber: incident.CycleNumber, Environment: incident.Environment, ExpectedCommits: request.ExpectedCommits, VerifierInput: request}
			fixture.mutate(&reservation)
			payload := mustJSON(reservation)
			storedActor := fixture.eventActor
			if storedActor == "" {
				storedActor = "alice"
			}
			_, err := store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: reserveKey, RequestJSON: payload, Steps: []CaseMutationStep{{To: CaseDeploymentUnverified, Event: TransitionEvent{ID: stableID("event", reserveKey), EventType: "deployment_verification_reserved", ActorType: "user", ActorID: storedActor, PayloadJSON: payload}}}})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.eventActor == "" {
				if _, err := store.db.ExecContext(ctx, `UPDATE transition_events SET actor_id = '' WHERE idempotency_key = ?`, reserveKey); err != nil {
					t.Fatal(err)
				}
			}
			verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
			runner := &recordingPhaseRunner{}
			orchestrator := NewCaseOrchestrator(store, runner, nil, verifier)
			if err := orchestrator.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := store.GetCase(ctx, incident.ID)
			if err != nil || current.Status != CaseDeploymentUnverified || len(verifier.requests) != 0 || runner.startCount() != 0 {
				t.Fatalf("case=%+v verifies=%d starts=%d err=%v", current, len(verifier.requests), runner.startCount(), err)
			}
			auditKey := reserveKey + ":identity-invalid"
			audit, found, err := store.GetEventByIdempotencyKey(ctx, auditKey)
			if err != nil || !found || audit.EventType != "deployment_reservation_invalid" || audit.ActorType != "studio" {
				t.Fatalf("audit=%+v found=%v err=%v", audit, found, err)
			}
			restarted := NewCaseOrchestrator(store, runner, nil, verifier)
			if err := restarted.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			auditReplay, found, auditErr := store.GetEventByIdempotencyKey(ctx, auditKey)
			if auditErr != nil || !found || auditReplay.ID != audit.ID || len(verifier.requests) != 0 || runner.startCount() != 0 {
				t.Fatalf("recovery replay audit=%+v found=%v verifies=%d starts=%d err=%v", auditReplay, found, len(verifier.requests), runner.startCount(), auditErr)
			}
			reopened, err := orchestrator.ContinueWithEvidence(ctx, ContinueWithEvidenceCommand{CaseID: current.ID, ExpectedVersion: current.Version, IdempotencyKey: "replace-invalid-reservation", ActorID: "alice", InputJSON: []byte(`{"proof":"retry"}`)})
			if err != nil || reopened.Status != CaseWaitingDeployment {
				t.Fatalf("reopen case=%+v err=%v", reopened, err)
			}
			retried, err := orchestrator.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: reopened.ID, ExpectedVersion: reopened.Version, IdempotencyKey: "fresh-notification", ActorID: "alice", ObservedVersion: "fresh", ObservedCommits: map[string]string{"repo": "merge-1"}})
			if err != nil || retried.Status != CaseDeploymentUnverified || len(verifier.requests) != 1 {
				t.Fatalf("retry case=%+v verifies=%d err=%v", retried, len(verifier.requests), err)
			}
		})
	}
}

func TestRecoverInterruptedAppliesPersistedCompletionIntentWithoutRerunningPhase(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "recover-completion-intent", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{"mode":"reproduce"}`))
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "validator", Outcome: PhaseOutcomeReproduced, OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test"}`), Usage: AgentUsage{InputTokens: 3, OutputTokens: 2}}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	if got.Status != CaseInvestigating || got.CurrentAttemptID == attempt.ID {
		t.Fatalf("case = %+v", got)
	}
	finished, _ := store.GetAttempt(ctx, attempt.ID)
	if finished.Status != AttemptStatusSucceeded || string(finished.OutputJSON) != string(command.OutputJSON) || finished.Usage.InputTokens != 3 {
		t.Fatalf("finished = %+v", finished)
	}
	runner.mu.Lock()
	starts := append([]PhaseAttempt(nil), runner.starts...)
	runner.mu.Unlock()
	if len(starts) != 1 || starts[0].Phase != PhaseInvestigation {
		t.Fatalf("recovery reran original phase: %+v", starts)
	}
}

func TestRecoverInterruptedMalformedCompletionIntentFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "recover-malformed-intent", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{}`))
	if _, err := store.db.Exec(`UPDATE phase_attempts SET output_json = ? WHERE id = ?`, `{"kind":"phase_completion_intent","version":1,"command":{}}`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err == nil {
		t.Fatal("malformed completion intent was ignored")
	}
	got, _ := store.GetCase(ctx, incident.ID)
	stored, _ := store.GetAttempt(ctx, attempt.ID)
	if got.Status != CaseValidating || stored.Status != AttemptStatusRunning || runner.startCount() != 0 {
		t.Fatalf("case=%+v attempt=%+v starts=%d", got, stored, runner.startCount())
	}
}

func TestRecoverInterruptedCompletionIntentPreservesFixCodeChanges(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "recover-fix-intent", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{"root_cause":"race"}`))
	change := CodeChange{ID: "change-fix-intent", CaseID: incident.ID, AttemptID: attempt.ID, Repo: "api", BaseBranch: "test", FixBranch: "fix/race", FixCommit: "deadbeef", TestEvidence: []byte(`[{"repo":"api","commit":"deadbeef","command":"go test ./...","result":"passed"}]`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "fixer", Outcome: PhaseOutcomeFixPushed, OutputJSON: []byte(`{"fix_status":"fixed_pushed","environment":"test","branches":[{"repo":"api","base_branch":"test","fix_branch":"fix/race","commit":"deadbeef","pushed":true,"target_environment_branch":"test","push_remote":"origin"}],"changes":[{"repo":"api","summary":"repair race"}],"tests":[{"repo":"api","commit":"deadbeef","command":"go test ./...","result":"passed"}],"deployment_notice":"deploy api","risks":[]}`), CodeChanges: []CodeChange{change}}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	changes, _ := store.ListCodeChanges(ctx, incident.ID)
	if got.Status != CaseWaitingMergeApproval || len(changes) != 1 || changes[0].FixCommit != "deadbeef" || runner.startCount() != 0 {
		t.Fatalf("case=%+v changes=%+v starts=%d", got, changes, runner.startCount())
	}
}
