package bughub

import (
	"bytes"
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
	ErrApprovalNotReady      = errors.New("workflow approval is not ready")
	ErrApprovalScope         = errors.New("workflow approval scope is invalid")
	ErrAttemptNotCurrent     = errors.New("phase attempt is not current")
	ErrCancelWorkerSaturated = errors.New("external cancel worker capacity is saturated")
)

// cancelWorkerCapacity bounds context-ignoring PhaseRunner.Cancel calls owned
// by one orchestrator. A slot is held until the dependency actually returns.
const cancelWorkerCapacity = 4

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

// RecoveryContextResolver reloads the full persisted Bug/Bot execution
// context before a recovered or automatically-created phase is scheduled.
// In particular, Bot.Path must point at the installed workspace; reconstructing
// only key/target is not sufficient for CLI execution.
type RecoveryContextResolver interface {
	ResolveRecoveryContext(context.Context, IncidentCase, PhaseAttempt) (Bug, BotRef, error)
}

type RecoveryContextResolverFunc func(context.Context, IncidentCase, PhaseAttempt) (Bug, BotRef, error)

func (fn RecoveryContextResolverFunc) ResolveRecoveryContext(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
	return fn(ctx, incident, attempt)
}

type MergeRequest struct {
	CaseID         string            `json:"case_id"`
	FixCommits     map[string]string `json:"fix_commits"`
	TargetBranches map[string]string `json:"target_branches"`
	Changes        []CodeChange      `json:"changes,omitempty"`
	TargetHeads    map[string]string `json:"target_heads,omitempty"`
}

func (r MergeRequest) Clone() MergeRequest {
	r.FixCommits = CloneStringMap(r.FixCommits)
	r.TargetBranches = CloneStringMap(r.TargetBranches)
	r.TargetHeads = CloneStringMap(r.TargetHeads)
	cloned := make([]CodeChange, len(r.Changes))
	for i := range r.Changes {
		cloned[i] = r.Changes[i].Clone()
	}
	r.Changes = cloned
	return r
}

type MergeResult struct {
	Pushed               bool                             `json:"pushed"`
	Conflict             bool                             `json:"conflict"`
	MergeCommits         map[string]string                `json:"merge_commits"`
	BaselineMergeCommits map[string]string                `json:"baseline_merge_commits,omitempty"`
	Repositories         map[string]MergeRepositoryResult `json:"repositories,omitempty"`
	ErrorMessage         string                           `json:"error_message,omitempty"`
}

func (r MergeResult) Clone() MergeResult {
	r.MergeCommits = CloneStringMap(r.MergeCommits)
	r.BaselineMergeCommits = CloneStringMap(r.BaselineMergeCommits)
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
	TargetHead  string `json:"target_head,omitempty"`
	ApprovalKey string `json:"approval_key,omitempty"`
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

type StartCaseCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	Bug             Bug
	Bot             BotRef
	InputJSON       json.RawMessage
}

type ResetCaseCommand struct {
	CaseID          string
	NewCaseID       string
	IdempotencyKey  string
	ActorID         string
	ExpectedVersion int64
	Bug             Bug
	Bot             BotRef
	FrontendEntry   FrontendEntryBinding
	FrontendEntries []FrontendEntryBinding
	InputJSON       json.RawMessage
}

// WorkflowWarning reports a durable workflow side-effect that needs operator
// attention without turning the already-committed command into a retryable
// failure.
type WorkflowWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResetCaseOutcome keeps the historical ResetCase API compatible while giving
// newer callers structured warnings about external runner cancellation.
type ResetCaseOutcome struct {
	Case     IncidentCase      `json:"case"`
	Warnings []WorkflowWarning `json:"warnings"`
}

// CreateAndStartCaseCommand is the production entrypoint for a Bug that does
// not have a durable Case yet. ExpectedVersion is zero only for first
// creation. When CaseID names an immutable legacy archive, the command creates
// a deterministic new Case for the next cycle and leaves the archive intact.
type CreateAndStartCaseCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	Bug             Bug
	Bot             BotRef
	FrontendEntry   FrontendEntryBinding
	FrontendEntries []FrontendEntryBinding
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

type CompleteRemediationCommand struct {
	CaseID             string
	ExpectedVersion    int64
	IdempotencyKey     string
	ActorID            string
	RootCauseAttemptID string
	Summary            string
	Evidence           string
	Bug                Bug
	Bot                BotRef
}

type ApproveMergeCommand struct {
	CaseID          string
	ExpectedVersion int64
	IdempotencyKey  string
	ActorID         string
	FixCommits      map[string]string
	TargetBranches  map[string]string
	TargetHeads     map[string]string
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
	PhaseOutcomeReproduced                 PhaseOutcome = "reproduced"
	PhaseOutcomeNotReproduced              PhaseOutcome = "not_reproduced"
	PhaseOutcomeNeedsEvidence              PhaseOutcome = "needs_evidence"
	PhaseOutcomeValidationEvidenceRequired PhaseOutcome = "validation_evidence_required"
	PhaseOutcomeSystemFailed               PhaseOutcome = "system_failed"
	PhaseOutcomeRootCauseReady             PhaseOutcome = "root_cause_ready"
	PhaseOutcomeFixPushed                  PhaseOutcome = "fix_pushed"
	PhaseOutcomeFixFailed                  PhaseOutcome = "fix_failed"
	PhaseOutcomeFixedVerified              PhaseOutcome = "fixed_verified"
	PhaseOutcomeStillReproduces            PhaseOutcome = "still_reproduces"
)

type CompleteAttemptCommand struct {
	CaseID             string
	AttemptID          string
	ExpectedVersion    int64
	IdempotencyKey     string
	ActorID            string
	Outcome            PhaseOutcome
	OutputJSON         json.RawMessage
	ErrorCode          string
	ErrorMessage       string
	Usage              AgentUsage
	CodeChanges        []CodeChange
	remoteFixInspected bool
}

type CaseOrchestrator struct {
	store            *CaseStore
	runner           PhaseRunner
	git              GitIntegration
	recoveryContext  RecoveryContextResolver
	recoveryContexts map[string]resolvedRecoveryContext
	mu               sync.Mutex
	recoveryStarted  map[string]struct{}
	scheduleTimeout  time.Duration
	cancelTimeout    time.Duration
	cancelWorkers    chan struct{}
}

func NewCaseOrchestrator(store *CaseStore, runner PhaseRunner, git GitIntegration) *CaseOrchestrator {
	return &CaseOrchestrator{store: store, runner: runner, git: git, recoveryStarted: make(map[string]struct{}), recoveryContexts: make(map[string]resolvedRecoveryContext), scheduleTimeout: 30 * time.Second, cancelTimeout: 30 * time.Second, cancelWorkers: make(chan struct{}, cancelWorkerCapacity)}
}

type resolvedRecoveryContext struct {
	bug Bug
	bot BotRef
}

func (o *CaseOrchestrator) SetRecoveryContextResolver(resolver RecoveryContextResolver) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recoveryContext = resolver
}

func (o *CaseOrchestrator) resolveRecoveryContext(ctx context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
	o.mu.Lock()
	if cached, ok := o.recoveryContexts[recoveryContextKey(incident, attempt)]; ok {
		o.mu.Unlock()
		return cached.bug, cached.bot, nil
	}
	resolver := o.recoveryContext
	o.mu.Unlock()
	return resolveRecoveryContextWith(ctx, resolver, incident, attempt)
}

func resolveRecoveryContextWith(ctx context.Context, resolver RecoveryContextResolver, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
	if resolver == nil {
		return Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, nil
	}
	bug, bot, err := resolver.ResolveRecoveryContext(ctx, incident.Clone(), attempt.Clone())
	if err != nil {
		return Bug{}, BotRef{}, err
	}
	if strings.TrimSpace(bug.ID) != incident.BugID {
		return Bug{}, BotRef{}, fmt.Errorf("resolved Bug %q does not match Case Bug %q", bug.ID, incident.BugID)
	}
	if strings.TrimSpace(bot.Key) != attempt.BotKey || strings.TrimSpace(bot.Target) != attempt.AgentTarget {
		return Bug{}, BotRef{}, fmt.Errorf("resolved Bot %q/%q does not match attempt Bot %q/%q", bot.Key, bot.Target, attempt.BotKey, attempt.AgentTarget)
	}
	if strings.TrimSpace(bot.Path) == "" {
		return Bug{}, BotRef{}, errors.New("resolved recovery Bot workspace path is required")
	}
	return bug, bot, nil
}

