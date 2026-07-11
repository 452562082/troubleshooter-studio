package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
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
	InspectFix(context.Context, FixInspectionRequest) (FixInspection, error)
	ResumePush(context.Context, MergeRequest) (MergeResult, error)
}

type FixInspectionRequest struct {
	CaseID  string       `json:"case_id"`
	Attempt PhaseAttempt `json:"attempt"`
	Changes []CodeChange `json:"changes"`
}
type FixInspection struct {
	Complete     bool         `json:"complete"`
	Changes      []CodeChange `json:"changes"`
	ErrorMessage string       `json:"error_message,omitempty"`
}

func (i FixInspection) Clone() FixInspection {
	cloned := i
	cloned.Changes = make([]CodeChange, len(i.Changes))
	for n := range i.Changes {
		cloned.Changes[n] = i.Changes[n].Clone()
	}
	return cloned
}

type DeploymentVerifier interface {
	Verify(context.Context, DeploymentVerificationRequest) (DeploymentObservation, error)
}

type MergeRequest struct {
	CaseID         string            `json:"case_id"`
	FixCommits     map[string]string `json:"fix_commits"`
	TargetBranches map[string]string `json:"target_branches"`
	Changes        []CodeChange      `json:"changes,omitempty"`
}

func (r MergeRequest) Clone() MergeRequest {
	r.FixCommits = CloneStringMap(r.FixCommits)
	r.TargetBranches = CloneStringMap(r.TargetBranches)
	cloned := make([]CodeChange, len(r.Changes))
	for i := range r.Changes {
		cloned[i] = r.Changes[i].Clone()
	}
	r.Changes = cloned
	return r
}

type MergeResult struct {
	Pushed       bool                             `json:"pushed"`
	Conflict     bool                             `json:"conflict"`
	MergeCommits map[string]string                `json:"merge_commits"`
	Repositories map[string]MergeRepositoryResult `json:"repositories,omitempty"`
	ErrorMessage string                           `json:"error_message,omitempty"`
}

func (r MergeResult) Clone() MergeResult {
	r.MergeCommits = CloneStringMap(r.MergeCommits)
	if r.Repositories != nil {
		cloned := make(map[string]MergeRepositoryResult, len(r.Repositories))
		for repo, result := range r.Repositories {
			cloned[repo] = result
		}
		r.Repositories = cloned
	}
	return r
}

type MergeInspection struct {
	FixPushed    bool                             `json:"fix_pushed"`
	MergePushed  bool                             `json:"merge_pushed"`
	Conflict     bool                             `json:"conflict"`
	MergeCommits map[string]string                `json:"merge_commits"`
	Repositories map[string]MergeRepositoryResult `json:"repositories,omitempty"`
}

type MergeRepositoryResult struct {
	MergeCommit string `json:"merge_commit,omitempty"`
	Pushed      bool   `json:"pushed"`
	Conflict    bool   `json:"conflict"`
	Error       string `json:"error,omitempty"`
}

func (i MergeInspection) Clone() MergeInspection {
	i.MergeCommits = CloneStringMap(i.MergeCommits)
	if i.Repositories != nil {
		cloned := make(map[string]MergeRepositoryResult, len(i.Repositories))
		for repo, result := range i.Repositories {
			cloned[repo] = result
		}
		i.Repositories = cloned
	}
	return i
}

type DeploymentVerificationRequest struct {
	CaseID          string            `json:"case_id"`
	Environment     string            `json:"environment"`
	ExpectedCommits map[string]string `json:"expected_commits"`
	ObservedVersion string            `json:"observed_version,omitempty"`
	ObservedCommits map[string]string `json:"observed_commits,omitempty"`
}

