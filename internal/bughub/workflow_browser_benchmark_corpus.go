package bughub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const BrowserDecisionBenchmarkCorpusVersion = 1
const MaxBrowserDecisionBenchmarkCorpusBytes = 8 << 20
const MaxBrowserDecisionBenchmarkCorpusSamples = 100_000

type BrowserDecisionBenchmarkCorpus struct {
	Version    int                                 `json:"version"`
	Thresholds *BrowserDecisionBenchmarkThresholds `json:"thresholds,omitempty"`
	Samples    []BrowserDecisionBenchmarkSample    `json:"samples"`
}

type BrowserDecisionBenchmarkOutput struct {
	Version                   int                                `json:"version"`
	Thresholds                BrowserDecisionBenchmarkThresholds `json:"thresholds"`
	Report                    BrowserDecisionBenchmarkReport     `json:"report"`
	OrdinaryCompletionRate    float64                            `json:"ordinary_completion_rate"`
	RecipeCompletionRate      float64                            `json:"recipe_completion_rate"`
	LocatorCompletionUplift   float64                            `json:"locator_completion_uplift"`
	ConclusionConsistencyRate float64                            `json:"conclusion_consistency_rate"`
	ManualSelectionRate       float64                            `json:"manual_selection_rate"`
	WrongElementActionRate    float64                            `json:"wrong_element_action_rate"`
}

func DecodeBrowserDecisionBenchmarkCorpus(content []byte) (BrowserDecisionBenchmarkCorpus, error) {
	if len(content) == 0 || len(content) > MaxBrowserDecisionBenchmarkCorpusBytes {
		return BrowserDecisionBenchmarkCorpus{}, errors.New("browser decision benchmark corpus size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var corpus BrowserDecisionBenchmarkCorpus
	if err := decoder.Decode(&corpus); err != nil {
		return BrowserDecisionBenchmarkCorpus{}, fmt.Errorf("decode browser decision benchmark corpus: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BrowserDecisionBenchmarkCorpus{}, errors.New("browser decision benchmark corpus has trailing content")
	}
	if corpus.Version != BrowserDecisionBenchmarkCorpusVersion || len(corpus.Samples) > MaxBrowserDecisionBenchmarkCorpusSamples {
		return BrowserDecisionBenchmarkCorpus{}, errors.New("browser decision benchmark corpus shape is invalid")
	}
	return corpus, nil
}

func RunBrowserDecisionBenchmarkCorpus(corpus BrowserDecisionBenchmarkCorpus) (BrowserDecisionBenchmarkOutput, error) {
	thresholds := DefaultBrowserDecisionBenchmarkThresholds()
	if corpus.Thresholds != nil {
		thresholds = *corpus.Thresholds
	}
	report, err := EvaluateBrowserDecisionBenchmark(corpus.Samples, thresholds)
	if err != nil {
		return BrowserDecisionBenchmarkOutput{}, err
	}
	return BrowserDecisionBenchmarkOutput{
		Version: BrowserDecisionBenchmarkCorpusVersion, Thresholds: thresholds, Report: report,
		OrdinaryCompletionRate: report.OrdinaryCompletionRate(), RecipeCompletionRate: report.RecipeCompletionRate(),
		LocatorCompletionUplift: report.LocatorCompletionUplift(), ConclusionConsistencyRate: report.ConclusionConsistencyRate(),
		ManualSelectionRate: report.ManualSelectionRate(), WrongElementActionRate: report.WrongElementActionRate(),
	}, nil
}

func validBrowserBenchmarkIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && character != '.' &&
			(character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}
