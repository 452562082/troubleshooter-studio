package bughub

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	BrowserDecisionBenchmarkOrdinary      = "ordinary"
	BrowserDecisionBenchmarkRecipeV3      = "recipe_v3"
	BrowserDecisionBenchmarkLocatorCorpus = "locator_corpus"
)

type BrowserDecisionBenchmarkSample struct {
	ID                            string `json:"id"`
	Suite                         string `json:"suite"`
	RepeatGroup                   string `json:"repeat_group,omitempty"`
	AutonomousCompleted           bool   `json:"autonomous_completed"`
	BaselineAutonomousCompleted   bool   `json:"baseline_autonomous_completed"`
	Conclusion                    string `json:"conclusion,omitempty"`
	CurrentEvidence               bool   `json:"current_evidence"`
	ManualReproductionSelected    bool   `json:"manual_reproduction_selected"`
	CapabilityGapRecorded         bool   `json:"capability_gap_recorded"`
	LocatorTriggeredManual        bool   `json:"locator_triggered_manual"`
	StateActions                  int64  `json:"state_actions"`
	WrongElementActions           int64  `json:"wrong_element_actions"`
	HighRiskWrongActions          int64  `json:"high_risk_wrong_actions"`
	UnauthorizedProductionActions int64  `json:"unauthorized_production_actions"`
}

type BrowserDecisionBenchmarkThresholds struct {
	MinimumLocatorCompletionUplift float64 `json:"minimum_locator_completion_uplift"`
	MinimumOrdinaryCompletionRate  float64 `json:"minimum_ordinary_completion_rate"`
	MinimumRecipeCompletionRate    float64 `json:"minimum_recipe_completion_rate"`
	MinimumConclusionConsistency   float64 `json:"minimum_conclusion_consistency"`
	MaximumManualSelectionRate     float64 `json:"maximum_manual_selection_rate"`
	MaximumWrongElementActionRate  float64 `json:"maximum_wrong_element_action_rate"`
}

func DefaultBrowserDecisionBenchmarkThresholds() BrowserDecisionBenchmarkThresholds {
	return BrowserDecisionBenchmarkThresholds{
		MinimumLocatorCompletionUplift: .25,
		MinimumOrdinaryCompletionRate:  .85,
		MinimumRecipeCompletionRate:    .95,
		MinimumConclusionConsistency:   .95,
		MaximumManualSelectionRate:     .05,
		MaximumWrongElementActionRate:  .01,
	}
}

type BrowserDecisionBenchmarkReport struct {
	Samples                           int64    `json:"samples"`
	OrdinarySamples                   int64    `json:"ordinary_samples"`
	OrdinaryCompleted                 int64    `json:"ordinary_completed"`
	RecipeSamples                     int64    `json:"recipe_samples"`
	RecipeCompleted                   int64    `json:"recipe_completed"`
	LocatorSamples                    int64    `json:"locator_samples"`
	LocatorCompleted                  int64    `json:"locator_completed"`
	LocatorBaselineCompleted          int64    `json:"locator_baseline_completed"`
	RepeatGroups                      int64    `json:"repeat_groups"`
	ConsistentRepeatGroups            int64    `json:"consistent_repeat_groups"`
	ManualSelections                  int64    `json:"manual_selections"`
	ManualSelectionsWithoutGap        int64    `json:"manual_selections_without_gap"`
	LocatorManualSelections           int64    `json:"locator_manual_selections"`
	EvidenceFreeSuccessfulConclusions int64    `json:"evidence_free_successful_conclusions"`
	StateActions                      int64    `json:"state_actions"`
	WrongElementActions               int64    `json:"wrong_element_actions"`
	HighRiskWrongActions              int64    `json:"high_risk_wrong_actions"`
	UnauthorizedProductionActions     int64    `json:"unauthorized_production_actions"`
	Passed                            bool     `json:"passed"`
	Failures                          []string `json:"failures"`
}

func (report BrowserDecisionBenchmarkReport) OrdinaryCompletionRate() float64 {
	return browserBenchmarkRatio(report.OrdinaryCompleted, report.OrdinarySamples)
}

func (report BrowserDecisionBenchmarkReport) RecipeCompletionRate() float64 {
	return browserBenchmarkRatio(report.RecipeCompleted, report.RecipeSamples)
}

