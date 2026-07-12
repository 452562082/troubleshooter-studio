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

const validReproducedPhaseYAML = "verification_status: reproduced\nenvironment: test\nobserved_behavior: timeout\nexpected_behavior: success\nevidence:\n  - kind: api\n    path: evidence.json\n    environment: test\n    redaction_status: not_required\ngaps: []\n"

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
			document := "verification_status: " + tc.status + "\nenvironment: test\nevidence: []\ngaps: []\n"
			if tc.status == "reproduced" {
				document = "verification_status: reproduced\nenvironment: test\nobserved_behavior: timeout\nexpected_behavior: success\nevidence:\n  - kind: api\n    path: response.json\n    environment: test\n    redaction_status: not_required\ngaps: []\n"
			}
			got, err := ParseValidationResult([]byte(document))
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

func TestValidationReproducedRequiresCompleteScenarioAndEvidence(t *testing.T) {
	valid := "verification_status: reproduced\nenvironment: test\nobserved_behavior: timeout\nexpected_behavior: success\nevidence:\n  - kind: api\n    path: response.json\n    environment: test\n    redaction_status: not_required\ngaps: []\n"
	if _, err := ParseValidationResult([]byte(valid)); err != nil {
		t.Fatalf("complete reproduced result rejected: %v", err)
	}
	for name, document := range map[string]string{
		"missing observed behavior": strings.Replace(valid, "observed_behavior: timeout\n", "", 1),
		"missing expected behavior": strings.Replace(valid, "expected_behavior: success\n", "", 1),
		"missing evidence":          strings.Replace(valid, "evidence:\n  - kind: api\n    path: response.json\n    environment: test\n    redaction_status: not_required\n", "evidence: []\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseValidationResult([]byte(document)); err == nil {
				t.Fatal("accepted incomplete reproduced result")
			}
		})
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

type phaseExecutorFunc func(context.Context, string, BotRef, string, func(InvestigationEvent)) (PhaseExecutionResult, error)

func (fn phaseExecutorFunc) ExecutePhase(ctx context.Context, id string, bot BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	return fn(ctx, id, bot, prompt, emit)
}
func (phaseExecutorFunc) CancelPhase(context.Context, string) error { return nil }

type flakyCleanupStaging struct {
	attemptEvidenceStaging
	calls int
}

type lifecycleStaging struct {
	mu       sync.Mutex
	path     string
	cleanups int
	closes   int
}

func (s *lifecycleStaging) Path() string { return s.path }
func (s *lifecycleStaging) Capture(string) (capturedArtifactSource, error) {
	return capturedArtifactSource{}, errors.New("unexpected capture")
}
func (s *lifecycleStaging) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups++
	return nil
}
func (s *lifecycleStaging) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return nil
}
func (s *lifecycleStaging) lifecycle() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanups, s.closes
}

func (s *flakyCleanupStaging) Cleanup() error {
	s.calls++
	if s.calls == 1 {
		return errors.New("injected first cleanup failure")
	}
	return s.attemptEvidenceStaging.Cleanup()
}

func (s *phaseExecutorStub) ExecutePhase(_ context.Context, attemptID string, _ BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
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
	if strings.Contains(result.FinalYAML, "path: evidence.json") {
		if staging := stagingPathFromPrompt(prompt); staging != "" {
			_ = os.WriteFile(filepath.Join(staging, "evidence.json"), []byte(`{"status":"timeout"}`), 0o600)
		}
	}
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
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML, Usage: AgentUsage{InputTokens: 12, OutputTokens: 7}}, event: InvestigationEvent{Type: "agent_message", Message: "working"}}
	completed := make(chan CompleteAttemptCommand, 2)
	runner := NewAgentPhaseRunner(store, executor, legacy, phaseArtifactsRoot(t), func(_ context.Context, cmd CompleteAttemptCommand) error {
		completed <- cmd
		return nil
	})
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex", Env: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex", Env: "test"}); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex", Env: "test"}); err != nil {
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
		{PhaseValidation, AttemptReproduce, validReproducedPhaseYAML, 2},
		{PhaseInvestigation, "", "investigation_status: root_cause_ready\nenvironment: test\nroot_cause: race\nconfidence: high\nevidence: []\ngaps: []\n", 2},
		{PhaseFix, "", "fix_status: failed\nenvironment: test\nbranches: []\nchanges: []\ntests: []\ndeployment_notice: no deployment; fix failed\nrisks: []\nblocked_reason: failed\nevidence: []\n", 1},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-retry-"+string(tc.phase), statusForRunningPhase(tc.phase))
			attempt := createPhaseRunnerAttempt(t, store, incident, tc.phase, tc.mode)
			executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: tc.yaml}, errors: []error{errors.New("process exited")}}
			done := make(chan struct{}, 1)
			runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { done <- struct{}{}; return nil })
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
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, cmd CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex", Env: "test"}); err == nil {
		t.Fatal("regression started without a matched deployment")
	}
}

