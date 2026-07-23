package bughub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type ArtifactReference struct {
	Kind            string          `yaml:"kind" json:"kind"`
	Path            string          `yaml:"path" json:"path"`
	CapturedAt      time.Time       `yaml:"captured_at" json:"captured_at"`
	Environment     string          `yaml:"environment" json:"environment"`
	Version         string          `yaml:"version,omitempty" json:"version,omitempty"`
	RequestID       string          `yaml:"request_id,omitempty" json:"request_id,omitempty"`
	TraceID         string          `yaml:"trace_id,omitempty" json:"trace_id,omitempty"`
	RedactionStatus RedactionStatus `yaml:"redaction_status" json:"redaction_status"`
}

type ValidationResult struct {
	VerificationStatus string              `yaml:"verification_status" json:"verification_status"`
	Environment        string              `yaml:"environment" json:"environment"`
	ObservedBehavior   string              `yaml:"observed_behavior,omitempty" json:"observed_behavior,omitempty"`
	ExpectedBehavior   string              `yaml:"expected_behavior,omitempty" json:"expected_behavior,omitempty"`
	ScenarioHash       string              `yaml:"scenario_hash,omitempty" json:"scenario_hash,omitempty"`
	Evidence           []ArtifactReference `yaml:"evidence" json:"evidence"`
	Gaps               []string            `yaml:"gaps" json:"gaps"`
}

func (r ValidationResult) CaseStatus() CaseStatus {
	status, _ := validationCaseStatus(r.VerificationStatus)
	return status
}

