package browserverify

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

// TestLiveFunhubBrowserDecisionLoop exercises the real pinned persistent session,
// Host binding, transaction journal, effect evaluator and finish evidence on a
// non-production site. Decision selection is deterministic so this smoke
// isolates Host/runtime correctness from external model variability.
func TestLiveFunhubBrowserDecisionLoop(t *testing.T) {
	if os.Getenv("TSHOOT_LIVE_FUNHUB_DECISION") != "1" {
		t.Skip("set TSHOOT_LIVE_FUNHUB_DECISION=1 to run")
	}
	const startURL = "https://funhub-web-test.guadd.fun/"
	const attemptID = "live-funhub-browser-decision"
	const caseID = "live-funhub-browser-decision-case"
	scenarioSHA := strings.Repeat("a", 64)
	exact := true
	plan := bughub.BrowserPlan{
		Version: 2, DeviceProfile: "mobile", StartURL: startURL,
		ScenarioContract: &bughub.BrowserScenarioContract{
			Version: 1, Goal: "搜索用户汤圆", Basis: "bug", CausalActionIDs: []string{"open-search", "enter-nickname", "submit-search", "switch-user-results"},
			FrontendEntryIDs: []string{"funhub"}, ContextSHA256: scenarioSHA,
			Evidence: []bughub.BrowserScenarioEvidence{{Kind: "ui_assertions"}},
		},
		Actions: []bughub.BrowserAction{
			{ID: "open-search", Action: "goto", URL: "https://funhub-web-test.guadd.fun/search"},
			{ID: "enter-nickname", Action: "fill", Locator: &bughub.BrowserLocator{Kind: "placeholder", Value: "请输入搜索关键词", Exact: &exact}, Value: "汤圆"},
			{ID: "submit-search", Action: "click", Locator: &bughub.BrowserLocator{Kind: "role", Value: "button", Name: "搜索", Exact: &exact}},
			{ID: "switch-user-results", Action: "click", Locator: &bughub.BrowserLocator{Kind: "role", Value: "button", Name: "用户", Exact: &exact}},
		},
		Assertions: []bughub.BrowserAssertion{{Kind: "visible_text", Value: "汤圆"}},
	}
	store, err := bughub.OpenCaseStore(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateCase(context.Background(), bughub.IncidentCase{
		ID: caseID, BugID: "live-funhub-browser-decision-bug", Status: bughub.CasePendingValidation, CycleNumber: 1, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(context.Background(), bughub.PhaseAttempt{
		ID: attemptID, CaseID: caseID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce,
		Status: bughub.AttemptStatusRunning, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntimeManager(filepath.Join(home, ".tshoot", "bugs"), nil)
	verifier := NewHostVerifier(runtime, nil, net.DefaultResolver)
	provider := bughub.BrowserDecisionProviderFunc(func(_ context.Context, observation bughub.BrowserDecisionLoopObservation) ([]byte, error) {
		nextAction := 0
		for _, step := range observation.History {
			if !step.Exploration && step.Outcome == bughub.BrowserEffectConfirmed {
				nextAction++
			}
		}
		if nextAction >= len(plan.Actions) {
			return json.Marshal(bughub.BrowserDecision{
				Version: bughub.BrowserDecisionVersion, Decision: "conclude", SceneID: observation.Scene.SceneID,
				RationaleCode: "evidence_sufficient", ConclusionCode: "current_evidence_sufficient",
			})
		}
		action := plan.Actions[nextAction]
		ref := ""
		if action.Action != "goto" {
			var findErr error
			ref, findErr = liveFunhubDecisionElementRef(observation.Scene, action.ID)
			if findErr != nil {
				return nil, findErr
			}
		}
		effects := bughub.BrowserDecisionExpectedEffects{}
		switch action.ID {
		case "open-search":
			effects.AnyOf = []bughub.BrowserDecisionEffect{{Kind: "url_contains", SameOriginPathContains: "/search"}}
		case "enter-nickname":
			effects.AnyOf = []bughub.BrowserDecisionEffect{{Kind: "input_value_persisted", ElementRef: ref}}
		case "submit-search":
			effects.AnyOf = []bughub.BrowserDecisionEffect{{Kind: "url_contains", SameOriginPathContains: "/search-result"}}
		case "switch-user-results":
			effects.AnyOf = []bughub.BrowserDecisionEffect{
				{Kind: "scene_changed"},
				{Kind: "element_visible", Role: "button", NameContains: "用户"},
			}
		}
		return json.Marshal(bughub.BrowserDecision{
			Version: bughub.BrowserDecisionVersion, Decision: "act", SceneID: observation.Scene.SceneID,
			RationaleCode:  "scenario_next_step",
			Action:         &bughub.BrowserDecisionAction{ActionID: action.ID, Type: action.Action, ElementRef: ref},
			ExpectedEffect: &effects,
		})
	})
	var events []bughub.InvestigationEvent
	runner := bughub.HostBrowserDecisionCoordinatorRunner{
		Opener: verifier, Provider: provider,
		Transactions: bughub.BrowserStepTransactionCoordinator{Store: store},
		Recovery:     bughub.ConservativeBrowserDecisionRecoveryProvider{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := runner.ExecuteBrowserDecision(ctx, bughub.BrowserDecisionCoordinatorRunRequest{
		Verification: bughub.BrowserVerificationRequest{
			CaseID: caseID, AttemptID: attemptID, CycleNumber: 1, SystemID: "funhub", Environment: "test",
			Version: BrowserRuntimeVersion, Plan: plan, StagingDir: t.TempDir(),
			Emit: func(progress bughub.BrowserProgress) {
				t.Logf("progress: code=%s action=%s", progress.Code, progress.ActionID)
			},
			Policy: bughub.BrowserSecurityPolicy{
				AllowedOrigins:     []string{"https://base-resources.chainthink.cn", "https://funhub-web-test.guadd.fun", "https://truss-api-test.guadd.fun"},
				ApplicationOrigins: []string{"https://funhub-web-test.guadd.fun"}, StartOrigins: []string{"https://funhub-web-test.guadd.fun"},
				PrivateOrigins: []string{"https://base-resources.chainthink.cn", "https://funhub-web-test.guadd.fun", "https://truss-api-test.guadd.fun"},
			},
		},
		Plan: plan,
		Readiness: func(context.Context, bughub.BrowserScene) (bughub.BrowserDecisionLoopReadiness, error) {
			return bughub.BrowserDecisionLoopReadiness{
				ScenarioContractSHA256: scenarioSHA, FrontendEntryID: "funhub",
				ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
			}, nil
		},
		Emit: func(event bughub.InvestigationEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || len(result.Artifacts) == 0 || result.FinalScreenshotPath == "" {
		t.Fatalf("result status=%s code=%s artifacts=%d screenshot=%q", result.Status, result.ErrorCode, len(result.Artifacts), result.FinalScreenshotPath)
	}
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	confirmedSteps, confirmedOK := events[0].Meta["confirmed_steps"].(int)
	if events[0].Type != "browser_decision_loop_metrics" || events[0].Meta["status"] != bughub.BrowserDecisionLoopConcluded || !confirmedOK || confirmedSteps < 4 {
		t.Fatalf("events=%+v", events)
	}
	journal, err := store.ListBrowserDecisionSteps(context.Background(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	confirmedJournal := 0
	for _, step := range journal {
		if step.Status == bughub.BrowserDecisionStepConfirmed {
			confirmedJournal++
		}
	}
	if confirmedJournal < 4 {
		t.Fatalf("confirmed journal steps=%d all=%+v", confirmedJournal, journal)
	}
}

func liveFunhubDecisionElementRef(scene bughub.BrowserScene, actionID string) (string, error) {
	matches := make([]string, 0, 2)
	observed := make([]string, 0, 12)
	for _, element := range scene.Elements {
		if len(observed) < cap(observed) && element.States.Visible {
			observed = append(observed, fmt.Sprintf("%s|%s|%s", element.Role, element.Name, element.LocatorHints.Placeholder))
		}
		matched := false
		switch actionID {
		case "enter-nickname":
			matched = element.LocatorHints.Placeholder == "请输入搜索关键词"
		case "submit-search":
			matched = element.Name == "搜索"
		case "switch-user-results":
			matched = element.Name == "用户"
		}
		if matched && element.States.Visible && element.States.InViewport {
			matches = append(matches, element.Ref)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("live Funhub action %s has %d current Scene targets (url=%s visible=%q)", actionID, len(matches), scene.URL, observed)
	}
	return matches[0], nil
}
