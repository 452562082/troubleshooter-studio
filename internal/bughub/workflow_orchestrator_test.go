package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordingPhaseRunner struct {
	mu       sync.Mutex
	starts   []PhaseAttempt
	bugs     []Bug
	bots     []BotRef
	startErr error
	cancels  []string
}

type deadlinePhaseRunner struct{ sawDeadline bool }

type ignoringCancelPhaseRunner struct {
	release     chan struct{}
	sawDeadline atomic.Bool
	calls       atomic.Int32
}

func (r *ignoringCancelPhaseRunner) Start(context.Context, PhaseAttempt, Bug, BotRef) error {
	return nil
}
func (r *ignoringCancelPhaseRunner) Cancel(ctx context.Context, _ string) error {
	r.calls.Add(1)
	_, hasDeadline := ctx.Deadline()
	r.sawDeadline.Store(hasDeadline)
	<-r.release
	return nil
}

type saturatedCancelPhaseRunner struct {
	release chan struct{}
	calls   atomic.Int32
	active  atomic.Int32
	maximum atomic.Int32
}

func (r *saturatedCancelPhaseRunner) Start(context.Context, PhaseAttempt, Bug, BotRef) error {
	return nil
}
func (r *saturatedCancelPhaseRunner) Cancel(context.Context, string) error {
	r.calls.Add(1)
	active := r.active.Add(1)
	for {
		maximum := r.maximum.Load()
		if active <= maximum || r.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	<-r.release
	r.active.Add(-1)
	return nil
}

func (r *deadlinePhaseRunner) Start(ctx context.Context, _ PhaseAttempt, _ Bug, _ BotRef) error {
	_, r.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}
func (r *deadlinePhaseRunner) Cancel(context.Context, string) error { return nil }

func TestOrchestratorBlockedRunnerUsesBoundedDetachedContext(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-runner-deadline", CasePendingInvestigation)
	runner := &deadlinePhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	o.scheduleTimeout = 20 * time.Millisecond
	got, err := o.StartCase(context.Background(), StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:deadline", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`)})
	if !errors.Is(err, context.DeadlineExceeded) || !runner.sawDeadline || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v deadline=%v err=%v", got, runner.sawDeadline, err)
	}
}

func TestCreateAndStartCaseCreatesFirstCaseAndReplaysExactly(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	cmd := CreateAndStartCaseCommand{
		CaseID: "case-first", ExpectedVersion: 0, IdempotencyKey: "create:first", ActorID: "alice",
		Bug: Bug{ID: "bug-first", Source: "zentao", SystemID: "base", Env: "test"},
		Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{"mode":"reproduce"}`),
	}
	first, err := o.CreateAndStartCase(ctx, cmd)
	if err != nil || first.Status != CaseInvestigating || first.Version != 2 || first.BugID != cmd.Bug.ID {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := o.CreateAndStartCase(ctx, cmd)
	if err != nil || second != first || runner.startCount() != 1 {
		t.Fatalf("second=%+v starts=%d err=%v", second, runner.startCount(), err)
	}
}

func TestCreateAndStartCaseFallsBackToSelectedBotSystem(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	created, err := o.CreateAndStartCase(ctx, CreateAndStartCaseCommand{
		CaseID: "case-bot-system", ExpectedVersion: 0, IdempotencyKey: "create:bot-system", ActorID: "alice",
		Bug:       Bug{ID: "bug-bot-system", Source: "zentao", Env: "test"},
		Bot:       BotRef{Key: "base|codex", SystemID: "base", Target: "codex", Path: "/workspace/base", Env: "test"},
		InputJSON: []byte(`{"mode":"reproduce"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SystemID != "base" {
		t.Fatalf("created SystemID = %q, want selected Bot system base", created.SystemID)
	}
	persisted, err := store.GetCase(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SystemID != "base" {
		t.Fatalf("persisted SystemID = %q, want selected Bot system base", persisted.SystemID)
	}
}

func TestCreateAndStartCaseContinuesLegacyArchiveAsNewCase(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	archived := IncidentCase{ID: "legacy-case", BugID: "bug-old", Source: "legacy-runs-json", Status: CaseLegacyArchived, CycleNumber: 1}
	if err := store.CreateCase(ctx, archived); err != nil {
		t.Fatal(err)
	}
	archived, _ = store.GetCase(ctx, archived.ID)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	cmd := CreateAndStartCaseCommand{
		CaseID: archived.ID, ExpectedVersion: archived.Version, IdempotencyKey: "continue:legacy", ActorID: "alice",
		Bug: Bug{ID: archived.BugID, Source: "zentao", SystemID: "base", Env: "test"},
		Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`),
	}
	continued, err := o.CreateAndStartCase(ctx, cmd)
	if err != nil || continued.ID == archived.ID || continued.Status != CaseInvestigating || continued.CycleNumber != archived.CycleNumber+1 {
		t.Fatalf("continued=%+v err=%v", continued, err)
	}
	unchanged, err := store.GetCase(ctx, archived.ID)
	if err != nil || unchanged.Status != CaseLegacyArchived || unchanged.Version != archived.Version {
		t.Fatalf("archive=%+v err=%v", unchanged, err)
	}
	replayed, err := o.CreateAndStartCase(ctx, cmd)
	if err != nil || replayed != continued || runner.startCount() != 1 {
		t.Fatalf("replayed=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
}

func TestCreateAndStartCaseRecoversCrashAfterCreationBeforeStart(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	pending := IncidentCase{ID: "case-created-only", BugID: "bug-created-only", Source: "zentao", SystemID: "base", Environment: "test", Status: CasePendingInvestigation, CycleNumber: 1, SelectedBotKey: "base|codex"}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	command := CreateAndStartCaseCommand{
		CaseID: pending.ID, ExpectedVersion: 0, IdempotencyKey: "create:resume", ActorID: "alice",
		Bug: Bug{ID: pending.BugID, Source: pending.Source, SystemID: pending.SystemID, Env: pending.Environment},
		Bot: BotRef{Key: pending.SelectedBotKey, Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`),
	}
	if result, err := store.CreateCaseWithIdentity(ctx, CaseCreation{Case: pending, IdempotencyKey: command.IdempotencyKey, ActorID: command.ActorID, RequestJSON: mustJSON(command)}); err != nil || result.Replay {
		t.Fatalf("creation result=%+v err=%v", result, err)
	}
	started, err := o.CreateAndStartCase(ctx, command)
	if err != nil || started.Status != CaseInvestigating || runner.startCount() != 1 {
		t.Fatalf("started=%+v starts=%d err=%v", started, runner.startCount(), err)
	}
}

func TestCreateAndStartCaseReusesDifferentKeyButRejectsMutatedIdentityAfterCreationCrash(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	pending := IncidentCase{ID: "case-identity", BugID: "bug-identity", Source: "zentao", SystemID: "base", Environment: "test", Status: CasePendingInvestigation, CycleNumber: 1, SelectedBotKey: "base|codex"}
	base := CreateAndStartCaseCommand{CaseID: pending.ID, ExpectedVersion: 0, IdempotencyKey: "create:identity", ActorID: "alice", Bug: Bug{ID: pending.BugID, Source: pending.Source, SystemID: pending.SystemID, Env: pending.Environment}, Bot: BotRef{Key: pending.SelectedBotKey, Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{"mode":"reproduce"}`)}
	if _, err := store.CreateCaseWithIdentity(ctx, CaseCreation{Case: pending, IdempotencyKey: base.IdempotencyKey, ActorID: base.ActorID, RequestJSON: mustJSON(base)}); err != nil {
		t.Fatal(err)
	}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil)
	differentKey := base
	differentKey.IdempotencyKey = "different-key"
	reused, err := o.CreateAndStartCase(ctx, differentKey)
	if err != nil || reused.ID != pending.ID || reused.Status != CasePendingInvestigation {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
	mutations := []CreateAndStartCaseCommand{base, base, base, base}
	mutations[0].ActorID = "bob"
	mutations[1].InputJSON = []byte(`{"mode":"different"}`)
	mutations[2].Bot.Target = "claude-code"
	mutations[3].Bot.Path = "/workspace/other"
	for index, command := range mutations {
		if _, err := o.CreateAndStartCase(ctx, command); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("mutation %d err=%v", index, err)
		}
	}
}

func TestCreateAndStartLegacyRefreshReusesOpenSuccessor(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	archived := IncidentCase{ID: "legacy-refresh", BugID: "bug-refresh", Source: "legacy-runs-json", Status: CaseLegacyArchived, CycleNumber: 1}
	if err := store.CreateCase(ctx, archived); err != nil {
		t.Fatal(err)
	}
	archived, _ = store.GetCase(ctx, archived.ID)
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil)
	base := CreateAndStartCaseCommand{CaseID: archived.ID, ExpectedVersion: archived.Version, IdempotencyKey: "legacy:first", ActorID: "alice", Bug: Bug{ID: archived.BugID, Source: "zentao", SystemID: "base", Env: "test"}, Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`)}
	first, err := o.CreateAndStartCase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	refresh := base
	refresh.IdempotencyKey = "legacy:refresh"
	reused, err := o.CreateAndStartCase(ctx, refresh)
	if err != nil || reused.ID != first.ID {
		t.Fatalf("reused=%+v first=%+v err=%v", reused, first, err)
	}
	cases, err := store.ListCases(ctx)
	if err != nil || len(cases) != 2 {
		t.Fatalf("first=%s cases=%+v err=%v", first.ID, cases, err)
	}
}

func TestCreateAndStartCaseConcurrentExactCommandRunsAgentOnce(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil)
	command := CreateAndStartCaseCommand{CaseID: "case-concurrent-create", ExpectedVersion: 0, IdempotencyKey: "create:concurrent", ActorID: "alice", Bug: Bug{ID: "bug-concurrent", Source: "zentao", SystemID: "base", Env: "test"}, Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`)}
	const workers = 8
	results := make(chan IncidentCase, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := o.CreateAndStartCase(ctx, command)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var first IncidentCase
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if first.ID == "" {
			first = result
		}
		if result != first {
			t.Fatalf("result=%+v first=%+v", result, first)
		}
	}
	if runner.startCount() != 1 {
		t.Fatalf("starts=%d", runner.startCount())
	}
}

func TestCreateAndStartCaseConcurrentDifferentIDsSchedulesOnce(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, nil)
	commands := []CreateAndStartCaseCommand{
		{CaseID: "case-concurrent-a", IdempotencyKey: "create:concurrent-a", ActorID: "alice", Bug: Bug{ID: "bug-concurrent-different", Source: "zentao", SystemID: "base", Env: "test"}, Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`)},
		{CaseID: "case-concurrent-b", IdempotencyKey: "create:concurrent-b", ActorID: "bob", Bug: Bug{ID: "bug-concurrent-different", Source: "zentao", SystemID: "base", Env: "test"}, Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`)},
	}
	ready := make(chan struct{}, len(commands))
	release := make(chan struct{})
	results := make(chan IncidentCase, len(commands))
	errs := make(chan error, len(commands))
	var wg sync.WaitGroup
	for _, command := range commands {
		command := command
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-release
			result, err := o.CreateAndStartCase(ctx, command)
			results <- result
			errs <- err
		}()
	}
	for range commands {
		<-ready
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var returnedID string
	for result := range results {
		if returnedID == "" {
			returnedID = result.ID
		}
		if result.ID != returnedID {
			t.Fatalf("returned case IDs differ: %q and %q", returnedID, result.ID)
		}
	}
	if runner.startCount() != 1 {
		t.Fatalf("starts=%d", runner.startCount())
	}
}

func TestOrchestratorCancelReturnsByDeadlineWhenRunnerIgnoresContext(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "case-cancel-deadline", CasePendingInvestigation, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
	runner := &ignoringCancelPhaseRunner{release: make(chan struct{})}
	t.Cleanup(func() { close(runner.release) })
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	o.cancelTimeout = 20 * time.Millisecond
	started := time.Now()
	got, err := o.CancelAttempt(ctx, CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel:deadline", ActorID: "alice"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || !runner.sawDeadline.Load() || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v elapsed=%s deadline=%v err=%v", got, time.Since(started), runner.sawDeadline.Load(), err)
	}
	events, listErr := store.ListEvents(ctx, incident.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if events[len(events)-1].EventType != "runner_cancel_timed_out" || events[len(events)-1].ActorType != "studio" {
		t.Fatalf("events=%+v", events)
	}
	stored, storedErr := store.GetAttempt(ctx, attempt.ID)
	if storedErr != nil || stored.FinishedAt == nil || stored.FinishedAt.IsZero() {
		t.Fatalf("attempt=%+v err=%v", stored, storedErr)
	}
	replayed, replayErr := o.CancelAttempt(ctx, CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel:deadline", ActorID: "alice"})
	if !errors.Is(replayErr, context.DeadlineExceeded) || replayed.Version != got.Version || runner.calls.Load() != 1 {
		t.Fatalf("replayed=%+v calls=%d err=%v", replayed, runner.calls.Load(), replayErr)
	}
}

func TestOrchestratorCancelExactReplayUsesPersistedFinishedAtAndDoesNotCancelTwice(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "case-cancel-replay", CasePendingInvestigation, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	cmd := CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel:exact-replay", ActorID: "alice"}
	first, err := o.CancelAttempt(ctx, cmd)
	if err != nil || first.Status != CaseWaitingEvidence {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	stored, err := store.GetAttempt(ctx, attempt.ID)
	if err != nil || stored.FinishedAt == nil || stored.FinishedAt.IsZero() {
		t.Fatalf("attempt=%+v err=%v", stored, err)
	}
	second, err := o.CancelAttempt(ctx, cmd)
	if err != nil || second.Version != first.Version || len(runner.cancels) != 1 {
		t.Fatalf("second=%+v cancels=%v err=%v", second, runner.cancels, err)
	}
}

func TestOrchestratorCancelWorkerCapacityBoundsIgnoringDependencies(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &saturatedCancelPhaseRunner{release: make(chan struct{})}
	t.Cleanup(func() { close(runner.release) })
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	o.cancelTimeout = 10 * time.Millisecond
	var saturatedCommand CancelAttemptCommand
	for i := 0; i < cancelWorkerCapacity+2; i++ {
		incident, attempt := createRunningPhase(t, store, fmt.Sprintf("case-cancel-capacity-%d", i), CasePendingInvestigation, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
		command := CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: fmt.Sprintf("cancel:capacity:%d", i), ActorID: "alice"}
		got, err := o.CancelAttempt(ctx, command)
		if got.Status != CaseWaitingEvidence {
			t.Fatalf("case %d=%+v err=%v", i, got, err)
		}
		if i < cancelWorkerCapacity && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("case %d err=%v", i, err)
		}
		if i >= cancelWorkerCapacity && !errors.Is(err, ErrCancelWorkerSaturated) {
			t.Fatalf("case %d saturation err=%v", i, err)
		}
		if i == cancelWorkerCapacity {
			saturatedCommand = command
			events, listErr := store.ListEvents(ctx, incident.ID)
			if listErr != nil || events[len(events)-1].EventType != "runner_cancel_saturated" {
				t.Fatalf("events=%+v err=%v", events, listErr)
			}
		}
	}
	if calls, maximum := runner.calls.Load(), runner.maximum.Load(); calls != cancelWorkerCapacity || maximum > cancelWorkerCapacity {
		t.Fatalf("calls=%d maximum=%d capacity=%d", calls, maximum, cancelWorkerCapacity)
	}
	if _, err := o.CancelAttempt(ctx, saturatedCommand); !errors.Is(err, ErrCancelWorkerSaturated) || runner.calls.Load() != cancelWorkerCapacity {
		t.Fatalf("saturated replay calls=%d err=%v", runner.calls.Load(), err)
	}
	unrelated := createWorkflowCase(t, store, "case-cancel-unrelated", CasePendingInvestigation)
	started, err := o.StartCase(ctx, StartCaseCommand{CaseID: unrelated.ID, ExpectedVersion: unrelated.Version, IdempotencyKey: "start:unrelated", ActorID: "alice", Bug: Bug{ID: unrelated.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{}`)})
	if err != nil || started.Status != CaseInvestigating {
		t.Fatalf("unrelated=%+v err=%v", started, err)
	}
}

