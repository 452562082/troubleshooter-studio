package bughub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestCaseStore(t *testing.T) *CaseStore {
	t.Helper()
	store, err := OpenCaseStore(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatalf("OpenCaseStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestCreateCaseWithIdentityReturnsExistingOpenCaseForBug(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	firstCommand := CreateAndStartCaseCommand{
		CaseID: "case-a", IdempotencyKey: "create:case-a", ActorID: "alice",
		Bug: Bug{ID: "bug-840", Source: "zentao", SystemID: "base", Env: "test"},
		Bot: BotRef{Key: "base|codex", Target: "codex", Path: "/workspace/base", Env: "test"}, InputJSON: []byte(`{}`),
	}
	first, err := orchestrator.CreateAndStartCase(ctx, firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	requested := IncidentCase{ID: "case-b", BugID: first.BugID, Source: first.Source, SystemID: first.SystemID, Environment: first.Environment, Status: CasePendingValidation, CycleNumber: 1, SelectedBotKey: first.SelectedBotKey}
	creation := CaseCreation{Case: requested, IdempotencyKey: "create:case-b", ActorID: "bob", RequestJSON: json.RawMessage(`{"case_id":"case-b"}`)}
	result, err := store.CreateCaseWithIdentity(ctx, creation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Case.ID != first.ID || result.Replay || !result.ExistingOpen {
		t.Fatalf("result=%+v", result)
	}
	cases, err := store.ListCases(ctx)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	stored, err := store.GetCase(ctx, first.ID)
	if err != nil || stored.Version != first.Version {
		t.Fatalf("stored=%+v first=%+v err=%v", stored, first, err)
	}
	event, found, err := store.GetEventByIdempotencyKey(ctx, creation.IdempotencyKey)
	if err != nil || !found || event.EventType != "open_case_reused" {
		t.Fatalf("event=%+v found=%v err=%v", event, found, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE incident_cases SET status = ?, version = version + 1 WHERE id = ?`, CaseFixedVerified, first.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.CreateCaseWithIdentity(ctx, creation)
	if err != nil || !replayed.Replay || !replayed.ExistingOpen || replayed.Case != result.Case {
		t.Fatalf("replayed=%+v result=%+v err=%v", replayed, result, err)
	}
}

func TestResetRelationshipFieldsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := time.Now().UTC()
	old := IncidentCase{ID: "case-old", BugID: "bug-840", Status: CaseResetArchived, CycleNumber: 1, Version: 2, SupersededByCaseID: "case-new", ClosedAt: &closed}
	next := IncidentCase{ID: "case-new", BugID: "bug-840", Status: CasePendingValidation, CycleNumber: 1, Version: 1, ResetFromCaseID: "case-old"}
	if err := store.CreateCase(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCase(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	gotOld, err := reopened.GetCase(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotNext, err := reopened.GetCase(context.Background(), next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotOld.SupersededByCaseID != next.ID || gotNext.ResetFromCaseID != old.ID {
		t.Fatalf("old=%+v next=%+v", gotOld, gotNext)
	}
}

func TestCaseStoreTransitionIsTransactionalAndIdempotent(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	c := IncidentCase{ID: "case-1", BugID: "zentao-909", Status: CasePendingValidation, CycleNumber: 1, Version: 1}
	if err := store.CreateCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	e := TransitionEvent{ID: "event-1", CaseID: c.ID, IdempotencyKey: "validate:case-1:1"}
	updated, replay, err := store.Transition(ctx, c.ID, 1, CaseValidating, e)
	if err != nil || replay || updated.Version != 2 {
		t.Fatalf("updated=%+v replay=%v err=%v", updated, replay, err)
	}
	replayed, replay, err := store.Transition(ctx, c.ID, 1, CaseValidating, e)
	if err != nil || !replay || replayed.Version != 2 {
		t.Fatalf("replayed=%+v replay=%v err=%v", replayed, replay, err)
	}
	if events, _ := store.ListEvents(ctx, c.ID); len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	conflicting := e.Clone()
	conflicting.ID = "event-different"
	if _, _, err := store.Transition(ctx, c.ID, 1, CaseValidating, conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	conflicting = e.Clone()
	conflicting.PayloadJSON = json.RawMessage(`{"different":true}`)
	if _, _, err := store.Transition(ctx, c.ID, 1, CaseValidating, conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting payload replay error = %v", err)
	}
}

func TestCaseStoreSortsFixedWidthTimestampsChronologically(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	second := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	for _, incident := range []IncidentCase{
		{ID: "case-zero", BugID: "bug-zero", Status: CasePendingValidation, CycleNumber: 1, Version: 1, UpdatedAt: second},
		{ID: "case-later", BugID: "bug-later", Status: CasePendingValidation, CycleNumber: 1, Version: 1, UpdatedAt: second.Add(time.Nanosecond)},
	} {
		if err := store.CreateCase(ctx, incident); err != nil {
			t.Fatal(err)
		}
	}
	cases, err := store.ListCases(ctx)
	if err != nil || len(cases) != 2 || cases[0].ID != "case-later" {
		t.Fatalf("ListCases = %+v, err=%v", cases, err)
	}

	first := validEvent(1, "case-zero")
	first.CreatedAt = second
	if _, _, err := store.Transition(ctx, "case-zero", 1, CaseValidating, first); err != nil {
		t.Fatal(err)
	}
	next := validEvent(2, "case-zero")
	next.CreatedAt = second.Add(time.Nanosecond)
	if _, _, err := store.Transition(ctx, "case-zero", 2, CaseReproduced, next); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListEvents(ctx, "case-zero")
	if err != nil || len(events) != 2 || events[0].ID != first.ID || events[1].ID != next.ID {
		t.Fatalf("ListEvents = %+v, err=%v", events, err)
	}
}

func TestCaseStoreCreatesSecureSQLiteDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(root, "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}

	for name, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := store.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s = %d, want %d", name, got, want)
		}
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	for _, sqlitePath := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Stat(sqlitePath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("%s mode = %o, must not grant group/other access", filepath.Base(sqlitePath), got)
		}
	}
}

func TestCaseStoreSecuresExistingParentDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "permissive")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("existing directory mode = %o, want 700", got)
	}
}

func TestCaseStoreCaseRoundTripAndValidation(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	closedAt := time.Date(2026, 7, 11, 10, 0, 0, 123, time.UTC)
	incident := IncidentCase{
		ID: "case-roundtrip", BugID: "bug-1", Source: "zentao", SystemID: "shop",
		Environment: "test", Status: CaseFixedVerified, CycleNumber: 2,
		CurrentAttemptID: "attempt-2", SelectedBotKey: "bot-a", Version: 4,
		CreatedAt: closedAt.Add(-time.Hour), UpdatedAt: closedAt.Add(-time.Minute), ClosedAt: &closedAt,
	}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	wantClosedAt := closedAt
	*incident.ClosedAt = time.Time{}

	got, err := store.GetCase(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClosedAt == nil || !got.ClosedAt.Equal(wantClosedAt) || got.Version != 4 || got.SelectedBotKey != "bot-a" {
		t.Fatalf("GetCase = %+v", got)
	}
	*got.ClosedAt = time.Time{}
	again, err := store.GetCase(ctx, incident.ID)
	if err != nil || again.ClosedAt == nil || !again.ClosedAt.Equal(wantClosedAt) {
		t.Fatalf("second GetCase = %+v, err=%v", again, err)
	}
	listed, err := store.ListCases(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != incident.ID {
		t.Fatalf("ListCases = %+v, err=%v", listed, err)
	}
	if err := store.CreateCase(ctx, IncidentCase{}); err == nil {
		t.Fatal("CreateCase accepted invalid record")
	}
	if _, err := store.GetCase(ctx, "missing"); !errors.Is(err, ErrCaseNotFound) {
		t.Fatalf("GetCase missing error = %v", err)
	}
}

func TestCaseStoreAttemptLifecycleAndCloning(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-attempt")
	started := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	attempt := PhaseAttempt{
		ID: "attempt-1", CaseID: "case-attempt", CycleNumber: 1, Phase: PhaseValidation,
		Mode: AttemptReproduce, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot-a",
		InputJSON: json.RawMessage(`{"prompt":"original"}`), OutputJSON: json.RawMessage(`{}`), StartedAt: started,
	}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.InputJSON[11] = 'X'

	finished := started.Add(time.Minute)
	attempt.InputJSON = json.RawMessage(`{"prompt":"ignored-update"}`)
	attempt.OutputJSON = json.RawMessage(`{"result":"fixed"}`)
	attempt.Status = AttemptStatusSucceeded
	attempt.FinishedAt = &finished
	attempt.Usage = AgentUsage{InputTokens: 10, OutputTokens: 20, Duration: 3 * time.Second}
	if err := store.FinishAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	finishedAttempt := attempt.Clone()
	attempt.OutputJSON[11] = 'X'
	stored := readAttempt(t, store, attempt.ID)
	if string(stored.InputJSON) != `{"prompt":"original"}` || string(stored.OutputJSON) != `{"result":"fixed"}` {
		t.Fatalf("stored attempt JSON input=%s output=%s", stored.InputJSON, stored.OutputJSON)
	}
	if stored.Usage != (AgentUsage{InputTokens: 10, OutputTokens: 20, Duration: 3 * time.Second}) {
		t.Fatalf("stored usage = %+v", stored.Usage)
	}
	if err := store.FinishAttempt(ctx, finishedAttempt); err != nil {
		t.Fatalf("identical FinishAttempt replay: %v", err)
	}
	conflictingFinish := finishedAttempt.Clone()
	conflictingFinish.Status = AttemptStatusFailed
	conflictingFinish.OutputJSON = json.RawMessage(`{"result":"late failure"}`)
	if err := store.FinishAttempt(ctx, conflictingFinish); !errors.Is(err, ErrAttemptAlreadyFinished) {
		t.Fatalf("conflicting FinishAttempt error = %v", err)
	}
	stored = readAttempt(t, store, attempt.ID)
	if stored.Status != AttemptStatusSucceeded || string(stored.OutputJSON) != `{"result":"fixed"}` {
		t.Fatalf("late callback overwrote terminal attempt: %+v", stored)
	}
	nilTimeAttempt := validFixAttempt("attempt-nil-finished", "case-attempt")
	if err := store.CreateAttempt(ctx, nilTimeAttempt); err != nil {
		t.Fatal(err)
	}
	nilTimeAttempt.Status = AttemptStatusSucceeded
	nilTimeAttempt.OutputJSON = json.RawMessage(`{"result":"done"}`)
	if err := store.FinishAttempt(ctx, nilTimeAttempt); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, nilTimeAttempt); err != nil {
		t.Fatalf("nil FinishedAt replay: %v", err)
	}
	if err := store.CreateAttempt(ctx, PhaseAttempt{}); err == nil {
		t.Fatal("CreateAttempt accepted invalid record")
	}
	legacy := attempt
	legacy.ID = "attempt-legacy"
	legacy.Phase = PhaseLegacy
	legacy.Mode = ""
	if err := store.CreateAttempt(ctx, legacy); err == nil {
		t.Fatal("CreateAttempt accepted migration-only legacy record")
	}
	attempt.Status = AttemptStatusRunning
	if err := store.FinishAttempt(ctx, attempt); err == nil {
		t.Fatal("FinishAttempt accepted non-terminal status")
	}
}

func TestCaseStorePersistsAndReplacesValidationRecipe(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-recipe")
	attempt := PhaseAttempt{
		ID: "attempt-recipe", CaseID: "case-recipe", CycleNumber: 1, Phase: PhaseValidation,
		Mode: AttemptReproduce, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot-a",
		InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`), StartedAt: time.Now().UTC(),
	}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	planSHA, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	scenarioOne := strings.Repeat("a", 64)
	stored, err := store.StoreValidationRecipe(ctx, ValidationRecipe{
		CaseID: attempt.CaseID, ScenarioSHA256: scenarioOne, PlanSHA256: planSHA,
		Plan: plan, SourceAttemptID: attempt.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.CaseID != attempt.CaseID || stored.ScenarioSHA256 != scenarioOne || stored.PlanSHA256 != planSHA || stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("stored recipe=%+v", stored)
	}

	loaded, found, err := store.GetValidationRecipe(ctx, attempt.CaseID)
	if err != nil || !found || loaded.PlanSHA256 != planSHA || loaded.Plan.StartURL != plan.StartURL {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
	scenarioTwo := strings.Repeat("b", 64)
	stored.ScenarioSHA256 = scenarioTwo
	stored.UpdatedAt = stored.UpdatedAt.Add(time.Second)
	replaced, err := store.StoreValidationRecipe(ctx, stored)
	if err != nil || replaced.ScenarioSHA256 != scenarioTwo || !replaced.CreatedAt.Equal(stored.CreatedAt) {
		t.Fatalf("replaced=%+v err=%v", replaced, err)
	}
	if _, err := store.StoreValidationRecipe(ctx, ValidationRecipe{CaseID: attempt.CaseID, ScenarioSHA256: "bad", PlanSHA256: planSHA, Plan: plan, SourceAttemptID: attempt.ID}); err == nil {
		t.Fatal("StoreValidationRecipe accepted an invalid scenario digest")
	}
	createTestCase(t, store, "case-recipe-other")
	if _, err := store.StoreValidationRecipe(ctx, ValidationRecipe{
		CaseID: "case-recipe-other", ScenarioSHA256: scenarioOne, PlanSHA256: planSHA,
		Plan: plan, SourceAttemptID: attempt.ID,
	}); err == nil || !strings.Contains(err.Error(), "same case") {
		t.Fatalf("StoreValidationRecipe cross-case source error=%v", err)
	}
}

func TestCaseStoreRecordsValidatedStructuredRecords(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-records")
	attempt := validFixAttempt("attempt-fix", "case-records")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	approval := Approval{
		ID: "approval-1", CaseID: "case-records", Kind: ApprovalMergeEnvironmentBranch,
		Actor: "user-1", CaseVersion: 1, ScopeJSON: json.RawMessage(`{"environment":"test"}`),
		FixCommits: map[string]string{"api": "abc123"}, TargetBranches: map[string]string{"api": "env/test"},
	}
	if err := store.RecordApproval(ctx, approval, "approval:case-records:1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordApproval(ctx, approval, "approval:case-records:1"); err != nil {
		t.Fatalf("approval replay: %v", err)
	}
	conflictingApproval := approval.Clone()
	conflictingApproval.FixCommits["api"] = "different"
	if err := store.RecordApproval(ctx, conflictingApproval, "approval:case-records:1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting approval replay error = %v", err)
	}
	conflictingApproval = approval.Clone()
	conflictingApproval.ApprovedAt = time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	if err := store.RecordApproval(ctx, conflictingApproval, "approval:case-records:1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting approval timestamp error = %v", err)
	}
	approval.FixCommits["api"] = "mutated"
	approval.ScopeJSON[2] = 'X'

	change := CodeChange{
		ID: "change-1", CaseID: "case-records", AttemptID: attempt.ID, Repo: "api",
		BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "abc123",
		TestEvidence: json.RawMessage(`["go test ./..."]`), TargetEnvironmentBranch: "env/test",
	}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	change.TestEvidence[2] = 'X'

	verified := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	observation := DeploymentObservation{
		ID: "deploy-1", CaseID: "case-records", Environment: "test",
		ExpectedCommits: map[string]string{"api": "abc123"}, VerificationSource: "k8s",
		ObservedImages: map[string]string{"api": "api:v1"}, ObservedCommits: map[string]string{"api": "abc123"},
		VerifiedAt: &verified, Result: DeploymentResultMatched,
	}
	if err := store.RecordDeploymentObservation(ctx, observation, "deploy:case-records:1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentObservation(ctx, observation, "deploy:case-records:1"); err != nil {
		t.Fatalf("deployment replay: %v", err)
	}
	conflictingObservation := observation.Clone()
	conflictingObservation.ID = "deploy-different"
	if err := store.RecordDeploymentObservation(ctx, conflictingObservation, "deploy:case-records:1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting deployment replay error = %v", err)
	}
	conflictingObservation = observation.Clone()
	notified := time.Date(2026, 7, 11, 11, 30, 0, 0, time.UTC)
	conflictingObservation.UserNotifiedAt = &notified
	if err := store.RecordDeploymentObservation(ctx, conflictingObservation, "deploy:case-records:1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting deployment timestamp error = %v", err)
	}
	observation.ExpectedCommits["api"] = "mutated"
	observation.ObservedImages["api"] = "mutated"

	var scope, fixes, evidence, expected, images string
	if err := store.db.QueryRow(`SELECT scope_json, fix_commits_json FROM approvals WHERE id = ?`, "approval-1").Scan(&scope, &fixes); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT test_evidence_json FROM code_changes WHERE id = ?`, "change-1").Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT expected_commits_json, observed_images_json FROM deployment_observations WHERE id = ?`, "deploy-1").Scan(&expected, &images); err != nil {
		t.Fatal(err)
	}
	if scope != `{"environment":"test"}` || fixes != `{"api":"abc123"}` || evidence != `["go test ./..."]` || expected != `{"api":"abc123"}` || images != `{"api":"api:v1"}` {
		t.Fatalf("stored values scope=%s fixes=%s evidence=%s expected=%s images=%s", scope, fixes, evidence, expected, images)
	}
	if err := store.RecordApproval(ctx, Approval{}, "bad"); err == nil {
		t.Fatal("RecordApproval accepted invalid record")
	}
	if err := store.RecordCodeChange(ctx, CodeChange{}); err == nil {
		t.Fatal("RecordCodeChange accepted invalid record")
	}
	if err := store.RecordDeploymentObservation(ctx, DeploymentObservation{}, "bad"); err == nil {
		t.Fatal("RecordDeploymentObservation accepted invalid record")
	}
}

