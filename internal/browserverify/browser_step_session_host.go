package browserverify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type browserStepSessionTransport interface {
	Step(context.Context, workerStepSessionCommand) (workerStepSessionResult, error)
	Close() error
}

// browserStepEvidenceFreezer persists the sanitized, Host-bound observation
// before a journal transition can reference it. The callback is deliberately
// required: a Scene ref must never point at evidence that was not frozen.
type browserStepEvidenceFreezer func(
	context.Context,
	int,
	workerStepSessionResult,
	bughub.BrowserScene,
) (string, error)

type boundNodeBrowserStepSession struct {
	mu         sync.Mutex
	transport  browserStepSessionTransport
	resolver   IPResolver
	policy     bughub.BrowserSecurityPolicy
	attemptID  string
	current    bughub.BrowserScene
	currentRef string
	sequence   int
	freeze     browserStepEvidenceFreezer
	closed     bool
}

type boundBrowserStepSessionInitial struct {
	Scene    bughub.BrowserScene
	SceneRef string
}

// bindNodeBrowserStepSession converts the untrusted ready envelope into a
// Host-bound Scene and freezes it. It is a shadow API until the default browser
// coordinator explicitly opts into the single-step loop.
func bindNodeBrowserStepSession(
	ctx context.Context,
	transport browserStepSessionTransport,
	initial workerStepSessionResult,
	resolver IPResolver,
	policy bughub.BrowserSecurityPolicy,
	attemptID string,
	freeze browserStepEvidenceFreezer,
) (*boundNodeBrowserStepSession, boundBrowserStepSessionInitial, error) {
	if transport == nil || freeze == nil || strings.TrimSpace(attemptID) == "" {
		return nil, boundBrowserStepSessionInitial{}, errors.New("browser step session Host binding is unavailable")
	}
	fail := func(err error) (*boundNodeBrowserStepSession, boundBrowserStepSessionInitial, error) {
		return nil, boundBrowserStepSessionInitial{}, errors.Join(err, transport.Close())
	}
	sanitized, err := validateAndSanitizeWorkerStepSessionResult(ctx, resolver, policy, initial, true, workerStepSessionCommand{})
	if err != nil {
		return fail(err)
	}
	if sanitized.Status == "login_required" {
		return fail(&verifierError{code: "browser_login_required", cause: errors.New("browser step session requires login")})
	}
	if sanitized.Scene == nil {
		return fail(errors.New("browser step session initial Scene is missing"))
	}
	bound := bindBrowserScene(attemptID, sanitized.Scene)
	if bound == nil {
		return fail(errors.New("browser step session initial Scene binding failed"))
	}
	sanitized.Scene = bound
	ref, err := freeze(ctx, 0, sanitized, *bound)
	if err != nil || !validFrozenBrowserStepSceneRef(ref) {
		return fail(errors.Join(errors.New("browser step session initial Scene freeze failed"), err))
	}
	session := &boundNodeBrowserStepSession{
		transport: transport, resolver: resolver, policy: policy, attemptID: attemptID,
		current: *bound, currentRef: ref, freeze: freeze,
	}
	return session, boundBrowserStepSessionInitial{Scene: *bound, SceneRef: ref}, nil
}

func (session *boundNodeBrowserStepSession) Close() error {
	if session == nil || session.transport == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closeLocked()
}

