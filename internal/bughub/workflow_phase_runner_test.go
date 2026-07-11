package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPhaseResultValidationStatusMappingIsStrict(t *testing.T) {
	cases := []struct {
		status string
		want   CaseStatus
	}{
		{"reproduced", CaseReproduced},
		{"not_reproduced", CaseNotReproduced},
		{"insufficient_info", CaseWaitingEvidence},
		{"fixed_verified", CaseFixedVerified},
		{"still_reproduces", CaseStillReproduces},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			got, err := ParseValidationResult([]byte("verification_status: " + tc.status + "\nenvironment: test\nevidence: []\ngaps: []\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got.CaseStatus() != tc.want {
				t.Fatalf("status = %q, want %q", got.CaseStatus(), tc.want)
			}
		})
	}
	for _, invalid := range []string{"fixed", "REPRODUCED", "reproduced ", ""} {
		if _, err := ParseValidationResult([]byte("verification_status: \"" + invalid + "\"\nenvironment: test\nevidence: []\ngaps: []\n")); err == nil {
			t.Fatalf("accepted invalid status %q", invalid)
		}
	}
}

type phaseExecutorStub struct {
	mu       sync.Mutex
	calls    int
	result   PhaseExecutionResult
	errors   []error
	event    InvestigationEvent
	cancelID string
}

func (s *phaseExecutorStub) ExecutePhase(_ context.Context, attemptID string, _ BotRef, _ string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	result := s.result
	var err error
	if call <= len(s.errors) {
		err = s.errors[call-1]
	}
	event := s.event
	s.mu.Unlock()
	if emit != nil && event.Type != "" {
		emit(event)
	}
	return result, err
}

func (s *phaseExecutorStub) CancelPhase(_ context.Context, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelID = attemptID
	return nil
}

func TestAgentPhaseRunnerCompletesOnceAndTagsProjectionEvents(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-phase-runner", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	legacy := NewInvestigationStore(t.TempDir())
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n", Usage: AgentUsage{InputTokens: 12, OutputTokens: 7}}, event: InvestigationEvent{Type: "agent_message", Message: "working"}}
	completed := make(chan CompleteAttemptCommand, 2)
	runner := NewAgentPhaseRunner(store, executor, legacy, filepath.Join(t.TempDir(), "artifacts"), func(_ context.Context, cmd CompleteAttemptCommand) error {
		completed <- cmd
		return nil
	})
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "validator", Target: "codex", Env: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "validator", Target: "codex", Env: "test"}); err != nil {
		t.Fatal(err)
	}
	var cmd CompleteAttemptCommand
	select {
	case cmd = <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("completion callback was not called")
	}
	if cmd.Outcome != PhaseOutcomeReproduced || cmd.Usage.InputTokens != 12 || cmd.Usage.OutputTokens != 7 {
		t.Fatalf("completion = %+v", cmd)
	}
	select {
	case duplicate := <-completed:
		t.Fatalf("duplicate callback: %+v", duplicate)
	case <-time.After(50 * time.Millisecond):
	}
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "validator", Target: "codex", Env: "test"}); err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	if executor.calls != 1 {
		t.Fatalf("duplicate starts executed process %d times", executor.calls)
	}
	executor.mu.Unlock()
	run, err := legacy.Get(attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.FinalMessage != executor.result.FinalYAML {
		t.Fatalf("legacy projection final = %q", run.FinalMessage)
	}
	found := false
	for _, event := range run.Events {
		if event.Type == "agent_message" {
			found = true
			for key, want := range map[string]any{"case_id": incident.ID, "attempt_id": attempt.ID, "cycle_number": attempt.CycleNumber, "phase": string(attempt.Phase)} {
				if fmt.Sprint(event.Meta[key]) != fmt.Sprint(want) {
					t.Errorf("event %s = %#v, want %#v", key, event.Meta[key], want)
				}
			}
		}
	}
	if !found {
		t.Fatal("tagged process event not projected")
	}
}

