package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrApprovalNotReady  = errors.New("workflow approval is not ready")
	ErrApprovalScope     = errors.New("workflow approval scope is invalid")
	ErrAttemptNotCurrent = errors.New("phase attempt is not current")
)

var workflowCommandLocks = commandLockRegistry{locks: make(map[string]*commandLock)}

type commandLock struct {
	mu   sync.Mutex
	refs int
}

type commandLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*commandLock
}

func (r *commandLockRegistry) acquire(key string) func() {
	r.mu.Lock()
	entry := r.locks[key]
	if entry == nil {
		entry = &commandLock{}
		r.locks[key] = entry
	}
	entry.refs++
	r.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(r.locks, key)
		}
		r.mu.Unlock()
	}
}

type PhaseRunner interface {
	Start(context.Context, PhaseAttempt, Bug, BotRef) error
	Cancel(context.Context, string) error
}

type GitIntegration interface {
	MergeAndPush(context.Context, MergeRequest) (MergeResult, error)
	Inspect(context.Context, MergeRequest) (MergeInspection, error)
}

type DeploymentVerifier interface {
	Verify(context.Context, DeploymentVerificationRequest) (DeploymentObservation, error)
}

type MergeRequest struct {
	CaseID         string            `json:"case_id"`
	FixCommits     map[string]string `json:"fix_commits"`
	TargetBranches map[string]string `json:"target_branches"`
}

func (r MergeRequest) Clone() MergeRequest {
	r.FixCommits = CloneStringMap(r.FixCommits)
	r.TargetBranches = CloneStringMap(r.TargetBranches)
	return r
}

