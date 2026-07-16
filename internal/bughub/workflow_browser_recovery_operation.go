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

var ErrBrowserRecoveryOutcomeUncertain = errors.New("incident browser recovery outcome is uncertain")

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
	case BrowserRecoveryLogin, BrowserRecoveryRepair:
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
		return BrowserRecoveryOperation{}, false, fmt.Errorf("%w: browser recovery key %q", ErrIdempotencyConflict, request.IdempotencyKey)
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
			return BrowserRecoveryOperation{}, false, fmt.Errorf("%w: browser recovery key %q", ErrIdempotencyConflict, request.IdempotencyKey)
		}
		if err := tx.Commit(); err != nil {
			return BrowserRecoveryOperation{}, false, err
		}
		return existing, false, nil
	}
	var collision string
	if queryErr := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM transition_events WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&collision); queryErr == nil {
		return BrowserRecoveryOperation{}, false, fmt.Errorf("%w: browser recovery key %q is already a Case mutation", ErrIdempotencyConflict, request.IdempotencyKey)
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return BrowserRecoveryOperation{}, false, queryErr
	}
	if queryErr := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM browser_recovery_operations WHERE operation=? AND case_id=? AND attempt_id=?`, request.Operation, request.CaseID, request.AttemptID).Scan(&collision); queryErr == nil {
		return BrowserRecoveryOperation{}, false, fmt.Errorf("%w: browser recovery attempt already has operation %q", ErrIdempotencyConflict, request.Operation)
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return BrowserRecoveryOperation{}, false, queryErr
	}
	attempt, err := getAttempt(ctx, tx, request.AttemptID)
	if err != nil || attempt.CaseID != request.CaseID || attempt.CycleNumber != request.CycleNumber {
		return BrowserRecoveryOperation{}, false, errors.New("browser recovery attempt identity is invalid")
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
	return BrowserRecoveryOperation{}, fmt.Errorf("%w: browser recovery outcome %q", ErrIdempotencyConflict, request.IdempotencyKey)
}

func (s *CaseStore) CompleteBrowserRecoveryOperation(ctx context.Context, request BrowserRecoveryOperationRequest, claimToken string, resultCase IncidentCase) error {
	fingerprint, err := browserRecoveryOperationFingerprint(request)
	if err != nil {
		return err
	}
	if resultCase.ID != request.CaseID || resultCase.CycleNumber != request.CycleNumber || resultCase.Version < request.ExpectedVersion+1 || resultCase.Validate() != nil {
		return errors.New("browser recovery continuation result is invalid")
	}
	encoded, err := json.Marshal(resultCase)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_recovery_operations SET status=?,outcome_code=?,result_case_json=?,updated_at=? WHERE idempotency_key=? AND request_fingerprint=? AND status=? AND claim_token=?`, BrowserRecoveryContinued, "continued", string(encoded), formatStoreTime(time.Now().UTC()), request.IdempotencyKey, fingerprint, BrowserRecoveryEffectSucceeded, claimToken)
	if err != nil {
		return fmt.Errorf("complete browser recovery operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	operation, found, err := s.GetBrowserRecoveryOperation(ctx, request)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return errors.New("browser recovery operation is missing")
	}
	if rows == 1 || operation.Status == BrowserRecoveryContinued && operation.ClaimToken == claimToken && operation.ResultCase.ID == resultCase.ID && operation.ResultCase.Version == resultCase.Version && operation.ResultCase.CurrentAttemptID == resultCase.CurrentAttemptID {
		return nil
	}
	return fmt.Errorf("%w: browser recovery continuation %q", ErrIdempotencyConflict, request.IdempotencyKey)
}
