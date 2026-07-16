package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

const (
	browserPrimaryExecution                    = "primary"
	browserRepairExecution                     = "repair-1"
	browserCoordinatorPlanJournalName          = "coordinator-plan.json"
	browserCoordinatorPlanJournalKind          = "studio_browser_coordinator_plan"
	browserCoordinatorPlanJournalVersion       = 1
	maxBrowserCoordinatorPlanJournalSize int64 = 2 << 20
)

var browserOutcomeCodes = map[string]string{
	"login_required":   "browser_login_required",
	"runtime_broken":   "browser_runtime_broken",
	"locator_failed":   "browser_locator_failed",
	"assertion_failed": "browser_assertion_failed",
	"policy_blocked":   "browser_policy_blocked",
	"interrupted":      "browser_execution_interrupted",
}

type BrowserCoordinator struct {
	Executor PhaseAgentExecutor
	Verifier BrowserVerifier
}

type BrowserCoordinatorRequest struct {
	Attempt    PhaseAttempt
	Bug        Bug
	Bot        BotRef
	BasePrompt string
	Policy     BrowserSecurityPolicy
	StagingDir string
	Emit       func(InvestigationEvent)
	// FreezeArtifacts must synchronously capture and register the exact bytes
	// bound by HostVerifier before any later agent call can mutate staging.
	FreezeArtifacts func(context.Context, []BrowserArtifactReference) ([]BrowserFrozenArtifact, error)
}

// BrowserFrozenArtifact is the host-owned immutable copy bound to a verifier
// reference. It is consumed only inside the browser coordinator and is never
// projected into workflow results, events, or public HTTP DTOs.
type BrowserFrozenArtifact struct {
	ReferencePath   string
	Kind            string
	SHA256          string
	Size            int64
	PathOrReference string
	Content         []byte
}

type browserFrozenArtifact = BrowserFrozenArtifact

type BrowserCoordinatorResult struct {
	FinalYAML        string
	Usage            AgentUsage
	BrowserArtifacts []BrowserArtifactReference
	BrowserResult    BrowserVerificationResult
	RepairCount      int
	ErrorCode        string
	ErrorMessage     string
}

type browserCoordinatorPlanJournal struct {
	Kind       string      `json:"kind"`
	Version    int         `json:"version"`
	CaseID     string      `json:"case_id"`
	Cycle      int         `json:"cycle_number"`
	AttemptID  string      `json:"attempt_id"`
	Execution  string      `json:"execution"`
	PlanSHA256 string      `json:"plan_sha256"`
	Plan       BrowserPlan `json:"plan"`
}

