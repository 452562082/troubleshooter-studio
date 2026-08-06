package bughub

import (
	"context"
	"errors"
	"fmt"
)

const BrowserDecisionLoopMaxDecisions = 64

const (
	BrowserDecisionLoopConcluded     = "concluded"
	BrowserDecisionLoopAssistance    = "assistance_required"
	BrowserDecisionLoopCapabilityGap = "capability_gap"
	BrowserDecisionLoopRecovery      = "recovery_required"
	BrowserDecisionLoopUncertain     = "uncertain"
)

const (
	BrowserDecisionLoopInvalidCode  = "browser_validator_decision_invalid"
	BrowserDecisionLoopLimitCode    = "browser_decision_loop_exhausted"
	BrowserDecisionLoopRecoveryCode = "browser_step_recovery_required"
)

const (
	BrowserRecoveryRetryNotDispatched      = "worker_proved_not_dispatched"
	BrowserRecoveryRetryObstructionRemoved = "confirmed_obstruction_removed_before_action"
)

type BrowserDecisionLoopObservation struct {
	AttemptID  string
	StepNo     int
	Scene      BrowserScene
	Plan       BrowserPlan
	Candidates []BrowserExplorationCandidate
	History    []BrowserDecisionLoopStep
	Recovery   *BrowserDecisionRecoveryObservation
}

type BrowserDecisionProvider interface {
	DecideBrowserStep(context.Context, BrowserDecisionLoopObservation) ([]byte, error)
}

type BrowserDecisionProviderFunc func(context.Context, BrowserDecisionLoopObservation) ([]byte, error)

func (function BrowserDecisionProviderFunc) DecideBrowserStep(ctx context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
	return function(ctx, observation)
}

type BrowserDecisionLoopReadiness struct {
	ScenarioContractSHA256 string
	FrontendEntryID        string
	IsProduction           bool
	ScenarioReady          bool
	LoginReady             bool
	TestInputsReady        bool
	AuthorizationReady     bool
	DirectTargetAvailable  bool
}

type BrowserDecisionLoopReadinessProvider func(context.Context, BrowserScene) (BrowserDecisionLoopReadiness, error)

// BrowserDecisionRecoveryProvider is a Host-owned policy/evidence adapter. It
// may expose bounded recovery signals or authorize a retry with a stable proof
// code, but page content and Validator output must never implement it.
type BrowserDecisionRecoveryProvider interface {
	AssessBrowserDecisionRecovery(context.Context, BrowserDecisionRecoveryObservation) (BrowserDecisionRecoveryAssessment, error)
}

type BrowserDecisionRecoveryProviderFunc func(context.Context, BrowserDecisionRecoveryObservation) (BrowserDecisionRecoveryAssessment, error)

func (function BrowserDecisionRecoveryProviderFunc) AssessBrowserDecisionRecovery(ctx context.Context, observation BrowserDecisionRecoveryObservation) (BrowserDecisionRecoveryAssessment, error) {
	return function(ctx, observation)
}

type BrowserDecisionRecoveryObservation struct {
	AttemptID     string                      `json:"attempt_id"`
	ActionID      string                      `json:"action_id"`
	ActionType    string                      `json:"action_type"`
	Outcome       string                      `json:"outcome"`
	FailedStepNo  int                         `json:"failed_step_no"`
	RecoverySteps int                         `json:"recovery_steps"`
	Scene         BrowserScene                `json:"-"`
	Evidence      BrowserStepRecoveryEvidence `json:"-"`
}

type BrowserDecisionRecoveryAssessment struct {
	Signals        BrowserRecoverySignals
	RetryScenario  bool
	RetryProofCode string
	Exhausted      bool
}

type browserDecisionRecoveryState struct {
	ActionID      string
	ActionType    string
	Outcome       string
	FailedStepNo  int
	RecoverySteps int
	Evidence      BrowserStepRecoveryEvidence
}

type BrowserDecisionLoopStep struct {
	StepNo        int
	ActionID      string
	ActionType    string
	Exploration   bool
	Replay        bool
	Outcome       string
	BeforeSceneID string
	AfterSceneID  string
}