type DeploymentReservation struct {
	ReservationID           string                        `json:"reservation_id"`
	ReservationKey          string                        `json:"reservation_key"`
	OriginalExpectedVersion int64                         `json:"original_expected_version"`
	CycleNumber             int                           `json:"cycle_number"`
	Environment             string                        `json:"environment"`
	ExpectedCommits         map[string]string             `json:"expected_commits"`
	Bug                     Bug                           `json:"bug"`
	Bot                     BotRef                        `json:"bot"`
	VerifierInput           DeploymentVerificationRequest `json:"verifier_input"`
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
	CodeChanges     []CodeChange
}

type CaseOrchestrator struct {
	store           *CaseStore
	runner          PhaseRunner
	git             GitIntegration
	deployment      DeploymentVerifier
	mu              sync.Mutex
	recoveryStarted map[string]struct{}
	scheduleTimeout time.Duration
}

func NewCaseOrchestrator(store *CaseStore, runner PhaseRunner, git GitIntegration, deployment DeploymentVerifier) *CaseOrchestrator {
	return &CaseOrchestrator{store: store, runner: runner, git: git, deployment: deployment, recoveryStarted: make(map[string]struct{}), scheduleTimeout: 30 * time.Second}
}

func (o *CaseOrchestrator) startPhase(attempt PhaseAttempt, bug Bug, bot BotRef) error {
	timeout := o.scheduleTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return o.runner.Start(ctx, attempt.Clone(), bug, bot)
}

func (o *CaseOrchestrator) wasRecoveryStarted(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.recoveryStarted[id]
	return ok
}
func (o *CaseOrchestrator) markRecoveryStarted(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recoveryStarted[id] = struct{}{}
}

func (o *CaseOrchestrator) StartCase(ctx context.Context, cmd StartCaseCommand) (IncidentCase, error) {
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
	attempt := newAttempt(incident, PhaseFix, "", cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, incident.CurrentAttemptID)
	update := CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(attempt.ID), SelectedBotKey: workflowStringPtr(cmd.Bot.Key)}
	payload := mustJSON(map[string]string{"attempt_id": attempt.ID, "root_cause_attempt_id": cmd.RootCauseAttemptID})
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), Approvals: []Approval{approval}, CreateAttempts: []PhaseAttempt{attempt}, Snapshot: update, Steps: []CaseMutationStep{{To: CaseFixing, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "fix_approved", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay {
		return mutation.Case, nil
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, attempt, cmd.IdempotencyKey, errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(attempt, cmd.Bug, cmd.Bot); err != nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, attempt, cmd.IdempotencyKey, err)
	}
	return mutation.Case, nil
}