type InvestigationResult struct {
	InvestigationStatus string              `yaml:"investigation_status" json:"investigation_status"`
	Environment         string              `yaml:"environment" json:"environment"`
	RootCause           string              `yaml:"root_cause,omitempty" json:"root_cause,omitempty"`
	Confidence          string              `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	RootCauseType       RootCauseType       `yaml:"root_cause_type,omitempty" json:"root_cause_type,omitempty"`
	Remediation         RemediationPlan     `yaml:"remediation,omitempty" json:"remediation,omitempty"`
	CallChain           []CallChainHop      `yaml:"call_chain" json:"call_chain"`
	Evidence            []ArtifactReference `yaml:"evidence" json:"evidence"`
	ValidationGaps      []string            `yaml:"validation_gaps" json:"validation_gaps"`
	Gaps                []string            `yaml:"gaps" json:"gaps"`
	UncheckedScopes     []string            `yaml:"unchecked_scopes" json:"unchecked_scopes"`
}

type RootCauseType string

const (
	RootCauseCode               RootCauseType = "code"
	RootCauseData               RootCauseType = "data"
	RootCauseConfiguration      RootCauseType = "configuration"
	RootCauseInfrastructure     RootCauseType = "infrastructure"
	RootCauseNetwork            RootCauseType = "network"
	RootCauseExternalDependency RootCauseType = "external_dependency"
	RootCauseTransient          RootCauseType = "transient"
)

type RemediationMode string

const (
	RemediationCodeChange       RemediationMode = "code_change"
	RemediationOperatorAction   RemediationMode = "operator_action"
	RemediationExternalRecovery RemediationMode = "external_recovery"
	RemediationObserveOnly      RemediationMode = "observe_only"
)

// RemediationPlan tells the orchestrator which workflow owns the next action.
// It is deliberately descriptive: investigation remains read-only and never
// grants permission to mutate data, configuration, or runtime resources.
type RemediationPlan struct {
	Mode         RemediationMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	Repositories []string        `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Target       string          `yaml:"target,omitempty" json:"target,omitempty"`
	Summary      string          `yaml:"summary,omitempty" json:"summary,omitempty"`
	Rollback     string          `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	Verification string          `yaml:"verification,omitempty" json:"verification,omitempty"`
}

func (r InvestigationResult) UsesCodeFixWorkflow() bool {
	return r.RootCauseType == RootCauseCode && r.Remediation.Mode == RemediationCodeChange
}

// CallChainHop is one evidence-backed location in an investigation path. The
// precision field is deliberately explicit so a static repository candidate
// can never be displayed as an exact runtime source location.
type CallChainHop struct {
	Kind      string `yaml:"kind" json:"kind"`
	Name      string `yaml:"name" json:"name"`
	Service   string `yaml:"service,omitempty" json:"service,omitempty"`
	Repo      string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Revision  string `yaml:"revision,omitempty" json:"revision,omitempty"`
	Protocol  string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Operation string `yaml:"operation,omitempty" json:"operation,omitempty"`
	File      string `yaml:"file,omitempty" json:"file,omitempty"`
	Line      int    `yaml:"line,omitempty" json:"line,omitempty"`
	Precision string `yaml:"precision" json:"precision"`
	Evidence  string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	RequestID string `yaml:"request_id,omitempty" json:"request_id,omitempty"`
	TraceID   string `yaml:"trace_id,omitempty" json:"trace_id,omitempty"`
}

type PhaseResult struct {
	Outcome        PhaseOutcome
	OutputJSON     json.RawMessage
	CodeChanges    []CodeChange
	ArtifactInputs []ArtifactReference
}

type PhaseExecutionResult struct {
	FinalYAML string
	Usage     AgentUsage
}

// PhaseAttachment is immutable, host-provided evidence for one phase call.
// Path is an ephemeral local view and must never be persisted in agent output.
type PhaseAttachment struct {
	Kind     string
	MIMEType string
	Path     string
	SHA256   string
	Size     int64
}

type PhaseAgentExecutor interface {
	ExecutePhase(context.Context, string, BotRef, string, func(InvestigationEvent)) (PhaseExecutionResult, error)
	CancelPhase(context.Context, string) error
}

// PhaseAttachmentExecutor is implemented by target adapters that can make
// host evidence available to the model, rather than merely mentioning a host
// filesystem path in the prompt.
type PhaseAttachmentExecutor interface {
	ExecutePhaseWithAttachments(context.Context, string, BotRef, string, []PhaseAttachment, func(InvestigationEvent)) (PhaseExecutionResult, error)
}

type PhaseCompletionFunc func(context.Context, CompleteAttemptCommand) error

type AgentPhaseRunner struct {
	store                       *CaseStore
	executor                    PhaseAgentExecutor
	legacy                      *InvestigationStore
	artifactsRoot               string
	complete                    PhaseCompletionFunc
	eventSink                   InvestigationEventSink
	browserVerifier             BrowserVerifier
	browserPolicyResolver       BrowserPolicyResolver
	frontendRuntimeResolver     FrontendRuntimeResolver
	repositoryAccessResolver    RepositoryAccessResolver
	codeIntelligenceResolver    CodeIntelligenceResolver
	fixWorkspaceManager         *FixWorkspaceManager
	openStaging                 func(string, string) (attemptEvidenceStaging, error)
	completionReconcileAttempts int
	completionReconcileDelay    time.Duration

	mu        sync.Mutex
	active    map[string]context.CancelFunc
	scheduled map[string]struct{}
}

func (r *AgentPhaseRunner) SetEventSink(sink InvestigationEventSink) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventSink = sink
}

func NewAgentPhaseRunner(store *CaseStore, executor PhaseAgentExecutor, legacy *InvestigationStore, artifactsRoot string, complete PhaseCompletionFunc) *AgentPhaseRunner {
	return &AgentPhaseRunner{store: store, executor: executor, legacy: legacy, artifactsRoot: artifactsRoot, complete: complete, completionReconcileAttempts: 6, completionReconcileDelay: 2 * time.Second, active: make(map[string]context.CancelFunc), scheduled: make(map[string]struct{})}
}

func (r *AgentPhaseRunner) SetCompletionCallback(complete PhaseCompletionFunc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.complete = complete
}

func (r *AgentPhaseRunner) SetBrowserVerifier(verifier BrowserVerifier, resolver BrowserPolicyResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.browserVerifier = verifier
	r.browserPolicyResolver = resolver
}

func (r *AgentPhaseRunner) SetFixWorkspaceManager(manager *FixWorkspaceManager) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fixWorkspaceManager = manager
}

func (r *AgentPhaseRunner) Start(ctx context.Context, attempt PhaseAttempt, bug Bug, bot BotRef) error {
	if r == nil || r.store == nil || r.executor == nil {
		return errors.New("agent phase runner requires store and executor")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := attempt.Validate(); err != nil {
		return err
	}
	claimToken, err := newAttemptRunClaimToken()
	if err != nil {
		return err
	}
	// Start's caller context only bounds synchronous scheduling. The phase is a
	// durable background job and must outlive the orchestrator's short scheduling
	// timeout; explicit Cancel owns its lifetime after the reservation is visible.
	runCtx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if _, alreadyScheduled := r.scheduled[attempt.ID]; alreadyScheduled {
		r.mu.Unlock()
		cancel()
		return nil
	}
	r.scheduled[attempt.ID] = struct{}{}
	r.active[attempt.ID] = cancel
	complete := r.complete
	browserVerifier := r.browserVerifier
	browserPolicyResolver := r.browserPolicyResolver
	frontendRuntimeResolver := r.frontendRuntimeResolver
	repositoryAccessResolver := r.repositoryAccessResolver
	codeIntelligenceResolver := r.codeIntelligenceResolver
	fixWorkspaceManager := r.fixWorkspaceManager
	r.mu.Unlock()
	releaseReservation := func() {
		cancel()
		r.mu.Lock()
		delete(r.scheduled, attempt.ID)
		delete(r.active, attempt.ID)
		r.mu.Unlock()
	}
	fail := func(cause error) error {
		releaseReservation()
		return cause
	}
	if complete == nil {
		return fail(errors.New("agent phase completion callback is required"))
	}
	if strings.TrimSpace(attempt.AgentTarget) != "" && strings.TrimSpace(bot.Target) != attempt.AgentTarget {
		return fail(fmt.Errorf("bot target %q does not match persisted attempt target %q", bot.Target, attempt.AgentTarget))
	}
	if strings.TrimSpace(bot.Key) != attempt.BotKey {
		return fail(fmt.Errorf("bot key %q does not match persisted attempt bot %q", bot.Key, attempt.BotKey))
	}
	if attempt.Phase == PhaseLegacy {
		return fail(errors.New("legacy attempts are read-only projections"))
	}
	incident, err := r.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(incident.CurrentAttemptID) == "" || incident.CurrentAttemptID != attempt.ID {
		return fail(ErrAttemptNotCurrent)
	}
	if incident.Status != statusForPhase(attempt.Phase) || incident.CycleNumber != attempt.CycleNumber || strings.TrimSpace(incident.SelectedBotKey) == "" || incident.SelectedBotKey != attempt.BotKey {
		return fail(errors.New("phase attempt is not bound to the current Case status, cycle, and selected bot"))
	}
	persisted, err := r.store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		return fail(err)
	}
	if persisted.Status != AttemptStatusQueued && persisted.Status != AttemptStatusRunning {
		return fail(fmt.Errorf("phase attempt %s is not runnable: %s", persisted.ID, persisted.Status))
	}
	if !sameRunnableAttempt(persisted, attempt) {
		return fail(errors.New("caller phase attempt does not match persisted attempt"))
	}
	if _, found, err := parseCompletionIntent(persisted.OutputJSON); err != nil {
		return fail(err)
	} else if found {
		return fail(errors.New("phase attempt already has a persisted completion intent"))
	}
	executionBot, err := ExecutionBotForPhase(attempt.Phase, bot)
	if err != nil {
		return fail(err)
	}
	if attempt.Phase == PhaseValidation || attempt.Phase == PhaseRegression {
		bug, err = r.withSupplementalValidationScreenshots(ctx, attempt, bug)
		if err != nil {
			return fail(fmt.Errorf("prepare supplemental validation screenshots: %w", err))
		}
	}
	prompt, err := r.promptForAttempt(attempt, bug, executionBot)
	if err != nil {
		return fail(err)
	}
	remediationReassessment := isRemediationReassessmentAttempt(attempt)
	openStaging := r.openStaging
	if openStaging == nil {
		if attempt.Phase == PhaseValidation || attempt.Phase == PhaseRegression {
			openStaging = openOrCreateBrowserAttemptStaging
		} else {
			openStaging = openAttemptEvidenceStaging
		}
	}
	staging, err := openStaging(r.artifactsRoot, attempt.ID)
	if err != nil {
		return fail(fmt.Errorf("create Studio evidence staging: %w", err))
	}
	if !remediationReassessment {
		handoffPrompt, handoffErr := r.materializeInvestigationEvidence(ctx, attempt, staging)
		if handoffErr != nil {
			return fail(releaseUntransferredStaging(staging, fmt.Errorf("prepare validation evidence handoff: %w", handoffErr)))
		}
		prompt += handoffPrompt
		frontendPrompt, frontendErr := r.materializeFrontendRuntime(ctx, attempt, incident, staging, frontendRuntimeResolver)
		if frontendErr != nil {
			return fail(releaseUntransferredStaging(staging, fmt.Errorf("prepare frontend runtime handoff: %w", frontendErr)))
		}
		prompt += frontendPrompt
	}
	checkpoint := (*FixCheckpoint)(nil)
	if attempt.Phase == PhaseFix {
		checkpoint = &FixCheckpoint{AttemptID: attempt.ID, CaseID: attempt.CaseID, StagingLocator: fixCheckpointLocator(staging)}
	}
	if err := ctx.Err(); err != nil {
		releaseReservation()
		return releaseUntransferredStaging(staging, err)
	}
	if err := r.store.ClaimRunnableAttempt(ctx, AttemptRunClaim{Attempt: attempt, ClaimToken: claimToken, Checkpoint: checkpoint}); err != nil {
		releaseReservation()
		return releaseUntransferredStaging(staging, err)
	}
	releaseClaim := func() {
		durable, durableCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer durableCancel()
		_ = r.store.ReleaseAttemptRunClaim(durable, attempt.ID, attempt.CaseID, claimToken)
	}
	if err := ctx.Err(); err != nil {
		releaseClaim()
		releaseReservation()
		return releaseUntransferredStaging(staging, err)
	}
	valid, err := r.store.ValidateAttemptRunClaim(ctx, attempt, claimToken)
	if err != nil || !valid {
		if err == nil {
			err = ErrAttemptRunClaimConflict
		}
		releaseClaim()
		releaseReservation()
		return releaseUntransferredStaging(staging, err)
	}
	var fixWorkspace *FixWorkspaceLease
	if attempt.Phase == PhaseFix && fixWorkspaceManager != nil {
		fixWorkspace, err = fixWorkspaceManager.Prepare(ctx, attempt.CaseID, attempt.ID, incident.Environment, executionBot, attempt.InputJSON)
		if err != nil {
			releaseClaim()
			releaseReservation()
			return releaseUntransferredStaging(staging, fmt.Errorf("prepare locked source-baseline worktrees: %w", err))
		}
		prompt += fixWorkspace.Prompt()
	}
	if !remediationReassessment {
		repositoryPrompt, repositoryErr := r.materializeRepositoryAccess(ctx, attempt, incident, staging, repositoryAccessResolver, fixWorkspace)
		if repositoryErr != nil {
			releaseClaim()
			releaseReservation()
			if fixWorkspace != nil {
				_ = fixWorkspace.Close(context.Background())
			}
			return releaseUntransferredStaging(staging, fmt.Errorf("prepare repository access boundary: %w", repositoryErr))
		}
		prompt += repositoryPrompt
		prompt += evidenceStagingPrompt(staging.Path())
	}
	if attempt.Phase == PhaseFix {
		prompt += "\n## Durable fix checkpoint (mandatory)\n\nBefore the first repository push, atomically write `" + fixCheckpointManifestName + "` in the Studio staging directory (write a temporary sibling, fsync, then rename) with state=`prepared`; include every planned repository commit/branch/remote/test. After all pushes succeed, atomically replace it with the same manifest and state=`pushed` before reporting completion. JSON fields: kind=`" + fixCheckpointManifestKind + "`, version=1, case_id=`" + attempt.CaseID + "`, attempt_id=`" + attempt.ID + "`, state=`prepared|pushed`, result=<the exact structured FixResult also returned as final YAML>. Never include credentials. Recovery treats the SSH remote branch as truth, so a crash after push but before the state update remains recoverable while a pre-push crash cannot be misreported.\n"
	}
	r.startLegacyProjection(attempt, bug, executionBot)
	go r.run(runCtx, attempt.Clone(), incident.Clone(), bug, executionBot, prompt, staging, fixWorkspace, incident.Version, claimToken, complete, browserVerifier, browserPolicyResolver, codeIntelligenceResolver)
	return nil
}

// withSupplementalValidationScreenshots resolves immutable screenshots that a
// user uploaded while the Case was blocked. The paths never enter prompts or
// persisted Agent output; BrowserCoordinator converts them into bounded host
// attachments for both planning and evaluation.
func (r *AgentPhaseRunner) withSupplementalValidationScreenshots(ctx context.Context, attempt PhaseAttempt, bug Bug) (Bug, error) {
	if r == nil || r.store == nil || strings.TrimSpace(attempt.ParentAttemptID) == "" {
		return bug, nil
	}
	ancestorIDs := make(map[string]struct{})
	parentID := strings.TrimSpace(attempt.ParentAttemptID)
	for parentID != "" {
		if _, duplicate := ancestorIDs[parentID]; duplicate {
			return Bug{}, errors.New("validation evidence ancestry contains a cycle")
		}
		parent, err := r.store.GetAttempt(ctx, parentID)
		if err != nil {
			return Bug{}, err
		}
		if parent.CaseID != attempt.CaseID || parent.CycleNumber != attempt.CycleNumber {
			return Bug{}, errors.New("validation evidence ancestor does not belong to the current Case cycle")
		}
		ancestorIDs[parent.ID] = struct{}{}
		parentID = strings.TrimSpace(parent.ParentAttemptID)
	}
	registered, err := r.store.ListEvidenceArtifacts(ctx, attempt.CaseID)
	if err != nil {
		return Bug{}, err
	}
	candidates := make([]EvidenceArtifact, 0)
	for _, artifact := range registered {
		if artifact.Kind != "user_screenshot" {
			continue
		}
		if _, belongs := ancestorIDs[artifact.AttemptID]; belongs {
			candidates = append(candidates, artifact)
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].CapturedAt.Equal(candidates[right].CapturedAt) {
			return candidates[left].ID > candidates[right].ID
		}
		return candidates[left].CapturedAt.After(candidates[right].CapturedAt)
	})
	if len(candidates) > maxPhaseAttachments {
		candidates = candidates[:maxPhaseAttachments]
	}
	for index, candidate := range candidates {
		content, err := ReadEvidenceArtifactFromRoot(ctx, r.store, r.artifactsRoot, attempt.CaseID, candidate.ID)
		if err != nil {
			return Bug{}, err
		}
		if !bytes.HasPrefix(content.Content, browserPNGSignature) {
			return Bug{}, errors.New("supplemental validation screenshot is not a PNG image")
		}
		bug.Attachments = append(bug.Attachments, Attachment{
			ID:        candidate.ID,
			Name:      fmt.Sprintf("用户补充证据-%d.png", index+1),
			Type:      "image/png",
			LocalPath: content.Artifact.PathOrReference,
		})
	}
	return bug, nil
}

func newAttemptRunClaimToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func releaseUntransferredStaging(staging attemptEvidenceStaging, cause error) error {
	if staging == nil {
		return cause
	}
	if ownership, ok := staging.(interface{ CreatedForCurrentOpen() bool }); ok && !ownership.CreatedForCurrentOpen() {
		return errors.Join(cause, staging.Close())
	}
	cleanupErr := staging.Cleanup()
	closeErr := staging.Close()
	return errors.Join(cause, cleanupErr, closeErr)
}

func sameRunnableAttempt(persisted, caller PhaseAttempt) bool {
	return persisted.ID == caller.ID && persisted.CaseID == caller.CaseID &&
		persisted.CycleNumber == caller.CycleNumber && persisted.Phase == caller.Phase &&
		persisted.Mode == caller.Mode && persisted.AgentTarget == caller.AgentTarget &&
		persisted.BotKey == caller.BotKey && bytes.Equal(persisted.InputJSON, caller.InputJSON)
}

func (r *AgentPhaseRunner) Cancel(ctx context.Context, attemptID string) error {
	if r == nil || r.executor == nil {
		return errors.New("agent phase runner is unavailable")
	}
	r.mu.Lock()
	cancel, ok := r.active[attemptID]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	err := r.executor.CancelPhase(ctx, attemptID)
	if ok && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !ok && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (r *AgentPhaseRunner) run(ctx context.Context, attempt PhaseAttempt, incident IncidentCase, bug Bug, bot BotRef, prompt string, staging attemptEvidenceStaging, fixWorkspace *FixWorkspaceLease, expectedVersion int64, claimToken string, complete PhaseCompletionFunc, browserVerifier BrowserVerifier, browserPolicyResolver BrowserPolicyResolver, codeIntelligenceResolver CodeIntelligenceResolver) {
	started := time.Now()
	cleaned := false
	preserveStaging := false
	releaseClaim := func() {
		durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.store.ReleaseAttemptRunClaim(durable, attempt.ID, attempt.CaseID, claimToken)
	}
	var runtimeReceiptMu sync.Mutex
	datastoreReadObserved := false
	codeGraphQueryObserved := false
	defer func() {
		if !cleaned && !preserveStaging {
			_ = staging.Cleanup()
		}
		_ = staging.Close()
	}()
	defer func() {
		if fixWorkspace == nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_ = fixWorkspace.Close(cleanupCtx)
	}()
	defer func() {
		r.mu.Lock()
		cancel := r.active[attempt.ID]
		delete(r.active, attempt.ID)
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	emit := func(event InvestigationEvent) {
		if ctx.Err() != nil {
			return
		}
		if eventProvesDatastoreRead(event) {
			runtimeReceiptMu.Lock()
			datastoreReadObserved = true
			runtimeReceiptMu.Unlock()
		}
		if eventProvesCodeGraphQuery(event) {
			runtimeReceiptMu.Lock()
			codeGraphQueryObserved = true
			runtimeReceiptMu.Unlock()
		}
		r.projectEvent(attempt, event)
	}
	hasDatastoreRead := func() bool {
		runtimeReceiptMu.Lock()
		defer runtimeReceiptMu.Unlock()
		return datastoreReadObserved
	}
	hasCodeGraphQuery := func() bool {
		runtimeReceiptMu.Lock()
		defer runtimeReceiptMu.Unlock()
		return codeGraphQueryObserved
	}
	if err := ctx.Err(); err != nil {
		releaseClaim()
		return
	}
	claimValid, claimErr := r.store.ValidateAttemptRunClaim(ctx, attempt, claimToken)
	if claimErr != nil || !claimValid || ctx.Err() != nil {
		releaseClaim()
		return
	}
	remediationReassessment := isRemediationReassessmentAttempt(attempt)
	codeIntelligence := CodeIntelligenceManifest{}
	var codeIntelligenceErr error
	if !remediationReassessment {
		var codeIntelligencePrompt string
		codeIntelligence, codeIntelligencePrompt, codeIntelligenceErr = r.materializeCodeIntelligence(ctx, attempt, incident, staging, codeIntelligenceResolver)
		if codeIntelligenceErr == nil {
			prompt += codeIntelligencePrompt
			if attempt.Phase == PhaseInvestigation {
				state := "fallback"
				message := "CodeGraph 未就绪，代码取证将使用 rg/Read"
				if codeIntelligence.HasReadyRepository() {
					state = "ready"
					message = fmt.Sprintf("CodeGraph %d/%d 个仓库已由 Studio 准备完成", codeIntelligence.Ready, codeIntelligence.Total)
				}
				emit(InvestigationEvent{Type: "code_intelligence", Message: message, Meta: map[string]any{"state": state, "ready": codeIntelligence.Ready, "total": codeIntelligence.Total}})
			}
		}
	}
	var result PhaseExecutionResult
	var runErr = codeIntelligenceErr
	var coordinated *BrowserCoordinatorResult
	freezeBrowserArtifacts := func(ctx context.Context, references []BrowserArtifactReference) ([]browserFrozenArtifact, error) {
		return r.freezeBrowserArtifacts(ctx, attempt, staging, references)
	}
	route := browserRouteJournal{}
	if attempt.Phase == PhaseValidation || attempt.Phase == PhaseRegression {
		var routeCode string
		var routeErr error
		route, routeCode, routeErr = r.resolveBrowserRoute(ctx, attempt, incident, bug, browserPolicyResolver, staging)
		if routeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				runErr = ctxErr
			} else {
				coordinatorResult := browserCoordinatorFailure(BrowserCoordinatorResult{}, "browser_execution_interrupted")
				coordinated = &coordinatorResult
			}
		} else if routeCode != "" {
			coordinatorResult := browserCoordinatorFailure(BrowserCoordinatorResult{}, routeCode)
			coordinated = &coordinatorResult
		} else {
			boundPrompt, promptErr := r.promptForAttempt(attempt, routeBrowserBug(bug, route), bot)
			if promptErr != nil {
				coordinatorResult := browserCoordinatorFailure(BrowserCoordinatorResult{}, "browser_execution_interrupted")
				coordinated = &coordinatorResult
			} else {
				prompt = boundPrompt + evidenceStagingPrompt(staging.Path())
			}
		}
	}
	if coordinated == nil && runErr == nil {
		if route.Assisted {
			browserBug := routeBrowserBug(bug, route)
			clarifications, clarificationErr := r.browserUserClarifications(ctx, attempt)
			if clarificationErr != nil {
				coordinatorResult := browserCoordinatorFailure(BrowserCoordinatorResult{}, "browser_execution_interrupted")
				coordinated = &coordinatorResult
			} else {
				coordinatorResult, executeErr := (BrowserCoordinator{Executor: r.executor, Verifier: browserVerifier, Recipes: r.store}).Execute(ctx, BrowserCoordinatorRequest{Attempt: attempt, Bug: browserBug, Bot: bot, BasePrompt: prompt, UserClarifications: clarifications, Policy: route.Policy, StagingDir: staging.Path(), Emit: emit, FreezeArtifacts: freezeBrowserArtifacts})
				coordinated = &coordinatorResult
				runErr = executeErr
				result = PhaseExecutionResult{FinalYAML: coordinatorResult.FinalYAML, Usage: coordinatorResult.Usage}
			}
		} else {
			result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
		}
	} else if coordinated != nil {
		result = PhaseExecutionResult{FinalYAML: coordinated.FinalYAML, Usage: coordinated.Usage}
	}
	if runErr != nil && coordinated == nil && attempt.Phase != PhaseFix && ctx.Err() == nil {
		r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "read-only phase process retry"})
		firstUsage := result.Usage
		result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
		result.Usage.InputTokens += firstUsage.InputTokens
		result.Usage.OutputTokens += firstUsage.OutputTokens
	}
	if runErr == nil && coordinated == nil && attempt.Phase == PhaseInvestigation && !remediationReassessment && strings.EqualFold(strings.TrimSpace(bot.Target), "codex") && investigationInputRequiresDatastoreRead(attempt.InputJSON) && !hasDatastoreRead() && ctx.Err() == nil {
		r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "排障缺少数据层只读查询，正在自动补齐"})
		firstUsage := result.Usage
		retryPrompt := prompt + "\n\n## Mandatory datastore evidence retry\nThe frozen validation input contains request_facts with business parameters. Before returning YAML, you MUST resolve the target environment datastore from routing and execute the configured read-only mongodb/mysql/postgresql/doris/redis/elasticsearch MCP tool as applicable. Use request_facts values as the query filter, record collection/table, filter, row count, and compared fields in evidence. Do not ask the user to query it.\n"
		result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, retryPrompt, emit)
		result.Usage.InputTokens += firstUsage.InputTokens
		result.Usage.OutputTokens += firstUsage.OutputTokens
		if runErr == nil && !hasDatastoreRead() {
			runErr = errors.New("configured datastore evidence was not queried")
		}
	}
	if runErr == nil && coordinated == nil && attempt.Phase == PhaseInvestigation && !remediationReassessment && codeIntelligence.HasReadyRepository() && investigationResultRequiresCodeGraph(result.FinalYAML) && !hasCodeGraphQuery() && ctx.Err() == nil {
		r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "代码根因缺少 CodeGraph 查询，正在自动补齐"})
		firstUsage := result.Usage
		retryPrompt := prompt + "\n\n## Mandatory CodeGraph evidence retry\nThe proposed result is a code root cause, but no completed `codegraph_explore` MCP receipt was observed. Read `" + codeIntelligenceManifestName + "`, call `codegraph_explore` for the implicated ready repository using its exact `project_path` and `maxFiles=4`, then corroborate graph inferences with runtime evidence and deployed-revision source. Do not execute the CodeGraph CLI.\n"
		result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, retryPrompt, emit)
		result.Usage.InputTokens += firstUsage.InputTokens
		result.Usage.OutputTokens += firstUsage.OutputTokens
		if runErr == nil && !hasCodeGraphQuery() {
			runErr = errors.New("configured CodeGraph was ready but codegraph_explore was not queried")
		}
	}
	if runErr == nil && coordinated == nil && attempt.Phase == PhaseInvestigation && !remediationReassessment && ctx.Err() == nil {
		unknown, allowed, repositoryErr := validateInvestigationRemediationRepositories(staging, result.FinalYAML)
		if repositoryErr != nil {
			runErr = repositoryErr
		} else if len(unknown) != 0 {
			r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "修复建议引用了未配置仓库，正在按 Studio 仓库清单重新评估"})
			firstUsage := result.Usage
			allowedJSON, _ := json.Marshal(allowed)
			unknownJSON, _ := json.Marshal(unknown)
			retryPrompt := prompt + "\n\n## Mandatory remediation repository correction\nThe proposed code remediation used repository identifiers " + string(unknownJSON) + " that are not in the Studio repository access manifest. The only exact configured repository identifiers allowed in `remediation.repositories` are " + string(allowedJSON) + ". Re-evaluate the affected code using those exact identifiers. Never substitute a service, Deployment, container, image, directory basename, or inferred alias. If none of the configured repositories can implement the fix, return `insufficient_info` and state the exact repository configuration gap instead of inventing a repository name.\n"
			result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, retryPrompt, emit)
			result.Usage.InputTokens += firstUsage.InputTokens
			result.Usage.OutputTokens += firstUsage.OutputTokens
			if runErr == nil {
				remaining, _, checkErr := validateInvestigationRemediationRepositories(staging, result.FinalYAML)
				if checkErr != nil {
					runErr = checkErr
				} else if len(remaining) != 0 {
					runErr = fmt.Errorf("remediation repositories are not configured for this Case: %s", strings.Join(remaining, ", "))
				}
			}
		}
	}
	if ctx.Err() != nil {
		releaseClaim()
		cleanupErr := staging.Cleanup()
		cleaned = cleanupErr == nil
		errorText := ctx.Err().Error()
		if cleanupErr != nil {
			errorText = "cancelled; evidence staging cleanup failed: " + cleanupErr.Error()
		}
		r.finishLegacy(attempt.ID, InvestigationCancelled, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(errorText))
		return
	}
	command := CompleteAttemptCommand{CaseID: attempt.CaseID, AttemptID: attempt.ID, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: firstNonEmpty(bot.Key, attempt.BotKey, "agent"), Usage: result.Usage}
	command.Usage.Duration = time.Since(started)
	if coordinated != nil && coordinated.ErrorCode != "" && runErr == nil {
		command.Outcome = browserFailureOutcome(attempt.Phase, coordinated.ErrorCode)
		command.OutputJSON = browserStopOutput(*coordinated)
		command.ErrorCode = coordinated.ErrorCode
		command.ErrorMessage = coordinated.ErrorMessage
	} else if runErr != nil {
		command.Outcome = failureOutcome(attempt.Phase)
		command.OutputJSON = mustJSON(map[string]any{"error_code": "agent_process_failed", "error_message": runErr.Error()})
		command.ErrorCode = "agent_process_failed"
		command.ErrorMessage = runErr.Error()
	} else {
		parsed, err := ParsePhaseResult(attempt, []byte(result.FinalYAML))
		if err != nil {
			command.Outcome = failureOutcome(attempt.Phase)
			command.OutputJSON = mustJSON(map[string]any{"error_code": "invalid_phase_result", "error_message": err.Error()})
			command.ErrorCode = "invalid_phase_result"
			command.ErrorMessage = err.Error()
		} else {
			command.Outcome, command.OutputJSON, command.CodeChanges = parsed.Outcome, parsed.OutputJSON, parsed.CodeChanges
			if err := r.validateFixCheckpoint(staging, attempt, parsed); err != nil {
				command.Outcome = failureOutcome(attempt.Phase)
				command.OutputJSON = mustJSON(map[string]any{"error_code": "fix_checkpoint_invalid", "error_message": err.Error()})
				command.ErrorCode = "fix_checkpoint_invalid"
				command.ErrorMessage = err.Error()
				command.CodeChanges = nil
			} else if err := fixWorkspace.ValidateResult(ctx, parsed); err != nil {
				command.Outcome = failureOutcome(attempt.Phase)
				command.OutputJSON = mustJSON(map[string]any{"error_code": "fix_base_mismatch", "error_message": err.Error()})
				command.ErrorCode = "fix_base_mismatch"
				command.ErrorMessage = err.Error()
				command.CodeChanges = nil
			} else if err := r.validateResultEnvironment(ctx, attempt, parsed); err != nil {
				command.Outcome = failureOutcome(attempt.Phase)
				command.OutputJSON = mustJSON(map[string]any{"error_code": "phase_environment_mismatch", "error_message": err.Error()})
				command.ErrorCode = "phase_environment_mismatch"
				command.ErrorMessage = err.Error()
			} else if err := r.validateRegressionEvidence(ctx, attempt, parsed); err != nil {
				command.Outcome = PhaseOutcomeNeedsEvidence
				command.OutputJSON = mustJSON(map[string]any{"error_code": "regression_evidence_invalid", "error_message": err.Error(), "evidence_limitation": true})
				command.ErrorCode = "regression_evidence_invalid"
				command.ErrorMessage = err.Error()
			} else if err := func() error {
				if coordinated != nil {
					return nil
				}
				return r.registerArtifacts(ctx, attempt, staging, parsed.ArtifactInputs)
			}(); err != nil {
				if attempt.Phase == PhaseFix && parsed.Outcome == PhaseOutcomeFixFailed {
					sanitized, sanitizeErr := fixResultWithoutOptionalEvidence(parsed.OutputJSON)
					if sanitizeErr == nil {
						command.OutputJSON = sanitized
						r.projectEvent(attempt, InvestigationEvent{Type: "agent_message", Message: "修复已阻塞；Agent 返回的无效可选制品引用已安全忽略"})
					} else {
						command.Outcome = failureOutcome(attempt.Phase)
						command.OutputJSON = mustJSON(map[string]any{"error_code": artifactErrorCode(err), "error_message": err.Error(), "evidence_limitation": true})
						command.ErrorCode = artifactErrorCode(err)
						command.ErrorMessage = err.Error()
					}
				} else {
					command.Outcome = failureOutcome(attempt.Phase)
					command.OutputJSON = mustJSON(map[string]any{"error_code": artifactErrorCode(err), "error_message": err.Error(), "evidence_limitation": true})
					command.ErrorCode = artifactErrorCode(err)
					command.ErrorMessage = err.Error()
				}
			} else if err := r.validateRegisteredRegressionEvidence(ctx, attempt, parsed); err != nil {
				command.Outcome = PhaseOutcomeNeedsEvidence
				command.OutputJSON = mustJSON(map[string]any{"error_code": "regression_evidence_invalid", "error_message": err.Error(), "evidence_limitation": true})
				command.ErrorCode = "regression_evidence_invalid"
				command.ErrorMessage = err.Error()
			}
		}
	}
	if completionContainsSensitiveData(command) {
		command.Outcome = failureOutcome(attempt.Phase)
		command.OutputJSON = mustJSON(map[string]any{"error_code": "sensitive_phase_output", "evidence_limitation": true})
		command.ErrorCode = "sensitive_phase_output"
		command.ErrorMessage = "agent phase output contained sensitive data and was not persisted"
		command.CodeChanges = nil
	}
	command.ExpectedVersion = expectedVersion
	preserveStaging = true
	if err := r.store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		releaseClaim()
		r.finishLegacy(attempt.ID, InvestigationFailed, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(err.Error()))
		return
	}
	completionErr := complete(ctx, command)
	if attempt.Phase == PhaseFix && errors.Is(completionErr, ErrFixInspectionUnavailable) {
		attempts := r.completionReconcileAttempts
		if attempts < 1 {
			attempts = 1
		}
		delay := r.completionReconcileDelay
		for retry := 1; retry < attempts && errors.Is(completionErr, ErrFixInspectionUnavailable); retry++ {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				completionErr = ctx.Err()
			case <-timer.C:
				completionErr = complete(ctx, command)
			}
		}
	}
	if completionErr != nil {
		if attempt.Phase == PhaseFix {
			// The completion boundary performs the authoritative remote-ref check.
			// Preserve the durable checkpoint for bounded startup recovery when that
			// external read is temporarily unavailable or reports a mismatch.
			preserveStaging = true
		}
		r.finishLegacy(attempt.ID, InvestigationFailed, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(completionErr.Error()))
		return
	}
	preserveStaging = false
	if cleanupErr := staging.Cleanup(); cleanupErr == nil {
		cleaned = true
	}
	status := InvestigationSucceeded
	if command.ErrorCode != "" {
		status = InvestigationFailed
	}
	r.finishLegacy(attempt.ID, status, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(command.ErrorMessage))
}

func evidenceStagingPrompt(path string) string {
	return "\n## Studio evidence staging (mandatory)\n\nSTUDIO_EVIDENCE_STAGING_DIR=" + path + "\nWrite every evidence file beneath this exact directory. In final YAML, every evidence entry must include non-empty `kind`, `path`, and `environment`; `path` must be a clean relative path from this directory, while absolute paths and `..` are rejected. Scratch notes and summary files are not evidence: leave `evidence: []` when there is no registrable runtime or test artifact. Studio derives timestamps and redaction status from securely opened file bytes.\n"
}

func fixResultWithoutOptionalEvidence(output json.RawMessage) (json.RawMessage, error) {
	var result FixResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}
	if result.FixStatus != "blocked" && result.FixStatus != "failed" {
		return nil, errors.New("only blocked or failed fix results have optional evidence")
	}
	result.Evidence = []ArtifactReference{}
	return json.Marshal(result)
}

func (r *AgentPhaseRunner) validateFixCheckpoint(staging attemptEvidenceStaging, attempt PhaseAttempt, result PhaseResult) error {
	if attempt.Phase != PhaseFix || result.Outcome != PhaseOutcomeFixPushed {
		return nil
	}
	captured, err := staging.Capture(fixCheckpointManifestName)
	if err != nil {
		return err
	}
	changes, err := parseFixCheckpointManifest(captured.Content, attempt, false)
	if err != nil {
		return err
	}
	return validateFixCheckpointMatchesResult(changes, result.CodeChanges)
}

func safeLegacyPhaseText(value string) string {
	if containsSensitiveData([]byte(value)) {
		return "[sensitive phase output suppressed]"
	}
	return value
}

func completionContainsSensitiveData(command CompleteAttemptCommand) bool {
	if containsSensitiveData(command.OutputJSON) || containsSensitiveData([]byte(command.ErrorMessage)) {
		return true
	}
	changes, err := json.Marshal(command.CodeChanges)
	return err != nil || containsSensitiveData(changes)
}

func (r *AgentPhaseRunner) validateResultEnvironment(ctx context.Context, attempt PhaseAttempt, result PhaseResult) error {
	incident, err := r.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	var envelope struct {
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal(result.OutputJSON, &envelope); err != nil {
		return err
	}
	if strings.TrimSpace(envelope.Environment) == "" || envelope.Environment != incident.Environment {
		return fmt.Errorf("phase result environment %q does not match case environment %q", envelope.Environment, incident.Environment)
	}
	return nil
}

func (r *AgentPhaseRunner) promptForAttempt(attempt PhaseAttempt, bug Bug, bot BotRef) (string, error) {
	switch attempt.Phase {
	case PhaseValidation:
		if attempt.Mode != AttemptReproduce {
			return "", fmt.Errorf("validation phase requires reproduce mode")
		}
		continuation, err := r.validationPromptContext(context.Background(), attempt)
		if err != nil {
			return "", err
		}
		if len(continuation.UserInputs) == 0 && continuation.StructuredInput == "" && continuation.PreviousResult == "" {
			return BuildCodexValidationPrompt(bug, bot), nil
		}
		return buildCodexDurableValidationContinuePrompt(bug, bot, continuation.UserInputs, continuation.StructuredInput, continuation.PreviousResult), nil
	case PhaseRegression:
		if attempt.Mode != AttemptRegression {
			return "", fmt.Errorf("regression phase requires regression mode")
		}
		var input RegressionValidationInput
		if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
			return "", fmt.Errorf("decode regression input: %w", err)
		}
		if err := r.validateRegressionInputBinding(context.Background(), attempt, input); err != nil {
			return "", err
		}
		return BuildRegressionValidationPrompt(bug, bot, input), nil
	case PhaseInvestigation:
		if reassessment, ok := remediationReassessmentFromInput(attempt.InputJSON); ok {
			return buildRemediationReassessmentPrompt(reassessment)
		}
		prompt := buildStructuredInvestigationPrompt(bug, bot)
		if dispute, ok := rootCauseDisputeFromInput(attempt.InputJSON); ok {
			disputePrompt, err := buildRootCauseDisputePrompt(dispute)
			if err != nil {
				return "", err
			}
			prompt += disputePrompt
		}
		if len(attempt.InputJSON) != 0 && string(attempt.InputJSON) != "{}" {
			prompt += "\n## Studio structured investigation input\n\n```json\n" + string(attempt.InputJSON) + "\n```\n"
		}
		return prompt, nil
	case PhaseFix:
		prompt := BuildCodexFixPrompt(bug, bot, InvestigationRun{}, "")
		handoff, err := r.fixPromptHandoff(context.Background(), attempt)
		if err != nil {
			return "", err
		}
		prompt += "\n## 已批准的排障交接\n\n下列根因与修复方向已通过 Studio 授权关口。先在锁定工作区核对实际代码，再做最小修复；不得忽略或自行替换该交接。\n\n```json\n" + string(handoff) + "\n```\n"
		if len(attempt.InputJSON) != 0 && string(attempt.InputJSON) != "{}" {
			prompt += "\n## 已授权结构化阶段输入\n\n```json\n" + string(attempt.InputJSON) + "\n```\n"
		}
		if rework, ok := fixReworkFromInput(attempt.InputJSON); ok {
			prompt += fmt.Sprintf(`
## 重新修复约束

这是对已推送修复 %s 的全新修复 Attempt，不是继续修改旧分支。

- 用户反馈必须作为已批准修复方案的实施约束：%s
- 旧修复结果仅供比较，不得 checkout、amend、force-push 或复用其中的修复分支。
- 必须从 Studio 提供的干净锁定基线重新实施，并为每个新修复分支使用精确后缀：%s
- 若用户反馈与已锁定根因或本次授权仓库范围冲突，输出 blocked，不得自行扩大范围。
`, rework.SourceFixAttemptID, rework.UserFeedback, rework.RequiredFixBranchSuffix)
		}
		return prompt, nil
	default:
		return "", fmt.Errorf("unsupported phase %q", attempt.Phase)
	}
}

func (r *AgentPhaseRunner) fixPromptHandoff(ctx context.Context, attempt PhaseAttempt) ([]byte, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("fix prompt requires a workflow store")
	}
	parentID := strings.TrimSpace(attempt.ParentAttemptID)
	if parentID == "" {
		return nil, errors.New("fix prompt requires an approved root-cause attempt")
	}
	parent, err := r.store.GetAttempt(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("load approved root-cause attempt: %w", err)
	}
	if parent.CaseID != attempt.CaseID || parent.CycleNumber != attempt.CycleNumber || parent.Phase != PhaseInvestigation || parent.Status != AttemptStatusSucceeded {
		return nil, errors.New("fix prompt root-cause handoff does not match the fix attempt")
	}
	result, err := ParseInvestigationResult(parent.OutputJSON)
	if err != nil {
		return nil, fmt.Errorf("parse approved root-cause handoff: %w", err)
	}
	if result.InvestigationStatus != "root_cause_ready" || !result.UsesCodeFixWorkflow() {
		return nil, errors.New("fix prompt requires an approved code root cause and remediation plan")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode approved root-cause handoff: %w", err)
	}
	return encoded, nil
}

type validationPromptContext struct {
	UserInputs      []string
	StructuredInput string
	PreviousResult  string
}

func (r *AgentPhaseRunner) validationPromptContext(ctx context.Context, attempt PhaseAttempt) (validationPromptContext, error) {
	chain := []PhaseAttempt{attempt.Clone()}
	seen := map[string]struct{}{attempt.ID: {}}
	current := attempt
	for strings.TrimSpace(current.ParentAttemptID) != "" {
		parentID := strings.TrimSpace(current.ParentAttemptID)
		if _, duplicate := seen[parentID]; duplicate {
			return validationPromptContext{}, errors.New("validation continuation chain contains a cycle")
		}
		if r == nil || r.store == nil {
			return validationPromptContext{}, errors.New("validation continuation requires a workflow store")
		}
		parent, err := r.store.GetAttempt(ctx, parentID)
		if err != nil {
			return validationPromptContext{}, fmt.Errorf("load validation continuation parent %s: %w", parentID, err)
		}
		if parent.CaseID != attempt.CaseID || parent.CycleNumber != attempt.CycleNumber || parent.Phase != PhaseValidation || parent.Mode != AttemptReproduce {
			return validationPromptContext{}, errors.New("validation continuation parent does not match the current case, cycle, phase, and mode")
		}
		seen[parentID] = struct{}{}
		chain = append(chain, parent)
		current = parent
	}

	result := validationPromptContext{}
	for index := len(chain) - 1; index >= 0; index-- {
		userInput, _, err := validationPromptInput(chain[index].InputJSON)
		if err != nil {
			return validationPromptContext{}, err
		}
		if userInput != "" {
			result.UserInputs = append(result.UserInputs, userInput)
		}
	}
	_, structuredInput, err := validationPromptInput(attempt.InputJSON)
	if err != nil {
		return validationPromptContext{}, err
	}
	result.StructuredInput = structuredInput
	if len(chain) > 1 {
		result.PreviousResult, err = formattedPromptJSON(chain[1].OutputJSON)
		if err != nil {
			return validationPromptContext{}, fmt.Errorf("format previous validation result: %w", err)
		}
	}
	return result, nil
}

func validationPromptInput(input json.RawMessage) (string, string, error) {
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", "", fmt.Errorf("decode validation input: %w", err)
	}
	userInput := ""
	if raw, exists := fields["user_input"]; exists {
		value, ok := raw.(string)
		if !ok {
			return "", "", errors.New("validation input user_input must be a string")
		}
		userInput = strings.TrimSpace(value)
	}
	delete(fields, "user_input")
	delete(fields, "mode")
	delete(fields, "target_environment")
	if len(fields) == 0 {
		return userInput, "", nil
	}
	encoded, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode structured validation input: %w", err)
	}
	return userInput, string(encoded), nil
}

func (r *AgentPhaseRunner) browserUserClarifications(ctx context.Context, attempt PhaseAttempt) ([]string, error) {
	switch attempt.Phase {
	case PhaseValidation:
		continuation, err := r.validationPromptContext(ctx, attempt)
		if err != nil {
			return nil, err
		}
		return append([]string(nil), continuation.UserInputs...), nil
	case PhaseRegression:
		var input RegressionValidationInput
		if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
			return nil, fmt.Errorf("decode regression clarification: %w", err)
		}
		if len(input.SupplementalEvidence) == 0 {
			return nil, nil
		}
		userInput, structured, err := validationPromptInput(input.SupplementalEvidence)
		if err == nil {
			result := make([]string, 0, 2)
			if userInput != "" {
				result = append(result, userInput)
			}
			if structured != "" {
				result = append(result, structured)
			}
			return result, nil
		}
		formatted, formatErr := formattedPromptJSON(input.SupplementalEvidence)
		if formatErr != nil {
			return nil, fmt.Errorf("decode regression supplemental evidence: %w", formatErr)
		}
		return []string{formatted}, nil
	default:
		return nil, nil
	}
}

func formattedPromptJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *AgentPhaseRunner) validateRegressionInputBinding(ctx context.Context, attempt PhaseAttempt, input RegressionValidationInput) error {
	if strings.TrimSpace(input.OriginalValidationAttemptID) == "" || strings.TrimSpace(input.OriginalReproduction) == "" || strings.TrimSpace(input.ExpectedBehavior) == "" || strings.TrimSpace(input.OriginalObservedBehavior) == "" || strings.TrimSpace(input.OriginalScenarioHash) == "" || input.CycleNumber < 1 || strings.TrimSpace(input.DeploymentObservationID) == "" || strings.TrimSpace(input.DeploymentReservationID) == "" || strings.TrimSpace(input.TargetEnvironment) == "" || (len(input.ExpectedFixCommits) == 0 && strings.TrimSpace(input.RemediationBindingID) == "") {
		return errors.New("regression input requires original validation, reproduction, expected behavior, scenario hash, cycle, deployment observation, expected commits, and target environment")
	}
	for repo, commit := range input.ExpectedFixCommits {
		if strings.TrimSpace(repo) == "" || strings.TrimSpace(commit) == "" {
			return errors.New("regression expected commits require non-empty repository and commit")
		}
	}
	incident, err := r.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	if attempt.CycleNumber != input.CycleNumber {
		return errors.New("regression attempt cycle does not match its deterministic input")
	}
	return (&CaseOrchestrator{store: r.store}).validatePersistedRegressionBinding(ctx, incident, input)
}

func buildStructuredInvestigationPrompt(bug Bug, bot BotRef) string {
	var sb strings.Builder
	sb.WriteString("请作为选定的 AI 排障机器人执行只读根因分析。先遵循 incident-investigator/SKILL.md 的取证流程。\n")
	sb.WriteString("本 Studio 阶段契约优先于 incident-investigator 中面向普通交互式会话的 ASK_USER / missing_critical_evidence 兼容规则；不得把部署、配置、trace、日志、指标、数据库或 K8s 取证转为用户补证。\n")
	sb.WriteString("Studio 已由验证 Agent 完成复现并冻结证据；第 1 步只是接收并校验 Studio structured investigation input 与 validation-evidence-manifest.json，不是再次复现。不得调用 bug-verifier、api-verifier、attachment-evidence-verifier，不得重新操作浏览器复现。\n")
	sb.WriteString("证据责任必须分流：先读取 manifest 全部文件；request_facts 是首次浏览器执行从实际请求中自动发现或按白名单冻结的业务参数事实，必须优先用于 trace、日志和数据库关联；response_facts 是首次执行对真实 JSON 响应生成的无原始值结构摘要（字段路径、数组长度、唯一值数量、相等字段对、计数字段与数组长度关系）；已有 response_assertions 则是业务预期字段关系的权威机器判定。response_facts 已覆盖所需字段结构或关系时，不得仅因缺少原始 response body 或预声明 response_assertions 而写 validation_gaps。不得索要或持久化原始 request body；不得索要或持久化原始 response body。只有冻结证据确实缺失、损坏，或验证宿主尚未安全生成业务判断必需且无法由 response_facts 替代的精确断言时才写入 validation_gaps，Studio 才会自动交回验证 Agent 补采。\n")
	sb.WriteString("在最终输出前，必须根据环境和服务读 routing，并对本问题因果判断真正需要的部署版本、调用链、日志、指标、配置、数据库和 K8s 调用已安装的目标环境 skill / MCP；不得为填满七步而查询与候选根因无关的系统。对数据库，单集群时直接调 `<type>-<env>`；service-to-datastore-source 空映射不代表 MCP 不存在。未真实调用工具及其只读 fallback 前，不得声称“缺少映射后的只读工具”。仅在结论仍为 insufficient_info、该范围会改变候选根因判断且工具实际失败时写 unchecked_scopes；已有等价或更强证据时不得记录重复替代工具、可选观测项或 source map。root_cause_ready 时 unchecked_scopes 必须为 []；gaps 只允许记录必须由用户提供的权限、登录态、测试账号或外部资料。\n")
	sb.WriteString("最终 YAML 必须显式输出 validation_gaps、gaps、unchecked_scopes 三个数组，无内容时也必须写 []。任何要求用户提供 deployment revision/image digest/rollout、trace/日志/指标、配置、数据库查询结果、K8s 状态或原始 response body 的 gaps 都是无效阶段结果。\n")
	sb.WriteString("call_chain 精度必须与证据字段一致：source_mapped 必须同时提供 repo、实际部署 revision、file、正数 line 和 evidence；deployed_revision 必须提供 repo、实际部署 revision 和 evidence；runtime_verified 与 static_candidate 必须提供 evidence。缺少任一必填字段时必须主动降级到字段能够证明的更弱精度，绝不能在 revision 为空时输出 source_mapped。\n")
	sb.WriteString("call_chain 定位精度与根因就绪度必须分开判断。缺少部署 revision、image digest 或匹配 source map 本身只会让对应 hop 降级为 static_candidate/unavailable；当冻结运行时证据与代码逻辑已独立闭合因果链时，不得仅因此降低 confidence、输出 insufficient_info 或写入 validation_gaps/gaps/unchecked_scopes，必须输出 root_cause_ready、confidence: high 和 unchecked_scopes: []。只有该缺失确实会改变候选根因判断时，才保留 insufficient_info 并在 unchecked_scopes 说明。\n")
	sb.WriteString("remediation.mode 为 code_change 时，repositories 必须只列出修复建议实际要求修改的代码仓库，不能把仅参与调用链、仅用于定位或只需回归验证的仓库列入；非代码处置时 repositories 必须为 []。\n")
	sb.WriteString("只有 confidence: high 且 validation_gaps: [] 且 gaps: [] 时才能输出 investigation_status: root_cause_ready；confidence 为 medium/low、validation_gaps 非空或 gaps 非空时必须输出 investigation_status: insufficient_info。\n")
	sb.WriteString("执行每一步前，必须单独发送且原样发送对应进度标记（标记不是最终结果）：\n")
	for index, step := range investigationPhaseSteps {
		fmt.Fprintf(&sb, "[[TSHOOT_STEP phase=investigation index=%d key=%s]]\n", index+1, step.Key)
	}
	sb.WriteString(GenerateContext(bug, bot))
	sb.WriteString("\n最终只输出严格 YAML，不得添加字段或解释性段落：\n")
	sb.WriteString("investigation_status: root_cause_ready | insufficient_info\nenvironment: <env>\nroot_cause: <直接和深层根因；信息不足时为空>\nconfidence: high | medium | low\nroot_cause_type: code | data | configuration | infrastructure | network | external_dependency | transient\nremediation:\n  mode: code_change | operator_action | external_recovery | observe_only\n  repositories: [] # code_change 时列出实际需要修改的仓库；其它模式为空\n  target: <需要改动或等待恢复的具体对象>\n  summary: <最小处置建议；排障阶段不得执行写操作>\n  rollback: <operator_action 必填；其它模式可空>\n  verification: <处置后如何用原场景回归>\ncall_chain:\n  - kind: <browser|frontend|gateway|service|queue|datastore|external>\n    name: <节点名称>\n    service: <可空>\n    repo: <可空>\n    revision: <可空；必须是实际部署版本>\n    protocol: <可空>\n    operation: <可空；HTTP method/path、RPC method、topic/queue 等>\n    file: <可空；仓库相对路径>\n    line: 0 # 未知时为 0\n    precision: runtime_verified | source_mapped | deployed_revision | static_candidate | unavailable\n    evidence: <可空；支持该跳的证据摘要>\n    request_id: <可空>\n    trace_id: <可空>\nevidence:\n  - kind: <trace|log|metric|code|config|data|command>\n    path: <Studio staging 目录内的相对路径>\n    captured_at: <RFC3339；仅兼容输出，Studio 以 fstat 为准>\n    environment: <env>\n    version: <可空>\n    request_id: <可空>\n    trace_id: <可空>\n    redaction_status: redacted | not_required # Studio 总会重新扫描\nvalidation_gaps: [] # 仅验证 Agent 应自动补采的冻结证据缺口\ngaps: [] # 仅必须由用户补充的阻塞项\nunchecked_scopes: [] # 非阻塞且未覆盖的范围\n")
	return sb.String()
}

func investigationInputRequiresDatastoreRead(input json.RawMessage) bool {
	var envelope struct {
		ValidationEvidence []InvestigationEvidenceReference `json:"validation_evidence"`
		RegressionEvidence []InvestigationEvidenceReference `json:"regression_evidence_refs"`
	}
	if len(input) == 0 || json.Unmarshal(input, &envelope) != nil {
		return false
	}
	for _, reference := range append(envelope.ValidationEvidence, envelope.RegressionEvidence...) {
		if reference.Kind == "request_facts" {
			return true
		}
	}
	return false
}

func eventProvesDatastoreRead(event InvestigationEvent) bool {
	if event.Type != "mcp_tool_call" && event.Type != "command_execution" {
		return false
	}
	encoded, _ := json.Marshal(event.Raw)
	text := strings.ToLower(event.Message + " " + string(encoded))
	for _, marker := range []string{"mongodb", "mongosh", "pymongo", "mysql", "postgres", "doris", "redis", "elasticsearch", "clickhouse"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (r *AgentPhaseRunner) registerArtifacts(ctx context.Context, attempt PhaseAttempt, staging attemptEvidenceStaging, references []ArtifactReference) error {
	registered, err := r.store.ListEvidenceArtifacts(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	priorDigests := make([]string, 0, len(registered))
	if attempt.Phase == PhaseRegression {
		for _, artifact := range registered {
			if artifact.AttemptID != attempt.ID {
				priorDigests = append(priorDigests, artifact.SHA256)
			}
		}
	}
	for _, reference := range references {
		if strings.TrimSpace(reference.Path) == "" || strings.TrimSpace(reference.Kind) == "" || strings.TrimSpace(reference.Environment) == "" {
			return errors.New("artifact relative path, kind, and environment are required")
		}
		captured, err := staging.Capture(reference.Path)
		if err != nil {
			return err
		}
		if _, err := registerCapturedArtifact(ctx, r.store, ArtifactInput{ArtifactsRoot: r.artifactsRoot, SourcePath: filepath.Join(staging.Path(), reference.Path), CaseID: attempt.CaseID, AttemptID: attempt.ID, Kind: reference.Kind, CapturedAt: captured.CapturedAt, Environment: reference.Environment, Version: reference.Version, RequestID: reference.RequestID, TraceID: reference.TraceID, RedactionStatus: RedactionStatusNotRequired, RejectSHA256: priorDigests, RejectSensitive: true}, captured); err != nil {
			return err
		}
	}
	return nil
}

func (r *AgentPhaseRunner) freezeBrowserArtifacts(ctx context.Context, attempt PhaseAttempt, staging attemptEvidenceStaging, references []BrowserArtifactReference) ([]browserFrozenArtifact, error) {
	frozen := make([]browserFrozenArtifact, 0, len(references))
	for _, reference := range references {
		if strings.TrimSpace(reference.Path) == "" || strings.TrimSpace(reference.Kind) == "" || strings.TrimSpace(reference.Environment) == "" || len(reference.SHA256) != sha256.Size*2 || reference.SHA256 != strings.ToLower(reference.SHA256) || reference.Size < 0 {
			return nil, errors.New("verified browser artifact metadata is invalid")
		}
		if _, err := hex.DecodeString(reference.SHA256); err != nil {
			return nil, errors.New("verified browser artifact digest is invalid")
		}
		captured, err := staging.Capture(reference.Path)
		if err != nil {
			return nil, err
		}
		if captured.SHA256 != reference.SHA256 || int64(len(captured.Content)) != reference.Size {
			return nil, errors.New("verified browser artifact changed before registration")
		}
		// These references come from the HostVerifier's current browser execution
		// and are bound to this attempt's private staging directory. Deterministic
		// pages may legitimately produce byte-identical screenshots or facts on a
		// fresh replay, so freshness is established by this execution boundary and
		// the verified digest/size above rather than by requiring changed content.
		// Agent-reported artifacts still use registerArtifacts, which rejects
		// digests registered by earlier attempts.
		artifact, err := registerCapturedArtifact(ctx, r.store, ArtifactInput{ArtifactsRoot: r.artifactsRoot, SourcePath: filepath.Join(staging.Path(), reference.Path), CaseID: attempt.CaseID, AttemptID: attempt.ID, Kind: reference.Kind, CapturedAt: captured.CapturedAt, Environment: reference.Environment, Version: reference.Version, RequestID: reference.RequestID, TraceID: reference.TraceID, RedactionStatus: RedactionStatusNotRequired, RejectSensitive: true}, captured)
		if err != nil {
			return nil, err
		}
		published, err := captureRegisteredArtifact(artifact.PathOrReference, r.artifactsRoot, attempt.CaseID, reference.SHA256)
		if err != nil || published.SHA256 != reference.SHA256 || int64(len(published.Content)) != reference.Size {
			return nil, errors.New("registered browser artifact could not be verified")
		}
		frozen = append(frozen, browserFrozenArtifact{
			ReferencePath: reference.Path, Kind: reference.Kind, SHA256: reference.SHA256, Size: reference.Size,
			PathOrReference: artifact.PathOrReference, Content: append([]byte(nil), published.Content...),
		})
	}
	return frozen, nil
}

func (r *AgentPhaseRunner) validateRegressionEvidence(_ context.Context, attempt PhaseAttempt, result PhaseResult) error {
	if attempt.Phase != PhaseRegression || (result.Outcome != PhaseOutcomeFixedVerified && result.Outcome != PhaseOutcomeStillReproduces) {
		return nil
	}
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		return err
	}
	var validation ValidationResult
	if err := json.Unmarshal(result.OutputJSON, &validation); err != nil {
		return err
	}
	if validation.Environment != input.TargetEnvironment {
		return errors.New("regression result environment does not match the target environment")
	}
	if input.OriginalScenarioHash == "" || validation.ScenarioHash != input.OriginalScenarioHash {
		return errors.New("regression result must preserve the original scenario hash")
	}
	if len(result.ArtifactInputs) == 0 {
		return errors.New("regression result requires fresh evidence from the current attempt")
	}
	hasCorrelation := false
	for _, artifact := range result.ArtifactInputs {
		if artifact.Environment != input.TargetEnvironment {
			return errors.New("regression evidence environment does not match the target environment")
		}
		if input.ObservedDeploymentVersion != "" && artifact.Version != input.ObservedDeploymentVersion {
			return errors.New("regression evidence version does not match the observed deployment version")
		}
		hasCorrelation = hasCorrelation || strings.TrimSpace(artifact.RequestID) != "" || strings.TrimSpace(artifact.TraceID) != ""
	}
	if !hasCorrelation {
		return errors.New("regression evidence requires a fresh request_id or trace_id")
	}
	return nil
}

func (r *AgentPhaseRunner) validateRegisteredRegressionEvidence(ctx context.Context, attempt PhaseAttempt, result PhaseResult) error {
	if attempt.Phase != PhaseRegression || (result.Outcome != PhaseOutcomeFixedVerified && result.Outcome != PhaseOutcomeStillReproduces) {
		return nil
	}
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		return err
	}
	artifacts, err := (&CaseOrchestrator{store: r.store}).currentRegressionArtifacts(ctx, attempt, input)
	if err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return ErrRegressionFreshEvidence
	}
	return nil
}

func (r *AgentPhaseRunner) startLegacyProjection(attempt PhaseAttempt, bug Bug, bot BotRef) {
	if r.legacy == nil {
		return
	}
	preview := fmt.Sprintf("durable workflow phase: phase=%s mode=%s", attempt.Phase, attempt.Mode)
	_ = r.legacy.ProjectAttempt(InvestigationRun{ID: attempt.ID, BugID: bug.ID, BotKey: bot.Key, Status: InvestigationRunning, StartedAt: attempt.StartedAt, PromptPreview: preview, ContinuationOf: attempt.ParentAttemptID})
}

func (r *AgentPhaseRunner) projectEvent(attempt PhaseAttempt, event InvestigationEvent) {
	raw, rawErr := json.Marshal(event.Raw)
	meta, metaErr := json.Marshal(event.Meta)
	if containsSensitiveData([]byte(event.Message)) || rawErr != nil || containsSensitiveData(raw) || metaErr != nil || containsSensitiveData(meta) {
		event.Message = "[sensitive phase event suppressed]"
		event.Raw = nil
		event.Meta = nil
	}
	if event.Meta == nil {
		event.Meta = make(map[string]any)
	}
	event.Meta["case_id"] = attempt.CaseID
	event.Meta["attempt_id"] = attempt.ID
	event.Meta["cycle_number"] = attempt.CycleNumber
	event.Meta["phase"] = string(attempt.Phase)
	r.mu.Lock()
	sink := r.eventSink
	r.mu.Unlock()
	run := InvestigationRun{ID: attempt.ID, BotKey: attempt.BotKey, Status: InvestigationRunning}
	if r.legacy != nil {
		_ = r.legacy.ProjectEvent(attempt.ID, event)
		if projected, err := r.legacy.Get(attempt.ID); err == nil {
			run = projected
		}
	}
	if sink != nil {
		sink(run, event)
	}
}

func (r *AgentPhaseRunner) finishLegacy(id string, status InvestigationStatus, final, message string) {
	if r.legacy != nil {
		_ = r.legacy.FinishProjection(id, status, final, message)
	}
}

func failureOutcome(phase Phase) PhaseOutcome {
	if phase == PhaseFix {
		return PhaseOutcomeFixFailed
	}
	return PhaseOutcomeNeedsEvidence
}

func browserFailureOutcome(phase Phase, code string) PhaseOutcome {
	if phase == PhaseFix || !strings.HasPrefix(code, "browser_") {
		return failureOutcome(phase)
	}
	if browserBusinessEvidenceFailure(code) {
		return PhaseOutcomeNeedsEvidence
	}
	return PhaseOutcomeSystemFailed
}

func artifactErrorCode(err error) string {
	if errors.Is(err, ErrSecureArtifactStoreUnsupported) {
		return "secure_artifact_store_unsupported"
	}
	if errors.Is(err, ErrEvidenceArtifactReused) {
		return "evidence_artifact_reused"
	}
	return "artifact_registration_failed"
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

type RegressionValidationInput struct {
	OriginalValidationAttemptID string            `json:"original_validation_attempt_id"`
	OriginalReproduction        string            `json:"original_reproduction"`
	ExpectedBehavior            string            `json:"expected_behavior"`
	OriginalObservedBehavior    string            `json:"original_observed_behavior"`
	OriginalEvidenceReferences  []string          `json:"original_evidence_refs"`
	OriginalScenarioHash        string            `json:"scenario_hash"`
	CycleNumber                 int               `json:"cycle_number"`
	ExpectedFixCommits          map[string]string `json:"expected_fix_commits"`
	RemediationBindingID        string            `json:"remediation_binding_id,omitempty"`
	RemediationType             RootCauseType     `json:"remediation_type,omitempty"`
	RemediationSummary          string            `json:"remediation_summary,omitempty"`
	DeploymentObservationID     string            `json:"deployment_observation_id"`
	DeploymentReservationID     string            `json:"deployment_reservation_id"`
	ObservedDeploymentVersion   string            `json:"observed_deployment_version"`
	TargetEnvironment           string            `json:"target_environment"`
	SupplementalEvidence        json.RawMessage   `json:"supplemental_evidence,omitempty"`
}

func ParseValidationResult(data []byte) (ValidationResult, error) {
	result, err := decodeValidationResultStrict(data)
	if err != nil {
		return ValidationResult{}, err
	}
	if err := validateValidationResult(result); err != nil {
		return ValidationResult{}, err
	}
	return result, nil
}

func decodeValidationResultStrict(data []byte) (ValidationResult, error) {
	var result ValidationResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return ValidationResult{}, fmt.Errorf("parse validation result: %w", err)
	}
	return result, nil
}

func validateValidationResult(result ValidationResult) error {
	status, err := validationCaseStatus(result.VerificationStatus)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Environment) == "" {
		return errors.New("validation environment is required")
	}
	if status != CaseWaitingEvidence && len(result.Gaps) != 0 {
		return errors.New("terminal validation result must not contain blocking gaps")
	}
	if status == CaseReproduced {
		if strings.TrimSpace(result.ObservedBehavior) == "" || strings.TrimSpace(result.ExpectedBehavior) == "" {
			return errors.New("reproduced validation requires observed_behavior and expected_behavior")
		}
		if len(result.Evidence) == 0 {
			return errors.New("reproduced validation requires at least one evidence artifact")
		}
	}
	return nil
}

func ParseInvestigationResult(data []byte) (InvestigationResult, error) {
	var result InvestigationResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return InvestigationResult{}, fmt.Errorf("parse investigation result: %w", err)
	}
	switch result.InvestigationStatus {
	case "root_cause_ready":
		if strings.TrimSpace(result.RootCause) == "" {
			return InvestigationResult{}, errors.New("root_cause_ready requires root_cause")
		}
	case "insufficient_info":
	default:
		return InvestigationResult{}, fmt.Errorf("unsupported investigation status %q", result.InvestigationStatus)
	}
	if strings.TrimSpace(result.Environment) == "" {
		return InvestigationResult{}, errors.New("investigation environment is required")
	}
	if result.Confidence != "high" && result.Confidence != "medium" && result.Confidence != "low" {
		return InvestigationResult{}, fmt.Errorf("unsupported investigation confidence %q", result.Confidence)
	}
	if normalizeInvestigationCallChainPrecision(result.CallChain) {
		result.UncheckedScopes = appendUniqueString(result.UncheckedScopes, "Studio conservatively downgraded incomplete call-chain precision because required deployment or source evidence was unavailable.")
	}
	if result.InvestigationStatus == "root_cause_ready" {
		if result.Confidence != "high" || len(result.ValidationGaps) != 0 || len(result.Gaps) != 0 {
			// A tentative root cause is still useful evidence, but it must not cross
			// the remediation gate until confidence is high and every blocking gap
			// is closed. Normalize a premature terminal declaration into the
			// recoverable evidence path instead of discarding its artifacts.
			result.InvestigationStatus = "insufficient_info"
			if len(result.ValidationGaps) == 0 && len(result.Gaps) == 0 {
				result.Gaps = []string{fmt.Sprintf("root cause confidence is %s; additional evidence is required", result.Confidence)}
			}
		} else {
			// Results produced before remediation routing existed were necessarily
			// code-fix results. Preserve those already-durable Cases while requiring
			// every new prompt to emit the explicit fields below.
			if result.RootCauseType == "" && result.Remediation.Mode == "" {
				result.RootCauseType = RootCauseCode
				result.Remediation = RemediationPlan{Mode: RemediationCodeChange, Target: "legacy investigation result", Summary: result.RootCause, Verification: "run regression validation"}
			}
			if err := validateRemediationPlan(result.RootCauseType, result.Remediation); err != nil {
				return InvestigationResult{}, err
			}
			// A high-confidence terminal result is already complete. Optional or
			// redundant tool failures do not belong in its durable phase summary;
			// they remain observable in the attempt event stream and evidence.
			result.UncheckedScopes = nil
		}
	}
	if len(result.CallChain) > 64 {
		return InvestigationResult{}, errors.New("investigation call_chain exceeds 64 hops")
	}
	for index, hop := range result.CallChain {
		if strings.TrimSpace(hop.Kind) == "" || strings.TrimSpace(hop.Name) == "" {
			return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] requires kind and name", index)
		}
		switch hop.Kind {
		case "browser", "frontend", "gateway", "service", "queue", "datastore", "external":
		default:
			return InvestigationResult{}, fmt.Errorf("unsupported investigation call_chain[%d] kind %q", index, hop.Kind)
		}
		switch hop.Precision {
		case "runtime_verified", "source_mapped", "deployed_revision", "static_candidate", "unavailable":
		default:
			return InvestigationResult{}, fmt.Errorf("unsupported investigation call_chain[%d] precision %q", index, hop.Precision)
		}
		if hop.Line < 0 {
			return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] line must not be negative", index)
		}
		if hop.Precision == "source_mapped" && (strings.TrimSpace(hop.Repo) == "" || strings.TrimSpace(hop.Revision) == "" || strings.TrimSpace(hop.File) == "" || hop.Line == 0 || strings.TrimSpace(hop.Evidence) == "") {
			return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] source_mapped precision requires repo, deployed revision, file, line, and evidence", index)
		}
		if hop.Precision == "deployed_revision" && (strings.TrimSpace(hop.Repo) == "" || strings.TrimSpace(hop.Revision) == "" || strings.TrimSpace(hop.Evidence) == "") {
			return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] deployed_revision precision requires repo, revision, and evidence", index)
		}
		if (hop.Precision == "runtime_verified" || hop.Precision == "static_candidate") && strings.TrimSpace(hop.Evidence) == "" {
			return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] %s precision requires evidence", index, hop.Precision)
		}
		for _, value := range []string{hop.Kind, hop.Name, hop.Service, hop.Repo, hop.Revision, hop.Protocol, hop.Operation, hop.File, hop.Evidence, hop.RequestID, hop.TraceID} {
			if len(value) > 1000 {
				return InvestigationResult{}, fmt.Errorf("investigation call_chain[%d] contains an oversized field", index)
			}
		}
	}
	return result, nil
}

// normalizeInvestigationCallChainPrecision preserves useful investigation
// output without accepting a stronger location claim than its fields prove.
// Missing evidence always moves precision down; it can never be inferred or
// filled from a repository's current HEAD.
func normalizeInvestigationCallChainPrecision(callChain []CallChainHop) bool {
	downgraded := false
	for index := range callChain {
		hop := &callChain[index]
		switch hop.Precision {
		case "source_mapped":
			if strings.TrimSpace(hop.Repo) != "" && strings.TrimSpace(hop.Revision) != "" && strings.TrimSpace(hop.File) != "" && hop.Line > 0 && strings.TrimSpace(hop.Evidence) != "" {
				continue
			}
			if strings.TrimSpace(hop.Repo) != "" && strings.TrimSpace(hop.Revision) != "" && strings.TrimSpace(hop.Evidence) != "" {
				hop.Precision = "deployed_revision"
			} else if strings.TrimSpace(hop.Evidence) != "" {
				hop.Precision = "static_candidate"
			} else {
				hop.Precision = "unavailable"
			}
			downgraded = true
		case "deployed_revision":
			if strings.TrimSpace(hop.Repo) != "" && strings.TrimSpace(hop.Revision) != "" && strings.TrimSpace(hop.Evidence) != "" {
				continue
			}
			if strings.TrimSpace(hop.Evidence) != "" {
				hop.Precision = "static_candidate"
			} else {
				hop.Precision = "unavailable"
			}
			downgraded = true
		case "runtime_verified", "static_candidate":
			if strings.TrimSpace(hop.Evidence) == "" {
				hop.Precision = "unavailable"
				downgraded = true
			}
		}
	}
	return downgraded
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// SafeLegacyInvestigationProjection recovers the useful, structured portion of
// a runs.json result that phase validation rejected. It is display-only:
// callers must never use this projection to change durable Case or attempt
// state.
func SafeLegacyInvestigationProjection(data []byte) (json.RawMessage, bool) {
	result, err := ParseInvestigationResult(data)
	if err != nil || result.InvestigationStatus != "insufficient_info" {
		return nil, false
	}
	encoded, err := json.Marshal(result)
	if err != nil || containsSensitiveData(encoded) {
		return nil, false
	}
	return encoded, true
}

func validateRemediationPlan(rootCauseType RootCauseType, plan RemediationPlan) error {
	switch rootCauseType {
	case RootCauseCode:
		if plan.Mode != RemediationCodeChange {
			return errors.New("code root cause requires code_change remediation")
		}
	case RootCauseData, RootCauseConfiguration, RootCauseInfrastructure, RootCauseNetwork:
		if plan.Mode != RemediationOperatorAction {
			return fmt.Errorf("%s root cause requires operator_action remediation", rootCauseType)
		}
	case RootCauseExternalDependency:
		if plan.Mode != RemediationExternalRecovery {
			return errors.New("external_dependency root cause requires external_recovery remediation")
		}
	case RootCauseTransient:
		if plan.Mode != RemediationObserveOnly {
			return errors.New("transient root cause requires observe_only remediation")
		}
	default:
		return fmt.Errorf("unsupported root_cause_type %q", rootCauseType)
	}
	for name, value := range map[string]string{"target": plan.Target, "summary": plan.Summary, "verification": plan.Verification} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("remediation %s is required", name)
		}
		if len(value) > 2000 {
			return fmt.Errorf("remediation %s is too large", name)
		}
	}
	if plan.Mode == RemediationOperatorAction && strings.TrimSpace(plan.Rollback) == "" {
		return errors.New("operator_action remediation requires rollback")
	}
	if len(plan.Rollback) > 2000 {
		return errors.New("remediation rollback is too large")
	}
	seenRepositories := make(map[string]struct{}, len(plan.Repositories))
	for _, repo := range plan.Repositories {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return errors.New("remediation repositories require non-empty names")
		}
		if _, exists := seenRepositories[repo]; exists {
			return fmt.Errorf("remediation repositories contains duplicate %q", repo)
		}
		seenRepositories[repo] = struct{}{}
	}
	if len(plan.Repositories) > 16 {
		return errors.New("remediation repositories exceeds 16 entries")
	}
	if plan.Mode != RemediationCodeChange && len(plan.Repositories) != 0 {
		return errors.New("non-code remediation must not declare repositories")
	}
	return nil
}

func ParsePhaseResult(attempt PhaseAttempt, data []byte) (PhaseResult, error) {
	switch attempt.Phase {
	case PhaseValidation, PhaseRegression:
		validation, err := ParseValidationResult(data)
		if err != nil {
			return PhaseResult{}, err
		}
		status := validation.CaseStatus()
		if attempt.Phase == PhaseValidation && attempt.Mode == AttemptReproduce {
			if status != CaseReproduced && status != CaseNotReproduced && status != CaseWaitingEvidence {
				return PhaseResult{}, fmt.Errorf("validation reproduce mode cannot return %q", validation.VerificationStatus)
			}
		} else if attempt.Phase == PhaseRegression && attempt.Mode == AttemptRegression {
			if status != CaseFixedVerified && status != CaseStillReproduces && status != CaseWaitingEvidence {
				return PhaseResult{}, fmt.Errorf("validation regression mode cannot return %q", validation.VerificationStatus)
			}
		} else {
			return PhaseResult{}, fmt.Errorf("phase %q and mode %q are incompatible", attempt.Phase, attempt.Mode)
		}
		outcome := PhaseOutcomeNeedsEvidence
		switch status {
		case CaseReproduced:
			outcome = PhaseOutcomeReproduced
		case CaseNotReproduced:
			outcome = PhaseOutcomeNotReproduced
		case CaseFixedVerified:
			outcome = PhaseOutcomeFixedVerified
		case CaseStillReproduces:
			outcome = PhaseOutcomeStillReproduces
		}
		encoded, _ := json.Marshal(validation)
		return PhaseResult{Outcome: outcome, OutputJSON: encoded, ArtifactInputs: validation.Evidence}, nil
	case PhaseInvestigation:
		if attempt.Mode != "" {
			return PhaseResult{}, fmt.Errorf("investigation does not accept mode %q", attempt.Mode)
		}
		if isRemediationReassessmentAttempt(attempt) {
			return parseRemediationReassessmentResult(attempt, data)
		}
		result, err := ParseInvestigationResult(data)
		if err != nil {
			return PhaseResult{}, err
		}
		outcome := PhaseOutcomeRootCauseReady
		if result.InvestigationStatus == "insufficient_info" {
			if len(result.ValidationGaps) != 0 && len(result.Gaps) == 0 {
				outcome = PhaseOutcomeValidationEvidenceRequired
			} else {
				outcome = PhaseOutcomeNeedsEvidence
			}
		}
		encoded, _ := json.Marshal(result)
		return PhaseResult{Outcome: outcome, OutputJSON: encoded, ArtifactInputs: result.Evidence}, nil
	case PhaseFix:
		if attempt.Mode != "" {
			return PhaseResult{}, fmt.Errorf("fix does not accept mode %q", attempt.Mode)
		}
		result, err := ParseFixResult(data)
		if err != nil {
			return PhaseResult{}, err
		}
		if err := validateFixReworkResult(attempt.InputJSON, result); err != nil {
			return PhaseResult{}, err
		}
		outcome := PhaseOutcomeFixFailed
		var changes []CodeChange
		if result.FixStatus == "fixed_pushed" {
			outcome = PhaseOutcomeFixPushed
			for index, branch := range result.Branches {
				repositoryTests := make([]FixTestResult, 0, len(result.Tests))
				for _, test := range result.Tests {
					if test.Repo == branch.Repo {
						repositoryTests = append(repositoryTests, test)
					}
				}
				testEvidence, _ := json.Marshal(repositoryTests)
				changes = append(changes, CodeChange{ID: stableID("change", fmt.Sprintf("%s:%s:%d", attempt.ID, branch.Repo, index)), CaseID: attempt.CaseID, AttemptID: attempt.ID, Repo: branch.Repo, BaseBranch: branch.BaseBranch, FixBranch: branch.FixBranch, FixCommit: branch.Commit, TestEvidence: testEvidence, TargetEnvironmentBranch: branch.TargetEnvironmentBranch, PushRemote: branch.PushRemote, PushStatus: "pushed"})
			}
		}
		encoded, _ := json.Marshal(result)
		return PhaseResult{Outcome: outcome, OutputJSON: encoded, CodeChanges: changes, ArtifactInputs: result.Evidence}, nil
	default:
		return PhaseResult{}, fmt.Errorf("unsupported executable phase %q", attempt.Phase)
	}
}

func BuildRegressionValidationPrompt(bug Bug, bot BotRef, input RegressionValidationInput) string {
	var sb strings.Builder
	sb.WriteString("你是 Bug 验证 Agent，当前 mode=regression。只复查原始场景，不得读取业务源码，不得分析根因，也不得提出修复建议。\n")
	sb.WriteString("必须在目标环境重新执行相同场景，并采集 fresh evidence；旧证据只用于对照，不能作为本次结论。\n")
	observedVersion := strings.TrimSpace(input.ObservedDeploymentVersion)
	if observedVersion == "" {
		observedVersion = "<未采集；不要猜测，回归证据 version 可留空>"
	}
	fmt.Fprintf(&sb, "original_validation_attempt_id: %s\noriginal_reproduction: %s\nexpected_behavior: %s\noriginal_observed_behavior: %s\nscenario_hash: %s\ncycle_number: %d\ntarget_environment: %s\ndeployment_observation_id: %s\ndeployment_reservation_id: %s\nobserved_deployment_version: %s\n", input.OriginalValidationAttemptID, input.OriginalReproduction, input.ExpectedBehavior, input.OriginalObservedBehavior, input.OriginalScenarioHash, input.CycleNumber, input.TargetEnvironment, input.DeploymentObservationID, input.DeploymentReservationID, observedVersion)
	if input.RemediationBindingID != "" {
		fmt.Fprintf(&sb, "remediation_binding_id: %s\nremediation_type: %s\nremediation_summary: %s\n", input.RemediationBindingID, input.RemediationType, input.RemediationSummary)
	}
	sb.WriteString("expected_fix_commits:\n")
	keys := make([]string, 0, len(input.ExpectedFixCommits))
	for key := range input.ExpectedFixCommits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&sb, "  %s: %s\n", key, input.ExpectedFixCommits[key])
	}
	sb.WriteString("original_evidence_refs:\n")
	for _, reference := range input.OriginalEvidenceReferences {
		fmt.Fprintf(&sb, "  - %s\n", reference)
	}
	if len(input.SupplementalEvidence) != 0 {
		fmt.Fprintf(&sb, "supplemental_evidence: %s\n", input.SupplementalEvidence)
	}
	sb.WriteString("每条新证据必须包含本次 attempt 的 artifact path，并包含新的 request_id 或 trace_id；Studio 以安全 fstat 时间校验 captured_at 晚于 attempt 开始时间。\n")
	sb.WriteString(GenerateContext(bug, bot))
	sb.WriteString(validationOutputContract())
	return sb.String()
}

func validationCaseStatus(status string) (CaseStatus, error) {
	switch status {
	case "reproduced":
		return CaseReproduced, nil
	case "not_reproduced":
		return CaseNotReproduced, nil
	case "insufficient_info":
		return CaseWaitingEvidence, nil
	case "fixed_verified":
		return CaseFixedVerified, nil
	case "still_reproduces":
		return CaseStillReproduces, nil
	default:
		return "", fmt.Errorf("unsupported verification status %q", status)
	}
}

func decodeStrictYAML(data []byte, target any) error {
	data = bytes.TrimSpace(data)
	if bytes.HasPrefix(data, []byte("```yaml")) && bytes.HasSuffix(data, []byte("```")) {
		data = bytes.TrimSpace(data[len("```yaml") : len(data)-len("```")])
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple YAML documents are not allowed")
		}
		return err
	}
	return nil
}
