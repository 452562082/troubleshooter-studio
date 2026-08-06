package main

import (
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func TestBrowserDecisionRolloutPolicyFromUserConfigDefaultsOff(t *testing.T) {
	policy, configured, err := browserDecisionRolloutPolicyFromUserConfig(&userconfig.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if configured || policy != bughub.DefaultBrowserDecisionRolloutPolicy() {
		t.Fatalf("policy=%+v configured=%v", policy, configured)
	}
}

func TestBrowserDecisionRolloutPolicyFromUserConfigValidatesExplicitConfig(t *testing.T) {
	policy, configured, err := browserDecisionRolloutPolicyFromUserConfig(&userconfig.Config{
		BrowserDecisionRollout: &userconfig.BrowserDecisionRolloutConfig{Version: 1, Enabled: true, Percentage: 25},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !configured || !policy.Enabled || policy.Percentage != 25 || policy.Version != 1 {
		t.Fatalf("policy=%+v configured=%v", policy, configured)
	}
	for _, invalid := range []*userconfig.BrowserDecisionRolloutConfig{
		{Version: 2, Enabled: true, Percentage: 25},
		{Version: 1, Enabled: true, Percentage: -1},
		{Version: 1, Enabled: true, Percentage: 101},
	} {
		if _, _, err := browserDecisionRolloutPolicyFromUserConfig(&userconfig.Config{BrowserDecisionRollout: invalid}); err == nil {
			t.Fatalf("invalid config accepted: %+v", invalid)
		}
	}
}

func TestIncidentBrowserDecisionRolloutStatusReportsActivePolicy(t *testing.T) {
	app := &App{
		workflowRunner:                    bughub.NewAgentPhaseRunner(nil, nil, nil, "", nil),
		workflowBrowserDecisionPolicy:     bughub.BrowserDecisionRolloutPolicy{Version: 1, Enabled: true, Percentage: 10},
		workflowBrowserDecisionConfigured: true,
	}
	status, err := app.GetIncidentBrowserDecisionRolloutStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != 1 || !status.Enabled || status.Percentage != 10 || status.Source != "user_config" {
		t.Fatalf("status=%+v", status)
	}
}