func recoveryContextKey(incident IncidentCase, attempt PhaseAttempt) string {
	return incident.ID + "\x1f" + attempt.BotKey + "\x1f" + attempt.AgentTarget
}

func (o *CaseOrchestrator) setRecoveryContexts(contexts map[string]resolvedRecoveryContext) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recoveryContexts = contexts
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
	if incident.Status != CasePendingInvestigation {
		return IncidentCase{}, &ErrInvalidTransition{From: incident.Status, To: CaseInvestigating}
	}
	attempt := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, "")
	return o.beginPhase(ctx, incident, CaseInvestigating, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "investigation_started")
}

func (o *CaseOrchestrator) ResetCase(ctx context.Context, cmd ResetCaseCommand) (IncidentCase, error) {
	outcome, err := o.ResetCaseWithOutcome(ctx, cmd)
	if err == nil && hasWorkflowWarning(outcome.Warnings, "reset_replacement_start_failed") {
		err = errors.New("replacement Case phase start failed; retry validation from the preserved Case")
	}
	return outcome.Case, err
}

func (o *CaseOrchestrator) ResetCaseWithOutcome(ctx context.Context, cmd ResetCaseCommand) (ResetCaseOutcome, error) {
	if o == nil || o.store == nil {
		return ResetCaseOutcome{}, errors.New("case orchestrator store is required")
	}
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return ResetCaseOutcome{}, err
	}
	if err := validateNewWorkflowCaseID(cmd.NewCaseID); err != nil {
		return ResetCaseOutcome{}, fmt.Errorf("replacement Case ID: %w", err)
	}
	if cmd.CaseID == cmd.NewCaseID {
		return ResetCaseOutcome{}, errors.New("replacement Case ID must differ from archived Case ID")
	}
	if strings.TrimSpace(cmd.Bug.ID) == "" || strings.TrimSpace(cmd.Bot.Key) == "" || strings.TrimSpace(cmd.Bot.Target) == "" {
		return ResetCaseOutcome{}, errors.New("Bug ID and Bot key/target are required")
	}
	if err := validateJSONObject("reset Case input", cmd.InputJSON, true); err != nil {
		return ResetCaseOutcome{}, err
	}
	if !SupportsIncidentWorkflowTarget(cmd.Bot.Target) {
		return ResetCaseOutcome{}, fmt.Errorf("unsupported incident workflow target %q", cmd.Bot.Target)
	}
	environment := resolveIncidentEnvironment(cmd.Bug, cmd.Bot)
	if environment == "" {
		return ResetCaseOutcome{}, errors.New("replacement environment is required")
	}

	release := workflowCommandLocks.acquire("reset-case:" + cmd.CaseID)
	defer release()
	incident, err := o.store.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return ResetCaseOutcome{}, err
	}
	if incident.BugID != strings.TrimSpace(cmd.Bug.ID) {
		return ResetCaseOutcome{}, fmt.Errorf("reset Bug %q does not match Case Bug %q", cmd.Bug.ID, incident.BugID)
	}
	if incident.Source != "" && cmd.Bug.Source != incident.Source {
		return ResetCaseOutcome{}, fmt.Errorf("reset Bug source %q does not match Case source %q", cmd.Bug.Source, incident.Source)
	}
	if incident.SystemID != "" && cmd.Bug.SystemID != incident.SystemID {
		return ResetCaseOutcome{}, fmt.Errorf("reset Bug system %q does not match Case system %q", cmd.Bug.SystemID, incident.SystemID)
	}
	resetRequest := CaseReset{
		CaseID:                      cmd.CaseID,
		NewCaseID:                   cmd.NewCaseID,
		IdempotencyKey:              cmd.IdempotencyKey,
		ActorID:                     cmd.ActorID,
		ExpectedVersion:             cmd.ExpectedVersion,
		SelectedBotKey:              cmd.Bot.Key,
		ReplacementBotTarget:        cmd.Bot.Target,
		ReplacementSystemID:         cmd.Bug.SystemID,
		ReplacementEnvironment:      environment,
		ReplacementFrontendEntry:    cmd.FrontendEntry.Clone(),
		ReplacementFrontendEntries:  cloneFrontendEntryBindings(cmd.FrontendEntries),
		RequestJSON:                 mustJSON(cmd),
		replayOnlyLegacyEnvironment: resolveLegacyResetEnvironment(cmd.Bug, cmd.Bot),
	}
	fingerprint, err := caseResetFingerprint(resetRequest)
	if err != nil {
		return ResetCaseOutcome{}, err
	}
	result, err := o.store.ResetCaseWithReplacement(ctx, resetRequest)
	if err != nil {
		return ResetCaseOutcome{}, err
	}

	warnings, cancellationErr := o.processResetRunnerCancellation(result, cmd.IdempotencyKey, fingerprint)
	if cancellationErr != nil {
		return ResetCaseOutcome{Case: result.Replacement, Warnings: warnings}, cancellationErr
	}
	if result.Replay {
		failureEvent, found, loadErr := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey+":start:schedule-failed")
		if loadErr == nil && found && failureEvent.CaseID == result.Replacement.ID && failureEvent.EventType == "phase_schedule_failed" {
			replacement := result.Replacement
			if persisted, err := o.store.GetCase(ctx, result.Replacement.ID); err == nil {
				replacement = persisted
			}
			warnings = append(warnings, resetReplacementStartFailedWarning()...)
			return ResetCaseOutcome{Case: replacement, Warnings: warnings}, nil
		}
	}
	replacement, startErr := o.StartCase(ctx, StartCaseCommand{
		CaseID:          result.Replacement.ID,
		ExpectedVersion: result.Replacement.Version,
		IdempotencyKey:  cmd.IdempotencyKey + ":start",
		ActorID:         cmd.ActorID,
		Bug:             cmd.Bug,
		Bot:             cmd.Bot,
		InputJSON:       cmd.InputJSON,
	})
	if startErr != nil {
		if replacement.ID == "" {
			persisted, loadErr := o.store.GetCase(context.Background(), result.Replacement.ID)
			if loadErr == nil {
				replacement = persisted
			} else {
				replacement = result.Replacement
			}
		}
		warnings = append(warnings, resetReplacementStartFailedWarning()...)
		return ResetCaseOutcome{Case: replacement, Warnings: warnings}, nil
	}
	return ResetCaseOutcome{Case: replacement, Warnings: warnings}, nil
}

func hasWorkflowWarning(warnings []WorkflowWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func (o *CaseOrchestrator) processResetRunnerCancellation(result CaseResetResult, resetKey, fingerprint string) ([]WorkflowWarning, error) {
	if result.CancelledAttemptID == "" {
		return nil, nil
	}
	if result.requestFingerprint != "" {
		fingerprint = result.requestFingerprint
	}
	if o.runner == nil {
		operation, found, err := o.store.GetResetCancellationOperation(context.Background(), resetKey, fingerprint)
		if err != nil || !found {
			return resetCancellationStateUnavailableWarning(), nil //nolint:nilerr // Reset is already committed; cancellation uncertainty is returned as a structured warning.
		}
		return warningsForResetCancellation(operation), nil
	}
	claimToken, err := newAttemptRunClaimToken()
	if err != nil {
		return resetCancellationStateUnavailableWarning(), nil //nolint:nilerr // Reset is already committed; do not invite an unsafe retry of the completed reset.
	}
	durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	operation, acquired, err := o.store.ClaimResetCancellation(durable, resetKey, fingerprint, claimToken)
	cancel()
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return nil, err
		}
		return resetCancellationStateUnavailableWarning(), nil
	}
	if !acquired {
		return warningsForResetCancellation(operation), nil
	}
	completionStatus := ResetCancellationSucceeded
	if err := o.cancelPhase(result.CancelledAttemptID); err != nil {
		completionStatus = ResetCancellationFailed
	}
	durable, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	completed, err := o.store.CompleteResetCancellation(durable, resetKey, fingerprint, claimToken, completionStatus)
	cancel()
	if err != nil {
		return resetCancellationUnknownWarning(), nil //nolint:nilerr // Cancellation may have happened; report the unknown outcome without repeating it.
	}
	return warningsForResetCancellation(completed), nil
}

