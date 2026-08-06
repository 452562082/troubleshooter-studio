package bughub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BrowserStepTransactionCoordinator composes current-scene binding with the
// durable step journal. Browser execution is intentionally outside this type:
// callers must persist prepared and executing before invoking the Worker.
type BrowserStepTransactionCoordinator struct {
	Store BrowserDecisionStepStore
}

type BoundBrowserStepExecutionRequest struct {
	AttemptID string
	StepNo    int
	Before    BrowserScene
	Step      BoundBrowserDecisionStep
}

type BoundBrowserStepExecutionResult struct {
	After            BrowserScene
	AfterSceneRef    string
	Effect           BrowserStepEffectEvaluation
	RecoveryEvidence BrowserStepRecoveryEvidence
}

type BoundBrowserStepExecutor interface {
	ExecuteBoundBrowserStep(context.Context, BoundBrowserStepExecutionRequest) (BoundBrowserStepExecutionResult, error)
}

type BrowserStepTransactionRunResult struct {
	Bound       BoundBrowserDecisionStep
	Journal     BrowserDecisionStep
	Execution   BoundBrowserStepExecutionResult
	Replay      bool
	DidExecute  bool
	Interrupted bool
}

func (c BrowserStepTransactionCoordinator) Prepare(
	ctx context.Context,
	plan BrowserPlan,
	decision BrowserDecision,
	scene BrowserScene,
	attemptID string,
	stepNo int,
	beforeSceneRef string,
) (BoundBrowserDecisionStep, BrowserDecisionStep, bool, error) {
	if c.Store == nil {
		return BoundBrowserDecisionStep{}, BrowserDecisionStep{}, false, errors.New("browser decision step store is unavailable")
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, attemptID)
	if err != nil {
		return BoundBrowserDecisionStep{}, BrowserDecisionStep{}, false, err
	}
	stored, replay, err := c.Store.PrepareBrowserDecisionStep(ctx, BrowserDecisionStep{
		AttemptID:         attemptID,
		StepNo:            stepNo,
		SceneSHA256:       bound.BeforeSceneSHA256,
		DecisionSHA256:    bound.DecisionSHA256,
		ActionFingerprint: bound.ActionFingerprint,
		Status:            BrowserDecisionStepPrepared,
		BeforeSceneRef:    strings.TrimSpace(beforeSceneRef),
	})
	if err != nil {
		return BoundBrowserDecisionStep{}, BrowserDecisionStep{}, false, err
	}
	return bound, stored, replay, nil
}

func (c BrowserStepTransactionCoordinator) Start(ctx context.Context, bound BoundBrowserDecisionStep, attemptID string, stepNo int) (BrowserDecisionStep, bool, error) {
	if c.Store == nil || !validLowerSHA256(bound.DecisionSHA256) {
		return BrowserDecisionStep{}, false, errors.New("browser decision step start is unavailable")
	}
	return c.Store.StartBrowserDecisionStep(ctx, attemptID, stepNo, bound.DecisionSHA256)
}

func (c BrowserStepTransactionCoordinator) Complete(
	ctx context.Context,
	attemptID string,
	stepNo int,
	evaluation BrowserStepEffectEvaluation,
	afterSceneRef string,
) (BrowserDecisionStep, bool, error) {
	if c.Store == nil {
		return BrowserDecisionStep{}, false, errors.New("browser decision step store is unavailable")
	}
	status, effectCode, err := browserDecisionStepTerminalState(evaluation)
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	return c.Store.CompleteBrowserDecisionStep(ctx, attemptID, stepNo, status, effectCode, strings.TrimSpace(afterSceneRef))
}

// Run enforces write-ahead execution. If Start is an idempotent replay, the
// action might already have reached the browser, so Run marks it uncertain and
// never calls the executor again. A returned executor error follows the same
// rule. This is the crash-safety boundary used by the persistent Worker.
func (c BrowserStepTransactionCoordinator) Run(
	ctx context.Context,
	executor BoundBrowserStepExecutor,
	plan BrowserPlan,
	decision BrowserDecision,
	before BrowserScene,
	attemptID string,
	stepNo int,
	beforeSceneRef string,
) (BrowserStepTransactionRunResult, error) {
	var result BrowserStepTransactionRunResult
	if executor == nil {
		return result, errors.New("bound browser step executor is unavailable")
	}
	bound, journal, prepareReplay, err := c.Prepare(ctx, plan, decision, before, attemptID, stepNo, beforeSceneRef)
	result.Bound, result.Journal, result.Replay = bound, journal, prepareReplay
	if err != nil {
		return result, err
	}
	if journal.Status.terminal() {
		return result, nil
	}
	journal, startReplay, err := c.Start(ctx, bound, attemptID, stepNo)
	result.Journal = journal
	result.Replay = result.Replay || startReplay
	if err != nil {
		return result, err
	}
	if startReplay {
		uncertain, completeErr := c.persistUncertain(ctx, attemptID, stepNo)
		result.Journal = uncertain
		result.Interrupted = true
		return result, completeErr
	}

	result.DidExecute = true
	execution, executeErr := executor.ExecuteBoundBrowserStep(ctx, BoundBrowserStepExecutionRequest{
		AttemptID: attemptID, StepNo: stepNo, Before: before, Step: bound,
	})
	result.Execution = execution
	if executeErr != nil {
		uncertain, completeErr := c.persistUncertain(ctx, attemptID, stepNo)
		result.Journal = uncertain
		result.Interrupted = true
		return result, errors.Join(executeErr, completeErr)
	}
	if err := validateBoundBrowserDecisionScene(execution.After, attemptID, execution.After.SceneID); err != nil {
		uncertain, completeErr := c.persistUncertain(ctx, attemptID, stepNo)
		result.Journal = uncertain
		result.Interrupted = true
		return result, errors.Join(fmt.Errorf("validate browser step after-scene: %w", err), completeErr)
	}
	completed, completeReplay, err := c.Complete(ctx, attemptID, stepNo, execution.Effect, execution.AfterSceneRef)
	result.Journal = completed
	result.Replay = result.Replay || completeReplay
	if err != nil {
		return result, err
	}
	return result, nil
}

func (c BrowserStepTransactionCoordinator) persistUncertain(ctx context.Context, attemptID string, stepNo int) (BrowserDecisionStep, error) {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	step, _, err := c.Complete(recoveryCtx, attemptID, stepNo, BrowserStepEffectEvaluation{Outcome: BrowserEffectUncertain}, "")
	return step, err
}

func browserDecisionStepTerminalState(evaluation BrowserStepEffectEvaluation) (BrowserDecisionStepStatus, string, error) {
	switch evaluation.Outcome {
	case BrowserEffectConfirmed:
		return BrowserDecisionStepConfirmed, BrowserStepEffectConfirmedCode, nil
	case BrowserEffectNoEffect:
		return BrowserDecisionStepNoEffect, BrowserStepNoEffectCode, nil
	case BrowserEffectBlocked:
		code := strings.TrimSpace(evaluation.BlockCode)
		if !strings.HasPrefix(code, "browser_") {
			code = "browser_" + code
		}
		if !validBrowserStepEffectCode(code) {
			code = "browser_action_blocked"
		}
		return BrowserDecisionStepBlocked, code, nil
	case BrowserEffectAmbiguous:
		return BrowserDecisionStepAmbiguous, BrowserStepAmbiguousCode, nil
	case BrowserEffectUncertain:
		return BrowserDecisionStepUncertain, BrowserStepUncertainCode, nil
	default:
		return "", "", errors.New("browser decision step effect outcome is invalid")
	}
}