func (r *recordingPhaseRunner) Start(_ context.Context, attempt PhaseAttempt, bug Bug, bot BotRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, attempt.Clone())
	r.bugs = append(r.bugs, bug)
	r.bots = append(r.bots, bot)
	return r.startErr
}

func (r *recordingPhaseRunner) LoadFixCheckpoint(context.Context, PhaseAttempt) ([]CodeChange, error) {
	return nil, nil
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
	fixErrors     []error
	fixCalls      int
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
	inspection := g.inspection.Clone()
	if inspection.Repositories == nil {
		inspection.Repositories = map[string]MergeRepositoryResult{}
		for repo, fix := range request.FixCommits {
			head := "head-" + repo
			inspection.Repositories[repo] = MergeRepositoryResult{TargetHead: head, ApprovalKey: MergeApprovalKey(request.CaseID, repo, fix, request.TargetBranches[repo], head)}
		}
	}
	return inspection, g.err
}

func (g *recordingGitIntegration) InspectFix(_ context.Context, request FixInspectionRequest) (FixInspection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inspections = append(g.inspections, MergeRequest{CaseID: request.CaseID})
	g.fixCalls++
	if g.fixCalls <= len(g.fixErrors) {
		return FixInspection{}, g.fixErrors[g.fixCalls-1]
	}
	return g.fixInspection.Clone(), g.err
}