func warningsForResetCancellation(operation ResetCancellationOperation) []WorkflowWarning {
	switch operation.Status {
	case ResetCancellationSucceeded:
		return nil
	case ResetCancellationFailed:
		return []WorkflowWarning{{Code: "reset_runner_cancel_failed", Message: "旧阶段 Agent 未能确认停止，请人工检查其运行状态。"}}
	case ResetCancellationClaimed:
		return resetCancellationUnknownWarning()
	default:
		return resetCancellationStateUnavailableWarning()
	}
}

func resetCancellationUnknownWarning() []WorkflowWarning {
	return []WorkflowWarning{{Code: "reset_runner_cancel_unknown", Message: "旧阶段 Agent 的停止结果未知，请人工检查其运行状态；系统不会自动重复停止。"}}
}

func resetCancellationStateUnavailableWarning() []WorkflowWarning {
	return []WorkflowWarning{{Code: "reset_runner_cancel_state_unavailable", Message: "无法读取旧阶段 Agent 的停止状态，请人工检查；系统不会在状态未知时自动停止。"}}
}

func resetReplacementStartFailedWarning() []WorkflowWarning {
	return []WorkflowWarning{{Code: "reset_replacement_start_failed", Message: "接替 Case 的新阶段未能启动，已保留为可恢复状态；请刷新 Case 或重试开始排障。"}}
}

func (o *CaseOrchestrator) CreateAndStartCase(ctx context.Context, cmd CreateAndStartCaseCommand) (IncidentCase, error) {
	if o == nil || o.store == nil {
		return IncidentCase{}, errors.New("case orchestrator store is required")
	}
	if strings.TrimSpace(cmd.CaseID) == "" || strings.TrimSpace(cmd.IdempotencyKey) == "" || strings.TrimSpace(cmd.ActorID) == "" {
		return IncidentCase{}, errors.New("case ID, idempotency key, and actor ID are required")
	}
	if err := validateNewWorkflowCaseID(cmd.CaseID); err != nil {
		return IncidentCase{}, fmt.Errorf("case ID: %w", err)
	}
	if cmd.ExpectedVersion < 0 {
		return IncidentCase{}, errors.New("expected version must not be negative")
	}
	if strings.TrimSpace(cmd.Bug.ID) == "" || strings.TrimSpace(cmd.Bot.Key) == "" || strings.TrimSpace(cmd.Bot.Target) == "" {
		return IncidentCase{}, errors.New("Bug ID and Bot key/target are required")
	}
	if strings.TrimSpace(cmd.Bug.SystemID) == "" {
		cmd.Bug.SystemID = strings.TrimSpace(cmd.Bot.SystemID)
	}
	if len(cmd.InputJSON) == 0 {
		cmd.InputJSON = []byte(`{}`)
	}
	if err := validateJSONObject("start Case input", cmd.InputJSON, true); err != nil {
		return IncidentCase{}, err
	}
	// Serialize creation and scheduling by Bug ID so different client-generated
	// Case IDs for the same Bug converge before either can schedule twice.
	release := workflowCommandLocks.acquire("create-start-bug:" + cmd.Bug.ID)
	defer release()

	targetID := cmd.CaseID
	cycle := 1
	existing, getErr := o.store.GetCase(ctx, cmd.CaseID)
	switch {
	case errors.Is(getErr, ErrCaseNotFound):
		if cmd.ExpectedVersion != 0 {
			return IncidentCase{}, fmt.Errorf("%w: expected %d for missing Case", ErrCaseVersionConflict, cmd.ExpectedVersion)
		}
	case getErr != nil:
		return IncidentCase{}, getErr
	case existing.Status == CaseLegacyArchived:
		if existing.Version != cmd.ExpectedVersion {
			return IncidentCase{}, fmt.Errorf("%w: expected %d, current %d", ErrCaseVersionConflict, cmd.ExpectedVersion, existing.Version)
		}
		cycle = existing.CycleNumber + 1
		targetID = stableID("case-cycle", fmt.Sprintf("%s:%d", existing.ID, cycle))
	case cmd.ExpectedVersion == 0:
		cycle = existing.CycleNumber
	default:
		return o.StartCase(ctx, StartCaseCommand{CaseID: existing.ID, ExpectedVersion: cmd.ExpectedVersion, IdempotencyKey: cmd.IdempotencyKey, ActorID: cmd.ActorID, Bug: cmd.Bug, Bot: cmd.Bot, InputJSON: cmd.InputJSON})
	}
	environment := resolveIncidentEnvironment(cmd.Bug, cmd.Bot)
	pending := IncidentCase{
		ID: targetID, BugID: cmd.Bug.ID, Source: cmd.Bug.Source, SystemID: cmd.Bug.SystemID,
		Environment: environment, FrontendEntry: cmd.FrontendEntry.Clone(),
		FrontendEntries: newFrontendEntryBindings(cmd.FrontendEntries),
		Status:          CasePendingInvestigation, CycleNumber: cycle, SelectedBotKey: cmd.Bot.Key,
	}
	creation, createErr := o.store.CreateCaseWithIdentity(ctx, CaseCreation{Case: pending, IdempotencyKey: cmd.IdempotencyKey, ActorID: cmd.ActorID, RequestJSON: mustJSON(cmd)})
	if createErr != nil {
		return IncidentCase{}, createErr
	}
	if creation.ExistingOpen {
		return creation.Case.Clone(), nil
	}
	return o.StartCase(ctx, StartCaseCommand{CaseID: creation.Case.ID, ExpectedVersion: creation.Case.Version, IdempotencyKey: cmd.IdempotencyKey + ":start", ActorID: cmd.ActorID, Bug: cmd.Bug, Bot: cmd.Bot, InputJSON: cmd.InputJSON})
}

func resolveIncidentEnvironment(bug Bug, bot BotRef) string {
	if env := strings.TrimSpace(bot.Env); env != "" {
		return env
	}
	if env := strings.TrimSpace(bug.BotEnv); env != "" {
		return env
	}
	return strings.TrimSpace(bug.Env)
}

func resolveLegacyResetEnvironment(bug Bug, bot BotRef) string {
	if env := strings.TrimSpace(bug.Env); env != "" {
		return env
	}
	return strings.TrimSpace(bot.Env)
}

