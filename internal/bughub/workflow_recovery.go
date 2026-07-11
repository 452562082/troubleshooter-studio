package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// RecoverInterrupted is a synchronous startup pass. It never leaves recovery
// goroutines behind: every external inspection or runner schedule completes
// before this method returns.
func (o *CaseOrchestrator) RecoverInterrupted(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o == nil || o.store == nil {
		return errors.New("case orchestrator store is required")
	}
	attempts, err := o.store.ListAttempts(ctx, AttemptFilter{Statuses: []AttemptStatus{AttemptStatusRunning}})
	if err != nil {
		return err
	}
	var recoveredErr error
	processedCases := make(map[string]struct{})
	for _, attempt := range attempts {
		if _, scheduledHere := o.recoveryStarted[attempt.ID]; scheduledHere {
			continue
		}
		if err := o.recoverAttempt(ctx, attempt); err != nil {
			recoveredErr = errors.Join(recoveredErr, fmt.Errorf("recover attempt %s: %w", attempt.ID, err))
		}
		processedCases[attempt.CaseID] = struct{}{}
	}
	cases, err := o.store.ListCases(ctx)
	if err != nil {
		return errors.Join(recoveredErr, err)
	}
	for _, incident := range cases {
		if incident.Status != CaseMerging {
			continue
		}
		if _, alreadyProcessed := processedCases[incident.ID]; alreadyProcessed {
			continue
		}
		if err := o.recoverMergeWithoutAttempt(ctx, incident); err != nil {
			recoveredErr = errors.Join(recoveredErr, fmt.Errorf("recover merging case %s: %w", incident.ID, err))
		}
	}
	return recoveredErr
}

func (o *CaseOrchestrator) recoverMergeWithoutAttempt(ctx context.Context, incident IncidentCase) error {
	approvals, err := o.store.ListApprovals(ctx, incident.ID)
	if err != nil {
		return err
	}
	for index := len(approvals) - 1; index >= 0; index-- {
		approval := approvals[index]
		if approval.Kind != ApprovalMergeEnvironmentBranch {
			continue
		}
		request := MergeRequest{CaseID: incident.ID, FixCommits: CloneStringMap(approval.FixCommits), TargetBranches: CloneStringMap(approval.TargetBranches)}
		return o.inspectInterruptedMerge(ctx, incident, request, "recovery:"+incident.ID+":merge")
	}
	_, _, err = o.transition(ctx, incident, CaseMergeConflict, "recovery:"+incident.ID+":merge:no-approval", "recovery", "merge_recovery_failed", map[string]string{"error": "merge approval is missing"}, CaseSnapshotUpdate{})
	return err
}

func (o *CaseOrchestrator) recoverAttempt(ctx context.Context, attempt PhaseAttempt) error {
	incident, err := o.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	if incident.CurrentAttemptID != attempt.ID {
		if CanTransition(incident.Status, statusForPhase(attempt.Phase)) {
			return o.recoverPreparedAttempt(ctx, incident, attempt)
		}
		return o.interruptDetachedAttempt(ctx, attempt)
	}
	attempt.Status = AttemptStatusInterrupted
	attempt.OutputJSON = []byte(`{}`)
	attempt.ErrorCode = "studio_restarted"
	attempt.ErrorMessage = "phase process was interrupted by Studio restart"
	if err := o.store.FinishAttempt(ctx, attempt); err != nil {
		if errors.Is(err, ErrAttemptAlreadyFinished) {
			return nil
		}
		return err
	}
	switch incident.Status {
	case CaseValidating, CaseInvestigating:
		return o.recoverReadOnly(ctx, incident, attempt, true)
	case CaseRegressionValidating:
		matched, err := o.latestDeploymentMatched(ctx, incident.ID)
		if err != nil {
			return err
		}
		return o.recoverReadOnly(ctx, incident, attempt, matched)
	case CaseFixing:
		return o.recoverFix(ctx, incident, attempt)
	case CaseMerging:
		return o.recoverMerge(ctx, incident, attempt)
	default:
		return nil
	}
}

