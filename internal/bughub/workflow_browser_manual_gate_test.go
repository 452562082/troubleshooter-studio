package bughub

import (
	"strings"
	"testing"
)

func browserCapabilityGapDecision(t *testing.T, sceneID string, channels string) BrowserDecision {
	t.Helper()
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: capability_gap
scene_id: `+sceneID+`
rationale_code: automation_exhausted
capability_gap_code: browser_capability_gap
exhausted_channels: [`+channels+`]
`), sceneID)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func browserManualGateInput(t *testing.T) BrowserManualReproductionGateInput {
	t.Helper()
	return BrowserManualReproductionGateInput{
		Decision:      browserCapabilityGapDecision(t, "scene-capability-gap", "semantic_grounding, structured_grounding, safe_exploration"),
		ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
		ExplorationExhausted: true,
		ChannelStates: map[string]string{
			"semantic_grounding":   BrowserAutomationChannelExhausted,
			"structured_grounding": BrowserAutomationChannelExhausted,
			"safe_exploration":     BrowserAutomationChannelExhausted,
			"ai_observe":           BrowserAutomationChannelUnavailable,
			"visual_grounding":     BrowserAutomationChannelUnavailable,
		},
	}
}

func TestBrowserManualReproductionGateRequiresExplicitUserChoice(t *testing.T) {
	input := browserManualGateInput(t)
	decision, err := EvaluateBrowserManualReproductionGate(input)
	if err != nil || !decision.Available || decision.StartAllowed || decision.Code != BrowserManualReproductionAvailableCode {
		t.Fatalf("available decision=%+v err=%v", decision, err)
	}
	input.UserOptedIn = true
	decision, err = EvaluateBrowserManualReproductionGate(input)
	if err != nil || !decision.Available || !decision.StartAllowed || decision.Code != BrowserManualReproductionSelectedCode {
		t.Fatalf("selected decision=%+v err=%v", decision, err)
	}
}

func TestBrowserManualReproductionGateAcceptsExhaustedOptionalProviders(t *testing.T) {
	input := browserManualGateInput(t)
	input.Decision = browserCapabilityGapDecision(t, "scene-capability-gap", "semantic_grounding, structured_grounding, ai_observe, visual_grounding, safe_exploration")
	input.ChannelStates["ai_observe"] = BrowserAutomationChannelExhausted
	input.ChannelStates["visual_grounding"] = BrowserAutomationChannelExhausted
	decision, err := EvaluateBrowserManualReproductionGate(input)
	if err != nil || !decision.Available {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestBrowserManualReproductionGateDeniesEveryIncompleteOrSystemFailurePath(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BrowserManualReproductionGateInput)
	}{
		{name: "production", mutate: func(input *BrowserManualReproductionGateInput) { input.IsProduction = true }},
		{name: "scenario missing", mutate: func(input *BrowserManualReproductionGateInput) { input.ScenarioReady = false }},
		{name: "login missing", mutate: func(input *BrowserManualReproductionGateInput) { input.LoginReady = false }},
		{name: "test input missing", mutate: func(input *BrowserManualReproductionGateInput) { input.TestInputsReady = false }},
		{name: "authorization missing", mutate: func(input *BrowserManualReproductionGateInput) { input.AuthorizationReady = false }},
		{name: "exploration not exhausted", mutate: func(input *BrowserManualReproductionGateInput) { input.ExplorationExhausted = false }},
		{name: "safe candidate exists", mutate: func(input *BrowserManualReproductionGateInput) { input.SafeCandidateExists = true }},
		{name: "system failure", mutate: func(input *BrowserManualReproductionGateInput) { input.SystemFailure = true }},
		{name: "semantic grounding missing", mutate: func(input *BrowserManualReproductionGateInput) { input.ChannelStates["semantic_grounding"] = "pending" }},
		{name: "AI provider pending", mutate: func(input *BrowserManualReproductionGateInput) { input.ChannelStates["ai_observe"] = "pending" }},
		{name: "false optional declaration", mutate: func(input *BrowserManualReproductionGateInput) {
			input.Decision = browserCapabilityGapDecision(t, "scene-capability-gap", "semantic_grounding, structured_grounding, ai_observe, safe_exploration")
		}},
		{name: "not capability gap", mutate: func(input *BrowserManualReproductionGateInput) {
			input.Decision = BrowserDecision{
				Version: BrowserDecisionVersion, Decision: "conclude", SceneID: "scene-capability-gap",
				RationaleCode: "evidence_sufficient", ConclusionCode: "current_evidence_sufficient",
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := browserManualGateInput(t)
			test.mutate(&input)
			decision, err := EvaluateBrowserManualReproductionGate(input)
			if err != nil || decision.Available || decision.StartAllowed || decision.Code != BrowserManualReproductionDeniedCode {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestBrowserManualReproductionGateRejectsNonCanonicalDecision(t *testing.T) {
	input := browserManualGateInput(t)
	input.Decision.SceneID = ""
	decision, err := EvaluateBrowserManualReproductionGate(input)
	if err == nil || decision.Available || decision.StartAllowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestBrowserManualReproductionGateProofIsAttemptBoundAndCanonical(t *testing.T) {
	input := browserManualGateInput(t)
	scene := BrowserScene{SceneID: "scene-capability-gap"}
	readiness := BrowserDecisionLoopReadiness{
		ScenarioContractSHA256: strings.Repeat("a", 64), FrontendEntryID: "primary",
		ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
	}
	proof, err := issueBrowserManualReproductionGateProof(input, "attempt-capability-gap", scene, readiness)
	if err != nil || proof == nil {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	if err := ValidateBrowserManualReproductionGateProof(*proof, "attempt-capability-gap"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBrowserManualReproductionGateProof(*proof, "attempt-other"); err == nil {
		t.Fatal("cross-attempt proof was accepted")
	}
	tampered := *proof
	tampered.ExhaustedChannels = []string{"semantic_grounding", "safe_exploration", "structured_grounding"}
	if err := ValidateBrowserManualReproductionGateProof(tampered, tampered.AttemptID); err == nil {
		t.Fatal("non-canonical proof was accepted")
	}
}
