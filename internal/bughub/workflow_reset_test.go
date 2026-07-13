package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResetCaseWithReplacementIsAtomic(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-atomic")

	result, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-atomic-next", "reset-atomic"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Replay || result.CancelledAttemptID != attempt.ID {
		t.Fatalf("result=%+v", result)
	}
	if result.Archived.Status != CaseResetArchived || result.Archived.Version != incident.Version+1 || result.Archived.ClosedAt == nil || result.Archived.CurrentAttemptID != "" || result.Archived.SupersededByCaseID != result.Replacement.ID {
		t.Fatalf("archived=%+v", result.Archived)
	}
	if result.Replacement.Status != CasePendingValidation || result.Replacement.Version != 1 || result.Replacement.ResetFromCaseID != incident.ID || result.Replacement.SelectedBotKey != "validator" || result.Replacement.CurrentAttemptID != "" || result.Replacement.ClosedAt != nil {
		t.Fatalf("replacement=%+v", result.Replacement)
	}
	storedAttempt, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil || storedAttempt.Status != AttemptStatusCancelled || storedAttempt.FinishedAt == nil {
		t.Fatalf("attempt=%+v err=%v", storedAttempt, err)
	}
	var claim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&claim); err != nil || claim != "" {
		t.Fatalf("claim=%q err=%v", claim, err)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	oldEvents, _ := store.ListEvents(context.Background(), incident.ID)
	newEvents, _ := store.ListEvents(context.Background(), result.Replacement.ID)
	if len(oldEvents) != 1 || oldEvents[0].EventType != "case_reset" || len(newEvents) != 1 || newEvents[0].EventType != "case_created_from_reset" {
		t.Fatalf("old events=%+v new events=%+v", oldEvents, newEvents)
	}
}

