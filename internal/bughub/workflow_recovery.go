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
	if o == nil || o.store == nil {
		return errors.New("case orchestrator store is required")
	}
	attempts, err := o.store.ListAttempts(ctx, AttemptFilter{Statuses: []AttemptStatus{AttemptStatusQueued, AttemptStatusRunning}})
	if err != nil {
		return err
	}
	var recoveredErr error
	processedCases := make(map[string]struct{})
	for _, attempt := range attempts {
		if o.wasRecoveryStarted(attempt.ID) {
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
		if incident.Status == CaseDeploymentUnverified {
			if recoveryErr := o.recoverDeploymentVerification(ctx, incident); recoveryErr != nil {
				recoveredErr = errors.Join(recoveredErr, recoveryErr)
			}
			continue
		}
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
	for _, incident := range cases {
		if incident.CurrentAttemptID == "" {
			continue
		}
		if _, done := processedCases[incident.ID]; done {
			continue
		}
		attempt, getErr := o.store.GetAttempt(ctx, incident.CurrentAttemptID)
		if getErr != nil {
			continue
		}
		if attempt.Status == AttemptStatusSucceeded || attempt.Status == AttemptStatusFailed || attempt.Status == AttemptStatusCancelled || attempt.Status == AttemptStatusInterrupted {
			if reconcileErr := o.reconcileTerminalCurrent(ctx, incident, attempt); reconcileErr != nil {
				recoveredErr = errors.Join(recoveredErr, reconcileErr)
			}
		}
	}
	return recoveredErr
}

func (o *CaseOrchestrator) recoverDeploymentVerification(ctx context.Context, incident IncidentCase) error {
	events, err := o.store.ListEvents(ctx, incident.ID)
	if err != nil {
		return err
	}
	var reservation DeploymentReservation
	found := false
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType != "deployment_verification_reserved" {
			continue
		}
		typed, ok, getErr := o.store.GetEventByIdempotencyKey(ctx, events[i].IdempotencyKey)
		if getErr != nil {
			return getErr
		}
		if !ok || json.Unmarshal(typed.PayloadJSON, &reservation) != nil {
			continue
		}
		found = true
		break
	}
	if !found {
		return nil
	}
	if _, resultFound, resultErr := o.store.GetEventByIdempotencyKey(ctx, reservation.ReservationKey+":result"); resultErr != nil || resultFound {
		return resultErr
	}
	if o.deployment == nil {
		return nil
	}
	observation, verifyErr := o.deployment.Verify(ctx, reservation.VerifierInput)
	_, recordErr := o.recordDeploymentResult(incident, reservation, observation, verifyErr)
	return recordErr
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
		var scope MergeApprovalScope
		if json.Unmarshal(approval.ScopeJSON, &scope) != nil || scope.CycleNumber != incident.CycleNumber || scope.FixAttemptID != incident.CurrentAttemptID {
			continue
		}
		all, loadErr := o.store.ListCodeChanges(ctx, incident.ID)
		if loadErr != nil {
			return loadErr
		}
		byID := map[string]CodeChange{}
		for _, change := range all {
			byID[change.ID] = change
		}
		selected := []CodeChange{}
		for _, approved := range scope.CodeChanges {
			change, ok := byID[approved.ID]
			if !ok {
				return ErrApprovalScope
			}
			selected = append(selected, change)
		}
		request := MergeRequest{CaseID: incident.ID, FixCommits: CloneStringMap(approval.FixCommits), TargetBranches: CloneStringMap(approval.TargetBranches), Changes: selected}
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
		return o.interruptDetachedAttempt(ctx, incident, attempt)
	}
	completion, found, err := parseCompletionIntent(attempt.OutputJSON)
	if err != nil {
		return err
	}
	if found {
		if completion.CaseID != incident.ID || completion.AttemptID != attempt.ID {
			return errors.New("persisted completion intent is not bound to current Case attempt")
		}
		completion.ExpectedVersion = incident.Version
		_, err := o.CompleteAttempt(ctx, completion)
		return err
	}
	attempt.Status = AttemptStatusInterrupted
	attempt.OutputJSON = []byte(`{}`)
	attempt.ErrorCode = "studio_restarted"
	attempt.ErrorMessage = "phase process was interrupted by Studio restart"
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
		if err := o.reserveInspectionOnly(ctx, incident, attempt); err != nil {
			return err
		}
		incident, err = o.store.GetCase(ctx, incident.ID)
		if err != nil {
			return err
		}
		return o.recoverFix(ctx, incident, attempt)
	case CaseMerging:
		if err := o.reserveInterruptedSideEffect(ctx, incident, attempt); err != nil {
			return err
		}
		incident, err = o.store.GetCase(ctx, incident.ID)
		if err != nil {
			return err
		}
		return o.recoverMerge(ctx, incident, attempt)
	default:
		return nil
	}
}

