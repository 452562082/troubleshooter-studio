package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeploymentProofSchemaMigratesV1Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(legacyWorkflowStoreSchema); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(workflowStoreSchemaV1Upgrade); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(workflowSchemaMigrationDetail{Version: 1, Fingerprint: fingerprint})
	if _, err = tx.Exec(`INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`, workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != workflowStoreSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	columns, err := workflowTableColumns(context.Background(), store.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"verified_commit_ancestors_json", "observed_at", "diagnostic_code", "diagnostic_message"} {
		if !containsString(columns["deployment_observations"], required) {
			t.Fatalf("missing %s columns=%v", required, columns["deployment_observations"])
		}
	}
	for _, required := range []string{"attempt_id", "case_id", "staging_locator", "created_at"} {
		if !containsString(columns["fix_checkpoints"], required) {
			t.Fatalf("missing fix_checkpoints.%s columns=%v", required, columns["fix_checkpoints"])
		}
	}
	if !containsString(columns["phase_attempts"], "completion_identity_sha256") {
		t.Fatalf("missing phase_attempts.completion_identity_sha256 columns=%v", columns["phase_attempts"])
	}
	if !containsString(columns["phase_attempts"], "run_claim_token") {
		t.Fatalf("missing phase_attempts.run_claim_token columns=%v", columns["phase_attempts"])
	}
	for _, required := range []string{"reset_from_case_id", "superseded_by_case_id"} {
		if !containsString(columns["incident_cases"], required) {
			t.Fatalf("missing incident_cases.%s columns=%v", required, columns["incident_cases"])
		}
	}
	for _, required := range []string{"reset_key", "case_id", "attempt_id", "request_fingerprint", "status", "claim_token", "outcome_code", "created_at", "updated_at"} {
		if !containsString(columns["reset_cancellation_operations"], required) {
			t.Fatalf("missing reset_cancellation_operations.%s columns=%v", required, columns["reset_cancellation_operations"])
		}
	}
}

func TestDeploymentProofSchemaMigratesV2ThroughV6Stores(t *testing.T) {
	for _, initialVersion := range []int{2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("v%d", initialVersion), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{legacyWorkflowStoreSchema, workflowStoreSchemaV1Upgrade, workflowStoreSchemaV2Upgrade} {
				if _, err = tx.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if initialVersion >= 3 {
				if _, err = tx.Exec(workflowStoreSchemaV3Upgrade); err != nil {
					t.Fatal(err)
				}
			}
			if initialVersion >= 4 {
				if _, err = tx.Exec(workflowStoreSchemaV4Upgrade); err != nil {
					t.Fatal(err)
				}
			}
			if initialVersion >= 5 {
				if _, err = tx.Exec(workflowStoreSchemaV5Upgrade); err != nil {
					t.Fatal(err)
				}
			}
			if initialVersion >= 6 {
				if _, err = tx.Exec(workflowStoreSchemaV6Upgrade); err != nil {
					t.Fatal(err)
				}
			}
			fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
			if err != nil {
				t.Fatal(err)
			}
			detail, _ := json.Marshal(workflowSchemaMigrationDetail{Version: initialVersion, Fingerprint: fingerprint})
			if _, err = tx.Exec(`INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`, workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, initialVersion)); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := OpenCaseStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var version int
			if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != workflowStoreSchemaVersion {
				t.Fatalf("version=%d err=%v", version, err)
			}
			columns, err := workflowTableColumns(context.Background(), store.db)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"attempt_id", "case_id", "staging_locator", "created_at"} {
				if !containsString(columns["fix_checkpoints"], required) {
					t.Fatalf("missing fix_checkpoints.%s columns=%v", required, columns["fix_checkpoints"])
				}
			}
			if !containsString(columns["phase_attempts"], "completion_identity_sha256") {
				t.Fatalf("missing completion identity columns=%v", columns["phase_attempts"])
			}
			if !containsString(columns["phase_attempts"], "run_claim_token") {
				t.Fatalf("missing run claim columns=%v", columns["phase_attempts"])
			}
			for _, required := range []string{"reset_from_case_id", "superseded_by_case_id"} {
				if !containsString(columns["incident_cases"], required) {
					t.Fatalf("missing incident_cases.%s columns=%v", required, columns["incident_cases"])
				}
			}
			for _, required := range []string{"reset_key", "case_id", "attempt_id", "request_fingerprint", "status", "claim_token", "outcome_code", "created_at", "updated_at"} {
				if !containsString(columns["reset_cancellation_operations"], required) {
					t.Fatalf("missing reset cancellation operation %s columns=%v", required, columns["reset_cancellation_operations"])
				}
			}
		})
	}
}