func (g *recordingGitIntegration) ResumePush(_ context.Context, request MergeRequest) (MergeResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merges = append(g.merges, request.Clone())
	g.resumeCalls++
	return g.result.Clone(), g.err
}

func TestInspectFixWithRetryDoesNotDuplicateUnavailableError(t *testing.T) {
	git := &recordingGitIntegration{err: fmt.Errorf("%w: SSH transport failed", ErrFixInspectionUnavailable)}
	orchestrator := NewCaseOrchestrator(newOrchestratorStore(t), &recordingPhaseRunner{}, git)

	_, err := orchestrator.inspectFixWithRetry(context.Background(), FixInspectionRequest{CaseID: "case-1"})
	if !errors.Is(err, ErrFixInspectionUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if count := strings.Count(err.Error(), ErrFixInspectionUnavailable.Error()); count != 1 {
		t.Fatalf("unavailable error repeated %d times: %v", count, err)
	}
	if git.fixCalls != fixInspectionMaxAttempts {
		t.Fatalf("inspection calls=%d", git.fixCalls)
	}
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
	incident := createWorkflowCase(t, store, "case-start", CasePendingInvestigation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	cmd := StartCaseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:case-start:v1",
		Bug: Bug{ID: incident.BugID, Source: incident.Source}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{"mode":"reproduce"}`), ActorID: "alice",
	}
	got, err := o.StartCase(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CaseInvestigating || runner.startCount() != 1 {
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

func TestOrchestratorReproducedCannotAdvanceWithoutRegisteredEvidence(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-reproduced-no-evidence", CasePendingInvestigation)
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil)
	incident, err := o.StartCase(ctx, StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "no-evidence:start", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
	output := []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"success","evidence":[{"kind":"api","path":"response.json","environment":"test","redaction_status":"not_required"}],"gaps":[]}`)
	if _, err := o.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "no-evidence:complete", ActorID: "validator", Outcome: PhaseOutcomeReproduced, OutputJSON: output}); err == nil {
		t.Fatal("reproduced advanced without a registered artifact")
	}
	current, _ := store.GetCase(ctx, incident.ID)
	if current.Status != CaseInvestigating {
		t.Fatalf("status=%s", current.Status)
	}
}

