package bughub

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ConfirmValidationCommand accepts a reproduced validation result and starts
// investigation from its frozen evidence. A reproduced result is deliberately
// not auto-advanced so the operator can correct a misunderstood scenario.
type ConfirmValidationCommand struct {
	CaseID              string
	ExpectedVersion     int64
	IdempotencyKey      string
	ActorID             string
	ValidationAttemptID string
	Bug                 Bug
	Bot                 BotRef
}

func ConfirmValidationKey(caseID, validationAttemptID string, caseVersion int64) string {
	return fmt.Sprintf("confirm-validation:%s:%s:%d", strings.TrimSpace(caseID), strings.TrimSpace(validationAttemptID), caseVersion)
}

func ReviseValidationKey(caseID, validationAttemptID string, caseVersion int64) string {
	return fmt.Sprintf("revise-validation:%s:%s:%d", strings.TrimSpace(caseID), strings.TrimSpace(validationAttemptID), caseVersion)
}

func (o *CaseOrchestrator) ConfirmValidation(ctx context.Context, cmd ConfirmValidationCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	cmd.CaseID = strings.TrimSpace(cmd.CaseID)
	cmd.ActorID = strings.TrimSpace(cmd.ActorID)
	cmd.ValidationAttemptID = strings.TrimSpace(cmd.ValidationAttemptID)
	if cmd.ValidationAttemptID == "" {
		return IncidentCase{}, errors.New("validation attempt ID is required")
	}
	if cmd.IdempotencyKey != ConfirmValidationKey(cmd.CaseID, cmd.ValidationAttemptID, cmd.ExpectedVersion) {
		return IncidentCase{}, ErrApprovalScope
	}

	event, replay, err := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if replay && event.EventType != "validation_confirmed" {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	validation, err := o.store.GetAttempt(ctx, cmd.ValidationAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if validation.CaseID != cmd.CaseID || validation.Phase != PhaseValidation || validation.Mode != AttemptReproduce ||
		validation.Status != AttemptStatusSucceeded || validation.BotKey != cmd.Bot.Key || validation.AgentTarget != cmd.Bot.Target {
		return IncidentCase{}, ErrApprovalScope
	}
	if !replay && (incident.Status != CaseReproduced || incident.CurrentAttemptID != validation.ID || incident.CycleNumber != validation.CycleNumber) {
		return IncidentCase{}, ErrApprovalNotReady
	}
	result, err := ParseValidationResult(validation.OutputJSON)
	if err != nil || result.VerificationStatus != "reproduced" {
		return IncidentCase{}, ErrApprovalScope
	}
	investigationInput, err := o.buildInitialInvestigationInput(ctx, validation, validation.OutputJSON)
	if err != nil {
		return IncidentCase{}, err
	}
	investigationInput, err = o.carryRootCauseDisputeAfterValidationRefresh(ctx, validation, investigationInput)
	if err != nil {
		return IncidentCase{}, err
	}
	// Rebuild the original deterministic mutation on replay even if the live
	// Case has since advanced to another cycle.
	incident.CycleNumber = validation.CycleNumber
	attempt := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", cmd.Bot, investigationInput, validation.ID)
	payload := mustJSON(map[string]string{
		"validation_attempt_id":    validation.ID,
		"investigation_attempt_id": attempt.ID,
	})
	return o.beginPhaseWithUpdateAndPayload(ctx, incident, CaseInvestigating, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "validation_confirmed", CaseSnapshotUpdate{}, payload)
}
