package bughub

import (
	"fmt"
	"math"
	"testing"
)

func TestEvaluateBrowserDecisionBenchmarkPassesAllProposedGates(t *testing.T) {
	var samples []BrowserDecisionBenchmarkSample
	for index := 0; index < 20; index++ {
		samples = append(samples, benchmarkSample(fmt.Sprintf("ordinary-%d", index), BrowserDecisionBenchmarkOrdinary))
	}
	for index := 0; index < 20; index++ {
		sample := benchmarkSample(fmt.Sprintf("recipe-%d", index), BrowserDecisionBenchmarkRecipeV3)
		samples = append(samples, sample)
	}
	for index := 0; index < 20; index++ {
		sample := benchmarkSample(fmt.Sprintf("locator-%d", index), BrowserDecisionBenchmarkLocatorCorpus)
		if index < 10 {
			sample.BaselineAutonomousCompleted = true
		}
		samples = append(samples, sample)
	}
	for index := 0; index < 3; index++ {
		samples[index].RepeatGroup = "ordinary-repeat"
	}

	report, err := EvaluateBrowserDecisionBenchmark(samples, DefaultBrowserDecisionBenchmarkThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || len(report.Failures) != 0 || report.LocatorCompletionUplift() != .5 || report.ConclusionConsistencyRate() != 1 {
		t.Fatalf("expected benchmark to pass: %#v", report)
	}
}

func TestEvaluateBrowserDecisionBenchmarkFailsClosedOnCoverageAndSafety(t *testing.T) {
	sample := benchmarkSample("ordinary-1", BrowserDecisionBenchmarkOrdinary)
	sample.CurrentEvidence = false
	sample.ManualReproductionSelected = true
	sample.LocatorTriggeredManual = true
	sample.WrongElementActions = 1
	sample.HighRiskWrongActions = 1
	sample.UnauthorizedProductionActions = 1

	report, err := EvaluateBrowserDecisionBenchmark([]BrowserDecisionBenchmarkSample{sample}, DefaultBrowserDecisionBenchmarkThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.ManualSelectionsWithoutGap != 1 || report.EvidenceFreeSuccessfulConclusions != 1 {
		t.Fatalf("unsafe or incomplete corpus must fail: %#v", report)
	}
	for _, expected := range []string{
		"locator_corpus_missing", "recipe_v3_corpus_missing", "repeat_consistency_corpus_missing",
		"manual_selection_without_capability_gap", "locator_triggered_manual_reproduction",
		"successful_conclusion_without_current_evidence", "unauthorized_production_action", "high_risk_wrong_action",
		"wrong_element_action_rate_above_threshold",
	} {
		if !containsBenchmarkFailure(report.Failures, expected) {
			t.Fatalf("missing failure %q in %#v", expected, report.Failures)
		}
	}
}

func TestEvaluateBrowserDecisionBenchmarkRejectsMalformedInput(t *testing.T) {
	tests := []BrowserDecisionBenchmarkSample{
		{},
		{ID: "bad-suite", Suite: "unknown"},
		{ID: "missing-conclusion", Suite: BrowserDecisionBenchmarkOrdinary, AutonomousCompleted: true},
		{ID: "bad-counter", Suite: BrowserDecisionBenchmarkOrdinary, StateActions: 1, WrongElementActions: 2},
	}
	for _, sample := range tests {
		if _, err := EvaluateBrowserDecisionBenchmark([]BrowserDecisionBenchmarkSample{sample}, DefaultBrowserDecisionBenchmarkThresholds()); err == nil {
			t.Fatalf("expected malformed sample to fail: %#v", sample)
		}
	}
	if _, err := EvaluateBrowserDecisionBenchmark(nil, BrowserDecisionBenchmarkThresholds{MinimumOrdinaryCompletionRate: 2}); err == nil {
		t.Fatal("expected malformed thresholds to fail")
	}
	if _, err := EvaluateBrowserDecisionBenchmark(nil, BrowserDecisionBenchmarkThresholds{MinimumOrdinaryCompletionRate: math.NaN()}); err == nil {
		t.Fatal("expected NaN threshold to fail")
	}
	duplicate := benchmarkSample("duplicate", BrowserDecisionBenchmarkOrdinary)
	if _, err := EvaluateBrowserDecisionBenchmark([]BrowserDecisionBenchmarkSample{duplicate, duplicate}, DefaultBrowserDecisionBenchmarkThresholds()); err == nil {
		t.Fatal("expected duplicate sample to fail")
	}
}

func benchmarkSample(id, suite string) BrowserDecisionBenchmarkSample {
	return BrowserDecisionBenchmarkSample{
		ID: id, Suite: suite, AutonomousCompleted: true, Conclusion: "reproduced", CurrentEvidence: true, StateActions: 10,
	}
}

func containsBenchmarkFailure(failures []string, expected string) bool {
	for _, failure := range failures {
		if failure == expected {
			return true
		}
	}
	return false
}
