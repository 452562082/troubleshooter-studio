package bughub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func eligibleBrowserRecoveryOperationFixture(t *testing.T, store *CaseStore, suffix string, operation BrowserRecoveryOperationKind) (IncidentCase, PhaseAttempt, BrowserRecoveryOperationRequest) {
	t.Helper()
	code := "browser_login_required"
	if operation == BrowserRecoveryRepair {
		code = "browser_runtime_broken"
	}
	attemptID := "attempt-browser-" + suffix
	incident := IncidentCase{
		ID: "case-browser-" + suffix, BugID: "bug-browser-" + suffix,
		Status: CaseWaitingEvidence, CycleNumber: 1, CurrentAttemptID: attemptID, Version: 1,
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	attempt := PhaseAttempt{
		ID: attemptID, CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed,
		InputJSON: []byte(`{}`), OutputJSON: mustJSON(map[string]string{"error_code": code}),
		FinishedAt: &finished, ErrorCode: code, ErrorMessage: "safe browser stop",
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	request := BrowserRecoveryOperationRequest{
		Operation: operation, CaseID: incident.ID, AttemptID: attempt.ID,
		ExpectedErrorCode: code, CycleNumber: incident.CycleNumber, ExpectedVersion: incident.Version,
		ActorID: "desktop-user", IdempotencyKey: "browser-operation-" + suffix,
	}
	return incident, attempt, request
}

func TestBrowserRecoveryClaimRejectsCaseStateDriftBeforeExternalEffect(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, _, request := eligibleBrowserRecoveryOperationFixture(t, store, "state-drift", BrowserRecoveryLogin)
	if _, err := store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "advance-before-browser-claim",
		RequestJSON: []byte(`{"advance":true}`),
		Steps: []CaseMutationStep{{To: CaseWaitingEvidence, AuditOnly: true, Event: TransitionEvent{
			ID: "advance-before-browser-claim", EventType: "ordinary_evidence_noted", ActorType: "user", ActorID: "other-user", PayloadJSON: []byte(`{}`),
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-state-drift"); err == nil {
		t.Fatal("browser recovery claim accepted a stale Case version")
	}
}

func TestBrowserRecoveryClaimBlocksGenericCaseMutation(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, _, request := eligibleBrowserRecoveryOperationFixture(t, store, "generic-block", BrowserRecoveryLogin)
	if _, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-generic-block"); err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	if _, err := store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "ordinary-continuation-after-claim",
		RequestJSON: []byte(`{"ordinary":true}`),
		Steps: []CaseMutationStep{{To: CaseWaitingEvidence, AuditOnly: true, Event: TransitionEvent{
			ID: "ordinary-continuation-after-claim", EventType: "ordinary_evidence_noted", ActorType: "user", ActorID: "other-user", PayloadJSON: []byte(`{}`),
		}}},
	}); err == nil {
		t.Fatal("generic Case mutation bypassed an unresolved browser recovery reservation")
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil || current.Version != incident.Version {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestBrowserRecoveryClaimSerializesWithSecondStoreCaseWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
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

	for iteration := 0; iteration < 20; iteration++ {
		suffix := fmt.Sprintf("two-store-%d", iteration)
		incident, _, request := eligibleBrowserRecoveryOperationFixture(t, first, suffix, BrowserRecoveryLogin)
		start := make(chan struct{})
		claimResult := make(chan struct {
			acquired bool
			err      error
		}, 1)
		mutationResult := make(chan error, 1)
		go func() {
			<-start
			_, acquired, claimErr := first.ClaimBrowserRecoveryOperation(context.Background(), request, "claim-"+suffix)
			claimResult <- struct {
				acquired bool
				err      error
			}{acquired: acquired, err: claimErr}
		}()
		go func() {
			<-start
			_, mutationErr := second.ApplyCaseMutation(context.Background(), CaseMutation{
				CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "ordinary-writer-" + suffix,
				RequestJSON: []byte(`{"ordinary":true}`),
				Steps: []CaseMutationStep{{To: CaseWaitingEvidence, AuditOnly: true, Event: TransitionEvent{
					ID: "ordinary-writer-" + suffix, EventType: "ordinary_evidence_noted", ActorType: "user", ActorID: "other-user", PayloadJSON: []byte(`{}`),
				}}},
			})
			mutationResult <- mutationErr
		}()
		close(start)
		claim := <-claimResult
		mutationErr := <-mutationResult
		if claim.acquired && claim.err == nil {
			if !errors.Is(mutationErr, ErrBrowserRecoveryReserved) {
				t.Fatalf("iteration=%d acquired claim but mutation error=%v", iteration, mutationErr)
			}
			continue
		}
		if mutationErr != nil || !errors.Is(claim.err, ErrBrowserRecoveryNotEligible) {
			t.Fatalf("iteration=%d claim acquired=%v err=%v mutation_err=%v", iteration, claim.acquired, claim.err, mutationErr)
		}
	}
}

func TestBrowserRecoveryReservationBlocksTransitionWriter(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, _, request := eligibleBrowserRecoveryOperationFixture(t, store, "transition-block", BrowserRecoveryRepair)
	if _, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-transition-block"); err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	if _, _, err := store.Transition(ctx, incident.ID, incident.Version, CaseValidating, TransitionEvent{
		ID: "transition-after-browser-claim", IdempotencyKey: "transition-after-browser-claim",
		EventType: "ordinary_evidence_continued", ActorType: "user", ActorID: "other-user", PayloadJSON: []byte(`{}`),
	}); !errors.Is(err, ErrBrowserRecoveryReserved) {
		t.Fatalf("transition error=%v", err)
	}
}

func browserRecoveryContinuationMutation(incident IncidentCase, blocked PhaseAttempt, request BrowserRecoveryOperationRequest) CaseMutation {
	fingerprint, _ := browserRecoveryOperationFingerprint(request)
	evidence := mustJSON(map[string]any{"browser_recovery": map[string]any{
		"operation": request.Operation, "blocked_attempt_id": request.AttemptID,
		"expected_browser_error_code": request.ExpectedErrorCode, "request_fingerprint": fingerprint,
	}})
	child := PhaseAttempt{
		ID: stableID("attempt", request.IdempotencyKey), CaseID: incident.ID,
		CycleNumber: incident.CycleNumber, Phase: blocked.Phase, Mode: blocked.Mode,
		Status: AttemptStatusRunning, InputJSON: evidence, OutputJSON: []byte(`{}`),
		ParentAttemptID: blocked.ID,
	}
	return CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: request.IdempotencyKey,
		RequestJSON: []byte(`{"browser_recovery":true}`), CreateAttempts: []PhaseAttempt{child},
		Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(child.ID)},
		Steps: []CaseMutationStep{{To: CaseValidating, Event: TransitionEvent{
			ID: stableID("event", request.IdempotencyKey), EventType: "evidence_continued",
			ActorType: "user", ActorID: request.ActorID, PayloadJSON: mustJSON(map[string]string{"attempt_id": child.ID}),
		}}},
	}
}