func (o *CaseOrchestrator) recoverPreparedAttempt(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	key := "recovery:" + attempt.ID + ":prepared"
	update := CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(attempt.ID), SelectedBotKey: workflowStringPtr(attempt.BotKey)}
	updated, replay, err := o.transition(ctx, incident, statusForPhase(attempt.Phase), key, "recovery", "prepared_attempt_recovered", map[string]string{"attempt_id": attempt.ID}, update)
	if err != nil || replay {
		return err
	}
	if o.runner == nil {
		_, scheduleErr := o.phaseScheduleFailure(ctx, updated, attempt, key, errors.New("phase runner is unavailable"))
		return scheduleErr
	}
	bug := Bug{ID: updated.BugID, Source: updated.Source, SystemID: updated.SystemID, Env: updated.Environment}
	bot := BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}
	if err := o.runner.Start(ctx, attempt.Clone(), bug, bot); err != nil {
		_, scheduleErr := o.phaseScheduleFailure(ctx, updated, attempt, key, err)
		return scheduleErr
	}
	o.recoveryStarted[attempt.ID] = struct{}{}
	return nil
}

func (o *CaseOrchestrator) interruptDetachedAttempt(ctx context.Context, attempt PhaseAttempt) error {
	attempt.Status = AttemptStatusInterrupted
	attempt.OutputJSON = []byte(`{}`)
	attempt.ErrorCode = "detached_attempt"
	attempt.ErrorMessage = "attempt was not the current Case attempt during recovery"
	if err := o.store.FinishAttempt(ctx, attempt); err != nil && !errors.Is(err, ErrAttemptAlreadyFinished) {
		return err
	}
	return nil
}

func (o *CaseOrchestrator) recoverReadOnly(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, retryAllowed bool) error {
	attempts, err := o.store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if err != nil {
		return err
	}
	count := 0
	for _, candidate := range attempts {
		if candidate.CycleNumber == attempt.CycleNumber && candidate.Phase == attempt.Phase {
			count++
		}
	}
	waiting, _, err := o.transition(ctx, incident, CaseWaitingEvidence, "recovery:"+attempt.ID+":interrupted", "recovery", "attempt_interrupted", map[string]string{"attempt_id": attempt.ID}, CaseSnapshotUpdate{})
	if err != nil {
		return err
	}
	if !retryAllowed || count >= 2 {
		return nil
	}
	key := "recovery:" + attempt.ID + ":retry"
	bot := BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}
	retry := newAttempt(waiting, attempt.Phase, attempt.Mode, key, bot, attempt.InputJSON, attempt.ID)
	bug := Bug{ID: waiting.BugID, Source: waiting.Source, SystemID: waiting.SystemID, Env: waiting.Environment}
	updated, err := o.beginPhase(ctx, waiting, statusForPhase(attempt.Phase), retry, bug, bot, key, "recovery", "interrupted_attempt_retried")
	if err == nil && updated.CurrentAttemptID == retry.ID {
		o.recoveryStarted[retry.ID] = struct{}{}
	}
	return err
}