func TestOrchestratorRejectsStaleCommandBeforeScheduling(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-stale", CasePendingInvestigation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
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
	rootAttempt := PhaseAttempt{ID: "investigation-root", CaseID: root.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"db timeout","confidence":"high","evidence":[],"gaps":[]}`)}
	if err := store.CreateAttempt(ctx, rootAttempt); err != nil {
		t.Fatal(err)
	}
	root, _, err := store.TransitionWithUpdate(ctx, root.ID, root.Version, CaseWaitingFixApproval, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(rootAttempt.ID)}, TransitionEvent{ID: "ready", IdempotencyKey: "ready", EventType: "root_cause_ready", ActorType: "agent", ActorID: "agent", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	git := &recordingGitIntegration{result: MergeResult{Pushed: true, MergeCommits: map[string]string{"repo": "merge-1"}, Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-1", Pushed: true}}}, inspection: MergeInspection{MergePushed: true, MergeCommits: map[string]string{"repo": "merge-1"}}}
	o := NewCaseOrchestrator(store, runner, git)

	if _, err := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: root.ID, ExpectedVersion: root.Version, IdempotencyKey: "merge-too-early", ActorID: "alice"}); !errors.Is(err, ErrApprovalNotReady) {
		t.Fatalf("early merge err=%v", err)
	}
	fixing, err := o.ApproveFix(ctx, ApproveFixCommand{CaseID: root.ID, ExpectedVersion: root.Version, IdempotencyKey: StartFixApprovalKey(root.ID, rootAttempt.ID, root.Version), ActorID: "alice", RootCauseAttemptID: rootAttempt.ID, Bug: Bug{ID: root.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"repo":"feature/work"}}`)})
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
	change := CodeChange{ID: "change-repo", CaseID: fixing.ID, AttemptID: fixAttempt.ID, Repo: "repo", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`[{"repo":"repo","commit":"fix-1","command":"go test ./...","result":"passed"}]`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	git.fixInspection = FixInspection{Complete: true, Changes: []CodeChange{change}}
	waiting, err := o.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: fixing.ID, AttemptID: fixAttempt.ID, ExpectedVersion: fixing.Version, IdempotencyKey: "fix-finished", Outcome: PhaseOutcomeFixPushed, OutputJSON: []byte(`{"fix_status":"fixed_pushed","environment":"test","branches":[{"repo":"repo","base_branch":"test","fix_branch":"fix/bug","commit":"fix-1","pushed":true,"target_environment_branch":"test","push_remote":"origin"}],"changes":[{"repo":"repo","summary":"repair bug"}],"tests":[{"repo":"repo","commit":"fix-1","command":"go test ./...","result":"passed"}],"deployment_notice":"deploy repo","risks":[]}`), ActorID: "fixer", CodeChanges: []CodeChange{change}})
	if err != nil || waiting.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", waiting, err)
	}
	mergeCommand := ApproveMergeCommand{CaseID: waiting.ID, ExpectedVersion: waiting.Version, IdempotencyKey: "approve-merge", ActorID: "alice", FixCommits: map[string]string{"repo": "caller-must-not-control"}, TargetBranches: map[string]string{"repo": "production"}, TargetHeads: map[string]string{"repo": "head-repo"}}
	merged, err := o.ApproveMerge(ctx, mergeCommand)
	if err != nil || merged.Status != CaseSubmitted {
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
	if err != nil || replayed.Status != CaseSubmitted || len(git.merges) != 1 {
		t.Fatalf("replayed=%+v merges=%d err=%v", replayed, len(git.merges), err)
	}
}