func TestPhaseResultRegressionFreshnessIgnoresClaimedTimeAndRejectsEnvironmentMismatch(t *testing.T) {
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
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), nil)
	evidencePath := filepath.Join(t.TempDir(), "fresh.har")
	if err := os.WriteFile(evidencePath, []byte(`{"status":200}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileTime := attempt.StartedAt.Add(2 * time.Second)
	if err := os.Chtimes(evidencePath, fileTime, fileTime); err != nil {
		t.Fatal(err)
	}
	makeResult := func(captured time.Time, environment string) PhaseResult {
		validation := ValidationResult{VerificationStatus: "fixed_verified", Environment: "test", ScenarioHash: "scenario", Evidence: []ArtifactReference{{Kind: "har", Path: evidencePath, CapturedAt: captured, Environment: environment, Version: "version-1", RequestID: "request-fresh", RedactionStatus: RedactionStatusNotRequired}}}
		output, _ := json.Marshal(validation)
		return PhaseResult{Outcome: PhaseOutcomeFixedVerified, OutputJSON: output, ArtifactInputs: validation.Evidence}
	}
	if err := runner.validateRegressionEvidence(context.Background(), attempt, makeResult(attempt.StartedAt.Add(-time.Second), "test")); err != nil {
		t.Fatalf("trusted agent-claimed timestamp: %v", err)
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
	staging, err := openAttemptEvidenceStaging(root, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	copyPath := filepath.Join(staging.Path(), "touched-copy.har")
	if err := os.WriteFile(copyPath, []byte(`{"same":"bytes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil)
	err = runner.registerArtifacts(context.Background(), second, staging, []ArtifactReference{{Kind: "har", Path: "touched-copy.har", CapturedAt: time.Now().UTC(), Environment: "test", RedactionStatus: RedactionStatusNotRequired}})
	if !errors.Is(err, ErrEvidenceArtifactReused) {
		t.Fatalf("reused bytes error = %v", err)
	}
}

func TestAgentPhaseRunnerOwnsEvidenceStagingAndUsesFstatMetadata(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-owned-staging", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	root := filepath.Join(resolvedTempDir(t), "artifacts")
	var staging string
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		staging = stagingPathFromPrompt(prompt)
		if staging == "" {
			return PhaseExecutionResult{}, errors.New("missing Studio staging path")
		}
		info, err := os.Stat(staging)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return PhaseExecutionResult{}, fmt.Errorf("staging=%q info=%v err=%v", staging, info, err)
		}
		entries, _ := os.ReadDir(staging)
		if len(entries) != 0 {
			return PhaseExecutionResult{}, fmt.Errorf("staging was not empty")
		}
		if err := os.WriteFile(filepath.Join(staging, "current.har"), []byte(`{"status":200}`), 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		old := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(staging, "current.har"), old, old); err != nil {
			return PhaseExecutionResult{}, err
		}
		if err := os.WriteFile(filepath.Join(staging, "concurrent-unclaimed.txt"), []byte("must not be registered"), 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		return PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nobserved_behavior: timeout\nexpected_behavior: success\nevidence:\n  - kind: har\n    path: current.har\n    captured_at: 2000-01-01T00:00:00Z\n    environment: test\n    redaction_status: redacted\ngaps: []\n"}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, root, func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.ErrorCode != "" {
		t.Fatalf("completion error = %s: %s", command.ErrorCode, command.ErrorMessage)
	}
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
	if !artifacts[0].CapturedAt.After(attempt.StartedAt) || artifacts[0].RedactionStatus != RedactionStatusNotRequired {
		t.Fatalf("trusted agent metadata instead of fstat/Studio scan: %+v", artifacts[0])
	}
	if staging == "" || !strings.Contains(staging, attempt.ID) {
		t.Fatalf("staging path = %q", staging)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("Studio staging directory was retained after completion: %v", err)
	}
}

func TestAgentPhaseRunnerRejectsOutsideAndFakeRedactedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		content string
	}{
		{name: "outside absolute", path: filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"), content: "safe"},
		{name: "fake redaction", path: "secret.txt", content: "authorization: Bearer abcdefghijklmnopqrstuvwxyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-staging-"+strings.ReplaceAll(tc.name, " ", "-"), CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			root := filepath.Join(resolvedTempDir(t), "artifacts-"+strings.ReplaceAll(tc.name, " ", "-"))
			var staging string
			executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
				staging = stagingPathFromPrompt(prompt)
				if staging == "" {
					return PhaseExecutionResult{}, errors.New("missing Studio staging path")
				}
				if !filepath.IsAbs(tc.path) {
					if err := os.WriteFile(filepath.Join(staging, tc.path), []byte(tc.content), 0o600); err != nil {
						return PhaseExecutionResult{}, err
					}
				}
				return PhaseExecutionResult{FinalYAML: fmt.Sprintf("verification_status: reproduced\nenvironment: test\nobserved_behavior: timeout\nexpected_behavior: success\nevidence:\n  - kind: command\n    path: %q\n    captured_at: 2099-01-01T00:00:00Z\n    environment: test\n    redaction_status: redacted\ngaps: []\n", tc.path)}, nil
			})
			completed := make(chan CompleteAttemptCommand, 1)
			runner := NewAgentPhaseRunner(store, executor, nil, root, func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
				t.Fatal(err)
			}
			command := <-completed
			if command.ErrorCode != "artifact_registration_failed" {
				t.Fatalf("error code = %q, message=%q", command.ErrorCode, command.ErrorMessage)
			}
			artifacts, _ := store.ListEvidenceArtifacts(context.Background(), incident.ID)
			if len(artifacts) != 0 {
				t.Fatalf("registered rejected artifacts: %+v", artifacts)
			}
			if _, err := os.Stat(staging); !os.IsNotExist(err) {
				t.Fatalf("rejected evidence staging was retained: %v", err)
			}
		})
	}
}