type MergeResult struct {
	Pushed       bool              `json:"pushed"`
	Conflict     bool              `json:"conflict"`
	MergeCommits map[string]string `json:"merge_commits"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

func (r MergeResult) Clone() MergeResult {
	r.MergeCommits = CloneStringMap(r.MergeCommits)
	return r
}

type MergeInspection struct {
	FixPushed    bool              `json:"fix_pushed"`
	MergePushed  bool              `json:"merge_pushed"`
	Conflict     bool              `json:"conflict"`
	MergeCommits map[string]string `json:"merge_commits"`
}

func (i MergeInspection) Clone() MergeInspection {
	i.MergeCommits = CloneStringMap(i.MergeCommits)
	return i
}

type DeploymentVerificationRequest struct {
	CaseID          string            `json:"case_id"`
	Environment     string            `json:"environment"`
	ExpectedCommits map[string]string `json:"expected_commits"`
	ObservedVersion string            `json:"observed_version,omitempty"`
	ObservedCommits map[string]string `json:"observed_commits,omitempty"`
}

func (r DeploymentVerificationRequest) Clone() DeploymentVerificationRequest {
	r.ExpectedCommits = CloneStringMap(r.ExpectedCommits)
	r.ObservedCommits = CloneStringMap(r.ObservedCommits)
	return r
}

type StartCaseCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	Bug             Bug
	Bot             BotRef
	InputJSON       json.RawMessage
}

type ContinueWithEvidenceCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	Phase           Phase
	Bug             Bug
	Bot             BotRef
	InputJSON       json.RawMessage
}

type ApproveFixCommand struct {
	CaseID             string
	ExpectedVersion    int64
	IdempotencyKey     string
	ActorID            string
	RootCauseAttemptID string
	Bug                Bug
	Bot                BotRef
	InputJSON          json.RawMessage
}

type ApproveMergeCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	FixCommits      map[string]string
	TargetBranches  map[string]string
}

type NotifyDeployedCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	ExpectedCommits map[string]string
	ObservedVersion string
	ObservedCommits map[string]string
	Bug             Bug
	Bot             BotRef
	InputJSON       json.RawMessage
}

type CancelAttemptCommand struct {
	CaseID          string
	AttemptID       string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
}

type PhaseOutcome string

const (
	PhaseOutcomeReproduced      PhaseOutcome = "reproduced"
	PhaseOutcomeNotReproduced   PhaseOutcome = "not_reproduced"
	PhaseOutcomeNeedsEvidence   PhaseOutcome = "needs_evidence"
	PhaseOutcomeRootCauseReady  PhaseOutcome = "root_cause_ready"
	PhaseOutcomeFixPushed       PhaseOutcome = "fix_pushed"
	PhaseOutcomeFixFailed       PhaseOutcome = "fix_failed"
	PhaseOutcomeFixedVerified   PhaseOutcome = "fixed_verified"
	PhaseOutcomeStillReproduces PhaseOutcome = "still_reproduces"
)

type CompleteAttemptCommand struct {
	CaseID          string
	AttemptID       string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	Outcome         PhaseOutcome
	OutputJSON      json.RawMessage
	ErrorCode       string
	ErrorMessage    string
	Usage           AgentUsage
}

type CaseOrchestrator struct {
	store           *CaseStore
	runner          PhaseRunner
	git             GitIntegration
	deployment      DeploymentVerifier
	mu              sync.Mutex
	recoveryStarted map[string]struct{}
}

func NewCaseOrchestrator(store *CaseStore, runner PhaseRunner, git GitIntegration, deployment DeploymentVerifier) *CaseOrchestrator {
	return &CaseOrchestrator{store: store, runner: runner, git: git, deployment: deployment, recoveryStarted: make(map[string]struct{})}
}

func (o *CaseOrchestrator) StartCase(ctx context.Context, cmd StartCaseCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CasePendingValidation {
		return IncidentCase{}, &ErrInvalidTransition{From: incident.Status, To: CaseValidating}
	}
	attempt := newAttempt(incident, PhaseValidation, AttemptReproduce, cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, "")
	return o.beginPhase(ctx, incident, CaseValidating, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "validation_started")
}

func (o *CaseOrchestrator) ContinueWithEvidence(ctx context.Context, cmd ContinueWithEvidenceCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingEvidence && incident.Status != CaseNotReproduced && incident.Status != CaseFixFailed && incident.Status != CaseDeploymentUnverified && incident.Status != CaseMergeConflict {
		return IncidentCase{}, ErrApprovalNotReady
	}
	to, mode, phase := continuationTarget(incident, cmd.Phase)
	if to == "" {
		return IncidentCase{}, fmt.Errorf("cannot continue phase %q from %s", cmd.Phase, incident.Status)
	}
	attempt := newAttempt(incident, phase, mode, cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, incident.CurrentAttemptID)
	return o.beginPhase(ctx, incident, to, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "evidence_continued")
}

func (o *CaseOrchestrator) ApproveFix(ctx context.Context, cmd ApproveFixCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingFixApproval {
		return IncidentCase{}, ErrApprovalNotReady
	}
	if cmd.RootCauseAttemptID == "" || cmd.RootCauseAttemptID != incident.CurrentAttemptID {
		return IncidentCase{}, ErrApprovalScope
	}
	scope, _ := json.Marshal(map[string]string{"root_cause_attempt_id": cmd.RootCauseAttemptID})
	approval := Approval{ID: stableID("approval", cmd.IdempotencyKey), CaseID: incident.ID, Kind: ApprovalStartFix, Actor: cmd.ActorID, CaseVersion: incident.Version, ScopeJSON: scope}
	if err := o.store.RecordApproval(ctx, approval, "approval:"+cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	}
	attempt := newAttempt(incident, PhaseFix, "", cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, incident.CurrentAttemptID)
	return o.beginPhase(ctx, incident, CaseFixing, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "fix_approved")
}

func (o *CaseOrchestrator) ApproveMerge(ctx context.Context, cmd ApproveMergeCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingMergeApproval {
		return IncidentCase{}, ErrApprovalNotReady
	}
	if len(cmd.FixCommits) == 0 || !sameStringMapKeys(cmd.FixCommits, cmd.TargetBranches) {
		return IncidentCase{}, ErrApprovalScope
	}
	scope, _ := json.Marshal(map[string]any{"fix_commits": cmd.FixCommits, "target_branches": cmd.TargetBranches})
	approval := Approval{ID: stableID("approval", cmd.IdempotencyKey), CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: cmd.ActorID, CaseVersion: incident.Version, ScopeJSON: scope, FixCommits: cmd.FixCommits, TargetBranches: cmd.TargetBranches}
	if err := o.store.RecordApproval(ctx, approval, "approval:"+cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	}
	merging, replay, err := o.transition(ctx, incident, CaseMerging, cmd.IdempotencyKey, cmd.ActorID, "merge_approved", map[string]any{"fix_commits": cmd.FixCommits, "target_branches": cmd.TargetBranches}, CaseSnapshotUpdate{})
	if err != nil || replay {
		if err != nil {
			return merging, err
		}
		return o.store.GetCase(ctx, incident.ID)
	}
	if o.git == nil {
		return o.externalFailure(ctx, merging, CaseMergeConflict, cmd.IdempotencyKey, cmd.ActorID, "merge_schedule_failed", errors.New("git integration is unavailable"))
	}
	result, scheduleErr := o.git.MergeAndPush(ctx, MergeRequest{CaseID: incident.ID, FixCommits: CloneStringMap(cmd.FixCommits), TargetBranches: CloneStringMap(cmd.TargetBranches)})
	if scheduleErr != nil || !result.Pushed {
		if scheduleErr == nil {
			scheduleErr = errors.New("merge was not pushed")
		}
		return o.externalFailure(ctx, merging, CaseMergeConflict, cmd.IdempotencyKey, cmd.ActorID, "merge_failed", scheduleErr)
	}
	waiting, _, err := o.transition(ctx, merging, CaseWaitingDeployment, cmd.IdempotencyKey+":completed", "git", "merge_pushed", result, CaseSnapshotUpdate{})
	return waiting, err
}

func (o *CaseOrchestrator) NotifyDeployed(ctx context.Context, cmd NotifyDeployedCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	release := workflowCommandLocks.acquire("deployment:" + cmd.CaseID + ":" + cmd.IdempotencyKey)
	defer release()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingDeployment {
		return IncidentCase{}, ErrApprovalNotReady
	}
	if len(cmd.ExpectedCommits) == 0 {
		return IncidentCase{}, ErrApprovalScope
	}
	now := time.Now().UTC()
	intentID := stableID("deployment-intent", cmd.IdempotencyKey)
	if observations, listErr := o.store.ListDeploymentObservations(ctx, incident.ID); listErr != nil {
		return IncidentCase{}, listErr
	} else {
		for _, stored := range observations {
			if stored.ID == intentID && stored.UserNotifiedAt != nil {
				now = *stored.UserNotifiedAt
				break
			}
		}
	}
	intent := DeploymentObservation{ID: intentID, CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: CloneStringMap(cmd.ExpectedCommits), UserNotifiedAt: &now, VerificationSource: "user-notification", ObservedVersion: cmd.ObservedVersion, ObservedCommits: CloneStringMap(cmd.ObservedCommits), Result: DeploymentResultUnavailable}
	if err := o.store.RecordDeploymentObservation(ctx, intent, "deployment-intent:"+cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	}
	if replay, err := o.hasEvent(ctx, incident.ID, cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	} else if replay {
		return o.store.GetCase(ctx, incident.ID)
	}
	if o.deployment == nil {
		return o.externalFailure(ctx, incident, CaseDeploymentUnverified, cmd.IdempotencyKey, cmd.ActorID, "deployment_verification_failed", errors.New("deployment verifier is unavailable"))
	}
	request := DeploymentVerificationRequest{CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: CloneStringMap(cmd.ExpectedCommits), ObservedVersion: cmd.ObservedVersion, ObservedCommits: CloneStringMap(cmd.ObservedCommits)}
	observation, verifyErr := o.deployment.Verify(ctx, request)
	if verifyErr != nil {
		return o.externalFailure(ctx, incident, CaseDeploymentUnverified, cmd.IdempotencyKey, cmd.ActorID, "deployment_verification_failed", verifyErr)
	}
	observation.ID = stableID("deployment", cmd.IdempotencyKey)
	observation.CaseID = incident.ID
	observation.Environment = incident.Environment
	observation.ExpectedCommits = CloneStringMap(cmd.ExpectedCommits)
	observation.UserNotifiedAt = &now
	if observation.VerificationSource == "" {
		observation.VerificationSource = "deployment-verifier"
	}
	if err := o.store.RecordDeploymentObservation(ctx, observation, "deployment:"+cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	}
	if observation.Result != DeploymentResultMatched {
		unverified, _, err := o.transition(ctx, incident, CaseDeploymentUnverified, cmd.IdempotencyKey, cmd.ActorID, "deployment_unverified", observation, CaseSnapshotUpdate{})
		return unverified, err
	}
	verified, replay, err := o.transition(ctx, incident, CaseDeploymentVerified, cmd.IdempotencyKey, cmd.ActorID, "deployment_verified", observation, CaseSnapshotUpdate{})
	if err != nil || replay {
		return verified, err
	}
	attempt := newAttempt(verified, PhaseRegression, AttemptRegression, cmd.IdempotencyKey+":regression", cmd.Bot, cmd.InputJSON, verified.CurrentAttemptID)
	return o.beginPhase(ctx, verified, CaseRegressionValidating, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey+":regression", cmd.ActorID, "regression_started")
}

func (o *CaseOrchestrator) CancelAttempt(ctx context.Context, cmd CancelAttemptCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if cmd.AttemptID == "" || incident.CurrentAttemptID != cmd.AttemptID {
		return IncidentCase{}, ErrAttemptNotCurrent
	}
	attempt, err := o.store.GetAttempt(ctx, cmd.AttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	attempt.Status, attempt.OutputJSON, attempt.ErrorCode = AttemptStatusCancelled, []byte(`{}`), "cancelled"
	if err := o.store.FinishAttempt(ctx, attempt); err != nil && !errors.Is(err, ErrAttemptAlreadyFinished) {
		return IncidentCase{}, err
	}
	to := failureStateForPhase(attempt.Phase)
	updated, replay, err := o.transition(ctx, incident, to, cmd.IdempotencyKey, cmd.ActorID, "attempt_cancelled", map[string]string{"attempt_id": attempt.ID}, CaseSnapshotUpdate{})
	if err != nil || replay {
		if err != nil {
			return updated, err
		}
		return o.store.GetCase(ctx, incident.ID)
	}
	if o.runner != nil {
		err = o.runner.Cancel(ctx, attempt.ID)
	}
	return updated, err
}

func (o *CaseOrchestrator) CompleteAttempt(ctx context.Context, cmd CompleteAttemptCommand) (IncidentCase, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.CurrentAttemptID != cmd.AttemptID {
		return IncidentCase{}, ErrAttemptNotCurrent
	}
	attempt, err := o.store.GetAttempt(ctx, cmd.AttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if len(cmd.OutputJSON) == 0 {
		cmd.OutputJSON = []byte(`{}`)
	}
	attempt.OutputJSON, attempt.ErrorCode, attempt.ErrorMessage, attempt.Usage = CloneRawMessage(cmd.OutputJSON), cmd.ErrorCode, cmd.ErrorMessage, cmd.Usage
	if cmd.Outcome == PhaseOutcomeFixFailed || cmd.Outcome == PhaseOutcomeNeedsEvidence {
		attempt.Status = AttemptStatusFailed
	} else {
		attempt.Status = AttemptStatusSucceeded
	}
	if err := o.store.FinishAttempt(ctx, attempt); err != nil && !errors.Is(err, ErrAttemptAlreadyFinished) {
		return IncidentCase{}, err
	}
	attempt, err = o.store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		return IncidentCase{}, err
	}
	return o.applyOutcome(ctx, incident, attempt, cmd)
}

func (o *CaseOrchestrator) applyOutcome(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, cmd CompleteAttemptCommand) (IncidentCase, error) {
	switch cmd.Outcome {
	case PhaseOutcomeReproduced:
		reproduced, replay, err := o.transition(ctx, incident, CaseReproduced, cmd.IdempotencyKey, cmd.ActorID, "validation_reproduced", cmd.OutputJSON, CaseSnapshotUpdate{})
		if err != nil {
			return IncidentCase{}, err
		}
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		next := newAttempt(reproduced, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, []byte(`{}`), attempt.ID)
		return o.beginPhase(ctx, reproduced, CaseInvestigating, next, Bug{ID: reproduced.BugID, Source: reproduced.Source}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, cmd.IdempotencyKey+":investigation", "studio", "investigation_started")
	case PhaseOutcomeNotReproduced:
		updated, replay, err := o.transition(ctx, incident, CaseNotReproduced, cmd.IdempotencyKey, cmd.ActorID, "validation_not_reproduced", cmd.OutputJSON, CaseSnapshotUpdate{})
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		return updated, err
	case PhaseOutcomeNeedsEvidence:
		updated, replay, err := o.transition(ctx, incident, CaseWaitingEvidence, cmd.IdempotencyKey, cmd.ActorID, "evidence_required", cmd.OutputJSON, CaseSnapshotUpdate{})
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		return updated, err
	case PhaseOutcomeRootCauseReady:
		root, replay, err := o.transition(ctx, incident, CaseRootCauseReady, cmd.IdempotencyKey, cmd.ActorID, "root_cause_ready", cmd.OutputJSON, CaseSnapshotUpdate{})
		if err != nil {
			return IncidentCase{}, err
		}
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		waiting, _, err := o.transition(ctx, root, CaseWaitingFixApproval, cmd.IdempotencyKey+":approval", "studio", "fix_approval_requested", map[string]string{"attempt_id": attempt.ID}, CaseSnapshotUpdate{})
		return waiting, err
	case PhaseOutcomeFixPushed:
		pushed, replay, err := o.transition(ctx, incident, CaseFixPushed, cmd.IdempotencyKey, cmd.ActorID, "fix_pushed", cmd.OutputJSON, CaseSnapshotUpdate{})
		if err != nil {
			return IncidentCase{}, err
		}
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		waiting, _, err := o.transition(ctx, pushed, CaseWaitingMergeApproval, cmd.IdempotencyKey+":approval", "studio", "merge_approval_requested", map[string]string{"attempt_id": attempt.ID}, CaseSnapshotUpdate{})
		return waiting, err
	case PhaseOutcomeFixFailed:
		failed, replay, err := o.transition(ctx, incident, CaseFixFailed, cmd.IdempotencyKey, cmd.ActorID, "fix_failed", cmd.OutputJSON, CaseSnapshotUpdate{})
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		return failed, err
	case PhaseOutcomeFixedVerified:
		if attempt.FinishedAt == nil {
			return IncidentCase{}, errors.New("finished regression attempt timestamp is required")
		}
		now := *attempt.FinishedAt
		closed, replay, err := o.transition(ctx, incident, CaseFixedVerified, cmd.IdempotencyKey, cmd.ActorID, "regression_fixed", cmd.OutputJSON, CaseSnapshotUpdate{ClosedAtSet: true, ClosedAt: &now})
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		return closed, err
	case PhaseOutcomeStillReproduces:
		still, replay, err := o.transition(ctx, incident, CaseStillReproduces, cmd.IdempotencyKey, cmd.ActorID, "regression_failed", cmd.OutputJSON, CaseSnapshotUpdate{})
		if err != nil {
			return IncidentCase{}, err
		}
		if replay {
			return o.store.GetCase(ctx, incident.ID)
		}
		cycle := still.CycleNumber + 1
		next := newAttempt(still, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, []byte(`{}`), attempt.ID)
		next.CycleNumber = cycle
		return o.beginPhaseWithUpdate(ctx, still, CaseInvestigating, next, Bug{ID: still.BugID, Source: still.Source}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, cmd.IdempotencyKey+":investigation", "studio", "next_cycle_investigation_started", CaseSnapshotUpdate{CycleNumber: &cycle})
	default:
		return IncidentCase{}, fmt.Errorf("unsupported phase outcome %q", cmd.Outcome)
	}
}

func (o *CaseOrchestrator) beginPhase(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string) (IncidentCase, error) {
	return o.beginPhaseWithUpdate(ctx, incident, to, attempt, bug, bot, key, actor, eventType, CaseSnapshotUpdate{})
}

func (o *CaseOrchestrator) beginPhaseWithUpdate(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string, update CaseSnapshotUpdate) (IncidentCase, error) {
	if err := o.ensureAttempt(ctx, attempt); err != nil {
		return IncidentCase{}, err
	}
	update.CurrentAttemptID = workflowStringPtr(attempt.ID)
	update.SelectedBotKey = workflowStringPtr(bot.Key)
	updated, replay, err := o.transition(ctx, incident, to, key, actor, eventType, map[string]string{"attempt_id": attempt.ID}, update)
	if err != nil {
		return updated, err
	}
	if replay {
		return o.store.GetCase(ctx, incident.ID)
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, updated, attempt, key, errors.New("phase runner is unavailable"))
	}
	if err := o.runner.Start(ctx, attempt.Clone(), bug, bot); err != nil {
		return o.phaseScheduleFailure(ctx, updated, attempt, key, err)
	}
	return updated, nil
}

func (o *CaseOrchestrator) phaseScheduleFailure(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, key string, cause error) (IncidentCase, error) {
	attempt.Status, attempt.OutputJSON, attempt.ErrorCode, attempt.ErrorMessage = AttemptStatusFailed, []byte(`{}`), "schedule_failed", cause.Error()
	_ = o.store.FinishAttempt(ctx, attempt)
	failed, _, err := o.transition(ctx, incident, failureStateForPhase(attempt.Phase), key+":schedule-failed", "studio", "phase_schedule_failed", map[string]string{"error": cause.Error(), "attempt_id": attempt.ID}, CaseSnapshotUpdate{})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return failed, fmt.Errorf("schedule phase %s: %w", attempt.Phase, cause)
}

func (o *CaseOrchestrator) externalFailure(ctx context.Context, incident IncidentCase, to CaseStatus, key, actor, eventType string, cause error) (IncidentCase, error) {
	failed, _, err := o.transition(ctx, incident, to, key+":failed", actor, eventType, map[string]string{"error": cause.Error()}, CaseSnapshotUpdate{})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return failed, cause
}

func (o *CaseOrchestrator) transition(ctx context.Context, incident IncidentCase, to CaseStatus, key, actor, eventType string, payload any, update CaseSnapshotUpdate) (IncidentCase, bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return IncidentCase{}, false, err
	}
	event := TransitionEvent{ID: stableID("event", key), IdempotencyKey: key, EventType: eventType, ActorType: "user", ActorID: actor, PayloadJSON: encoded}
	return o.store.TransitionWithUpdate(ctx, incident.ID, incident.Version, to, update, event)
}

func (o *CaseOrchestrator) loadForCommand(ctx context.Context, caseID string, expected int64, key string) (IncidentCase, error) {
	if o == nil || o.store == nil {
		return IncidentCase{}, errors.New("case orchestrator store is required")
	}
	incident, err := o.store.GetCase(ctx, caseID)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status == CaseLegacyArchived {
		return IncidentCase{}, &ErrInvalidTransition{From: incident.Status, Reason: "legacy cases are immutable"}
	}
	if incident.Version == expected {
		return incident, nil
	}
	events, listErr := o.store.ListEvents(ctx, caseID)
	if listErr != nil {
		return IncidentCase{}, listErr
	}
	for _, event := range events {
		if event.IdempotencyKey == key {
			incident.Version = expected
			incident.Status = event.FromStatus
			return incident, nil
		}
	}
	return IncidentCase{}, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, expected, incident.Version)
}

func (o *CaseOrchestrator) hasEvent(ctx context.Context, caseID, key string) (bool, error) {
	_, ok, err := o.eventByKey(ctx, caseID, key)
	return ok, err
}

func (o *CaseOrchestrator) eventByKey(ctx context.Context, caseID, key string) (TransitionEvent, bool, error) {
	events, err := o.store.ListEvents(ctx, caseID)
	if err != nil {
		return TransitionEvent{}, false, err
	}
	for _, event := range events {
		if event.IdempotencyKey == key {
			return event, true, nil
		}
	}
	return TransitionEvent{}, false, nil
}

func (o *CaseOrchestrator) ensureAttempt(ctx context.Context, attempt PhaseAttempt) error {
	if err := o.store.CreateAttempt(ctx, attempt); err == nil {
		return nil
	}
	stored, loadErr := o.store.GetAttempt(ctx, attempt.ID)
	if loadErr != nil {
		return loadErr
	}
	if !sameOrchestratedAttempt(stored, attempt) {
		return fmt.Errorf("%w: attempt %s stored=%+v requested=%+v", ErrIdempotencyConflict, attempt.ID, stored, attempt)
	}
	return nil
}

func sameOrchestratedAttempt(left, right PhaseAttempt) bool {
	return left.ID == right.ID && left.CaseID == right.CaseID && left.CycleNumber == right.CycleNumber && left.Phase == right.Phase && left.Mode == right.Mode && left.Status == right.Status && left.AgentTarget == right.AgentTarget && left.BotKey == right.BotKey && string(left.InputJSON) == string(right.InputJSON) && left.ParentAttemptID == right.ParentAttemptID
}

func newAttempt(incident IncidentCase, phase Phase, mode AttemptMode, key string, bot BotRef, input json.RawMessage, parent string) PhaseAttempt {
	if len(input) == 0 {
		input = []byte(`{}`)
	}
	return PhaseAttempt{ID: stableID("attempt", key), CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: phase, Mode: mode, Status: AttemptStatusRunning, AgentTarget: bot.Target, BotKey: bot.Key, InputJSON: CloneRawMessage(input), OutputJSON: []byte(`{}`), ParentAttemptID: parent}
}

func validateCommand(caseID string, version int64, key, actor string) error {
	if strings.TrimSpace(caseID) == "" {
		return errors.New("case ID is required")
	}
	if version < 1 {
		return errors.New("expected version must be positive")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("idempotency key is required")
	}
	if strings.TrimSpace(actor) == "" {
		return errors.New("actor ID is required")
	}
	return nil
}

func stableID(kind, key string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + key))
	return kind + "-" + hex.EncodeToString(digest[:16])
}

func failureStateForPhase(phase Phase) CaseStatus {
	switch phase {
	case PhaseFix:
		return CaseFixFailed
	case PhaseRegression, PhaseValidation, PhaseInvestigation:
		return CaseWaitingEvidence
	default:
		return CaseWaitingEvidence
	}
}

func continuationTarget(incident IncidentCase, requested Phase) (CaseStatus, AttemptMode, Phase) {
	phase := requested
	if phase == "" {
		switch incident.Status {
		case CaseNotReproduced:
			phase = PhaseValidation
		case CaseFixFailed:
			phase = PhaseFix
		case CaseDeploymentUnverified:
			return "", "", ""
		case CaseMergeConflict:
			return "", "", ""
		default:
			phase = PhaseInvestigation
		}
	}
	switch phase {
	case PhaseValidation:
		return CaseValidating, AttemptReproduce, phase
	case PhaseInvestigation:
		return CaseInvestigating, "", phase
	case PhaseFix:
		return CaseFixing, "", phase
	case PhaseRegression:
		return CaseRegressionValidating, AttemptRegression, phase
	default:
		return "", "", ""
	}
}

func workflowStringPtr(value string) *string { return &value }
