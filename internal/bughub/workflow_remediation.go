package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRemediationNotApplicable = errors.New("root cause does not use the non-code remediation workflow")

func CompleteRemediationKey(caseID, rootCauseAttemptID string, caseVersion int64) string {
	return fmt.Sprintf("complete-remediation:%s:%s:%d", strings.TrimSpace(caseID), strings.TrimSpace(rootCauseAttemptID), caseVersion)
}

// CompleteRemediation records an operator- or externally-executed non-code
// action and immediately schedules a fresh business regression. Studio does
// not perform the mutation itself: the approval scope is an audit record of
// what was done, by whom, and which evidence the regression is bound to.
func (o *CaseOrchestrator) CompleteRemediation(ctx context.Context, cmd CompleteRemediationCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	if cmd.IdempotencyKey != CompleteRemediationKey(cmd.CaseID, cmd.RootCauseAttemptID, cmd.ExpectedVersion) {
		return IncidentCase{}, ErrApprovalScope
	}
	cmd.Summary = strings.TrimSpace(cmd.Summary)
	cmd.Evidence = strings.TrimSpace(cmd.Evidence)
	if cmd.Summary == "" || cmd.Evidence == "" {
		return IncidentCase{}, errors.New("remediation summary and evidence are required")
	}
	if len(cmd.Summary) > 2000 || len(cmd.Evidence) > 4000 || containsSensitiveData([]byte(cmd.Summary+"\n"+cmd.Evidence)) {
		return IncidentCase{}, errors.New("remediation summary or evidence is unsafe or too large")
	}
	if _, found, err := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	} else if found {
		return o.replayCompleteRemediation(ctx, cmd)
	}

	release := workflowCommandLocks.acquire("complete-remediation:" + cmd.CaseID)
	defer release()
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingRemediation || incident.CurrentAttemptID != cmd.RootCauseAttemptID {
		return IncidentCase{}, ErrApprovalNotReady
	}
	result, err := validatedRootCauseResult(ctx, o.store, incident, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if result.UsesCodeFixWorkflow() {
		return IncidentCase{}, ErrRemediationNotApplicable
	}

	bindingID := stableID("remediation-binding", cmd.IdempotencyKey)
	scope := RemediationApprovalScope{
		RootCauseAttemptID: cmd.RootCauseAttemptID,
		CycleNumber:        incident.CycleNumber,
		RootCauseType:      result.RootCauseType,
		Mode:               result.Remediation.Mode,
		Target:             result.Remediation.Target,
		RecommendedAction:  result.Remediation.Summary,
		Rollback:           result.Remediation.Rollback,
		Verification:       result.Remediation.Verification,
		Summary:            cmd.Summary,
		Evidence:           cmd.Evidence,
		BindingID:          bindingID,
	}
	approval := Approval{ID: stableID("approval", cmd.IdempotencyKey), CaseID: incident.ID, Kind: ApprovalCompleteRemediation, Actor: cmd.ActorID, CaseVersion: incident.Version, ScopeJSON: mustJSON(scope)}
	reservation := DeploymentReservation{
		ReservationID:           stableID("deployment-reservation", cmd.IdempotencyKey),
		ReservationKey:          cmd.IdempotencyKey,
		CallerIdempotencyKey:    cmd.IdempotencyKey,
		ActorID:                 cmd.ActorID,
		OriginalExpectedVersion: cmd.ExpectedVersion,
		CycleNumber:             incident.CycleNumber,
		Environment:             incident.Environment,
		ExpectedCommits:         map[string]string{},
		RemediationBindingID:    bindingID,
		RemediationType:         result.RootCauseType,
		RemediationSummary:      cmd.Summary,
		Bug:                     cmd.Bug,
		Bot:                     cmd.Bot,
		VerifierInput: DeploymentVerificationRequest{
			CaseID:          incident.ID,
			Environment:     incident.Environment,
			ExpectedCommits: map[string]string{},
			Source:          "manual-remediation",
		},
	}
	now := time.Now().UTC()
	observation := DeploymentObservation{
		ID:                 stableID("deployment", reservation.ReservationKey),
		CaseID:             incident.ID,
		Environment:        incident.Environment,
		ExpectedCommits:    map[string]string{},
		UserNotifiedAt:     &now,
		VerificationSource: "manual-remediation",
		ObservedAt:         now,
		DiagnosticCode:     "remediation_completed",
		DiagnosticMessage:  "非代码处置已由操作人确认，等待业务回归验证",
		Result:             DeploymentResultUnavailable,
	}
	payload := mustJSON(reservation)
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: cmd.CaseID, ExpectedVersion: cmd.ExpectedVersion, IdempotencyKey: cmd.IdempotencyKey,
		RequestJSON: mustJSON(cmd), Approvals: []Approval{approval}, Observations: []DeploymentObservation{observation},
		Steps: []CaseMutationStep{{To: CaseRemediationApplied, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "remediation_regression_reserved", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}},
	})
	if err != nil {
		return IncidentCase{}, err
	}
	current, err := o.store.GetCase(ctx, mutation.Case.ID)
	if err != nil {
		return IncidentCase{}, err
	}
	if current.Status == CaseRemediationApplied {
		if _, startErr := o.StartRegression(ctx, current.ID, current.Version); startErr != nil && !errors.Is(startErr, ErrRegressionDuplicate) {
			if waiting, handled, readinessErr := o.failSafeRegressionReadiness(ctx, current, startErr); handled {
				return waiting, readinessErr
			}
			return current, startErr
		}
		current, err = o.store.GetCase(ctx, current.ID)
	}
	return current, err
}

func (o *CaseOrchestrator) replayCompleteRemediation(ctx context.Context, cmd CompleteRemediationCommand) (IncidentCase, error) {
	approvals, err := o.store.ListApprovals(ctx, cmd.CaseID)
	if err != nil {
		return IncidentCase{}, err
	}
	for _, approval := range approvals {
		if approval.Kind != ApprovalCompleteRemediation || approval.Actor != cmd.ActorID || approval.CaseVersion != cmd.ExpectedVersion {
			continue
		}
		var scope RemediationApprovalScope
		if json.Unmarshal(approval.ScopeJSON, &scope) != nil {
			continue
		}
		if scope.RootCauseAttemptID == cmd.RootCauseAttemptID && scope.Summary == cmd.Summary && scope.Evidence == cmd.Evidence {
			return o.store.GetCase(ctx, cmd.CaseID)
		}
	}
	return IncidentCase{}, ErrIdempotencyConflict
}