func (c BrowserCoordinator) Execute(ctx context.Context, request BrowserCoordinatorRequest) (BrowserCoordinatorResult, error) {
	var result BrowserCoordinatorResult
	var frozenArtifacts []browserFrozenArtifact
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(request.Bug.FrontendURL) == "" {
		return browserCoordinatorFailure(result, "browser_url_required"), nil
	}
	if c.Executor == nil {
		return browserCoordinatorFailure(result, "browser_validator_unavailable"), nil
	}
	if c.Verifier == nil {
		return browserCoordinatorFailure(result, "browser_verifier_unavailable"), nil
	}
	if request.FreezeArtifacts == nil {
		return browserCoordinatorFailure(result, "browser_artifact_freezer_unavailable"), nil
	}

	plan, found, err := loadBrowserCoordinatorPlan(request, browserPrimaryExecution)
	if err != nil {
		return browserCoordinatorFailure(result, "browser_execution_interrupted"), nil
	}
	if !found {
		planning, executeErr := c.Executor.ExecutePhase(ctx, request.Attempt.ID, request.Bot, browserPlannerPrompt(request), request.Emit)
		addAgentUsage(&result.Usage, planning.Usage)
		if executeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return browserCoordinatorFailure(result, "browser_validator_failed"), nil
		}
		plan, err = ParseBrowserPlan([]byte(planning.FinalYAML))
		if err != nil || validateDurableBrowserPlan(plan) != nil || validateBrowserPlanStartOrigin(plan, request.Policy) != nil {
			return browserCoordinatorFailure(result, "browser_validator_plan_invalid"), nil
		}
		if err := persistBrowserCoordinatorPlan(request, browserPrimaryExecution, plan); err != nil {
			return browserCoordinatorFailure(result, "browser_execution_interrupted"), nil
		}
	}
	if validateBrowserPlanStartOrigin(plan, request.Policy) != nil {
		return browserCoordinatorFailure(result, "browser_validator_plan_invalid"), nil
	}

	primary, primaryFrozen, err := c.executeBrowser(ctx, request, plan, browserPrimaryExecution)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return browserCoordinatorFailure(result, browserVerifierErrorCode(err)), nil
	}
	result.BrowserResult = primary
	result.BrowserArtifacts = appendBrowserArtifacts(result.BrowserArtifacts, primary.Artifacts)
	frozenArtifacts = append(frozenArtifacts, primaryFrozen...)

	if primary.Status == "locator_failed" {
		repaired, repairFound, journalErr := loadBrowserCoordinatorPlan(request, browserRepairExecution)
		if journalErr != nil {
			return browserCoordinatorFailure(result, "browser_execution_interrupted"), nil
		}
		if repairFound {
			if validateBrowserPlanStartOrigin(repaired, request.Policy) != nil || validateBrowserRepair(plan, primary.FailedActionID, repaired) != nil {
				return browserCoordinatorFailure(result, "browser_execution_interrupted"), nil
			}
		} else {
			repairing, repairErr := c.Executor.ExecutePhase(ctx, request.Attempt.ID, request.Bot, browserRepairPrompt(plan, primary), request.Emit)
			addAgentUsage(&result.Usage, repairing.Usage)
			if repairErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return result, ctxErr
				}
				return browserCoordinatorFailure(result, "browser_validator_failed"), nil
			}
			var parseErr error
			repaired, parseErr = ParseBrowserPlan([]byte(repairing.FinalYAML))
			if parseErr != nil || validateDurableBrowserPlan(repaired) != nil || validateBrowserPlanStartOrigin(repaired, request.Policy) != nil || validateBrowserRepair(plan, primary.FailedActionID, repaired) != nil {
				return browserCoordinatorFailure(result, "browser_validator_plan_invalid"), nil
			}
			if err := persistBrowserCoordinatorPlan(request, browserRepairExecution, repaired); err != nil {
				return browserCoordinatorFailure(result, "browser_execution_interrupted"), nil
			}
		}
		result.RepairCount = 1
		repairedResult, repairedFrozen, executeErr := c.executeBrowser(ctx, request, repaired, browserRepairExecution)
		if executeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return browserCoordinatorFailure(result, browserVerifierErrorCode(executeErr)), nil
		}
		result.BrowserResult = repairedResult
		result.BrowserArtifacts = appendBrowserArtifacts(result.BrowserArtifacts, repairedResult.Artifacts)
		frozenArtifacts = append(frozenArtifacts, repairedFrozen...)
	}

	if result.BrowserResult.Status != "completed" {
		code, ok := browserOutcomeCodes[result.BrowserResult.Status]
		if !ok {
			code = "browser_verifier_failed"
		}
		return browserCoordinatorFailure(result, code), nil
	}

	evaluatorPrompt, cleanupEvaluatorEvidence, err := browserEvaluatorPrompt(result.BrowserResult, result.BrowserArtifacts, frozenArtifacts)
	if err != nil {
		return browserCoordinatorFailure(result, "browser_artifact_invalid"), nil
	}
	cleanedEvaluatorEvidence := false
	defer func() {
		if !cleanedEvaluatorEvidence {
			_ = cleanupEvaluatorEvidence()
		}
	}()
	evaluation, err := c.Executor.ExecutePhase(ctx, request.Attempt.ID, request.Bot, evaluatorPrompt, request.Emit)
	cleanupErr := cleanupEvaluatorEvidence()
	cleanedEvaluatorEvidence = true
	if cleanupErr != nil {
		return browserCoordinatorFailure(result, "browser_artifact_invalid"), nil
	}
	addAgentUsage(&result.Usage, evaluation.Usage)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return browserCoordinatorFailure(result, "browser_validator_failed"), nil
	}
	validation, err := decodeValidationResultStrict([]byte(evaluation.FinalYAML))
	if err != nil {
		return browserCoordinatorFailure(result, "browser_evaluator_result_invalid"), nil
	}
	validation.Evidence = browserArtifactReferences(result.BrowserArtifacts)
	if err := validateValidationResult(validation); err != nil {
		return browserCoordinatorFailure(result, "browser_evaluator_result_invalid"), nil
	}
	canonical, err := json.Marshal(validation)
	if err != nil {
		return browserCoordinatorFailure(result, "browser_evaluator_result_invalid"), nil
	}
	parsed, err := ParsePhaseResult(request.Attempt, canonical)
	if err != nil {
		return browserCoordinatorFailure(result, "browser_evaluator_result_invalid"), nil
	}
	if browserTerminalOutcome(parsed.Outcome) && !browserHasFinalScreenshot(result.BrowserResult, result.BrowserArtifacts) {
		return browserCoordinatorFailure(result, "browser_screenshot_required"), nil
	}
	result.FinalYAML = string(canonical)
	return result, nil
}

