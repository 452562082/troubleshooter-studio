package bughub

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recordingPhaseRunner struct {
	mu       sync.Mutex
	starts   []PhaseAttempt
	startErr error
	cancels  []string
}

func (r *recordingPhaseRunner) Start(_ context.Context, attempt PhaseAttempt, _ Bug, _ BotRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, attempt.Clone())
	return r.startErr
}

func (r *recordingPhaseRunner) Cancel(_ context.Context, attemptID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, attemptID)
	return nil
}

func (r *recordingPhaseRunner) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

type recordingGitIntegration struct {
	mu          sync.Mutex
	merges      []MergeRequest
	inspections []MergeRequest
	result      MergeResult
	inspection  MergeInspection
	err         error
}

func (g *recordingGitIntegration) MergeAndPush(_ context.Context, request MergeRequest) (MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges = append(g.merges, request.Clone())
	return g.result.Clone(), g.err
}

func (g *recordingGitIntegration) Inspect(_ context.Context, request MergeRequest) (MergeInspection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inspections = append(g.inspections, request.Clone())
	return g.inspection.Clone(), g.err
}

type recordingDeploymentVerifier struct {
	mu       sync.Mutex
	requests []DeploymentVerificationRequest
	result   DeploymentObservation
	err      error
}

func (v *recordingDeploymentVerifier) Verify(_ context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.requests = append(v.requests, request.Clone())
	return v.result.Clone(), v.err
}

func newOrchestratorStore(t *testing.T) *CaseStore {
	t.Helper()
	store, err := OpenCaseStore(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createWorkflowCase(t *testing.T, store *CaseStore, id string, status CaseStatus) IncidentCase {
	t.Helper()
	incident := IncidentCase{ID: id, BugID: "bug-" + id, Source: "test", SystemID: "shop", Environment: "test", Status: status, CycleNumber: 1, Version: 1}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOrchestratorStartCaseCommitsBeforeSchedulingAndDuplicateDoesNotScheduleTwice(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-start", CasePendingValidation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	cmd := StartCaseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:case-start:v1",
		Bug: Bug{ID: incident.BugID, Source: incident.Source}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{"mode":"reproduce"}`), ActorID: "alice",
	}
	got, err := o.StartCase(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseValidating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d", got, runner.startCount())
	}
	started := runner.starts[0]
	persisted, err := store.GetCase(ctx, incident.ID)
	if err != nil || persisted.CurrentAttemptID != started.ID {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	if _, err := o.StartCase(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if runner.startCount() != 1 {
		t.Fatalf("duplicate scheduled %d starts", runner.startCount())
	}
}

func TestOrchestratorRejectsStaleCommandBeforeScheduling(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-stale", CasePendingValidation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	_, err := o.StartCase(context.Background(), StartCaseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version + 1, IdempotencyKey: "start:stale",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`), ActorID: "alice",
	})
	if !errors.Is(err, ErrCaseVersionConflict) || runner.startCount() != 0 {
		t.Fatalf("err=%v starts=%d", err, runner.startCount())
	}
}

func TestOrchestratorRequiresSeparateScopeBoundFixAndMergeApprovals(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	root := createWorkflowCase(t, store, "case-gates", CaseRootCauseReady)
	rootAttempt := PhaseAttempt{ID: "investigation-root", CaseID: root.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{"root_cause":"db timeout"}`)}
	if err := store.CreateAttempt(ctx, rootAttempt); err != nil {
		t.Fatal(err)
	}
	root, _, err := store.TransitionWithUpdate(ctx, root.ID, root.Version, CaseWaitingFixApproval, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(rootAttempt.ID)}, TransitionEvent{ID: "ready", IdempotencyKey: "ready", EventType: "root_cause_ready", ActorType: "agent", ActorID: "agent", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	git := &recordingGitIntegration{result: MergeResult{Pushed: true, MergeCommits: map[string]string{"repo": "merge-1"}}}
	o := NewCaseOrchestrator(store, runner, git, &recordingDeploymentVerifier{})

	if _, err := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: root.ID, ExpectedVersion: root.Version, IdempotencyKey: "merge-too-early", ActorID: "alice"}); !errors.Is(err, ErrApprovalNotReady) {
		t.Fatalf("early merge err=%v", err)
	}
	fixing, err := o.ApproveFix(ctx, ApproveFixCommand{CaseID: root.ID, ExpectedVersion: root.Version, IdempotencyKey: "approve-fix", ActorID: "alice", RootCauseAttemptID: rootAttempt.ID, Bug: Bug{ID: root.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)})
	if err != nil || fixing.Status != CaseFixing {
		t.Fatalf("case=%+v err=%v", fixing, err)
	}
	approvals, listErr := store.ListApprovals(ctx, root.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(approvals) != 1 {
		t.Fatal("fix approval was not recorded exactly once")
	}

	fixAttempt, err := store.GetAttempt(ctx, fixing.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := o.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: fixing.ID, AttemptID: fixAttempt.ID, ExpectedVersion: fixing.Version, IdempotencyKey: "fix-finished", Outcome: PhaseOutcomeFixPushed, OutputJSON: []byte(`{"fix_commit":"fix-1"}`), ActorID: "fixer"})
	if err != nil || waiting.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", waiting, err)
	}
	mergeCommand := ApproveMergeCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "approve-merge", ActorID: "alice", FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": "test"}}
	merged, err := o.ApproveMerge(ctx, mergeCommand)
	if err != nil || merged.Status != CaseWaitingDeployment {
		t.Fatalf("case=%+v err=%v", merged, err)
	}
	approvals, listErr = store.ListApprovals(ctx, root.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(approvals) != 2 {
		t.Fatal("fix and merge approvals were not separate")
	}
	replayed, err := o.ApproveMerge(ctx, mergeCommand)
	if err != nil || replayed.Status != CaseWaitingDeployment || len(git.merges) != 1 {
		t.Fatalf("replayed=%+v merges=%d err=%v", replayed, len(git.merges), err)
	}
}

func TestOrchestratorSchedulingFailureRecordsExplicitFailureAfterCommit(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-schedule-fail", CasePendingValidation)
	runner := &recordingPhaseRunner{startErr: errors.New("runner unavailable")}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	got, err := o.StartCase(ctx, StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:fail", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`), ActorID: "alice"})
	if err == nil || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	events, err := store.ListEvents(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != "phase_schedule_failed" {
		t.Fatalf("events=%+v", events)
	}
}

