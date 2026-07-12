package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type workflowBindingRunner struct {
	mu     sync.Mutex
	starts int
}

func (r *workflowBindingRunner) Start(context.Context, bughub.PhaseAttempt, bughub.Bug, bughub.BotRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	return nil
}

func (*workflowBindingRunner) Cancel(context.Context, string) error { return nil }

func (r *workflowBindingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func newWorkflowBindingApp(t *testing.T, dbPath string) (*App, *bughub.CaseStore, *workflowBindingRunner) {
	t.Helper()
	store, err := bughub.OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	orchestrator := bughub.NewCaseOrchestrator(store, runner, nil, nil)
	app := &App{
		workflowStore:        store,
		workflowOrchestrator: orchestrator,
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, Title: "checkout fails", Env: "test", SystemID: "base"}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			return bughub.BotRef{Key: key, Target: "codex", Path: t.TempDir(), SystemID: "base", Env: "test"}, nil
		},
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	return app, store, runner
}

func createPendingBindingCase(t *testing.T, store *bughub.CaseStore, id string) bughub.IncidentCase {
	t.Helper()
	incident := bughub.IncidentCase{
		ID: id, BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCase(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestListIncidentCasesWorksWithoutWailsContext(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	createPendingBindingCase(t, store, "case-nil-context")

	items, err := app.ListIncidentCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "case-nil-context" {
		t.Fatalf("cases = %+v", items)
	}
}

func TestStartIncidentCaseValidatesScalarsBeforeOpeningRuntime(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{workflowRoot: rootFile}).StartIncidentCase(StartIncidentCaseInput{})
	if err == nil || err.Error() != "case_id is required" {
		t.Fatalf("error = %v", err)
	}
}

func TestStartIncidentCaseRejectsStaleVersionBeforeScheduling(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	incident := createPendingBindingCase(t, store, "case-stale")

	_, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: incident.ID, ExpectedVersion: incident.Version + 1, IdempotencyKey: "start-stale", ActorID: "user-1",
		InputJSON: map[string]any{"mode": "reproduce"},
	})
	if !errors.Is(err, bughub.ErrCaseVersionConflict) {
		t.Fatalf("error = %v", err)
	}
	if runner.count() != 0 {
		t.Fatalf("runner starts = %d", runner.count())
	}
}

func TestStartIncidentCaseCreatesFirstDurableCase(t *testing.T) {
	app, _, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	input := StartIncidentCaseInput{
		CaseID: "case-new", BugID: "bug-1", BotKey: "base|codex", ExpectedVersion: 0,
		IdempotencyKey: "create-new", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"},
	}
	first, err := app.StartIncidentCase(input)
	if err != nil || first.Status != bughub.CaseValidating || first.BugID != input.BugID {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := app.StartIncidentCase(input)
	if err != nil || second != first || runner.count() != 1 {
		t.Fatalf("second=%+v starts=%d err=%v", second, runner.count(), err)
	}
}

func TestStartIncidentCaseContinuesLegacyArchiveAsNewCase(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	archived := bughub.IncidentCase{ID: "case-legacy", BugID: "bug-1", Source: "legacy-runs-json", Status: bughub.CaseLegacyArchived, CycleNumber: 1}
	if err := store.CreateCase(context.Background(), archived); err != nil {
		t.Fatal(err)
	}
	archived, _ = store.GetCase(context.Background(), archived.ID)
	continued, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: archived.ID, BugID: archived.BugID, BotKey: "base|codex", ExpectedVersion: archived.Version,
		IdempotencyKey: "continue-legacy", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err != nil || continued.ID == archived.ID || continued.CycleNumber != 2 || continued.Status != bughub.CaseValidating {
		t.Fatalf("continued=%+v err=%v", continued, err)
	}
	unchanged, _ := store.GetCase(context.Background(), archived.ID)
	if unchanged.Status != bughub.CaseLegacyArchived || unchanged.Version != archived.Version {
		t.Fatalf("archive mutated: %+v", unchanged)
	}
}

func TestStartIncidentCaseDuplicateCommandSchedulesOnce(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	incident := createPendingBindingCase(t, store, "case-duplicate")
	input := StartIncidentCaseInput{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start-once", ActorID: "user-1",
		InputJSON: map[string]any{"mode": "reproduce"},
	}

	first, err := app.StartIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.StartIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != second.Version || first.CurrentAttemptID != second.CurrentAttemptID {
		t.Fatalf("duplicate result diverged: first=%+v second=%+v", first, second)
	}
	if runner.count() != 1 {
		t.Fatalf("runner starts = %d", runner.count())
	}
}

func TestStartIncidentCaseEmitsVersionedSnapshot(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	incident := createPendingBindingCase(t, store, "case-event")
	var eventName string
	var payload IncidentCaseEventPayload
	app.workflowEmit = func(name string, value any) {
		eventName = name
		var ok bool
		payload, ok = value.(IncidentCaseEventPayload)
		if !ok {
			t.Fatalf("payload type = %T", value)
		}
	}

	updated, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start-event", ActorID: "user-1",
		InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventName != incidentCaseEvent || payload.Kind != "snapshot" || payload.Case == nil || payload.Snapshot == nil || payload.Case.Version != updated.Version || payload.Snapshot.Case.ID != incident.ID {
		t.Fatalf("event %q payload=%+v updated=%+v", eventName, payload, updated)
	}
}

func TestIncidentWorkflowStartupErrorEmitsAndCanRetry(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []IncidentCaseEventPayload
	app := &App{workflowRoot: rootFile, workflowEmit: func(name string, value any) {
		if name != incidentCaseEvent {
			t.Fatalf("event name = %s", name)
		}
		events = append(events, value.(IncidentCaseEventPayload))
	}}
	if err := app.startIncidentWorkflow(context.Background()); err == nil {
		t.Fatal("startup unexpectedly succeeded")
	}
	if len(events) != 1 || events[0].Kind != "startup_error" || events[0].Error == nil || !events[0].Error.Retryable {
		t.Fatalf("events = %+v", events)
	}
	root := t.TempDir()
	app.workflowRoot = root
	if err := app.startIncidentWorkflow(context.Background()); err != nil {
		t.Fatalf("retry startup: %v", err)
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	if _, err := os.Stat(filepath.Join(root, "workflows.db")); err != nil {
		t.Fatal(err)
	}
}

func TestIncidentWorkflowRestartReloadsPersistedCase(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workflows.db")
	app, store, _ := newWorkflowBindingApp(t, dbPath)
	createPendingBindingCase(t, store, "case-restart")
	if err := app.closeIncidentWorkflow(); err != nil {
		t.Fatal(err)
	}

	restarted := &App{workflowRoot: root}
	if err := restarted.initializeIncidentWorkflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.closeIncidentWorkflow() })
	got, err := restarted.GetIncidentCase("case-restart")
	if err != nil {
		t.Fatal(err)
	}
	if got.Case.Status != bughub.CasePendingValidation || got.Case.Version != 1 {
		t.Fatalf("reloaded case = %+v", got.Case)
	}
}

