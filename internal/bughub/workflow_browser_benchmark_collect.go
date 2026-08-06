package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const BrowserDecisionBenchmarkCollectionVersion = 1
const maxBrowserDecisionBenchmarkRunsBytes = 512 << 20

const (
	BrowserBenchmarkSkipNoRun           = "no_projected_run"
	BrowserBenchmarkSkipNoMetric        = "no_autonomous_metric"
	BrowserBenchmarkSkipMalformedMetric = "malformed_autonomous_metric"
	BrowserBenchmarkSkipScenarioMissing = "scenario_binding_missing"
	BrowserBenchmarkSkipInvalidSample   = "invalid_derived_sample"
	BrowserBenchmarkSkipDuplicateRun    = "duplicate_projected_run"
)

type BrowserDecisionBenchmarkCollection struct {
	Version            int                            `json:"version"`
	ScannedAttempts    int64                          `json:"scanned_attempts"`
	EligibleAttempts   int64                          `json:"eligible_attempts"`
	AutonomousAttempts int64                          `json:"autonomous_attempts"`
	CollectedSamples   int64                          `json:"collected_samples"`
	Skipped            map[string]int64               `json:"skipped"`
	Corpus             BrowserDecisionBenchmarkCorpus `json:"corpus"`
}

type browserDecisionCollectedMetric struct {
	Status                        string
	ScenarioContractSHA256        string
	RecipeVersion                 int64
	StateActions                  int64
	WrongElementActions           int64
	HighRiskWrongActions          int64
	UnauthorizedProductionActions int64
}

// CollectBrowserDecisionBenchmarkFromRoot reads Studio's compatibility event
// projection and durable workflow database without mutating either source. It
// emits only pseudonymous identifiers, enums, booleans, digests and counters.
// Attempts recorded before the complete safety metric contract are skipped
// instead of receiving guessed zero values.
func CollectBrowserDecisionBenchmarkFromRoot(ctx context.Context, root string) (BrowserDecisionBenchmarkCollection, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return BrowserDecisionBenchmarkCollection{}, errors.New("browser benchmark collection root is required")
	}
	store, err := OpenCaseStoreReadOnly(filepath.Join(root, "workflows.db"))
	if err != nil {
		return BrowserDecisionBenchmarkCollection{}, err
	}
	defer store.Close()

	runsPath := NewInvestigationStore(root).Path()
	info, statErr := os.Stat(runsPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return BrowserDecisionBenchmarkCollection{}, statErr
	}
	if statErr == nil && (!info.Mode().IsRegular() || info.Size() > maxBrowserDecisionBenchmarkRunsBytes) {
		return BrowserDecisionBenchmarkCollection{}, errors.New("browser benchmark projected runs file is invalid")
	}
	var runs []InvestigationRun
	if statErr == nil {
		investigationStoreMu.Lock()
		runs, err = NewInvestigationStore(root).readAll()
		investigationStoreMu.Unlock()
		if err != nil {
			return BrowserDecisionBenchmarkCollection{}, err
		}
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{})
	if err != nil {
		return BrowserDecisionBenchmarkCollection{}, err
	}
	cases, err := store.ListCases(ctx)
	if err != nil {
		return BrowserDecisionBenchmarkCollection{}, err
	}
	artifactsByAttempt := make(map[string][]EvidenceArtifact)
	for _, incident := range cases {
		artifacts, listErr := store.ListEvidenceArtifacts(ctx, incident.ID)
		if listErr != nil {
			return BrowserDecisionBenchmarkCollection{}, listErr
		}
		for _, artifact := range artifacts {
			artifactsByAttempt[artifact.AttemptID] = append(artifactsByAttempt[artifact.AttemptID], artifact)
		}
	}
	runByID := make(map[string]InvestigationRun, len(runs))
	duplicateRuns := make(map[string]struct{})
	for _, run := range runs {
		if _, duplicate := runByID[run.ID]; duplicate {
			duplicateRuns[run.ID] = struct{}{}
			continue
		}
		runByID[run.ID] = run
	}
	priorLocator := make(map[string]bool)
	collection := BrowserDecisionBenchmarkCollection{
		Version:         BrowserDecisionBenchmarkCollectionVersion,
		ScannedAttempts: int64(len(attempts)), Skipped: make(map[string]int64),
		Corpus: BrowserDecisionBenchmarkCorpus{Version: BrowserDecisionBenchmarkCorpusVersion, Samples: []BrowserDecisionBenchmarkSample{}},
	}
	for _, attempt := range attempts {
		if attempt.Phase != PhaseValidation && attempt.Phase != PhaseRegression {
			continue
		}
		collection.EligibleAttempts++
		_, duplicate := duplicateRuns[attempt.ID]
		if duplicate {
			collection.skip(BrowserBenchmarkSkipDuplicateRun)
			priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
			continue
		}
		run, found := runByID[attempt.ID]
		if !found {
			collection.skip(BrowserBenchmarkSkipNoRun)
			priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
			continue
		}
		metric, found, metricErr := latestBrowserDecisionCollectedMetric(run.Events, attempt)
		if metricErr != nil {
			collection.skip(BrowserBenchmarkSkipMalformedMetric)
			priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
			continue
		}
		if !found {
			collection.skip(BrowserBenchmarkSkipNoMetric)
			priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
			continue
		}
		collection.AutonomousAttempts++
		if !validLowerSHA256(metric.ScenarioContractSHA256) {
			collection.skip(BrowserBenchmarkSkipScenarioMissing)
			priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
			continue
		}
		sample := browserDecisionBenchmarkSampleFromAttempt(
			attempt, artifactsByAttempt[attempt.ID], metric,
			priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed",
		)
		if err := validateBrowserDecisionBenchmarkSample(sample); err != nil {
			collection.skip(BrowserBenchmarkSkipInvalidSample)
		} else {
			collection.Corpus.Samples = append(collection.Corpus.Samples, sample)
			collection.CollectedSamples++
		}
		priorLocator[attempt.CaseID] = priorLocator[attempt.CaseID] || attempt.ErrorCode == "browser_locator_failed"
	}
	sort.Slice(collection.Corpus.Samples, func(i, j int) bool {
		return collection.Corpus.Samples[i].ID < collection.Corpus.Samples[j].ID
	})
	return collection, nil
}