func TestBrowserRecoverySpecializedContinuationRequiresExactChildMarker(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "exact-marker", BrowserRecoveryLogin)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-exact-marker")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	operation, err = store.RecordBrowserRecoveryOutcome(ctx, request, operation.ClaimToken, BrowserRecoveryEffectSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	mutation := browserRecoveryContinuationMutation(incident, blocked, request)
	mutation.CreateAttempts[0].InputJSON = []byte(`{"ordinary_evidence":true}`)
	if _, err := store.ApplyBrowserRecoveryCaseMutation(ctx, mutation, request, operation.ClaimToken); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("marker mismatch error=%v", err)
	}
}

func TestBrowserRecoveryEffectSucceededIsConsumedWithExactContinuationOnce(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "consume-once", BrowserRecoveryLogin)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-consume-once")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	operation, err = store.RecordBrowserRecoveryOutcome(ctx, request, operation.ClaimToken, BrowserRecoveryEffectSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	mutation := browserRecoveryContinuationMutation(incident, blocked, request)
	first, err := store.ApplyBrowserRecoveryCaseMutation(ctx, mutation, request, operation.ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ApplyBrowserRecoveryCaseMutation(ctx, mutation, request, operation.ClaimToken)
	if err != nil || !replayed.Replay || replayed.Case.CurrentAttemptID != first.Case.CurrentAttemptID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	completed, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || completed.Status != BrowserRecoveryContinued || completed.ResultCase.CurrentAttemptID != first.Case.CurrentAttemptID {
		t.Fatalf("completed=%+v found=%v err=%v", completed, found, err)
	}
	var childCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM phase_attempts WHERE parent_attempt_id=?`, blocked.ID).Scan(&childCount); err != nil || childCount != 1 {
		t.Fatalf("child_count=%d err=%v", childCount, err)
	}
}

func TestBrowserRecoveryAtomicConsumeFailureRollsBackCaseAttemptAndEvent(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "consume-rollback", BrowserRecoveryLogin)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-consume-rollback")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	operation, err = store.RecordBrowserRecoveryOutcome(ctx, request, operation.ClaimToken, BrowserRecoveryEffectSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_browser_recovery_consume BEFORE UPDATE OF status ON browser_recovery_operations WHEN NEW.status='continued' BEGIN SELECT RAISE(ABORT, 'injected consume failure'); END`); err != nil {
		t.Fatal(err)
	}
	mutation := browserRecoveryContinuationMutation(incident, blocked, request)
	if _, err := store.ApplyBrowserRecoveryCaseMutation(ctx, mutation, request, operation.ClaimToken); err == nil {
		t.Fatal("injected consume failure succeeded")
	}
	current, err := store.GetCase(ctx, incident.ID)
	if err != nil || current.Version != incident.Version || current.CurrentAttemptID != blocked.ID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	var children, events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM phase_attempts WHERE parent_attempt_id=?`, blocked.ID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transition_events WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&events); err != nil {
		t.Fatal(err)
	}
	pending, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || pending.Status != BrowserRecoveryEffectSucceeded || children != 0 || events != 0 {
		t.Fatalf("pending=%+v found=%v children=%d events=%d err=%v", pending, found, children, events, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_browser_recovery_consume`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyBrowserRecoveryCaseMutation(ctx, mutation, request, operation.ClaimToken); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserRecoverySpecializedContinuationRejectsUncertainAndMismatchedReservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BrowserRecoveryOperationRequest)
		status BrowserRecoveryOperationStatus
	}{
		{name: "outcome uncertain", status: BrowserRecoveryOutcomeUncertain},
		{name: "fingerprint mismatch", status: BrowserRecoveryEffectSucceeded, mutate: func(request *BrowserRecoveryOperationRequest) { request.ActorID = "different-user" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestCaseStore(t)
			ctx := context.Background()
			incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "reject-"+strings.ReplaceAll(test.name, " ", "-"), BrowserRecoveryLogin)
			operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-reject-specialized")
			if err != nil || !acquired {
				t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
			}
			operation, err = store.RecordBrowserRecoveryOutcome(ctx, request, operation.ClaimToken, test.status)
			if err != nil {
				t.Fatal(err)
			}
			consume := request
			if test.mutate != nil {
				test.mutate(&consume)
			}
			if _, err := store.ApplyBrowserRecoveryCaseMutation(ctx, browserRecoveryContinuationMutation(incident, blocked, request), consume, operation.ClaimToken); err == nil {
				t.Fatal("specialized continuation consumed an ineligible reservation")
			}
			current, loadErr := store.GetCase(ctx, incident.ID)
			if loadErr != nil || current.Version != incident.Version || current.CurrentAttemptID != blocked.ID {
				t.Fatalf("current=%+v err=%v", current, loadErr)
			}
		})
	}
}

