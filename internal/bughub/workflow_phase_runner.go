package bughub

import (
	"bytes"
	"context"
	"crypto/rand"
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
	Evidence            []ArtifactReference `yaml:"evidence" json:"evidence"`
	Gaps                []string            `yaml:"gaps" json:"gaps"`
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

type PhaseAgentExecutor interface {
	ExecutePhase(context.Context, string, BotRef, string, func(InvestigationEvent)) (PhaseExecutionResult, error)
	CancelPhase(context.Context, string) error
}

type PhaseCompletionFunc func(context.Context, CompleteAttemptCommand) error

type AgentPhaseRunner struct {
	store                       *CaseStore
	executor                    PhaseAgentExecutor
	legacy                      *InvestigationStore
	artifactsRoot               string
	complete                    PhaseCompletionFunc
	eventSink                   InvestigationEventSink
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
	return &AgentPhaseRunner{store: store, executor: executor, legacy: legacy, artifactsRoot: artifactsRoot, complete: complete, openStaging: openAttemptEvidenceStaging, completionReconcileAttempts: 6, completionReconcileDelay: 2 * time.Second, active: make(map[string]context.CancelFunc), scheduled: make(map[string]struct{})}
}

func (r *AgentPhaseRunner) SetCompletionCallback(complete PhaseCompletionFunc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.complete = complete
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
	prompt, err := r.promptForAttempt(attempt, bug, bot)
	if err != nil {
		return fail(err)
	}
	openStaging := r.openStaging
	if openStaging == nil {
		openStaging = openAttemptEvidenceStaging
	}
	staging, err := openStaging(r.artifactsRoot, attempt.ID)
	if err != nil {
		return fail(fmt.Errorf("create Studio evidence staging: %w", err))
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
	prompt += "\n## Studio evidence staging (mandatory)\n\nSTUDIO_EVIDENCE_STAGING_DIR=" + staging.Path() + "\nWrite every evidence file beneath this exact directory. In final YAML, `path` must be a clean relative path from this directory; absolute paths and `..` are rejected. Studio derives timestamps and redaction status from securely opened file bytes.\n"
	if attempt.Phase == PhaseFix {
		prompt += "\n## Durable fix checkpoint (mandatory)\n\nBefore the first repository push, atomically write `" + fixCheckpointManifestName + "` in the Studio staging directory (write a temporary sibling, fsync, then rename) with state=`prepared`; include every planned repository commit/branch/remote/test. After all pushes succeed, atomically replace it with the same manifest and state=`pushed` before reporting completion. JSON fields: kind=`" + fixCheckpointManifestKind + "`, version=1, case_id=`" + attempt.CaseID + "`, attempt_id=`" + attempt.ID + "`, state=`prepared|pushed`, result=<the exact structured FixResult also returned as final YAML>. Never include credentials. Recovery treats the SSH remote branch as truth, so a crash after push but before the state update remains recoverable while a pre-push crash cannot be misreported.\n"
	}
	r.startLegacyProjection(attempt, bug, bot)
	go r.run(runCtx, attempt.Clone(), bug, bot, prompt, staging, incident.Version, claimToken, complete)
	return nil
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

func (r *AgentPhaseRunner) run(ctx context.Context, attempt PhaseAttempt, _ Bug, bot BotRef, prompt string, staging attemptEvidenceStaging, expectedVersion int64, claimToken string, complete PhaseCompletionFunc) {
	started := time.Now()
	cleaned := false
	releaseClaim := func() {
		durable, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.store.ReleaseAttemptRunClaim(durable, attempt.ID, attempt.CaseID, claimToken)
	}
	defer func() {
		if !cleaned {
			_ = staging.Cleanup()
		}
		_ = staging.Close()
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
		r.projectEvent(attempt, event)
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
	result, runErr := r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
	if runErr != nil && attempt.Phase != PhaseFix && ctx.Err() == nil {
		r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "read-only phase process retry"})
		firstUsage := result.Usage
		result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
		result.Usage.InputTokens += firstUsage.InputTokens
		result.Usage.OutputTokens += firstUsage.OutputTokens
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
	if runErr != nil {
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
			} else if err := r.registerArtifacts(ctx, attempt, staging, parsed.ArtifactInputs); err != nil {
				command.Outcome = failureOutcome(attempt.Phase)
				command.OutputJSON = mustJSON(map[string]any{"error_code": artifactErrorCode(err), "error_message": err.Error(), "evidence_limitation": true})
				command.ErrorCode = artifactErrorCode(err)
				command.ErrorMessage = err.Error()
			} else if err := r.validateRegisteredRegressionEvidence(ctx, attempt, parsed); err != nil {
				command.Outcome = PhaseOutcomeNeedsEvidence
				command.OutputJSON = mustJSON(map[string]any{"error_code": "regression_evidence_invalid", "error_message": err.Error(), "evidence_limitation": true})
				command.ErrorCode = "regression_evidence_invalid"
				command.ErrorMessage = err.Error()
			}
		}
	}
	if attempt.Phase != PhaseFix {
		cleanupErr := staging.Cleanup()
		if cleanupErr != nil {
			command.Outcome = failureOutcome(attempt.Phase)
			command.OutputJSON = mustJSON(map[string]any{"error_code": "evidence_staging_cleanup_failed", "error_message": cleanupErr.Error(), "evidence_limitation": true})
			command.ErrorCode = "evidence_staging_cleanup_failed"
			command.ErrorMessage = cleanupErr.Error()
		}
		cleaned = cleanupErr == nil
	}
	if completionContainsSensitiveData(command) {
		command.Outcome = failureOutcome(attempt.Phase)
		command.OutputJSON = mustJSON(map[string]any{"error_code": "sensitive_phase_output", "evidence_limitation": true})
		command.ErrorCode = "sensitive_phase_output"
		command.ErrorMessage = "agent phase output contained sensitive data and was not persisted"
		command.CodeChanges = nil
	}
	command.ExpectedVersion = expectedVersion
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
			cleaned = true
		}
		r.finishLegacy(attempt.ID, InvestigationFailed, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(completionErr.Error()))
		return
	}
	if attempt.Phase == PhaseFix {
		if cleanupErr := staging.Cleanup(); cleanupErr == nil {
			cleaned = true
		}
	}
	status := InvestigationSucceeded
	if command.ErrorCode != "" {
		status = InvestigationFailed
	}
	r.finishLegacy(attempt.ID, status, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(command.ErrorMessage))
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
		prompt := buildStructuredInvestigationPrompt(bug, bot)
		if len(attempt.InputJSON) != 0 && string(attempt.InputJSON) != "{}" {
			prompt += "\n## Studio structured investigation input\n\n```json\n" + string(attempt.InputJSON) + "\n```\n"
		}
		return prompt, nil
	case PhaseFix:
		prompt := BuildCodexFixPrompt(bug, bot, InvestigationRun{}, "")
		if len(attempt.InputJSON) != 0 && string(attempt.InputJSON) != "{}" {
			prompt += "\n## 已授权结构化阶段输入\n\n```json\n" + string(attempt.InputJSON) + "\n```\n"
		}
		return prompt, nil
	default:
		return "", fmt.Errorf("unsupported phase %q", attempt.Phase)
	}
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
	if strings.TrimSpace(input.OriginalValidationAttemptID) == "" || strings.TrimSpace(input.OriginalReproduction) == "" || strings.TrimSpace(input.ExpectedBehavior) == "" || strings.TrimSpace(input.OriginalObservedBehavior) == "" || strings.TrimSpace(input.OriginalScenarioHash) == "" || input.CycleNumber < 1 || strings.TrimSpace(input.DeploymentObservationID) == "" || strings.TrimSpace(input.DeploymentReservationID) == "" || strings.TrimSpace(input.ObservedDeploymentVersion) == "" || strings.TrimSpace(input.TargetEnvironment) == "" || len(input.ExpectedFixCommits) == 0 {
		return errors.New("regression input requires original validation, reproduction, expected behavior, scenario hash, cycle, matched deployment, expected commits, observed deployment version, and target environment")
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
	sb.WriteString(GenerateContext(bug, bot))
	sb.WriteString("\n最终只输出严格 YAML，不得添加字段或解释性段落：\n")
	sb.WriteString("investigation_status: root_cause_ready | insufficient_info\nenvironment: <env>\nroot_cause: <直接和深层根因；信息不足时为空>\nconfidence: high | medium | low\nevidence:\n  - kind: <trace|log|metric|code|config|data|command>\n    path: <Studio staging 目录内的相对路径>\n    captured_at: <RFC3339；仅兼容输出，Studio 以 fstat 为准>\n    environment: <env>\n    version: <可空>\n    request_id: <可空>\n    trace_id: <可空>\n    redaction_status: redacted | not_required # Studio 总会重新扫描\ngaps: []\n")
	return sb.String()
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
	for _, artifact := range result.ArtifactInputs {
		if artifact.Environment != input.TargetEnvironment {
			return errors.New("regression evidence environment does not match the target environment")
		}
		if artifact.Version != input.ObservedDeploymentVersion {
			return errors.New("regression evidence version does not match the observed deployment version")
		}
		if strings.TrimSpace(artifact.RequestID) == "" && strings.TrimSpace(artifact.TraceID) == "" {
			return errors.New("regression evidence requires a fresh request_id or trace_id")
		}
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
	DeploymentObservationID     string            `json:"deployment_observation_id"`
	DeploymentReservationID     string            `json:"deployment_reservation_id"`
	ObservedDeploymentVersion   string            `json:"observed_deployment_version"`
	TargetEnvironment           string            `json:"target_environment"`
	SupplementalEvidence        json.RawMessage   `json:"supplemental_evidence,omitempty"`
}

func ParseValidationResult(data []byte) (ValidationResult, error) {
	var result ValidationResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return ValidationResult{}, fmt.Errorf("parse validation result: %w", err)
	}
	status, err := validationCaseStatus(result.VerificationStatus)
	if err != nil {
		return ValidationResult{}, err
	}
	if strings.TrimSpace(result.Environment) == "" {
		return ValidationResult{}, errors.New("validation environment is required")
	}
	if status != CaseWaitingEvidence && len(result.Gaps) != 0 {
		return ValidationResult{}, errors.New("terminal validation result must not contain blocking gaps")
	}
	if status == CaseReproduced {
		if strings.TrimSpace(result.ObservedBehavior) == "" || strings.TrimSpace(result.ExpectedBehavior) == "" {
			return ValidationResult{}, errors.New("reproduced validation requires observed_behavior and expected_behavior")
		}
		if len(result.Evidence) == 0 {
			return ValidationResult{}, errors.New("reproduced validation requires at least one evidence artifact")
		}
	}
	return result, nil
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
		if len(result.Gaps) != 0 {
			return InvestigationResult{}, errors.New("root_cause_ready must not contain blocking gaps")
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
	return result, nil
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
		result, err := ParseInvestigationResult(data)
		if err != nil {
			return PhaseResult{}, err
		}
		outcome := PhaseOutcomeRootCauseReady
		if result.InvestigationStatus == "insufficient_info" {
			outcome = PhaseOutcomeNeedsEvidence
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
	fmt.Fprintf(&sb, "original_validation_attempt_id: %s\noriginal_reproduction: %s\nexpected_behavior: %s\noriginal_observed_behavior: %s\nscenario_hash: %s\ncycle_number: %d\ntarget_environment: %s\ndeployment_observation_id: %s\ndeployment_reservation_id: %s\nobserved_deployment_version: %s\n", input.OriginalValidationAttemptID, input.OriginalReproduction, input.ExpectedBehavior, input.OriginalObservedBehavior, input.OriginalScenarioHash, input.CycleNumber, input.TargetEnvironment, input.DeploymentObservationID, input.DeploymentReservationID, input.ObservedDeploymentVersion)
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