func (collection *BrowserDecisionBenchmarkCollection) skip(code string) {
	collection.Skipped[code]++
}

func latestBrowserDecisionCollectedMetric(events []InvestigationEvent, attempt PhaseAttempt) (browserDecisionCollectedMetric, bool, error) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "browser_decision_loop_metrics" {
			continue
		}
		if bound, ok := event.Meta["attempt_id"].(string); ok && strings.TrimSpace(bound) != attempt.ID {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark metric attempt binding is invalid")
		}
		if bound, ok := event.Meta["case_id"].(string); ok && strings.TrimSpace(bound) != attempt.CaseID {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark metric case binding is invalid")
		}
		status, ok := event.Meta["status"].(string)
		if !ok || strings.TrimSpace(status) == "" {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark metric status is invalid")
		}
		metric := browserDecisionCollectedMetric{Status: status}
		var valid bool
		metric.StateActions, valid = browserDecisionMetricInt(event.Meta["state_actions"])
		if !valid {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark state action metric is invalid")
		}
		metric.WrongElementActions, valid = browserDecisionMetricInt(event.Meta["wrong_element_actions"])
		if !valid {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark wrong element metric is invalid")
		}
		metric.HighRiskWrongActions, valid = browserDecisionMetricInt(event.Meta["high_risk_wrong_actions"])
		if !valid {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark high risk metric is invalid")
		}
		metric.UnauthorizedProductionActions, valid = browserDecisionMetricInt(event.Meta["unauthorized_production_actions"])
		if !valid {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark production metric is invalid")
		}
		metric.RecipeVersion, valid = browserDecisionMetricInt(event.Meta["recipe_version"])
		if !valid || (metric.RecipeVersion != 0 && metric.RecipeVersion != AutonomousValidationRecipeVersion) {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark recipe metric is invalid")
		}
		metric.ScenarioContractSHA256, ok = event.Meta["scenario_contract_sha256"].(string)
		if !ok || metric.WrongElementActions > metric.StateActions || metric.HighRiskWrongActions > metric.WrongElementActions || metric.UnauthorizedProductionActions > metric.StateActions {
			return browserDecisionCollectedMetric{}, true, errors.New("browser benchmark safety metrics are inconsistent")
		}
		return metric, true, nil
	}
	return browserDecisionCollectedMetric{}, false, nil
}

