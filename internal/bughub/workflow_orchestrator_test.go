package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type deadlinePhaseRunner struct{ sawDeadline bool }

func (r *deadlinePhaseRunner) Start(ctx context.Context, _ PhaseAttempt, _ Bug, _ BotRef) error {
	_, r.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}
func (r *deadlinePhaseRunner) Cancel(context.Context, string) error { return nil }

func TestOrchestratorBlockedRunnerUsesBoundedDetachedContext(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-runner-deadline", CasePendingValidation)
	runner := &deadlinePhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	o.scheduleTimeout = 20 * time.Millisecond
	got, err := o.StartCase(context.Background(), StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:deadline", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`)})
	if !errors.Is(err, context.DeadlineExceeded) || !runner.sawDeadline || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v deadline=%v err=%v", got, runner.sawDeadline, err)
	}
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
	mu            sync.Mutex
	merges        []MergeRequest
	inspections   []MergeRequest
	result        MergeResult
	inspection    MergeInspection
	fixInspection FixInspection
	err           error
	mergeCalls    int
	resumeCalls   int
}

func (g *recordingGitIntegration) MergeAndPush(_ context.Context, request MergeRequest) (MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges = append(g.merges, request.Clone())
	g.mergeCalls++
	return g.result.Clone(), g.err
}

func (g *recordingGitIntegration) Inspect(_ context.Context, request MergeRequest) (MergeInspection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inspections = append(g.inspections, request.Clone())
	return g.inspection.Clone(), g.err
}

func (g *recordingGitIntegration) InspectFix(_ context.Context, request FixInspectionRequest) (FixInspection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inspections = append(g.inspections, MergeRequest{CaseID: request.CaseID})
	return g.fixInspection.Clone(), g.err
}

func (g *recordingGitIntegration) ResumePush(_ context.Context, request MergeRequest) (MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges = append(g.merges, request.Clone())
	g.resumeCalls++
	return g.result.Clone(), g.err
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

func addPushedWorkflowChange(t *testing.T, store *CaseStore, incident IncidentCase) IncidentCase {
	t.Helper()
	now := time.Now().UTC()
	attempt := PhaseAttempt{ID: incident.ID + "-fix", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: incident.ID + "-change", CaseID: incident.ID, AttemptID: attempt.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeCommit: "merge-1", PushStatus: "pushed"}
	if err := store.RecordCodeChange(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(context.Background(), CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: incident.ID + ":bind-fix", RequestJSON: []byte(`{}`), Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(attempt.ID)}, Steps: []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: incident.ID + "-bind-fix", EventType: "fix_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	incident = bound.Case
	scope := mustJSON(MergeApprovalScope{CycleNumber: incident.CycleNumber, FixAttemptID: attempt.ID, CodeChanges: []ApprovedCodeChange{{ID: change.ID, Repo: change.Repo, FixCommit: change.FixCommit, TargetBranch: change.TargetEnvironmentBranch}}})
	approval := Approval{ID: incident.ID + "-merge-approval", CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: incident.Version, ScopeJSON: scope, FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": "test"}}
	if err := store.RecordApproval(context.Background(), approval, incident.ID+"-merge-approval-key"); err != nil {
		t.Fatal(err)
	}
	return incident
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
	git := &recordingGitIntegration{result: MergeResult{Pushed: true, MergeCommits: map[string]string{"repo": "merge-1"}, Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}, inspection: MergeInspection{MergePushed: true, MergeCommits: map[string]string{"repo": "merge-1"}}}
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
	waiting, err := o.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: fixing.ID, AttemptID: fixAttempt.ID, ExpectedVersion: fixing.Version, IdempotencyKey: "fix-finished", Outcome: PhaseOutcomeFixPushed, OutputJSON: []byte(`{"fix_commit":"fix-1"}`), ActorID: "fixer", CodeChanges: []CodeChange{{ID: "change-repo", CaseID: fixing.ID, AttemptID: fixAttempt.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}}})
	if err != nil || waiting.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", waiting, err)
	}
	mergeCommand := ApproveMergeCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "approve-merge", ActorID: "alice", FixCommits: map[string]string{"repo": "caller-must-not-control"}, TargetBranches: map[string]string{"repo": "production"}}
	merged, err := o.ApproveMerge(ctx, mergeCommand)
	if err != nil || merged.Status != CaseWaitingDeployment {
		t.Fatalf("case=%+v err=%v", merged, err)
	}
	if len(git.merges) != 1 || git.merges[0].FixCommits["repo"] != "fix-1" || git.merges[0].TargetBranches["repo"] != "test" {
		t.Fatalf("merge request trusted caller maps: %+v", git.merges)
	}
	approvals, listErr = store.ListApprovals(ctx, root.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(approvals) != 2 {
		t.Fatal("fix and merge approvals were not separate")
	}
	var mergeScope MergeApprovalScope
	if err := json.Unmarshal(approvals[1].ScopeJSON, &mergeScope); err != nil || mergeScope.CycleNumber != waiting.CycleNumber || mergeScope.FixAttemptID != fixAttempt.ID || len(mergeScope.CodeChanges) != 1 || mergeScope.CodeChanges[0].ID != "change-repo" {
		t.Fatalf("merge scope=%+v err=%v", mergeScope, err)
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
	incident = addPushedWorkflowChange(t, store, incident)
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
	if len(verifier.requests) != 1 || verifier.requests[0].ExpectedCommits["repo"] != "merge-1" {
		t.Fatalf("verification trusted caller commits: %+v", verifier.requests)
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
	incident = addPushedWorkflowChange(t, store, incident)
	verifier := &blockingDeploymentVerifier{entered: make(chan struct{}, 2), release: make(chan struct{})}
	first := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, verifier)
	second := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, verifier)
	cmd := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "deploy:concurrent", ActorID: "alice", ExpectedCommits: map[string]string{"repo": "fix-1"}}
	errs := make(chan error, 2)
	go func() { _, err := first.NotifyDeployed(ctx, cmd); errs <- err }()
	<-verifier.entered
	secondCmd := cmd
	secondCmd.IdempotencyKey = "deploy:concurrent-other"
	go func() { _, err := second.NotifyDeployed(ctx, secondCmd); errs <- err }()
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