func TestApproveMergeIntegratesFixIntoConfirmedBaselineThenEnvironment(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-dual-merge", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "dual-fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	attempt := PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{"source_baselines":{"repo":"feature/work"}}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: "dual-change", CaseID: incident.ID, AttemptID: attempt.ID, Repo: "repo", BaseBranch: "feature/work", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	git := &recordingGitIntegration{result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merged-fix", Pushed: true}}}}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git)
	merged, err := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "approve-dual-merge", ActorID: "alice", TargetHeads: map[string]string{"repo": "head-repo"}})
	if err != nil || merged.Status != CaseSubmitted {
		t.Fatalf("case=%+v err=%v", merged, err)
	}
	if len(git.merges) != 2 || git.merges[0].TargetBranches["repo"] != "feature/work" || git.merges[1].TargetBranches["repo"] != "test" {
		t.Fatalf("merge order=%+v, want feature/work then test", git.merges)
	}
	approvals, err := store.ListApprovals(ctx, incident.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	var scope MergeApprovalScope
	if err := json.Unmarshal(approvals[0].ScopeJSON, &scope); err != nil || len(scope.CodeChanges) != 1 {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	approved := scope.CodeChanges[0]
	if approved.BaselineBranch != "feature/work" || approved.BaselineHead != "head-repo" || approved.TargetBranch != "test" || approved.TargetHead != "head-repo" {
		t.Fatalf("approved=%+v", approved)
	}
}

func TestOrchestratorSchedulingFailureRecordsExplicitFailureAfterCommit(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-schedule-fail", CasePendingInvestigation)
	secret := "Cookie: session=runner-secret Authorization: Bearer runner.token password=runner-pass storageState=runner-state"
	runner := &recordingPhaseRunner{startErr: errors.New(secret)}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	got, err := o.StartCase(ctx, StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:fail", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`), ActorID: "alice"})
	if err == nil || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	scheduleErr := err
	events, err := store.ListEvents(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != "phase_schedule_failed" {
		t.Fatalf("events=%+v", events)
	}
	attempt, loadErr := store.GetAttempt(ctx, got.CurrentAttemptID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for name, value := range map[string]string{
		"returned error": scheduleErr.Error(), "attempt message": attempt.ErrorMessage,
		"attempt output": string(attempt.OutputJSON), "failure event": string(events[1].PayloadJSON),
	} {
		for _, forbidden := range []string{secret, "runner-secret", "runner.token", "runner-pass", "runner-state"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("%s exposed %q: %q", name, forbidden, value)
			}
		}
	}
}

func TestPhaseScheduleErrorMessageKeepsSafeActionableCause(t *testing.T) {
	cause := errors.New("prepare locked source-baseline worktrees: lock standalone fix workspace for api: fatal: unable to read tree (abc123)")
	message := phaseScheduleErrorMessage(cause)
	if !strings.Contains(message, "unable to read tree") || !strings.Contains(message, "source-baseline") {
		t.Fatalf("message = %q, want actionable scheduling cause", message)
	}

	secret := errors.New("git fetch failed: Authorization: Bearer runner.secret-token")
	if got := phaseScheduleErrorMessage(secret); got != "阶段 Agent 启动失败，请检查运行环境后重试" {
		t.Fatalf("sensitive message = %q", got)
	}
}

func TestOrchestratorDuplicateKeyWithDifferentPayloadConflicts(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-exact-replay", CasePendingInvestigation)
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
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
	incident := createWorkflowCase(t, store, "case-concurrent", CasePendingInvestigation)
	runner := &recordingPhaseRunner{}
	first := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	second := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
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
	a := createWorkflowCase(t, store, "case-free-a", CasePendingInvestigation)
	b := createWorkflowCase(t, store, "case-free-b", CasePendingInvestigation)
	runner := &concurrentPhaseRunner{entered: make(chan string, 2), release: make(chan struct{})}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
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
	incident := createWorkflowCase(t, store, "case-cancel-context", CasePendingInvestigation)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &concurrentPhaseRunner{entered: make(chan string, 1), release: make(chan struct{}), cancel: cancel}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	got, err := o.StartCase(ctx, StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:cancel-context", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "bot", Target: "codex"}, InputJSON: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) || got.Status != CaseWaitingEvidence {
		t.Fatalf("case=%+v err=%v", got, err)
	}
}

func TestOrchestratorCancelCannotErasePersistedCompletionIntent(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancel-completion-intent", CaseInvestigating)
	attempt := PhaseAttempt{ID: "attempt-cancel-completion-intent", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Mode: "", Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE incident_cases SET current_attempt_id=?,selected_bot_key=? WHERE id=?`, attempt.ID, attempt.BotKey, incident.ID); err != nil {
		t.Fatal(err)
	}
	command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "bot", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"result":"done"}`)}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	o := NewCaseOrchestrator(store, runner, &recordingGitIntegration{})
	_, err := o.CancelAttempt(ctx, CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel-after-intent", ActorID: "alice"})
	if !errors.Is(err, ErrCompletionIntentPending) {
		t.Fatalf("cancel error = %v", err)
	}
	divergent := command
	divergent.Outcome = PhaseOutcomeRootCauseReady
	if _, err := o.CompleteAttempt(ctx, divergent); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("divergent completion error = %v", err)
	}
	stored, _ := store.GetAttempt(ctx, attempt.ID)
	if _, found, parseErr := parseCompletionIntent(stored.OutputJSON); parseErr != nil || !found || stored.Status != AttemptStatusRunning || len(runner.cancels) != 0 {
		t.Fatalf("attempt=%+v found=%v parseErr=%v cancels=%v", stored, found, parseErr, runner.cancels)
	}
}

func TestOrchestratorPersistedIntentWinsConcurrentCancelAndCompletion(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		ctx := context.Background()
		store := newOrchestratorStore(t)
		incident := createWorkflowCase(t, store, fmt.Sprintf("case-intent-race-%d", iteration), CaseInvestigating)
		attempt := PhaseAttempt{ID: fmt.Sprintf("attempt-intent-race-%d", iteration), CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Mode: "", Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		_, _ = store.db.Exec(`UPDATE incident_cases SET current_attempt_id=?,selected_bot_key=? WHERE id=?`, attempt.ID, attempt.BotKey, incident.ID)
		command := CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "bot", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"result":"done"}`)}
		if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
			t.Fatal(err)
		}
		o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{})
		start := make(chan struct{})
		var completeErr, cancelErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, completeErr = o.CompleteAttempt(ctx, command)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, cancelErr = o.CancelAttempt(ctx, CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel-intent-race", ActorID: "alice"})
		}()
		close(start)
		wg.Wait()
		if completeErr != nil {
			t.Fatalf("iteration %d completion error=%v cancel=%v", iteration, completeErr, cancelErr)
		}
		got, _ := store.GetCase(ctx, incident.ID)
		stored, _ := store.GetAttempt(ctx, attempt.ID)
		if got.Status != CaseWaitingEvidence || stored.Status != AttemptStatusFailed || string(stored.OutputJSON) != string(command.OutputJSON) {
			t.Fatalf("iteration %d case=%+v attempt=%+v cancel=%v", iteration, got, stored, cancelErr)
		}
	}
}

func TestOrchestratorCancelVsCompleteBindsOneAttemptOutcome(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "case-command-race", CasePendingInvestigation, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
	first := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{})
	second := NewCaseOrchestrator(store, &recordingPhaseRunner{}, &recordingGitIntegration{})
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := first.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "race:complete", ActorID: "agent", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"winner":"complete"}`)})
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