func TestAgentPhaseRunnerSecretScansStructuredOutputBeforeIntent(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-output-secret", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: reproduced\nenvironment: test\nobserved_behavior: 'authorization: Bearer abcdefghijklmnopqrstuvwxyz'\nexpected_behavior: success\nevidence:\n  - kind: api\n    path: evidence.json\n    environment: test\n    redaction_status: not_required\ngaps: []\n"}, event: InvestigationEvent{Type: "agent_message", Message: "authorization: Bearer streamed-secret", Raw: map[string]any{"authorization": "Bearer streamed-secret"}}}
	completed := make(chan CompleteAttemptCommand, 1)
	legacy := NewInvestigationStore(t.TempDir())
	runner := NewAgentPhaseRunner(store, executor, legacy, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return errors.New("stop after intent")
	})
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.ErrorCode != "sensitive_phase_output" || strings.Contains(string(command.OutputJSON), "Bearer") {
		t.Fatalf("unsafe completion command = %+v", command)
	}
	stored, _ := store.GetAttempt(context.Background(), attempt.ID)
	if strings.Contains(string(stored.OutputJSON), "Bearer") {
		t.Fatalf("secret persisted in intent: %s", stored.OutputJSON)
	}
	projected, err := legacy.Get(attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(projected.FinalMessage, "Bearer") || strings.Contains(projected.Error, "Bearer") {
		t.Fatalf("secret persisted in legacy projection: %+v", projected)
	}
	encodedProjection, _ := json.Marshal(projected)
	if strings.Contains(string(encodedProjection), "streamed-secret") {
		t.Fatalf("secret persisted in legacy event: %s", encodedProjection)
	}
}

func TestAgentPhaseRunnerEventSanitizationFailsClosed(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-event-fail-closed", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	legacy := NewInvestigationStore(t.TempDir())
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, legacy, phaseArtifactsRoot(t), nil)
	events := make(chan InvestigationEvent, 1)
	runner.SetEventSink(func(_ InvestigationRun, event InvestigationEvent) { events <- event })
	if err := legacy.ProjectAttempt(InvestigationRun{ID: attempt.ID, BugID: incident.BugID, Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}

	runner.projectEvent(attempt, InvestigationEvent{
		Type:    "agent_message",
		Message: "apparently safe",
		Raw:     map[string]any{"secret": "authorization: Bearer raw-secret", "unmarshalable": make(chan struct{})},
		Meta:    map[string]any{"secret": "authorization: Bearer metadata-secret"},
	})

	event := <-events
	if event.Message != "[sensitive phase event suppressed]" || event.Raw != nil {
		t.Fatalf("event was not suppressed: %+v", event)
	}
	for _, key := range []string{"case_id", "attempt_id", "cycle_number", "phase"} {
		if _, ok := event.Meta[key]; !ok {
			t.Fatalf("Studio metadata %q missing: %+v", key, event.Meta)
		}
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "raw-secret") || strings.Contains(string(encoded), "metadata-secret") || strings.Contains(string(encoded), "unmarshalable") {
		t.Fatalf("agent-controlled sensitive event data survived: %s", encoded)
	}
	projected, err := legacy.Get(attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projection), "raw-secret") || strings.Contains(string(projection), "metadata-secret") {
		t.Fatalf("sensitive event data persisted: %s", projection)
	}
}