func (o *CaseOrchestrator) reserveInspectionOnly(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	key := "recovery:" + attempt.ID + ":inspect"
	if event, found, err := o.store.GetEventByIdempotencyKey(ctx, key); err != nil {
		return err
	} else if found {
		if event.CaseID != incident.ID {
			return ErrIdempotencyConflict
		}
		return nil
	}
	if event, found, err := o.store.GetEventByIdempotencyKey(ctx, "recovery:"+attempt.ID+":result"); err != nil {
		return err
	} else if found {
		if event.CaseID != incident.ID {
			return ErrIdempotencyConflict
		}
		return nil
	}
	_, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]string{"attempt_id": attempt.ID}), Steps: []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", key), EventType: "side_effect_inspection_reserved", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}})
	return err
}

func (o *CaseOrchestrator) reserveInterruptedSideEffect(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	key := "recovery:" + attempt.ID + ":inspect"
	_, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]string{"attempt_id": attempt.ID}), FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", key), EventType: "side_effect_inspection_reserved", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}})
	return err
}

func (o *CaseOrchestrator) reconcileTerminalCurrent(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	to := failureStateForPhase(attempt.Phase)
	if !CanTransition(incident.Status, to) {
		return nil
	}
	key := "recovery:" + attempt.ID + ":terminal-reconcile"
	_, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]any{"attempt_id": attempt.ID, "status": attempt.Status}), Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", key), EventType: "terminal_attempt_reconciled", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}})
	return err
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
	if err := o.startPhase(attempt, bug, bot); err != nil {
		_, scheduleErr := o.phaseScheduleFailure(ctx, updated, attempt, key, err)
		return scheduleErr
	}
	o.markRecoveryStarted(attempt.ID)
	return nil
}

func (o *CaseOrchestrator) interruptDetachedAttempt(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	attempt.Status = AttemptStatusInterrupted
	attempt.OutputJSON = []byte(`{}`)
	attempt.ErrorCode = "detached_attempt"
	attempt.ErrorMessage = "attempt was not the current Case attempt during recovery"
	key := "recovery:" + attempt.ID + ":detached"
	_, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]string{"attempt_id": attempt.ID}), FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", key), EventType: "detached_attempt_interrupted", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}})
	return err
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
	key := "recovery:" + attempt.ID + ":interrupted"
	steps := []CaseMutationStep{{To: CaseWaitingEvidence, Event: TransitionEvent{ID: stableID("event", key), EventType: "attempt_interrupted", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}
	creates := []PhaseAttempt{}
	update := CaseSnapshotUpdate{}
	empty := ""
	update.CurrentAttemptID = &empty
	var retry PhaseAttempt
	if retryAllowed && count < 2 {
		bot := BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}
		retry = newAttempt(incident, attempt.Phase, attempt.Mode, key+":retry", bot, attempt.InputJSON, attempt.ID)
		creates = append(creates, retry)
		update.CurrentAttemptID = workflowStringPtr(retry.ID)
		steps = append(steps, CaseMutationStep{To: statusForPhase(attempt.Phase), Event: TransitionEvent{ID: stableID("event", key+":retry"), EventType: "interrupted_attempt_retried", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": retry.ID})}})
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]any{"attempt_id": attempt.ID, "retry": retryAllowed, "count": count}), FinishAttempts: []PhaseAttempt{attempt}, CreateAttempts: creates, Snapshot: update, Steps: steps})
	if err != nil {
		return err
	}
	if len(creates) == 0 || mutation.Replay {
		return nil
	}
	bug := Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment}
	bot := BotRef{Key: retry.BotKey, Target: retry.AgentTarget}
	if o.runner == nil {
		_, err = o.phaseScheduleFailure(ctx, mutation.Case, retry, key+":retry", errors.New("phase runner unavailable"))
		return err
	}
	if startErr := o.startPhase(retry, bug, bot); startErr != nil {
		_, err = o.phaseScheduleFailure(ctx, mutation.Case, retry, key+":retry", startErr)
		return err
	}
	o.markRecoveryStarted(retry.ID)
	return nil
}