func (o *CaseOrchestrator) ApproveMerge(ctx context.Context, cmd ApproveMergeCommand) (IncidentCase, error) {
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
	_, hasReservation, reservationErr := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey)
	if reservationErr != nil {
		return IncidentCase{}, reservationErr
	}
	fixAttempt, err := o.store.GetAttempt(ctx, incident.CurrentAttemptID)
	if err != nil || fixAttempt.Phase != PhaseFix || fixAttempt.Status != AttemptStatusSucceeded || fixAttempt.CycleNumber != incident.CycleNumber {
		return IncidentCase{}, ErrApprovalScope
	}
	changes, err := o.store.ListCodeChanges(ctx, incident.ID)
	if err != nil {
		return IncidentCase{}, err
	}
	fixes, targets := map[string]string{}, map[string]string{}
	selected := []CodeChange{}
	var scopeValue MergeApprovalScope
	if hasReservation {
		approvals, listErr := o.store.ListApprovals(ctx, incident.ID)
		if listErr != nil {
			return IncidentCase{}, listErr
		}
		approvalID := stableID("approval", cmd.IdempotencyKey)
		foundApproval := false
		for _, stored := range approvals {
			if stored.ID == approvalID {
				if json.Unmarshal(stored.ScopeJSON, &scopeValue) != nil {
					return IncidentCase{}, ErrApprovalScope
				}
				foundApproval = true
				break
			}
		}
		if !foundApproval {
			return IncidentCase{}, ErrApprovalScope
		}
		byID := map[string]CodeChange{}
		for _, change := range changes {
			byID[change.ID] = change
		}
		for _, approved := range scopeValue.CodeChanges {
			change, ok := byID[approved.ID]
			if !ok {
				return IncidentCase{}, ErrApprovalScope
			}
			fixes[change.Repo] = change.FixCommit
			targets[change.Repo] = change.TargetEnvironmentBranch
			selected = append(selected, change)
		}
	} else {
		for _, change := range changes {
			if change.AttemptID == incident.CurrentAttemptID && change.PushStatus == "pushed" && change.TargetEnvironmentBranch != "" {
				fixes[change.Repo] = change.FixCommit
				targets[change.Repo] = change.TargetEnvironmentBranch
				selected = append(selected, change)
			}
		}
	}
	if len(selected) == 0 || !sameStringMapKeys(fixes, targets) {
		return IncidentCase{}, ErrApprovalScope
	}
	approvedChanges := make([]ApprovedCodeChange, 0, len(selected))
	for _, change := range selected {
		approvedChanges = append(approvedChanges, ApprovedCodeChange{ID: change.ID, Repo: change.Repo, FixCommit: change.FixCommit, TargetBranch: change.TargetEnvironmentBranch})
	}
	sort.Slice(approvedChanges, func(i, j int) bool { return approvedChanges[i].Repo < approvedChanges[j].Repo })
	if !hasReservation {
		scopeValue = MergeApprovalScope{CycleNumber: incident.CycleNumber, FixAttemptID: incident.CurrentAttemptID, CodeChanges: approvedChanges}
	}
	scope, _ := json.Marshal(scopeValue)
	approval := Approval{ID: stableID("approval", cmd.IdempotencyKey), CaseID: incident.ID, Kind: ApprovalMergeEnvironmentBranch, Actor: cmd.ActorID, CaseVersion: incident.Version, ScopeJSON: scope, FixCommits: fixes, TargetBranches: targets}
	payload := mustJSON(map[string]any{"fix_commits": fixes, "target_branches": targets})
	reserved, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(map[string]any{"command": cmd, "derived_fixes": fixes, "derived_targets": targets}), Approvals: []Approval{approval}, Steps: []CaseMutationStep{{To: CaseMerging, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "merge_approved", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	if reserved.Replay {
		current, loadErr := o.store.GetCase(ctx, incident.ID)
		if loadErr != nil || current.Status != CaseMerging {
			return current, loadErr
		}
		return o.recoverReservedMerge(ctx, current, cmd.IdempotencyKey, selected, MergeRequest{CaseID: incident.ID, FixCommits: fixes, TargetBranches: targets})
	}
	if o.git == nil {
		return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, errors.New("git integration is unavailable"))
	}
	request := MergeRequest{CaseID: incident.ID, FixCommits: fixes, TargetBranches: targets}
	result, callErr := o.git.MergeAndPush(ctx, request)
	allPushed := len(result.Repositories) == len(selected)
	for index := range selected {
		repoResult, ok := result.Repositories[selected[index].Repo]
		if !ok {
			allPushed = false
			selected[index].PushStatus = "push_unknown"
			continue
		}
		if repoResult.MergeCommit != "" {
			selected[index].MergeCommit = repoResult.MergeCommit
		}
		if repoResult.Conflict {
			result.Conflict = true
			selected[index].PushStatus = "conflict"
			allPushed = false
		} else if repoResult.Pushed && repoResult.MergeCommit != "" {
			selected[index].PushStatus = "pushed"
		} else {
			allPushed = false
			if repoResult.MergeCommit != "" {
				selected[index].PushStatus = "merge_local"
			} else {
				selected[index].PushStatus = "push_unknown"
			}
		}
	}
	if callErr != nil {
		inspectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		inspection, inspectErr := o.git.Inspect(inspectCtx, request)
		cancel()
		if inspectErr == nil && inspection.Conflict {
			result.Conflict = true
		}
		if result.Conflict {
			return o.recordMergeConflict(reserved.Case, cmd.IdempotencyKey, callErr)
		}
		return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, callErr)
	}
	if result.Conflict {
		return o.recordMergeConflict(reserved.Case, cmd.IdempotencyKey, errors.New("merge conflict"))
	}
	if !allPushed {
		return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, errors.New("merge push is incomplete"))
	}
	if result.MergeCommits == nil {
		result.MergeCommits = map[string]string{}
	}
	for index := range selected {
		result.MergeCommits[selected[index].Repo] = selected[index].MergeCommit
	}
	completedKey := cmd.IdempotencyKey + ":completed"
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: reserved.Case.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(result), CodeChanges: selected, Steps: []CaseMutationStep{{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_pushed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(result)}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	return done.Case, nil
}

