package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxRemediationProposalBytes = 4000

// ReconsiderRemediationCommand asks the investigation agent to assess an
// operator-proposed remediation without granting any mutation permission.
type ReconsiderRemediationCommand struct {
	CaseID             string
	ExpectedVersion    int64
	IdempotencyKey     string
	ActorID            string
	RootCauseAttemptID string
	Proposal           string
	Bug                Bug
	Bot                BotRef
}

type remediationReassessmentInput struct {
	Kind                     string              `json:"kind"`
	Proposal                 string              `json:"proposal"`
	SourceRootCauseAttemptID string              `json:"source_root_cause_attempt_id"`
	PreviousResult           InvestigationResult `json:"previous_result"`
}

type remediationReassessmentEnvelope struct {
	RemediationReassessment remediationReassessmentInput `json:"remediation_reassessment"`
}

func ReconsiderRemediationKey(caseID, rootCauseAttemptID string, caseVersion int64) string {
	return fmt.Sprintf("reconsider-remediation:%s:%s:%d", strings.TrimSpace(caseID), strings.TrimSpace(rootCauseAttemptID), caseVersion)
}

func (o *CaseOrchestrator) ReconsiderRemediation(ctx context.Context, cmd ReconsiderRemediationCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	cmd.CaseID = strings.TrimSpace(cmd.CaseID)
	cmd.ActorID = strings.TrimSpace(cmd.ActorID)
	cmd.RootCauseAttemptID = strings.TrimSpace(cmd.RootCauseAttemptID)
	cmd.Proposal = strings.TrimSpace(cmd.Proposal)
	if cmd.IdempotencyKey != ReconsiderRemediationKey(cmd.CaseID, cmd.RootCauseAttemptID, cmd.ExpectedVersion) {
		return IncidentCase{}, ErrApprovalScope
	}
	if cmd.RootCauseAttemptID == "" {
		return IncidentCase{}, errors.New("root cause attempt ID is required")
	}
	if cmd.Proposal == "" {
		return IncidentCase{}, errors.New("remediation proposal is required")
	}
	if len([]byte(cmd.Proposal)) > maxRemediationProposalBytes {
		return IncidentCase{}, errors.New("remediation proposal is too large")
	}
	if containsSensitiveData([]byte(cmd.Proposal)) {
		return IncidentCase{}, errors.New("remediation proposal contains sensitive data")
	}

	if _, found, err := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	} else if found {
		return o.replayRemediationReassessment(ctx, cmd)
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingFixApproval {
		return IncidentCase{}, ErrApprovalNotReady
	}
	previous, err := validatedRootCauseResult(ctx, o.store, incident, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	root, err := o.store.GetAttempt(ctx, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if root.BotKey != cmd.Bot.Key || root.AgentTarget != cmd.Bot.Target {
		return IncidentCase{}, ErrApprovalScope
	}
	attempt, mutation, err := buildRemediationReassessmentMutation(cmd, incident.CycleNumber, root.InputJSON, previous)
	if err != nil {
		return IncidentCase{}, err
	}
	result, err := o.store.ApplyCaseMutation(ctx, mutation)
	if err != nil {
		return IncidentCase{}, err
	}
	if result.Replay {
		return result.Case, nil
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, result.Case, attempt, cmd.IdempotencyKey, errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(attempt, cmd.Bug, cmd.Bot); err != nil {
		return o.phaseScheduleFailure(ctx, result.Case, attempt, cmd.IdempotencyKey, err)
	}
	return result.Case, nil
}

func buildRemediationReassessmentMutation(cmd ReconsiderRemediationCommand, cycleNumber int, sourceInput json.RawMessage, previous InvestigationResult) (PhaseAttempt, CaseMutation, error) {
	incident := IncidentCase{ID: cmd.CaseID, CycleNumber: cycleNumber}
	input, err := canonicalJSONObject(sourceInput)
	if err != nil {
		return PhaseAttempt{}, CaseMutation{}, fmt.Errorf("invalid root-cause evidence handoff: %w", err)
	}
	var inputObject map[string]any
	if err := json.Unmarshal(input, &inputObject); err != nil {
		return PhaseAttempt{}, CaseMutation{}, err
	}
	inputObject["remediation_reassessment"] = remediationReassessmentInput{
		Kind:                     "user_remediation_proposal",
		Proposal:                 cmd.Proposal,
		SourceRootCauseAttemptID: cmd.RootCauseAttemptID,
		PreviousResult:           previous,
	}
	input, err = json.Marshal(inputObject)
	if err != nil {
		return PhaseAttempt{}, CaseMutation{}, err
	}
	attempt := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey, cmd.Bot, input, cmd.RootCauseAttemptID)
	proposalDigest := sha256.Sum256([]byte(cmd.Proposal))
	payload := mustJSON(map[string]string{
		"attempt_id":                   attempt.ID,
		"source_root_cause_attempt_id": cmd.RootCauseAttemptID,
		"proposal_sha256":              hex.EncodeToString(proposalDigest[:]),
	})
	request := mustJSON(map[string]any{
		"case_id":               cmd.CaseID,
		"expected_version":      cmd.ExpectedVersion,
		"idempotency_key":       cmd.IdempotencyKey,
		"actor_id":              cmd.ActorID,
		"root_cause_attempt_id": cmd.RootCauseAttemptID,
		"proposal_sha256":       hex.EncodeToString(proposalDigest[:]),
		"bot_key":               cmd.Bot.Key,
		"agent_target":          cmd.Bot.Target,
	})
	mutation := CaseMutation{
		CaseID:          cmd.CaseID,
		ExpectedVersion: cmd.ExpectedVersion,
		IdempotencyKey:  cmd.IdempotencyKey,
		RequestJSON:     request,
		CreateAttempts:  []PhaseAttempt{attempt},
		Snapshot:        CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(attempt.ID)},
		Steps: []CaseMutationStep{{To: CaseInvestigating, Event: TransitionEvent{
			ID:          stableID("event", cmd.IdempotencyKey),
			EventType:   "remediation_reassessment_requested",
			ActorType:   "user",
			ActorID:     cmd.ActorID,
			PayloadJSON: payload,
		}}},
	}
	return attempt, mutation, nil
}

func (o *CaseOrchestrator) replayRemediationReassessment(ctx context.Context, cmd ReconsiderRemediationCommand) (IncidentCase, error) {
	root, err := o.store.GetAttempt(ctx, cmd.RootCauseAttemptID)
	if err != nil || root.CaseID != cmd.CaseID || root.Phase != PhaseInvestigation || root.Status != AttemptStatusSucceeded || root.BotKey != cmd.Bot.Key || root.AgentTarget != cmd.Bot.Target {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	previous, err := ParseInvestigationResult(root.OutputJSON)
	if err != nil || previous.InvestigationStatus != "root_cause_ready" {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	_, mutation, err := buildRemediationReassessmentMutation(cmd, root.CycleNumber, root.InputJSON, previous)
	if err != nil {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	result, err := o.store.ApplyCaseMutation(ctx, mutation)
	if err != nil {
		return IncidentCase{}, err
	}
	if !result.Replay {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	return result.Case, nil
}

func remediationReassessmentFromInput(raw json.RawMessage) (remediationReassessmentInput, bool) {
	var envelope remediationReassessmentEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return remediationReassessmentInput{}, false
	}
	input := envelope.RemediationReassessment
	if input.Kind != "user_remediation_proposal" || strings.TrimSpace(input.Proposal) == "" || strings.TrimSpace(input.SourceRootCauseAttemptID) == "" {
		return remediationReassessmentInput{}, false
	}
	return input, true
}