type BrowserDecisionLoopRequest struct {
	AttemptID       string
	Plan            BrowserPlan
	InitialScene    BrowserScene
	InitialSceneRef string
	FirstStepNo     int
	RecipeVersion   int
}

type BrowserDecisionLoopResult struct {
	Status                 string
	ErrorCode              string
	Decision               BrowserDecision
	Scene                  BrowserScene
	SceneRef               string
	Steps                  []BrowserDecisionLoopStep
	Journal                BrowserDecisionStep
	Metrics                BrowserDecisionLoopMetrics
	ManualReproductionGate *BrowserManualReproductionGateProof
	ScenarioContractSHA256 string
	RecipeVersion          int
	autonomousTrace        []AutonomousRecipeTraceStep
}

// BrowserDecisionLoop is a shadow coordinator. It composes strict Decision
// parsing, deterministic exploration candidates, the durable transaction and
// the persistent single-step Executor, but it is not called by the current
// BrowserPlan default path.
type BrowserDecisionLoop struct {
	Provider     BrowserDecisionProvider
	Transactions BrowserStepTransactionCoordinator
	Executor     BoundBrowserStepExecutor
	Exploration  *BrowserExplorationGuard
	Readiness    BrowserDecisionLoopReadinessProvider
	SceneLoader  BrowserSceneEvidenceLoader
	Recovery     BrowserDecisionRecoveryProvider
	Emit         func(InvestigationEvent)
}

