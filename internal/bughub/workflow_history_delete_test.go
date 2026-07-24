package bughub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteTerminalCaseHistoryForBugPurgesRecordsAndArtifacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	closed := time.Now().UTC()
	deletedCase := IncidentCase{ID: "case-delete", BugID: "bug-delete", Status: CaseFixedVerified, CycleNumber: 1, Version: 1, ClosedAt: &closed}
	keptCase := IncidentCase{ID: "case-keep", BugID: "bug-keep", Status: CaseFixedVerified, CycleNumber: 1, Version: 1, ClosedAt: &closed}
	for _, incident := range []IncidentCase{deletedCase, keptCase} {
		if err := store.CreateCase(ctx, incident); err != nil {
			t.Fatal(err)
		}
	}
	attempt := PhaseAttempt{
		ID: "attempt-delete", CaseID: deletedCase.ID, CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded,
		AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
		StartedAt: closed.Add(-time.Minute), FinishedAt: &closed,
	}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	seedTerminalHistoryRelatedRows(t, store, deletedCase, attempt, closed)

	artifactsRoot := filepath.Join(root, "artifacts")
	caseArtifacts := filepath.Join(artifactsRoot, artifactStorageCaseComponent(deletedCase.ID))
	if err := os.MkdirAll(caseArtifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caseArtifacts, "evidence"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := DeleteTerminalCaseHistoryForBug(ctx, store, artifactsRoot, deletedCase.BugID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CaseIDs) != 1 || result.CaseIDs[0] != deletedCase.ID {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(caseArtifacts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact directory still exists: %v", err)
	}
	for _, table := range []string{
		"browser_recovery_operations", "reset_cancellation_operations", "validation_recipes",
		"fix_checkpoints", "evidence_artifacts", "code_changes", "approvals",
		"deployment_observations", "transition_events", "phase_attempts", "incident_cases",
	} {
		var count int
		query := "SELECT COUNT(*) FROM " + table + " WHERE "
		switch table {
		case "fix_checkpoints":
			query += "attempt_id=?"
		case "validation_recipes", "browser_recovery_operations", "reset_cancellation_operations",
			"evidence_artifacts", "code_changes", "approvals", "deployment_observations",
			"transition_events", "phase_attempts":
			query += "case_id=?"
		default:
			query += "id=?"
		}
		arg := deletedCase.ID
		if table == "fix_checkpoints" {
			arg = attempt.ID
		}
		if err := store.db.QueryRow(query, arg).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err := store.GetCase(ctx, keptCase.ID); err != nil {
		t.Fatalf("unrelated Case removed: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if cases, err := store.ListCases(ctx); err != nil || len(cases) != 1 || cases[0].ID != keptCase.ID {
		t.Fatalf("reopened cases=%+v err=%v", cases, err)
	}
	if replay, err := DeleteTerminalCaseHistoryForBug(ctx, store, artifactsRoot, deletedCase.BugID); err != nil || len(replay.CaseIDs) != 0 {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
}

func TestDeleteTerminalCaseHistoryForBugRejectsActiveCase(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	closed := time.Now().UTC()
	for _, incident := range []IncidentCase{
		{ID: "case-history", BugID: "bug-shared", Status: CaseResetArchived, CycleNumber: 1, Version: 1, ClosedAt: &closed},
		{ID: "case-active", BugID: "bug-shared", Status: CaseWaitingEvidence, CycleNumber: 2, Version: 1},
	} {
		if err := store.CreateCase(ctx, incident); err != nil {
			t.Fatal(err)
		}
	}

	result, err := DeleteTerminalCaseHistoryForBug(ctx, store, filepath.Join(t.TempDir(), "artifacts"), "bug-shared")
	if !errors.Is(err, ErrActiveCaseHistory) || len(result.CaseIDs) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if cases, listErr := store.ListCases(ctx); listErr != nil || len(cases) != 2 {
		t.Fatalf("cases=%+v err=%v", cases, listErr)
	}
}

func seedTerminalHistoryRelatedRows(t *testing.T, store *CaseStore, incident IncidentCase, attempt PhaseAttempt, now time.Time) {
	t.Helper()
	at := formatStoreTime(now)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"event-delete", incident.ID, CaseRegressionValidating, CaseFixedVerified, "regression_fixed", "agent", "validator", "event-delete-key", `{}`, at, "fingerprint", `{}`}},
		{`INSERT INTO evidence_artifacts (id,case_id,attempt_id,kind,path_or_reference,sha256,captured_at,environment,version,request_id,trace_id,redaction_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"artifact-delete", incident.ID, attempt.ID, "api", "/artifact/delete", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", at, "test", "v1", "request", "trace", RedactionStatusNotRequired}},
		{`INSERT INTO code_changes (id,case_id,attempt_id,repo,base_branch,fix_branch,fix_commit,test_evidence_json,target_environment_branch,merge_base_head,merge_commit,push_remote,push_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"change-delete", incident.ID, attempt.ID, "repo", "main", "fix/bug", "commit", `[]`, "test", "head", "merge", "origin", "pushed"}},
		{`INSERT INTO approvals (id,case_id,kind,actor,approved_at,case_version,scope_json,fix_commits_json,target_branches_json,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			[]any{"approval-delete", incident.ID, ApprovalStartFix, "alice", at, incident.Version, `{}`, `{}`, `{}`, "approval-delete-key"}},
		{`INSERT INTO deployment_observations (id,case_id,environment,expected_commits_json,user_notified_at,verification_source,observed_version,observed_images_json,observed_commits_json,verified_commit_ancestors_json,observed_at,diagnostic_code,diagnostic_message,verified_at,result,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"deployment-delete", incident.ID, "test", `{"repo":"commit"}`, at, "manual", "v1", `{}`, `{"repo":"commit"}`, `{}`, at, "", "", at, DeploymentResultMatched, "deployment-delete-key"}},
		{`INSERT INTO fix_checkpoints (attempt_id,case_id,staging_locator,created_at) VALUES (?,?,?,?)`,
			[]any{attempt.ID, incident.ID, "checkpoint", at}},
		{`INSERT INTO reset_cancellation_operations (reset_key,case_id,attempt_id,request_fingerprint,status,claim_token,outcome_code,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			[]any{"reset-delete", incident.ID, attempt.ID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResetCancellationSucceeded, "claim", "succeeded", at, at}},
		{`INSERT INTO browser_recovery_operations (idempotency_key,operation,case_id,attempt_id,expected_error_code,cycle_number,expected_version,actor_id,request_fingerprint,status,claim_token,outcome_code,result_case_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{"browser-delete", "repair", incident.ID, attempt.ID, "browser_locator_failed", 1, 1, "alice", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", BrowserRecoveryEffectFailed, "claim", "failed", `{}`, at, at}},
		{`INSERT INTO validation_recipes (case_id,scenario_sha256,plan_sha256,plan_json,source_attempt_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
			[]any{incident.ID, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", `{}`, attempt.ID, at, at}},
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed related row: %v", err)
		}
	}
}