func TestIncidentWorkflowStartupMigratesLegacyRunsOnce(t *testing.T) {
	root := t.TempDir()
	legacy := `[{"id":"legacy-run-1","bug_id":"bug-old","status":"succeeded"}]`
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	for restart := 0; restart < 2; restart++ {
		app := &App{workflowRoot: root}
		if err := app.initializeIncidentWorkflow(context.Background()); err != nil {
			t.Fatal(err)
		}
		items, err := app.ListIncidentCases()
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Status != bughub.CaseLegacyArchived {
			t.Fatalf("restart %d cases = %+v", restart, items)
		}
		if err := app.closeIncidentWorkflow(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncidentWorkflowStartupRecoversTerminalCurrentAttempt(t *testing.T) {
	root := t.TempDir()
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	incident := bughub.IncidentCase{
		ID: "case-recover", BugID: "bug-recover", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-recover", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(context.Background(), bughub.PhaseAttempt{
		ID: "attempt-recover", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseValidation,
		Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusFailed, AgentTarget: "codex", BotKey: "base|codex",
		InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: finished.Add(-time.Second), FinishedAt: &finished,
		ErrorCode: "process_failed", ErrorMessage: "interrupted before callback",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	app := &App{workflowRoot: root}
	if err := app.initializeIncidentWorkflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	got, err := app.GetIncidentCase(incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Case.Status != bughub.CaseWaitingEvidence {
		t.Fatalf("recovered status = %s", got.Case.Status)
	}
}

func TestIncidentWorkflowStartupRecoveryLoadsWorkspaceForQueuedAndRunningAttempts(t *testing.T) {
	root := t.TempDir()
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	queuedCase := bughub.IncidentCase{ID: "case-queued-context", BugID: "bug-context", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: "base|codex"}
	if err := store.CreateCase(ctx, queuedCase); err != nil {
		t.Fatal(err)
	}
	queued := bughub.PhaseAttempt{ID: "attempt-queued-context", CaseID: queuedCase.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusQueued, AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, queued); err != nil {
		t.Fatal(err)
	}
	runningCase := bughub.IncidentCase{ID: "case-running-context", BugID: "bug-context", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-running-context", SelectedBotKey: "base|codex"}
	if err := store.CreateCase(ctx, runningCase); err != nil {
		t.Fatal(err)
	}
	running := bughub.PhaseAttempt{ID: runningCase.CurrentAttemptID, CaseID: runningCase.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning, AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner := &workflowContextRunner{}
	app := &App{
		workflowRoot: root,
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, Title: "loaded from bug store", Env: "test", SystemID: "base"}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			return bughub.BotRef{Key: key, Target: "codex", Path: "/installed/base-workspace", Env: "test"}, nil
		},
		workflowRuntimeFactory: func(store *bughub.CaseStore, _ *bughub.InvestigationStore) incidentWorkflowRuntime {
			return incidentWorkflowRuntime{orchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
		},
	}
	if err := app.initializeIncidentWorkflow(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	if len(runner.bots) != 2 {
		t.Fatalf("recovered bots = %+v", runner.bots)
	}
	for _, bot := range runner.bots {
		if bot.Path != "/installed/base-workspace" {
			t.Fatalf("recovered bot = %+v", bot)
		}
	}
}

func TestIncidentWorkflowWailsModelsUseJSONObjectTypes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "wailsjs", "go", "models.ts"))
	if err != nil {
		t.Fatal(err)
	}
	models := string(data)
	for _, forbidden := range []string{
		"input_json: number[]", "input_json?: number[]", "output_json: number[]", "scope_json: number[]",
		"payload_json: number[]", "test_evidence: number[]",
	} {
		if strings.Contains(models, forbidden) {
			t.Fatalf("generated Wails model contains %q", forbidden)
		}
	}
	for _, required := range []string{
		"input_json: Record<string, any>", "output_json: Record<string, any>",
		"scope_json: Record<string, any>", "payload_json: Record<string, any>",
	} {
		if !strings.Contains(models, required) {
			t.Fatalf("generated Wails model missing %q", required)
		}
	}
}

type workflowContextRunner struct {
	bots []bughub.BotRef
}

func (r *workflowContextRunner) Start(_ context.Context, _ bughub.PhaseAttempt, _ bughub.Bug, bot bughub.BotRef) error {
	r.bots = append(r.bots, bot)
	return nil
}

func (*workflowContextRunner) Cancel(context.Context, string) error { return nil }
