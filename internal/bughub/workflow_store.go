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
	ErrUnsupportedWorkflowSchema = errors.New("unsupported workflow store schema")
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
		detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: workflowStoreSchemaVersion, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("encode workflow schema v3 detail: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at = ?, detail_json = ? WHERE key = ?`, formatStoreTime(time.Now().UTC()), string(detail), workflowStoreSchemaV1Key); err != nil {
			return fmt.Errorf("record workflow schema v3: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
			return fmt.Errorf("set workflow schema version 3: %w", err)
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
		current_attempt_id, selected_bot_key, version, created_at, updated_at, closed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		incident.ID, incident.BugID, incident.Source, incident.SystemID, incident.Environment,
		incident.Status, incident.CycleNumber, incident.CurrentAttemptID, incident.SelectedBotKey,
		incident.Version, formatStoreTime(incident.CreatedAt), formatStoreTime(incident.UpdatedAt),
		formatOptionalStoreTime(incident.ClosedAt),
	)
	if err != nil {
		return fmt.Errorf("create incident case: %w", err)
	}
	return nil
}

func (s *CaseStore) CreateCaseWithIdentity(ctx context.Context, creation CaseCreation) (created IncidentCase, replay bool, err error) {
	creation.Case = creation.Case.Clone()
	if creation.Case.Status != CasePendingValidation || creation.Case.CurrentAttemptID != "" || creation.Case.ClosedAt != nil {
		return created, false, errors.New("durable Case creation requires an open pending_validation Case without an attempt")
	}
	if blank(creation.IdempotencyKey) || blank(creation.ActorID) || len(creation.RequestJSON) == 0 || !json.Valid(creation.RequestJSON) {
		return created, false, errors.New("durable Case creation requires idempotency key, actor, and valid request JSON")
	}
	if err := creation.Case.Validate(); err != nil {
		return created, false, err
	}
	fingerprint, err := caseCreationFingerprint(creation)
	if err != nil {
		return created, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return created, false, fmt.Errorf("begin durable Case creation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var eventType, storedFingerprint, resultJSON string
	queryErr := tx.QueryRowContext(ctx, `SELECT event_type,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key=?`, creation.IdempotencyKey).Scan(&eventType, &storedFingerprint, &resultJSON)
	if queryErr == nil {
		if eventType != "case_created" || storedFingerprint != fingerprint {
			return created, false, ErrIdempotencyConflict
		}
		if err = json.Unmarshal([]byte(resultJSON), &created); err != nil {
			return created, false, fmt.Errorf("decode durable Case creation replay: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return IncidentCase{}, false, err
		}
		return created.Clone(), true, nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return created, false, queryErr
	}
	if _, caseErr := getCase(ctx, tx, creation.Case.ID); caseErr == nil {
		return created, false, ErrIdempotencyConflict
	} else if !errors.Is(caseErr, ErrCaseNotFound) {
		return created, false, caseErr
	}
	now := time.Now().UTC()
	created = creation.Case.Clone()
	if created.Version == 0 {
		created.Version = 1
	}
	if created.Version != 1 {
		return IncidentCase{}, false, errors.New("new durable Case version must be one")
	}
	if created.CreatedAt.IsZero() {
		created.CreatedAt = now
	}
	if created.UpdatedAt.IsZero() {
		created.UpdatedAt = created.CreatedAt
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO incident_cases (id,bug_id,source,system_id,environment,status,cycle_number,current_attempt_id,selected_bot_key,version,created_at,updated_at,closed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, created.ID, created.BugID, created.Source, created.SystemID, created.Environment, created.Status, created.CycleNumber, created.CurrentAttemptID, created.SelectedBotKey, created.Version, formatStoreTime(created.CreatedAt), formatStoreTime(created.UpdatedAt), nil); err != nil {
		return IncidentCase{}, false, fmt.Errorf("insert durable Case: %w", err)
	}
	resultBytes, marshalErr := json.Marshal(created)
	if marshalErr != nil {
		return IncidentCase{}, false, marshalErr
	}
	resultJSON = string(resultBytes)
	payload := mustJSON(map[string]any{"case_id": created.ID, "cycle_number": created.CycleNumber})
	if _, err = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", "case-created:"+creation.IdempotencyKey), created.ID, created.Status, created.Status, "case_created", "user", creation.ActorID, creation.IdempotencyKey, string(payload), formatStoreTime(now), fingerprint, resultJSON); err != nil {
		return IncidentCase{}, false, fmt.Errorf("insert durable Case creation identity: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return IncidentCase{}, false, err
	}
	return created.Clone(), false, nil
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

func (s *CaseStore) GetCase(ctx context.Context, id string) (IncidentCase, error) {
	return getCase(ctx, s.db, id)
}

func (s *CaseStore) ListCases(ctx context.Context) ([]IncidentCase, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, bug_id, source, system_id, environment,
		status, cycle_number, current_attempt_id, selected_bot_key, version,
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
			current_attempt_id, selected_bot_key, version, created_at, updated_at, closed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			incident.ID, incident.BugID, incident.Source, incident.SystemID, incident.Environment,
			incident.Status, incident.CycleNumber, incident.CurrentAttemptID, incident.SelectedBotKey,
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
		input_tokens = ?, output_tokens = ?, duration_nanos = ?
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

// CaseMutation is the single typed write boundary used by the orchestrator.
// All records, attempt changes, ordered transition/audit events, and the final
// Case snapshot commit or roll back together under one Case-version CAS.
type CaseMutation struct {
	CaseID                 string
	ExpectedVersion        int64
	IdempotencyKey         string
	RequestJSON            json.RawMessage
	Steps                  []CaseMutationStep
	CreateAttempts         []PhaseAttempt
	FinishAttempts         []PhaseAttempt
	Approvals              []Approval
	CodeChanges            []CodeChange
	Observations           []DeploymentObservation
	ExpectedAttemptOutputs map[string]json.RawMessage `json:"-"`
	Snapshot               CaseSnapshotUpdate
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

func (s *CaseStore) ApplyCaseMutation(ctx context.Context, mutation CaseMutation) (result CaseMutationResult, err error) {
	mutation = mutation.clone()
	if blank(mutation.CaseID) || mutation.ExpectedVersion < 1 || blank(mutation.IdempotencyKey) {
		return result, errors.New("compound Case mutation requires case ID, positive expected version, and idempotency key")
	}
	if len(mutation.RequestJSON) == 0 || !json.Valid(mutation.RequestJSON) {
		return result, errors.New("compound Case mutation request must be valid JSON")
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
			return result, fmt.Errorf("%w: compound mutation key %q", ErrIdempotencyConflict, mutation.IdempotencyKey)
		}
		if err = json.Unmarshal([]byte(resultJSON), &result.Case); err != nil {
			return result, fmt.Errorf("decode compound replay: %w", err)
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
	incident, err := getCase(ctx, tx, mutation.CaseID)
	if err != nil {
		return result, err
	}
	if incident.Version != mutation.ExpectedVersion {
		return result, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, mutation.ExpectedVersion, incident.Version)
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
		execResult, execErr := tx.ExecContext(ctx, `UPDATE phase_attempts SET status=?, output_json=?, finished_at=?, error_code=?, error_message=?, input_tokens=?, output_tokens=?, duration_nanos=? WHERE id=? AND case_id=? AND status IN (?,?) AND output_json=?`, attempt.Status, string(attempt.OutputJSON), formatOptionalStoreTime(attempt.FinishedAt), attempt.ErrorCode, attempt.ErrorMessage, attempt.Usage.InputTokens, attempt.Usage.OutputTokens, int64(attempt.Usage.Duration), attempt.ID, incident.ID, AttemptStatusQueued, AttemptStatusRunning, currentOutput)
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
	incident.Status = status
	applyCaseSnapshot(&incident, mutation.Snapshot)
	incident.Version++
	now := time.Now().UTC()
	incident.UpdatedAt = now
	if err = incident.Validate(); err != nil {
		return result, err
	}
	resultJSONBytes, _ := json.Marshal(incident.Clone())
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

// latestDeploymentReservationEvent reads the reservation envelope without
// validating actor identity. Recovery must be able to detect and audit a
// legacy/corrupt empty actor instead of failing before the identity gate.
func (s *CaseStore) latestDeploymentReservationEvent(ctx context.Context, caseID string) (TransitionEvent, bool, error) {
	var event TransitionEvent
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT id,case_id,event_type,actor_type,actor_id,idempotency_key,payload_json
		FROM transition_events WHERE case_id=? AND event_type='deployment_verification_reserved'
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
		environment, status, cycle_number, current_attempt_id, selected_bot_key, version,
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
		&incident.SelectedBotKey, &incident.Version, &createdAt, &updatedAt, &closedAt); err != nil {
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