func TestResetCaseWithReplacementReplaysSameReplacement(t *testing.T) {
	store := openTestCaseStore(t)
	incident, _ := prepareResetCase(t, store, "case-reset-replay")
	command := resetCommand(incident, "case-reset-replay-next", "reset-replay")
	first, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil || !second.Replay {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	second.Replay = false
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	changed := command
	changed.RequestJSON = json.RawMessage(`{"reason":"different"}`)
	if _, err := store.ResetCaseWithReplacement(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request err=%v", err)
	}
	changed = command
	changed.SelectedBotKey = "different-bot"
	if _, err := store.ResetCaseWithReplacement(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed bot err=%v", err)
	}
}

func TestResetCaseWithReplacementRejectsTerminalAndStaleCases(t *testing.T) {
	for _, status := range []CaseStatus{CaseFixedVerified, CaseLegacyArchived, CaseResetArchived} {
		t.Run(string(status), func(t *testing.T) {
			store := openTestCaseStore(t)
			closed := time.Now().UTC()
			incident := IncidentCase{ID: "terminal-" + string(status), BugID: "bug-terminal-" + string(status), Status: status, CycleNumber: 1, Version: 1, ClosedAt: &closed}
			if status == CaseLegacyArchived {
				incident.ClosedAt = nil
			}
			if err := store.CreateCase(context.Background(), incident); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, incident.ID+"-next", "reset-terminal-"+string(status))); err == nil {
				t.Fatal("terminal Case reset succeeded")
			}
		})
	}
	t.Run("stale version", func(t *testing.T) {
		store := openTestCaseStore(t)
		incident := createWorkflowCase(t, store, "case-reset-stale", CaseWaitingEvidence)
		command := resetCommand(incident, "case-reset-stale-next", "reset-stale")
		command.ExpectedVersion++
		if _, err := store.ResetCaseWithReplacement(context.Background(), command); !errors.Is(err, ErrCaseVersionConflict) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("duplicate replacement", func(t *testing.T) {
		store := openTestCaseStore(t)
		incident := createWorkflowCase(t, store, "case-reset-duplicate", CaseWaitingEvidence)
		if err := store.CreateCase(context.Background(), IncidentCase{ID: "replacement-exists", BugID: "other-bug", Status: CasePendingValidation, CycleNumber: 1, Version: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "replacement-exists", "reset-duplicate")); err == nil {
			t.Fatal("duplicate replacement accepted")
		}
	})
}

func TestResetCaseWithReplacementPreservesRelatedRecords(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-records")
	now := formatStoreTime(time.Now().UTC())
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO evidence_artifacts (id,case_id,attempt_id,kind,path_or_reference,sha256,captured_at,environment,version,request_id,trace_id,redaction_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"artifact-reset", incident.ID, attempt.ID, "api", "/artifact", strings.Repeat("a", 64), now, "test", "v1", "request", "trace", RedactionStatusNotRequired}},
		{`INSERT INTO approvals (id,case_id,kind,actor,approved_at,case_version,scope_json,fix_commits_json,target_branches_json,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?)`, []any{"approval-reset", incident.ID, ApprovalStartFix, "alice", now, incident.Version, `{}`, `{}`, `{}`, "approval-reset-key"}},
		{`INSERT INTO code_changes (id,case_id,attempt_id,repo,base_branch,fix_branch,fix_commit,test_evidence_json,target_environment_branch,merge_base_head,merge_commit,push_remote,push_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"change-reset", incident.ID, attempt.ID, "repo", "main", "fix/reset", "abc", `{}`, "test", "", "", "origin", "pushed"}},
		{`INSERT INTO deployment_observations (id,case_id,environment,expected_commits_json,user_notified_at,verification_source,observed_version,observed_images_json,observed_commits_json,verified_commit_ancestors_json,observed_at,diagnostic_code,diagnostic_message,verified_at,result,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"observation-reset", incident.ID, "test", `{}`, nil, "manual", "v1", `{}`, `{}`, `{}`, now, "", "", nil, DeploymentResultUnavailable, "observation-reset-key"}},
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	before := relatedRecordCounts(t, store, incident.ID)
	if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-records-next", "reset-records")); err != nil {
		t.Fatal(err)
	}
	after := relatedRecordCounts(t, store, incident.ID)
	if before != after || after != [4]int{1, 1, 1, 1} {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestResetCaseWithReplacementRollsBackInjectedFailure(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-rollback")
	if _, err := store.db.Exec(`CREATE TRIGGER fail_reset_replacement_event BEFORE INSERT ON transition_events WHEN NEW.event_type='case_created_from_reset' BEGIN SELECT RAISE(ABORT, 'injected reset failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-rollback-next", "reset-rollback")); err == nil {
		t.Fatal("injected failure was ignored")
	}
	old, err := store.GetCase(context.Background(), incident.ID)
	if err != nil || old.Status != incident.Status || old.Version != incident.Version || old.ClosedAt != nil || old.SupersededByCaseID != "" {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	if _, err := store.GetCase(context.Background(), "case-reset-rollback-next"); !errors.Is(err, ErrCaseNotFound) {
		t.Fatalf("replacement err=%v", err)
	}
	storedAttempt, _ := store.GetAttempt(context.Background(), attempt.ID)
	if storedAttempt.Status != AttemptStatusRunning {
		t.Fatalf("attempt=%+v", storedAttempt)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || !found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	var events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transition_events WHERE idempotency_key LIKE 'reset-rollback%'`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestResetCaseWithReplacementSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident, _ := prepareResetCase(t, store, "case-reset-reopen")
	command := resetCommand(incident, "case-reset-reopen-next", "reset-reopen")
	first, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replay, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil || !replay.Replay || replay.Replacement.ID != first.Replacement.ID || replay.Archived.ID != first.Archived.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestResetCaseWithReplacementRedactsStoredRequest(t *testing.T) {
	store := openTestCaseStore(t)
	incident := createWorkflowCase(t, store, "case-reset-redact", CaseWaitingEvidence)
	secrets := []string{
		"github_pat_1234567890abcdefghijklmnop",
		"session_cookie_value_123456",
		"Bearer abcdefghijklmnop1234567890",
		"raw-password-value",
		"https://reset-user:reset-pass@example.test/private",
	}
	command := resetCommand(incident, "case-reset-redact-next", "reset-redact")
	command.RequestJSON = mustJSON(map[string]any{
		"token": secrets[0], "Cookie": secrets[1], "Authorization": secrets[2],
		"password": secrets[3], "command": "curl " + secrets[4] + " --fail",
	})
	if _, err := store.ResetCaseWithReplacement(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`SELECT payload_json,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key IN (?,?)`, command.IdempotencyKey, command.IdempotencyKey+":replacement")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var payload, fingerprint, result string
		if err := rows.Scan(&payload, &fingerprint, &result); err != nil {
			t.Fatal(err)
		}
		stored := payload + fingerprint + result
		for _, secret := range secrets {
			if strings.Contains(stored, secret) {
				t.Fatalf("stored reset identity contains secret %q: %s", secret, stored)
			}
		}
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
}

func prepareResetCase(t *testing.T, store *CaseStore, caseID string) (IncidentCase, PhaseAttempt) {
	t.Helper()
	incident := createWorkflowCase(t, store, caseID, CaseFixing)
	attempt := PhaseAttempt{ID: caseID + "-attempt", CaseID: caseID, CycleNumber: incident.CycleNumber, Phase: PhaseFix, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC()}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE incident_cases SET current_attempt_id=?,selected_bot_key=? WHERE id=?`, attempt.ID, attempt.BotKey, incident.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE phase_attempts SET run_claim_token=? WHERE id=?`, "claim-token", attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFixCheckpoint(context.Background(), FixCheckpoint{AttemptID: attempt.ID, CaseID: incident.ID, StagingLocator: attempt.ID + "-staging"}); err != nil {
		t.Fatal(err)
	}
	incident, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	return incident, attempt
}

func resetCommand(incident IncidentCase, newCaseID, key string) CaseReset {
	return CaseReset{CaseID: incident.ID, NewCaseID: newCaseID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", SelectedBotKey: "validator", RequestJSON: json.RawMessage(`{"reason":"retry from validation"}`)}
}

func relatedRecordCounts(t *testing.T, store *CaseStore, caseID string) [4]int {
	t.Helper()
	var result [4]int
	for index, table := range []string{"evidence_artifacts", "approvals", "code_changes", "deployment_observations"} {
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE case_id=?`, caseID).Scan(&result[index]); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