func TestBrowserRecoveryOperationErrorsNeverExposeCallerIdentity(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	_, _, request := eligibleBrowserRecoveryOperationFixture(t, store, "secret-errors", BrowserRecoveryLogin)
	request.IdempotencyKey = "Cookie=session-secret Authorization=Bearer-secret password=hunter2 storageState=private"
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-secret-errors")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	changed := request
	changed.ActorID = "different-user"
	_, _, getErr := store.GetBrowserRecoveryOperation(ctx, changed)
	_, outcomeErr := store.RecordBrowserRecoveryOutcome(ctx, request, "wrong-claim-token", BrowserRecoveryEffectSucceeded)
	for _, operationErr := range []error{getErr, outcomeErr} {
		if !errors.Is(operationErr, ErrIdempotencyConflict) {
			t.Fatalf("operation error=%v", operationErr)
		}
		for _, secret := range []string{"session-secret", "Bearer-secret", "hunter2", "storageState", "Cookie", "Authorization", "password"} {
			if strings.Contains(operationErr.Error(), secret) {
				t.Fatalf("operation error exposed %q: %v", secret, operationErr)
			}
		}
	}
}

func TestOrchestratorBrowserRecoveryContinuationUsesAtomicConsumePath(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "orchestrator-consume", BrowserRecoveryLogin)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-orchestrator-consume")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	operation, err = store.RecordBrowserRecoveryOutcome(ctx, request, operation.ClaimToken, BrowserRecoveryEffectSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	command := ContinueWithEvidenceCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: request.IdempotencyKey,
		ActorID: request.ActorID, Phase: blocked.Phase, Bug: Bug{ID: incident.BugID},
		Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: mustJSON(map[string]any{"browser_recovery": map[string]any{
			"operation": request.Operation, "blocked_attempt_id": request.AttemptID,
			"expected_browser_error_code": request.ExpectedErrorCode, "request_fingerprint": operation.RequestFingerprint,
		}}),
	}
	continued, err := orchestrator.ContinueBrowserRecoveryWithEvidence(ctx, command, operation)
	if err != nil {
		t.Fatal(err)
	}
	completed, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || completed.Status != BrowserRecoveryContinued || completed.ResultCase.CurrentAttemptID != continued.CurrentAttemptID {
		t.Fatalf("completed=%+v found=%v continued=%+v err=%v", completed, found, continued, err)
	}
	if len(runner.starts) != 1 || runner.starts[0].ParentAttemptID != blocked.ID {
		t.Fatalf("starts=%+v", runner.starts)
	}
}