type concurrentPhaseRunner struct {
	entered chan string
	release chan struct{}
	cancel  context.CancelFunc
}

func (r *concurrentPhaseRunner) Start(ctx context.Context, a PhaseAttempt, _ Bug, _ BotRef) error {
	r.entered <- a.CaseID
	if r.cancel != nil {
		r.cancel()
		return context.Canceled
	}
	<-r.release
	return nil
}
func (r *concurrentPhaseRunner) Cancel(context.Context, string) error { return nil }

func TestOrchestratorExternalRunnerDoesNotBlockUnrelatedCase(t *testing.T) {
	store := newOrchestratorStore(t)
	a := createWorkflowCase(t, store, "case-free-a", CasePendingValidation)
	b := createWorkflowCase(t, store, "case-free-b", CasePendingValidation)
	runner := &concurrentPhaseRunner{entered: make(chan string, 2), release: make(chan struct{})}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	errs := make(chan error, 2)
	for _, c := range []IncidentCase{a, b} {
		go func(c IncidentCase) {
			_, err := o.StartCase(context.Background(), StartCaseCommand{CaseID: c.ID, ExpectedVersion: c.Version, IdempotencyKey: "start:" + c.ID, ActorID: "alice", Bug: Bug{ID: c.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`)})
			errs <- err
		}(c)
	}
	<-runner.entered
	<-runner.entered
	close(runner.release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrchestratorCancelledRunnerStillDurablyRecordsFailure(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancel-context", CasePendingValidation)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &concurrentPhaseRunner{entered: make(chan string, 1), release: make(chan struct{}), cancel: cancel}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	got, err := o.StartCase(ctx, StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:cancel-context", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v err=%v", got, err)
	}
}

func TestOrchestratorCancelVsCompleteBindsOneAttemptOutcome(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "case-command-race", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{}`))
	first := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	second := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := first.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "race:complete", ActorID: "agent", Outcome: PhaseOutcomeReproduced, OutputJSON: []byte(`{"winner":"complete"}`)})
		errs <- err
	}()
	go func() {
		<-start
		_, err := second.CancelAttempt(ctx, CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "race:cancel", ActorID: "alice"})
		errs <- err
	}()
	close(start)
	success, failed := 0, 0
	for range 2 {
		if err := <-errs; err == nil {
			success++
		} else if errors.Is(err, ErrCaseVersionConflict) || errors.Is(err, ErrAttemptAlreadyFinished) {
			failed++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("success=%d failed=%d", success, failed)
	}
	stored, _ := store.GetAttempt(ctx, attempt.ID)
	if stored.Status == AttemptStatusSucceeded && string(stored.OutputJSON) != `{"winner":"complete"}` {
		t.Fatalf("attempt=%+v", stored)
	}
}