func TestAgentPhaseRunnerRetriesReadOnlyOnceButNeverFix(t *testing.T) {
	for _, tc := range []struct {
		phase Phase
		mode  AttemptMode
		yaml  string
		calls int
	}{
		{PhaseValidation, AttemptReproduce, "verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n", 2},
		{PhaseInvestigation, "", "investigation_status: root_cause_ready\nenvironment: test\nroot_cause: race\nconfidence: high\nevidence: []\ngaps: []\n", 2},
		{PhaseFix, "", "fix_status: failed\nenvironment: test\nbranches: []\nchanges: []\ntests: []\ndeployment_notice: ''\nrisks: []\nblocked_reason: failed\nevidence: []\n", 1},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-retry-"+string(tc.phase), statusForRunningPhase(tc.phase))
			attempt := createPhaseRunnerAttempt(t, store, incident, tc.phase, tc.mode)
			executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: tc.yaml}, errors: []error{errors.New("process exited")}}
			done := make(chan struct{}, 1)
			runner := NewAgentPhaseRunner(store, executor, nil, filepath.Join(t.TempDir(), "artifacts"), func(context.Context, CompleteAttemptCommand) error { done <- struct{}{}; return nil })
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex", Env: "test"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("runner did not finish")
			}
			executor.mu.Lock()
			calls := executor.calls
			executor.mu.Unlock()
			if calls != tc.calls {
				t.Fatalf("calls = %d, want %d", calls, tc.calls)
			}
		})
	}
}

func TestAgentPhaseRunnerRegressionRequiresMatchedDeploymentAndFreshSameEnvironmentEvidence(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-regression-fresh", CaseRegressionValidating)
	input, _ := json.Marshal(RegressionValidationInput{OriginalReproduction: "checkout", OriginalScenarioHash: "scenario", ExpectedFixCommits: map[string]string{"api": "fix-1"}, ObservedDeploymentVersion: "version-1", TargetEnvironment: "test"})
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseRegression, AttemptRegression)
	attempt.InputJSON = input
	// Update the fixture attempt through a fresh case because attempts are immutable while running.
	if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json = ? WHERE id = ?`, string(input), attempt.ID); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "fresh.har")
	if err := os.WriteFile(source, []byte(`{"status":200}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	yaml := "verification_status: fixed_verified\nenvironment: test\nscenario_hash: scenario\nevidence:\n  - kind: har\n    path: " + source + "\n    captured_at: " + fresh + "\n    environment: test\n    version: version-1\n    request_id: req-new\n    redaction_status: not_required\ngaps: []\n"
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: yaml}}
	runner := NewAgentPhaseRunner(store, executor, nil, filepath.Join(t.TempDir(), "artifacts"), func(_ context.Context, cmd CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "validator", Target: "codex", Env: "test"}); err == nil {
		t.Fatal("regression started without a matched deployment")
	}
}