func TestAgentPhaseRunnerCancellationCleansEvidenceStaging(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancel-staging", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	stagingReady := make(chan string, 1)
	executor := phaseExecutorFunc(func(ctx context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		staging := stagingPathFromPrompt(prompt)
		if err := os.WriteFile(filepath.Join(staging, "unscanned-secret.txt"), []byte("authorization: Bearer cancelled-secret"), 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		stagingReady <- staging
		<-ctx.Done()
		return PhaseExecutionResult{}, ctx.Err()
	})
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error {
		t.Fatal("cancelled run invoked completion")
		return nil
	})
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	staging := <-stagingReady
	if err := runner.Cancel(context.Background(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(staging)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled staging retained: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentPhaseRunnerDeferredCleanupRetriesAfterFirstFailure(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cleanup-retry", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	owned, err := openAttemptEvidenceStaging(phaseArtifactsRoot(t), attempt.ID+"-cleanup-retry")
	if err != nil {
		t.Fatal(err)
	}
	staging := &flakyCleanupStaging{attemptEvidenceStaging: owned}
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: not_reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}}
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), nil)
	claimToken := "cleanup-retry-claim"
	if err := store.ClaimRunnableAttempt(context.Background(), AttemptRunClaim{Attempt: attempt, ClaimToken: claimToken}); err != nil {
		t.Fatal(err)
	}
	runner.run(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}, "prompt", staging, incident.Version, claimToken, func(context.Context, CompleteAttemptCommand) error { return nil })
	if staging.calls != 2 {
		t.Fatalf("cleanup calls = %d, want initial failure plus deferred retry", staging.calls)
	}
	if _, err := os.Stat(owned.Path()); !os.IsNotExist(err) {
		t.Fatalf("staging retained after deferred retry: %v", err)
	}
}

func stagingPathFromPrompt(prompt string) string {
	const marker = "STUDIO_EVIDENCE_STAGING_DIR="
	index := strings.Index(prompt, marker)
	if index < 0 {
		return ""
	}
	line := prompt[index+len(marker):]
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	return strings.TrimSpace(line)
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
	if _, err := store.db.Exec(`UPDATE incident_cases SET current_attempt_id = ?, selected_bot_key = ? WHERE id = ?`, attempt.ID, attempt.BotKey, incident.ID); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func phaseArtifactsRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(resolvedTempDir(t), "phase-artifacts")
}

func TestAgentPhaseRunnerPreflightRequiresExactCurrentPersistedAttempt(t *testing.T) {
	mutations := map[string]func(*PhaseAttempt){
		"case":        func(a *PhaseAttempt) { a.CaseID = "other" },
		"cycle":       func(a *PhaseAttempt) { a.CycleNumber++ },
		"phase":       func(a *PhaseAttempt) { a.Phase, a.Mode = PhaseInvestigation, "" },
		"mode":        func(a *PhaseAttempt) { a.Mode = AttemptRegression },
		"target":      func(a *PhaseAttempt) { a.AgentTarget = "openclaw" },
		"bot":         func(a *PhaseAttempt) { a.BotKey = "other" },
		"input bytes": func(a *PhaseAttempt) { a.InputJSON = []byte(`{"different":true}`) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-preflight-"+strings.ReplaceAll(name, " ", "-"), CaseValidating)
			persisted := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			caller := persisted.Clone()
			mutate(&caller)
			executor := &phaseExecutorStub{}
			runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
			if err := runner.Start(context.Background(), caller, Bug{ID: incident.BugID}, BotRef{Key: caller.BotKey, Target: caller.AgentTarget}); err == nil {
				t.Fatal("accepted caller attempt that differs from persisted attempt")
			}
			executor.mu.Lock()
			defer executor.mu.Unlock()
			if executor.calls != 0 {
				t.Fatalf("executor calls = %d", executor.calls)
			}
		})
	}
}

func TestAgentPhaseRunnerPreflightRejectsMissingDetachedAndTerminalAttempts(t *testing.T) {
	for _, state := range []string{"missing-current", "detached-current", "terminal"} {
		t.Run(state, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-preflight-"+state, CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			switch state {
			case "missing-current":
				_, _ = store.db.Exec(`UPDATE incident_cases SET current_attempt_id = '' WHERE id = ?`, incident.ID)
			case "detached-current":
				_, _ = store.db.Exec(`UPDATE incident_cases SET current_attempt_id = 'other-attempt' WHERE id = ?`, incident.ID)
			case "terminal":
				_, _ = store.db.Exec(`UPDATE phase_attempts SET status = ? WHERE id = ?`, AttemptStatusSucceeded, attempt.ID)
			}
			executor := &phaseExecutorStub{}
			runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget}); err == nil {
				t.Fatalf("accepted %s attempt", state)
			}
			executor.mu.Lock()
			defer executor.mu.Unlock()
			if executor.calls != 0 {
				t.Fatalf("executor calls = %d", executor.calls)
			}
		})
	}
}

