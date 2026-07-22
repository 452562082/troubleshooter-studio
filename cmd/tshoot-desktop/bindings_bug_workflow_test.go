package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func TestLoadBugAndBotMaterializesAndPersistsIncidentEvidence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/files/101/download" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), []byte("incident-evidence")...))
	}))
	defer server.Close()
	if _, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: server.URL, Token: "secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	bug := bughub.Bug{ID: "zentao-101", Source: "zentao", Title: "页面年份未展示", Attachments: []bughub.Attachment{{
		ID: "101", Name: "页面证据.png", Type: "image/png", RemoteURL: "/data/upload/evidence",
	}}}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	app := &App{workflowLoadBot: func(string) (bughub.BotRef, error) {
		return bughub.BotRef{Key: "base|codex#validator", Target: "codex", Role: "validator"}, nil
	}}

	loaded, _, err := app.loadBugAndBot(bug.ID, "base|codex#validator")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Attachments) != 1 || loaded.Attachments[0].LocalPath == "" {
		t.Fatalf("incident evidence was not materialized: %+v", loaded.Attachments)
	}
	stored, found, err := bugStore().Get(bug.ID)
	if err != nil || !found || stored.Attachments[0].LocalPath != loaded.Attachments[0].LocalPath {
		t.Fatalf("materialized evidence was not persisted: found=%v stored=%+v err=%v", found, stored.Attachments, err)
	}
}

