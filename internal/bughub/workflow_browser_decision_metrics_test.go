package bughub

import (
	"encoding/json"
	"math"
	"testing"
)

func TestFoldBrowserDecisionLoopMetricEvents(t *testing.T) {
	concluded := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{
		Status: BrowserDecisionLoopConcluded,
		Metrics: BrowserDecisionLoopMetrics{
			DecisionCalls: 3, ScenarioSteps: 2, ConfirmedSteps: 2, RecoveryAssessments: 1, RecoveryRetries: 1,
		},
	})
	// Projected events are decoded through encoding/json as float64 values.
	decoded := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopCapabilityGap})
	for key, value := range decoded.Meta {
		if number, ok := value.(int); ok {
			decoded.Meta[key] = float64(number)
		}
	}

	aggregate := FoldBrowserDecisionLoopMetricEvents([]InvestigationEvent{
		{Type: "unrelated"}, concluded, decoded,
	})
	if aggregate.Runs != 2 || aggregate.ConcludedRuns != 1 || aggregate.CapabilityGapRuns != 1 {
		t.Fatalf("unexpected run totals: %#v", aggregate)
	}
	if aggregate.DecisionCalls != 3 || aggregate.ScenarioSteps != 2 || aggregate.RecoveryRetries != 1 {
		t.Fatalf("unexpected counter totals: %#v", aggregate)
	}
	if rate := aggregate.AutonomousCompletionRate(); rate != 0.5 {
		t.Fatalf("unexpected completion rate: %v", rate)
	}
}

func TestFoldBrowserDecisionLoopMetricEventsRejectsWholeMalformedEvent(t *testing.T) {
	valid := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopUncertain})
	malformed := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopConcluded})
	malformed.Meta["scenario_steps"] = -1
	missing := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopConcluded})
	delete(missing.Meta, "decision_calls")
	fractional := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopConcluded})
	fractional.Meta["confirmed_steps"] = 1.5

	aggregate := FoldBrowserDecisionLoopMetricEvents([]InvestigationEvent{malformed, missing, fractional, valid})
	if aggregate.Runs != 1 || aggregate.UncertainRuns != 1 || aggregate.MalformedEvents != 3 {
		t.Fatalf("malformed events should not be partially counted: %#v", aggregate)
	}
}

func TestFoldBrowserDecisionLoopMetricEventsSupportsJSONNumberAndSaturates(t *testing.T) {
	first := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopConcluded})
	second := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopConcluded})
	for _, event := range []*InvestigationEvent{&first, &second} {
		for key := range event.Meta {
			if key != "status" && key != "error_code" {
				event.Meta[key] = json.Number("0")
			}
		}
	}
	first.Meta["decision_calls"] = json.Number("9223372036854775807")
	second.Meta["decision_calls"] = json.Number("1")

	aggregate := FoldBrowserDecisionLoopMetricEvents([]InvestigationEvent{first, second})
	if aggregate.DecisionCalls != math.MaxInt64 || aggregate.Runs != 2 {
		t.Fatalf("metric fold must saturate: %#v", aggregate)
	}
}

func TestBrowserDecisionAggregateMetricsEmptyRate(t *testing.T) {
	if rate := (BrowserDecisionAggregateMetrics{}).AutonomousCompletionRate(); rate != 0 {
		t.Fatalf("empty completion rate must be zero, got %v", rate)
	}
}

func TestBrowserDecisionLoopMetricsConservativelyClassifiesStateActions(t *testing.T) {
	var metrics BrowserDecisionLoopMetrics
	metrics.recordStep(BrowserEffectConfirmed, "wait_for", false, false, false)
	metrics.recordStep(BrowserEffectConfirmed, "click", false, false, false)
	metrics.recordStep(BrowserEffectNoEffect, "click", true, false, false)
	metrics.recordStep(BrowserEffectBlocked, "fill", false, false, false)
	metrics.recordStep(BrowserEffectConfirmed, "select", false, false, true)

	if metrics.StateActions != 4 || metrics.WrongElementActions != 2 || metrics.HighRiskWrongActions != 1 || metrics.UnauthorizedProductionActions != 1 {
		t.Fatalf("unexpected safety counters: %#v", metrics)
	}
	if metrics.ScenarioSteps != 4 || metrics.ExplorationSteps != 1 || metrics.ConfirmedSteps != 3 || metrics.NoEffectSteps != 1 || metrics.BlockedSteps != 1 {
		t.Fatalf("unexpected step counters: %#v", metrics)
	}
}
