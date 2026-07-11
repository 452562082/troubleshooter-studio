package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
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
		if os.IsNotExist(err) {
			continue
		}
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

func TestCaseStoreConcurrentTransitionsOnlyOneWins(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-concurrent")

	start := make(chan struct{})
	type result struct {
		updated IncidentCase
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			updated, _, err := store.Transition(ctx, "case-concurrent", 1, CaseValidating, validEvent(index, "case-concurrent"))
			results <- result{updated: updated, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil && result.updated.Version == 2:
			wins++
		case errors.Is(result.err, ErrCaseVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected transition result: %+v", result)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
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