func (session *boundNodeBrowserStepSession) ExecuteBoundBrowserStep(
	ctx context.Context,
	request bughub.BoundBrowserStepExecutionRequest,
) (bughub.BoundBrowserStepExecutionResult, error) {
	if session == nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.New("browser step session is unavailable")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.transport == nil || request.AttemptID != session.attemptID || request.StepNo < 1 ||
		request.Before.SceneID != session.current.SceneID || request.Before.SceneSHA256 != session.current.SceneSHA256 ||
		request.Step.BeforeSceneSHA256 != session.current.SceneSHA256 {
		return bughub.BoundBrowserStepExecutionResult{}, errors.New("browser step session current Scene binding is stale")
	}
	if session.sequence >= 40 {
		return bughub.BoundBrowserStepExecutionResult{}, errors.New("browser step session action budget is exhausted")
	}
	session.sequence++
	passiveChecks := make([]string, 0, len(request.Step.PassiveChecks))
	for _, check := range request.Step.PassiveChecks {
		passiveChecks = append(passiveChecks, check.Kind)
	}
	command := workerStepSessionCommand{
		Command: "step", Sequence: session.sequence, SceneID: request.Before.SceneID,
		ActionID: request.Step.Action.ID, ActionType: request.Step.Action.Action,
		ElementRef: request.Step.ElementRef, PassiveChecks: passiveChecks,
	}
	worker, err := session.transport.Step(ctx, command)
	if err != nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(err, session.closeLocked())
	}
	sanitized, err := validateAndSanitizeWorkerStepSessionResult(ctx, session.resolver, session.policy, worker, false, command)
	if err != nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(err, session.closeLocked())
	}
	if sanitized.Status == "login_required" {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(
			&verifierError{code: "browser_login_required", cause: errors.New("browser step session requires login")},
			session.closeLocked(),
		)
	}
	if sanitized.Scene == nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(errors.New("browser step session after-Scene is missing"), session.closeLocked())
	}
	after := bindBrowserScene(session.attemptID, sanitized.Scene)
	if after == nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(errors.New("browser step session after-Scene binding failed"), session.closeLocked())
	}
	sanitized.Scene = after
	afterRef, err := session.freeze(ctx, request.StepNo, sanitized, *after)
	if err != nil || !validFrozenBrowserStepSceneRef(afterRef) {
		return bughub.BoundBrowserStepExecutionResult{}, errors.Join(errors.New("browser step session after-Scene freeze failed"), err, session.closeLocked())
	}
	session.current, session.currentRef = *after, afterRef
	if sanitized.Status == "scene_stale" {
		return bughub.BoundBrowserStepExecutionResult{
			After: *after, AfterSceneRef: afterRef,
			Effect:           bughub.BrowserStepEffectEvaluation{Outcome: bughub.BrowserEffectNoEffect},
			RecoveryEvidence: bughub.BrowserStepRecoveryEvidence{DispatchState: bughub.BrowserActionDispatchNotDispatched},
		}, nil
	}
	receipt := BrowserStepReceipt{
		ActionID: sanitized.Receipt.ActionID, ActionType: sanitized.Receipt.ActionType,
		TargetElementRef: sanitized.Receipt.TargetElementRef,
		InputPersisted:   sanitized.Receipt.InputPersisted, SelectionPersisted: sanitized.Receipt.SelectionPersisted,
		BlockedCode: sanitized.Receipt.BlockedCode,
	}
	decision := bughub.BrowserDecision{
		Action: &bughub.BrowserDecisionAction{
			ActionID: request.Step.Action.ID, Type: request.Step.Action.Action, ElementRef: request.Step.ElementRef,
		},
		ExpectedEffect: &request.Step.ExpectedEffect,
	}
	evaluation := EvaluateBrowserDecisionEffects(decision, &request.Before, after, receipt)
	return bughub.BoundBrowserStepExecutionResult{
		After: *after, AfterSceneRef: afterRef, Effect: evaluation,
		RecoveryEvidence: browserStepSessionRecoveryEvidence(sanitized.Status, sanitized.Receipt.BlockedCode),
	}, nil
}

func browserStepSessionRecoveryEvidence(status, blockedCode string) bughub.BrowserStepRecoveryEvidence {
	evidence := bughub.BrowserStepRecoveryEvidence{DispatchState: bughub.BrowserActionDispatchUnknown}
	if status != "locator_failed" {
		return evidence
	}
	switch strings.TrimSpace(blockedCode) {
	case "locator_ambiguous", "locator_not_found", "active_surface_not_found", "global_press_forbidden":
		evidence.DispatchState = bughub.BrowserActionDispatchNotDispatched
	}
	return evidence
}

func (session *boundNodeBrowserStepSession) closeLocked() error {
	if session.closed {
		return nil
	}
	session.closed = true
	if session.transport == nil {
		return nil
	}
	return session.transport.Close()
}