func TestWorkflowSchemaV7BackfillsCommittedV6ResetCancellationWithoutRepeatingRunnerCancel(t *testing.T) {
	for _, test := range []struct {
		name         string
		auditType    string
		auditPayload string
		wantStatus   ResetCancellationStatus
		wantOutcome  string
		wantWarning  string
	}{
		{name: "known success", auditType: "reset_runner_cancel_succeeded", auditPayload: `{"attempt_id":"legacy-reset-attempt","outcome":"succeeded"}`, wantStatus: ResetCancellationSucceeded, wantOutcome: "succeeded"},
		{name: "known failure", auditType: "reset_runner_cancel_failed", auditPayload: `{"attempt_id":"legacy-reset-attempt","outcome":"failed","warning_code":"reset_runner_cancel_failed"}`, wantStatus: ResetCancellationFailed, wantOutcome: "runner_cancel_failed", wantWarning: "reset_runner_cancel_failed"},
		{name: "missing audit is unknown", wantStatus: ResetCancellationClaimed, wantWarning: "reset_runner_cancel_unknown"},
		{name: "mismatched audit is unknown", auditType: "reset_runner_cancel_succeeded", auditPayload: `{"attempt_id":"different-attempt","outcome":"succeeded"}`, wantStatus: ResetCancellationClaimed, wantWarning: "reset_runner_cancel_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, command, fingerprint := createV6CommittedResetFixture(t, test.auditType, test.auditPayload)
			store, err := OpenCaseStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			operation, found, err := store.GetResetCancellationOperation(context.Background(), command.IdempotencyKey, fingerprint)
			if err != nil || !found || operation.CaseID != command.CaseID || operation.AttemptID != "legacy-reset-attempt" || operation.Status != test.wantStatus || operation.OutcomeCode != test.wantOutcome || operation.ClaimToken == "" {
				t.Fatalf("operation=%+v found=%v err=%v", operation, found, err)
			}
			runner := &recordingPhaseRunner{}
			orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
			outcome, err := orchestrator.ResetCaseWithOutcome(context.Background(), command)
			if err != nil || outcome.Case.ID != command.NewCaseID {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if test.wantWarning == "" {
				if len(outcome.Warnings) != 0 {
					t.Fatalf("warnings=%+v", outcome.Warnings)
				}
			} else if len(outcome.Warnings) != 1 || outcome.Warnings[0].Code != test.wantWarning {
				t.Fatalf("warnings=%+v", outcome.Warnings)
			}
			if len(runner.cancels) != 0 {
				t.Fatalf("legacy runner cancellation repeated: %v", runner.cancels)
			}
			replayed, err := orchestrator.ResetCaseWithOutcome(context.Background(), command)
			if err != nil || replayed.Case.ID != outcome.Case.ID || !reflect.DeepEqual(replayed.Warnings, outcome.Warnings) || len(runner.cancels) != 0 || len(runner.starts) != 1 {
				t.Fatalf("replayed=%+v err=%v cancels=%v starts=%v", replayed, err, runner.cancels, runner.starts)
			}
		})
	}
}

