package bughub

import (
	"encoding/json"
	"math"
)

// BrowserDecisionLoopMetrics contains only bounded counters and stable result
// codes. It is safe to emit for shadow/gray comparisons because it contains no
// page content, URLs, action values, model output, or artifact references.
type BrowserDecisionLoopMetrics struct {
	DecisionCalls                 int
	InvalidDecisions              int
	ScenarioSteps                 int
	ExplorationSteps              int
	ReplayedSteps                 int
	ConfirmedSteps                int
	NoEffectSteps                 int
	BlockedSteps                  int
	AmbiguousSteps                int
	UncertainSteps                int
	RecoveryAssessments           int
	RecoveryRetries               int
	StateActions                  int
	WrongElementActions           int
	HighRiskWrongActions          int
	UnauthorizedProductionActions int
}

func (metrics *BrowserDecisionLoopMetrics) recordStep(outcome, actionType string, exploration, replay, isProduction bool) {
	if metrics == nil {
		return
	}
	if exploration {
		metrics.ExplorationSteps++
	} else {
		metrics.ScenarioSteps++
	}
	if replay {
		metrics.ReplayedSteps++
	}
	if browserActionConsumesStateBudget(actionType) {
		metrics.StateActions++
		if isProduction {
			metrics.UnauthorizedProductionActions++
		}
		if outcome != BrowserEffectConfirmed {
			// Conservatively classify every unconfirmed state action as a
			// wrong-element action. A scenario action is also treated as high
			// risk. This may fail a gate, but cannot manufacture a safety pass.
			metrics.WrongElementActions++
			if !exploration {
				metrics.HighRiskWrongActions++
			}
		}
	}
	switch outcome {
	case BrowserEffectConfirmed:
		metrics.ConfirmedSteps++
	case BrowserEffectNoEffect:
		metrics.NoEffectSteps++
	case BrowserEffectBlocked:
		metrics.BlockedSteps++
	case BrowserEffectAmbiguous:
		metrics.AmbiguousSteps++
	case BrowserEffectUncertain:
		metrics.UncertainSteps++
	}
}

func BrowserDecisionLoopMetricEvent(result BrowserDecisionLoopResult) InvestigationEvent {
	return InvestigationEvent{
		Type:    "browser_decision_loop_metrics",
		Message: "浏览器自主验证单步循环指标已记录",
		Meta: map[string]any{
			"status": result.Status, "error_code": result.ErrorCode,
			"decision_calls": result.Metrics.DecisionCalls, "invalid_decisions": result.Metrics.InvalidDecisions,
			"scenario_steps": result.Metrics.ScenarioSteps, "exploration_steps": result.Metrics.ExplorationSteps,
			"replayed_steps": result.Metrics.ReplayedSteps, "confirmed_steps": result.Metrics.ConfirmedSteps,
			"no_effect_steps": result.Metrics.NoEffectSteps, "blocked_steps": result.Metrics.BlockedSteps,
			"ambiguous_steps": result.Metrics.AmbiguousSteps, "uncertain_steps": result.Metrics.UncertainSteps,
			"recovery_assessments": result.Metrics.RecoveryAssessments, "recovery_retries": result.Metrics.RecoveryRetries,
			"state_actions": result.Metrics.StateActions, "wrong_element_actions": result.Metrics.WrongElementActions,
			"high_risk_wrong_actions":         result.Metrics.HighRiskWrongActions,
			"unauthorized_production_actions": result.Metrics.UnauthorizedProductionActions,
			"scenario_contract_sha256":        result.ScenarioContractSHA256, "recipe_version": result.RecipeVersion,
		},
	}
}

// BrowserDecisionAggregateMetrics folds the projected loop events used by
// benchmark and gray-run reports. It is intentionally separate from
// WorkflowMetrics: InvestigationEvent is a compatibility/event-sink projection,
// not part of the durable IncidentCase transition history.
type BrowserDecisionAggregateMetrics struct {
	Runs                          int64
	ConcludedRuns                 int64
	AssistanceRequiredRuns        int64
	CapabilityGapRuns             int64
	RecoveryRequiredRuns          int64
	UncertainRuns                 int64
	UnknownStatusRuns             int64
	MalformedEvents               int64
	DecisionCalls                 int64
	InvalidDecisions              int64
	ScenarioSteps                 int64
	ExplorationSteps              int64
	ReplayedSteps                 int64
	ConfirmedSteps                int64
	NoEffectSteps                 int64
	BlockedSteps                  int64
	AmbiguousSteps                int64
	UncertainSteps                int64
	RecoveryAssessments           int64
	RecoveryRetries               int64
	StateActions                  int64
	WrongElementActions           int64
	HighRiskWrongActions          int64
	UnauthorizedProductionActions int64
}