func (loop BrowserDecisionLoop) Run(ctx context.Context, request BrowserDecisionLoopRequest) (result BrowserDecisionLoopResult, runErr error) {
	result = BrowserDecisionLoopResult{
		Scene: request.InitialScene, SceneRef: request.InitialSceneRef,
		Steps: []BrowserDecisionLoopStep{}, RecipeVersion: request.RecipeVersion,
		autonomousTrace: []AutonomousRecipeTraceStep{},
	}
	if loop.Emit != nil {
		defer func() { loop.Emit(BrowserDecisionLoopMetricEvent(result)) }()
	}
	if loop.Provider == nil || loop.Executor == nil || loop.Exploration == nil || loop.Transactions.Store == nil {
		return result, errors.New("browser decision loop dependencies are unavailable")
	}
	if err := validateDurableBrowserPlan(request.Plan); err != nil {
		return result, err
	}
	if err := validateBoundBrowserDecisionScene(request.InitialScene, request.AttemptID, request.InitialScene.SceneID); err != nil ||
		!validBrowserStepSceneReference(request.InitialSceneRef, true) {
		return result, errors.New("browser decision loop initial Scene is invalid")
	}
	stepNo := request.FirstStepNo
	if stepNo == 0 {
		stepNo = 1
	}
	if stepNo < 1 {
		return result, errors.New("browser decision loop first step is invalid")
	}
	nextScenarioAction := 0
	var recovery *browserDecisionRecoveryState
	for decisionCount := 0; decisionCount < BrowserDecisionLoopMaxDecisions; decisionCount++ {
		var candidates []BrowserExplorationCandidate
		var err error
		if recovery != nil {
			result.Metrics.RecoveryAssessments++
			assessment, assessErr := loop.Recovery.AssessBrowserDecisionRecovery(ctx, BrowserDecisionRecoveryObservation{
				AttemptID: request.AttemptID, ActionID: recovery.ActionID, ActionType: recovery.ActionType,
				Outcome: recovery.Outcome, FailedStepNo: recovery.FailedStepNo, RecoverySteps: recovery.RecoverySteps,
				Scene: result.Scene, Evidence: recovery.Evidence,
			})
			if assessErr != nil {
				return result, assessErr
			}
			if err := validateBrowserDecisionRecoveryAssessment(assessment); err != nil {
				result.Status, result.ErrorCode = BrowserDecisionLoopRecovery, BrowserDecisionLoopRecoveryCode
				return result, err
			}
			if assessment.Exhausted {
				result.Status, result.ErrorCode = BrowserDecisionLoopRecovery, BrowserDecisionLoopRecoveryCode
				return result, nil
			}
			if assessment.RetryScenario {
				result.Metrics.RecoveryRetries++
				recovery = nil
				candidates, err = BuildBrowserExplorationCandidates(result.Scene, request.AttemptID)
			} else {
				candidates, err = BuildBrowserRecoveryCandidates(result.Scene, request.AttemptID, assessment.Signals)
			}
		} else {
			candidates, err = BuildBrowserExplorationCandidates(result.Scene, request.AttemptID)
		}
		if err != nil {
			return result, err
		}
		readiness := BrowserDecisionLoopReadiness{}
		if loop.Readiness != nil {
			readiness, err = loop.Readiness(ctx, result.Scene)
			if err != nil {
				return result, err
			}
		}
		readiness.DirectTargetAvailable = readiness.DirectTargetAvailable ||
			browserScenarioDirectTargetAvailable(request.Plan, nextScenarioAction, result.Scene)
		if result.ScenarioContractSHA256 == "" {
			result.ScenarioContractSHA256 = readiness.ScenarioContractSHA256
		}
		candidates, err = loop.Exploration.AvailableCandidates(BrowserExplorationContext{
			AttemptID: request.AttemptID, ScenarioContractSHA256: readiness.ScenarioContractSHA256,
			FrontendEntryID: readiness.FrontendEntryID, Scene: result.Scene, IsProduction: readiness.IsProduction,
			ScenarioReady: readiness.ScenarioReady, LoginReady: readiness.LoginReady,
			TestInputsReady: readiness.TestInputsReady, AuthorizationReady: readiness.AuthorizationReady,
			DirectTargetAvailable: readiness.DirectTargetAvailable,
		}, candidates)
		if err != nil {
			return result, err
		}
		var recoveryObservation *BrowserDecisionRecoveryObservation
		if recovery != nil {
			value := BrowserDecisionRecoveryObservation{
				AttemptID: request.AttemptID, ActionID: recovery.ActionID, ActionType: recovery.ActionType,
				Outcome: recovery.Outcome, FailedStepNo: recovery.FailedStepNo, RecoverySteps: recovery.RecoverySteps,
				Scene: result.Scene, Evidence: recovery.Evidence,
			}
			recoveryObservation = &value
		}
		bindingPlan, err := BuildBrowserDecisionPlanWithExploration(request.Plan, result.Scene, request.AttemptID, candidates)
		if err != nil {
			return result, err
		}
		result.Metrics.DecisionCalls++
		raw, err := loop.Provider.DecideBrowserStep(ctx, BrowserDecisionLoopObservation{
			AttemptID: request.AttemptID, StepNo: stepNo, Scene: result.Scene,
			Plan: bindingPlan, Candidates: append([]BrowserExplorationCandidate(nil), candidates...),
			History: append([]BrowserDecisionLoopStep(nil), result.Steps...), Recovery: recoveryObservation,
		})
		if err != nil {
			return result, err
		}
		decision, err := ParseBrowserDecision(raw, result.Scene.SceneID)
		if err != nil {
			result.Metrics.InvalidDecisions++
			if correctionErr := loop.Exploration.ReserveDecisionCorrection(result.Scene, request.AttemptID); correctionErr != nil {
				result.ErrorCode = BrowserDecisionLoopInvalidCode
				return result, correctionErr
			}
			continue
		}
		result.Decision = decision
		switch decision.Decision {
		case "conclude":
			result.Status = BrowserDecisionLoopConcluded
			return result, nil
		case "assist":
			result.Status = BrowserDecisionLoopAssistance
			return result, nil
		case "capability_gap":
			channelStates := browserManualReproductionChannelStates(decision.ExhaustedChannels)
			safeCandidateExists := len(candidates) != 0 || readiness.DirectTargetAvailable
			proof, proofErr := issueBrowserManualReproductionGateProof(BrowserManualReproductionGateInput{
				Decision: decision, IsProduction: readiness.IsProduction,
				ScenarioReady: readiness.ScenarioReady, LoginReady: readiness.LoginReady,
				TestInputsReady: readiness.TestInputsReady, AuthorizationReady: readiness.AuthorizationReady,
				ExplorationExhausted: !safeCandidateExists, SafeCandidateExists: safeCandidateExists,
				ChannelStates: channelStates,
			}, request.AttemptID, result.Scene, readiness)
			if proofErr != nil {
				return result, proofErr
			}
			if proof == nil {
				result.Metrics.InvalidDecisions++
				if correctionErr := loop.Exploration.ReserveDecisionCorrection(result.Scene, request.AttemptID); correctionErr != nil {
					result.ErrorCode = BrowserDecisionLoopInvalidCode
					return result, correctionErr
				}
				continue
			}
			result.ManualReproductionGate = proof
			result.Status = BrowserDecisionLoopCapabilityGap
			return result, nil
		case "act":
		default:
			result.ErrorCode = BrowserDecisionLoopInvalidCode
			return result, errors.New("browser decision loop branch is invalid")
		}

		candidate, exploration := browserExplorationCandidateByActionID(candidates, decision.Action.ActionID)
		if exploration {
			if _, bindErr := BindBrowserDecisionStep(bindingPlan, decision, result.Scene, request.AttemptID); bindErr != nil {
				result.Metrics.InvalidDecisions++
				if correctionErr := loop.Exploration.ReserveDecisionCorrection(result.Scene, request.AttemptID); correctionErr != nil {
					result.ErrorCode = BrowserDecisionLoopInvalidCode
					return result, correctionErr
				}
				continue
			}
			admission, reserveErr := loop.Exploration.ReserveExploration(BrowserExplorationContext{
				AttemptID: request.AttemptID, ScenarioContractSHA256: readiness.ScenarioContractSHA256,
				FrontendEntryID: readiness.FrontendEntryID, Scene: result.Scene, IsProduction: readiness.IsProduction,
				ScenarioReady: readiness.ScenarioReady, LoginReady: readiness.LoginReady,
				TestInputsReady: readiness.TestInputsReady, AuthorizationReady: readiness.AuthorizationReady,
				DirectTargetAvailable: readiness.DirectTargetAvailable,
			}, candidate)
			if reserveErr != nil {
				result.ErrorCode = BrowserExplorationErrorCode(reserveErr)
				return result, reserveErr
			}
			transaction, runErr := loop.Transactions.Run(
				ctx, loop.Executor, bindingPlan, decision, result.Scene, request.AttemptID, stepNo, result.SceneRef,
			)
			result.Journal = transaction.Journal
			if runErr != nil {
				if transaction.DidExecute || transaction.Interrupted {
					_ = loop.Exploration.RecordOutcome(admission, BrowserEffectUncertain)
					result.Metrics.recordStep(BrowserEffectUncertain, candidate.Action.Action, true, false, readiness.IsProduction)
					result.Status, result.ErrorCode = BrowserDecisionLoopUncertain, BrowserStepUncertainCode
				}
				return result, runErr
			}
			after, afterRef, outcome, replay, recoveryErr := loop.resolveBrowserStepResult(ctx, request.AttemptID, stepNo, transaction)
			if recoveryErr != nil {
				result.Status, result.ErrorCode = BrowserDecisionLoopRecovery, BrowserDecisionLoopRecoveryCode
				return result, recoveryErr
			}
			if err := loop.Exploration.RecordOutcome(admission, outcome); err != nil {
				return result, err
			}
			result.Steps = append(result.Steps, BrowserDecisionLoopStep{
				StepNo: stepNo, ActionID: decision.Action.ActionID, ActionType: decision.Action.Type, Exploration: true,
				Replay: replay, Outcome: outcome, BeforeSceneID: result.Scene.SceneID,
				AfterSceneID: after.SceneID,
			})
			result.autonomousTrace = append(result.autonomousTrace, AutonomousRecipeTraceStep{
				Before: result.Scene, Bound: transaction.Bound, Outcome: outcome, Exploration: true,
				FrontendEntryID: readiness.FrontendEntryID,
			})
			result.Metrics.recordStep(outcome, candidate.Action.Action, true, replay, readiness.IsProduction)
			result.Scene, result.SceneRef = after, afterRef
			stepNo++
			if recovery != nil {
				recovery.RecoverySteps++
			}
			if outcome == BrowserEffectAmbiguous || outcome == BrowserEffectUncertain {
				result.Status, result.ErrorCode = BrowserDecisionLoopUncertain, BrowserStepUncertainCode
				return result, nil
			}
			continue
		}

		if recovery != nil || nextScenarioAction >= len(request.Plan.Actions) || request.Plan.Actions[nextScenarioAction].ID != decision.Action.ActionID {
			result.Metrics.InvalidDecisions++
			if correctionErr := loop.Exploration.ReserveDecisionCorrection(result.Scene, request.AttemptID); correctionErr != nil {
				result.ErrorCode = BrowserDecisionLoopInvalidCode
				return result, correctionErr
			}
			continue
		}
		scenarioAction := request.Plan.Actions[nextScenarioAction]
		if _, bindErr := BindBrowserDecisionStep(request.Plan, decision, result.Scene, request.AttemptID); bindErr != nil {
			result.Metrics.InvalidDecisions++
			if correctionErr := loop.Exploration.ReserveDecisionCorrection(result.Scene, request.AttemptID); correctionErr != nil {
				result.ErrorCode = BrowserDecisionLoopInvalidCode
				return result, correctionErr
			}
			continue
		}
		if err := loop.Exploration.ReserveScenarioAction(scenarioAction); err != nil {
			result.ErrorCode = BrowserExplorationErrorCode(err)
			return result, err
		}
		transaction, runErr := loop.Transactions.Run(
			ctx, loop.Executor, request.Plan, decision, result.Scene, request.AttemptID, stepNo, result.SceneRef,
		)
		result.Journal = transaction.Journal
		if runErr != nil {
			if transaction.DidExecute || transaction.Interrupted {
				result.Metrics.recordStep(BrowserEffectUncertain, scenarioAction.Action, false, false, readiness.IsProduction)
				result.Status, result.ErrorCode = BrowserDecisionLoopUncertain, BrowserStepUncertainCode
			}
			return result, runErr
		}
		after, afterRef, outcome, replay, recoveryErr := loop.resolveBrowserStepResult(ctx, request.AttemptID, stepNo, transaction)
		if recoveryErr != nil {
			result.Status, result.ErrorCode = BrowserDecisionLoopRecovery, BrowserDecisionLoopRecoveryCode
			return result, recoveryErr
		}
		result.Steps = append(result.Steps, BrowserDecisionLoopStep{
			StepNo: stepNo, ActionID: decision.Action.ActionID, ActionType: decision.Action.Type,
			Replay: replay, Outcome: outcome, BeforeSceneID: result.Scene.SceneID,
			AfterSceneID: after.SceneID,
		})
		result.autonomousTrace = append(result.autonomousTrace, AutonomousRecipeTraceStep{
			Before: result.Scene, Bound: transaction.Bound, Outcome: outcome,
			FrontendEntryID: readiness.FrontendEntryID,
		})
		result.Metrics.recordStep(outcome, scenarioAction.Action, false, replay, readiness.IsProduction)
		result.Scene, result.SceneRef = after, afterRef
		stepNo++
		if outcome != BrowserEffectConfirmed {
			if loop.Recovery != nil && (outcome == BrowserEffectNoEffect || outcome == BrowserEffectBlocked) {
				recovery = &browserDecisionRecoveryState{
					ActionID: scenarioAction.ID, ActionType: scenarioAction.Action, Outcome: outcome, FailedStepNo: stepNo - 1,
					Evidence: transaction.Execution.RecoveryEvidence,
				}
				continue
			}
			result.Status, result.ErrorCode = BrowserDecisionLoopRecovery, BrowserDecisionLoopRecoveryCode
			return result, nil
		}
		nextScenarioAction++
	}
	result.ErrorCode = BrowserDecisionLoopLimitCode
	return result, fmt.Errorf("browser decision loop exceeded %d decisions", BrowserDecisionLoopMaxDecisions)
}