func TestAgentPhaseRunnerPreflightBindsCaseStatusCycleAndSelectedBot(t *testing.T) {
	for _, mismatch := range []string{"status", "cycle", "selected-bot"} {
		t.Run(mismatch, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-snapshot-"+mismatch, CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			switch mismatch {
			case "status":
				_, _ = store.db.Exec(`UPDATE incident_cases SET status=? WHERE id=?`, CaseWaitingEvidence, incident.ID)
			case "cycle":
				_, _ = store.db.Exec(`UPDATE incident_cases SET cycle_number=2 WHERE id=?`, incident.ID)
			case "selected-bot":
				_, _ = store.db.Exec(`UPDATE incident_cases SET selected_bot_key='other' WHERE id=?`, incident.ID)
			}
			executor := &phaseExecutorStub{}
			runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err == nil {
				t.Fatalf("accepted Case %s mismatch", mismatch)
			}
			executor.mu.Lock()
			defer executor.mu.Unlock()
			if executor.calls != 0 {
				t.Fatalf("executor calls = %d", executor.calls)
			}
		})
	}
}

func TestAgentPhaseRunnerAvoidsStagingForPreflightFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		phase      Phase
		mode       AttemptMode
		input      []byte
		completion PhaseCompletionFunc
	}{
		{name: "prompt error", phase: PhaseRegression, mode: AttemptRegression, input: []byte(`{}`), completion: func(context.Context, CompleteAttemptCommand) error { return nil }},
		{name: "missing callback", phase: PhaseValidation, mode: AttemptReproduce, input: []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-start-cleanup-"+strings.ReplaceAll(tc.name, " ", "-"), statusForRunningPhase(tc.phase))
			attempt := createPhaseRunnerAttempt(t, store, incident, tc.phase, tc.mode)
			attempt.InputJSON = tc.input
			if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json=? WHERE id=?`, string(tc.input), attempt.ID); err != nil {
				t.Fatal(err)
			}
			staging := &lifecycleStaging{path: filepath.Join(t.TempDir(), "owned")}
			runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), tc.completion)
			runner.openStaging = func(string, string) (attemptEvidenceStaging, error) { return staging, nil }
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err == nil {
				t.Fatal("Start succeeded")
			}
			if cleanups, closes := staging.lifecycle(); cleanups != 0 || closes != 0 {
				t.Fatalf("staging lifecycle cleanup=%d close=%d", cleanups, closes)
			}
		})
	}
}

func TestAgentPhaseRunnerConcurrentFixStartCreatesOneCheckpointStaging(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-start-cleanup-race", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	created := make(chan *lifecycleStaging, 1)
	release := make(chan struct{})
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "invalid"}}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	runner.openStaging = func(string, string) (attemptEvidenceStaging, error) {
		staging := &lifecycleStaging{path: filepath.Join(t.TempDir(), attempt.ID+"-owned")}
		created <- staging
		<-release
		return staging, nil
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"})
		}()
	}
	first := <-created
	close(release)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		firstCleanups, firstCloses := first.lifecycle()
		if firstCleanups == 1 && firstCloses == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging lifecycle=%d/%d", firstCleanups, firstCloses)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case extra := <-created:
		t.Fatalf("duplicate Start created staging %+v", extra)
	default:
	}
}

func TestAgentPhaseRunnerCancelDuringStagingPreflightPreventsNonFixExecutor(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancel-staging-preflight", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	entered := make(chan struct{})
	release := make(chan struct{})
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML}}
	var orchestrator *CaseOrchestrator
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(ctx context.Context, command CompleteAttemptCommand) error {
		_, err := orchestrator.CompleteAttempt(ctx, command)
		return err
	})
	runner.openStaging = func(string, string) (attemptEvidenceStaging, error) {
		close(entered)
		<-release
		return &lifecycleStaging{path: filepath.Join(t.TempDir(), "cancel-preflight")}, nil
	}
	orchestrator = NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	startErr := make(chan error, 1)
	go func() {
		startErr <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"})
	}()
	<-entered
	cancelled, err := orchestrator.CancelAttempt(context.Background(), CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel-staging-preflight", ActorID: "alice"})
	if err != nil || cancelled.Status != CaseWaitingEvidence {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	close(release)
	if err := <-startErr; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrAttemptAlreadyFinished) {
		t.Fatalf("start err=%v", err)
	}
	time.Sleep(50 * time.Millisecond)
	executor.mu.Lock()
	calls := executor.calls
	executor.mu.Unlock()
	if calls != 0 {
		t.Fatalf("cancelled preflight started executor %d times", calls)
	}
}

func TestAgentPhaseRunnerCancelBeforeAtomicFixClaimCreatesNoCheckpointOrExecutor(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancel-fix-claim", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	entered := make(chan struct{})
	release := make(chan struct{})
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "must not execute"}}
	var orchestrator *CaseOrchestrator
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(ctx context.Context, command CompleteAttemptCommand) error {
		_, err := orchestrator.CompleteAttempt(ctx, command)
		return err
	})
	runner.openStaging = func(string, string) (attemptEvidenceStaging, error) {
		close(entered)
		<-release
		return &lifecycleStaging{path: filepath.Join(t.TempDir(), attempt.ID+"-cancelled")}, nil
	}
	orchestrator = NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	startErr := make(chan error, 1)
	go func() {
		startErr <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"})
	}()
	<-entered
	if _, err := orchestrator.CancelAttempt(context.Background(), CancelAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "cancel-before-fix-claim", ActorID: "alice"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-startErr; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrAttemptAlreadyFinished) {
		t.Fatalf("start err=%v", err)
	}
	time.Sleep(50 * time.Millisecond)
	executor.mu.Lock()
	calls := executor.calls
	executor.mu.Unlock()
	if calls != 0 {
		t.Fatalf("cancelled fix started executor %d times", calls)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
}

func TestAgentPhaseRunnerPhaseOutlivesSchedulingContext(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-scheduling-context", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	started := make(chan struct{})
	inspect := make(chan struct{})
	executorContext := make(chan error, 1)
	executor := phaseExecutorFunc(func(ctx context.Context, _ string, _ BotRef, _ string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		close(started)
		<-inspect
		executorContext <- ctx.Err()
		return PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML}, nil
	})
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	schedulingCtx, cancelScheduling := context.WithCancel(context.Background())
	if err := runner.Start(schedulingCtx, attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	<-started
	cancelScheduling()
	close(inspect)
	if err := <-executorContext; err != nil {
		t.Fatalf("durable phase inherited expired scheduling context: %v", err)
	}
}

func TestOrchestratorScheduledValidationAndFixOutliveSchedulingContext(t *testing.T) {
	for _, phase := range []Phase{PhaseValidation, PhaseFix} {
		t.Run(string(phase), func(t *testing.T) {
			store := newOrchestratorStore(t)
			status := CaseValidating
			mode := AttemptReproduce
			if phase == PhaseFix {
				status = CaseFixing
				mode = ""
			}
			incident := createWorkflowCase(t, store, "case-orchestrator-schedule-"+string(phase), status)
			attempt := createPhaseRunnerAttempt(t, store, incident, phase, mode)
			started := make(chan struct{})
			inspect := make(chan struct{})
			release := make(chan struct{})
			executorContext := make(chan error, 1)
			executor := phaseExecutorFunc(func(ctx context.Context, _ string, _ BotRef, _ string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
				close(started)
				<-inspect
				executorContext <- ctx.Err()
				<-release
				return PhaseExecutionResult{}, errors.New("test executor stopped")
			})
			runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
			orchestrator := NewCaseOrchestrator(store, runner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
			orchestrator.scheduleTimeout = time.Second
			if err := orchestrator.startPhase(attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
				t.Fatal(err)
			}
			<-started
			close(inspect)
			if err := <-executorContext; err != nil {
				t.Fatalf("%s executor inherited orchestrator scheduling context: %v", phase, err)
			}
			if phase == PhaseFix {
				if checkpoint, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || !found || checkpoint.AttemptID != attempt.ID {
					t.Fatalf("live fix checkpoint=%+v found=%v err=%v", checkpoint, found, err)
				}
			}
			if err := runner.Cancel(context.Background(), attempt.ID); err != nil {
				t.Fatal(err)
			}
			close(release)
		})
	}
}

func TestAgentPhaseRunnerCancelledSchedulingContextBeforeClaimStartsNoExecutor(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cancelled-scheduling-context", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{}
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Start(ctx, attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err=%v, want context.Canceled", err)
	}
	executor.mu.Lock()
	calls := executor.calls
	executor.mu.Unlock()
	if calls != 0 {
		t.Fatalf("cancelled scheduling context started executor %d times", calls)
	}
}

func TestAgentPhaseRunnerLegacyPreviewOmitsRegressionSecrets(t *testing.T) {
	store, incident, _, _ := prepareRegressionCase(t, 1)
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	attempt, err := orchestrator.StartRegression(context.Background(), incident.ID, incident.Version)
	if err != nil {
		t.Fatal(err)
	}
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	input.OriginalReproduction = "Authorization: Bearer preview-secret Cookie: sid=cookie-secret token=token-secret"
	attempt.InputJSON, _ = json.Marshal(input)
	if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json=? WHERE id=?`, string(attempt.InputJSON), attempt.ID); err != nil {
		t.Fatal(err)
	}
	legacy := NewInvestigationStore(t.TempDir())
	done := make(chan struct{}, 1)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: fixed_verified\nenvironment: test\nevidence: []\ngaps: []\n"}}, legacy, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { done <- struct{}{}; return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "validator", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	<-done
	raw, err := os.ReadFile(legacy.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"preview-secret", "cookie-secret", "token-secret", "Authorization", "Cookie"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("legacy runs.json contains %q: %s", secret, raw)
		}
	}
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
	base := "fix_status: fixed_pushed\nenvironment: test\nbranches:\n  - repo: api\n    base_branch: test\n    fix_branch: fix/bug\n    commit: deadbeef\n    pushed: true\n    target_environment_branch: test\n    push_remote: origin\nchanges:\n  - repo: api\n    summary: safe fix\ntests:\n  - repo: api\n    commit: deadbeef\n    command: go test ./...\n    result: passed\ndeployment_notice: deploy\nrisks: []\nblocked_reason: ''\nevidence: []\n"
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
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML}, event: InvestigationEvent{Type: "agent_message", Message: "event"}}
	done := make(chan struct{}, 1)
	events := make(chan InvestigationEvent, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { done <- struct{}{}; return nil })
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
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "openclaw"}); err == nil {
		t.Fatal("accepted bot target that differs from persisted attempt")
	}
}

