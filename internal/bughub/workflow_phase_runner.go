package bughub

import (
	"bytes"
	"context"
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

type FixBranchResult struct {
	Repo                    string `yaml:"repo" json:"repo"`
	BaseBranch              string `yaml:"base_branch" json:"base_branch"`
	FixBranch               string `yaml:"fix_branch" json:"fix_branch"`
	Commit                  string `yaml:"commit" json:"commit"`
	Pushed                  bool   `yaml:"pushed" json:"pushed"`
	TargetEnvironmentBranch string `yaml:"target_environment_branch" json:"target_environment_branch"`
	PushRemote              string `yaml:"push_remote,omitempty" json:"push_remote,omitempty"`
}

type FixTestResult struct {
	Repo    string `yaml:"repo" json:"repo"`
	Commit  string `yaml:"commit" json:"commit"`
	Command string `yaml:"command" json:"command"`
	Result  string `yaml:"result" json:"result"`
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

type FixResult struct {
	FixStatus        string              `yaml:"fix_status" json:"fix_status"`
	Environment      string              `yaml:"environment" json:"environment"`
	Branches         []FixBranchResult   `yaml:"branches" json:"branches"`
	Changes          []string            `yaml:"changes" json:"changes"`
	Tests            []FixTestResult     `yaml:"tests" json:"tests"`
	DeploymentNotice string              `yaml:"deployment_notice" json:"deployment_notice"`
	Risks            []string            `yaml:"risks" json:"risks"`
	BlockedReason    string              `yaml:"blocked_reason,omitempty" json:"blocked_reason,omitempty"`
	Evidence         []ArtifactReference `yaml:"evidence" json:"evidence"`
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
	store         *CaseStore
	executor      PhaseAgentExecutor
	legacy        *InvestigationStore
	artifactsRoot string
	complete      PhaseCompletionFunc
	eventSink     InvestigationEventSink

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
	return &AgentPhaseRunner{store: store, executor: executor, legacy: legacy, artifactsRoot: artifactsRoot, complete: complete, active: make(map[string]context.CancelFunc), scheduled: make(map[string]struct{})}
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
	r.mu.Lock()
	_, alreadyScheduled := r.scheduled[attempt.ID]
	r.mu.Unlock()
	if alreadyScheduled {
		return nil
	}
	if strings.TrimSpace(attempt.AgentTarget) != "" && strings.TrimSpace(bot.Target) != attempt.AgentTarget {
		return fmt.Errorf("bot target %q does not match persisted attempt target %q", bot.Target, attempt.AgentTarget)
	}
	if strings.TrimSpace(bot.Key) != attempt.BotKey {
		return fmt.Errorf("bot key %q does not match persisted attempt bot %q", bot.Key, attempt.BotKey)
	}
	if attempt.Phase == PhaseLegacy {
		return errors.New("legacy attempts are read-only projections")
	}
	incident, err := r.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(incident.CurrentAttemptID) == "" || incident.CurrentAttemptID != attempt.ID {
		return ErrAttemptNotCurrent
	}
	if incident.Status != statusForPhase(attempt.Phase) || incident.CycleNumber != attempt.CycleNumber || strings.TrimSpace(incident.SelectedBotKey) == "" || incident.SelectedBotKey != attempt.BotKey {
		return errors.New("phase attempt is not bound to the current Case status, cycle, and selected bot")
	}
	persisted, err := r.store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		return err
	}
	if persisted.Status != AttemptStatusQueued && persisted.Status != AttemptStatusRunning {
		return fmt.Errorf("phase attempt %s is not runnable: %s", persisted.ID, persisted.Status)
	}
	if !sameRunnableAttempt(persisted, attempt) {
		return errors.New("caller phase attempt does not match persisted attempt")
	}
	if _, found, err := parseCompletionIntent(persisted.OutputJSON); err != nil {
		return err
	} else if found {
		return errors.New("phase attempt already has a persisted completion intent")
	}
	staging, err := openAttemptEvidenceStaging(r.artifactsRoot, attempt.ID)
	if err != nil {
		return fmt.Errorf("create Studio evidence staging: %w", err)
	}
	prompt, err := r.promptForAttempt(attempt, bug, bot)
	if err != nil {
		_ = staging.Close()
		return err
	}
	prompt += "\n## Studio evidence staging (mandatory)\n\nSTUDIO_EVIDENCE_STAGING_DIR=" + staging.Path() + "\nWrite every evidence file beneath this exact directory. In final YAML, `path` must be a clean relative path from this directory; absolute paths and `..` are rejected. Studio derives timestamps and redaction status from securely opened file bytes.\n"
	r.mu.Lock()
	if _, exists := r.scheduled[attempt.ID]; exists {
		r.mu.Unlock()
		_ = staging.Close()
		return nil
	}
	complete := r.complete
	if complete == nil {
		r.mu.Unlock()
		_ = staging.Close()
		return errors.New("agent phase completion callback is required")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	r.scheduled[attempt.ID] = struct{}{}
	r.active[attempt.ID] = cancel
	r.mu.Unlock()

	r.startLegacyProjection(attempt, bug, bot, prompt)
	go r.run(runCtx, attempt.Clone(), bug, bot, prompt, staging, incident.Version, complete)
	return nil
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

func (r *AgentPhaseRunner) run(ctx context.Context, attempt PhaseAttempt, _ Bug, bot BotRef, prompt string, staging attemptEvidenceStaging, expectedVersion int64, complete PhaseCompletionFunc) {
	started := time.Now()
	cleaned := false
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
	result, runErr := r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
	if runErr != nil && attempt.Phase != PhaseFix && ctx.Err() == nil {
		r.projectEvent(attempt, InvestigationEvent{Type: "retry", Message: "read-only phase process retry"})
		firstUsage := result.Usage
		result, runErr = r.executor.ExecutePhase(ctx, attempt.ID, bot, prompt, emit)
		result.Usage.InputTokens += firstUsage.InputTokens
		result.Usage.OutputTokens += firstUsage.OutputTokens
	}
	if ctx.Err() != nil {
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
			if err := r.validateResultEnvironment(ctx, attempt, parsed); err != nil {
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
			}
		}
	}
	cleanupErr := staging.Cleanup()
	if cleanupErr != nil {
		command.Outcome = failureOutcome(attempt.Phase)
		command.OutputJSON = mustJSON(map[string]any{"error_code": "evidence_staging_cleanup_failed", "error_message": cleanupErr.Error(), "evidence_limitation": true})
		command.ErrorCode = "evidence_staging_cleanup_failed"
		command.ErrorMessage = cleanupErr.Error()
	}
	cleaned = cleanupErr == nil
	if completionContainsSensitiveData(command) {
		command.Outcome = failureOutcome(attempt.Phase)
		command.OutputJSON = mustJSON(map[string]any{"error_code": "sensitive_phase_output", "evidence_limitation": true})
		command.ErrorCode = "sensitive_phase_output"
		command.ErrorMessage = "agent phase output contained sensitive data and was not persisted"
		command.CodeChanges = nil
	}
	command.ExpectedVersion = expectedVersion
	if err := r.store.SaveCompletionIntentIfRunning(ctx, command); err != nil {
		r.finishLegacy(attempt.ID, InvestigationFailed, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(err.Error()))
		return
	}
	if err := complete(ctx, command); err != nil {
		r.finishLegacy(attempt.ID, InvestigationFailed, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(err.Error()))
		return
	}
	status := InvestigationSucceeded
	if command.ErrorCode != "" {
		status = InvestigationFailed
	}
	r.finishLegacy(attempt.ID, status, safeLegacyPhaseText(result.FinalYAML), safeLegacyPhaseText(command.ErrorMessage))
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
		return BuildCodexValidationPrompt(bug, bot), nil
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
		return buildStructuredInvestigationPrompt(bug, bot), nil
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

func (r *AgentPhaseRunner) validateRegressionInputBinding(ctx context.Context, attempt PhaseAttempt, input RegressionValidationInput) error {
	if strings.TrimSpace(input.OriginalReproduction) == "" || strings.TrimSpace(input.OriginalScenarioHash) == "" || strings.TrimSpace(input.ObservedDeploymentVersion) == "" || strings.TrimSpace(input.TargetEnvironment) == "" || len(input.ExpectedFixCommits) == 0 {
		return errors.New("regression input requires original reproduction, scenario hash, expected commits, observed deployment version, and target environment")
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
	if input.TargetEnvironment != incident.Environment {
		return errors.New("regression target environment does not match Case environment")
	}
	observations, err := r.store.ListDeploymentObservations(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	for index := len(observations) - 1; index >= 0; index-- {
		observation := observations[index]
		if observation.Result == DeploymentResultMatched && observation.Environment == input.TargetEnvironment && observation.ObservedVersion == input.ObservedDeploymentVersion && equalStringMap(observation.ExpectedCommits, input.ExpectedFixCommits) {
			return nil
		}
	}
	return errors.New("regression input is not bound to a matched deployment observation")
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

func (r *AgentPhaseRunner) validateRegressionEvidence(ctx context.Context, attempt PhaseAttempt, result PhaseResult) error {
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
	}
	observations, err := r.store.ListDeploymentObservations(ctx, attempt.CaseID)
	if err != nil {
		return err
	}
	for index := len(observations) - 1; index >= 0; index-- {
		observation := observations[index]
		if observation.Result == DeploymentResultMatched && observation.Environment == input.TargetEnvironment && observation.ObservedVersion == input.ObservedDeploymentVersion && equalStringMap(observation.ExpectedCommits, input.ExpectedFixCommits) {
			return nil
		}
	}
	return errors.New("regression requires a matched deployment observation for the expected commits, version, and environment")
}

func (r *AgentPhaseRunner) startLegacyProjection(attempt PhaseAttempt, bug Bug, bot BotRef, prompt string) {
	if r.legacy == nil {
		return
	}
	_ = r.legacy.ProjectAttempt(InvestigationRun{ID: attempt.ID, BugID: bug.ID, BotKey: bot.Key, Status: InvestigationRunning, StartedAt: attempt.StartedAt, PromptPreview: promptPreview(prompt), ContinuationOf: attempt.ParentAttemptID})
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
	OriginalReproduction       string            `json:"original_reproduction"`
	OriginalEvidenceReferences []string          `json:"original_evidence_refs"`
	OriginalScenarioHash       string            `json:"scenario_hash"`
	ExpectedFixCommits         map[string]string `json:"expected_fix_commits"`
	ObservedDeploymentVersion  string            `json:"observed_deployment_version"`
	TargetEnvironment          string            `json:"target_environment"`
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

func ParseFixResult(data []byte) (FixResult, error) {
	var result FixResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return FixResult{}, fmt.Errorf("parse fix result: %w", err)
	}
	switch result.FixStatus {
	case "fixed_pushed":
		if len(result.Branches) == 0 {
			return FixResult{}, errors.New("fixed_pushed requires branches")
		}
		for _, branch := range result.Branches {
			if strings.TrimSpace(branch.Repo) == "" || strings.TrimSpace(branch.BaseBranch) == "" || strings.TrimSpace(branch.FixBranch) == "" || strings.TrimSpace(branch.Commit) == "" || !branch.Pushed || strings.TrimSpace(branch.TargetEnvironmentBranch) == "" || strings.TrimSpace(branch.PushRemote) == "" {
				return FixResult{}, errors.New("fixed_pushed requires pushed commit and branch context for every repository")
			}
		}
		if len(result.Tests) == 0 {
			return FixResult{}, errors.New("fixed_pushed requires test evidence")
		}
	case "blocked", "failed":
		if strings.TrimSpace(result.BlockedReason) == "" {
			return FixResult{}, fmt.Errorf("%s requires blocked_reason", result.FixStatus)
		}
	default:
		return FixResult{}, fmt.Errorf("unsupported fix status %q", result.FixStatus)
	}
	if strings.TrimSpace(result.Environment) == "" {
		return FixResult{}, errors.New("fix environment is required")
	}
	for _, test := range result.Tests {
		if test.Result != "passed" && test.Result != "failed" && test.Result != "skipped" {
			return FixResult{}, fmt.Errorf("unsupported fix test result %q", test.Result)
		}
		if result.FixStatus == "fixed_pushed" && test.Result != "passed" {
			return FixResult{}, errors.New("fixed_pushed requires every reported test to pass")
		}
		if result.FixStatus == "fixed_pushed" && (strings.TrimSpace(test.Repo) == "" || strings.TrimSpace(test.Commit) == "" || strings.TrimSpace(test.Command) == "") {
			return FixResult{}, errors.New("fixed_pushed test evidence requires repository, commit, and command")
		}
	}
	if result.FixStatus == "fixed_pushed" {
		for _, branch := range result.Branches {
			matched := false
			for _, test := range result.Tests {
				if test.Repo == branch.Repo && test.Commit == branch.Commit {
					matched = true
					break
				}
			}
			if !matched {
				return FixResult{}, fmt.Errorf("fixed_pushed requires passing test evidence for %s@%s", branch.Repo, branch.Commit)
			}
		}
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
				testEvidence, _ := json.Marshal(result.Tests)
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
	fmt.Fprintf(&sb, "original_reproduction: %s\nscenario_hash: %s\ntarget_environment: %s\nobserved_deployment_version: %s\n", input.OriginalReproduction, input.OriginalScenarioHash, input.TargetEnvironment, input.ObservedDeploymentVersion)
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
	sb.WriteString("每条新证据必须包含 captured_at 和本次 attempt 的 artifact path，并尽量包含新的 request_id 或 trace_id。\n")
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
