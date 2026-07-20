package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrCaseNotFound              = errors.New("incident case not found")
	ErrCaseVersionConflict       = errors.New("incident case version conflict")
	ErrIdempotencyConflict       = errors.New("idempotency key conflicts with committed request")
	ErrAttemptAlreadyFinished    = errors.New("phase attempt is already finished")
	ErrAttemptRunClaimConflict   = errors.New("phase attempt run claim conflicts with another starter")
	ErrUnsupportedWorkflowSchema = errors.New("unsupported workflow store schema")
	resetURLUserinfoPattern      = regexp.MustCompile(`(?i)\bhttps?://[^\s/@]+(?::[^\s/@]*)?@`)
)

type CaseStore struct {
	db *sql.DB
}

// CaseCreation atomically persists a new pending Case and the exact durable
// command identity that created it. RequestJSON must include every execution
// identity field (actor, input, and full Bot context) used by the orchestrator.
type CaseCreation struct {
	Case           IncidentCase    `json:"case"`
	IdempotencyKey string          `json:"idempotency_key"`
	ActorID        string          `json:"actor_id"`
	RequestJSON    json.RawMessage `json:"request_json"`
}

// CaseCreationResult reports whether durable creation created a new Case,
// replayed the same command, or reused the Bug's current open Case.
type CaseCreationResult struct {
	Case         IncidentCase
	Replay       bool
	ExistingOpen bool
}

// CaseReset identifies one exact request to archive a non-terminal Case and
// create its pending replacement in the same transaction.
type CaseReset struct {
	CaseID                 string
	NewCaseID              string
	IdempotencyKey         string
	ActorID                string
	ExpectedVersion        int64
	SelectedBotKey         string
	ReplacementBotTarget   string
	ReplacementSystemID    string
	ReplacementEnvironment string
	RequestJSON            json.RawMessage
	// replayOnlyLegacyEnvironment is the environment that the pre-selected-Bot
	// resolver would have used. It is consulted only for an already committed
	// event and is excluded from new fingerprints, payloads, and writes.
	replayOnlyLegacyEnvironment string
}

// CaseResetResult is the immutable result stored with the reset event. Replay
// returns this snapshot rather than rebuilding it from mutable current rows.
type CaseResetResult struct {
	Archived           IncidentCase
	Replacement        IncidentCase
	CancelledAttemptID string
	Replay             bool
	// requestFingerprint is runtime-only metadata for the cancellation outbox;
	// result_case_json remains the immutable public reset snapshot.
	requestFingerprint string
}

type ResetCancellationStatus string

const (
	ResetCancellationPending   ResetCancellationStatus = "pending"
	ResetCancellationClaimed   ResetCancellationStatus = "claimed"
	ResetCancellationSucceeded ResetCancellationStatus = "succeeded"
	ResetCancellationFailed    ResetCancellationStatus = "failed"
)

// ResetCancellationOperation is the durable outbox entry for stopping the
// runner that belonged to a reset Case. A claimed entry is intentionally not
// leased: if its owner crashes, replay reports an unknown outcome and never
// calls a runner API that has no idempotency key a second time.
type ResetCancellationOperation struct {
	ResetKey           string
	CaseID             string
	AttemptID          string
	RequestFingerprint string
	Status             ResetCancellationStatus
	ClaimToken         string
	OutcomeCode        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type legacyImportBatch struct {
	MigrationKey string
	Cases        []IncidentCase
	Attempts     []PhaseAttempt
}

type workflowSchemaMigrationDetail struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

func OpenCaseStore(path string) (*CaseStore, error) {
	if path == "" {
		return nil, errors.New("case store path is required")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create case store directory: %w", err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect case store directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return nil, fmt.Errorf("case store parent %q is not a directory", parent)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return nil, fmt.Errorf("secure case store directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create case store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close new case store file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure case store permissions: %w", err)
	}
	if err := secureSQLiteFiles(path); err != nil {
		return nil, err
	}
	values := url.Values{}
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "busy_timeout(5000)")
	values.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: values.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open case store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &CaseStore{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := secureSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *CaseStore) initialize(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("initialize case store (%s): %w", pragma, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workflow schema initialization: %w", err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read workflow schema version: %w", err)
	}
	switch version {
	case 0:
		objectCount, err := workflowUserSchemaObjectCount(ctx, tx)
		if err != nil {
			return err
		}
		if objectCount != 0 {
			return fmt.Errorf("%w: pre-release unversioned workflow schema lacks exact idempotency metadata", ErrUnsupportedWorkflowSchema)
		}
		if _, err := tx.ExecContext(ctx, legacyWorkflowStoreSchema); err != nil {
			return fmt.Errorf("create workflow schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV1Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v1: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 1, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v1 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`,
			workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
			return fmt.Errorf("record workflow schema v1: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=1`); err != nil {
			return fmt.Errorf("set workflow schema version: %w", err)
		}
		version = 1
	case 1:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 1); err != nil {
			return err
		}
	case 2:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 2); err != nil {
			return err
		}
	case 3:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 3); err != nil {
			return err
		}
	case 4:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 4); err != nil {
			return err
		}
	case 5:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 5); err != nil {
			return err
		}
	case 6:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 6); err != nil {
			return err
		}
	case 7:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 7); err != nil {
			return err
		}
	case 8:
		if err := verifyWorkflowSchemaMarker(ctx, tx, 8); err != nil {
			return err
		}
	case workflowStoreSchemaVersion:
		// Verified below before the transaction is committed.
	default:
		// Future ordered migrations add explicit cases here. Unknown versions are
		// never modified or guessed.
		return fmt.Errorf("%w: user_version=%d", ErrUnsupportedWorkflowSchema, version)
	}
	if version == 1 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV2Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v2: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 2, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v2 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v2: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=2`); err != nil {
			return fmt.Errorf("set workflow schema version 2: %w", err)
		}
		version = 2
	}
	if version == 2 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV3Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v3: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 3, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v3 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v3: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
			return fmt.Errorf("set workflow schema version 3: %w", err)
		}
		version = 3
	}
	if version == 3 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV4Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v4: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 4, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v4 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v4: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=4`); err != nil {
			return fmt.Errorf("set workflow schema version 4: %w", err)
		}
		version = 4
	}
	if version == 4 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV5Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v5: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 5, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v5 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v5: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
			return fmt.Errorf("set workflow schema version 5: %w", err)
		}
		version = 5
	}
	if version == 5 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV6Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v6: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 6, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v6 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v6: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=6`); err != nil {
			return fmt.Errorf("set workflow schema version 6: %w", err)
		}
		version = 6
	}
	if version == 6 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV7Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v7: %w", err)
		}
		if err := backfillV6ResetCancellations(ctx, tx); err != nil {
			return fmt.Errorf("backfill workflow schema v7 reset cancellations: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 7, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v7 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v7: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=7`); err != nil {
			return fmt.Errorf("set workflow schema version 7: %w", err)
		}
		version = 7
	}
	if version == 7 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV8Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v8: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 8, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v8 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v8: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=8`); err != nil {
			return fmt.Errorf("set workflow schema version 8: %w", err)
		}
		version = 8
	}
	if version == 8 {
		if _, err := tx.ExecContext(ctx, workflowStoreSchemaV9Upgrade); err != nil {
			return fmt.Errorf("apply workflow schema v9: %w", err)
		}
		fingerprint, err := workflowSchemaFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: workflowStoreSchemaVersion, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v9 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v9: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=9`); err != nil {
			return fmt.Errorf("set workflow schema version 9: %w", err)
		}
	}
	tables, err := workflowTableColumns(ctx, tx)
	if err != nil {
		return err
	}
	v1Columns := cloneWorkflowColumns(legacyWorkflowTableColumns)
	v1Columns["transition_events"] = append(v1Columns["transition_events"], "request_fingerprint", "result_case_json")
	v1Columns["deployment_observations"] = append(v1Columns["deployment_observations"], "verified_commit_ancestors_json")
	v1Columns["deployment_observations"] = append(v1Columns["deployment_observations"], "observed_at", "diagnostic_code", "diagnostic_message")
	v1Columns["fix_checkpoints"] = []string{"attempt_id", "case_id", "staging_locator", "created_at"}
	v1Columns["phase_attempts"] = append(v1Columns["phase_attempts"], "completion_identity_sha256")
	v1Columns["phase_attempts"] = append(v1Columns["phase_attempts"], "run_claim_token")
	v1Columns["reset_cancellation_operations"] = []string{"reset_key", "case_id", "attempt_id", "request_fingerprint", "status", "claim_token", "outcome_code", "created_at", "updated_at"}
	v1Columns["browser_recovery_operations"] = []string{"idempotency_key", "operation", "case_id", "attempt_id", "expected_error_code", "cycle_number", "expected_version", "actor_id", "request_fingerprint", "status", "claim_token", "outcome_code", "result_case_json", "created_at", "updated_at"}
	if err := verifyWorkflowColumns(tables, v1Columns); err != nil {
		return err
	}
	if err := verifyRequiredWorkflowIndexes(ctx, tx); err != nil {
		return err
	}
	var detailJSON string
	if err := tx.QueryRowContext(ctx, `SELECT detail_json FROM schema_migrations WHERE key = ?`, workflowStoreSchemaV1Key).Scan(&detailJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: missing workflow schema v1 marker", ErrUnsupportedWorkflowSchema)
		}
		return fmt.Errorf("verify workflow schema marker: %w", err)
	}
	var detail workflowSchemaMigrationDetail
	if err := json.Unmarshal([]byte(detailJSON), &detail); err != nil || detail.Version != workflowStoreSchemaVersion || detail.Fingerprint == "" {
		return fmt.Errorf("%w: invalid workflow schema v1 marker", ErrUnsupportedWorkflowSchema)
	}
	fingerprint, err := workflowSchemaFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	if fingerprint != detail.Fingerprint {
		return fmt.Errorf("%w: workflow schema fingerprint mismatch", ErrUnsupportedWorkflowSchema)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workflow schema initialization: %w", err)
	}
	return nil
}

type legacyResetCancellation struct {
	ResetKey           string
	CaseID             string
	AttemptID          string
	RequestFingerprint string
	CreatedAt          string
	Archived           IncidentCase
}

func backfillV6ResetCancellations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT idempotency_key,case_id,request_fingerprint,result_case_json,created_at FROM transition_events WHERE event_type='case_reset' ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	var resets []legacyResetCancellation
	for rows.Next() {
		var reset legacyResetCancellation
		var resultJSON string
		if err := rows.Scan(&reset.ResetKey, &reset.CaseID, &reset.RequestFingerprint, &resultJSON, &reset.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		var result CaseResetResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			rows.Close()
			return fmt.Errorf("decode committed reset %q: %w", reset.ResetKey, err)
		}
		if blank(reset.ResetKey) || result.Archived.ID != reset.CaseID || result.Replacement.ID == "" {
			rows.Close()
			return fmt.Errorf("committed reset %q has invalid cancellation identity", reset.ResetKey)
		}
		if err := validateCaseResetResult(result, reset.CaseID, result.Replacement.ID); err != nil {
			rows.Close()
			return fmt.Errorf("validate committed reset %q: %w", reset.ResetKey, err)
		}
		if result.CancelledAttemptID == "" {
			continue
		}
		decodedFingerprint, decodeErr := hex.DecodeString(reset.RequestFingerprint)
		if decodeErr != nil || len(decodedFingerprint) != sha256.Size || reset.RequestFingerprint != strings.ToLower(reset.RequestFingerprint) {
			rows.Close()
			return fmt.Errorf("committed reset %q has invalid request fingerprint", reset.ResetKey)
		}
		if _, err := parseStoreTime(reset.CreatedAt); err != nil {
			rows.Close()
			return fmt.Errorf("committed reset %q has invalid timestamp", reset.ResetKey)
		}
		reset.AttemptID = result.CancelledAttemptID
		reset.Archived = result.Archived
		resets = append(resets, reset)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, reset := range resets {
		var attemptCaseID string
		var attemptStatus AttemptStatus
		if err := tx.QueryRowContext(ctx, `SELECT case_id,status FROM phase_attempts WHERE id=?`, reset.AttemptID).Scan(&attemptCaseID, &attemptStatus); err != nil {
			return fmt.Errorf("load committed reset %q attempt: %w", reset.ResetKey, err)
		}
		if attemptCaseID != reset.CaseID || attemptStatus != AttemptStatusCancelled {
			return fmt.Errorf("committed reset %q attempt binding is invalid", reset.ResetKey)
		}
		status, outcome, err := legacyResetCancellationAuditOutcome(ctx, tx, reset)
		if err != nil {
			return fmt.Errorf("load committed reset %q cancellation audit: %w", reset.ResetKey, err)
		}
		claimToken := "migration-v6:" + reset.ResetKey
		if _, err := tx.ExecContext(ctx, `INSERT INTO reset_cancellation_operations (reset_key,case_id,attempt_id,request_fingerprint,status,claim_token,outcome_code,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, reset.ResetKey, reset.CaseID, reset.AttemptID, reset.RequestFingerprint, status, claimToken, outcome, reset.CreatedAt, reset.CreatedAt); err != nil {
			return fmt.Errorf("insert committed reset %q cancellation: %w", reset.ResetKey, err)
		}
	}
	return nil
}

func legacyResetCancellationAuditOutcome(ctx context.Context, tx *sql.Tx, reset legacyResetCancellation) (ResetCancellationStatus, string, error) {
	unknown := func() (ResetCancellationStatus, string, error) { return ResetCancellationClaimed, "", nil }
	auditKey := reset.ResetKey + ":runner-cancel"
	var eventID, auditCaseID, fromStatus, toStatus, auditType, actorType, actorID, payloadJSON, createdAt, requestFingerprint, resultJSON string
	err := tx.QueryRowContext(ctx, `SELECT id,case_id,from_status,to_status,event_type,actor_type,actor_id,payload_json,created_at,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, auditKey).Scan(&eventID, &auditCaseID, &fromStatus, &toStatus, &auditType, &actorType, &actorID, &payloadJSON, &createdAt, &requestFingerprint, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return unknown()
	}
	if err != nil {
		return "", "", err
	}
	if eventID != stableID("event", auditKey) || auditCaseID != reset.CaseID || CaseStatus(fromStatus) != CaseResetArchived || CaseStatus(toStatus) != CaseResetArchived || actorType != "studio" || actorID != "orchestrator" {
		return unknown()
	}
	if _, err := parseStoreTime(createdAt); err != nil {
		return unknown()
	}
	var payload struct {
		AttemptID   string `json:"attempt_id"`
		Outcome     string `json:"outcome"`
		WarningCode string `json:"warning_code"`
	}
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || payload.AttemptID != reset.AttemptID {
		return unknown()
	}
	status, outcome := ResetCancellationClaimed, ""
	expectedPayload := map[string]string{"attempt_id": reset.AttemptID}
	switch {
	case auditType == "reset_runner_cancel_succeeded" && payload.Outcome == "succeeded" && payload.WarningCode == "":
		status, outcome = ResetCancellationSucceeded, "succeeded"
		expectedPayload["outcome"] = "succeeded"
	case auditType == "reset_runner_cancel_failed" && payload.Outcome == "failed" && payload.WarningCode == "reset_runner_cancel_failed":
		status, outcome = ResetCancellationFailed, "runner_cancel_failed"
		expectedPayload["outcome"] = "failed"
		expectedPayload["warning_code"] = "reset_runner_cancel_failed"
	default:
		return unknown()
	}
	expectedPayloadJSON, err := json.Marshal(expectedPayload)
	if err != nil || payloadJSON != string(expectedPayloadJSON) {
		return unknown()
	}
	fingerprintMaterial, err := json.Marshal(struct {
		CaseID      string          `json:"case_id"`
		Key         string          `json:"key"`
		EventType   string          `json:"event_type"`
		ActorType   string          `json:"actor_type"`
		ActorID     string          `json:"actor_id"`
		PayloadJSON json.RawMessage `json:"payload_json"`
	}{reset.CaseID, auditKey, auditType, actorType, actorID, json.RawMessage(payloadJSON)})
	if err != nil {
		return unknown()
	}
	digest := sha256.Sum256(fingerprintMaterial)
	if requestFingerprint != hex.EncodeToString(digest[:]) {
		return unknown()
	}
	storedArchived, err := getCase(ctx, tx, reset.CaseID)
	if err != nil {
		return "", "", err
	}
	expectedResultJSON, err := json.Marshal(storedArchived)
	if err != nil {
		return "", "", err
	}
	resetArchivedJSON, err := json.Marshal(reset.Archived)
	if err != nil {
		return "", "", err
	}
	var decodedResult IncidentCase
	if json.Unmarshal([]byte(resultJSON), &decodedResult) != nil || !bytes.Equal([]byte(resultJSON), expectedResultJSON) || !bytes.Equal(resetArchivedJSON, expectedResultJSON) {
		return unknown()
	}
	return status, outcome, nil
}

func verifyWorkflowSchemaMarker(ctx context.Context, tx *sql.Tx, expectedVersion int) error {
	var detailJSON string
	if err := tx.QueryRowContext(ctx, `SELECT detail_json FROM schema_migrations WHERE key = ?`, workflowStoreSchemaV1Key).Scan(&detailJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: missing workflow schema v1 marker", ErrUnsupportedWorkflowSchema)
		}
		return fmt.Errorf("verify workflow schema marker: %w", err)
	}
	var detail workflowSchemaMigrationDetail
	if err := json.Unmarshal([]byte(detailJSON), &detail); err != nil || detail.Version != expectedVersion || detail.Fingerprint == "" {
		return fmt.Errorf("%w: invalid workflow schema marker", ErrUnsupportedWorkflowSchema)
	}
	fingerprint, err := workflowSchemaFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	if fingerprint != detail.Fingerprint {
		return fmt.Errorf("%w: workflow schema fingerprint mismatch", ErrUnsupportedWorkflowSchema)
	}
	return nil
}