func (o *CaseOrchestrator) recoverReservedMerge(ctx context.Context, incident IncidentCase, key string, changes []CodeChange, request MergeRequest) (IncidentCase, error) {
	if o.git == nil {
		return incident, nil
	}
	inspection, err := o.git.Inspect(ctx, request)
	if err != nil {
		return incident, err
	}
	return o.resumeInspectedMerge(ctx, incident, key, changes, request, inspection)
}

func (o *CaseOrchestrator) resumeInspectedMerge(ctx context.Context, incident IncidentCase, key string, changes []CodeChange, request MergeRequest, inspection MergeInspection) (IncidentCase, error) {
	if inspection.Conflict {
		return o.recordMergeConflict(incident, key, errors.New("merge conflict confirmed"))
	}
	allPushed := true
	hasLocal := false
	for i := range changes {
		repoResult, ok := inspection.Repositories[changes[i].Repo]
		if !ok {
			repoResult.MergeCommit = inspection.MergeCommits[changes[i].Repo]
			repoResult.Pushed = inspection.MergePushed && repoResult.MergeCommit != ""
		}
		if repoResult.Conflict {
			return o.recordMergeConflict(incident, key, errors.New("merge conflict confirmed"))
		}
		if repoResult.MergeCommit != "" {
			changes[i].MergeCommit = repoResult.MergeCommit
		}
		if repoResult.Pushed && changes[i].MergeCommit != "" {
			changes[i].PushStatus = "pushed"
		} else {
			allPushed = false
			if changes[i].MergeCommit != "" {
				hasLocal = true
				changes[i].PushStatus = "merge_local"
			} else {
				changes[i].PushStatus = "push_unknown"
			}
		}
	}
	if !allPushed && hasLocal {
		request.Changes = changes
		pushResult, pushErr := o.git.ResumePush(ctx, request)
		if pushErr != nil {
			return o.recordMergeAmbiguous(incident, key, changes, pushErr)
		}
		allPushed = true
		for i := range changes {
			repoResult, ok := pushResult.Repositories[changes[i].Repo]
			if !ok || !repoResult.Pushed || repoResult.MergeCommit == "" {
				allPushed = false
				continue
			}
			changes[i].MergeCommit = repoResult.MergeCommit
			changes[i].PushStatus = "pushed"
		}
	}
	if !allPushed {
		return o.recordMergeAmbiguous(incident, key, changes, errors.New("merge push remains incomplete"))
	}
	completedKey := key + ":completed"
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(changes), CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_push_resumed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(changes)}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	return done.Case, nil
}