func validateBrowserDecisionRecoveryAssessment(assessment BrowserDecisionRecoveryAssessment) error {
	if assessment.Exhausted {
		if assessment.RetryScenario || assessment.RetryProofCode != "" || len(assessment.Signals.WaitElementRefs) != 0 || assessment.Signals.ConfirmedObstructionSurfaceRef != "" {
			return errors.New("browser decision recovery exhausted assessment contains actions")
		}
		return nil
	}
	if assessment.RetryScenario {
		if len(assessment.Signals.WaitElementRefs) != 0 || assessment.Signals.ConfirmedObstructionSurfaceRef != "" {
			return errors.New("browser decision recovery retry also contains candidates")
		}
		switch assessment.RetryProofCode {
		case BrowserRecoveryRetryNotDispatched, BrowserRecoveryRetryObstructionRemoved:
			return nil
		default:
			return errors.New("browser decision recovery retry proof is invalid")
		}
	}
	if assessment.RetryProofCode != "" || (len(assessment.Signals.WaitElementRefs) == 0 && assessment.Signals.ConfirmedObstructionSurfaceRef == "") {
		return errors.New("browser decision recovery assessment has no safe next step")
	}
	return nil
}

func (loop BrowserDecisionLoop) resolveBrowserStepResult(
	ctx context.Context,
	attemptID string,
	stepNo int,
	transaction BrowserStepTransactionRunResult,
) (BrowserScene, string, string, bool, error) {
	if transaction.DidExecute {
		if transaction.Execution.After.SceneID == "" || !validBrowserStepSceneReference(transaction.Execution.AfterSceneRef, true) {
			return BrowserScene{}, "", "", false, errors.New("browser decision step execution did not freeze its after-Scene")
		}
		return transaction.Execution.After, transaction.Execution.AfterSceneRef, transaction.Execution.Effect.Outcome, false, nil
	}
	journal := transaction.Journal
	if !journal.Status.terminal() || journal.AttemptID != attemptID || journal.StepNo != stepNo ||
		!validBrowserStepSceneReference(journal.AfterSceneRef, true) {
		return BrowserScene{}, "", "", true, errors.New("browser decision step journal cannot be recovered safely")
	}
	if loop.SceneLoader == nil {
		return BrowserScene{}, "", "", true, errors.New("browser decision step requires a frozen Scene loader")
	}
	content, err := loop.SceneLoader.LoadFrozenBrowserScene(ctx, attemptID, journal.AfterSceneRef)
	if err != nil {
		return BrowserScene{}, "", "", true, fmt.Errorf("load frozen browser Scene: %w", err)
	}
	after, err := DecodeFrozenBrowserScene(content, attemptID)
	if err != nil {
		return BrowserScene{}, "", "", true, err
	}
	outcome, err := browserDecisionJournalOutcome(journal)
	if err != nil {
		return BrowserScene{}, "", "", true, err
	}
	return after, journal.AfterSceneRef, outcome, true, nil
}