func (s *CaseStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *CaseStore) CreateCase(ctx context.Context, incident IncidentCase) error {
	incident = incident.Clone()
	if incident.ClosedAt != nil && incident.ClosedAt.IsZero() {
		return errors.New("incident case closed_at must not be zero when provided")
	}
	if err := incident.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if incident.Version == 0 {
		incident.Version = 1
	}
	if incident.Version < 1 {
		return errors.New("incident case version must be positive")
	}
	if incident.CreatedAt.IsZero() {
		incident.CreatedAt = now
	}
	if incident.UpdatedAt.IsZero() {
		incident.UpdatedAt = incident.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO incident_cases (
		id, bug_id, source, system_id, environment, status, cycle_number,
		current_attempt_id, selected_bot_key, reset_from_case_id, superseded_by_case_id,
		version, created_at, updated_at, closed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		incident.ID, incident.BugID, incident.Source, incident.SystemID, incident.Environment,
		incident.Status, incident.CycleNumber, incident.CurrentAttemptID, incident.SelectedBotKey,
		incident.ResetFromCaseID, incident.SupersededByCaseID,
		incident.Version, formatStoreTime(incident.CreatedAt), formatStoreTime(incident.UpdatedAt),
		formatOptionalStoreTime(incident.ClosedAt),
	)
	if err != nil {
		return fmt.Errorf("create incident case: %w", err)
	}
	return nil
}

func (s *CaseStore) CreateCaseWithIdentity(ctx context.Context, creation CaseCreation) (result CaseCreationResult, err error) {
	creation.Case = creation.Case.Clone()
	if creation.Case.Status != CasePendingValidation || creation.Case.CurrentAttemptID != "" || creation.Case.ClosedAt != nil {
		return result, errors.New("durable Case creation requires an open pending_validation Case without an attempt")
	}
	if blank(creation.IdempotencyKey) || blank(creation.ActorID) || len(creation.RequestJSON) == 0 || !json.Valid(creation.RequestJSON) {
		return result, errors.New("durable Case creation requires idempotency key, actor, and valid request JSON")
	}
	if err := creation.Case.Validate(); err != nil {
		return result, err
	}
	fingerprint, err := caseCreationFingerprint(creation)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin durable Case creation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var eventType, storedFingerprint, resultJSON string
	queryErr := tx.QueryRowContext(ctx, `SELECT event_type,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, creation.IdempotencyKey).Scan(&eventType, &storedFingerprint, &resultJSON)
	if queryErr == nil {
		if (eventType != "case_created" && eventType != "open_case_reused") || storedFingerprint != fingerprint {
			return result, ErrIdempotencyConflict
		}
		if err = json.Unmarshal([]byte(resultJSON), &result.Case); err != nil {
			return result, fmt.Errorf("decode durable Case creation replay: %w", err)
		}
		result.Replay = true
		result.ExistingOpen = eventType == "open_case_reused"
		if err = tx.Commit(); err != nil {
			return CaseCreationResult{}, err
		}
		result.Case = result.Case.Clone()
		return result, nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return result, queryErr
	}
	openCase, openErr := scanCase(tx.QueryRowContext(ctx, `SELECT id, bug_id, source, system_id,
		environment, status, cycle_number, current_attempt_id, selected_bot_key,
		reset_from_case_id, superseded_by_case_id, version, created_at, updated_at, closed_at
		FROM incident_cases WHERE bug_id = ? AND status NOT IN (?, ?, ?)
		ORDER BY updated_at DESC, id DESC LIMIT 1`, creation.Case.BugID, CaseFixedVerified, CaseLegacyArchived, CaseResetArchived))
	if openErr == nil {
		result.Case = openCase.Clone()
		result.ExistingOpen = true
		resultBytes, marshalErr := json.Marshal(result.Case)
		if marshalErr != nil {
			return CaseCreationResult{}, marshalErr
		}
		payload := mustJSON(map[string]any{"case_id": result.Case.ID, "requested_case_id": creation.Case.ID})
		now := time.Now().UTC()
		if _, err = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", "open-case-reused:"+creation.IdempotencyKey), result.Case.ID, result.Case.Status, result.Case.Status, "open_case_reused", "user", creation.ActorID, creation.IdempotencyKey, string(payload), formatStoreTime(now), fingerprint, string(resultBytes)); err != nil {
			return CaseCreationResult{}, fmt.Errorf("insert open Case reuse identity: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return CaseCreationResult{}, err
		}
		return result, nil
	}
	if !errors.Is(openErr, sql.ErrNoRows) {
		return result, openErr
	}
	if _, caseErr := getCase(ctx, tx, creation.Case.ID); caseErr == nil {
		return result, ErrIdempotencyConflict
	} else if !errors.Is(caseErr, ErrCaseNotFound) {
		return result, caseErr
	}
	now := time.Now().UTC()
	created := creation.Case.Clone()
	if created.Version == 0 {
		created.Version = 1
	}
	if created.Version != 1 {
		return CaseCreationResult{}, errors.New("new durable Case version must be one")
	}
	if created.CreatedAt.IsZero() {
		created.CreatedAt = now
	}
	if created.UpdatedAt.IsZero() {
		created.UpdatedAt = created.CreatedAt
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO incident_cases (id,bug_id,source,system_id,environment,status,cycle_number,current_attempt_id,selected_bot_key,reset_from_case_id,superseded_by_case_id,version,created_at,updated_at,closed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, created.ID, created.BugID, created.Source, created.SystemID, created.Environment, created.Status, created.CycleNumber, created.CurrentAttemptID, created.SelectedBotKey, created.ResetFromCaseID, created.SupersededByCaseID, created.Version, formatStoreTime(created.CreatedAt), formatStoreTime(created.UpdatedAt), nil); err != nil {
		return CaseCreationResult{}, fmt.Errorf("insert durable Case: %w", err)
	}
	resultBytes, marshalErr := json.Marshal(created)
	if marshalErr != nil {
		return CaseCreationResult{}, marshalErr
	}
	resultJSON = string(resultBytes)
	payload := mustJSON(map[string]any{"case_id": created.ID, "cycle_number": created.CycleNumber})
	if _, err = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", "case-created:"+creation.IdempotencyKey), created.ID, created.Status, created.Status, "case_created", "user", creation.ActorID, creation.IdempotencyKey, string(payload), formatStoreTime(now), fingerprint, resultJSON); err != nil {
		return CaseCreationResult{}, fmt.Errorf("insert durable Case creation identity: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return CaseCreationResult{}, err
	}
	result.Case = created.Clone()
	return result, nil
}

func caseCreationFingerprint(creation CaseCreation) (string, error) {
	identity := struct {
		CaseID         string          `json:"case_id"`
		BugID          string          `json:"bug_id"`
		Source         string          `json:"source"`
		SystemID       string          `json:"system_id"`
		Environment    string          `json:"environment"`
		CycleNumber    int             `json:"cycle_number"`
		SelectedBotKey string          `json:"selected_bot_key"`
		IdempotencyKey string          `json:"idempotency_key"`
		ActorID        string          `json:"actor_id"`
		RequestJSON    json.RawMessage `json:"request_json"`
	}{
		CaseID:         creation.Case.ID,
		BugID:          creation.Case.BugID,
		Source:         creation.Case.Source,
		SystemID:       creation.Case.SystemID,
		Environment:    creation.Case.Environment,
		CycleNumber:    creation.Case.CycleNumber,
		SelectedBotKey: creation.Case.SelectedBotKey,
		IdempotencyKey: creation.IdempotencyKey,
		ActorID:        creation.ActorID,
		RequestJSON:    CloneRawMessage(creation.RequestJSON),
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// ResetCaseWithReplacement archives one Case and creates its replacement as a
// single durable mutation. Historical attempts and related records remain
// attached to the archived Case.
func (s *CaseStore) ResetCaseWithReplacement(ctx context.Context, reset CaseReset) (result CaseResetResult, err error) {
	reset.RequestJSON = CloneRawMessage(reset.RequestJSON)
	if s == nil || s.db == nil || blank(reset.CaseID) || blank(reset.NewCaseID) ||
		blank(reset.IdempotencyKey) || blank(reset.ActorID) || blank(reset.SelectedBotKey) ||
		blank(reset.ReplacementBotTarget) || blank(reset.ReplacementEnvironment) || reset.ExpectedVersion < 1 {
		return result, errors.New("Case reset requires store, old and new Case IDs, positive version, idempotency key, actor, and replacement Bot binding")
	}
	if reset.CaseID == reset.NewCaseID {
		return result, errors.New("replacement Case ID must differ from archived Case ID")
	}
	if len(reset.RequestJSON) == 0 || !json.Valid(reset.RequestJSON) {
		return result, errors.New("Case reset request must be valid JSON")
	}
	fingerprint, err := caseResetFingerprint(reset)
	if err != nil {
		return result, err
	}
	legacyFingerprint, err := legacyCaseResetFingerprint(reset)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin Case reset: %w", err)
	}
	defer tx.Rollback()

	var eventType, storedFingerprint, resultJSON string
	queryErr := tx.QueryRowContext(ctx, `SELECT event_type,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, reset.IdempotencyKey).Scan(&eventType, &storedFingerprint, &resultJSON)
	if queryErr == nil {
		if eventType != "case_reset" {
			return result, fmt.Errorf("%w: Case reset key %q", ErrIdempotencyConflict, reset.IdempotencyKey)
		}
		type replayBinding struct {
			environment          string
			requireDerivedTarget bool
		}
		acceptedBindings := []replayBinding{}
		switch storedFingerprint {
		case fingerprint:
			acceptedBindings = append(acceptedBindings, replayBinding{environment: reset.ReplacementEnvironment})
		case legacyFingerprint:
			acceptedBindings = append(acceptedBindings, replayBinding{environment: reset.ReplacementEnvironment, requireDerivedTarget: true})
			if !blank(reset.replayOnlyLegacyEnvironment) && reset.replayOnlyLegacyEnvironment != reset.ReplacementEnvironment {
				acceptedBindings = append(acceptedBindings, replayBinding{environment: reset.replayOnlyLegacyEnvironment, requireDerivedTarget: true})
			}
		default:
			if !blank(reset.replayOnlyLegacyEnvironment) {
				legacyReset := reset
				legacyReset.ReplacementEnvironment = reset.replayOnlyLegacyEnvironment
				legacyExpandedFingerprint, fingerprintErr := caseResetFingerprint(legacyReset)
				if fingerprintErr != nil {
					return CaseResetResult{}, fingerprintErr
				}
				if storedFingerprint == legacyExpandedFingerprint {
					acceptedBindings = append(acceptedBindings, replayBinding{environment: reset.replayOnlyLegacyEnvironment})
				}
			}
		}
		if len(acceptedBindings) == 0 {
			return result, fmt.Errorf("%w: Case reset key %q", ErrIdempotencyConflict, reset.IdempotencyKey)
		}
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return CaseResetResult{}, fmt.Errorf("decode Case reset replay: %w", err)
		}
		if err := validateCaseResetResult(result, reset.CaseID, reset.NewCaseID); err != nil {
			return CaseResetResult{}, err
		}
		bindingMatches := false
		for _, accepted := range acceptedBindings {
			if caseResetBindingMatches(reset, result.Replacement, accepted.environment, accepted.requireDerivedTarget) {
				bindingMatches = true
				break
			}
		}
		if !bindingMatches {
			return CaseResetResult{}, fmt.Errorf("%w: Case reset binding %q", ErrIdempotencyConflict, reset.IdempotencyKey)
		}
		if result.CancelledAttemptID != "" {
			operation, found, operationErr := getResetCancellationOperation(ctx, tx, reset.IdempotencyKey)
			if operationErr != nil {
				return CaseResetResult{}, fmt.Errorf("load replayed reset cancellation: %w", operationErr)
			}
			if !found || operation.CaseID != result.Archived.ID || operation.AttemptID != result.CancelledAttemptID || operation.RequestFingerprint != storedFingerprint {
				return CaseResetResult{}, fmt.Errorf("%w: reset cancellation identity %q", ErrIdempotencyConflict, reset.IdempotencyKey)
			}
		}
		result.Replay = true
		result.requestFingerprint = storedFingerprint
		if err := tx.Commit(); err != nil {
			return CaseResetResult{}, fmt.Errorf("commit Case reset replay: %w", err)
		}
		return cloneCaseResetResult(result), nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return result, fmt.Errorf("load Case reset identity: %w", queryErr)
	}
	if _, found, operationErr := getResetCancellationOperation(ctx, tx, reset.IdempotencyKey); operationErr != nil {
		return result, fmt.Errorf("load reset cancellation identity: %w", operationErr)
	} else if found {
		return result, fmt.Errorf("%w: reset cancellation key %q", ErrIdempotencyConflict, reset.IdempotencyKey)
	}
	if err := rejectUnresolvedBrowserRecovery(ctx, tx, reset.CaseID); err != nil {
		return result, err
	}

	incident, err := getCase(ctx, tx, reset.CaseID)
	if err != nil {
		return result, err
	}
	if incident.Version != reset.ExpectedVersion {
		return result, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, reset.ExpectedVersion, incident.Version)
	}
	if IsTerminalCaseStatus(incident.Status) {
		return result, fmt.Errorf("terminal Case status %s cannot be reset", incident.Status)
	}
	if _, duplicateErr := getCase(ctx, tx, reset.NewCaseID); duplicateErr == nil {
		return result, fmt.Errorf("replacement Case %q already exists", reset.NewCaseID)
	} else if !errors.Is(duplicateErr, ErrCaseNotFound) {
		return result, duplicateErr
	}

	now := time.Now().UTC()
	if incident.CurrentAttemptID != "" {
		cancelled, cancelErr := tx.ExecContext(ctx, `UPDATE phase_attempts SET status=?,finished_at=?,run_claim_token='' WHERE id=? AND case_id=? AND status IN (?,?)`, AttemptStatusCancelled, formatStoreTime(now), incident.CurrentAttemptID, incident.ID, AttemptStatusQueued, AttemptStatusRunning)
		if cancelErr != nil {
			return result, fmt.Errorf("cancel current Case attempt: %w", cancelErr)
		}
		rows, rowsErr := cancelled.RowsAffected()
		if rowsErr != nil {
			return result, fmt.Errorf("inspect current Case attempt cancellation: %w", rowsErr)
		}
		if rows == 1 {
			result.CancelledAttemptID = incident.CurrentAttemptID
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fix_checkpoints WHERE attempt_id=? AND case_id=?`, incident.CurrentAttemptID, incident.ID); err != nil {
			return result, fmt.Errorf("delete reset Case fix checkpoint: %w", err)
		}
	}
	if result.CancelledAttemptID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reset_cancellation_operations (reset_key,case_id,attempt_id,request_fingerprint,status,claim_token,outcome_code,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, reset.IdempotencyKey, incident.ID, result.CancelledAttemptID, fingerprint, ResetCancellationPending, "", "", formatStoreTime(now), formatStoreTime(now)); err != nil {
			return result, fmt.Errorf("insert reset cancellation operation: %w", err)
		}
	}

	fromStatus := incident.Status
	archived := incident.Clone()
	archived.Status = CaseResetArchived
	archived.CurrentAttemptID = ""
	archived.SupersededByCaseID = reset.NewCaseID
	archived.Version++
	archived.UpdatedAt = now
	archived.ClosedAt = cloneTimePtr(&now)
	if err := archived.Validate(); err != nil {
		return result, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE incident_cases SET status=?,current_attempt_id='',superseded_by_case_id=?,version=?,updated_at=?,closed_at=? WHERE id=? AND version=?`, archived.Status, archived.SupersededByCaseID, archived.Version, formatStoreTime(now), formatStoreTime(now), archived.ID, reset.ExpectedVersion)
	if err != nil {
		return result, fmt.Errorf("archive reset Case: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("inspect reset Case archive: %w", err)
	}
	if rows != 1 {
		return result, ErrCaseVersionConflict
	}

	replacementSystemID := incident.SystemID
	if blank(replacementSystemID) {
		replacementSystemID = strings.TrimSpace(reset.ReplacementSystemID)
	}
	replacement := IncidentCase{
		ID:              reset.NewCaseID,
		BugID:           incident.BugID,
		Source:          incident.Source,
		SystemID:        replacementSystemID,
		Environment:     reset.ReplacementEnvironment,
		Status:          CasePendingValidation,
		CycleNumber:     1,
		SelectedBotKey:  reset.SelectedBotKey,
		ResetFromCaseID: incident.ID,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := replacement.Validate(); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO incident_cases (id,bug_id,source,system_id,environment,status,cycle_number,current_attempt_id,selected_bot_key,reset_from_case_id,superseded_by_case_id,version,created_at,updated_at,closed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, replacement.ID, replacement.BugID, replacement.Source, replacement.SystemID, replacement.Environment, replacement.Status, replacement.CycleNumber, replacement.CurrentAttemptID, replacement.SelectedBotKey, replacement.ResetFromCaseID, replacement.SupersededByCaseID, replacement.Version, formatStoreTime(now), formatStoreTime(now), nil); err != nil {
		return result, fmt.Errorf("insert reset replacement Case: %w", err)
	}

	result.Archived = archived.Clone()
	result.Replacement = replacement.Clone()
	result.requestFingerprint = fingerprint
	resultJSONBytes, err := json.Marshal(result)
	if err != nil {
		return CaseResetResult{}, fmt.Errorf("encode Case reset result: %w", err)
	}
	payload, err := caseResetEventPayload(reset, result)
	if err != nil {
		return CaseResetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", "case-reset:"+reset.IdempotencyKey), archived.ID, fromStatus, archived.Status, "case_reset", "user", reset.ActorID, reset.IdempotencyKey, string(payload), formatStoreTime(now), fingerprint, string(resultJSONBytes)); err != nil {
		return CaseResetResult{}, fmt.Errorf("insert Case reset event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", "case-created-from-reset:"+reset.IdempotencyKey), replacement.ID, replacement.Status, replacement.Status, "case_created_from_reset", "user", reset.ActorID, reset.IdempotencyKey+":replacement", string(payload), formatStoreTime(now.Add(time.Nanosecond)), fingerprint, string(resultJSONBytes)); err != nil {
		return CaseResetResult{}, fmt.Errorf("insert reset replacement event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CaseResetResult{}, fmt.Errorf("commit Case reset: %w", err)
	}
	return cloneCaseResetResult(result), nil
}

func getResetCancellationOperation(ctx context.Context, query caseQuery, resetKey string) (ResetCancellationOperation, bool, error) {
	var operation ResetCancellationOperation
	var createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT reset_key,case_id,attempt_id,request_fingerprint,status,claim_token,outcome_code,created_at,updated_at FROM reset_cancellation_operations WHERE reset_key=?`, resetKey).Scan(&operation.ResetKey, &operation.CaseID, &operation.AttemptID, &operation.RequestFingerprint, &operation.Status, &operation.ClaimToken, &operation.OutcomeCode, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ResetCancellationOperation{}, false, nil
	}
	if err != nil {
		return ResetCancellationOperation{}, false, err
	}
	operation.CreatedAt, err = parseStoreTime(createdAt)
	if err != nil {
		return ResetCancellationOperation{}, false, err
	}
	operation.UpdatedAt, err = parseStoreTime(updatedAt)
	if err != nil {
		return ResetCancellationOperation{}, false, err
	}
	decodedFingerprint, decodeErr := hex.DecodeString(operation.RequestFingerprint)
	if operation.ResetKey == "" || operation.CaseID == "" || operation.AttemptID == "" || decodeErr != nil || len(decodedFingerprint) != sha256.Size {
		return ResetCancellationOperation{}, false, errors.New("reset cancellation operation identity is invalid")
	}
	switch operation.Status {
	case ResetCancellationPending:
		if operation.ClaimToken != "" || operation.OutcomeCode != "" {
			return ResetCancellationOperation{}, false, errors.New("pending reset cancellation operation is invalid")
		}
	case ResetCancellationClaimed:
		if operation.ClaimToken == "" || operation.OutcomeCode != "" {
			return ResetCancellationOperation{}, false, errors.New("claimed reset cancellation operation is invalid")
		}
	case ResetCancellationSucceeded:
		if operation.ClaimToken == "" || operation.OutcomeCode != "succeeded" {
			return ResetCancellationOperation{}, false, errors.New("successful reset cancellation operation is invalid")
		}
	case ResetCancellationFailed:
		if operation.ClaimToken == "" || operation.OutcomeCode != "runner_cancel_failed" {
			return ResetCancellationOperation{}, false, errors.New("failed reset cancellation operation is invalid")
		}
	default:
		return ResetCancellationOperation{}, false, errors.New("reset cancellation operation status is invalid")
	}
	return operation, true, nil
}

func (s *CaseStore) GetResetCancellationOperation(ctx context.Context, resetKey, fingerprint string) (ResetCancellationOperation, bool, error) {
	if s == nil || s.db == nil || blank(resetKey) || len(fingerprint) != sha256.Size*2 {
		return ResetCancellationOperation{}, false, errors.New("reset cancellation key and fingerprint are required")
	}
	operation, found, err := getResetCancellationOperation(ctx, s.db, resetKey)
	if err != nil || !found {
		return operation, found, err
	}
	if operation.RequestFingerprint != fingerprint {
		return ResetCancellationOperation{}, false, fmt.Errorf("%w: reset cancellation key %q", ErrIdempotencyConflict, resetKey)
	}
	return operation, true, nil
}

func (s *CaseStore) ClaimResetCancellation(ctx context.Context, resetKey, fingerprint, claimToken string) (ResetCancellationOperation, bool, error) {
	if blank(claimToken) {
		return ResetCancellationOperation{}, false, errors.New("reset cancellation claim token is required")
	}
	now := formatStoreTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx, `UPDATE reset_cancellation_operations SET status=?,claim_token=?,updated_at=? WHERE reset_key=? AND request_fingerprint=? AND status=?`, ResetCancellationClaimed, claimToken, now, resetKey, fingerprint, ResetCancellationPending)
	if err != nil {
		return ResetCancellationOperation{}, false, fmt.Errorf("claim reset cancellation operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ResetCancellationOperation{}, false, err
	}
	operation, found, err := s.GetResetCancellationOperation(ctx, resetKey, fingerprint)
	if err != nil {
		return ResetCancellationOperation{}, false, err
	}
	if !found {
		return ResetCancellationOperation{}, false, errors.New("reset cancellation operation is missing")
	}
	if rows == 1 {
		if operation.Status != ResetCancellationClaimed || operation.ClaimToken != claimToken {
			return ResetCancellationOperation{}, false, errors.New("reset cancellation claim was not persisted")
		}
		return operation, true, nil
	}
	return operation, false, nil
}

func (s *CaseStore) CompleteResetCancellation(ctx context.Context, resetKey, fingerprint, claimToken string, status ResetCancellationStatus) (ResetCancellationOperation, error) {
	outcomeCode := ""
	switch status {
	case ResetCancellationSucceeded:
		outcomeCode = "succeeded"
	case ResetCancellationFailed:
		outcomeCode = "runner_cancel_failed"
	default:
		return ResetCancellationOperation{}, errors.New("reset cancellation completion status is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE reset_cancellation_operations SET status=?,outcome_code=?,updated_at=? WHERE reset_key=? AND request_fingerprint=? AND status=? AND claim_token=?`, status, outcomeCode, formatStoreTime(time.Now().UTC()), resetKey, fingerprint, ResetCancellationClaimed, claimToken)
	if err != nil {
		return ResetCancellationOperation{}, fmt.Errorf("complete reset cancellation operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ResetCancellationOperation{}, err
	}
	operation, found, err := s.GetResetCancellationOperation(ctx, resetKey, fingerprint)
	if err != nil {
		return ResetCancellationOperation{}, err
	}
	if !found {
		return ResetCancellationOperation{}, errors.New("reset cancellation operation is missing")
	}
	if rows == 1 || (operation.Status == status && operation.ClaimToken == claimToken && operation.OutcomeCode == outcomeCode) {
		return operation, nil
	}
	return ResetCancellationOperation{}, fmt.Errorf("%w: reset cancellation completion %q", ErrIdempotencyConflict, resetKey)
}

func caseResetFingerprint(reset CaseReset) (string, error) {
	identity := struct {
		CaseID                 string          `json:"case_id"`
		NewCaseID              string          `json:"new_case_id"`
		ExpectedVersion        int64           `json:"expected_version"`
		SelectedBotKey         string          `json:"selected_bot_key"`
		ReplacementBotTarget   string          `json:"replacement_bot_target"`
		ReplacementEnvironment string          `json:"replacement_environment"`
		ActorID                string          `json:"actor_id"`
		IdempotencyKey         string          `json:"idempotency_key"`
		RequestJSON            json.RawMessage `json:"request_json"`
	}{reset.CaseID, reset.NewCaseID, reset.ExpectedVersion, reset.SelectedBotKey, reset.ReplacementBotTarget, reset.ReplacementEnvironment, reset.ActorID, reset.IdempotencyKey, CloneRawMessage(reset.RequestJSON)}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode Case reset fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func legacyCaseResetFingerprint(reset CaseReset) (string, error) {
	identity := struct {
		CaseID          string          `json:"case_id"`
		NewCaseID       string          `json:"new_case_id"`
		ExpectedVersion int64           `json:"expected_version"`
		SelectedBotKey  string          `json:"selected_bot_key"`
		ActorID         string          `json:"actor_id"`
		IdempotencyKey  string          `json:"idempotency_key"`
		RequestJSON     json.RawMessage `json:"request_json"`
	}{reset.CaseID, reset.NewCaseID, reset.ExpectedVersion, reset.SelectedBotKey, reset.ActorID, reset.IdempotencyKey, CloneRawMessage(reset.RequestJSON)}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode legacy Case reset fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func caseResetBindingMatches(reset CaseReset, replacement IncidentCase, environment string, requireDerivedTarget bool) bool {
	if reset.SelectedBotKey != replacement.SelectedBotKey || environment != replacement.Environment {
		return false
	}
	if systemID := strings.TrimSpace(reset.ReplacementSystemID); systemID != "" && systemID != replacement.SystemID {
		return false
	}
	persistedTarget := incidentWorkflowTargetFromBotKey(replacement.SelectedBotKey)
	if persistedTarget == "" {
		return !requireDerivedTarget
	}
	return reset.ReplacementBotTarget == persistedTarget
}

func caseResetEventPayload(reset CaseReset, result CaseResetResult) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(reset.RequestJSON))
	decoder.UseNumber()
	var request any
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("decode Case reset request for audit: %w", err)
	}
	request = redactResetURLUserinfo(redactSensitiveAny(request))
	payload, err := json.Marshal(map[string]any{
		"archived_case_id":        result.Archived.ID,
		"replacement_case_id":     result.Replacement.ID,
		"cancelled_attempt_id":    result.CancelledAttemptID,
		"selected_bot_key":        reset.SelectedBotKey,
		"replacement_bot_target":  reset.ReplacementBotTarget,
		"replacement_system_id":   reset.ReplacementSystemID,
		"replacement_environment": reset.ReplacementEnvironment,
		"request":                 request,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Case reset audit payload: %w", err)
	}
	return payload, nil
}

func redactResetURLUserinfo(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = redactResetURLUserinfo(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = redactResetURLUserinfo(child)
		}
		return typed
	case string:
		redacted := redactSensitiveText(typed)
		redacted = resetURLUserinfoPattern.ReplaceAllStringFunc(redacted, func(match string) string {
			separator := strings.Index(match, "://")
			return match[:separator+3] + redactedValue + "@"
		})
		parsed, err := url.Parse(redacted)
		if err == nil && parsed.IsAbs() && parsed.Host != "" && parsed.User != nil {
			parsed.User = url.User(redactedValue)
			return parsed.String()
		}
		return redacted
	default:
		return value
	}
}

func validateCaseResetResult(result CaseResetResult, oldCaseID, newCaseID string) error {
	if result.Archived.ID != oldCaseID || result.Archived.Status != CaseResetArchived || result.Archived.SupersededByCaseID != newCaseID || result.Archived.ClosedAt == nil {
		return errors.New("stored Case reset archive result is invalid")
	}
	if result.Replacement.ID != newCaseID || result.Replacement.Status != CasePendingValidation || result.Replacement.ResetFromCaseID != oldCaseID {
		return errors.New("stored Case reset replacement result is invalid")
	}
	if err := result.Archived.Validate(); err != nil {
		return err
	}
	return result.Replacement.Validate()
}

func cloneCaseResetResult(result CaseResetResult) CaseResetResult {
	result.Archived = result.Archived.Clone()
	result.Replacement = result.Replacement.Clone()
	return result
}

func (s *CaseStore) GetCase(ctx context.Context, id string) (IncidentCase, error) {
	return getCase(ctx, s.db, id)
}

func (s *CaseStore) ListCases(ctx context.Context) ([]IncidentCase, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, bug_id, source, system_id, environment,
		status, cycle_number, current_attempt_id, selected_bot_key, reset_from_case_id,
		superseded_by_case_id, version,
		created_at, updated_at, closed_at
		FROM incident_cases ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list incident cases: %w", err)
	}
	defer rows.Close()
	var incidents []IncidentCase
	for rows.Next() {
		incident, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		incidents = append(incidents, incident.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list incident cases: %w", err)
	}
	return incidents, nil
}

func (s *CaseStore) CreateAttempt(ctx context.Context, attempt PhaseAttempt) error {
	return s.createAttempt(ctx, attempt, AttemptValidationOptions{})
}

func (s *CaseStore) importLegacyBatch(ctx context.Context, batch legacyImportBatch) (LegacyImportResult, error) {
	if blank(batch.MigrationKey) {
		return LegacyImportResult{}, errors.New("legacy migration key is required")
	}
	for _, incident := range batch.Cases {
		if incident.Status != CaseLegacyArchived {
			return LegacyImportResult{}, errors.New("legacy import cases must be archived")
		}
		if err := incident.Validate(); err != nil {
			return LegacyImportResult{}, err
		}
	}
	for _, attempt := range batch.Attempts {
		if attempt.Phase != PhaseLegacy {
			return LegacyImportResult{}, errors.New("legacy import attempts require legacy phase")
		}
		if err := attempt.ValidateWithOptions(AttemptValidationOptions{AllowLegacyMigration: true}); err != nil {
			return LegacyImportResult{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyImportResult{}, fmt.Errorf("begin legacy import: %w", err)
	}
	defer tx.Rollback()
	var marker int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE key = ?`, batch.MigrationKey).Scan(&marker)
	if err == nil {
		return LegacyImportResult{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return LegacyImportResult{}, fmt.Errorf("check legacy migration: %w", err)
	}
	result := LegacyImportResult{}
	for _, incident := range batch.Cases {
		insert, err := tx.ExecContext(ctx, `INSERT INTO incident_cases (
			id, bug_id, source, system_id, environment, status, cycle_number,
			current_attempt_id, selected_bot_key, reset_from_case_id, superseded_by_case_id,
			version, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			incident.ID, incident.BugID, incident.Source, incident.SystemID, incident.Environment,
			incident.Status, incident.CycleNumber, incident.CurrentAttemptID, incident.SelectedBotKey,
			incident.ResetFromCaseID, incident.SupersededByCaseID,
			incident.Version, formatStoreTime(incident.CreatedAt), formatStoreTime(incident.UpdatedAt),
			formatOptionalStoreTime(incident.ClosedAt))
		if err != nil {
			return LegacyImportResult{}, fmt.Errorf("import legacy case: %w", err)
		}
		rows, err := insert.RowsAffected()
		if err != nil {
			return LegacyImportResult{}, fmt.Errorf("inspect legacy case import: %w", err)
		}
		if rows == 0 {
			stored, err := getCase(ctx, tx, incident.ID)
			if err != nil || !sameImportedCase(stored, incident) {
				return LegacyImportResult{}, fmt.Errorf("%w: legacy case %s", ErrIdempotencyConflict, incident.ID)
			}
		} else {
			result.Cases++
		}
	}
	for _, attempt := range batch.Attempts {
		insert, err := tx.ExecContext(ctx, `INSERT INTO phase_attempts (
			id, case_id, cycle_number, phase, mode, status, agent_target, bot_key,
			input_json, output_json, parent_attempt_id, started_at, finished_at,
			error_code, error_message, input_tokens, output_tokens, duration_nanos
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			attempt.ID, attempt.CaseID, attempt.CycleNumber, attempt.Phase, attempt.Mode,
			attempt.Status, attempt.AgentTarget, attempt.BotKey, string(attempt.InputJSON),
			string(attempt.OutputJSON), attempt.ParentAttemptID, formatStoreTime(attempt.StartedAt),
			formatOptionalStoreTime(attempt.FinishedAt), attempt.ErrorCode, attempt.ErrorMessage,
			attempt.Usage.InputTokens, attempt.Usage.OutputTokens, int64(attempt.Usage.Duration))
		if err != nil {
			return LegacyImportResult{}, fmt.Errorf("import legacy attempt: %w", err)
		}
		rows, err := insert.RowsAffected()
		if err != nil {
			return LegacyImportResult{}, fmt.Errorf("inspect legacy attempt import: %w", err)
		}
		if rows == 0 {
			stored, err := getAttempt(ctx, tx, attempt.ID)
			if err != nil || !sameImportedAttempt(stored, attempt) {
				return LegacyImportResult{}, fmt.Errorf("%w: legacy attempt %s", ErrIdempotencyConflict, attempt.ID)
			}
		} else {
			result.Attempts++
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`,
		batch.MigrationKey, formatStoreTime(time.Now().UTC()), `{}`); err != nil {
		return LegacyImportResult{}, fmt.Errorf("record legacy migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LegacyImportResult{}, fmt.Errorf("commit legacy import: %w", err)
	}
	return result, nil
}

func sameImportedCase(left, right IncidentCase) bool {
	return left.ID == right.ID && left.BugID == right.BugID && left.Source == right.Source &&
		left.SystemID == right.SystemID && left.Environment == right.Environment &&
		left.Status == right.Status && left.CycleNumber == right.CycleNumber &&
		left.CurrentAttemptID == "" && left.SelectedBotKey == "" && left.Version == right.Version &&
		left.ResetFromCaseID == right.ResetFromCaseID && left.SupersededByCaseID == right.SupersededByCaseID &&
		left.ClosedAt == nil && right.ClosedAt == nil
}

func sameImportedAttempt(left, right PhaseAttempt) bool {
	return left.ID == right.ID && left.CaseID == right.CaseID && left.CycleNumber == right.CycleNumber &&
		left.Phase == right.Phase && left.Mode == right.Mode && left.Status == right.Status &&
		left.AgentTarget == right.AgentTarget && left.BotKey == right.BotKey &&
		bytes.Equal(left.InputJSON, right.InputJSON) && bytes.Equal(left.OutputJSON, right.OutputJSON) &&
		left.ParentAttemptID == right.ParentAttemptID && left.StartedAt.Equal(right.StartedAt) &&
		timesEqual(left.FinishedAt, right.FinishedAt) && left.ErrorCode == right.ErrorCode &&
		left.ErrorMessage == right.ErrorMessage && left.Usage == right.Usage
}

func (s *CaseStore) GetEvidenceArtifact(ctx context.Context, attemptID, sha256Digest, kind string) (EvidenceArtifact, bool, error) {
	artifact, err := scanEvidenceArtifact(s.db.QueryRowContext(ctx, `SELECT id, case_id, attempt_id,
		kind, path_or_reference, sha256, captured_at, environment, version, request_id,
		trace_id, redaction_status FROM evidence_artifacts
		WHERE attempt_id = ? AND sha256 = ? AND kind = ?`, attemptID, sha256Digest, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return EvidenceArtifact{}, false, nil
	}
	if err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("get evidence artifact: %w", err)
	}
	return artifact, true, nil
}

func (s *CaseStore) recordEvidenceArtifact(ctx context.Context, artifact EvidenceArtifact, beforeCommit func() error) (EvidenceArtifact, bool, error) {
	if artifact.CapturedAt.IsZero() {
		return EvidenceArtifact{}, false, errors.New("evidence artifact captured_at is required")
	}
	artifact.CapturedAt = artifact.CapturedAt.UTC()
	if err := artifact.Validate(); err != nil {
		return EvidenceArtifact{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("begin evidence registration: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO evidence_artifacts (
		id, case_id, attempt_id, kind, path_or_reference, sha256, captured_at,
		environment, version, request_id, trace_id, redaction_status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(attempt_id, sha256, kind) DO NOTHING`, artifact.ID, artifact.CaseID,
		artifact.AttemptID, artifact.Kind, artifact.PathOrReference, artifact.SHA256,
		formatStoreTime(artifact.CapturedAt), artifact.Environment, artifact.Version,
		artifact.RequestID, artifact.TraceID, artifact.RedactionStatus)
	if err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("register evidence artifact: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("inspect evidence registration: %w", err)
	}
	stored, err := scanEvidenceArtifact(tx.QueryRowContext(ctx, `SELECT id, case_id, attempt_id,
		kind, path_or_reference, sha256, captured_at, environment, version, request_id,
		trace_id, redaction_status FROM evidence_artifacts
		WHERE attempt_id = ? AND sha256 = ? AND kind = ?`, artifact.AttemptID, artifact.SHA256, artifact.Kind))
	if err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("read registered evidence artifact: %w", err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return EvidenceArtifact{}, false, fmt.Errorf("verify evidence publication before commit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return EvidenceArtifact{}, false, fmt.Errorf("commit evidence registration: %w", err)
	}
	return stored, rows == 1, nil
}

func (s *CaseStore) ListEvidenceArtifacts(ctx context.Context, caseID string) ([]EvidenceArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, case_id, attempt_id, kind, path_or_reference,
		sha256, captured_at, environment, version, request_id, trace_id, redaction_status
		FROM evidence_artifacts WHERE case_id = ? ORDER BY captured_at ASC, id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("list evidence artifacts: %w", err)
	}
	defer rows.Close()
	var artifacts []EvidenceArtifact
	for rows.Next() {
		artifact, err := scanEvidenceArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list evidence artifacts: %w", err)
	}
	return artifacts, nil
}

func scanEvidenceArtifact(row rowScanner) (EvidenceArtifact, error) {
	var artifact EvidenceArtifact
	var capturedAt string
	if err := row.Scan(&artifact.ID, &artifact.CaseID, &artifact.AttemptID, &artifact.Kind,
		&artifact.PathOrReference, &artifact.SHA256, &capturedAt, &artifact.Environment,
		&artifact.Version, &artifact.RequestID, &artifact.TraceID, &artifact.RedactionStatus); err != nil {
		return EvidenceArtifact{}, err
	}
	var err error
	artifact.CapturedAt, err = parseStoreTime(capturedAt)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	if err := artifact.Validate(); err != nil {
		return EvidenceArtifact{}, fmt.Errorf("validate stored evidence artifact: %w", err)
	}
	return artifact, nil
}

type AttemptFilter struct {
	CaseID   string
	Statuses []AttemptStatus
}

func (s *CaseStore) GetAttempt(ctx context.Context, id string) (PhaseAttempt, error) {
	return getAttempt(ctx, s.db, id)
}

func (s *CaseStore) ListAttempts(ctx context.Context, filter AttemptFilter) ([]PhaseAttempt, error) {
	allowed := make(map[AttemptStatus]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if !status.valid() {
			return nil, fmt.Errorf("unsupported attempt filter status %q", status)
		}
		allowed[status] = struct{}{}
	}
	query := `SELECT id, case_id, cycle_number, phase, mode, status,
		agent_target, bot_key, input_json, output_json, parent_attempt_id, started_at,
		finished_at, error_code, error_message, input_tokens, output_tokens, duration_nanos
		FROM phase_attempts`
	var args []any
	if filter.CaseID != "" {
		query += ` WHERE case_id = ?`
		args = append(args, filter.CaseID)
	}
	query += ` ORDER BY started_at ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list phase attempts: %w", err)
	}
	defer rows.Close()
	var attempts []PhaseAttempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan phase attempt: %w", err)
		}
		if len(allowed) != 0 {
			if _, ok := allowed[attempt.Status]; !ok {
				continue
			}
		}
		attempts = append(attempts, attempt.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list phase attempts: %w", err)
	}
	return attempts, nil
}

func (s *CaseStore) ListApprovals(ctx context.Context, caseID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, case_id, kind, actor, approved_at,
		case_version, scope_json, fix_commits_json, target_branches_json
		FROM approvals WHERE case_id = ? ORDER BY approved_at ASC, id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()
	var approvals []Approval
	for rows.Next() {
		var approval Approval
		var approvedAt, scope, fixes, targets string
		if err := rows.Scan(&approval.ID, &approval.CaseID, &approval.Kind, &approval.Actor,
			&approvedAt, &approval.CaseVersion, &scope, &fixes, &targets); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		approval.ApprovedAt, err = parseStoreTime(approvedAt)
		if err != nil {
			return nil, err
		}
		approval.ScopeJSON = CloneRawMessage([]byte(scope))
		if err := json.Unmarshal([]byte(fixes), &approval.FixCommits); err != nil {
			return nil, fmt.Errorf("decode approval fix commits: %w", err)
		}
		if err := json.Unmarshal([]byte(targets), &approval.TargetBranches); err != nil {
			return nil, fmt.Errorf("decode approval target branches: %w", err)
		}
		if err := approval.Validate(); err != nil {
			return nil, fmt.Errorf("validate stored approval: %w", err)
		}
		approvals = append(approvals, approval.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	return approvals, nil
}

func (s *CaseStore) ListCodeChanges(ctx context.Context, caseID string) ([]CodeChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, case_id, attempt_id, repo, base_branch,
		fix_branch, fix_commit, test_evidence_json, target_environment_branch,
		merge_base_head, merge_commit, push_remote, push_status
		FROM code_changes WHERE case_id = ? ORDER BY id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("list code changes: %w", err)
	}
	defer rows.Close()
	var changes []CodeChange
	for rows.Next() {
		var change CodeChange
		var evidence string
		if err := rows.Scan(&change.ID, &change.CaseID, &change.AttemptID, &change.Repo,
			&change.BaseBranch, &change.FixBranch, &change.FixCommit, &evidence,
			&change.TargetEnvironmentBranch, &change.MergeBaseHead, &change.MergeCommit,
			&change.PushRemote, &change.PushStatus); err != nil {
			return nil, fmt.Errorf("scan code change: %w", err)
		}
		change.TestEvidence = CloneRawMessage([]byte(evidence))
		if err := change.Validate(); err != nil {
			return nil, fmt.Errorf("validate stored code change: %w", err)
		}
		changes = append(changes, change.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list code changes: %w", err)
	}
	return changes, nil
}

func (s *CaseStore) ListDeploymentObservations(ctx context.Context, caseID string) ([]DeploymentObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, case_id, environment, expected_commits_json,
		user_notified_at, verification_source, observed_version, observed_images_json,
		observed_commits_json, verified_commit_ancestors_json, observed_at, diagnostic_code, diagnostic_message, verified_at, result
		FROM deployment_observations WHERE case_id = ? ORDER BY COALESCE(verified_at, user_notified_at, '') ASC, id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("list deployment observations: %w", err)
	}
	defer rows.Close()
	var observations []DeploymentObservation
	for rows.Next() {
		var observation DeploymentObservation
		var expected, images, commits, ancestors string
		var notifiedAt, verifiedAt sql.NullString
		var observedAt string
		if err := rows.Scan(&observation.ID, &observation.CaseID, &observation.Environment,
			&expected, &notifiedAt, &observation.VerificationSource, &observation.ObservedVersion,
			&images, &commits, &ancestors, &observedAt, &observation.DiagnosticCode, &observation.DiagnosticMessage, &verifiedAt, &observation.Result); err != nil {
			return nil, fmt.Errorf("scan deployment observation: %w", err)
		}
		if err := json.Unmarshal([]byte(expected), &observation.ExpectedCommits); err != nil {
			return nil, fmt.Errorf("decode deployment expected commits: %w", err)
		}
		if err := json.Unmarshal([]byte(images), &observation.ObservedImages); err != nil {
			return nil, fmt.Errorf("decode deployment observed images: %w", err)
		}
		if err := json.Unmarshal([]byte(commits), &observation.ObservedCommits); err != nil {
			return nil, fmt.Errorf("decode deployment observed commits: %w", err)
		}
		if err := json.Unmarshal([]byte(ancestors), &observation.VerifiedCommitAncestors); err != nil {
			return nil, fmt.Errorf("decode deployment verified commit ancestors: %w", err)
		}
		observation.UserNotifiedAt, err = parseOptionalStoreTime(notifiedAt)
		if err != nil {
			return nil, err
		}
		observation.VerifiedAt, err = parseOptionalStoreTime(verifiedAt)
		if err != nil {
			return nil, err
		}
		observation.ObservedAt, err = parseStoreTime(observedAt)
		if err != nil {
			return nil, fmt.Errorf("parse deployment observed_at: %w", err)
		}
		if err := observation.Validate(); err != nil {
			return nil, fmt.Errorf("validate stored deployment observation: %w", err)
		}
		observations = append(observations, observation.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployment observations: %w", err)
	}
	return observations, nil
}

func (s *CaseStore) createAttempt(ctx context.Context, attempt PhaseAttempt, options AttemptValidationOptions) error {
	attempt = attempt.Clone()
	if attempt.FinishedAt != nil && attempt.FinishedAt.IsZero() {
		return errors.New("phase attempt finished_at must not be zero when provided")
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}
	if err := attempt.ValidateWithOptions(options); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO phase_attempts (
		id, case_id, cycle_number, phase, mode, status, agent_target, bot_key,
		input_json, output_json, parent_attempt_id, started_at, finished_at,
		error_code, error_message, input_tokens, output_tokens, duration_nanos
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.CaseID, attempt.CycleNumber, attempt.Phase, attempt.Mode,
		attempt.Status, attempt.AgentTarget, attempt.BotKey, string(attempt.InputJSON),
		string(attempt.OutputJSON), attempt.ParentAttemptID, formatStoreTime(attempt.StartedAt),
		formatOptionalStoreTime(attempt.FinishedAt), attempt.ErrorCode, attempt.ErrorMessage,
		attempt.Usage.InputTokens, attempt.Usage.OutputTokens, int64(attempt.Usage.Duration),
	)
	if err != nil {
		return fmt.Errorf("create phase attempt: %w", err)
	}
	return nil
}

func (s *CaseStore) FinishAttempt(ctx context.Context, attempt PhaseAttempt) error {
	attempt = attempt.Clone()
	if attempt.FinishedAt != nil && attempt.FinishedAt.IsZero() {
		return errors.New("phase attempt finished_at must not be zero when provided")
	}
	if err := attempt.Validate(); err != nil {
		return err
	}
	switch attempt.Status {
	case AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusCancelled, AttemptStatusInterrupted:
	default:
		return fmt.Errorf("finish phase attempt requires terminal status, got %q", attempt.Status)
	}
	finishedAtProvided := attempt.FinishedAt != nil && !attempt.FinishedAt.IsZero()
	if !finishedAtProvided {
		finishedAt := time.Now().UTC()
		attempt.FinishedAt = &finishedAt
	}
	result, err := s.db.ExecContext(ctx, `UPDATE phase_attempts SET
		status = ?, output_json = ?, finished_at = ?, error_code = ?, error_message = ?,
		input_tokens = ?, output_tokens = ?, duration_nanos = ?, run_claim_token = ''
		WHERE id = ? AND case_id = ? AND status IN (?, ?)`, attempt.Status, string(attempt.OutputJSON),
		formatOptionalStoreTime(attempt.FinishedAt), attempt.ErrorCode, attempt.ErrorMessage,
		attempt.Usage.InputTokens, attempt.Usage.OutputTokens, int64(attempt.Usage.Duration),
		attempt.ID, attempt.CaseID, AttemptStatusQueued, AttemptStatusRunning)
	if err != nil {
		return fmt.Errorf("finish phase attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect finished phase attempt: %w", err)
	}
	if rows != 1 {
		stored, getErr := getAttempt(ctx, s.db, attempt.ID)
		if getErr != nil {
			return getErr
		}
		if sameAttemptFinish(stored, attempt, finishedAtProvided) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrAttemptAlreadyFinished, attempt.ID)
	}
	return nil
}

func (s *CaseStore) RecordApproval(ctx context.Context, approval Approval, idempotencyKey string) error {
	approval = approval.Clone()
	approvedAtProvided := !approval.ApprovedAt.IsZero()
	if approval.ApprovedAt.IsZero() {
		approval.ApprovedAt = time.Now().UTC()
	}
	if err := approval.Validate(); err != nil {
		return err
	}
	if blank(idempotencyKey) {
		return errors.New("approval idempotency key is required")
	}
	fixCommits, err := marshalStringMap(approval.FixCommits)
	if err != nil {
		return err
	}
	targetBranches, err := marshalStringMap(approval.TargetBranches)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO approvals (
		id, case_id, kind, actor, approved_at, case_version, scope_json,
		fix_commits_json, target_branches_json, idempotency_key
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(idempotency_key) DO NOTHING`, approval.ID, approval.CaseID, approval.Kind,
		approval.Actor, formatStoreTime(approval.ApprovedAt), approval.CaseVersion,
		string(approval.ScopeJSON), fixCommits, targetBranches, idempotencyKey)
	if err != nil {
		return fmt.Errorf("record approval: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect recorded approval: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var id, caseID, kind, actor, approvedAt, scope, storedFixes, storedTargets string
	var caseVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT id, case_id, kind, actor, approved_at, case_version,
		scope_json, fix_commits_json, target_branches_json FROM approvals WHERE idempotency_key = ?`,
		idempotencyKey).Scan(&id, &caseID, &kind, &actor, &approvedAt, &caseVersion, &scope, &storedFixes, &storedTargets); err != nil {
		return fmt.Errorf("load replayed approval: %w", err)
	}
	if id == approval.ID && caseID == approval.CaseID && kind == string(approval.Kind) &&
		actor == approval.Actor && caseVersion == approval.CaseVersion &&
		scope == string(approval.ScopeJSON) && storedFixes == fixCommits && storedTargets == targetBranches &&
		(!approvedAtProvided || approvedAt == formatStoreTime(approval.ApprovedAt)) {
		return nil
	}
	return fmt.Errorf("%w: approval key %q", ErrIdempotencyConflict, idempotencyKey)
}

func (s *CaseStore) RecordCodeChange(ctx context.Context, change CodeChange) error {
	change = change.Clone()
	if err := change.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO code_changes (
		id, case_id, attempt_id, repo, base_branch, fix_branch, fix_commit,
		test_evidence_json, target_environment_branch, merge_base_head, merge_commit,
		push_remote, push_status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, change.ID, change.CaseID,
		change.AttemptID, change.Repo, change.BaseBranch, change.FixBranch, change.FixCommit,
		string(change.TestEvidence), change.TargetEnvironmentBranch, change.MergeBaseHead,
		change.MergeCommit, change.PushRemote, change.PushStatus)
	if err != nil {
		return fmt.Errorf("record code change: %w", err)
	}
	return nil
}

func (s *CaseStore) RecordDeploymentObservation(ctx context.Context, observation DeploymentObservation, idempotencyKey string) error {
	observation = observation.Clone()
	if observation.ObservedAt.IsZero() {
		switch {
		case observation.VerifiedAt != nil:
			observation.ObservedAt = *observation.VerifiedAt
		case observation.UserNotifiedAt != nil:
			observation.ObservedAt = *observation.UserNotifiedAt
		default:
			observation.ObservedAt = time.Now().UTC()
		}
	}
	if observation.UserNotifiedAt != nil && observation.UserNotifiedAt.IsZero() {
		return errors.New("deployment observation user_notified_at must not be zero when provided")
	}
	if observation.VerifiedAt != nil && observation.VerifiedAt.IsZero() {
		return errors.New("deployment observation verified_at must not be zero when provided")
	}
	userNotifiedAtProvided := observation.UserNotifiedAt != nil && !observation.UserNotifiedAt.IsZero()
	verifiedAtProvided := observation.VerifiedAt != nil && !observation.VerifiedAt.IsZero()
	if err := observation.Validate(); err != nil {
		return err
	}
	if blank(idempotencyKey) {
		return errors.New("deployment observation idempotency key is required")
	}
	expectedCommits, err := marshalStringMap(observation.ExpectedCommits)
	if err != nil {
		return err
	}
	observedImages, err := marshalStringMap(observation.ObservedImages)
	if err != nil {
		return err
	}
	observedCommits, err := marshalStringMap(observation.ObservedCommits)
	if err != nil {
		return err
	}
	verifiedAncestors, err := marshalStringMap(observation.VerifiedCommitAncestors)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO deployment_observations (
		id, case_id, environment, expected_commits_json, user_notified_at,
		verification_source, observed_version, observed_images_json,
		observed_commits_json, verified_commit_ancestors_json, observed_at, diagnostic_code, diagnostic_message, verified_at, result, idempotency_key
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(idempotency_key) DO NOTHING`, observation.ID, observation.CaseID,
		observation.Environment, expectedCommits, formatOptionalStoreTime(observation.UserNotifiedAt),
		observation.VerificationSource, observation.ObservedVersion, observedImages, observedCommits, verifiedAncestors,
		formatStoreTime(observation.ObservedAt), observation.DiagnosticCode, observation.DiagnosticMessage, formatOptionalStoreTime(observation.VerifiedAt), observation.Result, idempotencyKey)
	if err != nil {
		return fmt.Errorf("record deployment observation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect deployment observation: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var id, caseID, environment, storedExpected, source, version, storedImages, storedCommits, storedAncestors, storedObservedAt, storedDiagnosticCode, storedDiagnosticMessage, resultValue string
	var storedUserNotifiedAt, storedVerifiedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT id, case_id, environment, expected_commits_json,
		user_notified_at, verification_source, observed_version, observed_images_json,
		observed_commits_json, verified_commit_ancestors_json, observed_at, diagnostic_code, diagnostic_message, verified_at, result
		FROM deployment_observations WHERE idempotency_key = ?`, idempotencyKey).Scan(
		&id, &caseID, &environment, &storedExpected, &storedUserNotifiedAt, &source, &version,
		&storedImages, &storedCommits, &storedAncestors, &storedObservedAt, &storedDiagnosticCode, &storedDiagnosticMessage, &storedVerifiedAt, &resultValue,
	); err != nil {
		return fmt.Errorf("load replayed deployment observation: %w", err)
	}
	if id == observation.ID && caseID == observation.CaseID && environment == observation.Environment &&
		storedExpected == expectedCommits && source == observation.VerificationSource &&
		version == observation.ObservedVersion && storedImages == observedImages &&
		storedCommits == observedCommits && storedAncestors == verifiedAncestors && storedObservedAt == formatStoreTime(observation.ObservedAt) && storedDiagnosticCode == observation.DiagnosticCode && storedDiagnosticMessage == observation.DiagnosticMessage && resultValue == string(observation.Result) &&
		optionalTimeMatches(storedUserNotifiedAt, observation.UserNotifiedAt, userNotifiedAtProvided) &&
		optionalTimeMatches(storedVerifiedAt, observation.VerifiedAt, verifiedAtProvided) {
		return nil
	}
	return fmt.Errorf("%w: deployment observation key %q", ErrIdempotencyConflict, idempotencyKey)
}

type CaseSnapshotUpdate struct {
	CurrentAttemptID *string
	SelectedBotKey   *string
	CycleNumber      *int
	ClosedAtSet      bool
	ClosedAt         *time.Time
}

type FixCheckpoint struct {
	AttemptID      string
	CaseID         string
	StagingLocator string
	CreatedAt      time.Time
}

type AttemptRunClaim struct {
	Attempt    PhaseAttempt
	ClaimToken string
	Checkpoint *FixCheckpoint
}

func (s *CaseStore) ClaimRunnableAttempt(ctx context.Context, claim AttemptRunClaim) (err error) {
	if s == nil || s.db == nil || strings.TrimSpace(claim.ClaimToken) == "" {
		return errors.New("attempt run claim requires store and token")
	}
	if err := claim.Attempt.Validate(); err != nil {
		return err
	}
	if claim.Checkpoint != nil {
		checkpoint := claim.Checkpoint
		if claim.Attempt.Phase != PhaseFix || checkpoint.AttemptID != claim.Attempt.ID || checkpoint.CaseID != claim.Attempt.CaseID {
			return errors.New("fix checkpoint must match the claimed fix attempt")
		}
		if err := validateArtifactComponent("fix checkpoint locator", checkpoint.StagingLocator); err != nil || !strings.HasPrefix(checkpoint.StagingLocator, checkpoint.AttemptID+"-") {
			return errors.New("fix checkpoint locator is not bound to its attempt")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	incident, err := getCase(ctx, tx, claim.Attempt.CaseID)
	if err != nil {
		return err
	}
	persisted, err := getAttempt(ctx, tx, claim.Attempt.ID)
	if err != nil {
		return err
	}
	if incident.CurrentAttemptID != persisted.ID || incident.Status != statusForPhase(persisted.Phase) || incident.CycleNumber != persisted.CycleNumber || incident.SelectedBotKey != persisted.BotKey || !sameRunnableAttempt(persisted, claim.Attempt) || (persisted.Status != AttemptStatusQueued && persisted.Status != AttemptStatusRunning) {
		return ErrAttemptAlreadyFinished
	}
	if _, found, parseErr := parseCompletionIntent(persisted.OutputJSON); parseErr != nil {
		return parseErr
	} else if found {
		return ErrCompletionIntentPending
	}
	result, err := tx.ExecContext(ctx, `UPDATE phase_attempts SET status=?,run_claim_token=? WHERE id=? AND case_id=? AND status IN (?,?) AND run_claim_token=''`, AttemptStatusRunning, claim.ClaimToken, persisted.ID, persisted.CaseID, AttemptStatusQueued, AttemptStatusRunning)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		var existing string
		if queryErr := tx.QueryRowContext(ctx, `SELECT run_claim_token FROM phase_attempts WHERE id=? AND case_id=?`, persisted.ID, persisted.CaseID).Scan(&existing); queryErr != nil {
			return queryErr
		}
		if existing != claim.ClaimToken {
			return ErrAttemptRunClaimConflict
		}
	}
	if claim.Checkpoint != nil {
		checkpoint := *claim.Checkpoint
		if checkpoint.CreatedAt.IsZero() {
			checkpoint.CreatedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fix_checkpoints (attempt_id,case_id,staging_locator,created_at) VALUES (?,?,?,?)`, checkpoint.AttemptID, checkpoint.CaseID, checkpoint.StagingLocator, formatStoreTime(checkpoint.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *CaseStore) ValidateAttemptRunClaim(ctx context.Context, attempt PhaseAttempt, claimToken string) (bool, error) {
	var status AttemptStatus
	var storedToken, currentAttempt, selectedBot string
	var caseStatus CaseStatus
	var cycle int
	err := s.db.QueryRowContext(ctx, `SELECT p.status,p.run_claim_token,c.current_attempt_id,c.status,c.cycle_number,c.selected_bot_key FROM phase_attempts p JOIN incident_cases c ON c.id=p.case_id WHERE p.id=? AND p.case_id=?`, attempt.ID, attempt.CaseID).Scan(&status, &storedToken, &currentAttempt, &caseStatus, &cycle, &selectedBot)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == AttemptStatusRunning && storedToken == claimToken && currentAttempt == attempt.ID && caseStatus == statusForPhase(attempt.Phase) && cycle == attempt.CycleNumber && selectedBot == attempt.BotKey, nil
}

func (s *CaseStore) ReleaseAttemptRunClaim(ctx context.Context, attemptID, caseID, claimToken string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE phase_attempts SET run_claim_token='' WHERE id=? AND case_id=? AND status IN (?,?) AND run_claim_token=?`, attemptID, caseID, AttemptStatusQueued, AttemptStatusRunning, claimToken)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM fix_checkpoints WHERE attempt_id=? AND case_id=?`, attemptID, caseID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *CaseStore) SaveFixCheckpoint(ctx context.Context, checkpoint FixCheckpoint) error {
	if s == nil || s.db == nil || strings.TrimSpace(checkpoint.AttemptID) == "" || strings.TrimSpace(checkpoint.CaseID) == "" {
		return errors.New("fix checkpoint store, attempt, and Case are required")
	}
	if err := validateArtifactComponent("fix checkpoint locator", checkpoint.StagingLocator); err != nil || !strings.HasPrefix(checkpoint.StagingLocator, checkpoint.AttemptID+"-") {
		return errors.New("fix checkpoint locator is not bound to its attempt")
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	var caseID string
	var phase Phase
	var status AttemptStatus
	if err := s.db.QueryRowContext(ctx, `SELECT case_id,phase,status FROM phase_attempts WHERE id=?`, checkpoint.AttemptID).Scan(&caseID, &phase, &status); err != nil {
		return err
	}
	if caseID != checkpoint.CaseID || phase != PhaseFix || (status != AttemptStatusQueued && status != AttemptStatusRunning) {
		return errors.New("fix checkpoint requires the current runnable fix attempt")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO fix_checkpoints (attempt_id,case_id,staging_locator,created_at) VALUES (?,?,?,?)
		ON CONFLICT(attempt_id) DO UPDATE SET staging_locator=excluded.staging_locator
		WHERE fix_checkpoints.case_id=excluded.case_id AND fix_checkpoints.staging_locator=excluded.staging_locator`, checkpoint.AttemptID, checkpoint.CaseID, checkpoint.StagingLocator, formatStoreTime(checkpoint.CreatedAt))
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ErrIdempotencyConflict
	}
	return nil
}

func (s *CaseStore) GetFixCheckpoint(ctx context.Context, attemptID string) (FixCheckpoint, bool, error) {
	var checkpoint FixCheckpoint
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT attempt_id,case_id,staging_locator,created_at FROM fix_checkpoints WHERE attempt_id=?`, attemptID).Scan(&checkpoint.AttemptID, &checkpoint.CaseID, &checkpoint.StagingLocator, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return FixCheckpoint{}, false, nil
	}
	if err != nil {
		return FixCheckpoint{}, false, err
	}
	checkpoint.CreatedAt, err = parseStoreTime(created)
	return checkpoint, true, err
}

func (s *CaseStore) DeleteFixCheckpoint(ctx context.Context, attemptID, caseID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM fix_checkpoints WHERE attempt_id=? AND case_id=?`, attemptID, caseID)
	return err
}

// CaseMutation is the single typed write boundary used by the orchestrator.
// All records, attempt changes, ordered transition/audit events, and the final
// Case snapshot commit or roll back together under one Case-version CAS.
type CaseMutation struct {
	CaseID                       string
	ExpectedVersion              int64
	IdempotencyKey               string
	RequestJSON                  json.RawMessage
	Steps                        []CaseMutationStep
	CreateAttempts               []PhaseAttempt
	FinishAttempts               []PhaseAttempt
	Approvals                    []Approval
	CodeChanges                  []CodeChange
	Observations                 []DeploymentObservation
	ExpectedAttemptOutputs       map[string]json.RawMessage `json:"-"`
	CompletionAttemptID          string
	CompletionIdentitySHA256     string
	DeleteFixCheckpointAttemptID string
	Snapshot                     CaseSnapshotUpdate
}

type CaseMutationStep struct {
	To        CaseStatus
	AuditOnly bool
	Event     TransitionEvent
}

type CaseMutationResult struct {
	Case   IncidentCase
	Replay bool
}

type MergeApprovalScope struct {
	CycleNumber  int                  `json:"cycle_number"`
	FixAttemptID string               `json:"fix_attempt_id"`
	CodeChanges  []ApprovedCodeChange `json:"code_changes"`
}
type ApprovedCodeChange struct {
	ID           string `json:"id"`
	Repo         string `json:"repo"`
	FixCommit    string `json:"fix_commit"`
	TargetBranch string `json:"target_branch"`
	TargetHead   string `json:"target_head,omitempty"`
	ApprovalKey  string `json:"approval_key,omitempty"`
}

func (m CaseMutation) clone() CaseMutation {
	cloned := m
	cloned.RequestJSON = CloneRawMessage(m.RequestJSON)
	cloned.Steps = append([]CaseMutationStep(nil), m.Steps...)
	for index := range cloned.Steps {
		cloned.Steps[index].Event = cloned.Steps[index].Event.Clone()
	}
	cloned.CreateAttempts = make([]PhaseAttempt, len(m.CreateAttempts))
	for i := range m.CreateAttempts {
		cloned.CreateAttempts[i] = m.CreateAttempts[i].Clone()
	}
	cloned.FinishAttempts = make([]PhaseAttempt, len(m.FinishAttempts))
	for i := range m.FinishAttempts {
		cloned.FinishAttempts[i] = m.FinishAttempts[i].Clone()
	}
	cloned.Approvals = make([]Approval, len(m.Approvals))
	for i := range m.Approvals {
		cloned.Approvals[i] = m.Approvals[i].Clone()
	}
	cloned.CodeChanges = make([]CodeChange, len(m.CodeChanges))
	for i := range m.CodeChanges {
		cloned.CodeChanges[i] = m.CodeChanges[i].Clone()
	}
	cloned.Observations = make([]DeploymentObservation, len(m.Observations))
	for i := range m.Observations {
		cloned.Observations[i] = m.Observations[i].Clone()
	}
	if m.ExpectedAttemptOutputs != nil {
		cloned.ExpectedAttemptOutputs = make(map[string]json.RawMessage, len(m.ExpectedAttemptOutputs))
		for id, output := range m.ExpectedAttemptOutputs {
			cloned.ExpectedAttemptOutputs[id] = CloneRawMessage(output)
		}
	}
	cloned.Snapshot.CurrentAttemptID = cloneStringPtr(m.Snapshot.CurrentAttemptID)
	cloned.Snapshot.SelectedBotKey = cloneStringPtr(m.Snapshot.SelectedBotKey)
	cloned.Snapshot.CycleNumber = cloneIntPtr(m.Snapshot.CycleNumber)
	cloned.Snapshot.ClosedAt = cloneTimePtr(m.Snapshot.ClosedAt)
	return cloned
}

func (s *CaseStore) ApplyCaseMutation(ctx context.Context, mutation CaseMutation) (CaseMutationResult, error) {
	return s.applyCaseMutation(ctx, mutation, nil)
}

func (s *CaseStore) ApplyBrowserRecoveryCaseMutation(ctx context.Context, mutation CaseMutation, request BrowserRecoveryOperationRequest, claimToken string) (CaseMutationResult, error) {
	consume, err := newBrowserRecoveryMutationConsume(mutation, request, claimToken)
	if err != nil {
		return CaseMutationResult{}, err
	}
	return s.applyCaseMutation(ctx, mutation, consume)
}

func (s *CaseStore) applyCaseMutation(ctx context.Context, mutation CaseMutation, consume *browserRecoveryMutationConsume) (result CaseMutationResult, err error) {
	mutation = mutation.clone()
	if blank(mutation.CaseID) || mutation.ExpectedVersion < 1 || blank(mutation.IdempotencyKey) {
		return result, errors.New("compound Case mutation requires case ID, positive expected version, and idempotency key")
	}
	if len(mutation.RequestJSON) == 0 || !json.Valid(mutation.RequestJSON) {
		return result, errors.New("compound Case mutation request must be valid JSON")
	}
	if (mutation.CompletionAttemptID == "") != (mutation.CompletionIdentitySHA256 == "") {
		return result, errors.New("completion attempt and identity digest must be provided together")
	}
	if mutation.CompletionIdentitySHA256 != "" {
		decoded, decodeErr := hex.DecodeString(mutation.CompletionIdentitySHA256)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return result, errors.New("completion identity digest must be SHA-256")
		}
		found := false
		for _, attempt := range mutation.FinishAttempts {
			if attempt.ID == mutation.CompletionAttemptID {
				found = true
				break
			}
		}
		if !found {
			return result, errors.New("completion identity must belong to a finished attempt")
		}
	}
	fingerprint, err := caseMutationFingerprint(mutation)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin compound Case mutation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var existingFingerprint, resultJSON string
	queryErr := tx.QueryRowContext(ctx, `SELECT request_fingerprint, result_case_json FROM transition_events WHERE idempotency_key = ?`, mutation.IdempotencyKey).Scan(&existingFingerprint, &resultJSON)
	if queryErr == nil {
		if existingFingerprint != fingerprint {
			return result, ErrIdempotencyConflict
		}
		if err = json.Unmarshal([]byte(resultJSON), &result.Case); err != nil {
			return result, fmt.Errorf("decode compound replay: %w", err)
		}
		if consume != nil {
			operation, consumeErr := loadBrowserRecoveryMutationConsume(ctx, tx, consume)
			if consumeErr != nil {
				return result, consumeErr
			}
			blocked, blockedErr := getAttempt(ctx, tx, consume.Request.AttemptID)
			if blockedErr != nil || validateBrowserRecoveryContinuationMutation(mutation, consume.Request, blocked) != nil {
				return result, ErrIdempotencyConflict
			}
			switch operation.Status {
			case BrowserRecoveryEffectSucceeded:
				if consumeErr = consumeBrowserRecoveryOperationTx(ctx, tx, consume, result.Case); consumeErr != nil {
					return result, consumeErr
				}
			case BrowserRecoveryContinued:
				if operation.ResultCase.ID != result.Case.ID || operation.ResultCase.Version != result.Case.Version || operation.ResultCase.CurrentAttemptID != result.Case.CurrentAttemptID {
					return result, ErrIdempotencyConflict
				}
			case BrowserRecoveryClaimed, BrowserRecoveryOutcomeUncertain:
				return result, ErrBrowserRecoveryOutcomeUncertain
			default:
				return result, ErrIdempotencyConflict
			}
		}
		result.Replay = true
		if err = tx.Commit(); err != nil {
			return result, err
		}
		return cloneCaseMutationResult(result), nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return result, queryErr
	}
	var recoveryOperation BrowserRecoveryOperation
	if consume == nil {
		if err = rejectUnresolvedBrowserRecovery(ctx, tx, mutation.CaseID); err != nil {
			return result, err
		}
	} else {
		recoveryOperation, err = loadBrowserRecoveryMutationConsume(ctx, tx, consume)
		if err != nil {
			return result, err
		}
		switch recoveryOperation.Status {
		case BrowserRecoveryEffectSucceeded:
		case BrowserRecoveryClaimed, BrowserRecoveryOutcomeUncertain:
			return result, ErrBrowserRecoveryOutcomeUncertain
		default:
			return result, ErrIdempotencyConflict
		}
	}
	incident, err := getCase(ctx, tx, mutation.CaseID)
	if err != nil {
		return result, err
	}
	if incident.Version != mutation.ExpectedVersion {
		return result, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, mutation.ExpectedVersion, incident.Version)
	}
	if consume != nil {
		blocked, blockedErr := getAttempt(ctx, tx, consume.Request.AttemptID)
		if blockedErr != nil || incident.Status != CaseWaitingEvidence || incident.CurrentAttemptID != consume.Request.AttemptID || incident.CycleNumber != consume.Request.CycleNumber || validateBrowserRecoveryContinuationMutation(mutation, consume.Request, blocked) != nil {
			return result, ErrIdempotencyConflict
		}
	}
	finishedIDs := map[string]struct{}{}
	for _, attempt := range mutation.FinishAttempts {
		finishedIDs[attempt.ID] = struct{}{}
	}
	for _, approval := range mutation.Approvals {
		if approval.CaseID != incident.ID {
			return result, errors.New("approval belongs to a different Case")
		}
		switch approval.Kind {
		case ApprovalStartFix:
			var scope struct {
				RootCauseAttemptID string `json:"root_cause_attempt_id"`
			}
			if json.Unmarshal(approval.ScopeJSON, &scope) != nil || scope.RootCauseAttemptID != incident.CurrentAttemptID {
				return result, errors.New("fix approval is not bound to current attempt")
			}
		case ApprovalCompleteRemediation:
			var scope RemediationApprovalScope
			if json.Unmarshal(approval.ScopeJSON, &scope) != nil || scope.RootCauseAttemptID != incident.CurrentAttemptID || scope.CycleNumber != incident.CycleNumber || scope.BindingID == "" {
				return result, errors.New("remediation approval is not bound to current cycle and root cause")
			}
		case ApprovalMergeEnvironmentBranch:
			var scope MergeApprovalScope
			if json.Unmarshal(approval.ScopeJSON, &scope) != nil || scope.CycleNumber != incident.CycleNumber || scope.FixAttemptID != incident.CurrentAttemptID || len(scope.CodeChanges) == 0 {
				return result, errors.New("merge approval is not bound to current cycle and fix attempt")
			}
			for _, approved := range scope.CodeChanges {
				var caseID, attemptID, repo, fixCommit, target string
				if queryErr := tx.QueryRowContext(ctx, `SELECT case_id,attempt_id,repo,fix_commit,target_environment_branch FROM code_changes WHERE id=?`, approved.ID).Scan(&caseID, &attemptID, &repo, &fixCommit, &target); queryErr != nil || caseID != incident.ID || attemptID != scope.FixAttemptID || repo != approved.Repo || fixCommit != approved.FixCommit || target != approved.TargetBranch {
					return result, errors.New("merge approval references an unrelated code change")
				}
				if approved.TargetHead == "" || approved.ApprovalKey != MergeApprovalKey(incident.ID, approved.Repo, approved.FixCommit, approved.TargetBranch, approved.TargetHead) {
					return result, errors.New("merge approval key does not match its exact repository scope")
				}
			}
		}
	}
	for _, observation := range mutation.Observations {
		if observation.CaseID != incident.ID {
			return result, errors.New("deployment observation belongs to a different Case")
		}
	}
	for _, change := range mutation.CodeChanges {
		if change.CaseID != incident.ID {
			return result, errors.New("code change belongs to a different Case")
		}
		if change.AttemptID != incident.CurrentAttemptID {
			if _, ok := finishedIDs[change.AttemptID]; !ok {
				return result, errors.New("code change is not bound to current or winning finished attempt")
			}
		}
		storedAttempt, attemptErr := getAttempt(ctx, tx, change.AttemptID)
		if attemptErr != nil || storedAttempt.CaseID != incident.ID {
			return result, errors.New("code change attempt belongs to a different Case")
		}
	}
	if len(mutation.Steps) == 0 {
		mutation.Steps = []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: "mutation-" + mutation.IdempotencyKey, EventType: "compound_mutation", ActorType: "studio", ActorID: "case-store", PayloadJSON: []byte(`{}`)}}}
	}
	status := incident.Status
	for index := range mutation.Steps {
		step := &mutation.Steps[index]
		step.Event.CaseID = incident.ID
		step.Event.FromStatus = status
		if step.Event.IdempotencyKey == "" {
			if index == 0 {
				step.Event.IdempotencyKey = mutation.IdempotencyKey
			} else {
				step.Event.IdempotencyKey = fmt.Sprintf("%s:step:%d", mutation.IdempotencyKey, index)
			}
		}
		if index == 0 && step.Event.IdempotencyKey != mutation.IdempotencyKey {
			return result, errors.New("first compound event must use the mutation idempotency key")
		}
		if step.AuditOnly {
			if step.To != status {
				return result, fmt.Errorf("audit step must retain status %s", status)
			}
			step.Event.ToStatus = status
			if err = validateAuditEvent(step.Event); err != nil {
				return result, err
			}
		} else {
			if !CanTransition(status, step.To) {
				return result, &ErrInvalidTransition{From: status, To: step.To}
			}
			step.Event.ToStatus = step.To
			if err = step.Event.Validate(); err != nil {
				return result, err
			}
			status = step.To
		}
	}
	for index := range mutation.CreateAttempts {
		attempt := &mutation.CreateAttempts[index]
		if attempt.CaseID != incident.ID {
			return result, errors.New("created attempt must belong to mutated Case")
		}
		if attempt.StartedAt.IsZero() {
			attempt.StartedAt = time.Now().UTC()
		}
		if err = attempt.Validate(); err != nil {
			return result, err
		}
		if err = insertAttemptTx(ctx, tx, *attempt); err != nil {
			return result, err
		}
	}
	for index := range mutation.FinishAttempts {
		attempt := &mutation.FinishAttempts[index]
		if attempt.CaseID != incident.ID {
			return result, errors.New("finished attempt must belong to mutated Case")
		}
		switch attempt.Status {
		case AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusCancelled, AttemptStatusInterrupted:
		default:
			return result, errors.New("compound attempt finish requires terminal status")
		}
		if attempt.FinishedAt == nil {
			now := time.Now().UTC()
			attempt.FinishedAt = &now
		}
		if err = attempt.Validate(); err != nil {
			return result, err
		}
		var currentOutput string
		if queryErr := tx.QueryRowContext(ctx, `SELECT output_json FROM phase_attempts WHERE id=? AND case_id=? AND status IN (?,?)`, attempt.ID, incident.ID, AttemptStatusQueued, AttemptStatusRunning).Scan(&currentOutput); queryErr != nil {
			if errors.Is(queryErr, sql.ErrNoRows) {
				return result, fmt.Errorf("%w: %s", ErrAttemptAlreadyFinished, attempt.ID)
			}
			return result, queryErr
		}
		expectedOutput, hasExpected := mutation.ExpectedAttemptOutputs[attempt.ID]
		if hasExpected {
			if !bytes.Equal([]byte(currentOutput), expectedOutput) {
				return result, fmt.Errorf("%w: attempt output changed before finish", ErrIdempotencyConflict)
			}
		} else if _, found, parseErr := parseCompletionIntent([]byte(currentOutput)); parseErr != nil {
			return result, parseErr
		} else if found {
			return result, ErrCompletionIntentPending
		}
		completionIdentity := ""
		if mutation.CompletionAttemptID == attempt.ID {
			completionIdentity = mutation.CompletionIdentitySHA256
		}
		execResult, execErr := tx.ExecContext(ctx, `UPDATE phase_attempts SET status=?, output_json=?, finished_at=?, error_code=?, error_message=?, input_tokens=?, output_tokens=?, duration_nanos=?, completion_identity_sha256=?, run_claim_token='' WHERE id=? AND case_id=? AND status IN (?,?) AND output_json=?`, attempt.Status, string(attempt.OutputJSON), formatOptionalStoreTime(attempt.FinishedAt), attempt.ErrorCode, attempt.ErrorMessage, attempt.Usage.InputTokens, attempt.Usage.OutputTokens, int64(attempt.Usage.Duration), completionIdentity, attempt.ID, incident.ID, AttemptStatusQueued, AttemptStatusRunning, currentOutput)
		if execErr != nil {
			return result, execErr
		}
		rows, _ := execResult.RowsAffected()
		if rows != 1 {
			return result, fmt.Errorf("%w: %s", ErrAttemptAlreadyFinished, attempt.ID)
		}
	}
	for index := range mutation.Approvals {
		if err = insertApprovalTx(ctx, tx, mutation.Approvals[index], mutation.IdempotencyKey+":approval:"+mutation.Approvals[index].ID); err != nil {
			return result, err
		}
	}
	for index := range mutation.CodeChanges {
		if err = upsertCodeChangeTx(ctx, tx, mutation.CodeChanges[index]); err != nil {
			return result, err
		}
	}
	for index := range mutation.Observations {
		if err = insertObservationTx(ctx, tx, mutation.Observations[index], mutation.IdempotencyKey+":observation:"+mutation.Observations[index].ID); err != nil {
			return result, err
		}
	}
	if mutation.DeleteFixCheckpointAttemptID != "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM fix_checkpoints WHERE attempt_id=? AND case_id=?`, mutation.DeleteFixCheckpointAttemptID, incident.ID); err != nil {
			return result, err
		}
	}
	incident.Status = status
	applyCaseSnapshot(&incident, mutation.Snapshot)
	incident.Version++
	now := time.Now().UTC()
	incident.UpdatedAt = now
	if err = incident.Validate(); err != nil {
		return result, err
	}
	resultJSONBytes, _ := json.Marshal(incident.Clone())
	if consume != nil {
		if err = consumeBrowserRecoveryOperationTx(ctx, tx, consume, incident); err != nil {
			return result, err
		}
	}
	updateResult, execErr := tx.ExecContext(ctx, `UPDATE incident_cases SET status=?,cycle_number=?,current_attempt_id=?,selected_bot_key=?,version=?,updated_at=?,closed_at=? WHERE id=? AND version=?`, incident.Status, incident.CycleNumber, incident.CurrentAttemptID, incident.SelectedBotKey, incident.Version, formatStoreTime(now), formatOptionalStoreTime(incident.ClosedAt), incident.ID, mutation.ExpectedVersion)
	if execErr != nil {
		return result, execErr
	}
	rows, _ := updateResult.RowsAffected()
	if rows != 1 {
		return result, ErrCaseVersionConflict
	}
	for index, step := range mutation.Steps {
		created := step.Event.CreatedAt
		if created.IsZero() {
			created = now.Add(time.Duration(index) * time.Nanosecond)
		}
		_, execErr = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, step.Event.ID, incident.ID, step.Event.FromStatus, step.Event.ToStatus, step.Event.EventType, step.Event.ActorType, step.Event.ActorID, step.Event.IdempotencyKey, string(step.Event.PayloadJSON), formatStoreTime(created), fingerprint, string(resultJSONBytes))
		if execErr != nil {
			return result, execErr
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.Case = incident.Clone()
	return cloneCaseMutationResult(result), nil
}