func (c BrowserCoordinator) executeBrowser(ctx context.Context, request BrowserCoordinatorRequest, plan BrowserPlan, execution string) (BrowserVerificationResult, []browserFrozenArtifact, error) {
	stagingDir, err := browserExecutionStagingDir(request.StagingDir, execution)
	if err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	environment, version, err := browserAttemptEnvironmentVersion(request.Attempt, request.Bug, request.Bot)
	if err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	browserRequest := BrowserVerificationRequest{
		CaseID: request.Attempt.CaseID, CycleNumber: request.Attempt.CycleNumber, AttemptID: request.Attempt.ID,
		SystemID:    firstNonEmpty(strings.TrimSpace(request.Bug.SystemID), strings.TrimSpace(request.Bot.SystemID)),
		Environment: environment, Version: version, Policy: request.Policy, Plan: plan, StagingDir: stagingDir,
		Emit: func(progress BrowserProgress) {
			if request.Emit != nil {
				request.Emit(browserProgressEvent(progress))
			}
		},
	}
	result, err := c.Verifier.Execute(ctx, browserRequest)
	if err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	rebased, err := rebaseBrowserResult(result, execution)
	if err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	applicationURL, applicationOrigin, err := canonicalBrowserURL(plan.StartURL)
	if err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	// The application/session origin is derived from the durable validated plan,
	// never from a worker redirect or the mutable Bug URL used by a later retry.
	rebased.ApplicationURL = applicationURL
	rebased.ApplicationOrigin = applicationOrigin
	if err := validateBrowserArtifactBinding(rebased.Artifacts, environment, version); err != nil {
		return BrowserVerificationResult{}, nil, err
	}
	frozen, err := request.FreezeArtifacts(ctx, append([]BrowserArtifactReference(nil), rebased.Artifacts...))
	if err != nil {
		return BrowserVerificationResult{}, nil, fmt.Errorf("browser_artifact_invalid: freeze verified browser artifacts: %w", err)
	}
	if err := validateFrozenBrowserArtifacts(rebased.Artifacts, frozen); err != nil {
		return BrowserVerificationResult{}, nil, fmt.Errorf("browser_artifact_invalid: validate frozen browser artifacts: %w", err)
	}
	return rebased, frozen, nil
}

func browserAssistedAttempt(bug Bug, attempt PhaseAttempt) bool {
	if attempt.Phase != PhaseValidation && attempt.Phase != PhaseRegression {
		return false
	}
	if strings.TrimSpace(bug.FrontendURL) != "" {
		return true
	}
	userInput, _, _ := validationPromptInput(attempt.InputJSON)
	text := strings.ToLower(userInput)
	return strings.Contains(text, "web") || strings.Contains(text, "页面") || strings.Contains(text, "浏览器")
}

func browserProgressEvent(progress BrowserProgress) InvestigationEvent {
	return InvestigationEvent{
		Type:    "browser_progress",
		Message: safeBoundedBrowserText(progress.Message, 512),
		Meta: map[string]any{
			"browser_code": safeBoundedBrowserText(progress.Code, 128),
			"action_id":    safeBoundedBrowserText(progress.ActionID, 128),
			"current":      progress.Current,
			"total":        progress.Total,
		},
	}
}

func browserCoordinatorFailure(result BrowserCoordinatorResult, code string) BrowserCoordinatorResult {
	result.FinalYAML = ""
	result.ErrorCode = code
	result.ErrorMessage = browserPublicErrorMessage(code)
	return result
}

func browserPublicErrorMessage(code string) string {
	switch code {
	case "browser_url_required":
		return "Web 验证缺少可访问的前端 URL"
	case "browser_login_required":
		return "需要在验证浏览器中完成登录"
	case "browser_runtime_broken":
		return "验证浏览器运行环境不可用"
	case "browser_locator_failed":
		return "页面元素定位失败，需要补充页面信息"
	case "browser_assertion_failed":
		return "页面断言未通过，需要补充业务证据"
	case "browser_policy_blocked":
		return "浏览器安全策略拒绝了验证计划"
	case "browser_policy_unavailable":
		return "浏览器安全策略当前不可用"
	case "browser_execution_interrupted":
		return "浏览器执行已中断，请重试"
	case "browser_screenshot_required":
		return "Web 验证缺少本次最终截图"
	case "browser_validator_plan_invalid":
		return "验证机器人返回了无效的浏览器计划"
	case "browser_evaluator_result_invalid":
		return "验证机器人返回了无效的验证结果"
	case "browser_validator_unavailable", "browser_validator_failed":
		return "验证机器人执行失败"
	case "browser_verifier_unavailable", "browser_verifier_failed":
		return "验证浏览器执行器不可用"
	case "browser_artifact_freezer_unavailable":
		return "验证浏览器证据冻结器不可用"
	default:
		return "浏览器验证失败"
	}
}

func browserStopOutput(result BrowserCoordinatorResult) json.RawMessage {
	envelope := map[string]any{
		"error_code":       result.ErrorCode,
		"error_message":    result.ErrorMessage,
		"failed_action_id": safeBoundedBrowserText(result.BrowserResult.FailedActionID, 128),
	}
	if browserBusinessEvidenceFailure(result.ErrorCode) {
		envelope["evidence_limitation"] = true
	} else {
		envelope["system_failure"] = true
	}
	if result.ErrorCode == "browser_login_required" {
		envelope["application_url"] = safeBoundedBrowserText(result.BrowserResult.ApplicationURL, 4096)
		envelope["application_origin"] = safeBoundedBrowserText(result.BrowserResult.ApplicationOrigin, 4096)
		envelope["login_origin"] = safeBoundedBrowserText(result.BrowserResult.LoginOrigin, 4096)
	}
	return mustJSON(envelope)
}

