package browserverify

import (
	"context"
	"fmt"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type fakeBrowserStepSessionTransport struct {
	result   workerStepSessionResult
	err      error
	commands []workerStepSessionCommand
	closed   int
}

func (transport *fakeBrowserStepSessionTransport) Step(_ context.Context, command workerStepSessionCommand) (workerStepSessionResult, error) {
	transport.commands = append(transport.commands, command)
	return transport.result, transport.err
}

func (transport *fakeBrowserStepSessionTransport) Close() error {
	transport.closed++
	return nil
}

func TestBoundNodeBrowserStepSessionValidatesBindsFreezesAndEvaluatesOneStep(t *testing.T) {
	ctx := context.Background()
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"},
		StartOrigins: []string{"https://app.test"},
	}
	beforeRaw := workerSceneFixture()
	afterRaw := workerSceneFixture()
	afterRaw.CapturedAt = "2026-08-04T12:00:01Z"
	afterRaw.Title = "User details"
	afterRaw.TextBlocks[0].Text = "User details"
	transport := &fakeBrowserStepSessionTransport{result: workerStepSessionResult{
		Status: "completed", FinalURL: afterRaw.URL, Title: afterRaw.Title, Scene: afterRaw,
		Receipt: &workerStepSessionReceipt{ActionID: "open-user", ActionType: "click", TargetElementRef: "e-1"},
		Effect: &workerStepSessionEffect{
			ActionID: "open-user", ActionType: "click", EffectStatus: "observed", SceneObserved: true,
			SceneChanged: true, SurfaceTransition: "unchanged",
		},
		Artifacts: []workerArtifact{},
	}}
	var frozen []string
	freeze := func(_ context.Context, step int, result workerStepSessionResult, scene bughub.BrowserScene) (string, error) {
		if result.Scene == nil || scene.AttemptID != "attempt-step-session" || scene.SceneID == "" {
			t.Fatalf("unbound freeze result=%+v scene=%+v", result, scene)
		}
		ref := fmt.Sprintf("browser-scenes/step-%02d.json", step)
		frozen = append(frozen, ref)
		return ref, nil
	}
	session, initial, err := bindNodeBrowserStepSession(ctx, transport, workerStepSessionResult{
		Status: "completed", FinalURL: beforeRaw.URL, Title: beforeRaw.Title, Scene: beforeRaw, Artifacts: []workerArtifact{},
	}, publicResolver("app.test"), policy, "attempt-step-session", freeze)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.ExecuteBoundBrowserStep(ctx, bughub.BoundBrowserStepExecutionRequest{
		AttemptID: "attempt-step-session", StepNo: 1, Before: initial.Scene,
		Step: bughub.BoundBrowserDecisionStep{
			BeforeSceneSHA256: initial.Scene.SceneSHA256, ElementRef: "e-1",
			Action:         bughub.BrowserAction{ID: "open-user", Action: "click", Value: "must-not-enter-command"},
			ExpectedEffect: bughub.BrowserDecisionExpectedEffects{AnyOf: []bughub.BrowserDecisionEffect{{Kind: "text_visible", Text: "User details"}}},
			PassiveChecks:  []bughub.BrowserDecisionPassiveCheck{{Kind: "screenshot"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Effect.Outcome != bughub.BrowserEffectConfirmed || result.After.AttemptID != "attempt-step-session" || result.AfterSceneRef != "browser-scenes/step-01.json" {
		t.Fatalf("execution result=%+v", result)
	}
	if len(transport.commands) != 1 || transport.commands[0].ElementRef != "e-1" || transport.commands[0].ActionID != "open-user" || len(transport.commands[0].PassiveChecks) != 1 {
		t.Fatalf("command=%+v", transport.commands)
	}
	if len(frozen) != 2 || initial.SceneRef != "browser-scenes/step-00.json" {
		t.Fatalf("frozen=%v initial=%+v", frozen, initial)
	}
	if err := session.Close(); err != nil || transport.closed != 1 {
		t.Fatalf("close err=%v calls=%d", err, transport.closed)
	}
}

func TestBoundNodeBrowserStepSessionTreatsWorkerStaleAsObservedNoEffect(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"},
	}
	raw := workerSceneFixture()
	transport := &fakeBrowserStepSessionTransport{result: workerStepSessionResult{
		Status: "scene_stale", ErrorCode: "browser_scene_stale", FinalURL: raw.URL, Title: raw.Title, Scene: raw, Artifacts: []workerArtifact{},
	}}
	freeze := func(_ context.Context, step int, _ workerStepSessionResult, _ bughub.BrowserScene) (string, error) {
		return fmt.Sprintf("browser-scenes/stale-%d.json", step), nil
	}
	session, initial, err := bindNodeBrowserStepSession(context.Background(), transport, workerStepSessionResult{
		Status: "completed", FinalURL: raw.URL, Title: raw.Title, Scene: raw, Artifacts: []workerArtifact{},
	}, publicResolver("app.test"), policy, "attempt-stale", freeze)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.ExecuteBoundBrowserStep(context.Background(), bughub.BoundBrowserStepExecutionRequest{
		AttemptID: "attempt-stale", StepNo: 1, Before: initial.Scene,
		Step: bughub.BoundBrowserDecisionStep{
			BeforeSceneSHA256: initial.Scene.SceneSHA256, ElementRef: "e-1",
			Action:         bughub.BrowserAction{ID: "open-user", Action: "click"},
			ExpectedEffect: bughub.BrowserDecisionExpectedEffects{AnyOf: []bughub.BrowserDecisionEffect{{Kind: "text_visible", Text: "details"}}},
		},
	})
	if err != nil || result.Effect.Outcome != bughub.BrowserEffectNoEffect || result.After.SceneID == "" ||
		result.RecoveryEvidence.DispatchState != bughub.BrowserActionDispatchNotDispatched {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrowserStepSessionRecoveryEvidenceOnlyMarksPreDispatchFailures(t *testing.T) {
	for _, test := range []struct {
		status string
		code   string
		want   string
	}{
		{status: "locator_failed", code: "locator_not_found", want: bughub.BrowserActionDispatchNotDispatched},
		{status: "locator_failed", code: "locator_ambiguous", want: bughub.BrowserActionDispatchNotDispatched},
		{status: "locator_failed", code: "active_surface_not_found", want: bughub.BrowserActionDispatchNotDispatched},
		{status: "locator_failed", code: "element_click_intercepted", want: bughub.BrowserActionDispatchUnknown},
		{status: "locator_failed", code: "input_value_not_persisted", want: bughub.BrowserActionDispatchUnknown},
		{status: "completed", code: "", want: bughub.BrowserActionDispatchUnknown},
	} {
		if got := browserStepSessionRecoveryEvidence(test.status, test.code).DispatchState; got != test.want {
			t.Fatalf("status=%q code=%q got=%q want=%q", test.status, test.code, got, test.want)
		}
	}
}

func TestBindNodeBrowserStepSessionClosesTransportWhenEvidenceCannotFreeze(t *testing.T) {
	raw := workerSceneFixture()
	transport := &fakeBrowserStepSessionTransport{}
	_, _, err := bindNodeBrowserStepSession(context.Background(), transport, workerStepSessionResult{
		Status: "completed", FinalURL: raw.URL, Title: raw.Title, Scene: raw, Artifacts: []workerArtifact{},
	}, publicResolver("app.test"), bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"},
	}, "attempt-freeze-failure", func(context.Context, int, workerStepSessionResult, bughub.BrowserScene) (string, error) {
		return "../unsafe-scene.json", nil
	})
	if err == nil || transport.closed != 1 {
		t.Fatalf("err=%v close calls=%d", err, transport.closed)
	}
}

func TestValidateWorkerStepSessionResultRejectsReceiptAndOriginForgery(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"},
	}
	command := workerStepSessionCommand{Command: "step", Sequence: 1, SceneID: "scene-1", ActionID: "open-user", ActionType: "click", ElementRef: "e-1", PassiveChecks: []string{}}
	base := workerStepSessionResult{
		Status: "completed", FinalURL: "https://app.test/users", Scene: workerSceneFixture(),
		Receipt:   &workerStepSessionReceipt{ActionID: "other-action", ActionType: "click", TargetElementRef: "e-1"},
		Effect:    &workerStepSessionEffect{ActionID: "open-user", ActionType: "click", EffectStatus: "observed", SceneObserved: true, SurfaceTransition: "unchanged"},
		Artifacts: []workerArtifact{},
	}
	if _, err := validateAndSanitizeWorkerStepSessionResult(context.Background(), publicResolver("app.test"), policy, base, false, command); err == nil {
		t.Fatal("expected forged receipt binding to fail")
	}
	base.Receipt.ActionID = "open-user"
	base.Scene.URL = "https://evil.test/users"
	base.Scene.Frames[0].URL = base.Scene.URL
	base.FinalURL = base.Scene.URL
	if _, err := validateAndSanitizeWorkerStepSessionResult(context.Background(), publicResolver("app.test", "evil.test"), policy, base, false, command); err == nil {
		t.Fatal("expected disallowed Scene origin to fail")
	}
}

func TestValidateWorkerStepSessionFinalResultRequiresBoundEvidenceShape(t *testing.T) {
	policy := bughub.BrowserSecurityPolicy{
		AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"},
	}
	raw := workerSceneFixture()
	result, err := validateAndSanitizeWorkerStepSessionFinalResult(context.Background(), publicResolver("app.test"), policy, workerStepSessionResult{
		Status: "completed", FinalURL: raw.URL, Title: "token=secret", FinalScreenshotPath: "browser/final.png",
		Scene: raw, Artifacts: []workerArtifact{{Kind: "screenshot", Path: "browser/final.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "[REDACTED]" || result.Scene == nil || result.FinalScreenshotPath != "browser/final.png" {
		t.Fatalf("sanitized final result=%+v", result)
	}

	invalid := result
	invalid.Receipt = &workerStepSessionReceipt{ActionID: "forged"}
	if _, err := validateAndSanitizeWorkerStepSessionFinalResult(context.Background(), publicResolver("app.test"), policy, invalid); err == nil {
		t.Fatal("expected final action receipt to fail")
	}
	login := workerStepSessionResult{
		Status: "login_required", ErrorCode: "browser_login_required", FinalURL: "https://app.test/login",
		LoginOrigin: "https://app.test", FinalScreenshotPath: "browser/login.png", Artifacts: []workerArtifact{},
	}
	if _, err := validateAndSanitizeWorkerStepSessionFinalResult(context.Background(), publicResolver("app.test"), policy, login); err == nil {
		t.Fatal("expected login final screenshot to fail")
	}
}