func insertAttemptTx(ctx context.Context, tx *sql.Tx, a PhaseAttempt) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO phase_attempts (id,case_id,cycle_number,phase,mode,status,agent_target,bot_key,input_json,output_json,parent_attempt_id,started_at,finished_at,error_code,error_message,input_tokens,output_tokens,duration_nanos) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.CaseID, a.CycleNumber, a.Phase, a.Mode, a.Status, a.AgentTarget, a.BotKey, string(a.InputJSON), string(a.OutputJSON), a.ParentAttemptID, formatStoreTime(a.StartedAt), formatOptionalStoreTime(a.FinishedAt), a.ErrorCode, a.ErrorMessage, a.Usage.InputTokens, a.Usage.OutputTokens, int64(a.Usage.Duration))
	return err
}

func insertApprovalTx(ctx context.Context, tx *sql.Tx, a Approval, key string) error {
	if a.ApprovedAt.IsZero() {
		a.ApprovedAt = time.Now().UTC()
	}
	if err := a.Validate(); err != nil {
		return err
	}
	fixes, _ := marshalStringMap(a.FixCommits)
	targets, _ := marshalStringMap(a.TargetBranches)
	_, err := tx.ExecContext(ctx, `INSERT INTO approvals (id,case_id,kind,actor,approved_at,case_version,scope_json,fix_commits_json,target_branches_json,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?)`, a.ID, a.CaseID, a.Kind, a.Actor, formatStoreTime(a.ApprovedAt), a.CaseVersion, string(a.ScopeJSON), fixes, targets, key)
	return err
}