func TestPhaseResultRegressionFreshnessRejectsEarlierCycleAndEnvironmentMismatch(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-regression-rules", CaseRegressionValidating)
	input := RegressionValidationInput{OriginalScenarioHash: "scenario", ExpectedFixCommits: map[string]string{"api": "fix-1"}, ObservedDeploymentVersion: "version-1", TargetEnvironment: "test"}
	inputJSON, _ := json.Marshal(input)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseRegression, AttemptRegression)
	attempt.InputJSON = inputJSON
	now := time.Now().UTC()
	if err := store.RecordDeploymentObservation(context.Background(), DeploymentObservation{ID: "deployment-regression-rules", CaseID: incident.ID, Environment: "test", ExpectedCommits: map[string]string{"api": "fix-1"}, VerificationSource: "test", ObservedVersion: "version-1", ObservedCommits: map[string]string{"api": "fix-1"}, VerifiedAt: &now, Result: DeploymentResultMatched}, "deployment-regression-rules"); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, filepath.Join(t.TempDir(), "artifacts"), nil)
	evidencePath := filepath.Join(t.TempDir(), "fresh.har")
	if err := os.WriteFile(evidencePath, []byte(`{"status":200}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileTime := attempt.StartedAt.Add(2 * time.Second)
	if err := os.Chtimes(evidencePath, fileTime, fileTime); err != nil {
		t.Fatal(err)
	}
	makeResult := func(captured time.Time, environment string) PhaseResult {
		validation := ValidationResult{VerificationStatus: "fixed_verified", Environment: "test", ScenarioHash: "scenario", Evidence: []ArtifactReference{{Kind: "har", Path: evidencePath, CapturedAt: captured, Environment: environment, Version: "version-1", RedactionStatus: RedactionStatusNotRequired}}}
		output, _ := json.Marshal(validation)
		return PhaseResult{Outcome: PhaseOutcomeFixedVerified, OutputJSON: output, ArtifactInputs: validation.Evidence}
	}
	if err := runner.validateRegressionEvidence(context.Background(), attempt, makeResult(attempt.StartedAt.Add(-time.Second), "test")); err == nil {
		t.Fatal("accepted evidence captured before the current attempt")
	}
	if err := runner.validateRegressionEvidence(context.Background(), attempt, makeResult(attempt.StartedAt.Add(time.Second), "prod")); err == nil {
		t.Fatal("accepted evidence from a different environment")
	}
	if err := runner.validateRegressionEvidence(context.Background(), attempt, makeResult(attempt.StartedAt.Add(time.Second), "test")); err != nil {
		t.Fatalf("rejected fresh matched evidence: %v", err)
	}
}

func TestAgentPhaseRunnerRejectsEarlierArtifactBytesAtNewTouchedPath(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-artifact-reuse", CaseRegressionValidating)
	first := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	root := filepath.Join(resolvedTempDir(t), "artifacts")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "original.har")
	if err := os.WriteFile(original, []byte(`{"same":"bytes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterArtifact(context.Background(), store, ArtifactInput{ArtifactsRoot: root, SourcePath: original, CaseID: incident.ID, AttemptID: first.ID, Kind: "har", Environment: "test", RedactionStatus: RedactionStatusNotRequired}); err != nil {
		t.Fatal(err)
	}
	first.Status = AttemptStatusSucceeded
	finished := time.Now().UTC()
	first.FinishedAt = &finished
	if err := store.FinishAttempt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "attempt-artifact-reuse-regression"
	second.Phase, second.Mode = PhaseRegression, AttemptRegression
	second.StartedAt = time.Now().UTC()
	if err := store.CreateAttempt(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "touched-copy.har")
	if err := os.WriteFile(copyPath, []byte(`{"same":"bytes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil)
	err := runner.registerArtifacts(context.Background(), second, []ArtifactReference{{Kind: "har", Path: copyPath, CapturedAt: time.Now().UTC(), Environment: "test", RedactionStatus: RedactionStatusNotRequired}})
	if !errors.Is(err, ErrEvidenceArtifactReused) {
		t.Fatalf("reused bytes error = %v", err)
	}
}

func statusForRunningPhase(phase Phase) CaseStatus {
	switch phase {
	case PhaseValidation:
		return CaseValidating
	case PhaseInvestigation:
		return CaseInvestigating
	case PhaseFix:
		return CaseFixing
	default:
		return CaseRegressionValidating
	}
}

func createPhaseRunnerAttempt(t *testing.T, store *CaseStore, incident IncidentCase, phase Phase, mode AttemptMode) PhaseAttempt {
	t.Helper()
	attempt := PhaseAttempt{ID: "attempt-" + incident.ID, CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: phase, Mode: mode, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC()}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestPhaseResultRejectsUnknownFieldsAndPhaseModeMismatch(t *testing.T) {
	if _, err := ParseValidationResult([]byte("verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\nextra: true\n")); err == nil {
		t.Fatal("accepted unknown validation field")
	}
	attempt := PhaseAttempt{Phase: PhaseValidation, Mode: AttemptReproduce}
	if _, err := ParsePhaseResult(attempt, []byte("verification_status: fixed_verified\nenvironment: test\nevidence: []\ngaps: []\n")); err == nil {
		t.Fatal("accepted regression-only status in reproduce mode")
	}
	attempt = PhaseAttempt{Phase: PhaseRegression, Mode: AttemptRegression}
	if _, err := ParsePhaseResult(attempt, []byte("verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n")); err == nil {
		t.Fatal("accepted reproduce-only status in regression mode")
	}
}

func TestPhaseResultFixRequiresPushRemoteAndPassingTests(t *testing.T) {
	base := "fix_status: fixed_pushed\nenvironment: test\nbranches:\n  - repo: api\n    base_branch: test\n    fix_branch: fix/bug\n    commit: deadbeef\n    pushed: true\n    target_environment_branch: test\n    push_remote: origin\nchanges: []\ntests:\n  - repo: api\n    commit: deadbeef\n    command: go test ./...\n    result: passed\ndeployment_notice: deploy\nrisks: []\nblocked_reason: ''\nevidence: []\n"
	if _, err := ParseFixResult([]byte(base)); err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string]string{
		"missing remote": strings.Replace(base, "    push_remote: origin\n", "    push_remote: ''\n", 1),
		"no tests":       strings.Replace(base, "tests:\n  - repo: api\n    commit: deadbeef\n    command: go test ./...\n    result: passed\n", "tests: []\n", 1),
		"failed test":    strings.Replace(base, "    result: passed\n", "    result: failed\n", 1),
		"wrong commit":   strings.Replace(base, "    commit: deadbeef\n    command: go test", "    commit: other\n    command: go test", 1),
		"empty command":  strings.Replace(base, "    command: go test ./...\n", "    command: ''\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFixResult([]byte(invalid)); err == nil {
				t.Fatal("accepted unsafe fixed_pushed result")
			}
		})
	}
}

func TestRegressionPromptCarriesDeploymentAndFreshEvidenceContract(t *testing.T) {
	input := RegressionValidationInput{
		OriginalReproduction:       "submit the same checkout",
		OriginalScenarioHash:       "scenario-sha256",
		ExpectedFixCommits:         map[string]string{"api": "deadbeef"},
		ObservedDeploymentVersion:  "api:test@deadbeef",
		TargetEnvironment:          "test",
		OriginalEvidenceReferences: []string{"artifact-old"},
	}
	prompt := BuildRegressionValidationPrompt(Bug{ID: "42"}, BotRef{Env: "test"}, input)
	for _, required := range []string{"scenario-sha256", "api: deadbeef", "api:test@deadbeef", "artifact-old", "test", "fresh", "request_id", "captured_at", "不得读取业务源码", "不得分析根因"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
}

func TestAgentPhaseRunnerEventSinkWorksWithoutLegacyProjection(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-event-sink", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}, event: InvestigationEvent{Type: "agent_message", Message: "event"}}
	done := make(chan struct{}, 1)
	events := make(chan InvestigationEvent, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, t.TempDir(), func(context.Context, CompleteAttemptCommand) error { done <- struct{}{}; return nil })
	runner.SetEventSink(func(_ InvestigationRun, event InvestigationEvent) { events <- event })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	<-done
	select {
	case event := <-events:
		if event.Meta["case_id"] != incident.ID || event.Meta["attempt_id"] != attempt.ID {
			t.Fatalf("untagged event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event sink received no event")
	}
}

func TestAgentPhaseRunnerRejectsAdapterTargetMismatch(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-target-mismatch", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, t.TempDir(), func(context.Context, CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "openclaw"}); err == nil {
		t.Fatal("accepted bot target that differs from persisted attempt")
	}
}

func TestAgentPhaseRunnerFixPromptIncludesAuthorizedStructuredInput(t *testing.T) {
	store := newOrchestratorStore(t)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, t.TempDir(), func(context.Context, CompleteAttemptCommand) error { return nil })
	attempt := PhaseAttempt{Phase: PhaseFix, InputJSON: []byte(`{"root_cause":"authorized-race-in-cache"}`)}
	prompt, err := runner.promptForAttempt(attempt, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "authorized-race-in-cache") || !strings.Contains(prompt, "结构化阶段输入") {
		t.Fatalf("fix prompt lost authorized input:\n%s", prompt)
	}
}

func TestAgentPhaseRunnerAccumulatesUsageAcrossReadOnlyRetry(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-retry-usage", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n", Usage: AgentUsage{InputTokens: 4, OutputTokens: 3}}, errors: []error{errors.New("retry")}}
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, t.TempDir(), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.Usage.InputTokens != 8 || command.Usage.OutputTokens != 6 {
		t.Fatalf("retry usage = %+v", command.Usage)
	}
}

func TestAgentPhaseRunnerInvokesCompletionExactlyOnceEvenWhenItFails(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-pending-completion", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}}
	callbacks := make(chan int, 2)
	var callbackCalls int
	runner := NewAgentPhaseRunner(store, executor, nil, t.TempDir(), func(context.Context, CompleteAttemptCommand) error {
		callbackCalls++
		callbacks <- callbackCalls
		return errors.New("store failure")
	})
	start := func() {
		if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
	start()
	if call := <-callbacks; call != 1 {
		t.Fatalf("first callback = %d", call)
	}
	time.Sleep(20 * time.Millisecond)
	start()
	select {
	case call := <-callbacks:
		t.Fatalf("duplicate completion callback = %d", call)
	case <-time.After(100 * time.Millisecond):
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 1 {
		t.Fatalf("agent reran %d times while retrying completion", executor.calls)
	}
}

func TestAgentPhaseRunnerRejectsUnboundRegressionInputBeforeProcessStart(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-unbound-regression", CaseRegressionValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseRegression, AttemptRegression)
	executor := &phaseExecutorStub{}
	runner := NewAgentPhaseRunner(store, executor, nil, t.TempDir(), func(context.Context, CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err == nil {
		t.Fatal("started regression without scenario/deployment/commit binding")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 0 {
		t.Fatalf("unbound regression executed %d processes", executor.calls)
	}
}