func (o *CaseOrchestrator) recordMergeConflict(incident IncidentCase, key string, cause error) (IncidentCase, error) {
	k := key + ":conflict"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: k, RequestJSON: mustJSON(map[string]string{"error": cause.Error()}), Steps: []CaseMutationStep{{To: CaseMergeConflict, Event: TransitionEvent{ID: stableID("event", k), EventType: "merge_conflict", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(map[string]string{"error": cause.Error()})}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return m.Case, cause
}
func (o *CaseOrchestrator) recordMergeAmbiguous(incident IncidentCase, key string, changes []CodeChange, cause error) (IncidentCase, error) {
	k := key + ":push-ambiguous"
	for i := range changes {
		if (changes[i].PushStatus == "pushed" && changes[i].MergeCommit != "") || changes[i].PushStatus == "conflict" || changes[i].PushStatus == "merge_local" || changes[i].PushStatus == "push_unknown" {
			continue
		} else if changes[i].MergeCommit != "" {
			changes[i].PushStatus = "merge_local"
		} else {
			changes[i].PushStatus = "push_unknown"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := mustJSON(map[string]string{"error": cause.Error()})
	m, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: k, RequestJSON: payload, CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseMerging, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", k), EventType: "merge_push_ambiguous", ActorType: "git", ActorID: "git-integration", PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return m.Case, cause
}

func (o *CaseOrchestrator) latestMergeDeploymentScope(ctx context.Context, incident IncidentCase) (MergeApprovalScope, []CodeChange, map[string]string, error) {
	approvals, err := o.store.ListApprovals(ctx, incident.ID)
	if err != nil {
		return MergeApprovalScope{}, nil, nil, err
	}
	var scope MergeApprovalScope
	found := false
	for i := len(approvals) - 1; i >= 0; i-- {
		if approvals[i].Kind != ApprovalMergeEnvironmentBranch {
			continue
		}
		var candidate MergeApprovalScope
		if json.Unmarshal(approvals[i].ScopeJSON, &candidate) == nil && candidate.CycleNumber == incident.CycleNumber && candidate.FixAttemptID == incident.CurrentAttemptID {
			scope = candidate
			found = true
			break
		}
	}
	if !found {
		return scope, nil, nil, ErrApprovalScope
	}
	all, err := o.store.ListCodeChanges(ctx, incident.ID)
	if err != nil {
		return scope, nil, nil, err
	}
	byID := map[string]CodeChange{}
	for _, change := range all {
		byID[change.ID] = change
	}
	selected := make([]CodeChange, 0, len(scope.CodeChanges))
	expected := map[string]string{}
	for _, approved := range scope.CodeChanges {
		change, ok := byID[approved.ID]
		if !ok || change.AttemptID != scope.FixAttemptID || change.Repo != approved.Repo || change.FixCommit != approved.FixCommit || change.TargetEnvironmentBranch != approved.TargetBranch || change.PushStatus != "pushed" || change.MergeCommit == "" {
			return scope, nil, nil, ErrApprovalScope
		}
		selected = append(selected, change)
		expected[change.Repo] = change.MergeCommit
	}
	if len(expected) != len(scope.CodeChanges) {
		return scope, nil, nil, ErrApprovalScope
	}
	return scope, selected, expected, nil
}

func (o *CaseOrchestrator) NotifyDeployed(ctx context.Context, cmd NotifyDeployedCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	reserveKey := fmt.Sprintf("deployment-reserve:%s:v%d", cmd.CaseID, cmd.ExpectedVersion)
	release := workflowCommandLocks.acquire(reserveKey)
	defer release()
	var reservation DeploymentReservation
	reservationEvent, found, err := o.store.GetEventByIdempotencyKey(ctx, reserveKey)
	if err != nil {
		return IncidentCase{}, err
	}
	incident, err := o.store.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return IncidentCase{}, err
	}
	if found {
		if err := json.Unmarshal(reservationEvent.PayloadJSON, &reservation); err != nil {
			return IncidentCase{}, err
		}
		supplied := reservation.VerifierInput
		supplied.ObservedVersion = cmd.ObservedVersion
		supplied.ObservedCommits = CloneStringMap(cmd.ObservedCommits)
		if !reflect.DeepEqual(supplied, reservation.VerifierInput) || !reflect.DeepEqual(cmd.Bot, reservation.Bot) || !reflect.DeepEqual(cmd.Bug, reservation.Bug) {
			return IncidentCase{}, ErrIdempotencyConflict
		}
		if _, resultFound, resultErr := o.store.GetEventByIdempotencyKey(ctx, reserveKey+":result"); resultErr != nil {
			return IncidentCase{}, resultErr
		} else if resultFound {
			return incident, nil
		}
	} else {
		if incident.Version != cmd.ExpectedVersion {
			return IncidentCase{}, ErrCaseVersionConflict
		}
		if incident.Status != CaseWaitingDeployment {
			return IncidentCase{}, ErrApprovalNotReady
		}
		scope, _, expected, scopeErr := o.latestMergeDeploymentScope(ctx, incident)
		if scopeErr != nil {
			return IncidentCase{}, scopeErr
		}
		request := DeploymentVerificationRequest{CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: expected, ObservedVersion: cmd.ObservedVersion, ObservedCommits: CloneStringMap(cmd.ObservedCommits)}
		reservation = DeploymentReservation{ReservationID: stableID("deployment-reservation", reserveKey), ReservationKey: reserveKey, OriginalExpectedVersion: cmd.ExpectedVersion, CycleNumber: scope.CycleNumber, Environment: incident.Environment, ExpectedCommits: expected, Bug: cmd.Bug, Bot: cmd.Bot, VerifierInput: request}
		payload := mustJSON(reservation)
		reserved, reserveErr := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: cmd.ExpectedVersion, IdempotencyKey: reserveKey, RequestJSON: payload, Steps: []CaseMutationStep{{To: CaseDeploymentUnverified, Event: TransitionEvent{ID: stableID("event", reserveKey), EventType: "deployment_verification_reserved", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}, {To: CaseDeploymentUnverified, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", reserveKey+":start"), EventType: "deployment_verification_started", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: payload}}}})
		if reserveErr != nil {
			return IncidentCase{}, reserveErr
		}
		incident = reserved.Case
	}
	request := reservation.VerifierInput
	if o.deployment == nil {
		return o.recordDeploymentResult(incident, reservation, DeploymentObservation{Result: DeploymentResultUnavailable, VerificationSource: "deployment-verifier"}, errors.New("deployment verifier unavailable"))
	}
	observation, verifyErr := o.deployment.Verify(ctx, request)
	return o.recordDeploymentResult(incident, reservation, observation, verifyErr)
}

func (o *CaseOrchestrator) recordDeploymentResult(incident IncidentCase, reservation DeploymentReservation, observation DeploymentObservation, verifyErr error) (IncidentCase, error) {
	durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	observation.ID = stableID("deployment", reservation.ReservationKey)
	observation.CaseID = incident.ID
	observation.Environment = incident.Environment
	observation.ExpectedCommits = CloneStringMap(reservation.ExpectedCommits)
	observation.UserNotifiedAt = &now
	if observation.VerificationSource == "" {
		observation.VerificationSource = "deployment-verifier"
	}
	key := reservation.ReservationKey + ":result"
	steps := []CaseMutationStep{}
	creates := []PhaseAttempt{}
	update := CaseSnapshotUpdate{}
	if observation.Result == DeploymentResultMatched && verifyErr == nil {
		steps = append(steps, CaseMutationStep{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", key), EventType: "deployment_verification_completed", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}}, CaseMutationStep{To: CaseDeploymentVerified, Event: TransitionEvent{ID: stableID("event", key+":verified"), EventType: "deployment_verified", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}})
		attempt := newAttempt(incident, PhaseRegression, AttemptRegression, key+":regression", reservation.Bot, mustJSON(reservation.VerifierInput), incident.CurrentAttemptID)
		creates = append(creates, attempt)
		update.CurrentAttemptID = workflowStringPtr(attempt.ID)
		update.SelectedBotKey = workflowStringPtr(reservation.Bot.Key)
		steps = append(steps, CaseMutationStep{To: CaseRegressionValidating, Event: TransitionEvent{ID: stableID("event", key+":regression"), EventType: "regression_started", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}})
	} else {
		if verifyErr != nil {
			observation.Result = DeploymentResultUnavailable
		}
		steps = append(steps, CaseMutationStep{To: CaseDeploymentUnverified, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", key), EventType: "deployment_unverified", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}})
	}
	mutation, err := o.store.ApplyCaseMutation(durable, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]any{"observation": observation, "error": fmt.Sprint(verifyErr)}), Observations: []DeploymentObservation{observation}, CreateAttempts: creates, Snapshot: update, Steps: steps})
	if err != nil {
		return IncidentCase{}, errors.Join(verifyErr, err)
	}
	if len(creates) > 0 && !mutation.Replay && o.runner != nil {
		if startErr := o.startPhase(creates[0], reservation.Bug, reservation.Bot); startErr != nil {
			return o.phaseScheduleFailure(context.Background(), mutation.Case, creates[0], key+":regression", startErr)
		}
	}
	return mutation.Case, verifyErr
}

func (o *CaseOrchestrator) CancelAttempt(ctx context.Context, cmd CancelAttemptCommand) (IncidentCase, error) {
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
	to := failureStateForPhase(attempt.Phase)
	payload := mustJSON(map[string]string{"attempt_id": attempt.ID})
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "attempt_cancelled", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay {
		return mutation.Case, nil
	}
	if o.runner != nil {
		err = o.runner.Cancel(ctx, attempt.ID)
	}
	return mutation.Case, err
}

func (o *CaseOrchestrator) CompleteAttempt(ctx context.Context, cmd CompleteAttemptCommand) (IncidentCase, error) {
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
	return o.applyOutcome(ctx, incident, attempt, cmd)
}

func (o *CaseOrchestrator) applyOutcome(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, cmd CompleteAttemptCommand) (IncidentCase, error) {
	if attempt.FinishedAt == nil {
		now := time.Now().UTC()
		attempt.FinishedAt = &now
	}
	actor := cmd.ActorID
	steps := []CaseMutationStep{}
	var next *PhaseAttempt
	update := CaseSnapshotUpdate{}
	add := func(to CaseStatus, eventType, actorType, actorID string, payload any) {
		steps = append(steps, CaseMutationStep{To: to, Event: TransitionEvent{ID: stableID("event", fmt.Sprintf("%s:%d", cmd.IdempotencyKey, len(steps))), EventType: eventType, ActorType: actorType, ActorID: actorID, PayloadJSON: mustJSON(payload)}})
	}
	switch cmd.Outcome {
	case PhaseOutcomeReproduced:
		add(CaseReproduced, "validation_reproduced", "agent", actor, cmd.OutputJSON)
		add(CaseInvestigating, "investigation_started", "studio", "orchestrator", map[string]string{"parent_attempt_id": attempt.ID})
		created := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, []byte(`{}`), attempt.ID)
		next = &created
		update.CurrentAttemptID = workflowStringPtr(created.ID)
	case PhaseOutcomeNotReproduced:
		add(CaseNotReproduced, "validation_not_reproduced", "agent", actor, cmd.OutputJSON)
	case PhaseOutcomeNeedsEvidence:
		add(CaseWaitingEvidence, "evidence_required", "agent", actor, cmd.OutputJSON)
	case PhaseOutcomeRootCauseReady:
		add(CaseRootCauseReady, "root_cause_ready", "agent", actor, cmd.OutputJSON)
		add(CaseWaitingFixApproval, "fix_approval_requested", "studio", "orchestrator", map[string]string{"attempt_id": attempt.ID})
	case PhaseOutcomeFixPushed:
		add(CaseFixPushed, "fix_pushed", "agent", actor, cmd.OutputJSON)
		add(CaseWaitingMergeApproval, "merge_approval_requested", "studio", "orchestrator", map[string]string{"attempt_id": attempt.ID})
	case PhaseOutcomeFixFailed:
		add(CaseFixFailed, "fix_failed", "agent", actor, cmd.OutputJSON)
	case PhaseOutcomeFixedVerified:
		add(CaseFixedVerified, "regression_fixed", "agent", actor, cmd.OutputJSON)
		update.ClosedAtSet = true
		update.ClosedAt = cloneTimePtr(attempt.FinishedAt)
	case PhaseOutcomeStillReproduces:
		cycle := incident.CycleNumber + 1
		add(CaseStillReproduces, "regression_failed", "agent", actor, cmd.OutputJSON)
		add(CaseInvestigating, "next_cycle_investigation_started", "studio", "orchestrator", map[string]int{"cycle": cycle})
		created := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, []byte(`{}`), attempt.ID)
		created.CycleNumber = cycle
		next = &created
		update.CycleNumber = &cycle
		update.CurrentAttemptID = workflowStringPtr(created.ID)
	default:
		return IncidentCase{}, fmt.Errorf("unsupported phase outcome %q", cmd.Outcome)
	}
	creates := []PhaseAttempt{}
	if next != nil {
		creates = append(creates, *next)
	}
	request := mustJSON(cmd)
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: request, FinishAttempts: []PhaseAttempt{attempt}, CreateAttempts: creates, CodeChanges: cmd.CodeChanges, Snapshot: update, Steps: steps})
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay || next == nil {
		return mutation.Case, nil
	}
	bug := Bug{ID: mutation.Case.BugID, Source: mutation.Case.Source, SystemID: mutation.Case.SystemID, Env: mutation.Case.Environment}
	bot := BotRef{Key: next.BotKey, Target: next.AgentTarget}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, *next, cmd.IdempotencyKey+":next", errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(*next, bug, bot); err != nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, *next, cmd.IdempotencyKey+":next", err)
	}
	return mutation.Case, nil
}

func (o *CaseOrchestrator) beginPhase(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string) (IncidentCase, error) {
	return o.beginPhaseWithUpdate(ctx, incident, to, attempt, bug, bot, key, actor, eventType, CaseSnapshotUpdate{})
}

func (o *CaseOrchestrator) beginPhaseWithUpdate(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string, update CaseSnapshotUpdate) (IncidentCase, error) {
	update.CurrentAttemptID = workflowStringPtr(attempt.ID)
	update.SelectedBotKey = workflowStringPtr(bot.Key)
	request, _ := json.Marshal(map[string]any{"attempt": attempt, "to": to, "event_type": eventType, "actor": actor})
	actorType := "user"
	if actor == "recovery" {
		actorType = "recovery"
	} else if actor == "studio" {
		actorType = "studio"
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: request, CreateAttempts: []PhaseAttempt{attempt}, Snapshot: update, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", key), EventType: eventType, ActorType: actorType, ActorID: actor, PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID})}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay {
		return mutation.Case, nil
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, attempt, key, errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(attempt, bug, bot); err != nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, attempt, key, err)
	}
	return mutation.Case, nil
}

func (o *CaseOrchestrator) phaseScheduleFailure(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, key string, cause error) (IncidentCase, error) {
	attempt.Status, attempt.OutputJSON, attempt.ErrorCode, attempt.ErrorMessage = AttemptStatusFailed, []byte(`{}`), "schedule_failed", cause.Error()
	failureKey := key + ":schedule-failed"
	request := mustJSON(map[string]string{"error": cause.Error(), "attempt_id": attempt.ID})
	failureCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mutation, err := o.store.ApplyCaseMutation(failureCtx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: failureKey, RequestJSON: request, FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: failureStateForPhase(attempt.Phase), Event: TransitionEvent{ID: stableID("event", failureKey), EventType: "phase_schedule_failed", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: request}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return mutation.Case, fmt.Errorf("schedule phase %s: %w", attempt.Phase, cause)
}

func mustJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

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
	actorType := "user"
	switch actor {
	case "recovery":
		actorType = "recovery"
	case "studio":
		actorType = "studio"
	case "git":
		actorType = "git"
	}
	event := TransitionEvent{ID: stableID("event", key), IdempotencyKey: key, EventType: eventType, ActorType: actorType, ActorID: actor, PayloadJSON: encoded}
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