func (o *CaseOrchestrator) ApproveFix(ctx context.Context, cmd ApproveFixCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	if cmd.IdempotencyKey != StartFixApprovalKey(cmd.CaseID, cmd.RootCauseAttemptID, cmd.ExpectedVersion) {
		return IncidentCase{}, ErrApprovalScope
	}
	if _, found, err := o.store.GetEventByIdempotencyKey(ctx, cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	} else if found {
		return o.replayFixApproval(ctx, cmd)
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingFixApproval {
		return IncidentCase{}, ErrApprovalNotReady
	}
	rootCauseResult, err := validatedRootCauseResult(ctx, o.store, incident, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if !rootCauseResult.UsesCodeFixWorkflow() {
		return IncidentCase{}, ErrApprovalScope
	}
	sourceBaselines, err := resolveRemediationFixSourceBaselines(cmd.Bot.Path, incident.Environment, cmd.InputJSON, rootCauseResult)
	if err != nil {
		return IncidentCase{}, fmt.Errorf("invalid fix source baseline approval: %w", err)
	}
	cmd.InputJSON, err = withFixSourceBaselines(cmd.InputJSON, sourceBaselines)
	if err != nil {
		return IncidentCase{}, fmt.Errorf("canonicalize fix source baseline approval: %w", err)
	}
	root, err := o.store.GetAttempt(ctx, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	cmd.InputJSON, err = withApprovedFixReworkContext(cmd.InputJSON, root.InputJSON)
	if err != nil {
		return IncidentCase{}, fmt.Errorf("inherit approved fix rework context: %w", err)
	}
	if incident.CurrentAttemptID != cmd.RootCauseAttemptID {
		if replay, replayErr := o.hasEvent(ctx, incident.ID, cmd.IdempotencyKey); replayErr != nil {
			return IncidentCase{}, replayErr
		} else if replay {
			return o.replayFixApproval(ctx, cmd)
		}
	}
	attempt, request := buildFixApprovalMutation(cmd, incident.CycleNumber, sourceBaselines)
	mutation, err := o.store.ApplyCaseMutation(ctx, request)
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

func buildFixApprovalMutation(cmd ApproveFixCommand, cycleNumber int, sourceBaselines map[string]string) (PhaseAttempt, CaseMutation) {
	incident := IncidentCase{ID: cmd.CaseID, CycleNumber: cycleNumber}
	scopeValue := map[string]any{"root_cause_attempt_id": cmd.RootCauseAttemptID, "source_baselines": sourceBaselines}
	if rework, ok := fixReworkFromInput(cmd.InputJSON); ok {
		scopeValue["rework_of_fix_attempt_id"] = rework.SourceFixAttemptID
		scopeValue["required_fix_branch_suffix"] = rework.RequiredFixBranchSuffix
	}
	scope, _ := json.Marshal(scopeValue)
	approval := Approval{ID: stableID("approval", cmd.IdempotencyKey), CaseID: cmd.CaseID, Kind: ApprovalStartFix, Actor: cmd.ActorID, CaseVersion: cmd.ExpectedVersion, ScopeJSON: scope}
	attempt := newAttempt(incident, PhaseFix, "", cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, cmd.RootCauseAttemptID)
	update := CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(attempt.ID), SelectedBotKey: workflowStringPtr(cmd.Bot.Key)}
	payload := mustJSON(map[string]string{"attempt_id": attempt.ID, "root_cause_attempt_id": cmd.RootCauseAttemptID})
	mutation := CaseMutation{CaseID: cmd.CaseID, ExpectedVersion: cmd.ExpectedVersion, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), Approvals: []Approval{approval}, CreateAttempts: []PhaseAttempt{attempt}, Snapshot: update, Steps: []CaseMutationStep{{To: CaseFixing, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "fix_approved", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}}
	return attempt, mutation
}

func (o *CaseOrchestrator) replayFixApproval(ctx context.Context, cmd ApproveFixCommand) (IncidentCase, error) {
	root, err := o.store.GetAttempt(ctx, cmd.RootCauseAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	incident, incidentErr := o.store.GetCase(ctx, cmd.CaseID)
	if incidentErr != nil {
		return IncidentCase{}, incidentErr
	}
	rootCauseResult, parseErr := ParseInvestigationResult(root.OutputJSON)
	if parseErr != nil || !rootCauseResult.UsesCodeFixWorkflow() {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	sourceBaselines, parseErr := resolveRemediationFixSourceBaselines(cmd.Bot.Path, incident.Environment, cmd.InputJSON, rootCauseResult)
	if parseErr != nil {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	cmd.InputJSON, parseErr = withFixSourceBaselines(cmd.InputJSON, sourceBaselines)
	if parseErr != nil {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	cmd.InputJSON, parseErr = withApprovedFixReworkContext(cmd.InputJSON, root.InputJSON)
	if parseErr != nil {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	_, request := buildFixApprovalMutation(cmd, root.CycleNumber, sourceBaselines)
	result, err := o.store.ApplyCaseMutation(ctx, request)
	if err != nil {
		return IncidentCase{}, err
	}
	if !result.Replay {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	return result.Case, nil
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
	if o.git == nil {
		return IncidentCase{}, errors.New("git integration is unavailable")
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
	fixes, targets, baselines := map[string]string{}, map[string]string{}, map[string]string{}
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
			baseline := strings.TrimSpace(approved.BaselineBranch)
			if baseline == "" { // Backward-compatible recovery for pre-dual-target approvals.
				baseline = change.TargetEnvironmentBranch
			}
			baselines[change.Repo] = baseline
			selected = append(selected, change)
		}
	} else {
		for _, change := range changes {
			// Every change produced by the winning successful fix attempt remains
			// in scope. PushStatus is subsequently reused for per-repository merge
			// progress (merge_local/push_unknown/conflict), so filtering only
			// "pushed" would silently drop the exact blocked repository when a
			// fresh approval is required after target-head drift.
			if change.AttemptID == incident.CurrentAttemptID && change.FixCommit != "" && change.TargetEnvironmentBranch != "" {
				fixes[change.Repo] = change.FixCommit
				targets[change.Repo] = change.TargetEnvironmentBranch
				baselines[change.Repo] = change.BaseBranch
				selected = append(selected, change)
			}
		}
	}
	if len(selected) == 0 || !sameStringMapKeys(fixes, targets) || !sameStringMapKeys(fixes, baselines) {
		return IncidentCase{}, ErrApprovalScope
	}
	approvedChanges := make([]ApprovedCodeChange, 0, len(selected))
	request := MergeRequest{CaseID: incident.ID, FixCommits: fixes, TargetBranches: targets, Changes: selected}
	targetHeads := map[string]string{}
	baselineRequest := buildBaselineMergeRequest(incident.ID, selected, baselines)
	baselineHeads := map[string]string{}
	if !hasReservation && o.git != nil {
		inspection, inspectErr := o.git.Inspect(ctx, request)
		if inspectErr != nil {
			return incident, inspectErr
		}
		for index := range selected {
			repoResult, ok := inspection.Repositories[selected[index].Repo]
			if !ok || strings.TrimSpace(repoResult.TargetHead) == "" || repoResult.ApprovalKey != MergeApprovalKey(incident.ID, selected[index].Repo, selected[index].FixCommit, selected[index].TargetEnvironmentBranch, repoResult.TargetHead) {
				return IncidentCase{}, ErrApprovalScope
			}
			targetHeads[selected[index].Repo] = repoResult.TargetHead
			selected[index].MergeBaseHead = repoResult.TargetHead
		}
		if len(targetHeads) != len(selected) {
			return IncidentCase{}, ErrApprovalScope
		}
		if !reflect.DeepEqual(targetHeads, cmd.TargetHeads) {
			return o.recordStaleMergeApproval(incident, cmd.IdempotencyKey, selected, targetHeads)
		}
		if len(baselineRequest.Changes) > 0 {
			baselineInspection, baselineErr := o.git.Inspect(ctx, baselineRequest)
			if baselineErr != nil {
				return incident, baselineErr
			}
			for _, change := range baselineRequest.Changes {
				repoResult, ok := baselineInspection.Repositories[change.Repo]
				if !ok || strings.TrimSpace(repoResult.TargetHead) == "" || repoResult.ApprovalKey != MergeApprovalKey(incident.ID, change.Repo, change.FixCommit, change.TargetEnvironmentBranch, repoResult.TargetHead) {
					return IncidentCase{}, ErrApprovalScope
				}
				baselineHeads[change.Repo] = repoResult.TargetHead
			}
		}
	}
	if hasReservation {
		for _, approved := range scopeValue.CodeChanges {
			if approved.TargetHead == "" || approved.ApprovalKey != MergeApprovalKey(incident.ID, approved.Repo, approved.FixCommit, approved.TargetBranch, approved.TargetHead) {
				return IncidentCase{}, ErrApprovalScope
			}
			targetHeads[approved.Repo] = approved.TargetHead
			if approved.BaselineBranch != "" && approved.BaselineBranch != approved.TargetBranch {
				if approved.BaselineHead == "" || approved.BaselineApprovalKey != MergeApprovalKey(incident.ID, approved.Repo, approved.FixCommit, approved.BaselineBranch, approved.BaselineHead) {
					return IncidentCase{}, ErrApprovalScope
				}
				baselineHeads[approved.Repo] = approved.BaselineHead
			}
		}
		if len(targetHeads) != len(selected) {
			return IncidentCase{}, ErrApprovalScope
		}
	}
	request.Changes = selected
	request.TargetHeads = targetHeads
	baselineRequest.TargetHeads = baselineHeads
	for _, change := range selected {
		head := targetHeads[change.Repo]
		baselineBranch := baselines[change.Repo]
		baselineHead := baselineHeads[change.Repo]
		if baselineBranch == change.TargetEnvironmentBranch {
			baselineHead = head
		}
		approvedChanges = append(approvedChanges, ApprovedCodeChange{ID: change.ID, Repo: change.Repo, FixCommit: change.FixCommit, BaselineBranch: baselineBranch, BaselineHead: baselineHead, BaselineApprovalKey: MergeApprovalKey(incident.ID, change.Repo, change.FixCommit, baselineBranch, baselineHead), TargetBranch: change.TargetEnvironmentBranch, TargetHead: head, ApprovalKey: MergeApprovalKey(incident.ID, change.Repo, change.FixCommit, change.TargetEnvironmentBranch, head)})
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
		return o.recoverReservedMerge(ctx, current, cmd.IdempotencyKey, selected, baselineRequest, request)
	}
	baselineResult := MergeResult{Pushed: true, MergeCommits: map[string]string{}}
	if len(baselineRequest.Changes) > 0 {
		baselineResult, err = o.git.MergeAndPush(ctx, baselineRequest)
		if err != nil {
			if errors.Is(err, ErrMergeApprovalStale) {
				return o.recordStaleMergeApproval(reserved.Case, cmd.IdempotencyKey, selected, nil)
			}
			if baselineResult.Conflict || errors.Is(err, ErrGitMergeConflict) {
				for i := range selected {
					if repoResult, ok := baselineResult.Repositories[selected[i].Repo]; ok && repoResult.Conflict {
						selected[i].PushStatus = "conflict"
					}
				}
				return o.recordMergeConflict(reserved.Case, cmd.IdempotencyKey, selected, fmt.Errorf("merge fix into confirmed baseline: %w", err))
			}
			return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, fmt.Errorf("push confirmed baseline: %w", err))
		}
		for _, change := range baselineRequest.Changes {
			repoResult, ok := baselineResult.Repositories[change.Repo]
			if !ok || !repoResult.Pushed || repoResult.MergeCommit == "" {
				return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, fmt.Errorf("confirmed baseline push is incomplete for %s", change.Repo))
			}
		}
	}
	result, callErr := o.git.MergeAndPush(ctx, request)
	result.BaselineMergeCommits = CloneStringMap(baselineResult.MergeCommits)
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
		if errors.Is(callErr, ErrMergeApprovalStale) {
			for i := range selected {
				if repoResult, ok := result.Repositories[selected[i].Repo]; ok && repoResult.TargetHead != "" {
					selected[i].MergeBaseHead = repoResult.TargetHead
				}
			}
			return o.recordStaleMergeApproval(reserved.Case, cmd.IdempotencyKey, selected, nil)
		}
		inspectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		inspection, inspectErr := o.git.Inspect(inspectCtx, request)
		cancel()
		if inspectErr == nil && inspection.Conflict {
			result.Conflict = true
		}
		if result.Conflict {
			return o.recordMergeConflict(reserved.Case, cmd.IdempotencyKey, selected, callErr)
		}
		return o.recordMergeAmbiguous(reserved.Case, cmd.IdempotencyKey, selected, callErr)
	}
	if result.Conflict {
		return o.recordMergeConflict(reserved.Case, cmd.IdempotencyKey, selected, errors.New("merge conflict"))
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
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: reserved.Case.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(result), CodeChanges: selected, Steps: []CaseMutationStep{{To: CaseSubmitted, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_pushed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(result)}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	return done.Case, nil
}

func buildBaselineMergeRequest(caseID string, changes []CodeChange, baselines map[string]string) MergeRequest {
	request := MergeRequest{CaseID: caseID, FixCommits: map[string]string{}, TargetBranches: map[string]string{}}
	for _, original := range changes {
		baseline := strings.TrimSpace(baselines[original.Repo])
		if baseline == "" || baseline == strings.TrimSpace(original.TargetEnvironmentBranch) {
			continue
		}
		change := original.Clone()
		change.TargetEnvironmentBranch = baseline
		change.MergeBaseHead = ""
		change.MergeCommit = ""
		request.Changes = append(request.Changes, change)
		request.FixCommits[change.Repo] = change.FixCommit
		request.TargetBranches[change.Repo] = baseline
	}
	return request
}

func (o *CaseOrchestrator) recordStaleMergeApproval(incident IncidentCase, key string, changes []CodeChange, heads map[string]string) (IncidentCase, error) {
	k := key + ":target-head-changed"
	payload := mustJSON(map[string]any{"target_heads": heads, "changes": changes})
	mutation, err := o.store.ApplyCaseMutation(context.Background(), CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: k, RequestJSON: payload, CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseWaitingMergeApproval, AuditOnly: incident.Status == CaseWaitingMergeApproval, Event: TransitionEvent{ID: stableID("event", k), EventType: "merge_approval_stale", ActorType: "git", ActorID: "git-integration", PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(ErrMergeApprovalStale, err)
	}
	return mutation.Case, ErrMergeApprovalStale
}

func (o *CaseOrchestrator) recoverReservedMerge(ctx context.Context, incident IncidentCase, key string, changes []CodeChange, baselineRequest, request MergeRequest) (IncidentCase, error) {
	if o.git == nil {
		return incident, nil
	}
	if len(baselineRequest.Changes) > 0 {
		baselineResult, baselineErr := o.git.MergeAndPush(ctx, baselineRequest)
		if baselineErr != nil {
			if errors.Is(baselineErr, ErrMergeApprovalStale) {
				return o.recordStaleMergeApproval(incident, key, changes, nil)
			}
			if baselineResult.Conflict || errors.Is(baselineErr, ErrGitMergeConflict) {
				return o.recordMergeConflict(incident, key, changes, fmt.Errorf("recover confirmed baseline merge: %w", baselineErr))
			}
			return o.recordMergeAmbiguous(incident, key, changes, fmt.Errorf("recover confirmed baseline push: %w", baselineErr))
		}
		for _, change := range baselineRequest.Changes {
			repoResult, ok := baselineResult.Repositories[change.Repo]
			if !ok || !repoResult.Pushed || repoResult.MergeCommit == "" {
				return o.recordMergeAmbiguous(incident, key, changes, fmt.Errorf("recovered baseline push is incomplete for %s", change.Repo))
			}
		}
	}
	inspection, err := o.git.Inspect(ctx, request)
	if err != nil {
		return incident, err
	}
	if err := validateMergeInspectionScope(request, inspection); err != nil {
		return incident, err
	}
	return o.resumeInspectedMerge(ctx, incident, key, changes, request, inspection)
}

func validateMergeInspectionScope(request MergeRequest, inspection MergeInspection) error {
	if len(request.FixCommits) == 0 || len(inspection.Repositories) != len(request.FixCommits) {
		return ErrApprovalScope
	}
	for repo, fix := range request.FixCommits {
		result, ok := inspection.Repositories[repo]
		if !ok || result.TargetHead == "" || result.ApprovalKey != MergeApprovalKey(request.CaseID, repo, fix, request.TargetBranches[repo], result.TargetHead) {
			return ErrApprovalScope
		}
	}
	return nil
}

func (o *CaseOrchestrator) resumeInspectedMerge(ctx context.Context, incident IncidentCase, key string, changes []CodeChange, request MergeRequest, inspection MergeInspection) (IncidentCase, error) {
	allPushed := true
	hasLocal := false
	hasConflict := false
	for i := range changes {
		repoResult, ok := inspection.Repositories[changes[i].Repo]
		if !ok {
			repoResult.MergeCommit = inspection.MergeCommits[changes[i].Repo]
			repoResult.Pushed = inspection.MergePushed && repoResult.MergeCommit != ""
		}
		if repoResult.Conflict {
			changes[i].PushStatus = "conflict"
			hasConflict = true
			allPushed = false
			continue
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
	if hasConflict {
		return o.recordMergeConflict(incident, key, changes, errors.New("merge conflict confirmed"))
	}
	if !allPushed && hasLocal {
		unfinished := make([]CodeChange, 0, len(changes))
		pushRequest := MergeRequest{CaseID: request.CaseID, FixCommits: map[string]string{}, TargetBranches: map[string]string{}}
		for _, change := range changes {
			if change.PushStatus != "merge_local" || change.MergeCommit == "" {
				continue
			}
			unfinished = append(unfinished, change.Clone())
			pushRequest.FixCommits[change.Repo] = change.FixCommit
			pushRequest.TargetBranches[change.Repo] = change.TargetEnvironmentBranch
			if approvedHead := request.TargetHeads[change.Repo]; approvedHead != "" {
				if pushRequest.TargetHeads == nil {
					pushRequest.TargetHeads = map[string]string{}
				}
				pushRequest.TargetHeads[change.Repo] = approvedHead
			}
		}
		pushRequest.Changes = unfinished
		pushResult, pushErr := o.git.ResumePush(ctx, pushRequest)
		if pushErr != nil {
			if errors.Is(pushErr, ErrMergeApprovalStale) {
				for i := range changes {
					if repoResult, ok := pushResult.Repositories[changes[i].Repo]; ok && repoResult.TargetHead != "" {
						changes[i].MergeBaseHead = repoResult.TargetHead
					}
				}
				return o.recordStaleMergeApproval(incident, key, changes, nil)
			}
			return o.recordMergeAmbiguous(incident, key, changes, pushErr)
		}
		for i := range changes {
			if changes[i].PushStatus != "merge_local" {
				continue
			}
			repoResult, ok := pushResult.Repositories[changes[i].Repo]
			if !ok || !repoResult.Pushed || repoResult.MergeCommit == "" || repoResult.MergeCommit != changes[i].MergeCommit {
				continue
			}
			changes[i].MergeCommit = repoResult.MergeCommit
			changes[i].PushStatus = "pushed"
		}
		allPushed = true
		for _, change := range changes {
			if change.PushStatus != "pushed" || change.MergeCommit == "" {
				allPushed = false
				break
			}
		}
	}
	if !allPushed {
		return o.recordMergeAmbiguous(incident, key, changes, errors.New("merge push remains incomplete"))
	}
	completedKey := key + ":completed"
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(changes), CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseSubmitted, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_push_resumed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(changes)}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	return done.Case, nil
}

func (o *CaseOrchestrator) recordMergeConflict(incident IncidentCase, key string, changes []CodeChange, cause error) (IncidentCase, error) {
	k := key + ":conflict"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := mustJSON(map[string]any{"error": cause.Error(), "repositories": changes})
	m, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: k, RequestJSON: payload, CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseMergeConflict, Event: TransitionEvent{ID: stableID("event", k), EventType: "merge_conflict", ActorType: "git", ActorID: "git-integration", PayloadJSON: payload}}}})
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
	completed := []string{}
	blocked := []string{}
	for _, change := range changes {
		if change.PushStatus == "pushed" && change.MergeCommit != "" {
			completed = append(completed, change.Repo)
		} else {
			blocked = append(blocked, change.Repo)
		}
	}
	sort.Strings(completed)
	sort.Strings(blocked)
	payload := mustJSON(map[string]any{"error": cause.Error(), "completed_repositories": completed, "blocked_repositories": blocked})
	m, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: k, RequestJSON: payload, CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseMerging, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", k), EventType: "merge_push_ambiguous", ActorType: "git", ActorID: "git-integration", PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return m.Case, cause
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
	if attempt.FinishedAt == nil {
		now := time.Now().UTC()
		attempt.FinishedAt = &now
	}
	to := failureStateForPhase(attempt.Phase)
	payload := mustJSON(map[string]string{"attempt_id": attempt.ID})
	mutationRequest := CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "attempt_cancelled", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}}
	if attempt.Phase == PhaseFix {
		mutationRequest.DeleteFixCheckpointAttemptID = attempt.ID
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, mutationRequest)
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay {
		if replayed, replayErr, found := o.replayedCancelOutcome(ctx, mutation.Case, cmd.IdempotencyKey); found {
			return replayed, replayErr
		}
		return mutation.Case, nil
	}
	if o.runner != nil {
		err = o.cancelPhase(attempt.ID)
		if err != nil {
			return o.recordCancelFailure(mutation.Case, cmd.IdempotencyKey, err)
		}
	}
	return mutation.Case, err
}

func (o *CaseOrchestrator) cancelPhase(attemptID string) error {
	select {
	case o.cancelWorkers <- struct{}{}:
	default:
		return ErrCancelWorkerSaturated
	}
	timeout := o.cancelTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		defer func() { <-o.cancelWorkers }()
		result <- o.runner.Cancel(ctx, attemptID)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *CaseOrchestrator) replayedCancelOutcome(ctx context.Context, committed IncidentCase, key string) (IncidentCase, error, bool) {
	event, found, err := o.store.GetEventByIdempotencyKey(ctx, key+":runner-cancel")
	if err != nil || !found {
		return committed, err, err != nil
	}
	current, loadErr := o.store.GetCase(ctx, committed.ID)
	if loadErr != nil {
		return IncidentCase{}, loadErr, true
	}
	switch event.EventType {
	case "runner_cancel_timed_out":
		return current, context.DeadlineExceeded, true
	case "runner_cancel_saturated":
		return current, ErrCancelWorkerSaturated, true
	default:
		return current, errors.New("external runner cancellation failed"), true
	}
}

func (o *CaseOrchestrator) recordCancelFailure(incident IncidentCase, key string, cause error) (IncidentCase, error) {
	eventType := "runner_cancel_failed"
	if errors.Is(cause, context.DeadlineExceeded) {
		eventType = "runner_cancel_timed_out"
	} else if errors.Is(cause, ErrCancelWorkerSaturated) {
		eventType = "runner_cancel_saturated"
	}
	durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auditKey := key + ":runner-cancel"
	payload := mustJSON(map[string]string{"error": cause.Error()})
	mutation, err := o.store.ApplyCaseMutation(durable, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: auditKey, RequestJSON: payload, Steps: []CaseMutationStep{{To: incident.Status, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", auditKey), EventType: eventType, ActorType: "studio", ActorID: "orchestrator", PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, errors.Join(cause, err)
	}
	return mutation.Case, cause
}

func (o *CaseOrchestrator) CompleteAttempt(ctx context.Context, cmd CompleteAttemptCommand) (IncidentCase, error) {
	if o == nil || o.store == nil {
		return IncidentCase{}, errors.New("case orchestrator store is required")
	}
	if len(cmd.OutputJSON) == 0 {
		cmd.OutputJSON = []byte(`{}`)
	}
	if strings.TrimSpace(cmd.IdempotencyKey) == "" {
		return IncidentCase{}, validateCompletionCommand(cmd)
	}
	release := workflowCommandLocks.acquire("complete-attempt:" + cmd.IdempotencyKey)
	defer release()
	if replayed, found, replayErr := o.replayAttemptCompletion(ctx, cmd); found || replayErr != nil {
		return replayed, replayErr
	}
	if err := validateCompletionCommand(cmd); err != nil {
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
	expectedAttemptOutput := CloneRawMessage(attempt.OutputJSON)
	if intent, found, parseErr := parseCompletionIntent(attempt.OutputJSON); parseErr != nil {
		return IncidentCase{}, parseErr
	} else if found {

		if !equivalentCompletionCommands(intent, cmd) {
			return IncidentCase{}, ErrIdempotencyConflict
		}
	}
	if err := validateCompletionAttemptPhase(attempt.Phase, cmd); err != nil {
		return IncidentCase{}, err
	}
	if err := validateFixReworkCompletion(attempt, cmd); err != nil {
		return IncidentCase{}, err
	}

	if cmd.Outcome == PhaseOutcomeFixPushed && !cmd.remoteFixInspected {
		inspection, inspectErr := o.inspectFixWithRetry(ctx, FixInspectionRequest{CaseID: incident.ID, Attempt: attempt.Clone(), Changes: cmd.CodeChanges})
		if inspectErr != nil {
			if errors.Is(inspectErr, ErrFixRemoteMismatch) {
				finishErr := o.finishFixRecoveryFailure(ctx, incident, attempt, inspectErr.Error())
				failed, loadErr := o.store.GetCase(ctx, incident.ID)
				return failed, errors.Join(inspectErr, finishErr, loadErr)
			}
			return IncidentCase{}, inspectErr
		}
		if !inspection.Complete || len(inspection.Changes) == 0 || validateFixCheckpointMatchesResult(inspection.Changes, cmd.CodeChanges) != nil {
			finishErr := o.finishFixRecoveryFailure(ctx, incident, attempt, ErrFixRemoteMismatch.Error())
			failed, loadErr := o.store.GetCase(ctx, incident.ID)
			return failed, errors.Join(ErrFixRemoteMismatch, finishErr, loadErr)
		}
	}

	attempt.OutputJSON, attempt.ErrorCode, attempt.ErrorMessage, attempt.Usage = CloneRawMessage(cmd.OutputJSON), cmd.ErrorCode, cmd.ErrorMessage, cmd.Usage
	if cmd.Outcome == PhaseOutcomeFixFailed || cmd.Outcome == PhaseOutcomeNeedsEvidence || cmd.Outcome == PhaseOutcomeSystemFailed {
		attempt.Status = AttemptStatusFailed
	} else {
		attempt.Status = AttemptStatusSucceeded
	}
	return o.applyOutcome(ctx, incident, attempt, cmd, expectedAttemptOutput)
}

func (o *CaseOrchestrator) replayAttemptCompletion(ctx context.Context, cmd CompleteAttemptCommand) (IncidentCase, bool, error) {
	replay, found, err := o.store.GetCommittedCaseMutation(ctx, cmd.IdempotencyKey)
	if err != nil || !found {
		return IncidentCase{}, found, err
	}
	if replay.Event.CaseID != cmd.CaseID {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	want, err := completionCommandIdentity(cmd)
	if err != nil {
		return IncidentCase{}, true, err
	}
	stored, identityFound, err := o.store.GetAttemptCompletionIdentity(ctx, cmd.AttemptID)
	if err != nil {
		return IncidentCase{}, true, err
	}
	if !identityFound {
		persisted, rebuildErr := o.rebuildCommittedCompletion(ctx, replay, cmd.AttemptID)
		if rebuildErr != nil {
			return IncidentCase{}, true, ErrIdempotencyConflict
		}
		stored, err = completionCommandIdentity(persisted)
		if err != nil {
			return IncidentCase{}, true, err
		}
	}
	if stored != want {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	return replay.ResultCase.Clone(), true, nil
}

func (o *CaseOrchestrator) rebuildCommittedCompletion(ctx context.Context, replay CommittedCaseMutation, attemptID string) (CompleteAttemptCommand, error) {
	attempt, err := o.store.GetAttempt(ctx, attemptID)
	if err != nil || attempt.CaseID != replay.Event.CaseID || attempt.FinishedAt == nil {
		return CompleteAttemptCommand{}, ErrIdempotencyConflict
	}
	outcomes := map[string]PhaseOutcome{
		"validation_reproduced":               PhaseOutcomeReproduced,
		"validation_not_reproduced":           PhaseOutcomeNotReproduced,
		"evidence_required":                   PhaseOutcomeNeedsEvidence,
		"validation_evidence_refresh_started": PhaseOutcomeValidationEvidenceRequired,
		"phase_system_failed":                 PhaseOutcomeSystemFailed,
		"root_cause_ready":                    PhaseOutcomeRootCauseReady,
		"fix_pushed":                          PhaseOutcomeFixPushed,
		"fix_failed":                          PhaseOutcomeFixFailed,
		"regression_fixed":                    PhaseOutcomeFixedVerified,
		"regression_failed":                   PhaseOutcomeStillReproduces,
	}
	outcome, ok := outcomes[replay.Event.EventType]
	if !ok || !jsonValuesEqual(replay.Event.PayloadJSON, attempt.OutputJSON) {
		return CompleteAttemptCommand{}, ErrIdempotencyConflict
	}
	persisted := CompleteAttemptCommand{CaseID: attempt.CaseID, AttemptID: attempt.ID, ExpectedVersion: replay.ResultCase.Version - 1, IdempotencyKey: replay.Event.IdempotencyKey, ActorID: replay.Event.ActorID, Outcome: outcome, OutputJSON: CloneRawMessage(attempt.OutputJSON), ErrorCode: attempt.ErrorCode, ErrorMessage: attempt.ErrorMessage, Usage: attempt.Usage}
	if outcome == PhaseOutcomeFixPushed {
		parsed, parseErr := ParsePhaseResult(attempt, attempt.OutputJSON)
		if parseErr != nil || parsed.Outcome != outcome {
			return CompleteAttemptCommand{}, ErrIdempotencyConflict
		}
		persisted.CodeChanges = parsed.CodeChanges
	}
	return persisted, nil
}

func jsonValuesEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

const fixInspectionMaxAttempts = 3

func (o *CaseOrchestrator) inspectFixWithRetry(ctx context.Context, request FixInspectionRequest) (FixInspection, error) {
	if o.git == nil {
		return FixInspection{}, ErrFixInspectionUnavailable
	}
	var inspection FixInspection
	var err error
	for attempt := 0; attempt < fixInspectionMaxAttempts; attempt++ {
		inspection, err = o.git.InspectFix(ctx, request)
		if err == nil {
			return inspection, nil
		}
		if errors.Is(err, ErrFixRemoteMismatch) {
			return inspection, err
		}
		if ctx.Err() != nil {
			return inspection, ctx.Err()
		}
	}
	if errors.Is(err, ErrFixInspectionUnavailable) {
		return inspection, err
	}
	return inspection, errors.Join(ErrFixInspectionUnavailable, err)
}

func (o *CaseOrchestrator) applyOutcome(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, cmd CompleteAttemptCommand, expectedAttemptOutput json.RawMessage) (IncidentCase, error) {
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
	case PhaseOutcomeNeedsEvidence:
		add(CaseWaitingEvidence, "evidence_required", "agent", actor, cmd.OutputJSON)
	case PhaseOutcomeSystemFailed:
		add(CaseWaitingEvidence, "phase_system_failed", "studio", "orchestrator", cmd.OutputJSON)
	case PhaseOutcomeRootCauseReady:
		result, err := ParseInvestigationResult(cmd.OutputJSON)
		if err != nil {
			return IncidentCase{}, err
		}
		add(CaseRootCauseReady, "root_cause_ready", "agent", actor, cmd.OutputJSON)
		if result.UsesCodeFixWorkflow() {
			add(CaseWaitingFixApproval, "fix_approval_requested", "studio", "orchestrator", map[string]string{"attempt_id": attempt.ID})
		} else {
			add(CaseWaitingRemediation, "remediation_confirmation_requested", "studio", "orchestrator", map[string]string{"attempt_id": attempt.ID, "root_cause_type": string(result.RootCauseType), "remediation_mode": string(result.Remediation.Mode)})
		}
	case PhaseOutcomeFixPushed:
		add(CaseFixPushed, "fix_pushed", "agent", actor, cmd.OutputJSON)
		add(CaseWaitingMergeApproval, "merge_approval_requested", "studio", "orchestrator", map[string]string{"attempt_id": attempt.ID})
	case PhaseOutcomeFixFailed:
		add(CaseFixFailed, "fix_failed", "agent", actor, cmd.OutputJSON)
	default:
		return IncidentCase{}, fmt.Errorf("unsupported phase outcome %q", cmd.Outcome)
	}
	creates := []PhaseAttempt{}
	var nextBug Bug
	var nextBot BotRef
	if next != nil {
		var contextErr error
		nextBug, nextBot, contextErr = o.resolveRecoveryContext(ctx, incident, *next)
		if contextErr != nil {
			return IncidentCase{}, fmt.Errorf("resolve next phase context: %w", contextErr)
		}
		creates = append(creates, *next)
	}
	request := mustJSON(cmd)
	completionIdentity, err := completionCommandIdentity(cmd)
	if err != nil {
		return IncidentCase{}, err
	}
	mutationRequest := CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: request, FinishAttempts: []PhaseAttempt{attempt}, CreateAttempts: creates, CodeChanges: cmd.CodeChanges, ExpectedAttemptOutputs: map[string]json.RawMessage{attempt.ID: expectedAttemptOutput}, CompletionAttemptID: attempt.ID, CompletionIdentitySHA256: completionIdentity, Snapshot: update, Steps: steps}
	if attempt.Phase == PhaseFix {
		mutationRequest.DeleteFixCheckpointAttemptID = attempt.ID
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, mutationRequest)
	if err != nil {
		return IncidentCase{}, err
	}
	if mutation.Replay || next == nil {
		return mutation.Case, nil
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, *next, cmd.IdempotencyKey+":next", errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(*next, nextBug, nextBot); err != nil {
		return o.phaseScheduleFailure(ctx, mutation.Case, *next, cmd.IdempotencyKey+":next", err)
	}
	return mutation.Case, nil
}

func (o *CaseOrchestrator) beginPhase(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string) (IncidentCase, error) {
	return o.beginPhaseWithUpdate(ctx, incident, to, attempt, bug, bot, key, actor, eventType, CaseSnapshotUpdate{})
}

func (o *CaseOrchestrator) beginPhaseWithUpdate(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string, update CaseSnapshotUpdate) (IncidentCase, error) {
	return o.beginPhaseWithUpdateAndPayload(ctx, incident, to, attempt, bug, bot, key, actor, eventType, update, nil)
}

func (o *CaseOrchestrator) beginPhaseWithUpdateAndPayload(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string, update CaseSnapshotUpdate, payload json.RawMessage) (IncidentCase, error) {
	return o.beginPhaseMutation(ctx, incident, to, attempt, bug, bot, key, actor, eventType, update, payload)
}

func (o *CaseOrchestrator) beginPhaseMutation(ctx context.Context, incident IncidentCase, to CaseStatus, attempt PhaseAttempt, bug Bug, bot BotRef, key, actor, eventType string, update CaseSnapshotUpdate, payload json.RawMessage) (IncidentCase, error) {
	update.CurrentAttemptID = workflowStringPtr(attempt.ID)
	update.SelectedBotKey = workflowStringPtr(bot.Key)
	request, _ := json.Marshal(map[string]any{"attempt": attempt, "to": to, "event_type": eventType, "actor": actor})
	actorType := "user"
	switch actor {
	case "recovery":
		actorType = "recovery"
	case "studio":
		actorType = "studio"
	}
	if len(payload) == 0 {
		payload = mustJSON(map[string]string{"attempt_id": attempt.ID})
	}
	mutationRequest := CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: request, CreateAttempts: []PhaseAttempt{attempt}, Snapshot: update, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", key), EventType: eventType, ActorType: actorType, ActorID: actor, PayloadJSON: payload}}}}
	mutation, err := o.store.ApplyCaseMutation(ctx, mutationRequest)
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
	errorCode := phaseScheduleErrorCode(cause)
	errorMessage := phaseScheduleErrorMessage(cause)
	attempt.Status = AttemptStatusFailed
	attempt.OutputJSON = mustJSON(map[string]string{"error_code": errorCode, "error_message": errorMessage})
	attempt.ErrorCode = errorCode
	attempt.ErrorMessage = errorMessage
	failureKey := key + ":schedule-failed"
	request := mustJSON(map[string]string{"error_code": errorCode, "error_message": errorMessage, "attempt_id": attempt.ID})
	failureCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mutationRequest := CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: failureKey, RequestJSON: request, FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: failureStateForPhase(attempt.Phase), Event: TransitionEvent{ID: stableID("event", failureKey), EventType: "phase_schedule_failed", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: request}}}}
	if attempt.Phase == PhaseFix {
		mutationRequest.DeleteFixCheckpointAttemptID = attempt.ID
	}
	mutation, err := o.store.ApplyCaseMutation(failureCtx, mutationRequest)
	scheduleErr := &phaseScheduleStartError{phase: attempt.Phase, message: errorMessage, cause: cause}
	if err != nil {
		return IncidentCase{}, errors.Join(scheduleErr, err)
	}
	return mutation.Case, scheduleErr
}

func phaseScheduleErrorCode(cause error) string {

	return "schedule_failed"
}

func phaseScheduleErrorMessage(cause error) string {

	const fallback = "阶段 Agent 启动失败，请检查运行环境后重试"
	if cause == nil {
		return fallback
	}
	detail := strings.TrimSpace(cause.Error())
	if detail == "" || containsSensitiveData([]byte(detail)) {
		return fallback
	}
	const maxDetailRunes = 600
	runes := []rune(detail)
	if len(runes) > maxDetailRunes {
		detail = string(runes[:maxDetailRunes]) + "…"
	}
	return "阶段 Agent 启动失败：" + detail
}

type phaseScheduleStartError struct {
	phase   Phase
	message string
	cause   error
}

func (e *phaseScheduleStartError) Error() string {
	return fmt.Sprintf("schedule phase %s: %s", e.phase, e.message)
}

func (e *phaseScheduleStartError) Unwrap() error { return e.cause }

func mustJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

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
	case PhaseInvestigation:
		return CaseWaitingEvidence
	default:
		return CaseWaitingEvidence
	}
}

func workflowStringPtr(value string) *string { return &value }

func (o *CaseOrchestrator) ContinueWithEvidence(ctx context.Context, cmd ContinueWithEvidenceCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	if err := validateJSONObject("continuation input", cmd.InputJSON, true); err != nil {
		return IncidentCase{}, err
	}
	if o == nil || o.store == nil {
		return IncidentCase{}, errors.New("case orchestrator store is required")
	}
	commandDigest := sha256.Sum256(mustJSON(cmd))
	identity := hex.EncodeToString(commandDigest[:])
	release := workflowCommandLocks.acquire("continue:" + cmd.IdempotencyKey)
	defer release()
	if replay, found, err := o.store.GetCommittedCaseMutation(ctx, cmd.IdempotencyKey); err != nil {
		return IncidentCase{}, err
	} else if found {
		var payload struct {
			Identity string `json:"continuation_identity_sha256"`
		}
		if json.Unmarshal(replay.Event.PayloadJSON, &payload) != nil || payload.Identity != identity || replay.Event.CaseID != cmd.CaseID || replay.Event.ActorID != cmd.ActorID {
			return IncidentCase{}, ErrIdempotencyConflict
		}
		return replay.ResultCase, nil
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if cmd.Phase != "" && cmd.Phase != PhaseInvestigation && cmd.Phase != PhaseFix {
		return IncidentCase{}, errors.New("only investigation and fix phases are supported")
	}
	if incident.Status == CaseMergeConflict {
		result, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), Steps: []CaseMutationStep{{To: CaseWaitingMergeApproval, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "merge_reinspection_confirmed", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: mustJSON(map[string]any{"evidence": cmd.InputJSON, "continuation_identity_sha256": identity})}}}})
		return result.Case, err
	}
	to, mode, phase := continuationTarget(incident, cmd.Phase)
	if to == "" {
		return IncidentCase{}, ErrApprovalNotReady
	}
	previous, err := o.store.GetAttempt(ctx, incident.CurrentAttemptID)
	if err != nil {
		return IncidentCase{}, err
	}
	if previous.CaseID != incident.ID || previous.CycleNumber != incident.CycleNumber || previous.Phase != phase || previous.BotKey != cmd.Bot.Key || previous.AgentTarget != cmd.Bot.Target {
		return IncidentCase{}, ErrApprovalScope
	}
	parentID, input := previous.ID, CloneRawMessage(cmd.InputJSON)
	if phase == PhaseFix {
		// A retry inherits the approved scope and root cause. Supplemental text cannot
		// replace the baseline, repositories or rework identity of the approved fix.
		var original, supplemental map[string]json.RawMessage
		if err := json.Unmarshal(previous.InputJSON, &original); err != nil {
			return IncidentCase{}, err
		}
		if err := json.Unmarshal(cmd.InputJSON, &supplemental); err != nil {
			return IncidentCase{}, err
		}
		if original == nil {
			original = map[string]json.RawMessage{}
		}
		if value, ok := supplemental["user_input"]; ok {
			original["user_input"] = value
		}
		input = mustJSON(original)
		parentID = previous.ParentAttemptID
	}
	attempt := newAttempt(incident, phase, mode, cmd.IdempotencyKey, cmd.Bot, input, parentID)
	result, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), CreateAttempts: []PhaseAttempt{attempt}, Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(attempt.ID)}, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: "evidence_continued", ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: mustJSON(map[string]string{"attempt_id": attempt.ID, "continuation_identity_sha256": identity})}}}})
	if err != nil || result.Replay {
		return result.Case, err
	}
	if o.runner == nil {
		return o.phaseScheduleFailure(ctx, result.Case, attempt, cmd.IdempotencyKey, errors.New("phase runner is unavailable"))
	}
	if err := o.startPhase(attempt, cmd.Bug, cmd.Bot); err != nil {
		return o.phaseScheduleFailure(ctx, result.Case, attempt, cmd.IdempotencyKey, err)
	}
	return result.Case, nil
}
func continuationTarget(incident IncidentCase, requested Phase) (CaseStatus, AttemptMode, Phase) {
	if incident.Status == CaseFixFailed && (requested == "" || requested == PhaseFix) {
		return CaseFixing, "", PhaseFix
	}
	if incident.Status == CaseWaitingEvidence && (requested == "" || requested == PhaseInvestigation) {
		return CaseInvestigating, "", PhaseInvestigation
	}
	return "", "", ""
}
