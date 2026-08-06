package bughub

import (
	"context"
	"strings"
	"testing"
	"time"
)

func browserDecisionStepStoreFixture(t *testing.T, store *CaseStore, suffix string) PhaseAttempt {
	t.Helper()
	caseID := "case-browser-step-" + suffix
	createTestCase(t, store, caseID)
	attempt := PhaseAttempt{
		ID: "attempt-browser-step-" + suffix, CaseID: caseID, CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning,
		AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
		StartedAt: time.Now().UTC(),
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func preparedBrowserDecisionStep(attemptID string, stepNo int, digestCharacter byte) BrowserDecisionStep {
	digest := strings.Repeat(string(digestCharacter), 64)
	return BrowserDecisionStep{
		AttemptID: attemptID, StepNo: stepNo, SceneSHA256: digest,
		DecisionSHA256: strings.Repeat("b", 64), ActionFingerprint: strings.Repeat("c", 64),
		Status: BrowserDecisionStepPrepared, BeforeSceneRef: "browser-scenes/step-before.json",
	}
}

func TestBrowserDecisionStepStoreEnforcesDurableLifecycleAndIdempotency(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	attempt := browserDecisionStepStoreFixture(t, store, "lifecycle")
	input := preparedBrowserDecisionStep(attempt.ID, 1, 'a')

	prepared, replay, err := store.PrepareBrowserDecisionStep(ctx, input)
	if err != nil || replay || prepared.Status != BrowserDecisionStepPrepared || prepared.CreatedAt.IsZero() {
		t.Fatalf("prepared=%+v replay=%v err=%v", prepared, replay, err)
	}
	preparedReplay, replay, err := store.PrepareBrowserDecisionStep(ctx, input)
	if err != nil || !replay || preparedReplay.DecisionSHA256 != input.DecisionSHA256 {
		t.Fatalf("prepared replay=%+v replay=%v err=%v", preparedReplay, replay, err)
	}
	conflict := input
	conflict.SceneSHA256 = strings.Repeat("d", 64)
	if _, _, err := store.PrepareBrowserDecisionStep(ctx, conflict); err == nil {
		t.Fatal("prepare accepted a conflicting scene for an existing step")
	}

	executing, replay, err := store.StartBrowserDecisionStep(ctx, attempt.ID, 1, input.DecisionSHA256)
	if err != nil || replay || executing.Status != BrowserDecisionStepExecuting {
		t.Fatalf("executing=%+v replay=%v err=%v", executing, replay, err)
	}
	_, replay, err = store.StartBrowserDecisionStep(ctx, attempt.ID, 1, input.DecisionSHA256)
	if err != nil || !replay {
		t.Fatalf("executing replay=%v err=%v", replay, err)
	}

	completed, replay, err := store.CompleteBrowserDecisionStep(ctx, attempt.ID, 1, BrowserDecisionStepConfirmed, BrowserStepEffectConfirmedCode, "browser-scenes/step-after.json")
	if err != nil || replay || completed.Status != BrowserDecisionStepConfirmed || completed.AfterSceneRef == "" {
		t.Fatalf("completed=%+v replay=%v err=%v", completed, replay, err)
	}
	_, replay, err = store.CompleteBrowserDecisionStep(ctx, attempt.ID, 1, BrowserDecisionStepConfirmed, BrowserStepEffectConfirmedCode, "browser-scenes/step-after.json")
	if err != nil || !replay {
		t.Fatalf("completion replay=%v err=%v", replay, err)
	}
	if _, _, err := store.CompleteBrowserDecisionStep(ctx, attempt.ID, 1, BrowserDecisionStepNoEffect, BrowserStepNoEffectCode, "browser-scenes/other-after.json"); err == nil {
		t.Fatal("terminal browser decision step was overwritten")
	}
}

func TestBrowserDecisionStepStoreMarksOnlyExecutingStepsUncertain(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	attempt := browserDecisionStepStoreFixture(t, store, "recovery")
	first := preparedBrowserDecisionStep(attempt.ID, 1, 'a')
	second := preparedBrowserDecisionStep(attempt.ID, 2, 'd')
	second.DecisionSHA256 = strings.Repeat("e", 64)
	second.ActionFingerprint = strings.Repeat("f", 64)
	second.BeforeSceneRef = "browser-scenes/step-2-before.json"
	for _, step := range []BrowserDecisionStep{first, second} {
		if _, _, err := store.PrepareBrowserDecisionStep(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.StartBrowserDecisionStep(ctx, attempt.ID, first.StepNo, first.DecisionSHA256); err != nil {
		t.Fatal(err)
	}
	changed, err := store.MarkExecutingBrowserDecisionStepsUncertain(ctx, attempt.ID)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	steps, err := store.ListBrowserDecisionSteps(ctx, attempt.ID)
	if err != nil || len(steps) != 2 || steps[0].Status != BrowserDecisionStepUncertain || steps[0].EffectCode != BrowserStepUncertainCode || steps[1].Status != BrowserDecisionStepPrepared {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	if _, _, err := store.StartBrowserDecisionStep(ctx, attempt.ID, first.StepNo, first.DecisionSHA256); err == nil {
		t.Fatal("uncertain browser decision step was replayed")
	}
}

func TestBrowserDecisionStepStoreRejectsUnsafeOrIneligiblePreparation(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "invalid")
	unsafe := preparedBrowserDecisionStep(attempt.ID, 1, 'a')
	unsafe.BeforeSceneRef = "../token=secret.json"
	if _, _, err := store.PrepareBrowserDecisionStep(context.Background(), unsafe); err == nil {
		t.Fatal("unsafe before-scene reference was accepted")
	}

	finished := time.Now().UTC()
	attempt.Status = AttemptStatusFailed
	attempt.FinishedAt = &finished
	attempt.ErrorCode = "browser_execution_interrupted"
	attempt.ErrorMessage = "interrupted"
	if err := store.FinishAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PrepareBrowserDecisionStep(context.Background(), preparedBrowserDecisionStep(attempt.ID, 1, 'a')); err == nil {
		t.Fatal("step preparation accepted a terminal attempt")
	}
}