func upsertCodeChangeTx(ctx context.Context, tx *sql.Tx, c CodeChange) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var caseID, attemptID, repo, base, branch, commit string
	err := tx.QueryRowContext(ctx, `SELECT case_id,attempt_id,repo,base_branch,fix_branch,fix_commit FROM code_changes WHERE id=?`, c.ID).Scan(&caseID, &attemptID, &repo, &base, &branch, &commit)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO code_changes (id,case_id,attempt_id,repo,base_branch,fix_branch,fix_commit,test_evidence_json,target_environment_branch,merge_base_head,merge_commit,push_remote,push_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.CaseID, c.AttemptID, c.Repo, c.BaseBranch, c.FixBranch, c.FixCommit, string(c.TestEvidence), c.TargetEnvironmentBranch, c.MergeBaseHead, c.MergeCommit, c.PushRemote, c.PushStatus)
		return err
	}
	if err != nil {
		return err
	}
	if caseID != c.CaseID || attemptID != c.AttemptID || repo != c.Repo || base != c.BaseBranch || branch != c.FixBranch || commit != c.FixCommit {
		return ErrIdempotencyConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE code_changes SET test_evidence_json=?,target_environment_branch=?,merge_base_head=?,merge_commit=?,push_remote=?,push_status=? WHERE id=?`, string(c.TestEvidence), c.TargetEnvironmentBranch, c.MergeBaseHead, c.MergeCommit, c.PushRemote, c.PushStatus, c.ID)
	return err
}

func insertObservationTx(ctx context.Context, tx *sql.Tx, o DeploymentObservation, key string) error {
	if o.ObservedAt.IsZero() {
		switch {
		case o.VerifiedAt != nil:
			o.ObservedAt = *o.VerifiedAt
		case o.UserNotifiedAt != nil:
			o.ObservedAt = *o.UserNotifiedAt
		default:
			o.ObservedAt = time.Now().UTC()
		}
	}
	if err := o.Validate(); err != nil {
		return err
	}
	expected, _ := marshalStringMap(o.ExpectedCommits)
	images, _ := marshalStringMap(o.ObservedImages)
	commits, _ := marshalStringMap(o.ObservedCommits)
	ancestors, _ := marshalStringMap(o.VerifiedCommitAncestors)
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_observations (id,case_id,environment,expected_commits_json,user_notified_at,verification_source,observed_version,observed_images_json,observed_commits_json,verified_commit_ancestors_json,observed_at,diagnostic_code,diagnostic_message,verified_at,result,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.CaseID, o.Environment, expected, formatOptionalStoreTime(o.UserNotifiedAt), o.VerificationSource, o.ObservedVersion, images, commits, ancestors, formatStoreTime(o.ObservedAt), o.DiagnosticCode, o.DiagnosticMessage, formatOptionalStoreTime(o.VerifiedAt), o.Result, key)
	return err
}

func applyCaseSnapshot(c *IncidentCase, u CaseSnapshotUpdate) {
	if u.CurrentAttemptID != nil {
		c.CurrentAttemptID = *u.CurrentAttemptID
	}
	if u.SelectedBotKey != nil {
		c.SelectedBotKey = *u.SelectedBotKey
	}
	if u.CycleNumber != nil {
		c.CycleNumber = *u.CycleNumber
	}
	if u.ClosedAtSet {
		c.ClosedAt = cloneTimePtr(u.ClosedAt)
	}
}

func validateAuditEvent(e TransitionEvent) error {
	if blank(e.ID) || blank(e.CaseID) || e.FromStatus != e.ToStatus || !e.FromStatus.valid() {
		return errors.New("invalid audit event status")
	}
	if blank(e.EventType) || blank(e.ActorType) || blank(e.ActorID) || blank(e.IdempotencyKey) || len(e.PayloadJSON) == 0 || !json.Valid(e.PayloadJSON) {
		return errors.New("invalid audit event")
	}
	return nil
}

func caseMutationFingerprint(m CaseMutation) (string, error) {
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(encoded)
	raws := []json.RawMessage{m.RequestJSON}
	for _, s := range m.Steps {
		raws = append(raws, s.Event.PayloadJSON)
	}
	for _, a := range m.CreateAttempts {
		raws = append(raws, a.InputJSON, a.OutputJSON)
	}
	for _, a := range m.FinishAttempts {
		raws = append(raws, a.InputJSON, a.OutputJSON)
	}
	for _, a := range m.Approvals {
		raws = append(raws, a.ScopeJSON)
	}
	for _, c := range m.CodeChanges {
		raws = append(raws, c.TestEvidence)
	}
	for _, raw := range raws {
		sum := sha256.Sum256(raw)
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cloneCaseMutationResult(r CaseMutationResult) CaseMutationResult {
	r.Case = r.Case.Clone()
	return r
}

type transitionFingerprintInput struct {
	EventID          string     `json:"event_id"`
	CaseID           string     `json:"case_id"`
	ExpectedVersion  int64      `json:"expected_version"`
	FromStatus       CaseStatus `json:"from_status"`
	ToStatus         CaseStatus `json:"to_status"`
	EventType        string     `json:"event_type"`
	ActorType        string     `json:"actor_type"`
	ActorID          string     `json:"actor_id"`
	PayloadSHA256    string     `json:"payload_sha256_base64"`
	RequestedAt      string     `json:"requested_at"`
	CurrentAttemptID *string    `json:"current_attempt_id"`
	SelectedBotKey   *string    `json:"selected_bot_key"`
	CycleNumber      *int       `json:"cycle_number"`
	ClosedAtSet      bool       `json:"closed_at_set"`
	ClosedAt         *string    `json:"closed_at"`
}

func (s *CaseStore) Transition(ctx context.Context, caseID string, expectedVersion int64, to CaseStatus, event TransitionEvent) (IncidentCase, bool, error) {
	return s.TransitionWithUpdate(ctx, caseID, expectedVersion, to, CaseSnapshotUpdate{}, event)
}

func (s *CaseStore) TransitionWithUpdate(ctx context.Context, caseID string, expectedVersion int64, to CaseStatus, update CaseSnapshotUpdate, event TransitionEvent) (updated IncidentCase, replay bool, err error) {
	event = event.Clone()
	if blank(caseID) {
		return IncidentCase{}, false, errors.New("transition case ID is required")
	}
	if event.CaseID != "" && event.CaseID != caseID {
		return IncidentCase{}, false, fmt.Errorf("transition event case ID %q does not match %q", event.CaseID, caseID)
	}
	if event.ToStatus != "" && event.ToStatus != to {
		return IncidentCase{}, false, fmt.Errorf("transition event target %q does not match %q", event.ToStatus, to)
	}
	if update.CycleNumber != nil && *update.CycleNumber < 1 {
		return IncidentCase{}, false, errors.New("transition cycle number must be positive")
	}
	if update.ClosedAt != nil && update.ClosedAt.IsZero() {
		return IncidentCase{}, false, errors.New("transition closed_at must not be zero when provided")
	}
	if !update.ClosedAtSet && update.ClosedAt != nil {
		return IncidentCase{}, false, errors.New("transition closed_at requires ClosedAtSet")
	}
	requestedAt := ""
	if !event.CreatedAt.IsZero() {
		requestedAt = formatStoreTime(event.CreatedAt)
	}
	if event.EventType == "" {
		event.EventType = "transition"
	}
	if event.ActorType == "" {
		event.ActorType = "studio"
	}
	if event.ActorID == "" {
		event.ActorID = "case-store"
	}
	if len(event.PayloadJSON) == 0 {
		event.PayloadJSON = []byte(`{}`)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IncidentCase{}, false, fmt.Errorf("begin case transition: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existingFingerprint, resultCaseJSON string
	var existingFrom CaseStatus
	err = tx.QueryRowContext(ctx, `SELECT from_status, request_fingerprint, result_case_json
		FROM transition_events WHERE idempotency_key = ?`, event.IdempotencyKey).Scan(
		&existingFrom, &existingFingerprint, &resultCaseJSON,
	)
	if err == nil {
		requestFrom := existingFrom
		if event.FromStatus != "" {
			requestFrom = event.FromStatus
		}
		fingerprint, fingerprintErr := transitionRequestFingerprint(caseID, expectedVersion, requestFrom, to, event, requestedAt, update)
		if fingerprintErr != nil {
			return IncidentCase{}, false, fingerprintErr
		}
		if fingerprint != existingFingerprint {
			return IncidentCase{}, false, fmt.Errorf("%w: transition key %q", ErrIdempotencyConflict, event.IdempotencyKey)
		}
		if err := json.Unmarshal([]byte(resultCaseJSON), &updated); err != nil {
			return IncidentCase{}, false, fmt.Errorf("decode replayed incident case: %w", err)
		}
		if err := updated.Validate(); err != nil {
			return IncidentCase{}, false, fmt.Errorf("validate replayed incident case: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return IncidentCase{}, false, fmt.Errorf("commit replayed case transition: %w", err)
		}
		return updated.Clone(), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IncidentCase{}, false, fmt.Errorf("check transition idempotency: %w", err)
	}
	if err = rejectUnresolvedBrowserRecovery(ctx, tx, caseID); err != nil {
		return IncidentCase{}, false, err
	}

	updated, err = getCase(ctx, tx, caseID)
	if err != nil {
		return IncidentCase{}, false, err
	}
	if updated.Version != expectedVersion {
		return IncidentCase{}, false, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, expectedVersion, updated.Version)
	}
	if err = ValidateTransition(updated, to); err != nil {
		return IncidentCase{}, false, err
	}
	if event.FromStatus != "" && event.FromStatus != updated.Status {
		return IncidentCase{}, false, fmt.Errorf("transition event source %q does not match %q", event.FromStatus, updated.Status)
	}

	event.CaseID = caseID
	event.FromStatus = updated.Status
	event.ToStatus = to
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if err = event.Validate(); err != nil {
		return IncidentCase{}, false, err
	}

	updated.Status = to
	if update.CurrentAttemptID != nil {
		updated.CurrentAttemptID = *update.CurrentAttemptID
	}
	if update.SelectedBotKey != nil {
		updated.SelectedBotKey = *update.SelectedBotKey
	}
	if update.CycleNumber != nil {
		updated.CycleNumber = *update.CycleNumber
	}
	if update.ClosedAtSet {
		updated.ClosedAt = cloneTimePtr(update.ClosedAt)
	}
	updated.Version++
	updated.UpdatedAt = event.CreatedAt
	if err := updated.Validate(); err != nil {
		return IncidentCase{}, false, err
	}
	fingerprint, err := transitionRequestFingerprint(caseID, expectedVersion, event.FromStatus, to, event, requestedAt, update)
	if err != nil {
		return IncidentCase{}, false, err
	}
	resultCase, err := json.Marshal(updated.Clone())
	if err != nil {
		return IncidentCase{}, false, fmt.Errorf("encode transition result case: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE incident_cases
		SET status = ?, cycle_number = ?, current_attempt_id = ?, selected_bot_key = ?, version = ?, updated_at = ?, closed_at = ?
		WHERE id = ? AND version = ?`, updated.Status, updated.CycleNumber, updated.CurrentAttemptID,
		updated.SelectedBotKey, updated.Version, formatStoreTime(updated.UpdatedAt),
		formatOptionalStoreTime(updated.ClosedAt), caseID, expectedVersion)
	if err != nil {
		return IncidentCase{}, false, fmt.Errorf("update incident case transition: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return IncidentCase{}, false, fmt.Errorf("inspect incident case transition: %w", err)
	}
	if rowsAffected != 1 {
		return IncidentCase{}, false, ErrCaseVersionConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO transition_events (
		id, case_id, from_status, to_status, event_type, actor_type, actor_id,
		idempotency_key, payload_json, created_at, request_fingerprint, result_case_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.CaseID, event.FromStatus,
		event.ToStatus, event.EventType, event.ActorType, event.ActorID, event.IdempotencyKey,
		string(event.PayloadJSON), formatStoreTime(event.CreatedAt), fingerprint, string(resultCase))
	if err != nil {
		return IncidentCase{}, false, fmt.Errorf("insert transition event: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return IncidentCase{}, false, fmt.Errorf("commit case transition: %w", err)
	}
	return updated.Clone(), false, nil
}

func (s *CaseStore) ListEvents(ctx context.Context, caseID string) ([]TransitionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, case_id, from_status, to_status,
		event_type, actor_type, actor_id, idempotency_key, payload_json, created_at
		FROM transition_events WHERE case_id = ? ORDER BY created_at ASC, id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("list transition events: %w", err)
	}
	defer rows.Close()
	var events []TransitionEvent
	for rows.Next() {
		var event TransitionEvent
		var payload, createdAt string
		if err := rows.Scan(&event.ID, &event.CaseID, &event.FromStatus, &event.ToStatus,
			&event.EventType, &event.ActorType, &event.ActorID, &event.IdempotencyKey,
			&payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan transition event: %w", err)
		}
		event.PayloadJSON = CloneRawMessage([]byte(payload))
		event.CreatedAt, err = parseStoreTime(createdAt)
		if err != nil {
			return nil, err
		}
		var validationErr error
		if event.FromStatus == event.ToStatus {
			validationErr = validateAuditEvent(event)
		} else {
			validationErr = event.Validate()
		}
		if validationErr != nil {
			return nil, fmt.Errorf("validate stored transition event: %w", validationErr)
		}
		events = append(events, event.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transition events: %w", err)
	}
	return events, nil
}

func (s *CaseStore) GetEventByIdempotencyKey(ctx context.Context, key string) (TransitionEvent, bool, error) {
	var event TransitionEvent
	var payload, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at FROM transition_events WHERE idempotency_key=?`, key).Scan(&event.ID, &event.CaseID, &event.FromStatus, &event.ToStatus, &event.EventType, &event.ActorType, &event.ActorID, &event.IdempotencyKey, &payload, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return TransitionEvent{}, false, nil
	}
	if err != nil {
		return TransitionEvent{}, false, err
	}
	event.PayloadJSON = []byte(payload)
	event.CreatedAt, err = parseStoreTime(created)
	if err != nil {
		return TransitionEvent{}, false, err
	}
	if event.FromStatus == event.ToStatus {
		err = validateAuditEvent(event)
	} else {
		err = event.Validate()
	}
	if err != nil {
		return TransitionEvent{}, false, err
	}
	return event.Clone(), true, nil
}

// CommittedCaseMutation is the immutable identity and result snapshot stored
// with the first event of one compound Case mutation.
type CommittedCaseMutation struct {
	Event       TransitionEvent
	Fingerprint string
	ResultCase  IncidentCase
}

// GetCommittedCaseMutation returns the durable replay identity without reading
// the mutable current Case snapshot.
func (s *CaseStore) GetCommittedCaseMutation(ctx context.Context, key string) (CommittedCaseMutation, bool, error) {
	var replay CommittedCaseMutation
	var payload, created, resultJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, key).Scan(&replay.Event.ID, &replay.Event.CaseID, &replay.Event.FromStatus, &replay.Event.ToStatus, &replay.Event.EventType, &replay.Event.ActorType, &replay.Event.ActorID, &replay.Event.IdempotencyKey, &payload, &created, &replay.Fingerprint, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return CommittedCaseMutation{}, false, nil
	}
	if err != nil {
		return CommittedCaseMutation{}, false, err
	}
	replay.Event.PayloadJSON = []byte(payload)
	replay.Event.CreatedAt, err = parseStoreTime(created)
	if err != nil {
		return CommittedCaseMutation{}, false, err
	}
	fingerprintBytes, decodeErr := hex.DecodeString(replay.Fingerprint)
	if decodeErr != nil || len(fingerprintBytes) != sha256.Size {
		return CommittedCaseMutation{}, false, errors.New("committed Case mutation fingerprint is invalid")
	}
	if err := json.Unmarshal([]byte(resultJSON), &replay.ResultCase); err != nil {
		return CommittedCaseMutation{}, false, fmt.Errorf("decode committed Case mutation result: %w", err)
	}
	if replay.ResultCase.ID != replay.Event.CaseID {
		return CommittedCaseMutation{}, false, errors.New("committed Case mutation result belongs to a different Case")
	}
	if err := replay.ResultCase.Validate(); err != nil {
		return CommittedCaseMutation{}, false, err
	}
	if replay.Event.FromStatus == replay.Event.ToStatus {
		err = validateAuditEvent(replay.Event)
	} else {
		err = replay.Event.Validate()
	}
	if err != nil {
		return CommittedCaseMutation{}, false, err
	}
	return replay, true, nil
}

func (s *CaseStore) GetAttemptCompletionIdentity(ctx context.Context, attemptID string) (string, bool, error) {
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT completion_identity_sha256 FROM phase_attempts WHERE id=?`, attemptID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if digest == "" {
		return "", false, nil
	}
	decoded, decodeErr := hex.DecodeString(digest)
	if decodeErr != nil || len(decoded) != sha256.Size {
		return "", false, errors.New("persisted completion identity is invalid")
	}
	return digest, true, nil
}

// latestDeploymentReservationEvent reads the reservation envelope without
// validating actor identity. Recovery must be able to detect and audit a
// legacy/corrupt empty actor instead of failing before the identity gate.
func (s *CaseStore) latestDeploymentReservationEvent(ctx context.Context, caseID string) (TransitionEvent, bool, error) {
	var event TransitionEvent
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,event_type,actor_type,actor_id,idempotency_key,payload_json
		FROM transition_events WHERE case_id=? AND event_type IN ('deployment_verification_reserved','remediation_regression_reserved')
		ORDER BY created_at DESC,id DESC LIMIT 1`, caseID).Scan(&event.ID, &event.CaseID, &event.EventType, &event.ActorType, &event.ActorID, &event.IdempotencyKey, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return TransitionEvent{}, false, nil
	}
	if err != nil {
		return TransitionEvent{}, false, fmt.Errorf("load latest deployment reservation event: %w", err)
	}
	event.PayloadJSON = CloneRawMessage([]byte(payload))
	return event, true, nil
}

func transitionRequestFingerprint(caseID string, expectedVersion int64, from, to CaseStatus, event TransitionEvent, requestedAt string, update CaseSnapshotUpdate) (string, error) {
	payloadDigest := sha256.Sum256([]byte(event.PayloadJSON))
	var closedAt *string
	if update.ClosedAt != nil {
		formatted := formatStoreTime(*update.ClosedAt)
		closedAt = &formatted
	}
	input := transitionFingerprintInput{
		EventID:          event.ID,
		CaseID:           caseID,
		ExpectedVersion:  expectedVersion,
		FromStatus:       from,
		ToStatus:         to,
		EventType:        event.EventType,
		ActorType:        event.ActorType,
		ActorID:          event.ActorID,
		PayloadSHA256:    base64.StdEncoding.EncodeToString(payloadDigest[:]),
		RequestedAt:      requestedAt,
		CurrentAttemptID: cloneStringPtr(update.CurrentAttemptID),
		SelectedBotKey:   cloneStringPtr(update.SelectedBotKey),
		CycleNumber:      cloneIntPtr(update.CycleNumber),
		ClosedAtSet:      update.ClosedAtSet,
		ClosedAt:         closedAt,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode transition request fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type caseQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowsQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(...any) error
}

func getCase(ctx context.Context, query caseQuery, id string) (IncidentCase, error) {
	incident, err := scanCase(query.QueryRowContext(ctx, `SELECT id, bug_id, source, system_id,
		environment, status, cycle_number, current_attempt_id, selected_bot_key,
		reset_from_case_id, superseded_by_case_id, version,
		created_at, updated_at, closed_at FROM incident_cases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return IncidentCase{}, fmt.Errorf("%w: %s", ErrCaseNotFound, id)
	}
	return incident, err
}

func scanCase(row rowScanner) (IncidentCase, error) {
	var incident IncidentCase
	var createdAt, updatedAt string
	var closedAt sql.NullString
	if err := row.Scan(&incident.ID, &incident.BugID, &incident.Source, &incident.SystemID,
		&incident.Environment, &incident.Status, &incident.CycleNumber, &incident.CurrentAttemptID,
		&incident.SelectedBotKey, &incident.ResetFromCaseID, &incident.SupersededByCaseID,
		&incident.Version, &createdAt, &updatedAt, &closedAt); err != nil {
		return IncidentCase{}, err
	}
	var err error
	incident.CreatedAt, err = parseStoreTime(createdAt)
	if err != nil {
		return IncidentCase{}, err
	}
	incident.UpdatedAt, err = parseStoreTime(updatedAt)
	if err != nil {
		return IncidentCase{}, err
	}
	if closedAt.Valid {
		closed, err := parseStoreTime(closedAt.String)
		if err != nil {
			return IncidentCase{}, err
		}
		incident.ClosedAt = &closed
	}
	if err := incident.Validate(); err != nil {
		return IncidentCase{}, fmt.Errorf("validate stored incident case: %w", err)
	}
	return incident.Clone(), nil
}

func getAttempt(ctx context.Context, query caseQuery, id string) (PhaseAttempt, error) {
	attempt, err := scanAttempt(query.QueryRowContext(ctx, `SELECT id, case_id, cycle_number, phase, mode, status,
		agent_target, bot_key, input_json, output_json, parent_attempt_id, started_at,
		finished_at, error_code, error_message, input_tokens, output_tokens, duration_nanos
		FROM phase_attempts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseAttempt{}, fmt.Errorf("phase attempt %q not found", id)
	}
	return attempt, err
}

func scanAttempt(row rowScanner) (PhaseAttempt, error) {
	var attempt PhaseAttempt
	var inputJSON, outputJSON, startedAt string
	var finishedAt sql.NullString
	var durationNanos int64
	err := row.Scan(
		&attempt.ID, &attempt.CaseID, &attempt.CycleNumber, &attempt.Phase, &attempt.Mode,
		&attempt.Status, &attempt.AgentTarget, &attempt.BotKey, &inputJSON, &outputJSON,
		&attempt.ParentAttemptID, &startedAt, &finishedAt, &attempt.ErrorCode,
		&attempt.ErrorMessage, &attempt.Usage.InputTokens, &attempt.Usage.OutputTokens, &durationNanos,
	)
	if err != nil {
		return PhaseAttempt{}, err
	}
	attempt.InputJSON = CloneRawMessage([]byte(inputJSON))
	attempt.OutputJSON = CloneRawMessage([]byte(outputJSON))
	attempt.StartedAt, err = parseStoreTime(startedAt)
	if err != nil {
		return PhaseAttempt{}, err
	}
	if finishedAt.Valid {
		finished, err := parseStoreTime(finishedAt.String)
		if err != nil {
			return PhaseAttempt{}, err
		}
		attempt.FinishedAt = &finished
	}
	attempt.Usage.Duration = time.Duration(durationNanos)
	if err := attempt.ValidateWithOptions(AttemptValidationOptions{AllowLegacyMigration: attempt.Phase == PhaseLegacy}); err != nil {
		return PhaseAttempt{}, fmt.Errorf("validate stored phase attempt: %w", err)
	}
	return attempt.Clone(), nil
}

func sameAttemptFinish(left, right PhaseAttempt, compareFinishedAt bool) bool {
	return left.ID == right.ID &&
		left.CaseID == right.CaseID &&
		left.Status == right.Status &&
		bytes.Equal(left.OutputJSON, right.OutputJSON) &&
		left.ErrorCode == right.ErrorCode &&
		left.ErrorMessage == right.ErrorMessage &&
		left.Usage == right.Usage &&
		(!compareFinishedAt || timesEqual(left.FinishedAt, right.FinishedAt))
}

func timesEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func optionalTimeMatches(stored sql.NullString, requested *time.Time, compare bool) bool {
	if !compare {
		return true
	}
	return stored.Valid && requested != nil && stored.String == formatStoreTime(*requested)
}

func secureSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("secure SQLite file %q: %w", candidate, err)
		}
	}
	return nil
}

func workflowTableColumns(ctx context.Context, query rowsQuery) (map[string][]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list workflow schema tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan workflow schema table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close workflow schema table rows: %w", err)
	}
	result := make(map[string][]string, len(tables))
	for _, table := range tables {
		quoted := strings.ReplaceAll(table, `"`, `""`)
		columnRows, err := query.QueryContext(ctx, `PRAGMA table_info("`+quoted+`")`)
		if err != nil {
			return nil, fmt.Errorf("inspect workflow table %q: %w", table, err)
		}
		for columnRows.Next() {
			var cid, notNull, pk int
			var name, columnType string
			var defaultValue any
			if err := columnRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				columnRows.Close()
				return nil, fmt.Errorf("scan workflow table %q: %w", table, err)
			}
			result[table] = append(result[table], name)
		}
		if err := columnRows.Close(); err != nil {
			return nil, fmt.Errorf("close workflow table %q columns: %w", table, err)
		}
	}
	return result, nil
}

func workflowUserSchemaObjectCount(ctx context.Context, query caseQuery) (int, error) {
	var count int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count workflow schema objects: %w", err)
	}
	return count, nil
}

