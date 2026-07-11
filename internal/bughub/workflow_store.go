package bughub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrCaseNotFound           = errors.New("incident case not found")
	ErrCaseVersionConflict    = errors.New("incident case version conflict")
	ErrIdempotencyConflict    = errors.New("idempotency key conflicts with committed request")
	ErrAttemptAlreadyFinished = errors.New("phase attempt is already finished")
)

type CaseStore struct {
	db *sql.DB
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
	if _, err := s.db.ExecContext(ctx, workflowStoreSchema); err != nil {
		return fmt.Errorf("initialize case store schema: %w", err)
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

func (s *CaseStore) createAttempt(ctx context.Context, attempt PhaseAttempt, options AttemptValidationOptions) error {
	attempt = attempt.Clone()
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
	result, err := s.db.ExecContext(ctx, `INSERT INTO deployment_observations (
		id, case_id, environment, expected_commits_json, user_notified_at,
		verification_source, observed_version, observed_images_json,
		observed_commits_json, verified_at, result, idempotency_key
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(idempotency_key) DO NOTHING`, observation.ID, observation.CaseID,
		observation.Environment, expectedCommits, formatOptionalStoreTime(observation.UserNotifiedAt),
		observation.VerificationSource, observation.ObservedVersion, observedImages, observedCommits,
		formatOptionalStoreTime(observation.VerifiedAt), observation.Result, idempotencyKey)
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
	var id, caseID, environment, storedExpected, source, version, storedImages, storedCommits, resultValue string
	var storedUserNotifiedAt, storedVerifiedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT id, case_id, environment, expected_commits_json,
		user_notified_at, verification_source, observed_version, observed_images_json,
		observed_commits_json, verified_at, result
		FROM deployment_observations WHERE idempotency_key = ?`, idempotencyKey).Scan(
		&id, &caseID, &environment, &storedExpected, &storedUserNotifiedAt, &source, &version,
		&storedImages, &storedCommits, &storedVerifiedAt, &resultValue,
	); err != nil {
		return fmt.Errorf("load replayed deployment observation: %w", err)
	}
	if id == observation.ID && caseID == observation.CaseID && environment == observation.Environment &&
		storedExpected == expectedCommits && source == observation.VerificationSource &&
		version == observation.ObservedVersion && storedImages == observedImages &&
		storedCommits == observedCommits && resultValue == string(observation.Result) &&
		optionalTimeMatches(storedUserNotifiedAt, observation.UserNotifiedAt, userNotifiedAtProvided) &&
		optionalTimeMatches(storedVerifiedAt, observation.VerifiedAt, verifiedAtProvided) {
		return nil
	}
	return fmt.Errorf("%w: deployment observation key %q", ErrIdempotencyConflict, idempotencyKey)
}

func (s *CaseStore) Transition(ctx context.Context, caseID string, expectedVersion int64, to CaseStatus, event TransitionEvent) (updated IncidentCase, replay bool, err error) {
	event = event.Clone()
	createdAtProvided := !event.CreatedAt.IsZero()
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

	var existingID, existingCaseID, existingEventType, existingActorType, existingActorID, existingPayload, existingCreatedAt string
	var existingTo CaseStatus
	err = tx.QueryRowContext(ctx, `SELECT id, case_id, to_status, event_type, actor_type,
		actor_id, payload_json, created_at FROM transition_events WHERE idempotency_key = ?`,
		event.IdempotencyKey).Scan(&existingID, &existingCaseID, &existingTo, &existingEventType,
		&existingActorType, &existingActorID, &existingPayload, &existingCreatedAt)
	if err == nil {
		if existingID != event.ID || existingCaseID != caseID || existingTo != to ||
			existingEventType != event.EventType || existingActorType != event.ActorType ||
			existingActorID != event.ActorID || existingPayload != string(event.PayloadJSON) ||
			(createdAtProvided && existingCreatedAt != formatStoreTime(event.CreatedAt)) {
			return IncidentCase{}, false, fmt.Errorf("%w: transition key %q", ErrIdempotencyConflict, event.IdempotencyKey)
		}
		updated, err = getCase(ctx, tx, caseID)
		if err != nil {
			return IncidentCase{}, false, err
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
	updated.Version++
	updated.UpdatedAt = event.CreatedAt
	result, err := tx.ExecContext(ctx, `UPDATE incident_cases
		SET status = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
		updated.Status, updated.Version, formatStoreTime(updated.UpdatedAt), caseID, expectedVersion)
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
		idempotency_key, payload_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.CaseID, event.FromStatus,
		event.ToStatus, event.EventType, event.ActorType, event.ActorID, event.IdempotencyKey,
		string(event.PayloadJSON), formatStoreTime(event.CreatedAt))
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
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("validate stored transition event: %w", err)
		}
		events = append(events, event.Clone())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transition events: %w", err)
	}
	return events, nil
}

type caseQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
	var attempt PhaseAttempt
	var inputJSON, outputJSON, startedAt string
	var finishedAt sql.NullString
	var durationNanos int64
	err := query.QueryRowContext(ctx, `SELECT id, case_id, cycle_number, phase, mode, status,
		agent_target, bot_key, input_json, output_json, parent_attempt_id, started_at,
		finished_at, error_code, error_message, input_tokens, output_tokens, duration_nanos
		FROM phase_attempts WHERE id = ?`, id).Scan(
		&attempt.ID, &attempt.CaseID, &attempt.CycleNumber, &attempt.Phase, &attempt.Mode,
		&attempt.Status, &attempt.AgentTarget, &attempt.BotKey, &inputJSON, &outputJSON,
		&attempt.ParentAttemptID, &startedAt, &finishedAt, &attempt.ErrorCode,
		&attempt.ErrorMessage, &attempt.Usage.InputTokens, &attempt.Usage.OutputTokens, &durationNanos,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseAttempt{}, fmt.Errorf("phase attempt %q not found", id)
	}
	if err != nil {
		return PhaseAttempt{}, fmt.Errorf("get phase attempt: %w", err)
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