func TestGetIncidentCaseAndEmittedSnapshotsHideArtifactPathsAndApplicationURLs(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerTextArtifact(t, app, store, "case-a", "attempt-a")
	const applicationURL = "https://app.test/oauth/start?state=oauth-query-sentinel"
	finished := time.Now().UTC()
	if err := store.CreateAttempt(context.Background(), bughub.PhaseAttempt{
		ID: "attempt-public-projection", CaseID: "case-a", CycleNumber: 1,
		Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusFailed,
		AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`),
		OutputJSON: []byte(`{"error_code":"browser_login_required","application_url":"` + applicationURL + `","application_origin":"https://app.test","login_origin":"https://login.test","nested":{"application_url":"` + applicationURL + `","path_or_reference":"` + artifact.PathOrReference + `"}}`),
		StartedAt:  finished.Add(-time.Second), FinishedAt: &finished,
		ErrorCode: "browser_login_required", ErrorMessage: "login required",
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"application_url":    applicationURL,
		"application_origin": "https://app.test",
		"login_origin":       "https://login.test",
		"path_or_reference":  artifact.PathOrReference,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Transition(context.Background(), "case-a", 1, bughub.CaseWaitingEvidence, bughub.TransitionEvent{
		ID: "event-public-projection", CaseID: "case-a", FromStatus: bughub.CaseValidating, ToStatus: bughub.CaseWaitingEvidence,
		EventType: "phase_completed", ActorType: "agent", ActorID: "validator", IdempotencyKey: "event-public-projection",
		PayloadJSON: payload, CreatedAt: finished,
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := app.GetIncidentCase("case-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
	artifactType := reflect.TypeOf(detail.Artifacts[0])
	if _, exposed := artifactType.FieldByName("PathOrReference"); exposed {
		t.Fatalf("public artifact type exposes PathOrReference: %s", artifactType)
	}
	if _, present := artifactType.FieldByName("Size"); !present {
		t.Fatalf("public artifact type has no Size: %s", artifactType)
	}

	assertPublic := func(name string, value any) {
		t.Helper()
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", name, marshalErr)
		}
		text := string(encoded)
		for _, forbidden := range []string{artifact.PathOrReference, applicationURL, "path_or_reference", "application_url"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s exposed %q: %s", name, forbidden, text)
			}
		}
		for _, required := range []string{"application_origin", "https://app.test", "login_origin", "https://login.test", `"size":9`} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omitted %q: %s", name, required, text)
			}
		}
	}
	assertPublic("detail", detail)

	var emitted []IncidentCaseEventPayload
	app.workflowEmit = func(name string, value any) {
		if name == incidentCaseEvent {
			emitted = append(emitted, value.(IncidentCaseEventPayload))
		}
	}
	app.emitIncidentCase("case-a")
	app.emitIncidentPhaseEvent("case-a", bughub.InvestigationEvent{
		Type:    "command_execution",
		Message: "  go test ./...  ",
		Raw:     map[string]any{"application_url": applicationURL, "path_or_reference": artifact.PathOrReference},
		Meta:    map[string]any{"case_id": "case-a", "attempt_id": "attempt-a", "phase": "investigation", "state": "completed", "exit_code": 0, "application_url": applicationURL},
	})
	if len(emitted) != 2 {
		t.Fatalf("emitted = %+v", emitted)
	}
	for index, event := range emitted {
		assertPublic(fmt.Sprintf("event %d", index), event)
	}
	phase := emitted[1].PhaseEvent
	if phase == nil || phase.Raw != nil || phase.Message != "go test ./..." {
		t.Fatalf("public phase event = %+v", phase)
	}
	if _, ok := phase.Meta["application_url"]; ok {
		t.Fatalf("public phase event exposed private meta = %+v", phase.Meta)
	}
	if phase.Meta["state"] != "completed" || phase.Meta["exit_code"] != 0 {
		t.Fatalf("public phase event omitted progress meta = %+v", phase.Meta)
	}
}

func TestSyncIncidentBugResolutionIsGatedBySuccessfulRegression(t *testing.T) {
	calls := 0
	app := &App{workflowResolveBug: func(_ context.Context, incident bughub.IncidentCase) error {
		calls++
		if incident.Status != bughub.CaseFixedVerified {
			t.Fatalf("resolver received status %s", incident.Status)
		}
		return nil
	}}

	for _, status := range []bughub.CaseStatus{
		bughub.CaseNotReproduced,
		bughub.CaseInvestigating,
		bughub.CaseStillReproduces,
		bughub.CaseRegressionValidating,
	} {
		if err := app.syncIncidentBugResolution(context.Background(), bughub.IncidentCase{ID: "case-gate", Status: status}); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	if calls != 0 {
		t.Fatalf("resolver calls before successful regression = %d", calls)
	}
	if err := app.syncIncidentBugResolution(context.Background(), bughub.IncidentCase{ID: "case-gate", Status: bughub.CaseFixedVerified}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("resolver calls after successful regression = %d, want 1", calls)
	}
}

func TestIncidentPhaseEventsForDetailRestoresOnlySafeCurrentAttemptProgress(t *testing.T) {
	root := t.TempDir()
	store := bughub.NewInvestigationStore(root)
	if err := store.Upsert(bughub.InvestigationRun{
		ID: "attempt-current", BugID: "bug-1", Status: bughub.InvestigationRunning,
		Events: []bughub.InvestigationEvent{
			{Type: "phase_step", Message: "接收复现证据与上下文", Raw: map[string]any{"token": "secret"}, Meta: map[string]any{"case_id": "case-1", "attempt_id": "attempt-current", "phase": "investigation", "step_key": "evidence_handoff", "step_index": 1, "step_total": 7, "state": "running", "token": "secret"}},
			{Type: "raw", Message: "Authorization: Bearer secret", Raw: map[string]any{"password": "secret"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	events := incidentPhaseEventsForDetail(root, bughub.IncidentCase{ID: "case-1", CurrentAttemptID: "attempt-current"})
	if len(events) != 1 || events[0].Type != "phase_step" || events[0].Raw != nil {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Meta["step_key"] != "evidence_handoff" || fmt.Sprint(events[0].Meta["step_index"]) != "1" || fmt.Sprint(events[0].Meta["step_total"]) != "7" {
		t.Fatalf("step meta = %+v", events[0].Meta)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "Authorization") || strings.Contains(string(encoded), "token") {
		t.Fatalf("restored progress exposed private data: %s", encoded)
	}
}

func TestIncidentPhaseEventsForDetailRetainsLatestInvestigationStepAcrossEventRollover(t *testing.T) {
	root := t.TempDir()
	store := bughub.NewInvestigationStore(root)
	events := []bughub.InvestigationEvent{{Type: "phase_step", Message: "横向运行时检查", Meta: map[string]any{"case_id": "case-1", "attempt_id": "attempt-current", "phase": "investigation", "step_key": "runtime_scope", "step_index": 3, "step_total": 7, "state": "running"}}}
	for index := 0; index < 110; index++ {
		events = append(events, bughub.InvestigationEvent{Type: "command_execution", Message: fmt.Sprintf("command-%d", index), Meta: map[string]any{"case_id": "case-1", "attempt_id": "attempt-current", "phase": "investigation", "state": "completed", "exit_code": 0}})
	}
	if err := store.Upsert(bughub.InvestigationRun{ID: "attempt-current", BugID: "bug-1", Status: bughub.InvestigationRunning, Events: events}); err != nil {
		t.Fatal(err)
	}

	restored := incidentPhaseEventsForDetail(root, bughub.IncidentCase{ID: "case-1", CurrentAttemptID: "attempt-current"})
	if len(restored) != 100 || restored[0].Type != "phase_step" || restored[0].Meta["step_key"] != "runtime_scope" || restored[len(restored)-1].Message != "command-109" {
		t.Fatalf("restored events lost step checkpoint: first=%+v last=%+v len=%d", restored[0], restored[len(restored)-1], len(restored))
	}
}

func TestIncidentPhaseAttemptsRecoversSafeLegacyInvestigationGaps(t *testing.T) {
	root := t.TempDir()
	legacy := bughub.NewInvestigationStore(root)
	if err := legacy.Upsert(bughub.InvestigationRun{
		ID: "attempt-investigation", BugID: "bug-1", Status: bughub.InvestigationFailed,
		FinalMessage: `investigation_status: root_cause_ready
environment: test
root_cause: frontend renders the same name twice
confidence: medium
call_chain: []
evidence: []
gaps: [missing response body]`,
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := incidentPhaseAttempts([]bughub.PhaseAttempt{{
		ID: "attempt-investigation", CaseID: "case-1", Phase: bughub.PhaseInvestigation,
		Status: bughub.AttemptStatusFailed, InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
		ErrorCode: "invalid_phase_result", ErrorMessage: "root_cause_ready must not contain blocking gaps",
	}}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].OutputJSON["investigation_status"] != "insufficient_info" {
		t.Fatalf("attempts = %+v", attempts)
	}
	gaps, ok := attempts[0].OutputJSON["gaps"].([]any)
	if !ok || len(gaps) != 1 || gaps[0] != "missing response body" {
		t.Fatalf("recovered gaps = %#v", attempts[0].OutputJSON["gaps"])
	}
	if attempts[0].ErrorCode != "invalid_phase_result" || attempts[0].ErrorMessage == "" {
		t.Fatalf("durable audit error was modified: %+v", attempts[0])
	}
}

func TestIncidentPhaseAttemptsRecoversSafelyDowngradedCallChainPrecision(t *testing.T) {
	root := t.TempDir()
	legacy := bughub.NewInvestigationStore(root)
	if err := legacy.Upsert(bughub.InvestigationRun{
		ID: "attempt-investigation", BugID: "bug-1", Status: bughub.InvestigationFailed,
		FinalMessage: `investigation_status: insufficient_info
environment: test
root_cause: frontend may render the same name twice
confidence: medium
call_chain:
  - kind: service
    name: user search
    repo: backend
    revision: ""
    file: internal/search.go
    line: 42
    precision: source_mapped
    evidence: current repository candidate
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes: []`,
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := incidentPhaseAttempts([]bughub.PhaseAttempt{{
		ID: "attempt-investigation", CaseID: "case-1", Phase: bughub.PhaseInvestigation,
		Status: bughub.AttemptStatusFailed, InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
		ErrorCode: "invalid_phase_result", ErrorMessage: "investigation call_chain[0] source_mapped precision requires repo, deployed revision, file, line, and evidence",
	}}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].OutputJSON["investigation_status"] != "insufficient_info" {
		t.Fatalf("attempts = %+v", attempts)
	}
	callChain, ok := attempts[0].OutputJSON["call_chain"].([]any)
	if !ok || len(callChain) != 1 {
		t.Fatalf("call chain = %#v", attempts[0].OutputJSON["call_chain"])
	}
	hop, ok := callChain[0].(map[string]any)
	if !ok || hop["precision"] != "static_candidate" {
		t.Fatalf("call chain hop = %#v", callChain[0])
	}
	if attempts[0].ErrorCode != "invalid_phase_result" || attempts[0].ErrorMessage == "" {
		t.Fatalf("durable audit error was modified: %+v", attempts[0])
	}
}

func TestReconcileIncidentBugResolutionsRecoversAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	remoteStatus := "active"
	postCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"bug":{"id":"840","title":"搜索结果不完整","status":%q}}`, remoteStatus)
		case http.MethodPost:
			postCount++
			remoteStatus = "resolved"
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "api_token", Token: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-840", Source: "zentao", SourceID: "840", PlatformID: platform.ID,
		Title: "搜索结果不完整", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	incident := bughub.IncidentCase{
		ID: "case-fixed-reconcile", BugID: "zentao-840", Source: "zentao", SystemID: "base",
		Environment: "test", Status: bughub.CaseFixedVerified, CycleNumber: 2,
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}

	app.reconcileIncidentBugResolutions(context.Background())
	app.reconcileIncidentBugResolutions(context.Background())
	if postCount != 1 {
		t.Fatalf("resolve POST count = %d, want 1", postCount)
	}
	stored, found, err := bugStore().Get("zentao-840")
	if err != nil || !found || stored.Status != "resolved" || stored.InboxState != bughub.BugInboxHistory || stored.ArchivedAt == nil {
		t.Fatalf("stored Bug = %+v found=%v err=%v", stored, found, err)
	}
}

func TestGeneratedWailsIncidentArtifactContractIsPathFree(t *testing.T) {
	models, err := os.ReadFile(filepath.Join("..", "..", "web", "wailsjs", "go", "models.ts"))
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := os.ReadFile(filepath.Join("..", "..", "web", "wailsjs", "go", "main", "App.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(models), "path_or_reference") || strings.Contains(string(models), "class EvidenceArtifact") {
		t.Fatalf("generated models expose internal artifact paths")
	}
	if !strings.Contains(string(models), "class IncidentArtifact") || !strings.Contains(string(models), "size: number") {
		t.Fatalf("generated models omit the path-free incident artifact DTO")
	}
	if !strings.Contains(string(declarations), "SaveIncidentArtifact(arg1:string,arg2:string):Promise<boolean>") {
		t.Fatalf("generated SaveIncidentArtifact contract is not boolean")
	}
}

type workflowBindingRunner struct {
	mu        sync.Mutex
	starts    int
	cancels   int
	bugs      []bughub.Bug
	bots      []bughub.BotRef
	startErr  error
	cancelErr error
}

type workflowBindingGit struct{ request bughub.MergeRequest }

func (g *workflowBindingGit) Inspect(_ context.Context, request bughub.MergeRequest) (bughub.MergeInspection, error) {
	result := bughub.MergeInspection{Repositories: map[string]bughub.MergeRepositoryResult{}}
	for repo, fix := range request.FixCommits {
		head := "head-" + repo
		result.Repositories[repo] = bughub.MergeRepositoryResult{TargetHead: head, ApprovalKey: bughub.MergeApprovalKey(request.CaseID, repo, fix, request.TargetBranches[repo], head)}
	}
	return result, nil
}
func (g *workflowBindingGit) MergeAndPush(_ context.Context, request bughub.MergeRequest) (bughub.MergeResult, error) {
	g.request = request.Clone()
	return bughub.MergeResult{Pushed: true, Repositories: map[string]bughub.MergeRepositoryResult{"api": {MergeCommit: "merge-api", Pushed: true}}}, nil
}
func (*workflowBindingGit) ResumePush(context.Context, bughub.MergeRequest) (bughub.MergeResult, error) {
	return bughub.MergeResult{}, nil
}
func (*workflowBindingGit) InspectFix(context.Context, bughub.FixInspectionRequest) (bughub.FixInspection, error) {
	return bughub.FixInspection{}, nil
}

func (r *workflowBindingRunner) Start(_ context.Context, _ bughub.PhaseAttempt, bug bughub.Bug, bot bughub.BotRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.bugs = append(r.bugs, bug)
	r.bots = append(r.bots, bot)
	return r.startErr
}

func (r *workflowBindingRunner) Cancel(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels++
	return r.cancelErr
}

func (r *workflowBindingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *workflowBindingRunner) cancelCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancels
}

func (r *workflowBindingRunner) lastBot() bughub.BotRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bots) == 0 {
		return bughub.BotRef{}
	}
	return r.bots[len(r.bots)-1]
}

func (r *workflowBindingRunner) lastBug() bughub.Bug {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bugs) == 0 {
		return bughub.Bug{}
	}
	return r.bugs[len(r.bugs)-1]
}