func validateAndSanitizeWorkerStepSessionResult(
	ctx context.Context,
	resolver IPResolver,
	policy bughub.BrowserSecurityPolicy,
	result workerStepSessionResult,
	initial bool,
	command workerStepSessionCommand,
) (workerStepSessionResult, error) {
	worker := workerResult{
		Status: result.Status, ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage,
		FailedActionID: result.FailedActionID, FinalURL: result.FinalURL, Title: result.Title,
		LoginOrigin: result.LoginOrigin, FinalScreenshotPath: result.FinalScreenshotPath, AccessibilitySummary: result.AccessibilitySummary,
		Scene: result.Scene, Artifacts: result.Artifacts,
	}
	if err := validateWorkerResultBounds(worker); err != nil {
		return workerStepSessionResult{}, err
	}
	urlWorker := worker
	if urlWorker.Status == "scene_stale" {
		urlWorker.Status = "completed"
	}
	if err := validateWorkerResultURLs(ctx, resolver, policy, urlWorker); err != nil {
		return workerStepSessionResult{}, err
	}
	for _, artifact := range result.Artifacts {
		if artifact.Kind != "screenshot" {
			return workerStepSessionResult{}, errors.New("browser step session returned a non-passive artifact")
		}
		if _, err := normalizeBrowserArtifactPath(artifact.Path); err != nil {
			return workerStepSessionResult{}, err
		}
	}
	if initial {
		if result.Receipt != nil || result.Effect != nil || len(result.Artifacts) != 0 || (result.Status != "completed" && result.Status != "login_required") {
			return workerStepSessionResult{}, errors.New("browser step session initial result shape is invalid")
		}
		if result.Status == "completed" && (result.Scene == nil || result.ErrorCode != "" || result.LoginOrigin != "") {
			return workerStepSessionResult{}, errors.New("browser step session completed initial result is invalid")
		}
		if result.Status == "login_required" && (result.ErrorCode != "browser_login_required" || result.Scene != nil || len(result.AccessibilitySummary) != 0) {
			return workerStepSessionResult{}, errors.New("browser step session login initial result is invalid")
		}
	} else if err := validateWorkerStepResultShape(result, command); err != nil {
		return workerStepSessionResult{}, err
	}
	sanitizedWorker := sanitizeWorkerResult(worker)
	result.ErrorCode, result.ErrorMessage = sanitizedWorker.ErrorCode, sanitizedWorker.ErrorMessage
	result.FailedActionID, result.FinalURL = sanitizedWorker.FailedActionID, sanitizedWorker.FinalURL
	result.Title, result.LoginOrigin, result.FinalScreenshotPath = sanitizedWorker.Title, sanitizedWorker.LoginOrigin, sanitizedWorker.FinalScreenshotPath
	result.AccessibilitySummary, result.Scene, result.Artifacts = sanitizedWorker.AccessibilitySummary, sanitizedWorker.Scene, sanitizedWorker.Artifacts
	if result.Effect != nil {
		result.Effect.ErrorCode = safeVerifierIdentifier(result.Effect.ErrorCode, 128)
		for _, surface := range []*workerStepSessionSurface{result.Effect.BeforeSurface, result.Effect.AfterSurface} {
			if surface != nil {
				surface.Name = redactVerifierText(surface.Name, 512)
			}
		}
	}
	return result, nil
}

func validateAndSanitizeWorkerStepSessionFinalResult(
	ctx context.Context,
	resolver IPResolver,
	policy bughub.BrowserSecurityPolicy,
	result workerStepSessionResult,
) (workerStepSessionResult, error) {
	worker := workerResult{
		Status: result.Status, ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage,
		FailedActionID: result.FailedActionID, FinalURL: result.FinalURL, Title: result.Title,
		LoginOrigin: result.LoginOrigin, FinalScreenshotPath: result.FinalScreenshotPath,
		AccessibilitySummary: result.AccessibilitySummary, Scene: result.Scene, Artifacts: result.Artifacts,
	}
	if result.Receipt != nil || result.Effect != nil {
		return workerStepSessionResult{}, errors.New("browser step session final result contains an action receipt")
	}
	if err := validateWorkerResultBounds(worker); err != nil {
		return workerStepSessionResult{}, err
	}
	if err := validateWorkerResultURLs(ctx, resolver, policy, worker); err != nil {
		return workerStepSessionResult{}, err
	}
	switch result.Status {
	case "completed":
		if result.ErrorCode != "" || result.ErrorMessage != "" || result.FinalScreenshotPath == "" || result.Scene == nil || result.LoginOrigin != "" {
			return workerStepSessionResult{}, errors.New("browser step session completed final result is invalid")
		}
	case "assertion_failed":
		if result.ErrorCode != "assertion_failed" || result.FinalScreenshotPath == "" || result.Scene == nil || result.LoginOrigin != "" {
			return workerStepSessionResult{}, errors.New("browser step session assertion final result is invalid")
		}
	case "login_required":
		if result.ErrorCode != "browser_login_required" || result.FinalScreenshotPath != "" || result.Scene != nil || len(result.AccessibilitySummary) != 0 {
			return workerStepSessionResult{}, errors.New("browser step session login final result is invalid")
		}
	default:
		return workerStepSessionResult{}, errors.New("browser step session final status is invalid")
	}
	sanitized := sanitizeWorkerResult(worker)
	result.ErrorCode, result.ErrorMessage = sanitized.ErrorCode, sanitized.ErrorMessage
	result.FailedActionID, result.FinalURL, result.Title = sanitized.FailedActionID, sanitized.FinalURL, sanitized.Title
	result.LoginOrigin, result.FinalScreenshotPath = sanitized.LoginOrigin, sanitized.FinalScreenshotPath
	result.AccessibilitySummary, result.Scene, result.Artifacts = sanitized.AccessibilitySummary, sanitized.Scene, sanitized.Artifacts
	return result, nil
}