func TestOrchestratorGenericContinuationCannotBypassBrowserRecoveryReservation(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	incident, blocked, request := eligibleBrowserRecoveryOperationFixture(t, store, "generic-orchestrator-block", BrowserRecoveryLogin)
	if _, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-generic-orchestrator"); err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	runner := &recordingPhaseRunner{}
	_, err := NewCaseOrchestrator(store, runner, nil, nil).ContinueWithEvidence(ctx, ContinueWithEvidenceCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "ordinary-evidence-after-browser-claim",
		ActorID: "other-user", Phase: blocked.Phase, Bug: Bug{ID: incident.BugID},
		Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{"ordinary":true}`),
	})
	if !errors.Is(err, ErrBrowserRecoveryReserved) || len(runner.starts) != 0 {
		t.Fatalf("starts=%+v err=%v", runner.starts, err)
	}
}

func TestBrowserRecoveryOperationClaimsExactFingerprintAndRejectsCollisions(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	_, _, request := eligibleBrowserRecoveryOperationFixture(t, store, "fingerprint", BrowserRecoveryLogin)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-a")
	if err != nil || !acquired || operation.Status != BrowserRecoveryClaimed || operation.RequestFingerprint == "" {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	replayed, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-b")
	if err != nil || acquired || replayed.ClaimToken != "claim-a" || replayed.RequestFingerprint != operation.RequestFingerprint {
		t.Fatalf("replayed=%+v acquired=%v err=%v", replayed, acquired, err)
	}

	for _, mutate := range []func(*BrowserRecoveryOperationRequest){
		func(changed *BrowserRecoveryOperationRequest) {
			changed.Operation = BrowserRecoveryRepair
			changed.ExpectedErrorCode = "browser_runtime_broken"
		},
		func(changed *BrowserRecoveryOperationRequest) { changed.ActorID = "other-user" },
		func(changed *BrowserRecoveryOperationRequest) { changed.ExpectedVersion++ },
	} {
		changed := request
		mutate(&changed)
		if _, _, err := store.ClaimBrowserRecoveryOperation(ctx, changed, "claim-c"); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("changed=%+v err=%v", changed, err)
		}
	}
}

func TestBrowserRecoveryOperationPersistsSafeOutcomeAndContinuationAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	incident, attempt, request := eligibleBrowserRecoveryOperationFixture(t, store, "reopen", BrowserRecoveryRepair)
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-reopen")
	if err != nil || !acquired {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	if _, err := store.RecordBrowserRecoveryOutcome(ctx, request, "claim-reopen", BrowserRecoveryEffectSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operation, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || operation.Status != BrowserRecoveryEffectSucceeded {
		t.Fatalf("operation=%+v found=%v err=%v", operation, found, err)
	}
	result, err := store.ApplyBrowserRecoveryCaseMutation(ctx, browserRecoveryContinuationMutation(incident, attempt, request), request, "claim-reopen")
	if err != nil {
		t.Fatal(err)
	}
	completed, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || completed.Status != BrowserRecoveryContinued || completed.ResultCase.ID != result.Case.ID || completed.ResultCase.Version != result.Case.Version {
		t.Fatalf("completed=%+v found=%v err=%v", completed, found, err)
	}
}

func TestBrowserRecoveryOperationSchemaRejectsInvalidStates(t *testing.T) {
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-browser-schema")
	attempt := validRunningAttempt("attempt-browser-schema", "case-browser-schema")
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.Exec(`INSERT INTO browser_recovery_operations (
		idempotency_key,operation,case_id,attempt_id,expected_error_code,cycle_number,expected_version,
		actor_id,request_fingerprint,status,claim_token,outcome_code,result_case_json,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"invalid-browser-state", BrowserRecoveryLogin, attempt.CaseID, attempt.ID, "browser_login_required", 1, 1,
		"desktop-user", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BrowserRecoveryEffectSucceeded, "claim", "", `{}`, formatStoreTime(attempt.StartedAt), formatStoreTime(attempt.StartedAt))
	if err == nil {
		t.Fatal("invalid browser recovery state bypassed database constraints")
	}
}

func TestBrowserRecoveryOperationSchemaMigratesV7AndRepeatedOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		legacyWorkflowStoreSchema, workflowStoreSchemaV1Upgrade, workflowStoreSchemaV2Upgrade,
		workflowStoreSchemaV3Upgrade, workflowStoreSchemaV4Upgrade, workflowStoreSchemaV5Upgrade,
		workflowStoreSchemaV6Upgrade, workflowStoreSchemaV7Upgrade,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 7, Fingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`, workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`PRAGMA user_version=7`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for open := 1; open <= 2; open++ {
		store, err := OpenCaseStore(path)
		if err != nil {
			t.Fatalf("open %d: %v", open, err)
		}
		var version int
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != workflowStoreSchemaVersion {
			t.Fatalf("open %d version=%d err=%v", open, version, err)
		}
		var ddl string
		if err := store.db.QueryRow(`SELECT lower(sql) FROM sqlite_master WHERE type='table' AND name='browser_recovery_operations'`).Scan(&ddl); err != nil {
			t.Fatalf("open %d schema: %v", open, err)
		}
		for _, required := range []string{"primary key", "unique(operation, case_id, attempt_id)", "outcome_uncertain", "request_fingerprint", "result_case_json"} {
			if !strings.Contains(strings.ReplaceAll(ddl, "\n", " "), required) {
				t.Fatalf("open %d schema missing %q: %s", open, required, ddl)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