func TestNotifyDeployedUsesLatestCycleMergeApprovalOnly(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-multicycle", BugID: "bug", Status: CaseWaitingDeployment, CycleNumber: 2, CurrentAttemptID: "fix-cycle-2", Version: 1, Environment: "test"}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	attempts := []PhaseAttempt{{ID: "fix-cycle-1", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}, {ID: "fix-cycle-2", CaseID: incident.ID, CycleNumber: 2, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}}
	for _, attempt := range attempts {
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	changes := []CodeChange{{ID: "change-cycle-1", CaseID: incident.ID, AttemptID: "fix-cycle-1", Repo: "repo", BaseBranch: "main", FixBranch: "fix/one", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeCommit: "merge-1", PushStatus: "pushed"}, {ID: "change-cycle-2", CaseID: incident.ID, AttemptID: "fix-cycle-2", Repo: "repo", BaseBranch: "main", FixBranch: "fix/two", FixCommit: "fix-2", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeCommit: "merge-2", PushStatus: "pushed"}}
	for _, change := range changes {
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	for index, change := range changes {
		cycle := index + 1
		scope := mustJSON(MergeApprovalScope{CycleNumber: cycle, FixAttemptID: change.AttemptID, CodeChanges: []ApprovedCodeChange{{ID: change.ID, Repo: change.Repo, FixCommit: change.FixCommit, TargetBranch: change.TargetEnvironmentBranch}}})
		approval := Approval{ID: fmt.Sprintf("approval-%d", cycle), CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: 1, ScopeJSON: scope, FixCommits: map[string]string{"repo": change.FixCommit}, TargetBranches: map[string]string{"repo": "test"}}
		if err := store.RecordApproval(ctx, approval, fmt.Sprintf("approval-key-%d", cycle)); err != nil {
			t.Fatal(err)
		}
	}
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{}, verifier)
	got, err := o.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "deploy-cycle-2", ActorID: "alice", ExpectedCommits: map[string]string{"repo": "merge-1"}})
	if err != nil || got.Status != CaseDeploymentUnverified {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	if len(verifier.requests) != 1 || verifier.requests[0].ExpectedCommits["repo"] != "merge-2" {
		t.Fatalf("requests=%+v", verifier.requests)
	}
}

func TestNotifyDeployedMatchedReplayReturnsCommittedRegressionWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-deploy-replay", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	now := time.Now().UTC()
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMatched, VerifiedAt: &now, ObservedCommits: map[string]string{"repo": "merge-1"}}}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, verifier)
	cmd := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "deploy-replay", ActorID: "alice", ObservedCommits: map[string]string{"repo": "merge-1"}, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}}
	first, err := o.NotifyDeployed(ctx, cmd)
	if err != nil || first.Status != CaseRegressionValidating {
		t.Fatalf("case=%+v err=%v", first, err)
	}
	second, err := o.NotifyDeployed(ctx, cmd)
	if err != nil || second.Status != CaseRegressionValidating || runner.startCount() != 1 || len(verifier.requests) != 1 {
		t.Fatalf("case=%+v starts=%d verifies=%d err=%v", second, runner.startCount(), len(verifier.requests), err)
	}
}

func TestApproveMergePersistsPartialPerRepoAndGlobalPushedCannotOverride(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-partial-merge", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "partial-fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	attempt := PhaseAttempt{ID: "partial-fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"a", "b"} {
		change := CodeChange{ID: "change-" + repo, CaseID: incident.ID, AttemptID: attempt.ID, Repo: repo, BaseBranch: "main", FixBranch: "fix/" + repo, FixCommit: "fix-" + repo, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	git := &recordingGitIntegration{result: MergeResult{Pushed: true, Repositories: map[string]MergeRepositoryResult{"a": {MergeCommit: "merge-a", Pushed: true}, "b": {MergeCommit: "merge-b", Pushed: false}}}}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	cmd := ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "partial-merge", ActorID: "alice"}
	got, err := o.ApproveMerge(ctx, cmd)
	if err == nil || got.Status != CaseMerging {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	changes, _ := store.ListCodeChanges(ctx, incident.ID)
	statuses := map[string]string{}
	commits := map[string]string{}
	for _, change := range changes {
		statuses[change.Repo] = change.PushStatus
		commits[change.Repo] = change.MergeCommit
	}
	if statuses["a"] != "pushed" || statuses["b"] != "merge_local" || commits["b"] != "merge-b" {
		t.Fatalf("statuses=%v commits=%v", statuses, commits)
	}
	git.mu.Lock()
	git.inspection = MergeInspection{Repositories: map[string]MergeRepositoryResult{"a": {MergeCommit: "merge-a", Pushed: true}, "b": {MergeCommit: "merge-b", Pushed: false}}}
	git.result = MergeResult{Repositories: map[string]MergeRepositoryResult{"a": {MergeCommit: "merge-a", Pushed: true}, "b": {MergeCommit: "merge-b", Pushed: true}}}
	git.mu.Unlock()
	resumed, resumeErr := o.ApproveMerge(ctx, cmd)
	if resumeErr != nil || resumed.Status != CaseWaitingDeployment || git.mergeCalls != 1 || git.resumeCalls != 1 {
		t.Fatalf("resumed=%+v merge=%d resume=%d err=%v", resumed, git.mergeCalls, git.resumeCalls, resumeErr)
	}
}

func workflowStringPointer(value string) *string { return &value }