func TestOrchestratorDuplicateKeyWithDifferentPayloadConflicts(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-exact-replay", CasePendingValidation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	cmd := StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:exact", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{"value":1}`), ActorID: "alice"}
	if _, err := o.StartCase(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.InputJSON = []byte(`{"value":2}`)
	if _, err := o.StartCase(ctx, cmd); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
	if runner.startCount() != 1 {
		t.Fatalf("starts=%d", runner.startCount())
	}
}

func TestOrchestratorConcurrentDuplicateAcrossInstancesSchedulesOnce(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-concurrent", CasePendingValidation)
	runner := &recordingPhaseRunner{}
	first := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	second := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	cmd := StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:concurrent", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`), ActorID: "alice"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, orchestrator := range []*CaseOrchestrator{first, second} {
		go func(current *CaseOrchestrator) { <-start; _, err := current.StartCase(ctx, cmd); errs <- err }(orchestrator)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if runner.startCount() != 1 {
		t.Fatalf("starts=%d", runner.startCount())
	}
}

func TestOrchestratorNotifyDeployedCannotReachRegressionWithoutMatchedObservation(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-deployment", CaseWaitingDeployment)
	runner := &recordingPhaseRunner{}
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, verifier)
	got, err := o.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "deploy:old", ActorID: "alice", ExpectedCommits: map[string]string{"repo": "fix-1"}, ObservedVersion: "old"})
	if err != nil || got.Status != CaseDeploymentUnverified {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	if runner.startCount() != 0 {
		t.Fatalf("regression starts=%d", runner.startCount())
	}
	if _, err := o.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "deploy:old", ActorID: "alice", ExpectedCommits: map[string]string{"repo": "fix-1"}, ObservedVersion: "old"}); err != nil {
		t.Fatal(err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("duplicate verifier requests=%d", len(verifier.requests))
	}
}

func TestOrchestratorDuplicateRegressionCompletionKeepsExactClosure(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "case-close", CaseDeploymentVerified, CaseRegressionValidating, PhaseRegression, AttemptRegression, []byte(`{}`))
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	cmd := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "regression:fixed", ActorID: "validator", Outcome: PhaseOutcomeFixedVerified, OutputJSON: []byte(`{"result":"fixed"}`)}
	closed, err := o.CompleteAttempt(ctx, cmd)
	if err != nil || closed.Status != CaseFixedVerified || closed.ClosedAt == nil {
		t.Fatalf("case=%+v err=%v", closed, err)
	}
	replayed, err := o.CompleteAttempt(ctx, cmd)
	if err != nil || replayed.ClosedAt == nil || !replayed.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

type blockingDeploymentVerifier struct {
	entered chan struct{}
	release chan struct{}
}

func (v *blockingDeploymentVerifier) Verify(_ context.Context, _ DeploymentVerificationRequest) (DeploymentObservation, error) {
	v.entered <- struct{}{}
	<-v.release
	return DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}, nil
}

func TestOrchestratorConcurrentDeploymentDuplicateAcrossInstancesSchedulesVerifierOnce(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-deploy-concurrent", CaseWaitingDeployment)
	verifier := &blockingDeploymentVerifier{entered: make(chan struct{}, 2), release: make(chan struct{})}
	first := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, verifier)
	second := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, verifier)
	cmd := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "deploy:concurrent", ActorID: "alice", ExpectedCommits: map[string]string{"repo": "fix-1"}}
	errs := make(chan error, 2)
	go func() { _, err := first.NotifyDeployed(ctx, cmd); errs <- err }()
	<-verifier.entered
	go func() { _, err := second.NotifyDeployed(ctx, cmd); errs <- err }()
	select {
	case <-verifier.entered:
		close(verifier.release)
		t.Fatal("duplicate verifier was scheduled before the first command committed")
	case <-time.After(100 * time.Millisecond):
		close(verifier.release)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-verifier.entered:
		t.Fatal("duplicate verifier was scheduled")
	default:
	}
}

func workflowStringPointer(value string) *string { return &value }