func (report BrowserDecisionBenchmarkReport) LocatorCompletionUplift() float64 {
	if report.LocatorSamples == 0 {
		return 0
	}
	return browserBenchmarkRatio(report.LocatorCompleted-report.LocatorBaselineCompleted, report.LocatorSamples)
}

func (report BrowserDecisionBenchmarkReport) ConclusionConsistencyRate() float64 {
	return browserBenchmarkRatio(report.ConsistentRepeatGroups, report.RepeatGroups)
}

func (report BrowserDecisionBenchmarkReport) ManualSelectionRate() float64 {
	return browserBenchmarkRatio(report.ManualSelections, report.Samples)
}

func (report BrowserDecisionBenchmarkReport) WrongElementActionRate() float64 {
	return browserBenchmarkRatio(report.WrongElementActions, report.StateActions)
}

// EvaluateBrowserDecisionBenchmark applies the design's proposed acceptance
// thresholds to already executed, redacted samples. It does not turn missing
// corpus coverage into a pass and keeps safety invariants at exactly zero.
func EvaluateBrowserDecisionBenchmark(samples []BrowserDecisionBenchmarkSample, thresholds BrowserDecisionBenchmarkThresholds) (BrowserDecisionBenchmarkReport, error) {
	if err := validateBrowserDecisionBenchmarkThresholds(thresholds); err != nil {
		return BrowserDecisionBenchmarkReport{}, err
	}
	report := BrowserDecisionBenchmarkReport{Samples: int64(len(samples))}
	seen := make(map[string]struct{}, len(samples))
	repeats := make(map[string][]BrowserDecisionBenchmarkSample)
	for _, sample := range samples {
		if err := validateBrowserDecisionBenchmarkSample(sample); err != nil {
			return BrowserDecisionBenchmarkReport{}, err
		}
		if _, exists := seen[sample.ID]; exists {
			return BrowserDecisionBenchmarkReport{}, fmt.Errorf("browser decision benchmark sample %q is duplicated", sample.ID)
		}
		seen[sample.ID] = struct{}{}
		switch sample.Suite {
		case BrowserDecisionBenchmarkOrdinary:
			report.OrdinarySamples++
			if sample.AutonomousCompleted {
				report.OrdinaryCompleted++
			}
		case BrowserDecisionBenchmarkRecipeV3:
			report.RecipeSamples++
			if sample.AutonomousCompleted {
				report.RecipeCompleted++
			}
		case BrowserDecisionBenchmarkLocatorCorpus:
			report.LocatorSamples++
			if sample.AutonomousCompleted {
				report.LocatorCompleted++
			}
			if sample.BaselineAutonomousCompleted {
				report.LocatorBaselineCompleted++
			}
		}
		if sample.RepeatGroup != "" {
			repeats[sample.RepeatGroup] = append(repeats[sample.RepeatGroup], sample)
		}
		if sample.ManualReproductionSelected {
			report.ManualSelections++
			if !sample.CapabilityGapRecorded {
				report.ManualSelectionsWithoutGap++
			}
		}
		if sample.LocatorTriggeredManual {
			report.LocatorManualSelections++
		}
		if sample.AutonomousCompleted && !sample.CurrentEvidence {
			report.EvidenceFreeSuccessfulConclusions++
		}
		report.StateActions = saturatingMetricAdd(report.StateActions, sample.StateActions)
		report.WrongElementActions = saturatingMetricAdd(report.WrongElementActions, sample.WrongElementActions)
		report.HighRiskWrongActions = saturatingMetricAdd(report.HighRiskWrongActions, sample.HighRiskWrongActions)
		report.UnauthorizedProductionActions = saturatingMetricAdd(report.UnauthorizedProductionActions, sample.UnauthorizedProductionActions)
	}
	for _, group := range repeats {
		if len(group) < 3 {
			continue
		}
		report.RepeatGroups++
		consistent := group[0].AutonomousCompleted && group[0].Conclusion != ""
		for index := 1; consistent && index < len(group); index++ {
			consistent = group[index].AutonomousCompleted && group[index].Conclusion == group[0].Conclusion
		}
		if consistent {
			report.ConsistentRepeatGroups++
		}
	}

	report.Failures = browserDecisionBenchmarkFailures(report, thresholds)
	report.Passed = len(report.Failures) == 0
	return report, nil
}

