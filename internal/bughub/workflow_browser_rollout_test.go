package bughub

import (
	"context"
	"testing"
)

type phaseBrowserDecisionCapableVerifier struct {
	BrowserDecisionSessionOpener
}

func (phaseBrowserDecisionCapableVerifier) Execute(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
	return BrowserVerificationResult{}, nil
}

func TestBrowserDecisionRolloutDefaultsDisabledAndStable(t *testing.T) {
	request := BrowserDecisionRolloutRequest{CaseID: "case-42", Capabilities: completeBrowserDecisionRolloutCapabilities()}
	first, err := DecideBrowserDecisionRollout(DefaultBrowserDecisionRolloutPolicy(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecideBrowserDecisionRollout(DefaultBrowserDecisionRolloutPolicy(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Enabled || first.Reason != BrowserDecisionRolloutDisabled {
		t.Fatalf("unexpected default decision: %#v", first)
	}
	if first.Bucket != second.Bucket || first.Bucket < 0 || first.Bucket >= browserDecisionRolloutBuckets {
		t.Fatalf("rollout bucket is not stable and bounded: %#v %#v", first, second)
	}
}

func TestBrowserDecisionRolloutRequiresNonProductionAndCompleteHostStack(t *testing.T) {
	policy := BrowserDecisionRolloutPolicy{Version: BrowserDecisionRolloutVersion, Enabled: true, Percentage: 100}
	production, err := DecideBrowserDecisionRollout(policy, BrowserDecisionRolloutRequest{
		CaseID: "case-production", IsProduction: true, Capabilities: completeBrowserDecisionRolloutCapabilities(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Enabled || production.Reason != BrowserDecisionRolloutProduction {
		t.Fatalf("production must be rejected: %#v", production)
	}

	missing, err := DecideBrowserDecisionRollout(policy, BrowserDecisionRolloutRequest{
		CaseID: "case-missing", Capabilities: BrowserDecisionRolloutCapabilities{PersistentSession: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Enabled || missing.Reason != BrowserDecisionRolloutCapabilityMissing {
		t.Fatalf("partial Host stack must be rejected: %#v", missing)
	}

	enabled, err := DecideBrowserDecisionRollout(policy, BrowserDecisionRolloutRequest{
		CaseID: "case-enabled", Capabilities: completeBrowserDecisionRolloutCapabilities(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Reason != BrowserDecisionRolloutEnabled {
		t.Fatalf("complete non-production stack should be enabled at 100%%: %#v", enabled)
	}
}

func TestBrowserDecisionRolloutHonorsDeterministicPercentage(t *testing.T) {
	capabilities := completeBrowserDecisionRolloutCapabilities()
	for i := 0; i < 200; i++ {
		caseID := "percentage-case-" + string(rune(i+1))
		request := BrowserDecisionRolloutRequest{CaseID: caseID, Capabilities: capabilities}
		decision, err := DecideBrowserDecisionRollout(BrowserDecisionRolloutPolicy{
			Version: BrowserDecisionRolloutVersion, Enabled: true, Percentage: 37,
		}, request)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Enabled != (decision.Bucket < 3700) {
			t.Fatalf("percentage decision does not match bucket: %#v", decision)
		}
	}
}

func TestBrowserDecisionRolloutRejectsInvalidPolicyOrCase(t *testing.T) {
	tests := []struct {
		name    string
		policy  BrowserDecisionRolloutPolicy
		request BrowserDecisionRolloutRequest
	}{
		{name: "version", policy: BrowserDecisionRolloutPolicy{Version: 2}, request: BrowserDecisionRolloutRequest{CaseID: "case"}},
		{name: "negative percentage", policy: BrowserDecisionRolloutPolicy{Version: 1, Percentage: -1}, request: BrowserDecisionRolloutRequest{CaseID: "case"}},
		{name: "large percentage", policy: BrowserDecisionRolloutPolicy{Version: 1, Percentage: 101}, request: BrowserDecisionRolloutRequest{CaseID: "case"}},
		{name: "missing case", policy: BrowserDecisionRolloutPolicy{Version: 1}, request: BrowserDecisionRolloutRequest{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecideBrowserDecisionRollout(test.policy, test.request); err == nil {
				t.Fatal("expected invalid rollout input to fail")
			}
		})
	}
}

func TestAgentPhaseRunnerBrowserDecisionPolicyIsExplicitDefaultOff(t *testing.T) {
	runner := NewAgentPhaseRunner(nil, nil, nil, "", nil)
	if runner.browserDecisionPolicy != DefaultBrowserDecisionRolloutPolicy() {
		t.Fatalf("default policy=%+v", runner.browserDecisionPolicy)
	}
	if err := runner.SetBrowserDecisionRolloutPolicy(BrowserDecisionRolloutPolicy{
		Version: BrowserDecisionRolloutVersion, Enabled: true, Percentage: 25,
	}); err != nil {
		t.Fatal(err)
	}
	if !runner.browserDecisionPolicy.Enabled || runner.browserDecisionPolicy.Percentage != 25 {
		t.Fatalf("configured policy=%+v", runner.browserDecisionPolicy)
	}
	if err := runner.SetBrowserDecisionRolloutPolicy(BrowserDecisionRolloutPolicy{Version: 1, Percentage: 101}); err == nil {
		t.Fatal("expected invalid runner policy to fail")
	}
	if runner.browserDecisionPolicy.Percentage != 25 {
		t.Fatal("invalid policy mutated the active configuration")
	}
}

func TestPhaseRunnerRegistersDecisionCompositionOnlyForCapableVerifier(t *testing.T) {
	store := openTestCaseStore(t)
	opener := &fakeBrowserDecisionSessionOpener{}
	runner := phaseBrowserDecisionCoordinatorRunner(
		phaseBrowserDecisionCapableVerifier{BrowserDecisionSessionOpener: opener},
		&phaseExecutorStub{}, store, PhaseAttempt{ID: "attempt-composition"}, BotRef{Target: "codex"}, "prompt", nil, nil,
	)
	if runner == nil {
		t.Fatal("capable verifier did not register autonomous runner")
	}
	if capabilities := runner.BrowserDecisionRolloutCapabilities(); !capabilities.complete() {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	legacy := browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
		return BrowserVerificationResult{}, nil
	})
	if got := phaseBrowserDecisionCoordinatorRunner(legacy, &phaseExecutorStub{}, store, PhaseAttempt{}, BotRef{}, "", nil, nil); got != nil {
		t.Fatalf("legacy verifier unexpectedly registered autonomous runner: %#v", got)
	}
}

func completeBrowserDecisionRolloutCapabilities() BrowserDecisionRolloutCapabilities {
	return BrowserDecisionRolloutCapabilities{
		PersistentSession: true, FrozenSceneStore: true, DecisionProvider: true, RecoveryEvidence: true,
	}
}