func TestAgentPhaseRunnerFixPromptIncludesAuthorizedStructuredInput(t *testing.T) {
	store := newOrchestratorStore(t)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	attempt := PhaseAttempt{Phase: PhaseFix, InputJSON: []byte(`{"root_cause":"authorized-race-in-cache"}`)}
	prompt, err := runner.promptForAttempt(attempt, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "authorized-race-in-cache") || !strings.Contains(prompt, "结构化阶段输入") {
		t.Fatalf("fix prompt lost authorized input:\n%s", prompt)
	}
}

func TestAgentPhaseRunnerFixCheckpointIsConsumedBeforeStagingCleanup(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-fix-checkpoint-normal", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	result := checkpointFixResult("api", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	final, _ := json.Marshal(result)
	parsed, err := ParsePhaseResult(attempt, final)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolvedTempDir(t), "normal-fix-checkpoint")
	var stagingPath string
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		stagingPath = stagingPathFromPrompt(prompt)
		manifest := FixCheckpointManifest{Kind: fixCheckpointManifestKind, Version: fixCheckpointManifestVersion, CaseID: incident.ID, AttemptID: attempt.ID, State: "pushed", Result: result}
		encoded, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(stagingPath, fixCheckpointManifestName), encoded, 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		return PhaseExecutionResult{FinalYAML: string(final)}, nil
	})
	done := make(chan error, 1)
	var orchestrator *CaseOrchestrator
	runner := NewAgentPhaseRunner(store, executor, nil, root, func(ctx context.Context, command CompleteAttemptCommand) error {
		_, err := orchestrator.CompleteAttempt(ctx, command)
		done <- err
		return err
	})
	orchestrator = NewCaseOrchestrator(store, runner, &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: parsed.CodeChanges}}, nil)
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(stagingPath)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging retained after transaction: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentPhaseRunnerPreservesFixCheckpointWhenRemoteInspectionUnavailable(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-fix-checkpoint-transient", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	result := checkpointFixResult("api", strings.Repeat("a", 40))
	final, _ := json.Marshal(result)
	root := filepath.Join(resolvedTempDir(t), "transient-fix-checkpoint")
	var stagingPath string
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		stagingPath = stagingPathFromPrompt(prompt)
		manifest := FixCheckpointManifest{Kind: fixCheckpointManifestKind, Version: fixCheckpointManifestVersion, CaseID: incident.ID, AttemptID: attempt.ID, State: "pushed", Result: result}
		encoded, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(stagingPath, fixCheckpointManifestName), encoded, 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		return PhaseExecutionResult{FinalYAML: string(final)}, nil
	})
	done := make(chan error, 1)
	var orchestrator *CaseOrchestrator
	runner := NewAgentPhaseRunner(store, executor, nil, root, func(ctx context.Context, command CompleteAttemptCommand) error {
		_, err := orchestrator.CompleteAttempt(ctx, command)
		done <- err
		return err
	})
	runner.completionReconcileAttempts = 1
	orchestrator = NewCaseOrchestrator(store, runner, &recordingGitIntegration{err: errors.New("temporary ssh outage")}, nil)
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrFixInspectionUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(stagingPath); err != nil {
		t.Fatalf("checkpoint staging was not preserved: %v", err)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || !found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	persisted, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil || persisted.Status != AttemptStatusRunning {
		t.Fatalf("attempt=%+v err=%v", persisted, err)
	}
	if _, found, err := parseCompletionIntent(persisted.OutputJSON); err != nil || !found {
		t.Fatalf("completion intent found=%v err=%v", found, err)
	}
}