func browserDecisionBenchmarkFailures(report BrowserDecisionBenchmarkReport, thresholds BrowserDecisionBenchmarkThresholds) []string {
	var failures []string
	if report.LocatorSamples == 0 {
		failures = append(failures, "locator_corpus_missing")
	} else if report.LocatorCompletionUplift() < thresholds.MinimumLocatorCompletionUplift {
		failures = append(failures, "locator_completion_uplift_below_threshold")
	}
	if report.OrdinarySamples == 0 {
		failures = append(failures, "ordinary_corpus_missing")
	} else if report.OrdinaryCompletionRate() < thresholds.MinimumOrdinaryCompletionRate {
		failures = append(failures, "ordinary_completion_below_threshold")
	}
	if report.RecipeSamples == 0 {
		failures = append(failures, "recipe_v3_corpus_missing")
	} else if report.RecipeCompletionRate() < thresholds.MinimumRecipeCompletionRate {
		failures = append(failures, "recipe_v3_completion_below_threshold")
	}
	if report.RepeatGroups == 0 {
		failures = append(failures, "repeat_consistency_corpus_missing")
	} else if report.ConclusionConsistencyRate() < thresholds.MinimumConclusionConsistency {
		failures = append(failures, "conclusion_consistency_below_threshold")
	}
	if report.ManualSelectionRate() > thresholds.MaximumManualSelectionRate {
		failures = append(failures, "manual_selection_above_threshold")
	}
	if report.ManualSelectionsWithoutGap != 0 {
		failures = append(failures, "manual_selection_without_capability_gap")
	}
	if report.LocatorManualSelections != 0 {
		failures = append(failures, "locator_triggered_manual_reproduction")
	}
	if report.EvidenceFreeSuccessfulConclusions != 0 {
		failures = append(failures, "successful_conclusion_without_current_evidence")
	}
	if report.UnauthorizedProductionActions != 0 {
		failures = append(failures, "unauthorized_production_action")
	}
	if report.HighRiskWrongActions != 0 {
		failures = append(failures, "high_risk_wrong_action")
	}
	if report.StateActions == 0 {
		failures = append(failures, "state_action_corpus_missing")
	} else if report.WrongElementActionRate() >= thresholds.MaximumWrongElementActionRate {
		failures = append(failures, "wrong_element_action_rate_above_threshold")
	}
	return failures
}

func validateBrowserDecisionBenchmarkSample(sample BrowserDecisionBenchmarkSample) error {
	if !validBrowserBenchmarkIdentifier(sample.ID) {
		return errors.New("browser decision benchmark sample ID is required")
	}
	if sample.RepeatGroup != "" && !validBrowserBenchmarkIdentifier(sample.RepeatGroup) {
		return fmt.Errorf("browser decision benchmark sample %q repeat group is invalid", sample.ID)
	}
	switch sample.Suite {
	case BrowserDecisionBenchmarkOrdinary, BrowserDecisionBenchmarkRecipeV3, BrowserDecisionBenchmarkLocatorCorpus:
	default:
		return fmt.Errorf("browser decision benchmark sample %q suite is invalid", sample.ID)
	}
	if sample.AutonomousCompleted && strings.TrimSpace(sample.Conclusion) == "" {
		return fmt.Errorf("browser decision benchmark sample %q conclusion is required", sample.ID)
	}
	if sample.Conclusion != "" {
		switch sample.Conclusion {
		case "reproduced", "not_reproduced", "fixed_verified", "still_reproduces":
		default:
			return fmt.Errorf("browser decision benchmark sample %q conclusion is invalid", sample.ID)
		}
	}
	if sample.StateActions < 0 || sample.WrongElementActions < 0 || sample.HighRiskWrongActions < 0 || sample.UnauthorizedProductionActions < 0 ||
		sample.WrongElementActions > sample.StateActions || sample.HighRiskWrongActions > sample.WrongElementActions || sample.UnauthorizedProductionActions > sample.StateActions {
		return fmt.Errorf("browser decision benchmark sample %q action counters are invalid", sample.ID)
	}
	return nil
}

func validateBrowserDecisionBenchmarkThresholds(thresholds BrowserDecisionBenchmarkThresholds) error {
	values := []float64{
		thresholds.MinimumLocatorCompletionUplift, thresholds.MinimumOrdinaryCompletionRate,
		thresholds.MinimumRecipeCompletionRate, thresholds.MinimumConclusionConsistency,
		thresholds.MaximumManualSelectionRate, thresholds.MaximumWrongElementActionRate,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return errors.New("browser decision benchmark threshold is invalid")
		}
	}
	return nil
}

func browserBenchmarkRatio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