func TestCaseStoreConcurrentIdempotentReplayAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	firstStore, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	createTestCase(t, firstStore, "case-idempotent-race")

	start := make(chan struct{})
	type result struct {
		version int64
		replay  bool
		err     error
	}
	results := make(chan result, 2)
	stores := []*CaseStore{firstStore, secondStore}
	for _, store := range stores {
		go func(store *CaseStore) {
			<-start
			updated, replay, err := store.Transition(context.Background(), "case-idempotent-race", 1, CaseValidating, validEvent(1, "case-idempotent-race"))
			results <- result{version: updated.Version, replay: replay, err: err}
		}(store)
	}
	close(start)
	writes, replays := 0, 0
	for range stores {
		result := <-results
		if result.err != nil || result.version != 2 {
			t.Fatalf("result = %+v", result)
		}
		if result.replay {
			replays++
		} else {
			writes++
		}
	}
	if writes != 1 || replays != 1 {
		t.Fatalf("writes=%d replays=%d", writes, replays)
	}
}

func TestCaseStoreTransitionRollsBackSnapshotWhenEventInsertFails(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-first")
	createTestCase(t, store, "case-rollback")
	first := validEvent(1, "case-first")
	first.ID = "duplicate-event-id"
	if _, _, err := store.Transition(ctx, "case-first", 1, CaseValidating, first); err != nil {
		t.Fatal(err)
	}
	second := validEvent(2, "case-rollback")
	second.ID = first.ID
	if _, _, err := store.Transition(ctx, "case-rollback", 1, CaseValidating, second); err == nil {
		t.Fatal("Transition succeeded with duplicate event ID")
	}
	got, err := store.GetCase(ctx, "case-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CasePendingValidation || got.Version != 1 {
		t.Fatalf("snapshot diverged after rollback: %+v", got)
	}
	if events, err := store.ListEvents(ctx, "case-rollback"); err != nil || len(events) != 0 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestCaseStoreReopenPreservesHistoryAndEventOutputsAreCloned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	createTestCase(t, store, "case-reopen")
	event := validEvent(1, "case-reopen")
	event.PayloadJSON = json.RawMessage(`{"proof":"original"}`)
	if _, _, err := store.Transition(ctx, "case-reopen", 1, CaseValidating, event); err != nil {
		t.Fatal(err)
	}
	event.PayloadJSON[10] = 'X'
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.ListEvents(ctx, "case-reopen")
	if err != nil || len(events) != 1 || string(events[0].PayloadJSON) != `{"proof":"original"}` {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events[0].PayloadJSON[10] = 'X'
	again, err := reopened.ListEvents(ctx, "case-reopen")
	if err != nil || string(again[0].PayloadJSON) != `{"proof":"original"}` {
		t.Fatalf("second events=%+v err=%v", again, err)
	}
}

func TestCaseStoreTransitionReplayBindsFullRequestAndReturnsCommittedSnapshot(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-exact-replay")
	firstEvent := validEvent(1, "case-exact-replay")
	firstEvent.PayloadJSON = json.RawMessage(`{"attempt_id":"attempt-1"}`)
	first, replay, err := store.Transition(ctx, "case-exact-replay", 1, CaseValidating, firstEvent)
	if err != nil || replay || first.Version != 2 {
		t.Fatalf("first=%+v replay=%v err=%v", first, replay, err)
	}
	secondEvent := validEvent(2, "case-exact-replay")
	if _, _, err := store.Transition(ctx, "case-exact-replay", 2, CaseReproduced, secondEvent); err != nil {
		t.Fatal(err)
	}

	replayed, replay, err := store.Transition(ctx, "case-exact-replay", 1, CaseValidating, firstEvent)
	if err != nil || !replay || replayed.Status != CaseValidating || replayed.Version != 2 {
		t.Fatalf("replayed=%+v replay=%v err=%v", replayed, replay, err)
	}
	for name, tc := range map[string]struct {
		expectedVersion int64
		mutate          func(*TransitionEvent)
	}{
		"expected version": {expectedVersion: 2},
		"actor": {expectedVersion: 1, mutate: func(event *TransitionEvent) {
			event.ActorID = "other-user"
		}},
		"payload": {expectedVersion: 1, mutate: func(event *TransitionEvent) {
			event.PayloadJSON = json.RawMessage(`{"attempt_id":"other"}`)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			event := firstEvent.Clone()
			if tc.mutate != nil {
				tc.mutate(&event)
			}
			if _, _, err := store.Transition(ctx, "case-exact-replay", tc.expectedVersion, CaseValidating, event); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCaseStoreTransitionFingerprintPreservesExactPayloadBytes(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-payload-bytes")
	event := validEvent(1, "case-payload-bytes")
	event.PayloadJSON = json.RawMessage(`{"value":1}`)
	if _, replay, err := store.Transition(ctx, "case-payload-bytes", 1, CaseValidating, event); err != nil || replay {
		t.Fatalf("first replay=%v err=%v", replay, err)
	}
	if _, replay, err := store.Transition(ctx, "case-payload-bytes", 1, CaseValidating, event); err != nil || !replay {
		t.Fatalf("identical replay=%v err=%v", replay, err)
	}
	whitespaceDistinct := event.Clone()
	whitespaceDistinct.PayloadJSON = json.RawMessage(`{ "value": 1 }`)
	if _, _, err := store.Transition(ctx, "case-payload-bytes", 1, CaseValidating, whitespaceDistinct); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("whitespace-distinct payload error=%v", err)
	}
}

func TestCaseStoreTransitionWithUpdateIsAtomicAndIdempotent(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-patch")
	attemptID := "attempt-current"
	cycle := 2
	closedAt := time.Date(2026, 7, 11, 14, 0, 0, 0, time.UTC)
	update := CaseSnapshotUpdate{
		CurrentAttemptID: &attemptID,
		CycleNumber:      &cycle,
		ClosedAtSet:      true,
		ClosedAt:         &closedAt,
	}
	event := validEvent(1, "case-patch")
	updated, replay, err := store.TransitionWithUpdate(ctx, "case-patch", 1, CaseValidating, update, event)
	if err != nil || replay || updated.CurrentAttemptID != attemptID || updated.CycleNumber != cycle ||
		updated.ClosedAt == nil || !updated.ClosedAt.Equal(closedAt) {
		t.Fatalf("updated=%+v replay=%v err=%v", updated, replay, err)
	}
	replayed, replay, err := store.TransitionWithUpdate(ctx, "case-patch", 1, CaseValidating, update, event)
	if err != nil || !replay || replayed.Version != updated.Version || replayed.CurrentAttemptID != attemptID {
		t.Fatalf("replayed=%+v replay=%v err=%v", replayed, replay, err)
	}
	differentCycle := 3
	conflictingUpdate := update
	conflictingUpdate.CycleNumber = &differentCycle
	if _, _, err := store.TransitionWithUpdate(ctx, "case-patch", 1, CaseValidating, conflictingUpdate, event); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting update error = %v", err)
	}
	stored, err := store.GetCase(ctx, "case-patch")
	if err != nil || stored.Version != 2 || stored.CycleNumber != cycle || stored.CurrentAttemptID != attemptID {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestCaseStoreTransitionWithUpdateSelectedBotSetClearAndNoChange(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	for index, tc := range []struct {
		name             string
		initial          string
		update           *string
		want             string
		conflictNoChange bool
	}{
		{name: "set", initial: "bot-old", update: stringPointer("bot-new"), want: "bot-new"},
		{name: "clear", initial: "bot-old", update: stringPointer(""), want: "", conflictNoChange: true},
		{name: "no change", initial: "bot-old", update: nil, want: "bot-old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseID := "case-bot-" + strings.ReplaceAll(tc.name, " ", "-")
			if err := store.CreateCase(ctx, IncidentCase{ID: caseID, BugID: "bug-" + caseID,
				Status: CasePendingValidation, CycleNumber: 1, Version: 1, SelectedBotKey: tc.initial}); err != nil {
				t.Fatal(err)
			}
			update := CaseSnapshotUpdate{SelectedBotKey: tc.update}
			event := validEvent(index+20, caseID)
			updated, replay, err := store.TransitionWithUpdate(ctx, caseID, 1, CaseValidating, update, event)
			if err != nil || replay || updated.SelectedBotKey != tc.want {
				t.Fatalf("updated=%+v replay=%v err=%v", updated, replay, err)
			}
			stored, err := store.GetCase(ctx, caseID)
			if err != nil || stored.SelectedBotKey != tc.want {
				t.Fatalf("stored=%+v err=%v", stored, err)
			}
			if tc.conflictNoChange {
				if _, _, err := store.TransitionWithUpdate(ctx, caseID, 1, CaseValidating, CaseSnapshotUpdate{}, event); !errors.Is(err, ErrIdempotencyConflict) {
					t.Fatalf("clear vs no-change error=%v", err)
				}
			}
		})
	}
}

func TestCaseStoreInitializesAndMigratesVersionedSchema(t *testing.T) {
	t.Run("new database", func(t *testing.T) {
		store := openTestCaseStore(t)
		var version int
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != workflowStoreSchemaVersion {
			t.Fatalf("user_version=%d", version)
		}
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE key = ?`, workflowStoreSchemaV1Key).Scan(&count); err != nil || count != 1 {
			t.Fatalf("migration count=%d err=%v", count, err)
		}
		assertTableColumns(t, store.db, "transition_events", "request_fingerprint", "result_case_json")
		assertTableColumns(t, store.db, "incident_cases", "reset_from_case_id", "superseded_by_case_id")
		assertTableColumns(t, store.db, "reset_cancellation_operations", "reset_key", "case_id", "attempt_id", "request_fingerprint", "status", "claim_token", "outcome_code", "created_at", "updated_at")
		assertTableColumns(t, store.db, "browser_recovery_operations", "idempotency_key", "operation", "case_id", "attempt_id", "expected_error_code", "cycle_number", "expected_version", "actor_id", "request_fingerprint", "status", "claim_token", "outcome_code", "result_case_json", "created_at", "updated_at")
		assertTableColumns(t, store.db, "validation_recipes", "case_id", "scenario_sha256", "plan_sha256", "plan_json", "source_attempt_id", "created_at", "updated_at")
		var cancellationDDL string
		if err := store.db.QueryRow(`SELECT lower(sql) FROM sqlite_master WHERE type='table' AND name='reset_cancellation_operations'`).Scan(&cancellationDDL); err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"primary key", "unique(case_id, attempt_id)", "status in ('pending','claimed','succeeded','failed')", "length(request_fingerprint) = 64", "request_fingerprint not glob '*[^0-9a-f]*'"} {
			if !strings.Contains(strings.ReplaceAll(cancellationDDL, "\n", " "), required) {
				t.Fatalf("reset cancellation schema missing %q: %s", required, cancellationDDL)
			}
		}
		var cancellationIndexTable string
		if err := store.db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='idx_reset_cancellations_status_updated'`).Scan(&cancellationIndexTable); err != nil || cancellationIndexTable != "reset_cancellation_operations" {
			t.Fatalf("reset cancellation index table=%q err=%v", cancellationIndexTable, err)
		}
		var browserRecoveryIndexTable string
		if err := store.db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='idx_browser_recovery_status_updated'`).Scan(&browserRecoveryIndexTable); err != nil || browserRecoveryIndexTable != "browser_recovery_operations" {
			t.Fatalf("browser recovery index table=%q err=%v", browserRecoveryIndexTable, err)
		}
		var validationRecipeIndexTable string
		if err := store.db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='idx_validation_recipes_scenario'`).Scan(&validationRecipeIndexTable); err != nil || validationRecipeIndexTable != "validation_recipes" {
			t.Fatalf("validation recipe index table=%q err=%v", validationRecipeIndexTable, err)
		}
		var browserRecoveryDDL string
		if err := store.db.QueryRow(`SELECT lower(sql) FROM sqlite_master WHERE type='table' AND name='browser_recovery_operations'`).Scan(&browserRecoveryDDL); err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"effect_succeeded", "effect_failed", "outcome_uncertain", "continued"} {
			if !strings.Contains(browserRecoveryDDL, required) {
				t.Fatalf("browser recovery schema missing %q: %s", required, browserRecoveryDDL)
			}
		}
	})

	t.Run("pre-release unversioned schema with explicit event time is rejected without mutation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private", "workflows.db")
		db := openRawWorkflowDB(t, path)
		if _, err := db.Exec(legacyWorkflowStoreSchema); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
		if _, err := db.Exec(`INSERT INTO incident_cases (
			id, bug_id, status, cycle_number, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`, "case-legacy-schema", "bug-legacy", CaseValidating,
			1, 2, formatStoreTime(createdAt.Add(-time.Hour)), formatStoreTime(createdAt)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO transition_events (id, case_id, from_status, to_status,
			event_type, actor_type, actor_id, idempotency_key, payload_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-event-1", "case-legacy-schema",
			CasePendingValidation, CaseValidating, "validation_started", "user", "user-1",
			"legacy-key-1", `{ "explicit": true }`, formatStoreTime(createdAt)); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
			t.Fatalf("error=%v", err)
		}
		db = openRawWorkflowDB(t, path)
		defer db.Close()
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
			t.Fatalf("user_version=%d err=%v", version, err)
		}
		var createdAtStored, payloadStored string
		if err := db.QueryRow(`SELECT created_at, payload_json FROM transition_events WHERE id = 'legacy-event-1'`).Scan(&createdAtStored, &payloadStored); err != nil {
			t.Fatal(err)
		}
		if createdAtStored != formatStoreTime(createdAt) || payloadStored != `{ "explicit": true }` {
			t.Fatalf("event mutated: created_at=%q payload=%q", createdAtStored, payloadStored)
		}
		columns := tableColumnNames(t, db, "transition_events")
		if columns["request_fingerprint"] || columns["result_case_json"] {
			t.Fatalf("legacy schema was partially mutated: %+v", columns)
		}
	})
}

