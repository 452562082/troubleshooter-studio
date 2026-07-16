package browserverify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type durableCoordinatorExecutor struct {
	plannerCalls   int
	evaluatorCalls int
}

func (e *durableCoordinatorExecutor) ExecutePhase(_ context.Context, _ string, _ bughub.BotRef, prompt string, _ func(bughub.InvestigationEvent)) (bughub.PhaseExecutionResult, error) {
	switch {
	case strings.Contains(prompt, "validation browser planner"):
		e.plannerCalls++
		if e.plannerCalls > 1 {
			return bughub.PhaseExecutionResult{}, errors.New("durable coordinator plan was regenerated")
		}
		return bughub.PhaseExecutionResult{FinalYAML: `version: 1
start_url: https://app.test/users
actions:
  - id: goto
    action: goto
    url: https://app.test/users
  - id: wait
    action: wait_for
    locator:
      kind: text
      value: Users
  - id: shot
    action: screenshot
assertions:
  - kind: visible_text
    value: Users
`}, nil
	case strings.Contains(prompt, "Evaluate the completed browser verification"):
		e.evaluatorCalls++
		return bughub.PhaseExecutionResult{FinalYAML: `verification_status: not_reproduced
environment: test
observed_behavior: Users page rendered normally
expected_behavior: Users page rendered normally
evidence: []
gaps: []
`}, nil
	default:
		return bughub.PhaseExecutionResult{}, errors.New("unexpected coordinator agent prompt")
	}
}

func (*durableCoordinatorExecutor) CancelPhase(context.Context, string) error { return nil }

func durableCoordinatorRequest(t *testing.T) bughub.BrowserCoordinatorRequest {
	t.Helper()
	return bughub.BrowserCoordinatorRequest{
		Attempt: bughub.PhaseAttempt{
			ID: "attempt-durable-contract", CaseID: "case-durable-contract", CycleNumber: 1,
			Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning,
			AgentTarget: "codex", BotKey: "shop|codex#validator", InputJSON: []byte(`{"mode":"reproduce"}`),
		},
		Bug:        bughub.Bug{ID: "bug-durable-contract", SystemID: "shop", Env: "test", FrontendURL: "https://app.test/users"},
		Bot:        bughub.BotRef{Key: "shop|codex#validator", SystemID: "shop", Target: "codex", Role: "validator", Env: "test"},
		BasePrompt: "durable browser recovery contract",
		Policy:     bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
		StagingDir: t.TempDir(),
		FreezeArtifacts: func(context.Context, []bughub.BrowserArtifactReference) error {
			return nil
		},
	}
}

func TestCoordinatorRecoveryReusesDurablePlanWithRealHostManifest(t *testing.T) {
	t.Run("completed manifest replay", func(t *testing.T) {
		executor := &durableCoordinatorExecutor{}
		worker := &fakeWorker{Result: completedWorkerResult()}
		coordinator := bughub.BrowserCoordinator{Executor: executor, Verifier: newTestHostVerifier(t, worker)}
		request := durableCoordinatorRequest(t)
		for retry := 0; retry < 2; retry++ {
			result, err := coordinator.Execute(context.Background(), request)
			if err != nil || result.ErrorCode != "" {
				t.Fatalf("retry=%d result=%+v err=%v", retry, result, err)
			}
		}
		if executor.plannerCalls != 1 || executor.evaluatorCalls != 2 || worker.Calls != 1 {
			t.Fatalf("planner=%d evaluator=%d worker=%d", executor.plannerCalls, executor.evaluatorCalls, worker.Calls)
		}
	})

	t.Run("first interruption safely reruns persisted read only plan", func(t *testing.T) {
		executor := &durableCoordinatorExecutor{}
		worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{errors.New("first worker interruption")}}
		coordinator := bughub.BrowserCoordinator{Executor: executor, Verifier: newTestHostVerifier(t, worker)}
		request := durableCoordinatorRequest(t)
		first, err := coordinator.Execute(context.Background(), request)
		if err != nil || first.ErrorCode == "" {
			t.Fatalf("first result=%+v err=%v", first, err)
		}
		second, err := coordinator.Execute(context.Background(), request)
		if err != nil || second.ErrorCode != "" {
			t.Fatalf("second result=%+v err=%v", second, err)
		}
		if executor.plannerCalls != 1 || executor.evaluatorCalls != 1 || worker.Calls != 2 {
			t.Fatalf("planner=%d evaluator=%d worker=%d", executor.plannerCalls, executor.evaluatorCalls, worker.Calls)
		}
	})

	t.Run("second interruption terminates stably", func(t *testing.T) {
		executor := &durableCoordinatorExecutor{}
		worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{errors.New("first worker interruption"), errors.New("second worker interruption")}}
		coordinator := bughub.BrowserCoordinator{Executor: executor, Verifier: newTestHostVerifier(t, worker)}
		request := durableCoordinatorRequest(t)
		for retry := 0; retry < 3; retry++ {
			result, err := coordinator.Execute(context.Background(), request)
			if err != nil || result.ErrorCode == "" {
				t.Fatalf("retry=%d result=%+v err=%v", retry, result, err)
			}
			if retry == 2 && result.ErrorCode != "browser_execution_interrupted" {
				t.Fatalf("terminal result=%+v", result)
			}
		}
		if executor.plannerCalls != 1 || executor.evaluatorCalls != 0 || worker.Calls != 2 {
			t.Fatalf("planner=%d evaluator=%d worker=%d", executor.plannerCalls, executor.evaluatorCalls, worker.Calls)
		}
	})
}