func (metrics BrowserDecisionAggregateMetrics) AutonomousCompletionRate() float64 {
	if metrics.Runs == 0 {
		return 0
	}
	return float64(metrics.ConcludedRuns) / float64(metrics.Runs)
}

// FoldBrowserDecisionLoopMetricEvents ignores unrelated events and rejects a
// malformed metrics event as one unit. It never partially adds untrusted
// counters, which keeps reports deterministic after JSON round trips.
func FoldBrowserDecisionLoopMetricEvents(events []InvestigationEvent) BrowserDecisionAggregateMetrics {
	var aggregate BrowserDecisionAggregateMetrics
	for _, event := range events {
		if event.Type != "browser_decision_loop_metrics" {
			continue
		}
		status, ok := event.Meta["status"].(string)
		if !ok || status == "" {
			aggregate.MalformedEvents = saturatingMetricAdd(aggregate.MalformedEvents, 1)
			continue
		}
		keys := []string{
			"decision_calls", "invalid_decisions", "scenario_steps", "exploration_steps",
			"replayed_steps", "confirmed_steps", "no_effect_steps", "blocked_steps",
			"ambiguous_steps", "uncertain_steps", "recovery_assessments", "recovery_retries",
			"state_actions", "wrong_element_actions", "high_risk_wrong_actions", "unauthorized_production_actions",
		}
		values := make([]int64, len(keys))
		valid := true
		for index, key := range keys {
			values[index], valid = browserDecisionMetricInt(event.Meta[key])
			if !valid {
				break
			}
		}
		if !valid {
			aggregate.MalformedEvents = saturatingMetricAdd(aggregate.MalformedEvents, 1)
			continue
		}
		aggregate.Runs = saturatingMetricAdd(aggregate.Runs, 1)
		switch status {
		case BrowserDecisionLoopConcluded:
			aggregate.ConcludedRuns = saturatingMetricAdd(aggregate.ConcludedRuns, 1)
		case BrowserDecisionLoopAssistance:
			aggregate.AssistanceRequiredRuns = saturatingMetricAdd(aggregate.AssistanceRequiredRuns, 1)
		case BrowserDecisionLoopCapabilityGap:
			aggregate.CapabilityGapRuns = saturatingMetricAdd(aggregate.CapabilityGapRuns, 1)
		case BrowserDecisionLoopRecovery:
			aggregate.RecoveryRequiredRuns = saturatingMetricAdd(aggregate.RecoveryRequiredRuns, 1)
		case BrowserDecisionLoopUncertain:
			aggregate.UncertainRuns = saturatingMetricAdd(aggregate.UncertainRuns, 1)
		default:
			aggregate.UnknownStatusRuns = saturatingMetricAdd(aggregate.UnknownStatusRuns, 1)
		}
		destinations := []*int64{
			&aggregate.DecisionCalls, &aggregate.InvalidDecisions, &aggregate.ScenarioSteps, &aggregate.ExplorationSteps,
			&aggregate.ReplayedSteps, &aggregate.ConfirmedSteps, &aggregate.NoEffectSteps, &aggregate.BlockedSteps,
			&aggregate.AmbiguousSteps, &aggregate.UncertainSteps, &aggregate.RecoveryAssessments, &aggregate.RecoveryRetries,
			&aggregate.StateActions, &aggregate.WrongElementActions, &aggregate.HighRiskWrongActions, &aggregate.UnauthorizedProductionActions,
		}
		for index := range values {
			*destinations[index] = saturatingMetricAdd(*destinations[index], values[index])
		}
	}
	return aggregate
}

func browserDecisionMetricInt(value any) (int64, bool) {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int32:
		number = int64(typed)
	case int64:
		number = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		number = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		number = int64(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || typed > math.MaxInt64 || math.Trunc(typed) != typed {
			return 0, false
		}
		number = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, number >= 0
}

func saturatingMetricAdd(left, right int64) int64 {
	if right > math.MaxInt64-left {
		return math.MaxInt64
	}
	return left + right
}