func browserBusinessEvidenceFailure(code string) bool {
	return strings.HasPrefix(code, "browser_login_") || code == "browser_locator_failed" || code == "browser_assertion_failed" || code == "browser_url_required"
}

func browserVerifierErrorCode(err error) string {
	if err == nil {
		return "browser_verifier_failed"
	}
	message := strings.ToLower(err.Error())
	prefix, _, _ := strings.Cut(message, ":")
	prefix = strings.TrimSpace(prefix)
	switch {
	case strings.Contains(message, "browser_execution_interrupted"), strings.Contains(message, "browser_worker_interrupted"):
		return "browser_execution_interrupted"
	case strings.Contains(message, "browser_runtime"):
		return "browser_runtime_broken"
	case strings.Contains(message, "browser origin is not allowed"),
		strings.Contains(message, "browser destination is blocked"),
		strings.Contains(message, "browser interaction is blocked"),
		strings.Contains(message, "browser_plan_invalid"):
		return "browser_policy_blocked"
	default:
		if _, ok := browserSystemErrorCodes[prefix]; ok {
			return prefix
		}
		return "browser_verifier_failed"
	}
}

var browserSystemErrorCodes = map[string]struct{}{
	"browser_artifact_invalid":         {},
	"browser_journal_unsafe":           {},
	"browser_reservation_write_failed": {},
	"browser_result_write_failed":      {},
	"browser_session_unavailable":      {},
	"browser_session_cleanup_failed":   {},
	"browser_session_temp_failed":      {},
	"browser_session_invalid":          {},
	"browser_session_store_missing":    {},
	"browser_session_save_failed":      {},
	"browser_worker_output_too_large":  {},
	"browser_worker_failed":            {},
	"browser_worker_protocol_invalid":  {},
}

func addAgentUsage(total *AgentUsage, addition AgentUsage) {
	if total == nil {
		return
	}
	total.InputTokens += addition.InputTokens
	total.OutputTokens += addition.OutputTokens
	total.Duration += addition.Duration
}

func browserAttemptEnvironmentVersion(attempt PhaseAttempt, bug Bug, bot BotRef) (string, string, error) {
	if attempt.Phase == PhaseRegression {
		var input RegressionValidationInput
		if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
			return "", "", fmt.Errorf("decode regression browser input: %w", err)
		}
		return strings.TrimSpace(input.TargetEnvironment), strings.TrimSpace(input.ObservedDeploymentVersion), nil
	}
	return effectiveBugEnv(bug, bot), "", nil
}