func TestAgentPhaseRunnerReconcilesTransientRemoteWithoutRerunningAgent(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-fix-checkpoint-reconcile", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	result := checkpointFixResult("api", strings.Repeat("a", 40))
	final, _ := json.Marshal(result)
	parsed, err := ParsePhaseResult(attempt, final)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolvedTempDir(t), "reconcile-fix-checkpoint")
	stagingPaths := make(chan string, 1)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: string(final)}}
	wrappedExecutor := phaseExecutorFunc(func(ctx context.Context, attemptID string, bot BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
		stagingPath := stagingPathFromPrompt(prompt)
		stagingPaths <- stagingPath
		manifest := FixCheckpointManifest{Kind: fixCheckpointManifestKind, Version: fixCheckpointManifestVersion, CaseID: incident.ID, AttemptID: attempt.ID, State: "pushed", Result: result}
		encoded, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(stagingPath, fixCheckpointManifestName), encoded, 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		return executor.ExecutePhase(ctx, attemptID, bot, prompt, emit)
	})
	git := &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: parsed.CodeChanges}, fixErrors: []error{errors.New("ssh unavailable 1"), errors.New("ssh unavailable 2"), errors.New("ssh unavailable 3")}}
	var orchestrator *CaseOrchestrator
	runner := NewAgentPhaseRunner(store, wrappedExecutor, nil, root, func(ctx context.Context, command CompleteAttemptCommand) error {
		_, err := orchestrator.CompleteAttempt(ctx, command)
		return err
	})
	runner.completionReconcileDelay = time.Millisecond
	orchestrator = NewCaseOrchestrator(store, runner, git, nil)
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err != nil {
		t.Fatal(err)
	}
	stagingPath := <-stagingPaths
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := store.GetCase(context.Background(), incident.ID)
		if current.Status == CaseWaitingMergeApproval {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("case did not reconcile: %+v", current)
		}
		time.Sleep(time.Millisecond)
	}
	executor.mu.Lock()
	executorCalls := executor.calls
	executor.mu.Unlock()
	if executorCalls != 1 {
		t.Fatalf("agent process calls=%d", executorCalls)
	}
	git.mu.Lock()
	fixCalls := git.fixCalls
	git.mu.Unlock()
	if fixCalls != fixInspectionMaxAttempts+1 {
		t.Fatalf("remote inspection calls=%d", fixCalls)
	}
	if _, found, _ := store.GetFixCheckpoint(context.Background(), attempt.ID); found {
		t.Fatal("reconciled checkpoint row remains")
	}
	deadline = time.Now().Add(time.Second)
	for {
		_, statErr := os.Stat(stagingPath)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reconciled staging remains: %v", statErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentPhaseRunnerAccumulatesUsageAcrossReadOnlyRetry(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-retry-usage", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML, Usage: AgentUsage{InputTokens: 4, OutputTokens: 3}}, errors: []error{errors.New("retry")}}
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
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
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: validReproducedPhaseYAML}}
	callbacks := make(chan int, 2)
	var callbackCalls int
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error {
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
	executorCalls := executor.calls
	executor.mu.Unlock()
	if executorCalls != 1 {
		t.Fatalf("agent reran %d times while retrying completion", executorCalls)
	}
	stored, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := parseCompletionIntent(stored.OutputJSON); err != nil || !found {
		t.Fatalf("completion intent found=%v err=%v raw=%s", found, err, stored.OutputJSON)
	}
	recoveryRunner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, recoveryRunner, &recordingGitIntegration{}, &recordingDeploymentVerifier{})
	if err := orchestrator.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := store.GetCase(context.Background(), incident.ID)
	if recovered.Status != CaseReproduced && recovered.Status != CaseInvestigating {
		t.Fatalf("recovered case = %+v", recovered)
	}
}