func (o *CaseOrchestrator) recoverFix(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	request, err := decodeRecoveryMergeRequest(incident.ID, attempt.InputJSON)
	if err != nil {
		_, _, transitionErr := o.transition(ctx, incident, CaseFixFailed, "recovery:"+attempt.ID+":invalid", "recovery", "fix_recovery_failed", map[string]string{"error": err.Error()}, CaseSnapshotUpdate{})
		return transitionErr
	}
	if o.git == nil {
		_, _, err := o.transition(ctx, incident, CaseFixFailed, "recovery:"+attempt.ID+":unavailable", "recovery", "fix_recovery_failed", map[string]string{"error": "git integration is unavailable"}, CaseSnapshotUpdate{})
		return err
	}
	inspection, inspectErr := o.git.Inspect(ctx, request)
	if inspectErr != nil || !inspection.FixPushed {
		message := "fix commit is not confirmed pushed"
		if inspectErr != nil {
			message = inspectErr.Error()
		}
		_, _, err := o.transition(ctx, incident, CaseFixFailed, "recovery:"+attempt.ID+":failed", "recovery", "fix_recovery_failed", map[string]string{"error": message}, CaseSnapshotUpdate{})
		return err
	}
	pushed, _, err := o.transition(ctx, incident, CaseFixPushed, "recovery:"+attempt.ID+":pushed", "recovery", "fix_push_confirmed", inspection, CaseSnapshotUpdate{})
	if err != nil {
		return err
	}
	_, _, err = o.transition(ctx, pushed, CaseWaitingMergeApproval, "recovery:"+attempt.ID+":merge-approval", "recovery", "merge_approval_requested", map[string]string{"attempt_id": attempt.ID}, CaseSnapshotUpdate{})
	return err
}

func (o *CaseOrchestrator) recoverMerge(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	request, err := decodeRecoveryMergeRequest(incident.ID, attempt.InputJSON)
	if err != nil {
		_, _, transitionErr := o.transition(ctx, incident, CaseMergeConflict, "recovery:"+attempt.ID+":invalid", "recovery", "merge_recovery_failed", map[string]string{"error": err.Error()}, CaseSnapshotUpdate{})
		return transitionErr
	}
	return o.inspectInterruptedMerge(ctx, incident, request, "recovery:"+attempt.ID)
}

func (o *CaseOrchestrator) inspectInterruptedMerge(ctx context.Context, incident IncidentCase, request MergeRequest, key string) error {
	if o.git == nil {
		_, _, err := o.transition(ctx, incident, CaseMergeConflict, key+":unavailable", "recovery", "merge_recovery_failed", map[string]string{"error": "git integration is unavailable"}, CaseSnapshotUpdate{})
		return err
	}
	inspection, inspectErr := o.git.Inspect(ctx, request)
	if inspectErr != nil || !inspection.MergePushed {
		message := "merge commit is not confirmed remote"
		if inspectErr != nil {
			message = inspectErr.Error()
		}
		_, _, err := o.transition(ctx, incident, CaseMergeConflict, key+":failed", "recovery", "merge_recovery_failed", map[string]string{"error": message}, CaseSnapshotUpdate{})
		return err
	}
	_, _, err := o.transition(ctx, incident, CaseWaitingDeployment, key+":pushed", "recovery", "merge_push_confirmed", inspection, CaseSnapshotUpdate{})
	return err
}

func (o *CaseOrchestrator) latestDeploymentMatched(ctx context.Context, caseID string) (bool, error) {
	observations, err := o.store.ListDeploymentObservations(ctx, caseID)
	if err != nil {
		return false, err
	}
	for index := len(observations) - 1; index >= 0; index-- {
		if observations[index].VerificationSource == "user-notification" {
			continue
		}
		return observations[index].Result == DeploymentResultMatched, nil
	}
	return false, nil
}

func decodeRecoveryMergeRequest(caseID string, input json.RawMessage) (MergeRequest, error) {
	var request MergeRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return MergeRequest{}, fmt.Errorf("decode recovery Git request: %w", err)
	}
	request.CaseID = caseID
	if len(request.FixCommits) == 0 || !sameStringMapKeys(request.FixCommits, request.TargetBranches) {
		return MergeRequest{}, ErrApprovalScope
	}
	return request, nil
}

func statusForPhase(phase Phase) CaseStatus {
	switch phase {
	case PhaseValidation:
		return CaseValidating
	case PhaseInvestigation:
		return CaseInvestigating
	case PhaseRegression:
		return CaseRegressionValidating
	default:
		return ""
	}
}