func browserExecutionStagingDir(root, execution string) (string, error) {
	if strings.TrimSpace(root) == "" || (execution != browserPrimaryExecution && execution != browserRepairExecution) {
		return "", errors.New("browser execution staging identity is invalid")
	}
	executions := filepath.Join(root, "browser-executions")
	if err := ensureOwnedBrowserDirectory(executions); err != nil {
		return "", err
	}
	directory := filepath.Join(executions, execution)
	if err := ensureOwnedBrowserDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func loadBrowserCoordinatorPlan(request BrowserCoordinatorRequest, execution string) (BrowserPlan, bool, error) {
	directory, err := browserExecutionStagingDir(request.StagingDir, execution)
	if err != nil {
		return BrowserPlan{}, false, err
	}
	path := filepath.Join(directory, browserCoordinatorPlanJournalName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			return BrowserPlan{}, false, readErr
		}
		if len(entries) != 0 {
			return BrowserPlan{}, false, errors.New("browser execution slot is missing its durable plan")
		}
		return BrowserPlan{}, false, nil
	}
	if err != nil {
		return BrowserPlan{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > maxBrowserCoordinatorPlanJournalSize {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return BrowserPlan{}, false, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 {
		_ = file.Close()
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal identity changed")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxBrowserCoordinatorPlanJournalSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return BrowserPlan{}, false, err
	}
	if len(content) == 0 || int64(len(content)) > maxBrowserCoordinatorPlanJournalSize || containsSensitiveData(content) {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal content is unsafe")
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var journal browserCoordinatorPlanJournal
	if err := decoder.Decode(&journal); err != nil {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal has trailing content")
	}
	if journal.Kind != browserCoordinatorPlanJournalKind || journal.Version != browserCoordinatorPlanJournalVersion ||
		journal.CaseID != request.Attempt.CaseID || journal.Cycle != request.Attempt.CycleNumber || journal.AttemptID != request.Attempt.ID || journal.Execution != execution {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal identity does not match its attempt slot")
	}
	if err := validateDurableBrowserPlan(journal.Plan); err != nil {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal contains an invalid plan")
	}
	planSHA, err := durableBrowserPlanSHA256(journal.Plan)
	if err != nil || journal.PlanSHA256 != planSHA {
		return BrowserPlan{}, false, errors.New("browser coordinator plan journal digest does not match")
	}
	return journal.Plan, true, nil
}

func persistBrowserCoordinatorPlan(request BrowserCoordinatorRequest, execution string, plan BrowserPlan) error {
	if err := validateDurableBrowserPlan(plan); err != nil {
		return err
	}
	directory, err := browserExecutionStagingDir(request.StagingDir, execution)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("browser execution slot is not empty before plan publication")
	}
	planSHA, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		return err
	}
	journal := browserCoordinatorPlanJournal{
		Kind: browserCoordinatorPlanJournalKind, Version: browserCoordinatorPlanJournalVersion,
		CaseID: request.Attempt.CaseID, Cycle: request.Attempt.CycleNumber, AttemptID: request.Attempt.ID,
		Execution: execution, PlanSHA256: planSHA, Plan: plan,
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if int64(len(encoded)) > maxBrowserCoordinatorPlanJournalSize || containsSensitiveData(encoded) {
		return errors.New("browser coordinator plan journal content is unsafe")
	}

	temporary, err := os.CreateTemp(directory, ".coordinator-plan-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writeErr := func() error {
		if _, err := temporary.Write(encoded); err != nil {
			return err
		}
		return temporary.Sync()
	}()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := ensureOwnedBrowserDirectory(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, browserCoordinatorPlanJournalName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("browser coordinator plan journal already exists")
		}
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncBrowserCoordinatorDirectory(directory)
}

func durableBrowserPlanSHA256(plan BrowserPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:]), nil
}

func validateDurableBrowserPlan(plan BrowserPlan) error {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	parsed, err := ParseBrowserPlan(encoded)
	if err != nil || !reflect.DeepEqual(parsed, plan) {
		return errors.New("browser plan is not canonical and strict")
	}
	if containsSensitiveData(encoded) {
		return errors.New("browser plan contains credential material")
	}
	if _, _, err := canonicalBrowserURL(plan.StartURL); err != nil {
		return err
	}
	for _, action := range plan.Actions {
		if action.Action == "goto" {
			if _, _, err := canonicalBrowserURL(action.URL); err != nil {
				return err
			}
		}
		if action.Action != "fill" {
			continue
		}
		fields := []string{action.ID, action.Value}
		if action.Locator != nil {
			fields = append(fields, action.Locator.Kind, action.Locator.Value, action.Locator.Name)
		}
		for _, field := range fields {
			if browserCredentialSemantic(field) {
				return errors.New("browser fill action has credential semantics")
			}
		}
	}
	return nil
}

func browserCredentialSemantic(value string) bool {
	lower := strings.ToLower(value)
	for _, fragment := range []string{"密码", "口令", "验证码", "账号", "用户名", "密钥"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	if at := strings.IndexByte(lower, '@'); at > 0 && at == strings.LastIndexByte(lower, '@') && at+1 < len(lower) && strings.Contains(lower[at+1:], ".") && !strings.ContainsAny(lower, " \t\r\n") {
		return true
	}
	var normalized strings.Builder
	var previous rune
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			if unicode.IsUpper(current) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
				normalized.WriteByte(' ')
			}
			normalized.WriteRune(unicode.ToLower(current))
		} else {
			normalized.WriteByte(' ')
		}
		previous = current
	}
	tokens := strings.Fields(normalized.String())
	for _, token := range tokens {
		switch token {
		case "password", "passwd", "pwd", "passcode", "pin", "otp", "mfa", "secret", "token", "auth", "cookie", "key", "login", "account", "username", "signin", "credential", "credentials", "userid", "email", "captcha":
			return true
		}
		for _, prefix := range []string{"password", "passwd", "passcode", "secret", "token", "auth", "cookie", "login", "account", "username", "signin", "credential", "userid", "email", "captcha"} {
			if strings.HasPrefix(token, prefix) || strings.HasSuffix(token, prefix) {
				return true
			}
		}
	}
	compact := strings.Join(tokens, "")
	for _, semantic := range []string{"password", "passwd", "passcode", "apikey", "accesskey", "privatekey", "login", "account", "username", "signin", "credential", "userid", "email", "captcha", "verificationcode"} {
		if strings.Contains(compact, semantic) {
			return true
		}
	}
	return false
}

var browserDurabilitySync = syncBrowserDirectory

func syncBrowserCoordinatorDirectory(path string) error {
	return browserDurabilitySync(path)
}

func syncBrowserDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if runtime.GOOS == "windows" {
		return closeErr
	}
	return errors.Join(syncErr, closeErr)
}