func verifyWorkflowColumns(actual, expected map[string][]string) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: tables=%v", ErrUnsupportedWorkflowSchema, sortedMapKeys(actual))
	}
	for table, expectedColumns := range expected {
		actualColumns, ok := actual[table]
		if !ok {
			return fmt.Errorf("%w: missing table %q", ErrUnsupportedWorkflowSchema, table)
		}
		actualSorted := append([]string(nil), actualColumns...)
		expectedSorted := append([]string(nil), expectedColumns...)
		sort.Strings(actualSorted)
		sort.Strings(expectedSorted)
		if strings.Join(actualSorted, "\x00") != strings.Join(expectedSorted, "\x00") {
			return fmt.Errorf("%w: table %q columns=%v", ErrUnsupportedWorkflowSchema, table, actualColumns)
		}
	}
	return nil
}

func verifyRequiredWorkflowIndexes(ctx context.Context, query rowsQuery) error {
	rows, err := query.QueryContext(ctx, `SELECT name, tbl_name FROM sqlite_master
		WHERE type = 'index' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("list workflow indexes: %w", err)
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			return fmt.Errorf("scan workflow index: %w", err)
		}
		found[name] = table
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list workflow indexes: %w", err)
	}
	for name, table := range requiredWorkflowIndexes {
		if found[name] != table {
			return fmt.Errorf("%w: missing or invalid index %q", ErrUnsupportedWorkflowSchema, name)
		}
	}
	return nil
}

func workflowSchemaFingerprint(ctx context.Context, query rowsQuery) (string, error) {
	rows, err := query.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY type ASC, name ASC`)
	if err != nil {
		return "", fmt.Errorf("read workflow schema definition: %w", err)
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var objectType, name, table, definition string
		if err := rows.Scan(&objectType, &name, &table, &definition); err != nil {
			return "", fmt.Errorf("scan workflow schema definition: %w", err)
		}
		for _, value := range []string{objectType, name, table, definition} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read workflow schema definition: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneWorkflowColumns(values map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(values))
	for key, columns := range values {
		cloned[key] = append([]string(nil), columns...)
	}
	return cloned
}

func sortedMapKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatStoreTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func formatOptionalStoreTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatStoreTime(*value)
}

func parseStoreTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse case store time %q: %w", value, err)
	}
	return parsed, nil
}

func parseOptionalStoreTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseStoreTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func marshalStringMap(values map[string]string) (string, error) {
	if values == nil {
		return `{}`, nil
	}
	encoded, err := json.Marshal(CloneStringMap(values))
	if err != nil {
		return "", fmt.Errorf("encode string map: %w", err)
	}
	return string(encoded), nil
}
