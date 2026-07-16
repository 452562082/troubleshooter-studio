package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BrowserRecoveryOperationKind string

const (
	BrowserRecoveryLogin  BrowserRecoveryOperationKind = "login"
	BrowserRecoveryRepair BrowserRecoveryOperationKind = "repair"
)

type BrowserRecoveryOperationStatus string

const (
	BrowserRecoveryClaimed          BrowserRecoveryOperationStatus = "claimed"
	BrowserRecoveryEffectSucceeded  BrowserRecoveryOperationStatus = "effect_succeeded"
	BrowserRecoveryOutcomeUncertain BrowserRecoveryOperationStatus = "outcome_uncertain"
	BrowserRecoveryContinued        BrowserRecoveryOperationStatus = "continued"
)

var (
	ErrBrowserRecoveryOutcomeUncertain = errors.New("incident browser recovery outcome is uncertain")
	ErrBrowserRecoveryReserved         = errors.New("incident browser recovery reserves this Case")
	ErrBrowserRecoveryNotEligible      = errors.New("incident browser recovery is not eligible")
)

type BrowserRecoveryOperationRequest struct {
	Operation         BrowserRecoveryOperationKind
	CaseID            string
	AttemptID         string
	ExpectedErrorCode string
	CycleNumber       int
	ExpectedVersion   int64
	ActorID           string
	IdempotencyKey    string
}