func TestAgentPhaseRunnerRejectsUnboundRegressionInputBeforeProcessStart(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-unbound-regression", CaseRegressionValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseRegression, AttemptRegression)
	executor := &phaseExecutorStub{}
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err == nil {
		t.Fatal("started regression without scenario/deployment/commit binding")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 0 {
		t.Fatalf("unbound regression executed %d processes", executor.calls)
	}
}

func TestAgentPhaseRunnerRejectsRegressionEnvironmentDifferentFromCaseBeforeProcessStart(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-regression-env-mismatch", CaseRegressionValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseRegression, AttemptRegression)
	input, _ := json.Marshal(RegressionValidationInput{OriginalReproduction: "checkout", OriginalScenarioHash: "hash", ExpectedFixCommits: map[string]string{"api": "fix-1"}, ObservedDeploymentVersion: "version-1", TargetEnvironment: "prod"})
	attempt.InputJSON = input
	if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json=? WHERE id=?`, string(input), attempt.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.RecordDeploymentObservation(context.Background(), DeploymentObservation{ID: "obs-env-mismatch", CaseID: incident.ID, Environment: "prod", ExpectedCommits: map[string]string{"api": "fix-1"}, VerificationSource: "test", ObservedVersion: "version-1", ObservedCommits: map[string]string{"api": "fix-1"}, VerifiedAt: &now, Result: DeploymentResultMatched}, "obs-env-mismatch"); err != nil {
		t.Fatal(err)
	}
	executor := &phaseExecutorStub{}
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, BotRef{Key: "bot", Target: "codex"}); err == nil {
		t.Fatal("started regression for an environment different from Case")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d", executor.calls)
	}
}