func openOrCreateBrowserAttemptStaging(root, attemptID string) (attemptEvidenceStaging, error) {
	staging, found, err := openExistingBrowserAttemptStaging(root, attemptID)
	if err != nil {
		return nil, err
	}
	if found {
		return &openedBrowserAttemptStaging{attemptEvidenceStaging: staging, created: false}, nil
	}
	staging, err = openAttemptEvidenceStaging(root, attemptID)
	if err != nil {
		return nil, err
	}
	return &openedBrowserAttemptStaging{attemptEvidenceStaging: staging, created: true}, nil
}

type openedBrowserAttemptStaging struct {
	attemptEvidenceStaging
	created bool
}

func (s *openedBrowserAttemptStaging) CreatedForCurrentOpen() bool { return s != nil && s.created }

func openExistingBrowserAttemptStaging(root, attemptID string) (attemptEvidenceStaging, bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var locator string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), attemptID+"-") {
			continue
		}
		if locator != "" {
			return nil, false, errors.New("multiple browser staging directories exist for one attempt")
		}
		locator = entry.Name()
	}
	if locator != "" {
		staging, err := openExistingAttemptEvidenceStaging(root, attemptID, locator)
		return staging, err == nil, err
	}
	return nil, false, nil
}

func ensureOwnedBrowserDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("browser execution staging directory is unsafe")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	if err := syncBrowserCoordinatorDirectory(path); err != nil {
		return err
	}
	if err := syncBrowserCoordinatorDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func rebaseBrowserResult(result BrowserVerificationResult, execution string) (BrowserVerificationResult, error) {
	rebased := result
	rebased.ErrorCode = safeBoundedBrowserText(result.ErrorCode, 128)
	rebased.ErrorMessage = ""
	rebased.FailedActionID = safeBoundedBrowserText(result.FailedActionID, 128)
	rebased.FinalURL = safeBoundedBrowserText(result.FinalURL, 4096)
	rebased.Title = safeBoundedBrowserText(result.Title, 512)
	rebased.ApplicationURL = safeBoundedBrowserText(result.ApplicationURL, 4096)
	rebased.ApplicationOrigin = safeBoundedBrowserText(result.ApplicationOrigin, 4096)
	rebased.LoginOrigin = safeBoundedBrowserText(result.LoginOrigin, 4096)
	rebased.AccessibilitySummary = boundedBrowserAccessibility(result.AccessibilitySummary)
	rebased.Artifacts = make([]BrowserArtifactReference, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		path, err := rebaseBrowserArtifactPath(artifact.Path, execution)
		if err != nil {
			return BrowserVerificationResult{}, err
		}
		artifact.Path = path
		rebased.Artifacts = append(rebased.Artifacts, artifact)
	}
	if strings.TrimSpace(result.FinalScreenshotPath) != "" {
		path, err := rebaseBrowserArtifactPath(result.FinalScreenshotPath, execution)
		if err != nil {
			return BrowserVerificationResult{}, err
		}
		rebased.FinalScreenshotPath = path
	}
	return rebased, nil
}

func rebaseBrowserArtifactPath(path, execution string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.ContainsRune(path, '\x00') {
		return "", errors.New("browser artifact path is unsafe")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", errors.New("browser artifact path is unsafe")
	}
	return filepath.ToSlash(filepath.Join("browser-executions", execution, filepath.FromSlash(clean))), nil
}

func appendBrowserArtifacts(current, additions []BrowserArtifactReference) []BrowserArtifactReference {
	seen := make(map[string]struct{}, len(current)+len(additions))
	result := make([]BrowserArtifactReference, 0, len(current)+len(additions))
	for _, artifact := range append(append([]BrowserArtifactReference(nil), current...), additions...) {
		key := artifact.Kind + "\x00" + artifact.Path
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, artifact)
	}
	return result
}

func validateBrowserArtifactBinding(artifacts []BrowserArtifactReference, environment, version string) error {
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case "screenshot", "network", "console", "browser_actions":
		default:
			return errors.New("browser verifier returned an unsupported artifact kind")
		}
		if artifact.Environment != environment || artifact.Version != version {
			return errors.New("browser artifact environment or version does not match its attempt")
		}
	}
	return nil
}

func browserArtifactReferences(references []BrowserArtifactReference) []ArtifactReference {
	result := make([]ArtifactReference, 0, len(references))
	for _, reference := range references {
		result = append(result, ArtifactReference{
			Kind: reference.Kind, Path: reference.Path, Environment: reference.Environment, Version: reference.Version,
			RequestID: reference.RequestID, TraceID: reference.TraceID, RedactionStatus: RedactionStatusNotRequired,
		})
	}
	return result
}

func browserTerminalOutcome(outcome PhaseOutcome) bool {
	switch outcome {
	case PhaseOutcomeReproduced, PhaseOutcomeNotReproduced, PhaseOutcomeFixedVerified, PhaseOutcomeStillReproduces:
		return true
	default:
		return false
	}
}