type BrowserRecoveryOperation struct {
	BrowserRecoveryOperationRequest
	RequestFingerprint string
	Status             BrowserRecoveryOperationStatus
	ClaimToken         string
	OutcomeCode        string
	ResultCase         IncidentCase
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type browserRecoveryMutationConsume struct {
	Request            BrowserRecoveryOperationRequest
	RequestFingerprint string
	ClaimToken         string
}

type browserRecoveryContinuationMarker struct {
	Operation          BrowserRecoveryOperationKind `json:"operation"`
	BlockedAttemptID   string                       `json:"blocked_attempt_id"`
	ExpectedErrorCode  string                       `json:"expected_browser_error_code"`
	RequestFingerprint string                       `json:"request_fingerprint"`
}

func browserRecoveryOperationFingerprint(request BrowserRecoveryOperationRequest) (string, error) {
	if err := validateBrowserRecoveryOperationRequest(request); err != nil {
		return "", err
	}
	identity := struct {
		Operation         BrowserRecoveryOperationKind `json:"operation"`
		CaseID            string                       `json:"case_id"`
		AttemptID         string                       `json:"attempt_id"`
		ExpectedErrorCode string                       `json:"expected_browser_error_code"`
		CycleNumber       int                          `json:"cycle_number"`
		ExpectedVersion   int64                        `json:"expected_version"`
		ActorID           string                       `json:"actor_id"`
		IdempotencyKey    string                       `json:"idempotency_key"`
	}{request.Operation, request.CaseID, request.AttemptID, request.ExpectedErrorCode, request.CycleNumber, request.ExpectedVersion, request.ActorID, request.IdempotencyKey}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode browser recovery fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateBrowserRecoveryOperationRequest(request BrowserRecoveryOperationRequest) error {
	switch request.Operation {
	case BrowserRecoveryLogin:
		if request.ExpectedErrorCode != "browser_login_required" {
			return errors.New("browser login recovery error code is invalid")
		}
	case BrowserRecoveryRepair:
		if request.ExpectedErrorCode != "browser_runtime_broken" {
			return errors.New("browser runtime recovery error code is invalid")
		}
	default:
		return errors.New("browser recovery operation is invalid")
	}
	if blank(request.CaseID) || blank(request.AttemptID) || blank(request.ExpectedErrorCode) || blank(request.ActorID) || blank(request.IdempotencyKey) || request.CycleNumber < 1 || request.ExpectedVersion < 1 {
		return errors.New("browser recovery operation identity is incomplete")
	}
	if !strings.HasPrefix(request.ExpectedErrorCode, "browser_") {
		return errors.New("browser recovery expected error code is invalid")
	}
	return nil
}

func scanBrowserRecoveryOperation(scanner rowScanner) (BrowserRecoveryOperation, bool, error) {
	var operation BrowserRecoveryOperation
	var createdAt, updatedAt, resultJSON string
	err := scanner.Scan(
		&operation.IdempotencyKey, &operation.Operation, &operation.CaseID, &operation.AttemptID,
		&operation.ExpectedErrorCode, &operation.CycleNumber, &operation.ExpectedVersion, &operation.ActorID,
		&operation.RequestFingerprint, &operation.Status, &operation.ClaimToken, &operation.OutcomeCode,
		&resultJSON, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserRecoveryOperation{}, false, nil
	}
	if err != nil {
		return BrowserRecoveryOperation{}, false, err
	}
	if err := validateBrowserRecoveryOperationRequest(operation.BrowserRecoveryOperationRequest); err != nil {
		return BrowserRecoveryOperation{}, false, errors.New("stored browser recovery identity is invalid")
	}
	fingerprint, err := browserRecoveryOperationFingerprint(operation.BrowserRecoveryOperationRequest)
	if err != nil || fingerprint != operation.RequestFingerprint {
		return BrowserRecoveryOperation{}, false, errors.New("stored browser recovery fingerprint is invalid")
	}
	operation.CreatedAt, err = parseStoreTime(createdAt)
	if err != nil {
		return BrowserRecoveryOperation{}, false, err
	}
	operation.UpdatedAt, err = parseStoreTime(updatedAt)
	if err != nil {
		return BrowserRecoveryOperation{}, false, err
	}
	switch operation.Status {
	case BrowserRecoveryClaimed:
		if operation.ClaimToken == "" || operation.OutcomeCode != "" || resultJSON != "{}" {
			return BrowserRecoveryOperation{}, false, errors.New("claimed browser recovery operation is invalid")
		}
	case BrowserRecoveryEffectSucceeded:
		if operation.ClaimToken == "" || operation.OutcomeCode != "succeeded" || resultJSON != "{}" {
			return BrowserRecoveryOperation{}, false, errors.New("successful browser recovery effect is invalid")
		}
	case BrowserRecoveryOutcomeUncertain:
		if operation.ClaimToken == "" || operation.OutcomeCode != "unknown" || resultJSON != "{}" {
			return BrowserRecoveryOperation{}, false, errors.New("uncertain browser recovery operation is invalid")
		}
	case BrowserRecoveryContinued:
		if operation.ClaimToken == "" || operation.OutcomeCode != "continued" || resultJSON == "{}" || json.Unmarshal([]byte(resultJSON), &operation.ResultCase) != nil || operation.ResultCase.ID != operation.CaseID || operation.ResultCase.CycleNumber != operation.CycleNumber || operation.ResultCase.Version < operation.ExpectedVersion+1 || operation.ResultCase.Validate() != nil {
			return BrowserRecoveryOperation{}, false, errors.New("continued browser recovery operation is invalid")
		}
	default:
		return BrowserRecoveryOperation{}, false, errors.New("browser recovery operation status is invalid")
	}
	return operation, true, nil
}

const browserRecoveryOperationSelect = `SELECT idempotency_key,operation,case_id,attempt_id,expected_error_code,cycle_number,expected_version,actor_id,request_fingerprint,status,claim_token,outcome_code,result_case_json,created_at,updated_at FROM browser_recovery_operations`

func getBrowserRecoveryOperation(ctx context.Context, query caseQuery, idempotencyKey string) (BrowserRecoveryOperation, bool, error) {
	return scanBrowserRecoveryOperation(query.QueryRowContext(ctx, browserRecoveryOperationSelect+` WHERE idempotency_key=?`, idempotencyKey))
}

func (s *CaseStore) GetBrowserRecoveryOperation(ctx context.Context, request BrowserRecoveryOperationRequest) (BrowserRecoveryOperation, bool, error) {
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil || s == nil || s.db == nil {
		if err != nil {
			return BrowserRecoveryOperation{}, false, err
		}
		return BrowserRecoveryOperation{}, false, errors.New("browser recovery operation store is required")
	}
	operation, found, err := getBrowserRecoveryOperation(ctx, s.db, request.IdempotencyKey)
	if err != nil || !found {
		return operation, found, err
	}
	if operation.RequestFingerprint != fingerprint {
		return BrowserRecoveryOperation{}, false, ErrIdempotencyConflict
	}
	return operation, true, nil
}

func (s *CaseStore) ClaimBrowserRecoveryOperation(ctx context.Context, request BrowserRecoveryOperationRequest, claimToken string) (BrowserRecoveryOperation, bool, error) {
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil {
		return BrowserRecoveryOperation{}, false, err
	}
	if s == nil || s.db == nil || blank(claimToken) {
		return BrowserRecoveryOperation{}, false, errors.New("browser recovery operation store and claim token are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserRecoveryOperation{}, false, fmt.Errorf("begin browser recovery claim: %w", err)
	}
	defer tx.Rollback()
	if existing, found, loadErr := getBrowserRecoveryOperation(ctx, tx, request.IdempotencyKey); loadErr != nil {
		return BrowserRecoveryOperation{}, false, loadErr
	} else if found {
		if existing.RequestFingerprint != fingerprint {
			return BrowserRecoveryOperation{}, false, ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return BrowserRecoveryOperation{}, false, err
		}
		return existing, false, nil
	}
	var collision string
	if queryErr := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM transition_events WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&collision); queryErr == nil {
		return BrowserRecoveryOperation{}, false, ErrIdempotencyConflict
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return BrowserRecoveryOperation{}, false, queryErr
	}
	if queryErr := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM browser_recovery_operations WHERE operation=? AND case_id=? AND attempt_id=?`, request.Operation, request.CaseID, request.AttemptID).Scan(&collision); queryErr == nil {
		return BrowserRecoveryOperation{}, false, ErrIdempotencyConflict
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return BrowserRecoveryOperation{}, false, queryErr
	}
	attempt, err := getAttempt(ctx, tx, request.AttemptID)
	if err != nil || !browserRecoveryAttemptEligible(attempt, request) {
		return BrowserRecoveryOperation{}, false, ErrBrowserRecoveryNotEligible
	}
	incident, err := getCase(ctx, tx, request.CaseID)
	if err != nil || incident.Status != CaseWaitingEvidence || incident.Version != request.ExpectedVersion || incident.CurrentAttemptID != request.AttemptID || incident.CycleNumber != request.CycleNumber {
		return BrowserRecoveryOperation{}, false, ErrBrowserRecoveryNotEligible
	}
	now := formatStoreTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_recovery_operations (idempotency_key,operation,case_id,attempt_id,expected_error_code,cycle_number,expected_version,actor_id,request_fingerprint,status,claim_token,outcome_code,result_case_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.IdempotencyKey, request.Operation, request.CaseID, request.AttemptID, request.ExpectedErrorCode, request.CycleNumber, request.ExpectedVersion, request.ActorID, fingerprint, BrowserRecoveryClaimed, claimToken, "", `{}`, now, now); err != nil {
		return BrowserRecoveryOperation{}, false, fmt.Errorf("insert browser recovery operation: %w", err)
	}
	operation, found, err := getBrowserRecoveryOperation(ctx, tx, request.IdempotencyKey)
	if err != nil || !found || operation.RequestFingerprint != fingerprint || operation.ClaimToken != claimToken {
		if err != nil {
			return BrowserRecoveryOperation{}, false, err
		}
		return BrowserRecoveryOperation{}, false, errors.New("browser recovery claim was not persisted")
	}
	if err := tx.Commit(); err != nil {
		return BrowserRecoveryOperation{}, false, err
	}
	return operation, true, nil
}

func (s *CaseStore) RecordBrowserRecoveryOutcome(ctx context.Context, request BrowserRecoveryOperationRequest, claimToken string, status BrowserRecoveryOperationStatus) (BrowserRecoveryOperation, error) {
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil {
		return BrowserRecoveryOperation{}, err
	}
	outcome := ""
	switch status {
	case BrowserRecoveryEffectSucceeded:
		outcome = "succeeded"
	case BrowserRecoveryOutcomeUncertain:
		outcome = "unknown"
	default:
		return BrowserRecoveryOperation{}, errors.New("browser recovery outcome status is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_recovery_operations SET status=?,outcome_code=?,updated_at=? WHERE idempotency_key=? AND request_fingerprint=? AND status=? AND claim_token=?`, status, outcome, formatStoreTime(time.Now().UTC()), request.IdempotencyKey, fingerprint, BrowserRecoveryClaimed, claimToken)
	if err != nil {
		return BrowserRecoveryOperation{}, fmt.Errorf("record browser recovery outcome: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return BrowserRecoveryOperation{}, err
	}
	operation, found, err := s.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found {
		if err != nil {
			return BrowserRecoveryOperation{}, err
		}
		return BrowserRecoveryOperation{}, errors.New("browser recovery operation is missing")
	}
	if rows == 1 || operation.Status == status && operation.ClaimToken == claimToken {
		return operation, nil
	}
	return BrowserRecoveryOperation{}, ErrIdempotencyConflict
}

func browserRecoveryAttemptEligible(attempt PhaseAttempt, request BrowserRecoveryOperationRequest) bool {
	if attempt.CaseID != request.CaseID || attempt.CycleNumber != request.CycleNumber || attempt.Status != AttemptStatusFailed || attempt.FinishedAt == nil || attempt.ErrorCode != request.ExpectedErrorCode {
		return false
	}
	switch attempt.Phase {
	case PhaseValidation:
		return attempt.Mode == AttemptReproduce
	case PhaseRegression:
		return attempt.Mode == AttemptRegression
	default:
		return false
	}
}

func rejectUnresolvedBrowserRecovery(ctx context.Context, query caseQuery, caseID string) error {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM browser_recovery_operations WHERE case_id=? AND status IN (?,?,?) LIMIT 1`, caseID, BrowserRecoveryClaimed, BrowserRecoveryEffectSucceeded, BrowserRecoveryOutcomeUncertain).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrBrowserRecoveryReserved
}

func newBrowserRecoveryMutationConsume(mutation CaseMutation, request BrowserRecoveryOperationRequest, claimToken string) (*browserRecoveryMutationConsume, error) {
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil {
		return nil, err
	}
	if blank(claimToken) || mutation.CaseID != request.CaseID || mutation.ExpectedVersion != request.ExpectedVersion || mutation.IdempotencyKey != request.IdempotencyKey {
		return nil, ErrIdempotencyConflict
	}
	return &browserRecoveryMutationConsume{Request: request, RequestFingerprint: fingerprint, ClaimToken: claimToken}, nil
}

func loadBrowserRecoveryMutationConsume(ctx context.Context, query caseQuery, consume *browserRecoveryMutationConsume) (BrowserRecoveryOperation, error) {
	if consume == nil {
		return BrowserRecoveryOperation{}, errors.New("browser recovery consume identity is required")
	}
	operation, found, err := getBrowserRecoveryOperation(ctx, query, consume.Request.IdempotencyKey)
	if err != nil {
		return BrowserRecoveryOperation{}, err
	}
	if !found || operation.RequestFingerprint != consume.RequestFingerprint || operation.ClaimToken != consume.ClaimToken {
		return BrowserRecoveryOperation{}, ErrIdempotencyConflict
	}
	return operation, nil
}

func validateBrowserRecoveryContinuationMutation(mutation CaseMutation, request BrowserRecoveryOperationRequest, blocked PhaseAttempt) error {
	if !browserRecoveryAttemptEligible(blocked, request) || len(mutation.Steps) != 1 || len(mutation.CreateAttempts) != 1 || len(mutation.FinishAttempts) != 0 || len(mutation.Approvals) != 0 || len(mutation.CodeChanges) != 0 || len(mutation.Observations) != 0 || len(mutation.ExpectedAttemptOutputs) != 0 || mutation.CompletionAttemptID != "" || mutation.CompletionIdentitySHA256 != "" || mutation.DeleteFixCheckpointAttemptID != "" {
		return ErrIdempotencyConflict
	}
	child := mutation.CreateAttempts[0]
	step := mutation.Steps[0]
	expectedStatus := CaseValidating
	if blocked.Phase == PhaseRegression {
		expectedStatus = CaseRegressionValidating
	}
	if child.ID != stableID("attempt", request.IdempotencyKey) || child.CaseID != request.CaseID || child.CycleNumber != request.CycleNumber || child.ParentAttemptID != request.AttemptID || child.Phase != blocked.Phase || child.Mode != blocked.Mode || child.Status != AttemptStatusRunning || mutation.Snapshot.CurrentAttemptID == nil || *mutation.Snapshot.CurrentAttemptID != child.ID || mutation.Snapshot.CycleNumber != nil || mutation.Snapshot.ClosedAtSet || step.To != expectedStatus || step.AuditOnly || step.Event.ID != stableID("event", request.IdempotencyKey) || step.Event.EventType != "evidence_continued" || step.Event.ActorType != "user" || step.Event.ActorID != request.ActorID || step.Event.IdempotencyKey != "" && step.Event.IdempotencyKey != request.IdempotencyKey {
		return ErrIdempotencyConflict
	}
	markerJSON := child.InputJSON
	if blocked.Phase == PhaseRegression {
		var regression RegressionValidationInput
		if json.Unmarshal(child.InputJSON, &regression) != nil {
			return ErrIdempotencyConflict
		}
		markerJSON = regression.SupplementalEvidence
	}
	var envelope struct {
		BrowserRecovery browserRecoveryContinuationMarker `json:"browser_recovery"`
	}
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil || json.Unmarshal(markerJSON, &envelope) != nil || envelope.BrowserRecovery.Operation != request.Operation || envelope.BrowserRecovery.BlockedAttemptID != request.AttemptID || envelope.BrowserRecovery.ExpectedErrorCode != request.ExpectedErrorCode || envelope.BrowserRecovery.RequestFingerprint != fingerprint {
		return ErrIdempotencyConflict
	}
	return nil
}

func consumeBrowserRecoveryOperationTx(ctx context.Context, tx *sql.Tx, consume *browserRecoveryMutationConsume, resultCase IncidentCase) error {
	encoded, err := json.Marshal(resultCase)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE browser_recovery_operations SET status=?,outcome_code=?,result_case_json=?,updated_at=? WHERE idempotency_key=? AND request_fingerprint=? AND status=? AND claim_token=?`, BrowserRecoveryContinued, "continued", string(encoded), formatStoreTime(time.Now().UTC()), consume.Request.IdempotencyKey, consume.RequestFingerprint, BrowserRecoveryEffectSucceeded, consume.ClaimToken)
	if err != nil {
		return fmt.Errorf("consume browser recovery continuation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	operation, err := loadBrowserRecoveryMutationConsume(ctx, tx, consume)
	if err != nil {
		return err
	}
	if operation.Status == BrowserRecoveryContinued && operation.ResultCase.ID == resultCase.ID && operation.ResultCase.Version == resultCase.Version && operation.ResultCase.CurrentAttemptID == resultCase.CurrentAttemptID {
		return nil
	}
	return ErrIdempotencyConflict
}
