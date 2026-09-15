package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

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
	archived := IncidentCase{ID: "legacy-reset-case", BugID: "legacy-reset-bug", Source: "zentao", SystemID: "base", Environment: "test", Status: CaseResetArchived, CycleNumber: 1, SelectedBotKey: "validator|codex", SupersededByCaseID: "legacy-reset-replacement", Version: 2, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, ClosedAt: &closedAt}
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
	reset := CaseReset{CaseID: command.CaseID, NewCaseID: command.NewCaseID, IdempotencyKey: command.IdempotencyKey, ActorID: command.ActorID, ExpectedVersion: command.ExpectedVersion, SelectedBotKey: command.Bot.Key, ReplacementBotTarget: command.Bot.Target, ReplacementEnvironment: command.Bug.Env, RequestJSON: mustJSON(command)}
	resetFingerprint, err := legacyCaseResetFingerprint(reset)
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