func browserHasFinalScreenshot(result BrowserVerificationResult, artifacts []BrowserArtifactReference) bool {
	if result.Status != "completed" || strings.TrimSpace(result.FinalScreenshotPath) == "" || !strings.EqualFold(filepath.Ext(result.FinalScreenshotPath), ".png") {
		return false
	}
	for _, artifact := range artifacts {
		if artifact.Kind == "screenshot" && artifact.Path == result.FinalScreenshotPath && strings.EqualFold(filepath.Ext(artifact.Path), ".png") {
			return true
		}
	}
	return false
}

func validateBrowserRepair(original BrowserPlan, failedActionID string, repaired BrowserPlan) error {
	failedIndex := -1
	for index, action := range original.Actions {
		if action.ID == failedActionID {
			failedIndex = index
			break
		}
	}
	if failedIndex < 0 {
		return errors.New("failed browser action is not part of the original plan")
	}
	if original.Version != repaired.Version || !reflect.DeepEqual(original.Assertions, repaired.Assertions) {
		return errors.New("browser repair changed the plan version or assertions")
	}
	originalURL, originalOrigin, err := canonicalBrowserURL(original.StartURL)
	if err != nil {
		return err
	}
	repairedURL, repairedOrigin, err := canonicalBrowserURL(repaired.StartURL)
	if err != nil || repairedURL != originalURL || repairedOrigin != originalOrigin {
		return errors.New("browser repair changed the start URL or origin")
	}
	want := original.Actions[failedIndex:]
	if len(repaired.Actions) != len(want) {
		return errors.New("browser repair must contain exactly the failed and remaining actions")
	}
	for index := range want {
		before, after := want[index], repaired.Actions[index]
		if before.ID != after.ID || before.Action != after.Action || before.URL != after.URL || before.Value != after.Value || before.Key != after.Key || before.ScreenshotAfter != after.ScreenshotAfter {
			return errors.New("browser repair may change locators only")
		}
		if before.Locator == nil && !reflect.DeepEqual(before.Locator, after.Locator) {
			return errors.New("browser repair added a locator to a non-locator action")
		}
	}
	return nil
}

func canonicalBrowserURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", "", errors.New("browser start URL is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", errors.New("browser start URL is invalid")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", "", errors.New("browser start URL query is invalid")
	}
	for key, values := range query {
		if browserCredentialSemantic(key) || strings.EqualFold(key, "code") || strings.EqualFold(key, "session") {
			return "", "", errors.New("browser start URL contains credential material")
		}
		for _, value := range values {
			if browserCredentialSemantic(value) || containsSensitiveData([]byte(value)) {
				return "", "", errors.New("browser start URL contains credential material")
			}
		}
	}
	hostname := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
	if hostname == "" || strings.Contains(hostname, "%") || strings.HasSuffix(parsed.Host, ":") {
		return "", "", errors.New("browser start URL host is invalid")
	}
	if address, addressErr := netip.ParseAddr(hostname); addressErr == nil {
		if address.Zone() != "" {
			return "", "", errors.New("browser start URL host is invalid")
		}
		hostname = address.String()
	}
	port := parsed.Port()
	if port != "" {
		numericPort, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || numericPort == 0 {
			return "", "", errors.New("browser start URL port is invalid")
		}
		port = strconv.FormatUint(numericPort, 10)
	}
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return parsed.String(), origin, nil
}

