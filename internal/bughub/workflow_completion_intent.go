package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const completionIntentKind = "phase_completion_intent"
const completionIntentVersion = 1

var ErrCompletionIntentPending = errors.New("phase completion intent is pending")

type completionIntentEnvelope struct {
	Kind    string                 `json:"kind"`
	Version int                    `json:"version"`
	Command CompleteAttemptCommand `json:"command"`
}

func marshalCompletionIntent(command CompleteAttemptCommand) ([]byte, error) {
	if err := validateCompletionCommand(command); err != nil {
		return nil, err
	}
	return json.Marshal(completionIntentEnvelope{Kind: completionIntentKind, Version: completionIntentVersion, Command: command})
}

func parseCompletionIntent(raw json.RawMessage) (CompleteAttemptCommand, bool, error) {
	var marker struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		if bytes.Contains(raw, []byte(completionIntentKind)) {
			return CompleteAttemptCommand{}, true, fmt.Errorf("decode completion intent marker: %w", err)
		}
		return CompleteAttemptCommand{}, false, nil
	}
	if marker.Kind != completionIntentKind {
		return CompleteAttemptCommand{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope completionIntentEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return CompleteAttemptCommand{}, true, fmt.Errorf("decode completion intent: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return CompleteAttemptCommand{}, true, errors.New("completion intent must contain one JSON object")
	}
	if envelope.Version != completionIntentVersion {
		return CompleteAttemptCommand{}, true, fmt.Errorf("unsupported completion intent version %d", envelope.Version)
	}
	if err := validateCompletionCommand(envelope.Command); err != nil {
		return CompleteAttemptCommand{}, true, fmt.Errorf("validate completion intent: %w", err)
	}
	return envelope.Command, true, nil
}

func validateCompletionCommand(command CompleteAttemptCommand) error {
	if err := validateCommand(command.CaseID, command.ExpectedVersion, command.IdempotencyKey, command.ActorID); err != nil {
		return err
	}
	if command.AttemptID == "" {
		return errors.New("completion attempt ID is required")
	}
	switch command.Outcome {
	case PhaseOutcomeReproduced, PhaseOutcomeNotReproduced, PhaseOutcomeNeedsEvidence, PhaseOutcomeSystemFailed,
		PhaseOutcomeRootCauseReady, PhaseOutcomeFixPushed, PhaseOutcomeFixFailed,
		PhaseOutcomeFixedVerified, PhaseOutcomeStillReproduces:
	default:
		return fmt.Errorf("unsupported completion outcome %q", command.Outcome)
	}
	if command.Outcome == PhaseOutcomeSystemFailed && !strings.HasPrefix(strings.TrimSpace(command.ErrorCode), "browser_") {
		return errors.New("system-failed completion requires a browser error code")
	}
	if err := validateJSONObject("completion output", command.OutputJSON, true); err != nil {
		return err
	}
	if err := command.Usage.Validate(); err != nil {
		return err
	}
	for _, change := range command.CodeChanges {
		if err := change.Validate(); err != nil {
			return err
		}
		if change.CaseID != command.CaseID || change.AttemptID != command.AttemptID {
			return errors.New("completion code change is not bound to command Case and attempt")
		}
	}
	return validateFixCompletionPayload(command)
}

func (s *CaseStore) SaveCompletionIntentIfRunning(ctx context.Context, command CompleteAttemptCommand) error {
	encoded, err := marshalCompletionIntent(command)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin completion intent save: %w", err)
	}
	defer tx.Rollback()
	var caseID, status, existing string
	var phase Phase
	if err := tx.QueryRowContext(ctx, `SELECT case_id,status,output_json,phase FROM phase_attempts WHERE id=?`, command.AttemptID).Scan(&caseID, &status, &existing, &phase); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCaseNotFound
		}
		return err
	}
	if caseID != command.CaseID {
		return errors.New("completion intent attempt belongs to a different Case")
	}
	if err := validateCompletionAttemptPhase(phase, command); err != nil {
		return err
	}
	if AttemptStatus(status) != AttemptStatusQueued && AttemptStatus(status) != AttemptStatusRunning {
		return fmt.Errorf("completion intent requires a runnable attempt, got %s", status)
	}
	if !isEmptyJSONObject([]byte(existing)) {
		stored, found, parseErr := parseCompletionIntent([]byte(existing))
		if parseErr != nil {
			return parseErr
		}
		if !found {
			return ErrIdempotencyConflict
		}
		storedEncoded, _ := marshalCompletionIntent(stored)
		if !bytes.Equal(storedEncoded, encoded) {
			return ErrIdempotencyConflict
		}
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE phase_attempts SET output_json=? WHERE id=? AND case_id=? AND status IN (?,?) AND output_json=?`, string(encoded), command.AttemptID, command.CaseID, AttemptStatusQueued, AttemptStatusRunning, existing)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ErrAttemptAlreadyFinished
	}
	return tx.Commit()
}

func isEmptyJSONObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
}

func equivalentCompletionCommands(left, right CompleteAttemptCommand) bool {
	left.ExpectedVersion = 0
	right.ExpectedVersion = 0
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func completionCommandIdentity(command CompleteAttemptCommand) (string, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
