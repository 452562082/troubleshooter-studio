package bughub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserDecisionBenchmarkCorpusStrictRoundTrip(t *testing.T) {
	corpus := BrowserDecisionBenchmarkCorpus{Version: 1, Samples: []BrowserDecisionBenchmarkSample{
		benchmarkSample("ordinary-1", BrowserDecisionBenchmarkOrdinary),
	}}
	encoded, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBrowserDecisionBenchmarkCorpus(encoded)
	if err != nil {
		t.Fatal(err)
	}
	output, err := RunBrowserDecisionBenchmarkCorpus(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if output.Version != 1 || output.Report.Samples != 1 || output.Report.Passed {
		t.Fatalf("output=%+v", output)
	}
	if !containsBenchmarkFailure(output.Report.Failures, "locator_corpus_missing") {
		t.Fatalf("failures=%v", output.Report.Failures)
	}
}

func TestBrowserDecisionBenchmarkCorpusRejectsUnknownAndUnsafeIdentity(t *testing.T) {
	valid := `{"version":1,"samples":[{"id":"ordinary-1","suite":"ordinary","autonomous_completed":false,"baseline_autonomous_completed":false,"current_evidence":false,"manual_reproduction_selected":false,"capability_gap_recorded":false,"locator_triggered_manual":false,"state_actions":0,"wrong_element_actions":0,"high_risk_wrong_actions":0,"unauthorized_production_actions":0}]}`
	for _, content := range []string{
		strings.Replace(valid, `"version":1`, `"version":1,"unknown":true`, 1),
		strings.Replace(valid, `"ordinary-1"`, `"../secret"`, 1),
		valid + `{}`,
	} {
		corpus, err := DecodeBrowserDecisionBenchmarkCorpus([]byte(content))
		if err == nil {
			_, err = RunBrowserDecisionBenchmarkCorpus(corpus)
		}
		if err == nil {
			t.Fatalf("unsafe corpus accepted: %s", content)
		}
	}
}

func TestBrowserDecisionBenchmarkEmptyCorpusIsValidButCannotPassGate(t *testing.T) {
	corpus, err := DecodeBrowserDecisionBenchmarkCorpus([]byte(`{"version":1,"samples":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := RunBrowserDecisionBenchmarkCorpus(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if output.Report.Passed || output.Report.Samples != 0 {
		t.Fatalf("empty corpus must fail closed: %+v", output.Report)
	}
	for _, required := range []string{"locator_corpus_missing", "ordinary_corpus_missing", "recipe_v3_corpus_missing", "repeat_consistency_corpus_missing", "state_action_corpus_missing"} {
		if !containsBenchmarkFailure(output.Report.Failures, required) {
			t.Fatalf("missing failure %q in %v", required, output.Report.Failures)
		}
	}
}
