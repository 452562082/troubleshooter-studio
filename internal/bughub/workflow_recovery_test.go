package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingDurableBrowserRouteRunner struct {
	*recordingPhaseRunner
	assisted  bool
	routeErr  error
	routeRead int
}

func (r *recordingDurableBrowserRouteRunner) BrowserRouteForRecovery(context.Context, PhaseAttempt) (bool, error) {
	r.routeRead++
	return r.assisted, r.routeErr
}

func persistFixCheckpointForTest(t *testing.T, store *CaseStore, root string, attempt PhaseAttempt, result FixResult, state string) string {
	t.Helper()
	staging, err := openAttemptEvidenceStaging(root, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := FixCheckpointManifest{Kind: fixCheckpointManifestKind, Version: fixCheckpointManifestVersion, CaseID: attempt.CaseID, AttemptID: attempt.ID, State: state, Result: result}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(staging.Path(), fixCheckpointManifestName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	locator := fixCheckpointLocator(staging)
	if err := store.SaveFixCheckpoint(context.Background(), FixCheckpoint{AttemptID: attempt.ID, CaseID: attempt.CaseID, StagingLocator: locator}); err != nil {
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	return locator
}

func checkpointFixResult(repo, commit string) FixResult {
	test := FixTestResult{Repo: repo, Commit: commit, Command: "go test ./...", Result: "passed"}
	return FixResult{FixStatus: "fixed_pushed", Environment: "test", Branches: []FixBranchResult{{Repo: repo, BaseBranch: "test", FixBranch: "fix/bug", Commit: commit, Pushed: true, TargetEnvironmentBranch: "test", PushRemote: "origin"}}, Changes: []FixChangeResult{{Repo: repo, Summary: "fix"}}, Tests: []FixTestResult{test}, DeploymentNotice: "deploy", Risks: []string{}, Evidence: []ArtifactReference{}}
}

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

func TestRecoverInterruptedBrowserAttemptReplaysSameAttemptWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-recovery", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	orchestrator.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, got IncidentCase, recovered PhaseAttempt) (Bug, BotRef, error) {
		if got.ID != incident.ID || recovered.ID != attempt.ID {
			t.Fatalf("context incident=%+v attempt=%+v", got, recovered)
		}
		return Bug{ID: incident.BugID, Env: "test", FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, attempt.BotKey, attempt.AgentTarget), nil
	}))
	if err := orchestrator.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	starts := append([]PhaseAttempt(nil), runner.starts...)
	runner.mu.Unlock()
	if len(starts) != 1 || starts[0].ID != attempt.ID {
		t.Fatalf("recovery starts=%+v", starts)
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil || current.CurrentAttemptID != attempt.ID || current.Status != CaseValidating {
		t.Fatalf("case=%+v err=%v", current, err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	if err := orchestrator.RecoverInterrupted(ctx); err != nil || runner.startCount() != 1 {
		t.Fatalf("duplicate recovery starts=%d err=%v", runner.startCount(), err)
	}
	restartedRunner := &recordingPhaseRunner{}
	restarted := NewCaseOrchestrator(store, restartedRunner, nil, nil)
	restarted.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(context.Context, IncidentCase, PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID, Env: "test", FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, attempt.BotKey, attempt.AgentTarget), nil
	}))
	if err := restarted.RecoverInterrupted(ctx); err != nil || restartedRunner.startCount() != 1 {
		t.Fatalf("second-process replay starts=%d err=%v", restartedRunner.startCount(), err)
	}
}