func TestWorkflowSchemaV7DowngradesTamperedV6ResetCancellationAuditToUnknown(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		value any
	}{
		{name: "event ID", query: `UPDATE transition_events SET id=? WHERE idempotency_key='legacy-reset-key:runner-cancel'`, value: "tampered-audit-id"},
		{name: "archive status", query: `UPDATE transition_events SET from_status=? WHERE idempotency_key='legacy-reset-key:runner-cancel'`, value: CaseFixing},
		{name: "request fingerprint", query: `UPDATE transition_events SET request_fingerprint=? WHERE idempotency_key='legacy-reset-key:runner-cancel'`, value: strings.Repeat("b", 64)},
		{name: "result snapshot", query: `UPDATE transition_events SET result_case_json=? WHERE idempotency_key='legacy-reset-key:runner-cancel'`, value: `{}`},
		{name: "created timestamp", query: `UPDATE transition_events SET created_at=? WHERE idempotency_key='legacy-reset-key:runner-cancel'`, value: "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, command, fingerprint := createV6CommittedResetFixture(t, "reset_runner_cancel_succeeded", `{"attempt_id":"legacy-reset-attempt","outcome":"succeeded"}`)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.query, test.value); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := OpenCaseStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			operation, found, err := store.GetResetCancellationOperation(context.Background(), command.IdempotencyKey, fingerprint)
			if err != nil || !found || operation.Status != ResetCancellationClaimed || operation.OutcomeCode != "" {
				t.Fatalf("operation=%+v found=%v err=%v", operation, found, err)
			}
		})
	}
	t.Run("payload extension with matching fingerprint", func(t *testing.T) {
		path, command, fingerprint := createV6CommittedResetFixture(t, "reset_runner_cancel_succeeded", `{"attempt_id":"legacy-reset-attempt","outcome":"succeeded"}`)
		auditKey := command.IdempotencyKey + ":runner-cancel"
		payload := `{"attempt_id":"legacy-reset-attempt","outcome":"succeeded","extra":"tampered"}`
		material, err := json.Marshal(struct {
			CaseID      string          `json:"case_id"`
			Key         string          `json:"key"`
			EventType   string          `json:"event_type"`
			ActorType   string          `json:"actor_type"`
			ActorID     string          `json:"actor_id"`
			PayloadJSON json.RawMessage `json:"payload_json"`
		}{command.CaseID, auditKey, "reset_runner_cancel_succeeded", "studio", "orchestrator", json.RawMessage(payload)})
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(material)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE transition_events SET payload_json=?,request_fingerprint=? WHERE idempotency_key=?`, payload, hex.EncodeToString(digest[:]), auditKey); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		store, err := OpenCaseStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		operation, found, err := store.GetResetCancellationOperation(context.Background(), command.IdempotencyKey, fingerprint)
		if err != nil || !found || operation.Status != ResetCancellationClaimed {
			t.Fatalf("operation=%+v found=%v err=%v", operation, found, err)
		}
	})
}

func TestWorkflowSchemaV7SkipsCommittedV6ResetWithoutCancelledAttempt(t *testing.T) {
	path, command, fingerprint := createV6CommittedResetFixture(t, "", "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var resultJSON string
	if err := db.QueryRow(`SELECT result_case_json FROM transition_events WHERE idempotency_key=?`, command.IdempotencyKey).Scan(&resultJSON); err != nil {
		t.Fatal(err)
	}
	var result CaseResetResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatal(err)
	}
	result.CancelledAttemptID = ""
	resultJSONBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transition_events SET result_case_json=? WHERE idempotency_key=?`, string(resultJSONBytes), command.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, found, err := store.GetResetCancellationOperation(context.Background(), command.IdempotencyKey, fingerprint); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestWorkflowSchemaV7RejectsCollidingCommittedV6ResetCancellationIdentity(t *testing.T) {
	path, command, _ := createV6CommittedResetFixture(t, "", "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) SELECT ?,case_id,from_status,to_status,event_type,actor_type,actor_id,?,payload_json,created_at,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, "legacy-reset-event-collision", "legacy-reset-key-collision", command.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if store, err := OpenCaseStore(path); err == nil {
		_ = store.Close()
		t.Fatal("colliding v6 reset cancellation identity was accepted")
	}
}

func createV6CommittedResetFixture(t *testing.T, auditType, auditPayload string) (string, ResetCaseCommand, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow-v6.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{legacyWorkflowStoreSchema, workflowStoreSchemaV1Upgrade, workflowStoreSchemaV2Upgrade, workflowStoreSchemaV3Upgrade, workflowStoreSchemaV4Upgrade, workflowStoreSchemaV5Upgrade, workflowStoreSchemaV6Upgrade} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(workflowSchemaMigrationDetail{Version: 6, Fingerprint: fingerprint})
	if _, err := tx.Exec(`INSERT INTO schema_migrations (key,applied_at,detail_json) VALUES (?,?,?)`, workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &CaseStore{db: db}
	now := time.Now().UTC()
	closedAt := now
	archived := IncidentCase{ID: "legacy-reset-case", BugID: "legacy-reset-bug", Source: "zentao", SystemID: "base", Environment: "test", Status: CaseResetArchived, CycleNumber: 1, SelectedBotKey: "validator", SupersededByCaseID: "legacy-reset-replacement", Version: 2, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, ClosedAt: &closedAt}
	replacement := IncidentCase{ID: "legacy-reset-replacement", BugID: archived.BugID, Source: archived.Source, SystemID: archived.SystemID, Environment: archived.Environment, Status: CasePendingValidation, CycleNumber: 2, SelectedBotKey: archived.SelectedBotKey, ResetFromCaseID: archived.ID, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateCase(context.Background(), archived); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCase(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	archived, err = store.GetCase(context.Background(), archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	attempt := PhaseAttempt{ID: "legacy-reset-attempt", CaseID: archived.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusCancelled, AgentTarget: "codex", BotKey: archived.SelectedBotKey, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now.Add(-time.Minute), FinishedAt: &finishedAt}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	command := ResetCaseCommand{CaseID: archived.ID, NewCaseID: replacement.ID, ExpectedVersion: 1, IdempotencyKey: "legacy-reset-key", ActorID: "alice", Bug: Bug{ID: archived.BugID, Source: archived.Source, SystemID: archived.SystemID, Env: archived.Environment}, Bot: BotRef{Key: archived.SelectedBotKey, Target: "codex", Env: archived.Environment}, InputJSON: []byte(`{}`)}
	reset := CaseReset{CaseID: command.CaseID, NewCaseID: command.NewCaseID, IdempotencyKey: command.IdempotencyKey, ActorID: command.ActorID, ExpectedVersion: command.ExpectedVersion, SelectedBotKey: command.Bot.Key, RequestJSON: mustJSON(command)}
	resetFingerprint, err := caseResetFingerprint(reset)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(CaseResetResult{Archived: archived, Replacement: replacement, CancelledAttemptID: attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "legacy-reset-event", archived.ID, CaseFixing, CaseResetArchived, "case_reset", "user", command.ActorID, command.IdempotencyKey, `{}`, formatStoreTime(now), resetFingerprint, string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	if auditType != "" {
		auditKey := command.IdempotencyKey + ":runner-cancel"
		fingerprintMaterial, err := json.Marshal(struct {
			CaseID      string          `json:"case_id"`
			Key         string          `json:"key"`
			EventType   string          `json:"event_type"`
			ActorType   string          `json:"actor_type"`
			ActorID     string          `json:"actor_id"`
			PayloadJSON json.RawMessage `json:"payload_json"`
		}{archived.ID, auditKey, auditType, "studio", "orchestrator", json.RawMessage(auditPayload)})
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(fingerprintMaterial)
		auditResultJSON, err := json.Marshal(archived)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", auditKey), archived.ID, CaseResetArchived, CaseResetArchived, auditType, "studio", "orchestrator", auditKey, auditPayload, formatStoreTime(now), hex.EncodeToString(digest[:]), string(auditResultJSON)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, command, resetFingerprint
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestManualVersionVerifierRequiresCompleteCommitProof(t *testing.T) {
	verifier := ManualVersionVerifier{
		Environment: "test",
		IsDescendant: func(_ context.Context, repo, expected, observed string) (bool, error) {
			return repo == "api" && expected == "merge-api" && observed == "release-api", nil
		},
	}
	request := DeploymentVerificationRequest{
		CaseID: "case-1", Environment: "test", Source: "manual",
		ExpectedCommits: map[string]string{"api": "merge-api", "worker": "merge-worker"},
		ObservedVersion: "build-42",
		ObservedCommits: map[string]string{"api": "release-api", "worker": "merge-worker"},
	}
	got, err := verifier.Verify(context.Background(), request)
	if err != nil || got.Result != DeploymentResultMatched || got.VerifiedAt == nil {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
	if got.ObservedCommits["api"] != "release-api" || got.VerifiedCommitAncestors["api"] != "merge-api" {
		t.Fatalf("descendant proof was not retained: %+v", got)
	}

	request.ObservedCommits = map[string]string{"api": "release-api"}
	got, err = verifier.Verify(context.Background(), request)
	if err != nil || got.Result != DeploymentResultMismatched {
		t.Fatalf("missing repository observation=%+v err=%v", got, err)
	}
}

func TestManualVersionVerifierRejectsInvalidMetadata(t *testing.T) {
	verifier := ManualVersionVerifier{Environment: "test"}
	base := DeploymentVerificationRequest{CaseID: "case", Environment: "test", Source: "manual", ExpectedCommits: map[string]string{"api": "abc"}, ObservedVersion: "v1", ObservedCommits: map[string]string{"api": "abc"}}
	for name, mutate := range map[string]func(*DeploymentVerificationRequest){
		"missing version":   func(v *DeploymentVerificationRequest) { v.ObservedVersion = "" },
		"missing source":    func(v *DeploymentVerificationRequest) { v.Source = "" },
		"wrong environment": func(v *DeploymentVerificationRequest) { v.Environment = "prod" },
	} {
		t.Run(name, func(t *testing.T) {
			request := base.Clone()
			mutate(&request)
			got, err := verifier.Verify(context.Background(), request)
			if err != nil || got.Result == DeploymentResultMatched {
				t.Fatalf("observation=%+v err=%v", got, err)
			}
		})
	}
}

func TestCompositeDeploymentVerifierAddsBoundedDiagnosticToEveryNonMatch(t *testing.T) {
	tests := map[string]*CompositeDeploymentVerifier{
		"nil composite": nil,
		"provider result without diagnostic": NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{
			"manual": &staticDeploymentVerifier{result: DeploymentResultMismatched},
		}),
	}
	for name, verifier := range tests {
		t.Run(name, func(t *testing.T) {
			got, _ := verifier.Verify(context.Background(), DeploymentVerificationRequest{Source: "manual"})
			if got.Result == DeploymentResultMatched || got.DiagnosticCode == "" || got.DiagnosticMessage == "" || len(got.DiagnosticCode) > 64 || len(got.DiagnosticMessage) > 128 {
				t.Fatalf("observation=%+v", got)
			}
		})
	}
}

type staticDeploymentVerifier struct {
	called int
	result DeploymentResult
}

type secretFailingDeploymentVerifier struct{}

func (secretFailingDeploymentVerifier) Verify(context.Context, DeploymentVerificationRequest) (DeploymentObservation, error) {
	return DeploymentObservation{VerificationSource: "http", Result: DeploymentResultUnavailable}, errors.New("Authorization: Bearer raw-secret https://user:pass@example.test")
}

func TestDeploymentVerifierRawFailureIsNeverPersisted(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := addPushedWorkflowChange(t, store, createWorkflowCase(t, store, "secret-deployment", CaseWaitingDeployment))
	o := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, secretFailingDeploymentVerifier{})
	_, err := o.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "secret-notice", ActorID: "alice"})
	if err == nil {
		t.Fatal("expected verifier failure")
	}
	events, listErr := store.ListEvents(ctx, incident.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	observations, listErr := store.ListDeploymentObservations(ctx, incident.ID)
	if listErr != nil || len(observations) != 1 || observations[0].DiagnosticCode != "verifier_unavailable" || observations[0].ObservedAt.IsZero() {
		t.Fatalf("observations=%+v err=%v", observations, listErr)
	}
	persisted, _ := json.Marshal(struct {
		Events       []TransitionEvent
		Observations []DeploymentObservation
	}{events, observations})
	for _, secret := range []string{"raw-secret", "user:pass", "Authorization"} {
		if strings.Contains(string(persisted), secret) {
			t.Fatalf("persisted verifier secret %q: %s", secret, persisted)
		}
	}
}

func (v *staticDeploymentVerifier) Verify(_ context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	v.called++
	return DeploymentObservation{Environment: request.Environment, ExpectedCommits: CloneStringMap(request.ExpectedCommits), ObservedVersion: request.ObservedVersion, ObservedCommits: CloneStringMap(request.ObservedCommits), VerificationSource: request.Source, Result: v.result}, nil
}

func TestCompositeDeploymentVerifierSelectsExactSource(t *testing.T) {
	manual := &staticDeploymentVerifier{result: DeploymentResultMatched}
	http := &staticDeploymentVerifier{result: DeploymentResultMismatched}
	composite := NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": manual, "http": http})
	got, err := composite.Verify(context.Background(), DeploymentVerificationRequest{Source: "http", Environment: "test", ExpectedCommits: map[string]string{"api": "x"}})
	if err != nil || got.Result != DeploymentResultMismatched || http.called != 1 || manual.called != 0 {
		t.Fatalf("observation=%+v manual=%d http=%d err=%v", got, manual.called, http.called, err)
	}
	if _, err := composite.Verify(context.Background(), DeploymentVerificationRequest{}); err != nil || manual.called != 1 {
		t.Fatalf("empty legacy source did not select manual: calls=%d err=%v", manual.called, err)
	}
	if _, err := composite.Verify(context.Background(), DeploymentVerificationRequest{Source: "unknown"}); !errors.Is(err, ErrDeploymentVerifierUnavailable) {
		t.Fatalf("unknown source error=%v", err)
	}
}

func TestNotifyDeployedManualProofPersistsResultAndReplaysExactly(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "manual-deployment", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	runner := &recordingPhaseRunner{}
	verifier := NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": ManualVersionVerifier{Environment: "test"}})
	orchestrator := NewCaseOrchestrator(store, runner, nil, verifier)
	command := NotifyDeployedCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "manual-deployment-notice", ActorID: "alice",
		ObservedVersion: "build-42", ObservedCommits: map[string]string{"repo": "merge-1"}, Source: "manual",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{"scenario":"original"}`),
	}
	first, err := orchestrator.NotifyDeployed(ctx, command)
	if err != nil || first.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d err=%v", first, runner.startCount(), err)
	}
	reservationEvent, found, err := store.GetEventByIdempotencyKey(ctx, fmt.Sprintf("deployment-reserve:%s:v%d", incident.ID, incident.Version))
	if err != nil || !found || reservationEvent.ActorID != "alice" {
		t.Fatalf("reservation event=%+v found=%v err=%v", reservationEvent, found, err)
	}
	var reservation DeploymentReservation
	if err := json.Unmarshal(reservationEvent.PayloadJSON, &reservation); err != nil || reservation.CallerIdempotencyKey != command.IdempotencyKey || reservation.ActorID != command.ActorID {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	restarted := NewCaseOrchestrator(store, runner, nil, verifier)
	second, err := restarted.NotifyDeployed(ctx, command)
	if err != nil || second.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", second, runner.startCount(), err)
	}
	observations, err := store.ListDeploymentObservations(ctx, incident.ID)
	if err != nil || len(observations) != 1 || observations[0].Result != DeploymentResultMatched || observations[0].ObservedVersion != "build-42" {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	regressions := 0
	for _, attempt := range attempts {
		if attempt.Phase == PhaseRegression {
			regressions++
		}
	}
	if err != nil || regressions != 1 {
		t.Fatalf("attempts=%+v regressions=%d err=%v", attempts, regressions, err)
	}
	changedActor := command
	changedActor.ActorID = "bob"
	if _, err := restarted.NotifyDeployed(ctx, changedActor); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different actor replay error=%v", err)
	}
	changedKey := command
	changedKey.IdempotencyKey = "another-notification"
	if _, err := restarted.NotifyDeployed(ctx, changedKey); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different caller key replay error=%v", err)
	}
	changedProof := command
	changedProof.ObservedVersion = "build-43"
	if _, err := restarted.NotifyDeployed(ctx, changedProof); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different deployment proof replay error=%v", err)
	}
}

func TestNotifyDeployedPersistsMismatchedAndUnavailableWithoutRegression(t *testing.T) {
	for name, fixture := range map[string]struct {
		version string
		commit  string
		result  DeploymentResult
	}{
		"mismatched":  {version: "old-build", commit: "old-commit", result: DeploymentResultMismatched},
		"unavailable": {version: "", commit: "merge-1", result: DeploymentResultUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "manual-"+name, CaseWaitingDeployment)
			incident = addPushedWorkflowChange(t, store, incident)
			runner := &recordingPhaseRunner{}
			orchestrator := NewCaseOrchestrator(store, runner, nil, NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": ManualVersionVerifier{Environment: "test"}}))
			got, err := orchestrator.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "notice-" + name, ActorID: "alice", ObservedVersion: fixture.version, ObservedCommits: map[string]string{"repo": fixture.commit}, Source: "manual"})
			if err != nil || got.Status != CaseDeploymentUnverified || runner.startCount() != 0 {
				t.Fatalf("case=%+v starts=%d err=%v", got, runner.startCount(), err)
			}
			observations, listErr := store.ListDeploymentObservations(ctx, incident.ID)
			if listErr != nil || len(observations) != 1 || observations[0].Result != fixture.result {
				t.Fatalf("observations=%+v err=%v", observations, listErr)
			}
		})
	}
}

func TestNotifyDeployedReservationIdentitySurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident := createWorkflowCase(t, store, "restart-deployment", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
	command := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "restart-notice", ActorID: "alice", ObservedVersion: "old"}
	if _, err := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, verifier).NotifyDeployed(ctx, command); err != nil {
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
	restarted := NewCaseOrchestrator(reopened, &recordingPhaseRunner{}, nil, verifier)
	if _, err := restarted.NotifyDeployed(ctx, command); err != nil {
		t.Fatalf("exact restart replay error=%v", err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("restart replay verifier calls=%d", len(verifier.requests))
	}
	command.ActorID = "bob"
	if _, err := restarted.NotifyDeployed(ctx, command); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("restart actor conflict error=%v", err)
	}
}

func TestNotifyDeployedReplayRejectsReservationEventPayloadActorMismatch(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "event-actor-mismatch", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, verifier)
	command := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "actor-bound-notice", ActorID: "alice", ObservedVersion: "old"}
	if _, err := orchestrator.NotifyDeployed(ctx, command); err != nil {
		t.Fatal(err)
	}
	reserveKey := fmt.Sprintf("deployment-reserve:%s:v%d", incident.ID, incident.Version)
	if _, err := store.db.ExecContext(ctx, `UPDATE transition_events SET actor_id = 'bob' WHERE idempotency_key = ?`, reserveKey); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.NotifyDeployed(ctx, command); !errors.Is(err, ErrIdempotencyConflict) || !errors.Is(err, ErrDeploymentReservationIdentityInvalid) {
		t.Fatalf("event/payload actor mismatch error=%v", err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("verifier calls=%d", len(verifier.requests))
	}
}