func newWorkflowBindingApp(t *testing.T, dbPath string) (*App, *bughub.CaseStore, *workflowBindingRunner) {
	t.Helper()
	store, err := bughub.OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	orchestrator := bughub.NewCaseOrchestrator(store, runner, nil, nil)
	botPath := t.TempDir()
	app := &App{
		workflowStore:        store,
		workflowOrchestrator: orchestrator,
		workflowBrowser: &fakeIncidentBrowserController{
			status: browserverify.RuntimeStatus{State: browserverify.RuntimeReady, Version: "1.61.1"},
		},
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, Source: "zentao", Title: "checkout fails", Env: "test", SystemID: "base"}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			return bughub.BotRef{Key: key, Target: "codex", Path: botPath, SystemID: "base", Env: "test"}, nil
		},
		workflowLoadDeploymentConfig: func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
			return &config.SystemConfig{System: config.System{ID: "base"}, Environments: []config.Environment{{ID: "test", IsProd: false}}}, nil
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

func TestInitializeIncidentWorkflowOwnsBrowserController(t *testing.T) {
	app := &App{workflowRoot: t.TempDir()}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	if err := app.initializeIncidentWorkflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if app.workflowBrowser == nil || app.workflowRunner == nil {
		t.Fatalf("browser=%T runner=%p", app.workflowBrowser, app.workflowRunner)
	}
	status := app.workflowBrowser.Status()
	if status.ErrorCode != "browser_runtime_missing" {
		t.Fatalf("browser status = %+v", status)
	}
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

func TestGetIncidentWorkflowMetricsIsReadOnly(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "metrics.db"))
	before := createPendingBindingCase(t, store, "case-metrics")

	metrics, err := app.GetIncidentWorkflowMetrics()
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.GetCase(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.OpenCases != 1 || metrics.CompletedCases != 0 {
		t.Fatalf("metrics=%+v", metrics)
	}
	if after.Version != before.Version || after.Status != before.Status || after.CycleNumber != before.CycleNumber {
		t.Fatalf("metrics changed Case: before=%+v after=%+v", before, after)
	}
}

func TestPollWorkflowRemindersUsesLocalWorkflowEvent(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "reminder.db"))
	waitingSince := time.Now().UTC().Add(-25 * time.Hour)
	incident := bughub.IncidentCase{ID: "case-reminder", BugID: "bug-reminder", SystemID: "base", Environment: "test", Status: bughub.CaseMerging, CycleNumber: 1, Version: 1, CreatedAt: waitingSince.Add(-time.Hour), UpdatedAt: waitingSince}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	event := bughub.TransitionEvent{ID: "wait-reminder", CaseID: incident.ID, FromStatus: bughub.CaseMerging, ToStatus: bughub.CaseWaitingDeployment, EventType: "merge_pushed", ActorType: "git", ActorID: "git", IdempotencyKey: "wait-reminder", PayloadJSON: []byte(`{}`), CreatedAt: waitingSince}
	if _, _, err := store.Transition(context.Background(), incident.ID, 1, bughub.CaseWaitingDeployment, event); err != nil {
		t.Fatal(err)
	}
	var name string
	var payload any
	app.workflowEmit = func(gotName string, gotPayload any) { name, payload = gotName, gotPayload }

	app.pollWorkflowReminders(context.Background())

	reminder, ok := payload.(bughub.WorkflowReminder)
	if name != incidentWorkflowReminderEvent || !ok || reminder.CaseID != incident.ID {
		t.Fatalf("event=%q payload=%+v", name, payload)
	}
	pending, err := app.ListPendingIncidentWorkflowReminders()
	if err != nil || len(pending) != 1 || pending[0].ReservationKey != reminder.ReservationKey {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := app.AckIncidentWorkflowReminder(AckIncidentWorkflowReminderInput{CaseID: reminder.CaseID, ReservationKey: reminder.ReservationKey, DeliveryAttempt: reminder.DeliveryAttempt, ActorID: "desktop-root"}); err != nil {
		t.Fatal(err)
	}
	pending, err = app.ListPendingIncidentWorkflowReminders()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after ack=%+v err=%v", pending, err)
	}
}

func TestPollWorkflowRemindersWithoutRuntimeRemainsPendingForLateMount(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "reminder-no-runtime.db"))
	waitingSince := time.Now().UTC().Add(-25 * time.Hour)
	incident := bughub.IncidentCase{ID: "case-no-runtime", BugID: "bug-no-runtime", SystemID: "base", Environment: "test", Status: bughub.CaseMerging, CycleNumber: 1, Version: 1, CreatedAt: waitingSince.Add(-time.Hour), UpdatedAt: waitingSince}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	event := bughub.TransitionEvent{ID: "wait-no-runtime", CaseID: incident.ID, FromStatus: bughub.CaseMerging, ToStatus: bughub.CaseWaitingDeployment, EventType: "merge_pushed", ActorType: "git", ActorID: "git", IdempotencyKey: "wait-no-runtime", PayloadJSON: []byte(`{}`), CreatedAt: waitingSince}
	if _, _, err := store.Transition(context.Background(), incident.ID, 1, bughub.CaseWaitingDeployment, event); err != nil {
		t.Fatal(err)
	}

	app.pollWorkflowReminders(context.Background())

	pending, err := app.ListPendingIncidentWorkflowReminders()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	events, err := store.ListEvents(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundFailure := false
	for _, item := range events {
		if item.EventType == "deployment_reminder_delivery_failed" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("missing durable delivery failure audit")
	}
}

func TestPollWorkflowRemindersUsesConfiguredProductionFlagForLiveAndOnline(t *testing.T) {
	for _, environment := range []string{"live", "online"} {
		t.Run(environment, func(t *testing.T) {
			app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "reminder-prod.db"))
			waitingSince := time.Now().UTC().Add(-25 * time.Hour)
			incident := bughub.IncidentCase{ID: "case-" + environment, BugID: "bug-" + environment, SystemID: "base", Environment: environment, Status: bughub.CaseMerging, CycleNumber: 1, Version: 1, CreatedAt: waitingSince.Add(-time.Hour), UpdatedAt: waitingSince}
			if err := store.CreateCase(context.Background(), incident); err != nil {
				t.Fatal(err)
			}
			event := bughub.TransitionEvent{ID: "wait-" + environment, CaseID: incident.ID, FromStatus: bughub.CaseMerging, ToStatus: bughub.CaseWaitingDeployment, EventType: "merge_pushed", ActorType: "git", ActorID: "git", IdempotencyKey: "wait-" + environment, PayloadJSON: []byte(`{}`), CreatedAt: waitingSince}
			if _, _, err := store.Transition(context.Background(), incident.ID, 1, bughub.CaseWaitingDeployment, event); err != nil {
				t.Fatal(err)
			}
			app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
				return &config.SystemConfig{System: config.System{ID: "base"}, Environments: []config.Environment{{ID: environment, IsProd: true}}}, nil
			}
			app.workflowEmit = func(string, any) { t.Fatal("configured production Case emitted a reminder") }

			app.pollWorkflowReminders(context.Background())

			pending, err := app.ListPendingIncidentWorkflowReminders()
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending=%+v err=%v", pending, err)
			}
		})
	}
}

func TestPollWorkflowRemindersDoesNotTreatEnvironmentNameAsProductionAuthority(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "reminder-name.db"))
	waitingSince := time.Now().UTC().Add(-25 * time.Hour)
	incident := bughub.IncidentCase{ID: "case-production-name", BugID: "bug-production-name", SystemID: "base", Environment: "production", Status: bughub.CaseMerging, CycleNumber: 1, Version: 1, CreatedAt: waitingSince.Add(-time.Hour), UpdatedAt: waitingSince}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	event := bughub.TransitionEvent{ID: "wait-production-name", CaseID: incident.ID, FromStatus: bughub.CaseMerging, ToStatus: bughub.CaseWaitingDeployment, EventType: "merge_pushed", ActorType: "git", ActorID: "git", IdempotencyKey: "wait-production-name", PayloadJSON: []byte(`{}`), CreatedAt: waitingSince}
	if _, _, err := store.Transition(context.Background(), incident.ID, 1, bughub.CaseWaitingDeployment, event); err != nil {
		t.Fatal(err)
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{System: config.System{ID: "base"}, Environments: []config.Environment{{ID: "production", IsProd: false}}}, nil
	}
	delivered := false
	app.workflowEmit = func(string, any) { delivered = true }

	app.pollWorkflowReminders(context.Background())

	if !delivered {
		t.Fatal("non-production Case was suppressed by its environment name")
	}
}

