package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	storedOld, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedOld.Status != CaseResetArchived || storedOld.CycleNumber != incident.CycleNumber || storedOld.Version != incident.Version+1 || storedOld.CurrentAttemptID != "" || storedOld.ClosedAt == nil || storedOld.SupersededByCaseID != result.Replacement.ID || storedOld.ResetFromCaseID != incident.ResetFromCaseID {
		t.Fatalf("persisted archived Case=%+v", storedOld)
	}
	storedNew, err := store.GetCase(context.Background(), result.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedNew.Status != CasePendingValidation || storedNew.CycleNumber != 1 || storedNew.Version != 1 || storedNew.CurrentAttemptID != "" || storedNew.ClosedAt != nil || storedNew.ResetFromCaseID != incident.ID || storedNew.SupersededByCaseID != "" {
		t.Fatalf("persisted replacement Case=%+v", storedNew)
	}
	if !reflect.DeepEqual(result.Archived, storedOld) || !reflect.DeepEqual(result.Replacement, storedNew) {
		t.Fatalf("returned result differs from persisted Cases: result=%+v old=%+v new=%+v", result, storedOld, storedNew)
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
	incident, attempt := prepareResetCase(t, store, "case-reset-replay")
	seedResetRelatedRecords(t, store, incident, attempt, "replay")
	command := resetCommand(incident, "case-reset-replay-next", "reset-replay")
	first, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	persistedReplacement, err := store.GetCase(context.Background(), first.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := resetDatabaseEffectsSnapshot(t, store)
	second, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil || !second.Replay {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	afterReplay := resetDatabaseEffectsSnapshot(t, store)
	if !reflect.DeepEqual(beforeReplay, afterReplay) {
		t.Fatalf("replay changed database effects:\nbefore=%+v\nafter=%+v", beforeReplay, afterReplay)
	}
	resetEvents := snapshotRows(t, store, `SELECT event_type,idempotency_key FROM transition_events WHERE idempotency_key IN (?,?) ORDER BY event_type`, command.IdempotencyKey, command.IdempotencyKey+":replacement")
	if len(resetEvents) != 2 {
		t.Fatalf("reset-created events=%d rows=%v", len(resetEvents), resetEvents)
	}
	if resetEvents[0][0] != "case_created_from_reset" || resetEvents[1][0] != "case_reset" {
		t.Fatalf("unexpected reset-created events=%v", resetEvents)
	}
	reloadedReplacement, err := store.GetCase(context.Background(), first.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedReplacement, reloadedReplacement) || !reflect.DeepEqual(first.Replacement, reloadedReplacement) {
		t.Fatalf("replacement changed on replay: first=%+v before=%+v after=%+v", first.Replacement, persistedReplacement, reloadedReplacement)
	}
	second.Replay = false
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}

	conflicts := []struct {
		name   string
		mutate func(*CaseReset)
	}{
		{"new Case ID", func(reset *CaseReset) { reset.NewCaseID += "-different" }},
		{"expected version", func(reset *CaseReset) { reset.ExpectedVersion++ }},
		{"actor", func(reset *CaseReset) { reset.ActorID = "bob" }},
		{"request", func(reset *CaseReset) { reset.RequestJSON = json.RawMessage(`{"reason":"different"}`) }},
		{"Bot", func(reset *CaseReset) { reset.SelectedBotKey = "different-bot" }},
	}
	for _, conflict := range conflicts {
		t.Run("fingerprint conflict "+conflict.name, func(t *testing.T) {
			changed := command
			changed.RequestJSON = CloneRawMessage(command.RequestJSON)
			conflict.mutate(&changed)
			if _, err := store.ResetCaseWithReplacement(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
		})
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
	seedResetRelatedRecords(t, store, incident, attempt, "preserve")
	seedResetTransitionEvent(t, store, incident)
	before := relatedRecordsSnapshot(t, store, incident.ID)
	beforeEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id=? ORDER BY id`, incident.ID)
	if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-records-next", "reset-records")); err != nil {
		t.Fatal(err)
	}
	after := relatedRecordsSnapshot(t, store, incident.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("related records changed:\nbefore=%+v\nafter=%+v", before, after)
	}
	afterPreexistingEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id=? AND idempotency_key NOT IN (?,?) ORDER BY id`, incident.ID, "reset-records", "reset-records:replacement")
	if !reflect.DeepEqual(beforeEvents, afterPreexistingEvents) {
		t.Fatalf("preexisting events changed:\nbefore=%+v\nafter=%+v", beforeEvents, afterPreexistingEvents)
	}
	allEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id IN (?,?) ORDER BY id`, incident.ID, "case-reset-records-next")
	if len(allEvents) != len(beforeEvents)+2 {
		t.Fatalf("events before=%d after=%d rows=%v", len(beforeEvents), len(allEvents), allEvents)
	}
}

func TestResetCaseWithReplacementRollsBackInjectedFailure(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-rollback")
	var initialClaim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&initialClaim); err != nil {
		t.Fatal(err)
	}
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
	storedAttempt, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedAttempt.Status != AttemptStatusRunning || storedAttempt.FinishedAt != nil {
		t.Fatalf("attempt=%+v", storedAttempt)
	}
	var claim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	if claim != initialClaim {
		t.Fatalf("run claim after rollback=%q, before=%q", claim, initialClaim)
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

type resetRelatedRecordsSnapshot struct {
	Artifacts              [][]string
	Approvals              [][]string
	CodeChanges            [][]string
	DeploymentObservations [][]string
}

type resetDatabaseSnapshot struct {
	Cases       [][]string
	Attempts    [][]string
	Checkpoints [][]string
	Events      [][]string
	Related     resetRelatedRecordsSnapshot
}

func seedResetRelatedRecords(t *testing.T, store *CaseStore, incident IncidentCase, attempt PhaseAttempt, suffix string) {
	t.Helper()
	now := formatStoreTime(time.Now().UTC())
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO evidence_artifacts (id,case_id,attempt_id,kind,path_or_reference,sha256,captured_at,environment,version,request_id,trace_id,redaction_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"artifact-reset-" + suffix, incident.ID, attempt.ID, "api", "/artifact/" + suffix, strings.Repeat("a", 63) + suffix[:1], now, "test", "v1", "request-" + suffix, "trace-" + suffix, RedactionStatusNotRequired}},
		{`INSERT INTO approvals (id,case_id,kind,actor,approved_at,case_version,scope_json,fix_commits_json,target_branches_json,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?)`, []any{"approval-reset-" + suffix, incident.ID, ApprovalStartFix, "alice", now, incident.Version, `{"scope":"` + suffix + `"}`, `{"repo":"abc"}`, `{"repo":"main"}`, "approval-reset-key-" + suffix}},
		{`INSERT INTO code_changes (id,case_id,attempt_id,repo,base_branch,fix_branch,fix_commit,test_evidence_json,target_environment_branch,merge_base_head,merge_commit,push_remote,push_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"change-reset-" + suffix, incident.ID, attempt.ID, "repo-" + suffix, "main", "fix/reset-" + suffix, "abc-" + suffix, `{"passed":true}`, "test", "base-head", "merge-commit", "origin", "pushed"}},
		{`INSERT INTO deployment_observations (id,case_id,environment,expected_commits_json,user_notified_at,verification_source,observed_version,observed_images_json,observed_commits_json,verified_commit_ancestors_json,observed_at,diagnostic_code,diagnostic_message,verified_at,result,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"observation-reset-" + suffix, incident.ID, "test", `{"repo":"abc"}`, now, "manual", "v1", `{"app":"image:v1"}`, `{"repo":"abc"}`, `{}`, now, "diagnostic", "message", nil, DeploymentResultUnavailable, "observation-reset-key-" + suffix}},
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedResetTransitionEvent(t *testing.T, store *CaseStore, incident IncidentCase) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "event-before-reset", incident.ID, incident.Status, incident.Status, "case_note", "user", "historian", "event-before-reset-key", `{"note":"keep exactly"}`, formatStoreTime(time.Now().UTC().Add(-time.Hour)), "preexisting-fingerprint", `{"preexisting":true}`); err != nil {
		t.Fatal(err)
	}
}

func relatedRecordsSnapshot(t *testing.T, store *CaseStore, caseID string) resetRelatedRecordsSnapshot {
	t.Helper()
	return resetRelatedRecordsSnapshot{
		Artifacts:              snapshotRows(t, store, `SELECT * FROM evidence_artifacts WHERE case_id=? ORDER BY id`, caseID),
		Approvals:              snapshotRows(t, store, `SELECT * FROM approvals WHERE case_id=? ORDER BY id`, caseID),
		CodeChanges:            snapshotRows(t, store, `SELECT * FROM code_changes WHERE case_id=? ORDER BY id`, caseID),
		DeploymentObservations: snapshotRows(t, store, `SELECT * FROM deployment_observations WHERE case_id=? ORDER BY id`, caseID),
	}
}

func resetDatabaseEffectsSnapshot(t *testing.T, store *CaseStore) resetDatabaseSnapshot {
	t.Helper()
	return resetDatabaseSnapshot{
		Cases:       snapshotRows(t, store, `SELECT * FROM incident_cases ORDER BY id`),
		Attempts:    snapshotRows(t, store, `SELECT * FROM phase_attempts ORDER BY id`),
		Checkpoints: snapshotRows(t, store, `SELECT * FROM fix_checkpoints ORDER BY attempt_id`),
		Events:      snapshotRows(t, store, `SELECT * FROM transition_events ORDER BY id`),
		Related: resetRelatedRecordsSnapshot{
			Artifacts:              snapshotRows(t, store, `SELECT * FROM evidence_artifacts ORDER BY id`),
			Approvals:              snapshotRows(t, store, `SELECT * FROM approvals ORDER BY id`),
			CodeChanges:            snapshotRows(t, store, `SELECT * FROM code_changes ORDER BY id`),
			DeploymentObservations: snapshotRows(t, store, `SELECT * FROM deployment_observations ORDER BY id`),
		},
	}
}

func snapshotRows(t *testing.T, store *CaseStore, query string, args ...any) [][]string {
	t.Helper()
	rows, err := store.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		record := make([]string, len(values))
		for index, value := range values {
			switch typed := value.(type) {
			case nil:
				record[index] = "<NULL>"
			case []byte:
				record[index] = string(typed)
			default:
				record[index] = fmt.Sprint(typed)
			}
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
