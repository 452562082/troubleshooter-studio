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

type DeploymentVerifier interface {
	Verify(context.Context, DeploymentVerificationRequest) (DeploymentObservation, error)
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

type DeploymentVerificationRequest struct {
	CaseID            string            `json:"case_id"`
	Environment       string            `json:"environment"`
	ExpectedCommits   map[string]string `json:"expected_commits"`
	ObservedVersion   string            `json:"observed_version,omitempty"`
	ObservedCommits   map[string]string `json:"observed_commits,omitempty"`
	Source            string            `json:"source"`
	ConfigFingerprint string            `json:"config_fingerprint"`
	ConfigSnapshot    json.RawMessage   `json:"config_snapshot"`
}

type DeploymentReservation struct {
	ReservationID           string                        `json:"reservation_id"`
	ReservationKey          string                        `json:"reservation_key"`
	CallerIdempotencyKey    string                        `json:"caller_idempotency_key"`
	ActorID                 string                        `json:"actor_id"`
	OriginalExpectedVersion int64                         `json:"original_expected_version"`
	CycleNumber             int                           `json:"cycle_number"`
	Environment             string                        `json:"environment"`
	ExpectedCommits         map[string]string             `json:"expected_commits"`
	Bug                     Bug                           `json:"bug"`
	Bot                     BotRef                        `json:"bot"`
	VerifierInput           DeploymentVerificationRequest `json:"verifier_input"`
	RegressionInputJSON     []byte                        `json:"regression_input_json"`
}

func (r DeploymentVerificationRequest) Clone() DeploymentVerificationRequest {
	r.ExpectedCommits = CloneStringMap(r.ExpectedCommits)
	r.ObservedCommits = CloneStringMap(r.ObservedCommits)
	r.ConfigSnapshot = CloneRawMessage(r.ConfigSnapshot)
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
	TargetHeads     map[string]string
}

type NotifyDeployedCommand struct {
	CaseID                    string
	ExpectedVersion           int64
	IdempotencyKey            string
	ActorID                   string
	ExpectedCommits           map[string]string
	ObservedVersion           string
	ObservedCommits           map[string]string
	Source                    string
	VerifierConfigFingerprint string
	VerifierConfigSnapshot    json.RawMessage
	Bug                       Bug
	Bot                       BotRef
	InputJSON                 json.RawMessage
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
	deployment       DeploymentVerifier
	recoveryContext  RecoveryContextResolver
	recoveryContexts map[string]resolvedRecoveryContext
	mu               sync.Mutex
	recoveryStarted  map[string]struct{}
	scheduleTimeout  time.Duration
	cancelTimeout    time.Duration
	cancelWorkers    chan struct{}
}

func NewCaseOrchestrator(store *CaseStore, runner PhaseRunner, git GitIntegration, deployment DeploymentVerifier) *CaseOrchestrator {
	return &CaseOrchestrator{store: store, runner: runner, git: git, deployment: deployment, recoveryStarted: make(map[string]struct{}), recoveryContexts: make(map[string]resolvedRecoveryContext), scheduleTimeout: 30 * time.Second, cancelTimeout: 30 * time.Second, cancelWorkers: make(chan struct{}, cancelWorkerCapacity)}
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
	if incident.Status != CasePendingValidation {
		return IncidentCase{}, &ErrInvalidTransition{From: incident.Status, To: CaseValidating}
	}
	attempt := newAttempt(incident, PhaseValidation, AttemptReproduce, cmd.IdempotencyKey, cmd.Bot, cmd.InputJSON, "")
	return o.beginPhase(ctx, incident, CaseValidating, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "validation_started")
}

func (o *CaseOrchestrator) CreateAndStartCase(ctx context.Context, cmd CreateAndStartCaseCommand) (IncidentCase, error) {
	if o == nil || o.store == nil {
		return IncidentCase{}, errors.New("case orchestrator store is required")
	}
	if strings.TrimSpace(cmd.CaseID) == "" || strings.TrimSpace(cmd.IdempotencyKey) == "" || strings.TrimSpace(cmd.ActorID) == "" {
		return IncidentCase{}, errors.New("case ID, idempotency key, and actor ID are required")
	}
	if cmd.ExpectedVersion < 0 {
		return IncidentCase{}, errors.New("expected version must not be negative")
	}
	if strings.TrimSpace(cmd.Bug.ID) == "" || strings.TrimSpace(cmd.Bot.Key) == "" || strings.TrimSpace(cmd.Bot.Target) == "" {
		return IncidentCase{}, errors.New("Bug ID and Bot key/target are required")
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
	environment := strings.TrimSpace(cmd.Bug.Env)
	if environment == "" {
		environment = strings.TrimSpace(cmd.Bot.Env)
	}
	pending := IncidentCase{ID: targetID, BugID: cmd.Bug.ID, Source: cmd.Bug.Source, SystemID: cmd.Bug.SystemID, Environment: environment, Status: CasePendingValidation, CycleNumber: cycle, SelectedBotKey: cmd.Bot.Key}
	creation, createErr := o.store.CreateCaseWithIdentity(ctx, CaseCreation{Case: pending, IdempotencyKey: cmd.IdempotencyKey, ActorID: cmd.ActorID, RequestJSON: mustJSON(cmd)})
	if createErr != nil {
		return IncidentCase{}, createErr
	}
	if creation.ExistingOpen {
		return creation.Case.Clone(), nil
	}
	return o.StartCase(ctx, StartCaseCommand{CaseID: creation.Case.ID, ExpectedVersion: creation.Case.Version, IdempotencyKey: cmd.IdempotencyKey + ":start", ActorID: cmd.ActorID, Bug: cmd.Bug, Bot: cmd.Bot, InputJSON: cmd.InputJSON})
}

func (o *CaseOrchestrator) ContinueWithEvidence(ctx context.Context, cmd ContinueWithEvidenceCommand) (IncidentCase, error) {
	if err := validateCommand(cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey, cmd.ActorID); err != nil {
		return IncidentCase{}, err
	}
	if cmd.Phase == PhaseRegression {
		release := workflowCommandLocks.acquire("continue-regression:" + cmd.IdempotencyKey)
		defer release()
		if replayed, found, replayErr := o.replayRegressionContinuation(ctx, cmd); found || replayErr != nil {
			return replayed, replayErr
		}
	}
	incident, err := o.loadForCommand(ctx, cmd.CaseID, cmd.ExpectedVersion, cmd.IdempotencyKey)
	if err != nil {
		return IncidentCase{}, err
	}
	if cmd.Phase == PhaseRegression {
		if replayed, found, replayErr := o.replayRegressionContinuation(ctx, cmd); found || replayErr != nil {
			return replayed, replayErr
		}
	}
	if incident.Status != CaseWaitingEvidence && incident.Status != CaseNotReproduced && incident.Status != CaseFixFailed && incident.Status != CaseDeploymentUnverified && incident.Status != CaseMergeConflict {
		return IncidentCase{}, ErrApprovalNotReady
	}
	if incident.Status == CaseDeploymentUnverified || incident.Status == CaseMergeConflict {
		to, eventType := CaseWaitingDeployment, "deployment_proof_updated"
		if incident.Status == CaseMergeConflict {
			to, eventType = CaseWaitingMergeApproval, "merge_reinspection_confirmed"
		}
		evidence := CloneRawMessage(cmd.InputJSON)
		if len(evidence) == 0 {
			evidence = []byte(`{}`)
		}
		if inputErr := validateJSONObject("continuation evidence", evidence, true); inputErr != nil {
			return IncidentCase{}, inputErr
		}
		payload := mustJSON(map[string]any{"evidence": evidence})
		mutation, mutationErr := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: cmd.IdempotencyKey, RequestJSON: mustJSON(cmd), Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", cmd.IdempotencyKey), EventType: eventType, ActorType: "user", ActorID: cmd.ActorID, PayloadJSON: payload}}}})
		if mutationErr != nil {
			return IncidentCase{}, mutationErr
		}
		return mutation.Case, nil
	}
	to, mode, phase := continuationTarget(incident, cmd.Phase)
	if to == "" {
		return IncidentCase{}, fmt.Errorf("cannot continue phase %q from %s", cmd.Phase, incident.Status)
	}
	input := CloneRawMessage(cmd.InputJSON)
	continuationIdentity := ""
	if phase == PhaseRegression {
		previous, loadErr := o.store.GetAttempt(ctx, incident.CurrentAttemptID)
		if loadErr != nil || previous.Phase != PhaseRegression || previous.Mode != AttemptRegression || previous.CycleNumber != incident.CycleNumber || previous.BotKey != cmd.Bot.Key || previous.AgentTarget != cmd.Bot.Target {
			return IncidentCase{}, ErrRegressionBinding
		}
		var regression RegressionValidationInput
		if json.Unmarshal(previous.InputJSON, &regression) != nil || o.validatePersistedRegressionBinding(ctx, incident, regression) != nil {
			return IncidentCase{}, ErrRegressionBinding
		}
		canonicalInput, inputErr := canonicalJSONObject(input)
		if inputErr != nil {
			return IncidentCase{}, inputErr
		}
		if containsSensitiveData(canonicalInput) {
			return IncidentCase{}, errors.New("regression supplemental evidence contains sensitive data")
		}
		continuationIdentity, inputErr = regressionContinuationIdentityDigest(cmd)
		if inputErr != nil {
			return IncidentCase{}, inputErr
		}
		regression.SupplementalEvidence = canonicalInput
		input = mustJSON(regression)
	}
	attempt := newAttempt(incident, phase, mode, cmd.IdempotencyKey, cmd.Bot, input, incident.CurrentAttemptID)
	if phase == PhaseRegression {
		payload := mustJSON(map[string]string{"attempt_id": attempt.ID, "continuation_identity_sha256": continuationIdentity})
		return o.beginPhaseWithUpdateAndPayload(ctx, incident, to, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "evidence_continued", CaseSnapshotUpdate{}, payload)
	}
	return o.beginPhase(ctx, incident, to, attempt, cmd.Bug, cmd.Bot, cmd.IdempotencyKey, cmd.ActorID, "evidence_continued")
}

func (o *CaseOrchestrator) replayRegressionContinuation(ctx context.Context, cmd ContinueWithEvidenceCommand) (IncidentCase, bool, error) {
	replay, found, err := o.store.GetCommittedCaseMutation(ctx, cmd.IdempotencyKey)
	if err != nil || !found {
		return IncidentCase{}, found, err
	}
	identityDigest, digestErr := regressionContinuationIdentityDigest(cmd)
	if digestErr != nil {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	validEvent := replay.Event.EventType == "evidence_continued" &&
		replay.Event.FromStatus == CaseWaitingEvidence && replay.Event.ActorID == cmd.ActorID &&
		replay.Event.CaseID == cmd.CaseID && replay.ResultCase.Version >= 2 &&
		cmd.ExpectedVersion == replay.ResultCase.Version-1 && cmd.Phase == PhaseRegression
	if !validEvent {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	retry, err := o.store.GetAttempt(ctx, replay.ResultCase.CurrentAttemptID)
	if err != nil || retry.Phase != PhaseRegression || retry.Mode != AttemptRegression || retry.CaseID != cmd.CaseID || retry.ParentAttemptID == "" || retry.ParentAttemptID == retry.ID || retry.BotKey != cmd.Bot.Key || retry.AgentTarget != cmd.Bot.Target {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	parent, err := o.store.GetAttempt(ctx, retry.ParentAttemptID)
	if err != nil || parent.Phase != PhaseRegression || parent.Mode != AttemptRegression || parent.CaseID != cmd.CaseID || parent.CycleNumber != retry.CycleNumber {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	var regression RegressionValidationInput
	if json.Unmarshal(parent.InputJSON, &regression) != nil {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	supplement, inputErr := canonicalJSONObject(cmd.InputJSON)
	if inputErr != nil || containsSensitiveData(supplement) {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	regression.SupplementalEvidence = supplement
	if !bytes.Equal(mustJSON(regression), retry.InputJSON) {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	var payload struct {
		AttemptID                  string `json:"attempt_id"`
		ContinuationIdentitySHA256 string `json:"continuation_identity_sha256"`
	}
	if json.Unmarshal(replay.Event.PayloadJSON, &payload) != nil || payload.AttemptID != retry.ID || payload.ContinuationIdentitySHA256 != identityDigest {
		return IncidentCase{}, true, ErrIdempotencyConflict
	}
	return replay.ResultCase.Clone(), true, nil
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
	if incident.CurrentAttemptID != cmd.RootCauseAttemptID {
		if replay, replayErr := o.hasEvent(ctx, incident.ID, cmd.IdempotencyKey); replayErr != nil {
			return IncidentCase{}, replayErr
		} else if replay {
			return o.replayFixApproval(ctx, cmd)
		}
	}
	if err := validateFixApprovalRootCause(ctx, o.store, incident, cmd.RootCauseAttemptID); err != nil {
		return IncidentCase{}, err
	}
	attempt, request := buildFixApprovalMutation(cmd, incident.CycleNumber)
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

func buildFixApprovalMutation(cmd ApproveFixCommand, cycleNumber int) (PhaseAttempt, CaseMutation) {
	incident := IncidentCase{ID: cmd.CaseID, CycleNumber: cycleNumber}
	scope, _ := json.Marshal(map[string]string{"root_cause_attempt_id": cmd.RootCauseAttemptID})
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
	_, request := buildFixApprovalMutation(cmd, root.CycleNumber)
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
			// Every change produced by the winning successful fix attempt remains
			// in scope. PushStatus is subsequently reused for per-repository merge
			// progress (merge_local/push_unknown/conflict), so filtering only
			// "pushed" would silently drop the exact blocked repository when a
			// fresh approval is required after target-head drift.
			if change.AttemptID == incident.CurrentAttemptID && change.FixCommit != "" && change.TargetEnvironmentBranch != "" {
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
	request := MergeRequest{CaseID: incident.ID, FixCommits: fixes, TargetBranches: targets, Changes: selected}
	targetHeads := map[string]string{}
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
	}
	if hasReservation {
		for _, approved := range scopeValue.CodeChanges {
			if approved.TargetHead == "" || approved.ApprovalKey != MergeApprovalKey(incident.ID, approved.Repo, approved.FixCommit, approved.TargetBranch, approved.TargetHead) {
				return IncidentCase{}, ErrApprovalScope
			}
			targetHeads[approved.Repo] = approved.TargetHead
		}
		if len(targetHeads) != len(selected) {
			return IncidentCase{}, ErrApprovalScope
		}
	}
	request.Changes = selected
	request.TargetHeads = targetHeads
	for _, change := range selected {
		head := targetHeads[change.Repo]
		approvedChanges = append(approvedChanges, ApprovedCodeChange{ID: change.ID, Repo: change.Repo, FixCommit: change.FixCommit, TargetBranch: change.TargetEnvironmentBranch, TargetHead: head, ApprovalKey: MergeApprovalKey(incident.ID, change.Repo, change.FixCommit, change.TargetEnvironmentBranch, head)})
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
		return o.recoverReservedMerge(ctx, current, cmd.IdempotencyKey, selected, request)
	}
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
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: reserved.Case.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(result), CodeChanges: selected, Steps: []CaseMutationStep{{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_pushed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(result)}}}})
	if err != nil {
		return IncidentCase{}, err
	}
	return done.Case, nil
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

func (o *CaseOrchestrator) recoverReservedMerge(ctx context.Context, incident IncidentCase, key string, changes []CodeChange, request MergeRequest) (IncidentCase, error) {
	if o.git == nil {
		return incident, nil
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
	done, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: completedKey, RequestJSON: mustJSON(changes), CodeChanges: changes, Steps: []CaseMutationStep{{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", completedKey), EventType: "merge_push_resumed", ActorType: "git", ActorID: "git-integration", PayloadJSON: mustJSON(changes)}}}})
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
		if identityErr := validateDeploymentReservationIdentity(reservation, reserveKey, cmd.IdempotencyKey, reservationEvent.ActorID); identityErr != nil || cmd.ActorID != reservationEvent.ActorID {
			if identityErr == nil {
				identityErr = fmt.Errorf("%w: command actor does not match reservation event", ErrDeploymentReservationIdentityInvalid)
			}
			return IncidentCase{}, errors.Join(ErrIdempotencyConflict, identityErr)
		}
		supplied := reservation.VerifierInput
		supplied.ObservedVersion = cmd.ObservedVersion
		supplied.ObservedCommits = CloneStringMap(cmd.ObservedCommits)
		supplied.Source = reservation.VerifierInput.Source
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
		request := DeploymentVerificationRequest{CaseID: incident.ID, Environment: incident.Environment, ExpectedCommits: expected, ObservedVersion: cmd.ObservedVersion, ObservedCommits: CloneStringMap(cmd.ObservedCommits), Source: normalizedDeploymentSource(cmd.Source), ConfigFingerprint: strings.TrimSpace(cmd.VerifierConfigFingerprint), ConfigSnapshot: CloneRawMessage(cmd.VerifierConfigSnapshot)}
		reservation = DeploymentReservation{ReservationID: stableID("deployment-reservation", reserveKey), ReservationKey: reserveKey, CallerIdempotencyKey: cmd.IdempotencyKey, ActorID: cmd.ActorID, OriginalExpectedVersion: cmd.ExpectedVersion, CycleNumber: scope.CycleNumber, Environment: incident.Environment, ExpectedCommits: expected, Bug: cmd.Bug, Bot: cmd.Bot, VerifierInput: request}
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

func normalizedDeploymentSource(source string) string {
	if source = strings.ToLower(strings.TrimSpace(source)); source != "" {
		return source
	}
	return "manual"
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
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = now
	}
	if observation.VerificationSource == "" {
		observation.VerificationSource = "deployment-verifier"
	}
	key := reservation.ReservationKey + ":result"
	steps := []CaseMutationStep{}
	if observation.Result == DeploymentResultMatched && verifyErr == nil {
		steps = append(steps, CaseMutationStep{To: CaseWaitingDeployment, Event: TransitionEvent{ID: stableID("event", key), EventType: "deployment_verification_completed", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}}, CaseMutationStep{To: CaseDeploymentVerified, Event: TransitionEvent{ID: stableID("event", key+":verified"), EventType: "deployment_verified", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}})
	} else {
		if verifyErr != nil {
			observation.Result = DeploymentResultUnavailable
			if observation.DiagnosticCode == "" {
				observation.DiagnosticCode = "verifier_unavailable"
				observation.DiagnosticMessage = "部署版本验证暂不可用"
			}
		}
		steps = append(steps, CaseMutationStep{To: CaseDeploymentUnverified, AuditOnly: true, Event: TransitionEvent{ID: stableID("event", key), EventType: "deployment_unverified", ActorType: "studio", ActorID: "deployment-verifier", PayloadJSON: mustJSON(observation)}})
	}
	mutation, err := o.store.ApplyCaseMutation(durable, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: mustJSON(map[string]any{"observation": observation, "error_code": observation.DiagnosticCode}), Observations: []DeploymentObservation{observation}, Steps: steps})
	if err != nil {
		return IncidentCase{}, errors.Join(verifyErr, err)
	}
	if observation.Result == DeploymentResultMatched && verifyErr == nil && !mutation.Replay {
		if _, startErr := o.StartRegression(durable, mutation.Case.ID, mutation.Case.Version); startErr != nil {
			current, _ := o.store.GetCase(durable, mutation.Case.ID)
			if waiting, handled, readinessErr := o.failSafeRegressionReadiness(durable, current, startErr); handled {
				return waiting, readinessErr
			}
			return current, startErr
		}
		current, loadErr := o.store.GetCase(durable, mutation.Case.ID)
		if loadErr != nil {
			return IncidentCase{}, loadErr
		}
		return current, nil
	}
	return mutation.Case, verifyErr
}

func (o *CaseOrchestrator) failSafeRegressionReadiness(ctx context.Context, incident IncidentCase, cause error) (IncidentCase, bool, error) {
	if !errors.Is(cause, ErrRegressionOriginalScenario) && !errors.Is(cause, ErrRegressionOriginalEvidence) {
		return IncidentCase{}, false, nil
	}
	if incident.Status == CaseWaitingEvidence {
		return incident, true, nil
	}
	if incident.Status != CaseDeploymentVerified {
		return IncidentCase{}, true, cause
	}
	reason := "original_validation_scenario_incomplete"
	if errors.Is(cause, ErrRegressionOriginalEvidence) {
		reason = "original_validation_evidence_missing"
	}
	key := fmt.Sprintf("regression-readiness:%s:cycle:%d:%s", incident.ID, incident.CycleNumber, reason)
	payload := mustJSON(map[string]string{"reason": reason})
	update := CaseSnapshotUpdate{}
	attempts, listErr := o.store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if listErr != nil {
		return IncidentCase{}, true, listErr
	}
	for index := len(attempts) - 1; index >= 0; index-- {
		candidate := attempts[index]
		if candidate.Phase == PhaseValidation && candidate.Mode == AttemptReproduce && candidate.Status == AttemptStatusSucceeded {
			update.CurrentAttemptID = workflowStringPtr(candidate.ID)
			break
		}
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: payload, Snapshot: update, Steps: []CaseMutationStep{{To: CaseWaitingEvidence, Event: TransitionEvent{ID: stableID("event", key), EventType: "regression_readiness_evidence_required", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: payload}}}})
	if err != nil {
		return IncidentCase{}, true, err
	}
	return mutation.Case, true, nil
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
	} else if found && !equivalentCompletionCommands(intent, cmd) {
		return IncidentCase{}, ErrIdempotencyConflict
	}
	if err := validateCompletionAttemptPhase(attempt.Phase, cmd); err != nil {
		return IncidentCase{}, err
	}
	if cmd.Outcome == PhaseOutcomeReproduced {
		result, parseErr := ParseValidationResult(cmd.OutputJSON)
		if parseErr != nil || result.VerificationStatus != "reproduced" {
			return IncidentCase{}, errors.Join(errors.New("reproduced completion requires a complete validation result"), parseErr)
		}
		artifacts, artifactErr := o.store.ListEvidenceArtifacts(ctx, incident.ID)
		if artifactErr != nil {
			return IncidentCase{}, artifactErr
		}
		registered := false
		for _, artifact := range artifacts {
			if artifact.AttemptID == attempt.ID {
				registered = true
				break
			}
		}
		if !registered {
			return IncidentCase{}, errors.New("reproduced completion requires a registered evidence artifact")
		}
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
	if err := o.validateRegressionCompletion(ctx, incident, attempt, cmd); err != nil {
		return IncidentCase{}, err
	}
	attempt.OutputJSON, attempt.ErrorCode, attempt.ErrorMessage, attempt.Usage = CloneRawMessage(cmd.OutputJSON), cmd.ErrorCode, cmd.ErrorMessage, cmd.Usage
	if cmd.Outcome == PhaseOutcomeFixFailed || cmd.Outcome == PhaseOutcomeNeedsEvidence {
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
		"validation_reproduced":     PhaseOutcomeReproduced,
		"validation_not_reproduced": PhaseOutcomeNotReproduced,
		"evidence_required":         PhaseOutcomeNeedsEvidence,
		"root_cause_ready":          PhaseOutcomeRootCauseReady,
		"fix_pushed":                PhaseOutcomeFixPushed,
		"fix_failed":                PhaseOutcomeFixFailed,
		"regression_fixed":          PhaseOutcomeFixedVerified,
		"regression_failed":         PhaseOutcomeStillReproduces,
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
		nextInput, err := o.buildNextCycleInvestigationInput(ctx, attempt, cmd.OutputJSON)
		if err != nil {
			return IncidentCase{}, err
		}
		created := newAttempt(incident, PhaseInvestigation, "", cmd.IdempotencyKey+":investigation", BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}, nextInput, attempt.ID)
		created.CycleNumber = cycle
		next = &created
		update.CycleNumber = &cycle
		update.CurrentAttemptID = workflowStringPtr(created.ID)
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
	update.CurrentAttemptID = workflowStringPtr(attempt.ID)
	update.SelectedBotKey = workflowStringPtr(bot.Key)
	request, _ := json.Marshal(map[string]any{"attempt": attempt, "to": to, "event_type": eventType, "actor": actor})
	actorType := "user"
	if actor == "recovery" {
		actorType = "recovery"
	} else if actor == "studio" {
		actorType = "studio"
	}
	if len(payload) == 0 {
		payload = mustJSON(map[string]string{"attempt_id": attempt.ID})
	}
	mutation, err := o.store.ApplyCaseMutation(ctx, CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, RequestJSON: request, CreateAttempts: []PhaseAttempt{attempt}, Snapshot: update, Steps: []CaseMutationStep{{To: to, Event: TransitionEvent{ID: stableID("event", key), EventType: eventType, ActorType: actorType, ActorID: actor, PayloadJSON: payload}}}})
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
	mutationRequest := CaseMutation{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: failureKey, RequestJSON: request, FinishAttempts: []PhaseAttempt{attempt}, Steps: []CaseMutationStep{{To: failureStateForPhase(attempt.Phase), Event: TransitionEvent{ID: stableID("event", failureKey), EventType: "phase_schedule_failed", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: request}}}}
	if attempt.Phase == PhaseFix {
		mutationRequest.DeleteFixCheckpointAttemptID = attempt.ID
	}
	mutation, err := o.store.ApplyCaseMutation(failureCtx, mutationRequest)
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