func TestPollWorkflowRemindersFailsClosedWhenEnvironmentConfigCannotBeResolved(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "reminder-unresolved.db"))
	waitingSince := time.Now().UTC().Add(-25 * time.Hour)
	incident := bughub.IncidentCase{ID: "case-unresolved", BugID: "bug-unresolved", SystemID: "base", Environment: "test", Status: bughub.CaseMerging, CycleNumber: 1, Version: 1, CreatedAt: waitingSince.Add(-time.Hour), UpdatedAt: waitingSince}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	event := bughub.TransitionEvent{ID: "wait-unresolved", CaseID: incident.ID, FromStatus: bughub.CaseMerging, ToStatus: bughub.CaseWaitingDeployment, EventType: "merge_pushed", ActorType: "git", ActorID: "git", IdempotencyKey: "wait-unresolved", PayloadJSON: []byte(`{}`), CreatedAt: waitingSince}
	if _, _, err := store.Transition(context.Background(), incident.ID, 1, bughub.CaseWaitingDeployment, event); err != nil {
		t.Fatal(err)
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return nil, errors.New("configuration unavailable")
	}
	app.workflowEmit = func(string, any) { t.Fatal("unresolved Case emitted a reminder") }

	app.pollWorkflowReminders(context.Background())

	pending, err := app.ListPendingIncidentWorkflowReminders()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
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