func (o *CaseOrchestrator) recoverFix(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) error {
	changes, err := o.store.ListCodeChanges(ctx, incident.ID)
	if err != nil {
		return err
	}
	relevant := []CodeChange{}
	for _, change := range changes {
		if change.AttemptID == attempt.ID {
			relevant = append(relevant, change)
		}
	}
	if o.git == nil {
		return o.finishFixRecovery(ctx, incident, attempt, relevant, false, "git integration unavailable")
	}
	inspection, inspectErr := o.git.InspectFix(ctx, FixInspectionRequest{CaseID: incident.ID, Attempt: attempt.Clone(), Changes: relevant})
	if inspectErr != nil || !inspection.Complete || len(inspection.Changes) == 0 {
		message := "fix commit is not confirmed pushed"
		if inspectErr != nil {
			message = inspectErr.Error()
		}
		return o.finishFixRecovery(ctx, incident, attempt, relevant, false, message)
	}
	relevant = inspection.Changes
	for i := range relevant {
		if relevant[i].ID == "" {
			relevant[i].ID = stableID("recovered-change", attempt.ID+":"+relevant[i].Repo)
		}
		relevant[i].CaseID = incident.ID
		relevant[i].AttemptID = attempt.ID
		if relevant[i].PushStatus != "pushed" || relevant[i].FixCommit == "" {
			return o.finishFixRecovery(ctx, incident, attempt, nil, false, "fix push is not confirmed")
		}
	}
	return o.finishFixRecovery(ctx, incident, attempt, relevant, true, "")
}

func (o *CaseOrchestrator) finishFixRecovery(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, changes []CodeChange, pushed bool, message string) error {
	key := "recovery:" + attempt.ID + ":result"
	steps := []CaseMutationStep{}
	attempt.OutputJSON = []byte(`{}`)
	if pushed {
		attempt.Status = AttemptStatusSucceeded
		for i := range changes {
			changes[i].PushStatus = "pushed"
		}
		steps = append(steps, CaseMutationStep{To: CaseFixPushed, Event: TransitionEvent{ID: stableID("event", key), EventType: "fix_push_confirmed", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}, CaseMutationStep{To: CaseWaitingMergeApproval, Event: TransitionEvent{ID: stableID("event", key+":approval"), EventType: "merge_approval_requested", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}})
	} else {
		attempt.Status = AttemptStatusFailed
		attempt.ErrorCode = "fix_recovery_failed"
		attempt.ErrorMessage = message
		steps = append(steps, CaseMutationStep{To: CaseFixFailed, Event: TransitionEvent{ID: stableID("event", key), EventType: "fix_recovery_failed", ActorType: "recovery", ActorID: "recovery", PayloadJSON: mustJSON(map[string]string{"error": message})}})
	}
	_, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]any{"pushed": pushed, "message": message}), FinishAttempts: []PhaseAttempt{attempt}, CodeChanges: changes, Steps: steps})
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
	allChanges, loadErr := o.store.ListCodeChanges(ctx, incident.ID)
	if loadErr != nil {
		return loadErr
	}
	changes := []CodeChange{}
	if len(request.Changes) > 0 {
		for _, change := range request.Changes {
			changes = append(changes, change.Clone())
		}
	} else {
		for _, change := range allChanges {
			if _, ok := request.FixCommits[change.Repo]; ok {
				changes = append(changes, change)
			}
		}
	}
	if o.git == nil {
		_, err := o.recordMergeAmbiguous(incident, key, changes, errors.New("git integration unavailable"))
		return err
	}
	inspection, inspectErr := o.git.Inspect(ctx, request)
	if inspectErr != nil {
		_, err := o.recordMergeAmbiguous(incident, key, changes, inspectErr)
		return err
	}
	_, err := o.resumeInspectedMerge(ctx, incident, key, changes, request, inspection)
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
	case PhaseFix:
		return CaseFixing
	default:
		return ""
	}
}