func TestRecoverInterruptedUsesDurableRouteInsteadOfCurrentBugURL(t *testing.T) {
	for _, test := range []struct {
		name       string
		assisted   bool
		currentURL string
	}{
		{name: "browser route survives cleared URL", assisted: true},
		{name: "non browser route survives added URL", assisted: false, currentURL: "https://app.example.com/users"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident, attempt := createRunningPhase(t, store, "recover-durable-route-"+strings.ReplaceAll(test.name, " ", "-"), CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{"mode":"reproduce"}`))
			runner := &recordingDurableBrowserRouteRunner{recordingPhaseRunner: &recordingPhaseRunner{}, assisted: test.assisted}
			orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
			orchestrator.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(context.Context, IncidentCase, PhaseAttempt) (Bug, BotRef, error) {
				return Bug{ID: incident.BugID, FrontendURL: test.currentURL}, installedPhaseRunnerBot(t, attempt.BotKey, attempt.AgentTarget), nil
			}))
			if err := orchestrator.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			runner.mu.Lock()
			starts := append([]PhaseAttempt(nil), runner.starts...)
			runner.mu.Unlock()
			if runner.routeRead != 2 || len(starts) != 1 {
				t.Fatalf("route reads=%d starts=%+v", runner.routeRead, starts)
			}
			if test.assisted && starts[0].ID != attempt.ID {
				t.Fatalf("browser route did not replay the same attempt: %+v", starts)
			}
			if !test.assisted {
				persisted, err := store.GetAttempt(ctx, attempt.ID)
				attempts, listErr := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
				if starts[0].ID == attempt.ID || err != nil || listErr != nil || persisted.Status != AttemptStatusInterrupted || len(attempts) != 2 {
					t.Fatalf("non-browser recovery start=%+v original=%+v attempts=%+v err=%v listErr=%v", starts[0], persisted, attempts, err, listErr)
				}
			}
		})
	}
}

func TestRecoverInterruptedPersistsBrokenBrowserRouteAsSystemFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "missing", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "browser-executions", "primary"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed", setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, browserRouteJournalName), []byte(`{"kind":"wrong"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident, attempt := createRunningPhase(t, store, "recover-invalid-route-"+test.name, CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{"mode":"reproduce"}`))
			root := phaseArtifactsRoot(t)
			staging, err := openOrCreateBrowserAttemptStaging(root, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, staging.Path())
			if err := staging.Close(); err != nil {
				t.Fatal(err)
			}
			executor := &scriptedPhaseExecutor{}
			hostCalls := 0
			runner := NewAgentPhaseRunner(store, executor, nil, root, nil)
			runner.SetBrowserVerifier(browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
				hostCalls++
				return BrowserVerificationResult{}, nil
			}), browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
				return testBrowserApplicationPolicy("https://app.example.com"), nil
			}))
			orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
			orchestrator.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(context.Context, IncidentCase, PhaseAttempt) (Bug, BotRef, error) {
				return Bug{}, BotRef{}, errors.New("recovery context must not mask an invalid durable route")
			}))
			if err := orchestrator.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := store.GetCase(ctx, incident.ID)
			persisted, attemptErr := store.GetAttempt(ctx, attempt.ID)
			events, eventErr := store.ListEvents(ctx, incident.ID)
			if err != nil || attemptErr != nil || eventErr != nil || current.Status != CaseWaitingEvidence || persisted.Status != AttemptStatusFailed || persisted.ErrorCode != "browser_execution_interrupted" {
				t.Fatalf("case=%+v attempt=%+v events=%+v err=%v attemptErr=%v eventErr=%v", current, persisted, events, err, attemptErr, eventErr)
			}
			if len(events) == 0 || events[len(events)-1].EventType != "phase_system_failed" || executor.Calls != 0 || hostCalls != 0 {
				t.Fatalf("events=%+v planner=%d host=%d", events, executor.Calls, hostCalls)
			}
		})
	}
}

func TestBrowserRecoveryReopensOriginalAttemptStaging(t *testing.T) {
	root := phaseArtifactsRoot(t)
	first, err := openOrCreateBrowserAttemptStaging(root, "attempt-browser-staging")
	if err != nil {
		t.Fatal(err)
	}
	path := first.Path()
	if err := os.MkdirAll(filepath.Join(path, "browser-executions", "primary", "browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "browser-executions", "primary", "browser", "reservation.json"), []byte(`{"state":"running"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openOrCreateBrowserAttemptStaging(root, "attempt-browser-staging")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	defer reopened.Cleanup()
	if reopened.Path() != path {
		t.Fatalf("reopened=%q original=%q", reopened.Path(), path)
	}
	if _, err := reopened.Capture("browser-executions/primary/browser/reservation.json"); err != nil {
		t.Fatalf("durable browser journal was not reopened: %v", err)
	}
}

func TestRecoveryIgnoresResetArchiveAndRecoversReplacement(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	old, oldAttempt := prepareResetCase(t, store, "recover-reset-archive")
	reset, err := store.ResetCaseWithReplacement(ctx, resetCommand(old, "recover-reset-replacement", "recover-reset"))
	if err != nil {
		t.Fatal(err)
	}
	// Model a stale runner row observed after reset commit. Terminal Case state is
	// authoritative, so recovery must not mutate or schedule this old attempt.
	if _, err := store.db.Exec(`UPDATE phase_attempts SET status=?,finished_at=NULL WHERE id=?`, AttemptStatusRunning, oldAttempt.ID); err != nil {
		t.Fatal(err)
	}
	replacementAttempt := PhaseAttempt{ID: "recover-reset-replacement-attempt", CaseID: reset.Replacement.ID, CycleNumber: reset.Replacement.CycleNumber, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, replacementAttempt); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	if err := orchestrator.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	archivedAttempt, err := store.GetAttempt(ctx, oldAttempt.ID)
	if err != nil || archivedAttempt.Status != AttemptStatusRunning {
		t.Fatalf("archived attempt=%+v err=%v", archivedAttempt, err)
	}
	replacement, err := store.GetCase(ctx, reset.Replacement.ID)
	if err != nil || replacement.Status != CaseValidating || replacement.CurrentAttemptID != replacementAttempt.ID {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.starts) != 1 || runner.starts[0].ID != replacementAttempt.ID {
		t.Fatalf("starts=%+v", runner.starts)
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

func TestRecoverFixCheckpointUsesRemoteBranchAsCrashTruth(t *testing.T) {
	for _, tc := range []struct {
		name            string
		state           string
		setup           func(*testing.T, gitFixture) string
		want            CaseStatus
		wantUnavailable bool
	}{
		{name: "push then crash before manifest update", state: "prepared", setup: func(t *testing.T, f gitFixture) string { return f.makeFix(t, "pushed\n") }, want: CaseWaitingMergeApproval},
		{name: "local commit before push crash", state: "prepared", setup: func(t *testing.T, f gitFixture) string {
			runGitTest(t, f.repo, "switch", "-c", "fix/bug")
			if err := os.WriteFile(filepath.Join(f.repo, "fix.txt"), []byte("local\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, f.repo, "add", "fix.txt")
			runGitTest(t, f.repo, "commit", "-m", "local fix")
			return strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD"))
		}, want: CaseFixFailed},
		{name: "remote branch drift", state: "pushed", setup: func(t *testing.T, f gitFixture) string {
			commit := f.makeFix(t, "first\n")
			if err := os.WriteFile(filepath.Join(f.repo, "drift.txt"), []byte("drift\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, f.repo, "add", "drift.txt")
			runGitTest(t, f.repo, "commit", "-m", "drift")
			runGitTest(t, f.repo, "push", "origin", "fix/bug")
			return commit
		}, want: CaseFixFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			commit := tc.setup(t, fixture)
			store := newOrchestratorStore(t)
			incident, attempt := createRunningPhase(t, store, "checkpoint-"+strings.ReplaceAll(tc.name, " ", "-"), CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
			root := filepath.Join(resolvedTempDir(t), "checkpoint-artifacts-"+stableID("test", tc.name))
			locator := persistFixCheckpointForTest(t, store, root, attempt, checkpointFixResult("api", commit), tc.state)
			runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil)
			o := NewCaseOrchestrator(store, runner, fixture.service(t), nil)
			recoverErr := o.RecoverInterrupted(context.Background())
			if tc.wantUnavailable {
				if !errors.Is(recoverErr, ErrFixInspectionUnavailable) {
					t.Fatalf("recovery err=%v", recoverErr)
				}
			} else if recoverErr != nil {
				t.Fatal(recoverErr)
			}
			got, _ := store.GetCase(context.Background(), incident.ID)
			if got.Status != tc.want {
				t.Fatalf("case=%+v", got)
			}
			if tc.want == CaseWaitingMergeApproval {
				changes, _ := store.ListCodeChanges(context.Background(), incident.ID)
				if len(changes) != 1 || changes[0].FixCommit != commit || changes[0].PushStatus != "pushed" {
					t.Fatalf("changes=%+v", changes)
				}
				if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); found {
					t.Fatal("checkpoint row not consumed transactionally")
				}
			} else if tc.wantUnavailable {
				persistedAttempt, _ := store.GetAttempt(context.Background(), attempt.ID)
				if persistedAttempt.Status != AttemptStatusRunning {
					t.Fatalf("attempt=%+v", persistedAttempt)
				}
				if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); !found {
					t.Fatal("transient remote failure consumed checkpoint")
				}
			} else if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); found {
				t.Fatal("authoritative mismatch left checkpoint row")
			} else if _, statErr := os.Stat(filepath.Join(root, ".staging", locator)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("terminal checkpoint staging remains: %v", statErr)
			}
		})
	}
}

func TestRecoverFixCheckpointFetchesExactRemoteObjectMissingLocally(t *testing.T) {
	fixture := newGitFixture(t)
	producer := filepath.Join(filepath.Dir(fixture.remote), "producer")
	runGitTest(t, filepath.Dir(fixture.remote), "clone", fixture.remote, producer)
	runGitTest(t, producer, "config", "user.name", "Remote Producer")
	runGitTest(t, producer, "config", "user.email", "producer@example.test")
	runGitTest(t, producer, "switch", "-c", "fix/bug", "origin/test")
	if err := os.WriteFile(filepath.Join(producer, "remote-only.txt"), []byte("remote only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, producer, "add", "remote-only.txt")
	runGitTest(t, producer, "commit", "-m", "remote-only fix")
	commit := strings.TrimSpace(runGitTest(t, producer, "rev-parse", "HEAD"))
	runGitTest(t, producer, "push", "origin", "fix/bug")
	if commandErr := gitRun(context.Background(), fixture.repo, "cat-file", "-e", commit+"^{commit}"); commandErr == nil {
		t.Fatal("fixture local repository unexpectedly already contains remote-only commit")
	}

	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "checkpoint-remote-only", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	root := filepath.Join(resolvedTempDir(t), "checkpoint-remote-only-root")
	persistFixCheckpointForTest(t, store, root, attempt, checkpointFixResult("api", commit), "prepared")
	o := NewCaseOrchestrator(store, NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil), fixture.service(t), nil)
	if err := o.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	if current.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v", current)
	}
	if commandErr := gitRun(context.Background(), fixture.repo, "cat-file", "-e", commit+"^{commit}"); commandErr != nil {
		t.Fatalf("remote exact verifier did not materialize commit: %v", commandErr)
	}
}

func TestRecoverPreparedFixCheckpointRequiresEveryRemoteRepository(t *testing.T) {
	a, b := newGitFixture(t), newGitFixture(t)
	commits := map[string]string{"a": a.makeFix(t, "a\n"), "b": b.makeFix(t, "b\n")}
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "checkpoint-multi", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	result := FixResult{FixStatus: "fixed_pushed", Environment: "test", DeploymentNotice: "deploy", Risks: []string{}, Evidence: []ArtifactReference{}}
	for _, repo := range []string{"a", "b"} {
		result.Branches = append(result.Branches, FixBranchResult{Repo: repo, BaseBranch: "test", FixBranch: "fix/bug", Commit: commits[repo], Pushed: true, TargetEnvironmentBranch: "test", PushRemote: "origin"})
		result.Changes = append(result.Changes, FixChangeResult{Repo: repo, Summary: "fix " + repo})
		result.Tests = append(result.Tests, FixTestResult{Repo: repo, Commit: commits[repo], Command: "go test ./...", Result: "passed"})
	}
	root := filepath.Join(resolvedTempDir(t), "checkpoint-multi-root")
	persistFixCheckpointForTest(t, store, root, attempt, result, "prepared")
	service := NewGitIntegrationService(filepath.Join(resolvedTempDir(t), "checkpoint-worktrees"), func(_ context.Context, _, repo string) (string, error) {
		if repo == "a" {
			return a.repo, nil
		}
		if repo == "b" {
			return b.repo, nil
		}
		return "", errors.New("unknown repo")
	})
	o := NewCaseOrchestrator(store, NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil), service, nil)
	if err := o.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(context.Background(), incident.ID)
	changes, _ := store.ListCodeChanges(context.Background(), incident.ID)
	if got.Status != CaseWaitingMergeApproval || len(changes) != 2 {
		t.Fatalf("case=%+v changes=%+v", got, changes)
	}
}

func TestRecoverFixCheckpointRetriesTransientInspectionWithoutConsumingState(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "checkpoint-transient", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	root := filepath.Join(resolvedTempDir(t), "checkpoint-transient-root")
	persistFixCheckpointForTest(t, store, root, attempt, checkpointFixResult("api", strings.Repeat("a", 40)), "pushed")
	git := &recordingGitIntegration{err: errors.New("temporary ssh outage")}
	o := NewCaseOrchestrator(store, NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil), git, nil)
	if err := o.RecoverInterrupted(context.Background()); !errors.Is(err, ErrFixInspectionUnavailable) {
		t.Fatalf("err=%v", err)
	}
	git.mu.Lock()
	calls := len(git.inspections)
	git.mu.Unlock()
	if calls != fixInspectionMaxAttempts {
		t.Fatalf("inspection calls=%d", calls)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	persistedAttempt, _ := store.GetAttempt(context.Background(), attempt.ID)
	if current.Status != CaseFixing || persistedAttempt.Status != AttemptStatusRunning {
		t.Fatalf("case=%+v attempt=%+v", current, persistedAttempt)
	}
	if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); !found {
		t.Fatal("transient inspection consumed durable checkpoint")
	}
}

func TestRecoverFixCompletionIntentConsumesCheckpointOnAuthoritativeMismatch(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "checkpoint-intent-mismatch", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	result := checkpointFixResult("api", strings.Repeat("a", 40))
	encoded, _ := json.Marshal(result)
	parsed, err := ParsePhaseResult(attempt, encoded)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolvedTempDir(t), "checkpoint-intent-mismatch-root")
	locator := persistFixCheckpointForTest(t, store, root, attempt, result, "pushed")
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "fixer", Outcome: parsed.Outcome, OutputJSON: parsed.OutputJSON, CodeChanges: parsed.CodeChanges}
	if err := store.SaveCompletionIntentIfRunning(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	git := &recordingGitIntegration{err: ErrFixRemoteMismatch}
	o := NewCaseOrchestrator(store, NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil), git, nil)
	if err := o.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetCase(context.Background(), incident.ID)
	if current.Status != CaseFixFailed {
		t.Fatalf("case=%+v", current)
	}
	if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); found {
		t.Fatal("terminal mismatch left checkpoint row")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".staging", locator)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("terminal mismatch left staging: %v", statErr)
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

func TestRecoverVerifiedPreReleaseCaseWithoutOriginalEvidenceFailsSafe(t *testing.T) {
	store, incident, original, _ := prepareRegressionCase(t, 1)
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM evidence_artifacts WHERE attempt_id=?`, original.ID); err != nil {
		t.Fatal(err)
	}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	if err := o.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(context.Background(), incident.ID)
	if err != nil || current.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v err=%v", current, err)
	}
	if current.CurrentAttemptID != original.ID {
		t.Fatalf("continuation attempt=%q want validation %q", current.CurrentAttemptID, original.ID)
	}
	continued, err := o.ContinueWithEvidence(context.Background(), ContinueWithEvidenceCommand{CaseID: current.ID, ExpectedVersion: current.Version, IdempotencyKey: "legacy-original-evidence", ActorID: "alice", Phase: PhaseValidation, Bug: Bug{ID: current.BugID}, Bot: BotRef{Key: current.SelectedBotKey, Target: original.AgentTarget}, InputJSON: []byte(`{"user_input":"fresh reproduction proof"}`)})
	if err != nil || continued.Status != CaseValidating {
		t.Fatalf("continued=%+v err=%v", continued, err)
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
	artifact := EvidenceArtifact{ID: "recover-original", CaseID: incident.ID, AttemptID: attempt.ID, Kind: "api", PathOrReference: "/artifact/recover", SHA256: strings.Repeat("a", 64), CapturedAt: attempt.StartedAt.Add(time.Second), Environment: "test", RequestID: "recover-request", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, artifact, nil); err != nil {
		t.Fatal(err)
	}
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "validator", Outcome: PhaseOutcomeReproduced, OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"success","evidence":[{"kind":"api","path":"response.json","environment":"test","redaction_status":"not_required"}],"gaps":[]}`), Usage: AgentUsage{InputTokens: 3, OutputTokens: 2}}
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

func TestRecoverInterruptedCleansBrowserStagingAfterPersistedIntentCompletes(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "recover-browser-intent-cleanup", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{"mode":"reproduce"}`))
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "validator", Outcome: PhaseOutcomeNotReproduced, OutputJSON: []byte(`{"verification_status":"not_reproduced","environment":"test","evidence":[],"gaps":[]}`)}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatal(err)
	}
	root := phaseArtifactsRoot(t)
	staging, err := openOrCreateBrowserAttemptStaging(root, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	stagingPath := staging.Path()
	if err := os.WriteFile(filepath.Join(stagingPath, "browser-route.json"), []byte(`{"durable":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, func(context.Context, CompleteAttemptCommand) error { return nil })
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	if err := orchestrator.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed intent staging remains: %v", err)
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
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: []CodeChange{change}}}, &recordingDeploymentVerifier{})
	if err := o.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCase(ctx, incident.ID)
	changes, _ := store.ListCodeChanges(ctx, incident.ID)
	if got.Status != CaseWaitingMergeApproval || len(changes) != 1 || changes[0].FixCommit != "deadbeef" || runner.startCount() != 0 {
		t.Fatalf("case=%+v changes=%+v starts=%d", got, changes, runner.startCount())
	}
}
