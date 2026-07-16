package bughub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserRecoveryOperationClaimsExactFingerprintAndRejectsCollisions(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	createTestCase(t, store, "case-browser-operation")
	attempt := validRunningAttempt("attempt-browser-operation", "case-browser-operation")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	request := BrowserRecoveryOperationRequest{
		Operation: BrowserRecoveryLogin, CaseID: attempt.CaseID, AttemptID: attempt.ID,
		ExpectedErrorCode: "browser_login_required", CycleNumber: attempt.CycleNumber,
		ExpectedVersion: 1, ActorID: "desktop-user", IdempotencyKey: "browser-operation-key",
	}
	operation, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-a")
	if err != nil || !acquired || operation.Status != BrowserRecoveryClaimed || operation.RequestFingerprint == "" {
		t.Fatalf("operation=%+v acquired=%v err=%v", operation, acquired, err)
	}
	replayed, acquired, err := store.ClaimBrowserRecoveryOperation(ctx, request, "claim-b")
	if err != nil || acquired || replayed.ClaimToken != "claim-a" || replayed.RequestFingerprint != operation.RequestFingerprint {
		t.Fatalf("replayed=%+v acquired=%v err=%v", replayed, acquired, err)
	}

	for _, mutate := range []func(*BrowserRecoveryOperationRequest){
		func(changed *BrowserRecoveryOperationRequest) { changed.Operation = BrowserRecoveryRepair },
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
	createTestCase(t, store, "case-browser-reopen")
	attempt := validRunningAttempt("attempt-browser-reopen", "case-browser-reopen")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	request := BrowserRecoveryOperationRequest{
		Operation: BrowserRecoveryRepair, CaseID: attempt.CaseID, AttemptID: attempt.ID,
		ExpectedErrorCode: "browser_runtime_broken", CycleNumber: attempt.CycleNumber,
		ExpectedVersion: 1, ActorID: "desktop-user", IdempotencyKey: "browser-reopen-key",
	}
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
	result := IncidentCase{ID: request.CaseID, BugID: "bug-" + request.CaseID, Status: CaseValidating, CycleNumber: 1, CurrentAttemptID: attempt.ID, Version: 2}
	if err := store.CompleteBrowserRecoveryOperation(ctx, request, "claim-reopen", result); err != nil {
		t.Fatal(err)
	}
	completed, found, err := store.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found || completed.Status != BrowserRecoveryContinued || completed.ResultCase.ID != result.ID || completed.ResultCase.Version != result.Version {
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