func validateBrowserPlanStartOrigin(plan BrowserPlan, policy BrowserSecurityPolicy) error {
	_, startOrigin, err := canonicalBrowserURL(plan.StartURL)
	if err != nil {
		return err
	}
	for _, configuredOrigins := range [][]string{policy.StartOrigins, policy.ApplicationOrigins} {
		allowed := false
		for _, configured := range configuredOrigins {
			_, origin, originErr := canonicalBrowserURL(strings.TrimSpace(configured))
			if originErr == nil && origin == startOrigin {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.New("browser start origin is not configured")
		}
	}
	return nil
}

func browserPlannerPrompt(request BrowserCoordinatorRequest) string {
	contextFields := map[string]any{
		"bug_id": request.Bug.ID, "title": request.Bug.Title, "description": request.Bug.Description,
		"steps": request.Bug.Steps, "expected": request.Bug.Expected, "actual": request.Bug.Actual,
		"frontend_url": request.Bug.FrontendURL, "phase": request.Attempt.Phase, "mode": request.Attempt.Mode,
		"cycle_number": request.Attempt.CycleNumber, "scope": request.BasePrompt,
	}
	return "You are the validation browser planner. Produce only BrowserPlan YAML. Do not output ValidationResult or prose.\n" +
		"Allowed actions are exactly goto, click, fill, press, select, wait_for, screenshot. Never use JavaScript, evaluate, XPath, file upload, credentials, cookies, headers, or storageState. Login must use the already-visible host browser session; never plan credential entry.\n" +
		"Current validation scope (redacted and bounded):\n" + safeBoundedBrowserJSON(contextFields, 24<<10) + "\n" +
		"Current environment and configured browser policy (redacted and bounded):\n" + safeBoundedBrowserJSON(request.Policy, 12<<10) + "\n" +
		"Exact schema:\nversion: 1\nstart_url: <absolute configured HTTP(S) URL>\nactions:\n  - id: <stable-id>\n    action: goto | click | fill | press | select | wait_for | screenshot\n    locator: {kind: role | label | text | placeholder | test_id | css, value: <value>, name: <optional accessible name>}\n    url: <goto only>\n    value: <fill/select only>\n    key: <press only>\n    screenshot_after: true | false\nassertions:\n  - kind: visible_text\n    value: <expected visible text>\n" +
		"Use the configured frontend_url as start_url. Respect is_prod and configured origins. Output BrowserPlan YAML only.\n"
}

func browserRepairPrompt(original BrowserPlan, failed BrowserVerificationResult) string {
	completed := make([]string, 0, len(original.Actions))
	for _, action := range original.Actions {
		if action.ID == failed.FailedActionID {
			break
		}
		completed = append(completed, action.ID)
	}
	report := map[string]any{
		"completed_action_ids": completed, "failed_action_id": failed.FailedActionID,
		"playwright_category": failed.ErrorCode, "final_url": failed.FinalURL, "title": failed.Title,
		"accessibility": boundedBrowserAccessibility(failed.AccessibilitySummary),
	}
	return "Repair the locator once. Output BrowserPlan YAML only.\n" +
		"The repaired plan must contain exactly the failed action and remaining actions in their original ID/order. It must not repeat completed actions.\n" +
		"Only locator fields may change. Keep version, normalized start_url/origin, action/url/value/key/screenshot_after fields, and assertions unchanged.\n" +
		"Original plan (bounded):\n" + safeBoundedBrowserJSON(original, 24<<10) + "\n" +
		"Sanitized failure report:\n" + safeBoundedBrowserJSON(report, 16<<10) + "\n"
}

func browserEvaluatorPrompt(result BrowserVerificationResult, artifacts []BrowserArtifactReference, frozen []browserFrozenArtifact) (string, func() error, error) {
	screenshotPath, structuredEvidence, cleanup, err := prepareBrowserEvaluatorEvidence(result, frozen)
	if err != nil {
		return "", func() error { return nil }, err
	}
	report := map[string]any{
		"status": result.Status, "final_url": result.FinalURL, "title": result.Title,
		"final_screenshot_path": result.FinalScreenshotPath,
	}
	prompt := "Evaluate the completed browser verification. Output only the strict ValidationResult YAML contract below.\n" +
		"Sanitized execution report:\n" + safeBoundedBrowserJSON(report, 12<<10) + "\n" +
		"Bounded accessibility summary:\n" + safeBoundedBrowserJSON(boundedBrowserAccessibility(result.AccessibilitySummary), 16<<10) + "\n" +
		"Exact host artifact relative references (authoritative; do not invent or alter paths):\n" + safeBoundedBrowserJSON(boundedBrowserArtifacts(artifacts), 24<<10) + "\n" +
		frozenBrowserEvidencePrompt(screenshotPath, structuredEvidence) +
		validationOutputContract()
	return prompt, cleanup, nil
}

func boundedBrowserAccessibility(nodes []BrowserAccessibilityNode) []BrowserAccessibilityNode {
	if len(nodes) > 50 {
		nodes = nodes[:50]
	}
	result := make([]BrowserAccessibilityNode, 0, len(nodes))
	for _, node := range nodes {
		node.Role = safeBoundedBrowserText(node.Role, 128)
		node.Name = safeBoundedBrowserText(node.Name, 512)
		result = append(result, node)
	}
	return result
}

func boundedBrowserArtifacts(artifacts []BrowserArtifactReference) []BrowserArtifactReference {
	if len(artifacts) > 128 {
		artifacts = artifacts[:128]
	}
	result := make([]BrowserArtifactReference, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifact.Kind = safeBoundedBrowserText(artifact.Kind, 128)
		artifact.Path = safeBoundedBrowserText(artifact.Path, 4096)
		artifact.Environment = safeBoundedBrowserText(artifact.Environment, 512)
		artifact.Version = safeBoundedBrowserText(artifact.Version, 512)
		artifact.RequestID = safeBoundedBrowserText(artifact.RequestID, 512)
		artifact.TraceID = safeBoundedBrowserText(artifact.TraceID, 512)
		result = append(result, artifact)
	}
	return result
}

func safeBoundedBrowserJSON(value any, limit int) string {
	redacted := redactResetURLUserinfo(redactSensitiveAny(value))
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "{}"
	}
	return safeBoundedBrowserText(string(encoded), limit)
}

func safeBoundedBrowserText(value string, limit int) string {
	if redacted, ok := redactResetURLUserinfo(value).(string); ok {
		value = redacted
	} else {
		value = redactSensitiveText(value)
	}
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = string([]byte(value)[:limit])
	return strings.ToValidUTF8(value, "") + "…"
}
