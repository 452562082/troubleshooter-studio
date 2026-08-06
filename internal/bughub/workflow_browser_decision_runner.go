package bughub

import (
	"context"
	"errors"
)

// HostBrowserDecisionCoordinatorRunner composes the package-neutral Host
// session opener with the strict loop. The production opener lives in
// browserverify to keep the dependency direction one-way.
type HostBrowserDecisionCoordinatorRunner struct {
	Opener       BrowserDecisionSessionOpener
	Provider     BrowserDecisionProvider
	Transactions BrowserStepTransactionCoordinator
	Recovery     BrowserDecisionRecoveryProvider
	Readiness    BrowserDecisionLoopReadinessProvider
	Budget       BrowserExplorationBudget
}

func (runner HostBrowserDecisionCoordinatorRunner) BrowserDecisionRolloutCapabilities() BrowserDecisionRolloutCapabilities {
	return BrowserDecisionRolloutCapabilities{
		PersistentSession: runner.Opener != nil,
		FrozenSceneStore:  runner.Opener != nil,
		DecisionProvider:  runner.Provider != nil && runner.Transactions.Store != nil,
		RecoveryEvidence:  runner.Recovery != nil,
	}
}

func (runner HostBrowserDecisionCoordinatorRunner) ExecuteBrowserDecision(ctx context.Context, request BrowserDecisionCoordinatorRunRequest) (_ BrowserVerificationResult, returnedErr error) {
	capabilities := runner.BrowserDecisionRolloutCapabilities()
	readiness := request.Readiness
	if readiness == nil {
		readiness = runner.Readiness
	}
	if !capabilities.complete() || runner.Transactions.Store == nil || readiness == nil {
		return BrowserVerificationResult{}, errors.New("browser_decision_runner_unavailable: autonomous browser runner dependencies are incomplete")
	}
	provider := runner.Provider
	recipeVersion := 0
	if request.AutonomousRecipe != nil {
		var err error
		provider, err = newAutonomousRecipeDecisionProvider(*request.AutonomousRecipe, request.Plan, provider)
		if err != nil {
			return BrowserVerificationResult{}, errors.New("browser_autonomous_recipe_invalid: autonomous recipe cannot be replayed")
		}
		recipeVersion = AutonomousValidationRecipeVersion
	}
	budget := runner.Budget
	if budget == (BrowserExplorationBudget{}) {
		budget = DefaultBrowserExplorationBudget()
	}
	exploration, err := NewBrowserExplorationGuard(budget)
	if err != nil {
		return BrowserVerificationResult{}, err
	}
	session, err := runner.Opener.OpenBrowserDecisionSession(ctx, request.Verification)
	if err != nil {
		return BrowserVerificationResult{}, err
	}
	defer func() {
		returnedErr = errors.Join(returnedErr, session.Close())
	}()
	initial, initialRef := session.InitialBrowserDecisionScene()
	plan := request.Plan
	if plan.Version == 0 {
		plan = request.Verification.Plan
	}
	loopResult, loopErr := (BrowserDecisionLoop{
		Provider: provider, Transactions: runner.Transactions, Executor: session,
		Exploration: exploration, Readiness: readiness, SceneLoader: session,
		Recovery: runner.Recovery, Emit: request.Emit,
	}).Run(ctx, BrowserDecisionLoopRequest{
		AttemptID: request.Verification.AttemptID, Plan: plan,
		InitialScene: initial, InitialSceneRef: initialRef, FirstStepNo: 1,
		RecipeVersion: recipeVersion,
	})
	if loopErr != nil && loopResult.Status == "" {
		return BrowserVerificationResult{}, loopErr
	}
	final, err := session.FinishBrowserDecision(ctx)
	if err != nil {
		return BrowserVerificationResult{}, err
	}
	final.autonomousTrace = append([]AutonomousRecipeTraceStep(nil), loopResult.autonomousTrace...)
	final.autonomousScenarioSHA256 = loopResult.ScenarioContractSHA256
	final.autonomousRecipeVersion = loopResult.RecipeVersion
	if loopResult.Status == BrowserDecisionLoopConcluded && loopErr == nil {
		return final, nil
	}
	final.Status = loopResult.Status
	final.ErrorCode = loopResult.ErrorCode
	final.ManualReproductionGate = loopResult.ManualReproductionGate
	if final.ErrorCode == "" {
		switch loopResult.Status {
		case BrowserDecisionLoopAssistance:
			final.ErrorCode = "browser_validation_needs_user_input"
		case BrowserDecisionLoopCapabilityGap:
			final.ErrorCode = "browser_capability_gap"
		case BrowserDecisionLoopRecovery:
			final.ErrorCode = BrowserDecisionLoopRecoveryCode
		case BrowserDecisionLoopUncertain:
			final.ErrorCode = BrowserStepUncertainCode
		default:
			final.ErrorCode = "browser_decision_loop_failed"
		}
	}
	if len(loopResult.Steps) != 0 {
		final.FailedActionID = loopResult.Steps[len(loopResult.Steps)-1].ActionID
	}
	return final, nil
}

var _ BrowserDecisionCoordinatorRunner = HostBrowserDecisionCoordinatorRunner{}