func browserDecisionBenchmarkSampleFromAttempt(attempt PhaseAttempt, artifacts []EvidenceArtifact, metric browserDecisionCollectedMetric, locatorCorpus bool) BrowserDecisionBenchmarkSample {
	suite := BrowserDecisionBenchmarkOrdinary
	if metric.RecipeVersion == AutonomousValidationRecipeVersion {
		suite = BrowserDecisionBenchmarkRecipeV3
	} else if locatorCorpus {
		suite = BrowserDecisionBenchmarkLocatorCorpus
	}
	conclusion, currentEvidence := browserDecisionBenchmarkConclusion(attempt, artifacts)
	autonomousCompleted := metric.Status == BrowserDecisionLoopConcluded && attempt.Status == AttemptStatusSucceeded && conclusion != ""
	manualSelected := false
	for _, artifact := range artifacts {
		if artifact.Kind == ManualReproductionArtifactKind && artifact.RedactionStatus != RedactionStatusPending {
			manualSelected = true
		}
	}
	capabilityGap := metric.Status == BrowserDecisionLoopCapabilityGap && attempt.ErrorCode == "browser_capability_gap" && browserAttemptHasManualGateProof(attempt)
	return BrowserDecisionBenchmarkSample{
		ID: browserBenchmarkOpaqueIdentifier("sample", attempt.ID), Suite: suite,
		RepeatGroup:         browserBenchmarkOpaqueIdentifier("scenario", metric.ScenarioContractSHA256),
		AutonomousCompleted: autonomousCompleted, BaselineAutonomousCompleted: false,
		Conclusion: conclusion, CurrentEvidence: currentEvidence,
		ManualReproductionSelected: manualSelected, CapabilityGapRecorded: capabilityGap,
		LocatorTriggeredManual: manualSelected && attempt.ErrorCode == "browser_locator_failed",
		StateActions:           metric.StateActions, WrongElementActions: metric.WrongElementActions,
		HighRiskWrongActions:          metric.HighRiskWrongActions,
		UnauthorizedProductionActions: metric.UnauthorizedProductionActions,
	}
}

func browserDecisionBenchmarkConclusion(attempt PhaseAttempt, artifacts []EvidenceArtifact) (string, bool) {
	var output struct {
		VerificationStatus string            `json:"verification_status"`
		Evidence           []json.RawMessage `json:"evidence"`
	}
	if json.Unmarshal(attempt.OutputJSON, &output) != nil {
		return "", false
	}
	conclusion := strings.TrimSpace(output.VerificationStatus)
	switch conclusion {
	case "reproduced", "not_reproduced", "fixed_verified", "still_reproduces":
	default:
		conclusion = ""
	}
	currentEvidence := false
	for _, artifact := range artifacts {
		if artifact.RedactionStatus != RedactionStatusPending {
			currentEvidence = true
			break
		}
	}
	return conclusion, currentEvidence && len(output.Evidence) != 0
}

func browserAttemptHasManualGateProof(attempt PhaseAttempt) bool {
	var output struct {
		ManualReproductionGate *BrowserManualReproductionGateProof `json:"manual_reproduction_gate"`
	}
	return json.Unmarshal(attempt.OutputJSON, &output) == nil && output.ManualReproductionGate != nil &&
		ValidateBrowserManualReproductionGateProof(*output.ManualReproductionGate, attempt.ID) == nil
}

func browserBenchmarkOpaqueIdentifier(domain, value string) string {
	digest := sha256.Sum256([]byte("tshoot:browser-benchmark:" + domain + ":v1\x00" + strings.TrimSpace(value)))
	return domain + "-" + hex.EncodeToString(digest[:16])
}