func validateWorkerStepResultShape(result workerStepSessionResult, command workerStepSessionCommand) error {
	switch result.Status {
	case "completed", "locator_failed":
		if result.Scene == nil || result.Receipt == nil || result.Effect == nil ||
			result.Receipt.ActionID != command.ActionID || result.Receipt.ActionType != command.ActionType ||
			result.Receipt.TargetElementRef != command.ElementRef || result.Effect.ActionID != command.ActionID ||
			result.Effect.ActionType != command.ActionType {
			return errors.New("browser step session action receipt binding is invalid")
		}
		if result.Status == "completed" && (result.ErrorCode != "" || result.Receipt.BlockedCode != "" || result.Effect.ErrorCode != "") {
			return errors.New("browser step session completed result contains an error")
		}
		if result.Status == "locator_failed" && (result.ErrorCode == "" || result.FailedActionID != command.ActionID || result.Receipt.BlockedCode != result.ErrorCode || result.Effect.ErrorCode != result.ErrorCode) {
			return errors.New("browser step session blocked result is inconsistent")
		}
	case "scene_stale":
		if result.ErrorCode != "browser_scene_stale" || result.Scene == nil || result.Receipt != nil || result.Effect != nil || len(result.Artifacts) != 0 {
			return errors.New("browser step session stale result shape is invalid")
		}
	case "login_required":
		if result.ErrorCode != "browser_login_required" || result.Scene != nil || result.Receipt != nil || result.Effect != nil || len(result.Artifacts) != 0 {
			return errors.New("browser step session login result shape is invalid")
		}
	default:
		return fmt.Errorf("unsupported browser step session status %q", result.Status)
	}
	if result.Receipt != nil {
		if result.Receipt.BlockedCode != "" && safeVerifierIdentifier(result.Receipt.BlockedCode, 128) != result.Receipt.BlockedCode {
			return errors.New("browser step session receipt error code is invalid")
		}
	}
	if result.Effect != nil {
		if !validWorkerStepEffect(result.Effect) {
			return errors.New("browser step session effect is invalid")
		}
	}
	return nil
}

func validWorkerStepEffect(effect *workerStepSessionEffect) bool {
	if effect == nil {
		return false
	}
	if effect.EffectStatus != "observed" && effect.EffectStatus != "blocked" && effect.EffectStatus != "unobserved" {
		return false
	}
	switch effect.SurfaceTransition {
	case "opened", "closed", "changed", "unchanged", "unobserved":
	default:
		return false
	}
	for _, surface := range []*workerStepSessionSurface{effect.BeforeSurface, effect.AfterSurface} {
		if surface == nil {
			continue
		}
		if !validBrowserSceneSurfaceType(surface.Type) || len(surface.Name) > 512 {
			return false
		}
	}
	return true
}

func validFrozenBrowserStepSceneRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 1024 || filepath.IsAbs(ref) || strings.Contains(ref, "\\") || strings.ContainsRune(ref, '\x00') || verifierCredentialPattern.MatchString(ref) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(ref))
	return clean == ref && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