func TestResetIncidentCaseValidatesScalarsBeforeOpeningRuntime(t *testing.T) {
	factoryCalls := 0
	app := &App{workflowRuntimeFactory: func(*bughub.CaseStore, *bughub.InvestigationStore) incidentWorkflowRuntime {
		factoryCalls++
		return incidentWorkflowRuntime{}
	}}
	_, err := app.ResetIncidentCase(ResetIncidentCaseInput{
		CaseID: "case-old", ExpectedVersion: 1, IdempotencyKey: "reset-case-old", ActorID: "user-1", BotKey: "base|codex",
	})
	if err == nil || err.Error() != "new_case_id is required" {
		t.Fatalf("error = %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("runtime factory calls = %d", factoryCalls)
	}
}

func TestResetIncidentCaseForwardsContextAndEmitsReplacement(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	botPath := t.TempDir()
	app.workflowLoadBot = func(key string) (bughub.BotRef, error) {
		return bughub.BotRef{Key: key, Target: "claude-code", Path: botPath, SystemID: "base", Env: "prod"}, nil
	}
	ctx := context.Background()
	old := bughub.IncidentCase{
		ID: "case-reset-old", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-reset-old", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(ctx, old); err != nil {
		t.Fatal(err)
	}
	attempt := bughub.PhaseAttempt{ID: old.CurrentAttemptID, CaseID: old.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning, AgentTarget: "codex", BotKey: old.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload IncidentCaseEventPayload
	app.workflowEmit = func(name string, value any) {
		if name == incidentCaseEvent {
			payload = value.(IncidentCaseEventPayload)
		}
	}
	input := ResetIncidentCaseInput{
		CaseID: old.ID, NewCaseID: "case-reset-new", BotKey: "base|claude-code", ExpectedVersion: old.Version,
		IdempotencyKey: "reset-case-old", ActorID: "user-1", InputJSON: map[string]any{"reason": "retry"},
	}

	replacement, err := app.ResetIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID != input.NewCaseID || replacement.ResetFromCaseID != old.ID || replacement.Status != bughub.CaseValidating || replacement.SelectedBotKey != input.BotKey || replacement.Environment != "prod" {
		t.Fatalf("replacement = %+v", replacement)
	}
	if archived.Status != bughub.CaseResetArchived || archived.SupersededByCaseID != replacement.ID {
		t.Fatalf("archived = %+v", archived)
	}
	if runner.count() != 1 || runner.cancelCount() != 1 {
		t.Fatalf("starts=%d cancels=%d", runner.count(), runner.cancelCount())
	}
	if payload.Case == nil || payload.Snapshot == nil || payload.Case.ID != replacement.ID || payload.Snapshot.Case.ID != replacement.ID || payload.Snapshot.Case.SelectedBotKey != input.BotKey || payload.Snapshot.Case.Environment != "prod" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(payload.Snapshot.Attempts) != 1 || payload.Snapshot.Attempts[0].BotKey != input.BotKey || payload.Snapshot.Attempts[0].AgentTarget != "claude-code" {
		t.Fatalf("snapshot attempts = %+v", payload.Snapshot.Attempts)
	}
}

func TestResetIncidentCaseWithWarningsReturnsStructuredCancelWarning(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	ctx := context.Background()
	old := bughub.IncidentCase{
		ID: "case-reset-warning-old", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-reset-warning-old", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, bughub.PhaseAttempt{ID: old.CurrentAttemptID, CaseID: old.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning, AgentTarget: "codex", BotKey: old.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner.cancelErr = errors.New("secret runner transport detail")

	input := ResetIncidentCaseInput{
		CaseID: old.ID, NewCaseID: "case-reset-warning-new", BotKey: old.SelectedBotKey, ExpectedVersion: old.Version,
		IdempotencyKey: "reset-case-warning", ActorID: "user-1",
	}
	result, err := app.ResetIncidentCaseWithWarnings(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Case.ID != "case-reset-warning-new" || !reflect.DeepEqual(result.Warnings, []bughub.WorkflowWarning{{Code: "reset_runner_cancel_failed", Message: "旧阶段 Agent 未能确认停止，请人工检查其运行状态。"}}) {
		t.Fatalf("result=%+v", result)
	}
	legacy, legacyErr := app.ResetIncidentCase(input)
	if legacyErr != nil || legacy.ID != result.Case.ID {
		t.Fatalf("cancel-only legacy result=%+v err=%v", legacy, legacyErr)
	}
}

func TestResetIncidentCaseWithWarningsResolvesCancelAndReplacementStartWarnings(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	ctx := context.Background()
	old := bughub.IncidentCase{ID: "case-reset-double-warning-old", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-reset-double-warning-old", SelectedBotKey: "base|codex"}
	if err := store.CreateCase(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, bughub.PhaseAttempt{ID: old.CurrentAttemptID, CaseID: old.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning, AgentTarget: "codex", BotKey: old.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner.cancelErr = errors.New("secret cancel failure")
	runner.startErr = errors.New("secret start failure")

	input := ResetIncidentCaseInput{CaseID: old.ID, NewCaseID: "case-reset-double-warning-new", BotKey: old.SelectedBotKey, ExpectedVersion: old.Version, IdempotencyKey: "reset-case-double-warning", ActorID: "user-1"}
	result, err := app.ResetIncidentCaseWithWarnings(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []bughub.WorkflowWarning{
		{Code: "reset_runner_cancel_failed", Message: "旧阶段 Agent 未能确认停止，请人工检查其运行状态。"},
		{Code: "reset_replacement_start_failed", Message: "接替 Case 的新阶段未能启动，已保留为可恢复状态；请刷新 Case 或重试开始验证。"},
	}
	if result.Case.ID != "case-reset-double-warning-new" || result.Case.Status != bughub.CaseWaitingEvidence || !reflect.DeepEqual(result.Warnings, want) {
		t.Fatalf("result=%+v", result)
	}
	legacy, legacyErr := app.ResetIncidentCase(input)
	if legacyErr == nil || legacy.ID != result.Case.ID {
		t.Fatalf("start-failed legacy result=%+v err=%v", legacy, legacyErr)
	}
}

func TestResetIncidentCaseWithWarningsReturnsStableConflictCode(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	old := bughub.IncidentCase{ID: "case-reset-conflict-code", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: "base|codex"}
	if err := store.CreateCase(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	_, err := app.ResetIncidentCaseWithWarnings(ResetIncidentCaseInput{
		CaseID: old.ID, NewCaseID: old.ID + "-next", BotKey: old.SelectedBotKey, ExpectedVersion: 2,
		IdempotencyKey: "reset-conflict-code", ActorID: "user-1",
	})
	if err == nil || !strings.Contains(err.Error(), "workflow_conflict:case_version_conflict") {
		t.Fatalf("err=%v", err)
	}
}

func TestResetIncidentCaseDuplicateCommandSchedulesOnce(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	ctx := context.Background()
	old := bughub.IncidentCase{ID: "case-reset-replay", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: "base|codex"}
	if err := store.CreateCase(ctx, old); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := ResetIncidentCaseInput{CaseID: old.ID, NewCaseID: "case-reset-replay-next", BotKey: old.SelectedBotKey, ExpectedVersion: old.Version, IdempotencyKey: "reset-replay", ActorID: "user-1"}

	first, err := app.ResetIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	starts, cancels := runner.count(), runner.cancelCount()
	second, err := app.ResetIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.CurrentAttemptID != first.CurrentAttemptID || runner.count() != starts || runner.cancelCount() != cancels {
		t.Fatalf("first=%+v second=%+v starts=%d/%d cancels=%d/%d", first, second, starts, runner.count(), cancels, runner.cancelCount())
	}
}

func TestApproveIncidentFixRejectsMismatchedDialogScopeBeforeOpeningRuntime(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{workflowRoot: rootFile}).ApproveIncidentFix(ApproveIncidentFixInput{
		CaseID: "case-1", ExpectedVersion: 7, IdempotencyKey: "approve-fix", ActorID: "alice", RootCauseAttemptID: "investigation-7",
	})
	if err == nil || !strings.Contains(err.Error(), "dialog snapshot scope") {
		t.Fatalf("err=%v", err)
	}
}

func TestListIncidentFixBranchesUsesOnlyApprovedRemediationRepositories(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	t.Setenv("HOME", root)
	backendRepo := filepath.Join(root, "base-backend")
	frontendRepo := filepath.Join(root, "base-frontend")
	initGitRepoWithBranch(t, backendRepo, "feature/xiaolong_v1.4")
	initGitRepoWithBranch(t, frontendRepo, "frontend-test")
	if err := userconfig.SetRepoPathsForSystem("base", map[string]string{"base-backend": backendRepo, "base-frontend": frontendRepo}); err != nil {
		t.Fatal(err)
	}

	app, store, _ := newWorkflowBindingApp(t, filepath.Join(root, "cases.db"))
	ctx := context.Background()
	incident := bughub.IncidentCase{
		ID: "case-fix-branches", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseWaitingFixApproval, CycleNumber: 1, SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	rootAttempt := bughub.PhaseAttempt{
		ID: "root-fix-branches", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseInvestigation,
		Status: bughub.AttemptStatusSucceeded, AgentTarget: "codex", BotKey: incident.SelectedBotKey, InputJSON: []byte(`{}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"backend field mismatch","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","repositories":["base-backend"],"target":"base-backend/service.go","summary":"fix mapping","verification":"run regression"},"call_chain":[{"kind":"frontend","name":"web","repo":"base-frontend","precision":"unavailable"},{"kind":"service","name":"api","repo":"base-backend","precision":"unavailable"}],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
	}
	if err := store.CreateAttempt(ctx, rootAttempt); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(ctx, bughub.CaseMutation{
		CaseID: current.ID, ExpectedVersion: current.Version, IdempotencyKey: "bind-fix-branches", RequestJSON: []byte(`{}`),
		Snapshot: bughub.CaseSnapshotUpdate{CurrentAttemptID: stringPointer(rootAttempt.ID)},
		Steps:    []bughub.CaseMutationStep{{To: bughub.CaseWaitingFixApproval, AuditOnly: true, Event: bughub.TransitionEvent{ID: "bind-fix-branches-event", EventType: "root_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	branches, err := app.ListIncidentFixBranches(bound.Case.ID, rootAttempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := branches["base-frontend"]; exposed {
		t.Fatalf("frontend call-chain repository leaked into fix options: %v", branches)
	}
	if !reflect.DeepEqual(branches["base-backend"], []string{"feature/xiaolong_v1.4", "main"}) {
		t.Fatalf("branches=%v, want base-backend branches only", branches)
	}
}

func TestReconsiderIncidentRemediationRejectsMismatchedDialogScopeBeforeOpeningRuntime(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{workflowRoot: rootFile}).ReconsiderIncidentRemediation(ReconsiderIncidentRemediationInput{
		CaseID: "case-1", ExpectedVersion: 7, IdempotencyKey: "reconsider-remediation:wrong", ActorID: "alice",
		RootCauseAttemptID: "investigation-7", Proposal: "改为后端统一字段语义",
	})
	if err == nil || !strings.Contains(err.Error(), "dialog snapshot scope") {
		t.Fatalf("err=%v", err)
	}
}

func TestReconsiderIncidentRemediationUsesPersistedBotAndStartsInvestigation(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	ctx := context.Background()
	incident := bughub.IncidentCase{
		ID: "case-reconsider-binding", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseWaitingFixApproval, CycleNumber: 1, SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := bughub.PhaseAttempt{
		ID: "root-reconsider-binding", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseInvestigation,
		Status: bughub.AttemptStatusSucceeded, AgentTarget: "codex", BotKey: incident.SelectedBotKey, InputJSON: []byte(`{}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"field mismatch","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"frontend","summary":"deduplicate labels","verification":"run regression"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
		StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(ctx, root); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(ctx, bughub.CaseMutation{
		CaseID: current.ID, ExpectedVersion: current.Version, IdempotencyKey: "bind-reconsider-root", RequestJSON: []byte(`{}`),
		Snapshot: bughub.CaseSnapshotUpdate{CurrentAttemptID: stringPointer(root.ID)},
		Steps:    []bughub.CaseMutationStep{{To: bughub.CaseWaitingFixApproval, AuditOnly: true, Event: bughub.TransitionEvent{ID: "bind-reconsider-root-event", EventType: "root_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := bughub.ReconsiderRemediationKey(bound.Case.ID, root.ID, bound.Case.Version)
	updated, err := app.ReconsiderIncidentRemediation(ReconsiderIncidentRemediationInput{
		CaseID: bound.Case.ID, ExpectedVersion: bound.Case.Version, IdempotencyKey: key, ActorID: "alice",
		RootCauseAttemptID: root.ID, Proposal: "优先在后端统一字段语义",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != bughub.CaseInvestigating || runner.count() != 1 {
		t.Fatalf("updated=%+v starts=%d", updated, runner.count())
	}
	if got := runner.lastBot(); got.Key != incident.SelectedBotKey || got.Env != "test" {
		t.Fatalf("runner bot=%+v", got)
	}
}

func TestCompleteIncidentRemediationRejectsMismatchedDialogScopeBeforeOpeningRuntime(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{workflowRoot: rootFile}).CompleteIncidentRemediation(CompleteIncidentRemediationInput{
		CaseID: "case-1", ExpectedVersion: 7, IdempotencyKey: "complete-remediation:wrong", ActorID: "alice",
		RootCauseAttemptID: "investigation-7", Summary: "restored runtime", Evidence: "ticket OPS-7",
	})
	if err == nil || !strings.Contains(err.Error(), "dialog snapshot scope") {
		t.Fatalf("err=%v", err)
	}
}

func TestApproveIncidentMergeForwardsTargetHeadsWithoutGrantingAuthority(t *testing.T) {
	store, err := bughub.OpenCaseStore(filepath.Join(t.TempDir(), "cases.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	incident := bughub.IncidentCase{ID: "case-merge-binding", BugID: "bug", Status: bughub.CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, bughub.PhaseAttempt{ID: "fix", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseFix, Status: bughub.AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCodeChange(ctx, bughub.CodeChange{ID: "change", CaseID: incident.ID, AttemptID: "fix", Repo: "api", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: "fix-api", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}); err != nil {
		t.Fatal(err)
	}
	git := &workflowBindingGit{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, &workflowBindingRunner{}, git, nil)}
	got, err := app.ApproveIncidentMerge(ApproveIncidentMergeInput{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "merge-binding", ActorID: "alice", FixCommits: map[string]string{"api": "caller"}, TargetBranches: map[string]string{"api": "prod"}, TargetHeads: map[string]string{"api": "head-api"}})
	if err != nil || got.Status != bughub.CaseWaitingDeployment {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	if git.request.TargetHeads["api"] != "head-api" || git.request.FixCommits["api"] != "fix-api" || git.request.TargetBranches["api"] != "test" {
		t.Fatalf("request=%+v", git.request)
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

func TestStartIncidentCaseRejectsWebCaseUntilHostRuntimeIsReady(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "用户页面搜索失败", FrontendURL: "https://app.test/users", Env: "test", SystemID: "base"}, nil
	}
	app.workflowBrowser = &fakeIncidentBrowserController{status: browserverify.RuntimeStatus{
		State: browserverify.RuntimeInstalling, Version: "1.61.1", ErrorCode: "browser_runtime_install_in_progress",
	}}

	_, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-runtime-preparing", BugID: "bug-runtime-preparing", BotKey: "base|codex", ExpectedVersion: 0,
		IdempotencyKey: "create:runtime-preparing", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err == nil || !strings.Contains(err.Error(), "browser_runtime_preparing") {
		t.Fatalf("error = %v, want browser_runtime_preparing", err)
	}
	if runner.count() != 0 {
		t.Fatalf("runner starts = %d", runner.count())
	}
	if cases, listErr := store.ListCases(context.Background()); listErr != nil {
		t.Fatal(listErr)
	} else if len(cases) != 0 {
		t.Fatalf("Web Case was created while runtime prepared: %+v", cases)
	}
}

func TestStartIncidentCaseHydratesUIBugFromSelectedBotEnvironment(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "【APP】用户昵称模糊搜索结果不完整", Env: "test"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System:       config.System{ID: "base"},
			Environments: []config.Environment{{ID: "test", WebDomain: "https://app.test"}},
		}, nil
	}

	created, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-ui-context", BugID: "bug-ui-context", BotKey: "base|codex", ExpectedVersion: 0,
		IdempotencyKey: "create:ui-context", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SystemID != "base" {
		t.Fatalf("created SystemID = %q, want base", created.SystemID)
	}
	persisted, err := store.GetCase(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.SystemID != "base" {
		t.Fatalf("persisted SystemID = %q, want base", persisted.SystemID)
	}
	startedBug := runner.lastBug()
	if startedBug.SystemID != "base" || startedBug.FrontendURL != "https://app.test" {
		t.Fatalf("runner Bug = %+v, want hydrated system and configured Web URL", startedBug)
	}
}

func TestStartIncidentCaseRequiresAndPersistsAmbiguousFrontendSelection(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "页面用户名称展示异常", Env: "test"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System: config.System{ID: "base"},
			Environments: []config.Environment{{ID: "test", FrontendEntries: []config.FrontendEntry{
				{ID: "consumer", Name: "C 端", URL: "https://m.test", Repo: "consumer-web", DeviceProfile: "mobile"},
				{ID: "admin", Name: "管理端", URL: "https://admin.test", Repo: "admin-web", DeviceProfile: "desktop"},
			}}},
		}, nil
	}
	resolution, err := app.ResolveIncidentFrontendEntry(ResolveIncidentFrontendEntryInput{BugID: "bug-multi-front", BotKey: "base|codex"})
	if err != nil || resolution.Status != bughub.FrontendResolutionAmbiguous {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	base := StartIncidentCaseInput{CaseID: "case-multi-front", BugID: "bug-multi-front", BotKey: "base|codex", ExpectedVersion: 0, IdempotencyKey: "create:multi-front", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"}}
	if _, err := app.StartIncidentCase(base); err == nil || !strings.Contains(err.Error(), "frontend_entry_selection_required") {
		t.Fatalf("error=%v", err)
	}
	base.FrontendEntryID = "admin"
	created, err := app.StartIncidentCase(base)
	if err != nil {
		t.Fatal(err)
	}
	if created.FrontendEntry.ID != "admin" || created.FrontendEntry.URL != "https://admin.test/" || created.FrontendEntry.ResolutionSource != "user" {
		t.Fatalf("created binding=%+v", created.FrontendEntry)
	}
	stored, err := store.GetCase(context.Background(), created.ID)
	if err != nil || stored.FrontendEntry != created.FrontendEntry {
		t.Fatalf("stored=%+v err=%v", stored.FrontendEntry, err)
	}
	startedBug := runner.lastBug()
	if startedBug.FrontendURL != "https://admin.test/" || startedBug.FrontendRepo != "admin-web" {
		t.Fatalf("runner bug=%+v", startedBug)
	}
}

func TestResolveIncidentRecoveryContextHydratesConfiguredFrontendURL(t *testing.T) {
	app := &App{}
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "【H5】用户名称重复展示"}, nil
	}
	app.workflowLoadBot = func(key string) (bughub.BotRef, error) {
		return bughub.BotRef{Key: key, Target: "codex", Path: t.TempDir(), SystemID: "base"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System:       config.System{ID: "base"},
			Environments: []config.Environment{{ID: "test", WebDomain: "https://app.test"}},
		}, nil
	}
	incident := bughub.IncidentCase{ID: "case-1", BugID: "bug-1", SystemID: "base", Environment: "test"}
	attempt := bughub.PhaseAttempt{ID: "attempt-refresh", BotKey: "base|codex", AgentTarget: "codex"}

	bug, bot, err := app.resolveIncidentRecoveryContext(context.Background(), incident, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if bot.Env != "test" || bug.SystemID != "base" || bug.FrontendURL != "https://app.test" {
		t.Fatalf("recovery context did not hydrate deployed browser config: bug=%+v bot=%+v", bug, bot)
	}
}

func TestStartIncidentCaseDoesNotRouteBackendBugThroughBrowser(t *testing.T) {
	app, _, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "数据库查询超时", Env: "test"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System:       config.System{ID: "base"},
			Environments: []config.Environment{{ID: "test", WebDomain: "https://app.test"}},
		}, nil
	}

	_, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-backend-context", BugID: "bug-backend-context", BotKey: "base|codex", ExpectedVersion: 0,
		IdempotencyKey: "create:backend-context", ActorID: "user-1", InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if startedBug := runner.lastBug(); startedBug.SystemID != "base" || startedBug.FrontendURL != "" {
		t.Fatalf("runner Bug = %+v, backend Bug must not be routed through browser", startedBug)
	}
}

func TestResetIncidentCaseHydratesReplacementBrowserContext(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "【APP】用户昵称模糊搜索结果不完整", Env: "test"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System:       config.System{ID: "base"},
			Environments: []config.Environment{{ID: "test", WebDomain: "https://app.test"}},
		}, nil
	}
	original := bughub.IncidentCase{
		ID: "case-ui-old", BugID: "bug-ui-reset", Source: "zentao", Environment: "test",
		Status: bughub.CaseWaitingEvidence, CycleNumber: 1, SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	original, err := store.GetCase(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := app.ResetIncidentCaseWithWarnings(ResetIncidentCaseInput{
		CaseID: original.ID, NewCaseID: "case-ui-replacement", BotKey: "base|codex",
		ExpectedVersion: original.Version, IdempotencyKey: "reset:ui-context", ActorID: "user-1",
		InputJSON: map[string]any{"mode": "reproduce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Case.SystemID != "base" || result.Case.Status != bughub.CaseValidating {
		t.Fatalf("replacement Case = %+v, want browser-bound validating Case", result.Case)
	}
	if startedBug := runner.lastBug(); startedBug.SystemID != "base" || startedBug.FrontendURL != "https://app.test" {
		t.Fatalf("runner Bug = %+v, want hydrated replacement browser context", startedBug)
	}
}

func TestStartIncidentCaseUsesPlatformMappedEnvironmentAcrossDesktopBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botKey := writeDiscoveredBugBot(t, root, "claude-code")
	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
		BotMappings: []bughub.PlatformBotMapping{{BotKey: botKey, Env: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bug := bughub.Bug{
		ID: "zentao-start-mapped", PlatformID: platform.ID, Source: "zentao",
		SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test",
	}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })

	matches, err := app.MatchBugBots(bug.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Bot.Key != botKey || matches[0].Bot.Env != "prod" {
		t.Fatalf("UI matches = %+v", matches)
	}
	platform.BotMappings = []bughub.PlatformBotMapping{{BotKey: botKey, Env: "stage"}}
	if _, err := bugPlatformStore().Upsert(platform); err != nil {
		t.Fatal(err)
	}
	created, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-start-mapped", BugID: bug.ID, BotKey: botKey,
		BotEnvironment: "prod", IdempotencyKey: "start:mapped", ActorID: "user-1",
		InputJSON: map[string]any{"target_environment": "prod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetCase(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Environment != "prod" || persisted.Environment != "prod" {
		t.Fatalf("created environment=%q persisted environment=%q, want platform mapping prod", created.Environment, persisted.Environment)
	}
	if got := runner.lastBot(); got.Key != botKey || got.Target != "claude-code" || got.Env != "prod" {
		t.Fatalf("runner Bot = %+v, want mapped environment prod", got)
	}
	attempt, err := store.GetAttempt(context.Background(), created.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(attempt.InputJSON), `"target_environment":"prod"`) {
		t.Fatalf("start input_json=%s, want target_environment prod", attempt.InputJSON)
	}
	if err := os.WriteFile(bugPlatformStore().Path(), []byte(`{"broken"`), 0o600); err != nil {
		t.Fatal(err)
	}
	replayed, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-start-mapped", BugID: bug.ID, BotKey: botKey,
		BotEnvironment: "prod", IdempotencyKey: "start:mapped", ActorID: "user-1",
		InputJSON: map[string]any{"target_environment": "prod"},
	})
	if err != nil || replayed != created || runner.count() != 1 {
		t.Fatalf("replayed=%+v created=%+v starts=%d err=%v", replayed, created, runner.count(), err)
	}
}

func TestResetIncidentCaseUsesPlatformMappedEnvironmentAcrossDesktopBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botKey := writeDiscoveredBugBot(t, root, "claude-code")
	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
		BotMappings: []bughub.PlatformBotMapping{{BotKey: botKey, Env: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bug := bughub.Bug{
		ID: "zentao-reset-mapped", PlatformID: platform.ID, Source: "zentao",
		SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test",
	}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	ctx := context.Background()
	original := bughub.IncidentCase{
		ID: "case-reset-mapped-old", BugID: bug.ID, Source: bug.Source, SystemID: bug.SystemID,
		Environment: "test", Status: bughub.CaseValidating, CycleNumber: 1,
		CurrentAttemptID: "attempt-reset-mapped-old", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, bughub.PhaseAttempt{
		ID: original.CurrentAttemptID, CaseID: original.ID, CycleNumber: 1,
		Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning,
		AgentTarget: "codex", BotKey: original.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	original, err = store.GetCase(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}

	matches, err := app.MatchBugBots(bug.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Bot.Key != botKey || matches[0].Bot.Env != "prod" {
		t.Fatalf("UI matches = %+v", matches)
	}
	platform.BotMappings = []bughub.PlatformBotMapping{{BotKey: botKey, Env: "stage"}}
	if _, err := bugPlatformStore().Upsert(platform); err != nil {
		t.Fatal(err)
	}
	input := ResetIncidentCaseInput{
		CaseID: original.ID, NewCaseID: "case-reset-mapped-new", BotKey: botKey, BotEnvironment: "prod",
		ExpectedVersion: original.Version, IdempotencyKey: "reset:mapped", ActorID: "user-1",
		InputJSON: map[string]any{"target_environment": "prod"},
	}
	replacement, err := app.ResetIncidentCase(input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := app.ResetIncidentCase(input)
	if err != nil || replayed != replacement {
		t.Fatalf("replayed=%+v replacement=%+v err=%v", replayed, replacement, err)
	}
	persisted, err := store.GetCase(ctx, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := store.GetCase(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Environment != "prod" || persisted.Environment != "prod" {
		t.Fatalf("replacement environment=%q persisted environment=%q, want platform mapping prod", replacement.Environment, persisted.Environment)
	}
	if archived.Environment != "test" || archived.SelectedBotKey != original.SelectedBotKey {
		t.Fatalf("archived binding changed: %+v", archived)
	}
	if got := runner.lastBot(); got.Key != botKey || got.Target != "claude-code" || got.Env != "prod" {
		t.Fatalf("runner Bot = %+v, want mapped environment prod", got)
	}
	if runner.count() != 1 || runner.cancelCount() != 1 {
		t.Fatalf("starts=%d cancels=%d", runner.count(), runner.cancelCount())
	}
	attempt, err := store.GetAttempt(ctx, replacement.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(attempt.InputJSON), `"target_environment":"prod"`) {
		t.Fatalf("replacement input_json=%s, want target_environment prod", attempt.InputJSON)
	}
}

func TestContinueIncidentCaseKeepsPersistedEnvironmentAcrossPlatformMappingChange(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botKey := writeDiscoveredBugBot(t, root, "claude-code")
	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
		BotMappings: []bughub.PlatformBotMapping{{BotKey: botKey, Env: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bug := bughub.Bug{
		ID: "zentao-continue-persisted", PlatformID: platform.ID, Source: "zentao",
		SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test",
	}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	incident := bughub.IncidentCase{
		ID: "case-continue-persisted", BugID: bug.ID, Source: bug.Source, SystemID: bug.SystemID,
		Environment: "test", Status: bughub.CaseWaitingEvidence, CycleNumber: 1, SelectedBotKey: botKey,
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	incident, err = store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}

	continued, err := app.ContinueIncidentCase(ContinueIncidentCaseInput{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "continue:persisted",
		ActorID: "user-1", Phase: bughub.PhaseInvestigation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if continued.Environment != "test" {
		t.Fatalf("continued Case environment=%q, want persisted test", continued.Environment)
	}
	if got := runner.lastBot(); got.Key != botKey || got.Env != "test" {
		t.Fatalf("continuation runner Bot = %+v, want persisted environment test", got)
	}
}

func TestStartIncidentCaseWithoutBotMappingKeepsBugEnvironmentFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botKey := writeDiscoveredBugBot(t, root, "codex")
	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bug := bughub.Bug{
		ID: "zentao-start-fallback", PlatformID: platform.ID, Source: "zentao",
		SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test",
	}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })

	created, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-start-fallback", BugID: bug.ID, BotKey: botKey,
		IdempotencyKey: "start:fallback", ActorID: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Environment != "legacy-test" {
		t.Fatalf("created environment=%q, want Bug.BotEnv fallback legacy-test", created.Environment)
	}
	if got := runner.lastBot(); got.Key != botKey || got.Env != "legacy-test" {
		t.Fatalf("runner Bot = %+v, want Bug.BotEnv fallback legacy-test", got)
	}
	attempt, err := store.GetAttempt(context.Background(), created.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if string(attempt.InputJSON) != `{}` {
		t.Fatalf("legacy input_json=%s, want unchanged empty object", attempt.InputJSON)
	}
}

func TestStartIncidentCaseExistingCaseKeepsPersistedEnvironment(t *testing.T) {
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "cases.db"))
	botPath := t.TempDir()
	app.workflowLoadBot = func(key string) (bughub.BotRef, error) {
		return bughub.BotRef{Key: key, Target: "codex", Path: botPath, SystemID: "base", Env: "prod"}, nil
	}
	incident := createPendingBindingCase(t, store, "case-start-persisted")

	started, err := app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: "start:persisted", ActorID: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Environment != "test" {
		t.Fatalf("started Case environment=%q, want persisted test", started.Environment)
	}
	if got := runner.lastBot(); got.Env != "test" {
		t.Fatalf("runner Bot = %+v, want persisted environment test", got)
	}
}

func TestResolveIncidentRecoveryContextKeepsPersistedEnvironment(t *testing.T) {
	app := &App{
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, Source: "zentao", SystemID: "base", BotEnv: "legacy-test", Env: "stage"}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			return bughub.BotRef{Key: key, Target: "claude-code", Path: t.TempDir(), Env: "prod"}, nil
		},
	}
	incident := bughub.IncidentCase{ID: "case-recovery-persisted", BugID: "bug-1", Environment: "test"}
	attempt := bughub.PhaseAttempt{ID: "attempt-recovery-persisted", BotKey: "base|claude-code", AgentTarget: "claude-code"}

	_, bot, err := app.resolveIncidentRecoveryContext(context.Background(), incident, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if bot.Env != "test" {
		t.Fatalf("recovery Bot = %+v, want persisted environment test", bot)
	}
}

func TestPersistedIncidentCommandsIgnoreMalformedPlatformConfiguration(t *testing.T) {
	t.Run("continuation", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("HOME", root)
		botKey := writeDiscoveredBugBot(t, root, "claude-code")
		platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
			ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
			BotMappings: []bughub.PlatformBotMapping{{BotKey: botKey, Env: "prod"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		bug := bughub.Bug{ID: "bug-malformed-continue", PlatformID: platform.ID, Source: "zentao", SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test"}
		if err := bugStore().Upsert(bug); err != nil {
			t.Fatal(err)
		}
		store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
		if err != nil {
			t.Fatal(err)
		}
		runner := &workflowBindingRunner{}
		app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
		t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
		incident := bughub.IncidentCase{ID: "case-malformed-continue", BugID: bug.ID, Source: bug.Source, SystemID: bug.SystemID, Environment: "test", Status: bughub.CaseWaitingEvidence, CycleNumber: 1, SelectedBotKey: botKey}
		if err := store.CreateCase(context.Background(), incident); err != nil {
			t.Fatal(err)
		}
		incident, err = store.GetCase(context.Background(), incident.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bugPlatformStore().Path(), []byte(`{"broken"`), 0o600); err != nil {
			t.Fatal(err)
		}

		continued, err := app.ContinueIncidentCase(ContinueIncidentCaseInput{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "continue:malformed-platform", ActorID: "user-1", Phase: bughub.PhaseInvestigation})
		if err != nil {
			t.Fatal(err)
		}
		if continued.Environment != "test" || runner.lastBot().Env != "test" {
			t.Fatalf("continued=%+v runner Bot=%+v", continued, runner.lastBot())
		}
	})

	t.Run("pending Start with unreadable platform", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("HOME", root)
		botKey := writeDiscoveredBugBot(t, root, "codex")
		platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		bug := bughub.Bug{ID: "bug-malformed-pending", PlatformID: platform.ID, Source: "zentao", SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test"}
		if err := bugStore().Upsert(bug); err != nil {
			t.Fatal(err)
		}
		store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
		if err != nil {
			t.Fatal(err)
		}
		runner := &workflowBindingRunner{}
		app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
		t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
		incident := bughub.IncidentCase{ID: "case-malformed-pending", BugID: bug.ID, Source: bug.Source, SystemID: bug.SystemID, Environment: "test", Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: botKey}
		if err := store.CreateCase(context.Background(), incident); err != nil {
			t.Fatal(err)
		}
		incident, err = store.GetCase(context.Background(), incident.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(bugPlatformStore().Path()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(bugPlatformStore().Path(), 0o700); err != nil {
			t.Fatal(err)
		}

		started, err := app.StartIncidentCase(StartIncidentCaseInput{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "start:malformed-platform", ActorID: "user-1"})
		if err != nil {
			t.Fatal(err)
		}
		if started.Environment != "test" || runner.lastBot().Env != "test" {
			t.Fatalf("started=%+v runner Bot=%+v", started, runner.lastBot())
		}
	})

	t.Run("recovery", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("HOME", root)
		botKey := writeDiscoveredBugBot(t, root, "claude-code")
		platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		bug := bughub.Bug{ID: "bug-malformed-recovery", PlatformID: platform.ID, Source: "zentao", SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "legacy-test"}
		if err := bugStore().Upsert(bug); err != nil {
			t.Fatal(err)
		}
		workflowRoot := filepath.Join(root, "workflow-root")
		if err := os.MkdirAll(workflowRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := bughub.OpenCaseStore(filepath.Join(workflowRoot, "workflows.db"))
		if err != nil {
			t.Fatal(err)
		}
		incident := bughub.IncidentCase{ID: "case-malformed-recovery", BugID: bug.ID, Source: bug.Source, SystemID: bug.SystemID, Environment: "test", Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: botKey}
		if err := store.CreateCase(context.Background(), incident); err != nil {
			t.Fatal(err)
		}
		attempt := bughub.PhaseAttempt{ID: "attempt-malformed-recovery", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusQueued, AgentTarget: "claude-code", BotKey: botKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
		if err := store.CreateAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bugPlatformStore().Path(), []byte(`{"broken"`), 0o600); err != nil {
			t.Fatal(err)
		}
		runner := &workflowBindingRunner{}
		app := &App{
			workflowRoot: workflowRoot,
			workflowRuntimeFactory: func(store *bughub.CaseStore, _ *bughub.InvestigationStore) incidentWorkflowRuntime {
				return incidentWorkflowRuntime{orchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
			},
		}
		if err := app.initializeIncidentWorkflow(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
		if runner.count() != 1 || runner.lastBot().Env != "test" {
			t.Fatalf("starts=%d runner Bot=%+v", runner.count(), runner.lastBot())
		}
	})
}

func TestStartIncidentCaseRejectsUnrecognizedEnvironmentSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botKey := writeDiscoveredBugBot(t, root, "codex")
	bug := bughub.Bug{ID: "bug-invalid-environment", Source: "zentao", SystemID: "base", Title: "checkout fails", Env: "stage", BotEnv: "test"}
	if err := bugStore().Upsert(bug); err != nil {
		t.Fatal(err)
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &workflowBindingRunner{}
	app := &App{workflowStore: store, workflowOrchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })

	_, err = app.StartIncidentCase(StartIncidentCaseInput{CaseID: "case-invalid-environment", BugID: bug.ID, BotKey: botKey, BotEnvironment: "forged", IdempotencyKey: "start:invalid-environment", ActorID: "user-1"})
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("error=%v", err)
	}
	if runner.count() != 0 {
		t.Fatalf("runner starts=%d", runner.count())
	}
	if _, err := store.GetCase(context.Background(), "case-invalid-environment"); !errors.Is(err, bughub.ErrCaseNotFound) {
		t.Fatalf("GetCase error=%v", err)
	}

	_, err = app.StartIncidentCase(StartIncidentCaseInput{
		CaseID: "case-mismatched-environment", BugID: bug.ID, BotKey: botKey, BotEnvironment: "test",
		IdempotencyKey: "start:mismatched-environment", ActorID: "user-1",
		InputJSON: map[string]any{"target_environment": "stage"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched input_json error=%v", err)
	}
	if runner.count() != 0 {
		t.Fatalf("runner starts after mismatched input=%d", runner.count())
	}
	if _, err := store.GetCase(context.Background(), "case-mismatched-environment"); !errors.Is(err, bughub.ErrCaseNotFound) {
		t.Fatalf("GetCase mismatched error=%v", err)
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

func TestIncidentWorkflowStartupRecoveryRetriesSameRuntimeAfterContextPreflightFailure(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workflows.db")
	store, err := bughub.OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for index, id := range []string{"case-recovery-first", "case-recovery-second"} {
		incident := bughub.IncidentCase{
			ID: id, BugID: "bug-" + id, Source: "zentao", SystemID: "base", Environment: "test",
			Status: bughub.CasePendingValidation, CycleNumber: 1, SelectedBotKey: fmt.Sprintf("base|codex-%d", index),
		}
		if err := store.CreateCase(ctx, incident); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateAttempt(ctx, bughub.PhaseAttempt{
			ID: id + "-attempt", CaseID: id, CycleNumber: 1, Phase: bughub.PhaseValidation,
			Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusQueued, AgentTarget: "codex",
			BotKey: incident.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	runner := &workflowBindingRunner{}
	failSecond := true
	factoryCalls := 0
	app := &App{
		workflowRoot: root,
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, Title: "loaded bug", Env: "test", SystemID: "base"}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			if failSecond && key == "base|codex-1" {
				return bughub.BotRef{}, errors.New("workspace mapping unavailable")
			}
			return bughub.BotRef{Key: key, Target: "codex", Path: "/installed/" + key, Env: "test"}, nil
		},
		workflowRuntimeFactory: func(store *bughub.CaseStore, _ *bughub.InvestigationStore) incidentWorkflowRuntime {
			factoryCalls++
			return incidentWorkflowRuntime{orchestrator: bughub.NewCaseOrchestrator(store, runner, nil, nil)}
		},
	}
	if err := app.initializeIncidentWorkflow(ctx); err == nil {
		t.Fatal("startup unexpectedly succeeded")
	}
	if factoryCalls != 1 || runner.count() != 0 {
		t.Fatalf("first startup factory calls=%d runner starts=%d", factoryCalls, runner.count())
	}
	if app.workflowStore == nil || app.workflowOrchestrator == nil {
		t.Fatal("failed recovery discarded the published runtime")
	}
	if _, err := app.workflowStore.GetCase(ctx, "case-recovery-first"); err != nil {
		t.Fatalf("retained store is unavailable: %v", err)
	}

	failSecond = false
	if err := app.initializeIncidentWorkflow(ctx); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	if factoryCalls != 1 || runner.count() != 2 {
		t.Fatalf("retry factory calls=%d runner starts=%d", factoryCalls, runner.count())
	}
	if err := app.initializeIncidentWorkflow(ctx); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if factoryCalls != 1 || runner.count() != 2 {
		t.Fatalf("duplicate retry factory calls=%d runner starts=%d", factoryCalls, runner.count())
	}

	incident, err := app.workflowStore.GetCase(ctx, "case-recovery-first")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := app.workflowOrchestrator.CompleteAttempt(ctx, bughub.CompleteAttemptCommand{
		CaseID: incident.ID, AttemptID: incident.CurrentAttemptID, ExpectedVersion: incident.Version,
		IdempotencyKey: "complete:recovery-first", ActorID: "agent", Outcome: bughub.PhaseOutcomeNeedsEvidence,
		OutputJSON: []byte(`{"gaps":["proof"]}`),
	})
	if err != nil || completed.Status != bughub.CaseWaitingEvidence {
		t.Fatalf("completion=%+v err=%v", completed, err)
	}
	persisted, err := app.workflowStore.GetCase(ctx, completed.ID)
	if err != nil || persisted.Status != bughub.CaseWaitingEvidence {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
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