func browserDecisionJournalOutcome(journal BrowserDecisionStep) (string, error) {
	switch journal.Status {
	case BrowserDecisionStepConfirmed:
		if journal.EffectCode == BrowserStepEffectConfirmedCode {
			return BrowserEffectConfirmed, nil
		}
	case BrowserDecisionStepNoEffect:
		if journal.EffectCode == BrowserStepNoEffectCode {
			return BrowserEffectNoEffect, nil
		}
	case BrowserDecisionStepBlocked:
		if validBrowserStepEffectCode(journal.EffectCode) {
			return BrowserEffectBlocked, nil
		}
	case BrowserDecisionStepAmbiguous:
		if journal.EffectCode == BrowserStepAmbiguousCode {
			return BrowserEffectAmbiguous, nil
		}
	case BrowserDecisionStepUncertain:
		if journal.EffectCode == BrowserStepUncertainCode {
			return BrowserEffectUncertain, nil
		}
	}
	return "", errors.New("browser decision step journal outcome is invalid")
}

func browserExplorationCandidateByActionID(candidates []BrowserExplorationCandidate, actionID string) (BrowserExplorationCandidate, bool) {
	var found BrowserExplorationCandidate
	count := 0
	for _, candidate := range candidates {
		if candidate.Action.ID == actionID {
			found = candidate
			count++
		}
	}
	return found, count == 1
}