func TestContinueWithEvidenceReopensMergeAuthorizationGate(t *testing.T) {
	ctx := context.Background()
	t.Run("merge inspection", func(t *testing.T) {
		store := newOrchestratorStore(t)
		incident := IncidentCase{ID: "case-merge-retry", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "merge-retry-fix", Version: 1}
		if err := store.CreateCase(ctx, incident); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		attempt := PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		change := CodeChange{ID: "merge-retry-change", CaseID: incident.ID, AttemptID: attempt.ID, Repo: "repo", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
		git := &recordingGitIntegration{result: MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {Conflict: true}}}}
		o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git)
		conflicted, conflictErr := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "merge:first-approval", ActorID: "alice", TargetHeads: map[string]string{"repo": "head-repo"}})
		if conflictErr == nil || conflicted.Status != CaseMergeConflict {
			t.Fatalf("conflicted=%+v err=%v", conflicted, conflictErr)
		}
		reopened, err := o.ContinueWithEvidence(ctx, ContinueWithEvidenceCommand{CaseID: incident.ID, ExpectedVersion: conflicted.Version, IdempotencyKey: "merge:fresh-inspection", ActorID: "alice", InputJSON: []byte(`{"inspection":"resolved"}`)})
		if err != nil || reopened.Status != CaseWaitingMergeApproval {
			t.Fatalf("reopened=%+v err=%v", reopened, err)
		}
		events, _ := store.ListEvents(ctx, incident.ID)
		if events[len(events)-1].ActorType != "user" || events[len(events)-1].EventType != "merge_reinspection_confirmed" {
			t.Fatalf("events=%+v", events)
		}
		git.mu.Lock()
		git.result = MergeResult{Repositories: map[string]MergeRepositoryResult{"repo": {MergeCommit: "merge-2", Pushed: true}}}
		git.mu.Unlock()
		merged, mergeErr := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: reopened.Version, IdempotencyKey: "merge:second-approval", ActorID: "alice", TargetHeads: map[string]string{"repo": "head-repo"}})
		if mergeErr != nil || merged.Status != CaseSubmitted || git.mergeCalls != 2 {
			t.Fatalf("merged=%+v calls=%d err=%v", merged, git.mergeCalls, mergeErr)
		}
	})
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
		change := CodeChange{ID: "change-" + repo, CaseID: incident.ID, AttemptID: attempt.ID, Repo: repo, BaseBranch: "test", FixBranch: "fix/" + repo, FixCommit: "fix-" + repo, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	git := &recordingGitIntegration{result: MergeResult{Pushed: true, Repositories: map[string]MergeRepositoryResult{"a": {MergeCommit: "merge-a", Pushed: true}, "b": {MergeCommit: "merge-b", Pushed: false}}}}
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git)
	cmd := ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "partial-merge", ActorID: "alice", TargetHeads: map[string]string{"a": "head-a", "b": "head-b"}}
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
	git.inspection = MergeInspection{Repositories: map[string]MergeRepositoryResult{
		"a": {MergeCommit: "merge-a", TargetHead: "head-a", ApprovalKey: MergeApprovalKey(incident.ID, "a", "fix-a", "test", "head-a"), Pushed: true},
		"b": {MergeCommit: "merge-b", TargetHead: "head-b", ApprovalKey: MergeApprovalKey(incident.ID, "b", "fix-b", "test", "head-b"), Pushed: false},
	}}
	git.result = MergeResult{Repositories: map[string]MergeRepositoryResult{"b": {MergeCommit: "merge-b", Pushed: true}}}
	git.mu.Unlock()
	resumed, resumeErr := o.ApproveMerge(ctx, cmd)
	if resumeErr != nil || resumed.Status != CaseSubmitted || git.mergeCalls != 1 || git.resumeCalls != 1 {
		t.Fatalf("resumed=%+v merge=%d resume=%d err=%v", resumed, git.mergeCalls, git.resumeCalls, resumeErr)
	}
	git.mu.Lock()
	resumeRequest := git.merges[len(git.merges)-1].Clone()
	git.mu.Unlock()
	if len(resumeRequest.Changes) != 1 || resumeRequest.Changes[0].Repo != "b" || len(resumeRequest.FixCommits) != 1 || resumeRequest.FixCommits["b"] != "fix-b" {
		t.Fatalf("resume request=%+v", resumeRequest)
	}
}

func workflowStringPointer(value string) *string { return &value }