func TestClaimRunnableAttemptAtomicallyBindsFixCheckpoint(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-atomic-run-claim", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	checkpoint := FixCheckpoint{AttemptID: attempt.ID, CaseID: attempt.CaseID, StagingLocator: attempt.ID + "-claim-a"}
	if err := store.ClaimRunnableAttempt(context.Background(), AttemptRunClaim{Attempt: attempt, ClaimToken: "claim-a", Checkpoint: &checkpoint}); err != nil {
		t.Fatal(err)
	}
	if valid, err := store.ValidateAttemptRunClaim(context.Background(), attempt, "claim-a"); err != nil || !valid {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
	storedCheckpoint, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID)
	if err != nil || !found || storedCheckpoint.StagingLocator != checkpoint.StagingLocator {
		t.Fatalf("checkpoint=%+v found=%v err=%v", storedCheckpoint, found, err)
	}
	second := checkpoint
	second.StagingLocator = attempt.ID + "-claim-b"
	if err := store.ClaimRunnableAttempt(context.Background(), AttemptRunClaim{Attempt: attempt, ClaimToken: "claim-b", Checkpoint: &second}); !errors.Is(err, ErrAttemptRunClaimConflict) {
		t.Fatalf("second claim err=%v", err)
	}
	storedCheckpoint, found, err = store.GetFixCheckpoint(context.Background(), attempt.ID)
	if err != nil || !found || storedCheckpoint.StagingLocator != checkpoint.StagingLocator {
		t.Fatalf("checkpoint changed after losing claim: %+v found=%v err=%v", storedCheckpoint, found, err)
	}
	if err := store.ReleaseAttemptRunClaim(context.Background(), attempt.ID, attempt.CaseID, "claim-a"); err != nil {
		t.Fatal(err)
	}
	if valid, err := store.ValidateAttemptRunClaim(context.Background(), attempt, "claim-a"); err != nil || valid {
		t.Fatalf("released valid=%v err=%v", valid, err)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || found {
		t.Fatalf("released checkpoint found=%v err=%v", found, err)
	}
}

func TestCaseStoreRejectsPartialAndUnknownSchemas(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{name: "partial", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`CREATE TABLE incident_cases (id TEXT PRIMARY KEY)`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown version", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`PRAGMA user_version=99`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "workflows.db")
			db := openRawWorkflowDB(t, path)
			tc.setup(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCaseStoreRejectsViewOnlyUnversionedSchemaWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	db := openRawWorkflowDB(t, path)
	const viewSQL = `CREATE VIEW stray_workflow_view AS SELECT 1 AS value`
	if _, err := db.Exec(viewSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
		t.Fatalf("error=%v", err)
	}
	db = openRawWorkflowDB(t, path)
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
	var objectType, storedSQL string
	if err := db.QueryRow(`SELECT type, sql FROM sqlite_master WHERE name = 'stray_workflow_view'`).Scan(&objectType, &storedSQL); err != nil {
		t.Fatal(err)
	}
	if objectType != "view" || storedSQL != viewSQL {
		t.Fatalf("view mutated: type=%q sql=%q", objectType, storedSQL)
	}
	var workflowTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'incident_cases'`).Scan(&workflowTables); err != nil || workflowTables != 0 {
		t.Fatalf("workflow tables=%d err=%v", workflowTables, err)
	}
}

func TestCaseStoreRejectsVersionedSchemaWithMissingConstraintIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db := openRawWorkflowDB(t, path)
	if _, err := db.Exec(`DROP INDEX idx_events_case_created`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
		t.Fatalf("error=%v", err)
	}
}

func TestCaseStoreRejectsUnversionedSchemaWithMissingRequiredIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	db := openRawWorkflowDB(t, path)
	if _, err := db.Exec(legacyWorkflowStoreSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_attempts_case_started`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
		t.Fatalf("error=%v", err)
	}
}

func TestCaseStoreRejectsUnversionedSchemaWithMissingForeignKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	db := openRawWorkflowDB(t, path)
	if _, err := db.Exec(legacyWorkflowStoreSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE phase_attempts;
		CREATE TABLE phase_attempts (
		  id TEXT PRIMARY KEY, case_id TEXT NOT NULL, cycle_number INTEGER NOT NULL,
		  phase TEXT NOT NULL, mode TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
		  agent_target TEXT NOT NULL DEFAULT '', bot_key TEXT NOT NULL DEFAULT '',
		  input_json TEXT NOT NULL DEFAULT '{}', output_json TEXT NOT NULL DEFAULT '{}',
		  parent_attempt_id TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT,
		  error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
		  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
		  duration_nanos INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_attempts_case_started ON phase_attempts(case_id, started_at);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCaseStore(path); !errors.Is(err, ErrUnsupportedWorkflowSchema) {
		t.Fatalf("error=%v", err)
	}
}

func TestCaseStoreTypedRecoveryReaders(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-readers")
	running := validFixAttempt("attempt-running", "case-readers")
	interrupted := validFixAttempt("attempt-interrupted", "case-readers")
	interrupted.Status = AttemptStatusInterrupted
	finished := time.Now().UTC()
	interrupted.FinishedAt = &finished
	for _, attempt := range []PhaseAttempt{running, interrupted} {
		if err := store.CreateAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetAttempt(ctx, running.ID)
	if err != nil || got.ID != running.ID {
		t.Fatalf("GetAttempt=%+v err=%v", got, err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: "case-readers", Statuses: []AttemptStatus{AttemptStatusRunning, AttemptStatusInterrupted}})
	if err != nil || len(attempts) != 2 {
		t.Fatalf("ListAttempts=%+v err=%v", attempts, err)
	}

	approval := Approval{ID: "approval-reader", CaseID: "case-readers", Kind: ApprovalStartFix,
		Actor: "user", CaseVersion: 1, ScopeJSON: json.RawMessage(`{"root_cause_attempt_id":"attempt-running"}`)}
	if err := store.RecordApproval(ctx, approval, "approval-reader-key"); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: "change-reader", CaseID: "case-readers", AttemptID: running.ID,
		Repo: "api", BaseBranch: "main", FixBranch: "fix/x", FixCommit: "abc", TestEvidence: json.RawMessage(`[]`)}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	observation := DeploymentObservation{ID: "deployment-reader", CaseID: "case-readers", Environment: "test",
		ExpectedCommits: map[string]string{"api": "abc"}, VerificationSource: "k8s", Result: DeploymentResultUnavailable}
	if err := store.RecordDeploymentObservation(ctx, observation, "deployment-reader-key"); err != nil {
		t.Fatal(err)
	}
	approvals, err := store.ListApprovals(ctx, "case-readers")
	if err != nil || len(approvals) != 1 || approvals[0].ID != approval.ID {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	changes, err := store.ListCodeChanges(ctx, "case-readers")
	if err != nil || len(changes) != 1 || changes[0].ID != change.ID {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	observations, err := store.ListDeploymentObservations(ctx, "case-readers")
	if err != nil || len(observations) != 1 || observations[0].ID != observation.ID {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	approvals[0].ScopeJSON[2] = 'X'
	changes[0].TestEvidence = append(changes[0].TestEvidence, 'X')
	observations[0].ExpectedCommits["api"] = "mutated"
	again, _ := store.ListApprovals(ctx, "case-readers")
	if string(again[0].ScopeJSON) != string(approval.ScopeJSON) {
		t.Fatal("approval reader leaked mutable JSON")
	}
	changesAgain, _ := store.ListCodeChanges(ctx, "case-readers")
	if string(changesAgain[0].TestEvidence) != string(change.TestEvidence) {
		t.Fatal("code change reader leaked mutable JSON")
	}
	observationsAgain, _ := store.ListDeploymentObservations(ctx, "case-readers")
	if observationsAgain[0].ExpectedCommits["api"] != "abc" {
		t.Fatal("deployment reader leaked mutable map")
	}
}

func TestCaseStoreRejectsNonNilZeroOptionalTimestamps(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	zero := time.Time{}
	incident := IncidentCase{ID: "case-zero-time", BugID: "bug", Status: CasePendingValidation, CycleNumber: 1, ClosedAt: &zero}
	if err := store.CreateCase(ctx, incident); err == nil {
		t.Fatal("CreateCase accepted non-nil zero ClosedAt")
	}
	createTestCase(t, store, "case-zero-time")
	attempt := validFixAttempt("attempt-zero-time", "case-zero-time")
	attempt.FinishedAt = &zero
	if err := store.CreateAttempt(ctx, attempt); err == nil {
		t.Fatal("CreateAttempt accepted non-nil zero FinishedAt")
	}
	observation := DeploymentObservation{ID: "deployment-zero-time", CaseID: "case-zero-time", Environment: "test",
		ExpectedCommits: map[string]string{"api": "abc"}, UserNotifiedAt: &zero, VerificationSource: "manual", Result: DeploymentResultUnavailable}
	if err := store.RecordDeploymentObservation(ctx, observation, "deployment-zero-time"); err == nil {
		t.Fatal("RecordDeploymentObservation accepted non-nil zero UserNotifiedAt")
	}
}

func TestCaseStoreRepairsDatabaseAndSidecarPermissionsOnReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(root, "workflows.db")
	initial, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	createTestCase(t, initial, "case-permissions")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path + "-wal", path + "-shm"} {
		if err := os.WriteFile(candidate, nil, 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(candidate, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode=%o", filepath.Base(candidate), got)
		}
	}
}

func TestCaseStoreIndependentStoresDistinctTransitionsOnlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "workflows.db")
	first, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	createTestCase(t, first, "case-independent-writers")
	start := make(chan struct{})
	results := make(chan error, 2)
	for index, store := range []*CaseStore{first, second} {
		go func(index int, store *CaseStore) {
			<-start
			_, _, err := store.Transition(context.Background(), "case-independent-writers", 1, CaseValidating, validEvent(index+10, "case-independent-writers"))
			results <- err
		}(index, store)
	}
	close(start)
	wins, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, ErrCaseVersionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func openRawWorkflowDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func assertTableColumns(t *testing.T, db *sql.DB, table string, columns ...string) {
	t.Helper()
	found := tableColumnNames(t, db, table)
	for _, column := range columns {
		if !found[column] {
			t.Fatalf("table %s missing column %s", table, column)
		}
	}
}

func tableColumnNames(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	return found
}

func stringPointer(value string) *string {
	return &value
}

func createTestCase(t *testing.T, store *CaseStore, id string) {
	t.Helper()
	if err := store.CreateCase(context.Background(), IncidentCase{
		ID: id, BugID: "bug-" + id, Status: CasePendingValidation, CycleNumber: 1, Version: 1,
	}); err != nil {
		t.Fatalf("CreateCase: %v", err)
	}
}

func validEvent(index int, caseID string) TransitionEvent {
	return TransitionEvent{
		ID: "event-" + time.Unix(int64(index), 0).UTC().Format("150405"), CaseID: caseID,
		EventType: "validation_started", ActorType: "user", ActorID: "user-1",
		IdempotencyKey: caseID + ":" + time.Unix(int64(index), 0).UTC().Format("150405"),
		PayloadJSON:    json.RawMessage(`{}`),
	}
}

func validFixAttempt(id, caseID string) PhaseAttempt {
	return PhaseAttempt{
		ID: id, CaseID: caseID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusRunning,
		InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`), StartedAt: time.Now().UTC(),
	}
}

func readAttempt(t *testing.T, store *CaseStore, id string) PhaseAttempt {
	t.Helper()
	var attempt PhaseAttempt
	var input, output, started, finished string
	var finishedNull *string
	var duration int64
	err := store.db.QueryRow(`SELECT id, case_id, cycle_number, phase, mode, status,
		agent_target, bot_key, input_json, output_json, parent_attempt_id, started_at,
		finished_at, error_code, error_message, input_tokens, output_tokens, duration_nanos
		FROM phase_attempts WHERE id = ?`, id).Scan(
		&attempt.ID, &attempt.CaseID, &attempt.CycleNumber, &attempt.Phase, &attempt.Mode, &attempt.Status,
		&attempt.AgentTarget, &attempt.BotKey, &input, &output, &attempt.ParentAttemptID, &started,
		&finishedNull, &attempt.ErrorCode, &attempt.ErrorMessage, &attempt.Usage.InputTokens,
		&attempt.Usage.OutputTokens, &duration,
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt.InputJSON = json.RawMessage(input)
	attempt.OutputJSON = json.RawMessage(output)
	attempt.StartedAt, err = parseStoreTime(started)
	if err != nil {
		t.Fatal(err)
	}
	if finishedNull != nil {
		finished = *finishedNull
		parsed, err := parseStoreTime(finished)
		if err != nil {
			t.Fatal(err)
		}
		attempt.FinishedAt = &parsed
	}
	attempt.Usage.Duration = time.Duration(duration)
	return attempt.Clone()
}

func TestCaseStoreSaveCompletionIntentIfRunningIsCASAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-completion-intent")
	attempt := validRunningAttempt("attempt-completion-intent", "case-completion-intent")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	command := CompleteAttemptCommand{CaseID: attempt.CaseID, AttemptID: attempt.ID, ExpectedVersion: 1, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "validator", Outcome: PhaseOutcomeNotReproduced, OutputJSON: []byte(`{"result":"not-reproduced"}`)}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	stored, err := store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := parseCompletionIntent(stored.OutputJSON)
	if err != nil || !found || got.Outcome != command.Outcome || string(got.OutputJSON) != string(command.OutputJSON) {
		t.Fatalf("intent=%+v found=%v err=%v raw=%s", got, found, err, stored.OutputJSON)
	}
	divergent := command
	divergent.Outcome = PhaseOutcomeReproduced
	if err := store.SaveCompletionIntentIfRunning(ctx, divergent); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("divergent save error = %v", err)
	}
}
