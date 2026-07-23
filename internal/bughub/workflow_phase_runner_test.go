package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
func (fn phaseExecutorFunc) ExecutePhaseWithAttachments(ctx context.Context, id string, bot BotRef, prompt string, attachments []PhaseAttachment, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	for _, attachment := range attachments {
		prompt += "\nFrozen final screenshot local path (read-only, original bytes): " + attachment.Path + "\n"
	}
	return fn(ctx, id, bot, prompt, emit)
}
func (phaseExecutorFunc) CancelPhase(context.Context, string) error { return nil }

type browserVerifierFunc func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error)

func (fn browserVerifierFunc) Execute(ctx context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
	return fn(ctx, request)
}

type browserPolicyResolverFunc func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error)

func (fn browserPolicyResolverFunc) ResolveBrowserPolicy(ctx context.Context, incident IncidentCase, bug Bug) (BrowserSecurityPolicy, error) {
	return fn(ctx, incident, bug)
}

func testBrowserApplicationPolicy(applicationOrigin string, additionalAllowedOrigins ...string) BrowserSecurityPolicy {
	allowedOrigins := append([]string{applicationOrigin}, additionalAllowedOrigins...)
	return BrowserSecurityPolicy{
		AllowedOrigins:     allowedOrigins,
		ApplicationOrigins: []string{applicationOrigin},
		StartOrigins:       []string{applicationOrigin},
	}
}

func verifiedBrowserArtifact(kind, path, environment string, content []byte) BrowserArtifactReference {
	digest := sha256.Sum256(content)
	return BrowserArtifactReference{Kind: kind, Path: path, Environment: environment, SHA256: fmt.Sprintf("%x", digest[:]), Size: int64(len(content))}
}

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

func (s *phaseExecutorStub) ExecutePhaseWithAttachments(ctx context.Context, attemptID string, bot BotRef, prompt string, _ []PhaseAttachment, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	return s.ExecutePhase(ctx, attemptID, bot, prompt, emit)
}

func TestAgentPhaseRunnerUsesDerivedValidatorWithoutChangingPersistedBaseBot(t *testing.T) {
	const validationYAML = "verification_status: insufficient_info\nenvironment: test\nevidence: []\ngaps: [missing browser evidence]\n"

	t.Run("validation", func(t *testing.T) {
		store := newOrchestratorStore(t)
		incident := createWorkflowCase(t, store, "case-phase-role-validation", CaseValidating)
		attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
		selected := installedPhaseRunnerBot(t, "bot", "codex")
		legacy := NewInvestigationStore(t.TempDir())
		executed := make(chan BotRef, 1)
		completed := make(chan struct{}, 1)
		executor := phaseExecutorFunc(func(_ context.Context, _ string, bot BotRef, _ string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
			executed <- bot
			return PhaseExecutionResult{FinalYAML: validationYAML}, nil
		})
		runner := NewAgentPhaseRunner(store, executor, legacy, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error {
			completed <- struct{}{}
			return nil
		})

		if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, selected); err != nil {
			t.Fatal(err)
		}
		assertValidatorExecutionBot(t, <-executed, selected)
		<-completed
		assertPersistedBaseBot(t, store, incident.ID, attempt.ID, selected.Key)
		projected, err := legacy.Get(attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if projected.BotKey != selected.Key+"#validator" {
			t.Fatalf("legacy execution bot key = %q", projected.BotKey)
		}
	})

	t.Run("regression", func(t *testing.T) {
		store, incident, _, _ := prepareRegressionCase(t, 1)
		reservationRunner := &recordingPhaseRunner{}
		orchestrator := NewCaseOrchestrator(store, reservationRunner, nil, nil)
		attempt, err := orchestrator.StartRegression(context.Background(), incident.ID, incident.Version)
		if err != nil {
			t.Fatal(err)
		}
		selected := installedPhaseRunnerBot(t, attempt.BotKey, attempt.AgentTarget)
		executed := make(chan BotRef, 1)
		completed := make(chan struct{}, 1)
		executor := phaseExecutorFunc(func(_ context.Context, _ string, bot BotRef, _ string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
			executed <- bot
			return PhaseExecutionResult{FinalYAML: validationYAML}, nil
		})
		runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error {
			completed <- struct{}{}
			return nil
		})

		if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, selected); err != nil {
			t.Fatal(err)
		}
		assertValidatorExecutionBot(t, <-executed, selected)
		<-completed
		assertPersistedBaseBot(t, store, incident.ID, attempt.ID, selected.Key)
	})

	t.Run("investigation", func(t *testing.T) {
		store := newOrchestratorStore(t)
		incident := createWorkflowCase(t, store, "case-phase-role-investigation", CaseInvestigating)
		attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
		selected := installedPhaseRunnerBot(t, "bot", "codex")
		executed := make(chan BotRef, 1)
		completed := make(chan struct{}, 1)
		executor := phaseExecutorFunc(func(_ context.Context, _ string, bot BotRef, _ string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
			executed <- bot
			return PhaseExecutionResult{FinalYAML: "investigation_status: insufficient_info\nenvironment: test\nconfidence: low\nevidence: []\ngaps: [missing trace]\n"}, nil
		})
		runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error {
			completed <- struct{}{}
			return nil
		})

		if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, selected); err != nil {
			t.Fatal(err)
		}
		if got := <-executed; got.Key != selected.Key || got.Path != selected.Path || got.Role != selected.Role {
			t.Fatalf("investigation execution bot = %+v", got)
		}
		<-completed
	})
}

func assertValidatorExecutionBot(t *testing.T, got, selected BotRef) {
	t.Helper()
	if got.Key != selected.Key+"#validator" || got.Role != "validator" || got.Path == selected.Path || filepath.Base(got.Path) != "base-validator" {
		t.Fatalf("validator execution bot = %+v", got)
	}
}

func assertPersistedBaseBot(t *testing.T, store *CaseStore, caseID, attemptID, baseKey string) {
	t.Helper()
	incident, err := store.GetCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if incident.SelectedBotKey != baseKey || attempt.BotKey != baseKey {
		t.Fatalf("persisted bot keys case=%q attempt=%q want=%q", incident.SelectedBotKey, attempt.BotKey, baseKey)
	}
}

func installedPhaseRunnerBot(t *testing.T, key, target string) BotRef {
	t.Helper()
	root := t.TempDir()
	basePath := filepath.Join(root, "base-troubleshooter")
	validatorPath := filepath.Join(root, "base-validator")
	for _, path := range []string{basePath, validatorPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return BotRef{
		Key:      key,
		Target:   target,
		Path:     basePath,
		SystemID: "base",
		Role:     "troubleshooter",
		Env:      "test",
		InternalAgents: []BotInternalAgent{
			{ID: "base-validator", Role: "validator"},
		},
	}
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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

func TestParseInvestigationResultAcceptsBoundedStructuredCallChain(t *testing.T) {
	result, err := ParseInvestigationResult([]byte(`
investigation_status: root_cause_ready
environment: test
root_cause: frontend called the wrong backend route
confidence: high
call_chain:
  - kind: frontend
    name: user search
    service: admin-web
    repo: admin-web
    revision: abc123
    protocol: http
    operation: GET /api/users
    file: src/search.ts
    line: 42
    precision: source_mapped
    evidence: initiator stack and matching source map
evidence: []
gaps: []
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CallChain) != 1 || result.CallChain[0].Repo != "admin-web" || result.CallChain[0].Line != 42 {
		t.Fatalf("call chain = %+v", result.CallChain)
	}
}

func TestParseInvestigationResultConservativelyDowngradesIncompleteCallChainPrecision(t *testing.T) {
	result, err := ParseInvestigationResult([]byte(`
investigation_status: insufficient_info
environment: test
confidence: medium
call_chain:
  - kind: service
    name: source without deployed revision
    repo: backend
    revision: ""
    file: internal/search.go
    line: 42
    precision: source_mapped
    evidence: current repository candidate
  - kind: service
    name: deployed revision without exact source line
    repo: backend
    revision: abc123
    line: 0
    precision: source_mapped
    evidence: deployment annotation
  - kind: service
    name: source claim without evidence
    repo: backend
    revision: abc123
    file: internal/search.go
    line: 42
    precision: source_mapped
    evidence: ""
  - kind: gateway
    name: runtime claim without evidence
    precision: runtime_verified
    evidence: ""
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes: []
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"static_candidate", "deployed_revision", "unavailable", "unavailable"}
	if len(result.CallChain) != len(want) {
		t.Fatalf("call chain = %+v", result.CallChain)
	}
	for index, precision := range want {
		if result.CallChain[index].Precision != precision {
			t.Errorf("call_chain[%d].precision = %q, want %q", index, result.CallChain[index].Precision, precision)
		}
	}
	if len(result.UncheckedScopes) != 1 || !strings.Contains(result.UncheckedScopes[0], "downgraded") {
		t.Fatalf("unchecked scopes = %+v", result.UncheckedScopes)
	}
}

func TestParsePhaseResultDowngradesLocationPrecisionWithoutBlockingReadyRootCause(t *testing.T) {
	parsed, err := ParsePhaseResult(PhaseAttempt{Phase: PhaseInvestigation}, []byte(`
investigation_status: root_cause_ready
environment: test
root_cause: frontend renders the same name twice
confidence: high
root_cause_type: code
remediation:
  mode: code_change
  target: frontend search card
  summary: suppress the duplicate fallback field
  verification: rerun the original search
call_chain:
  - kind: frontend
    name: search card
    repo: frontend
    revision: ""
    file: src/search-card.tsx
    line: 42
    precision: source_mapped
    evidence: current repository candidate
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes: []
`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Outcome != PhaseOutcomeRootCauseReady {
		t.Fatalf("outcome = %q", parsed.Outcome)
	}
	var result InvestigationResult
	if err := json.Unmarshal(parsed.OutputJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.InvestigationStatus != "root_cause_ready" || result.Confidence != "high" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.CallChain) != 1 || result.CallChain[0].Precision != "static_candidate" {
		t.Fatalf("call chain = %+v", result.CallChain)
	}
	if len(result.UncheckedScopes) != 0 {
		t.Fatalf("ready result retained optional precision limitation: %+v", result.UncheckedScopes)
	}
}

func TestParsePhaseResultRootCauseReadyDropsNonBlockingUncheckedScopes(t *testing.T) {
	parsed, err := ParsePhaseResult(PhaseAttempt{Phase: PhaseInvestigation}, []byte(`
investigation_status: root_cause_ready
environment: test
root_cause: frontend renders nick_name and text as duplicate headings
confidence: high
call_chain: []
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes:
  - optional ConfigMap query failed after the root cause was proven
  - source map was unavailable but the deployed bundle was verified
`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Outcome != PhaseOutcomeRootCauseReady {
		t.Fatalf("outcome = %q", parsed.Outcome)
	}
	var result InvestigationResult
	if err := json.Unmarshal(parsed.OutputJSON, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.UncheckedScopes) != 0 {
		t.Fatalf("root-cause-ready result retained non-blocking scopes: %+v", result.UncheckedScopes)
	}
}

func TestParsePhaseResultRoutesPrematureRootCauseToNeedsEvidence(t *testing.T) {
	tests := []struct {
		name       string
		confidence string
		gaps       string
		wantGap    string
	}{
		{name: "medium confidence", confidence: "medium", gaps: "[]", wantGap: "confidence"},
		{name: "blocking gaps", confidence: "high", gaps: "[missing deployed revision]", wantGap: "missing deployed revision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParsePhaseResult(PhaseAttempt{Phase: PhaseInvestigation}, []byte(fmt.Sprintf(`
investigation_status: root_cause_ready
environment: test
root_cause: frontend renders the same name twice
confidence: %s
call_chain: []
evidence: []
gaps: %s
`, test.confidence, test.gaps)))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Outcome != PhaseOutcomeNeedsEvidence {
				t.Fatalf("outcome = %q", parsed.Outcome)
			}
			var result InvestigationResult
			if err := json.Unmarshal(parsed.OutputJSON, &result); err != nil {
				t.Fatal(err)
			}
			if result.InvestigationStatus != "insufficient_info" || !strings.Contains(strings.Join(result.Gaps, "\n"), test.wantGap) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestParsePhaseResultRoutesValidationEvidenceGapBackToValidation(t *testing.T) {
	parsed, err := ParsePhaseResult(PhaseAttempt{Phase: PhaseInvestigation}, []byte(`
investigation_status: insufficient_info
environment: test
confidence: medium
call_chain: []
evidence: []
validation_gaps:
  - frozen Network evidence is missing the response body
gaps: []
unchecked_scopes:
  - Grafana metrics were not needed for this handoff
`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Outcome != PhaseOutcomeValidationEvidenceRequired {
		t.Fatalf("outcome = %q", parsed.Outcome)
	}
}

func TestSafeLegacyInvestigationProjectionRecoversOnlyNonSensitiveBlockingGaps(t *testing.T) {
	recovered, ok := SafeLegacyInvestigationProjection([]byte(`
investigation_status: root_cause_ready
environment: test
root_cause: frontend renders the same name twice
confidence: medium
call_chain: []
evidence: []
gaps:
  - missing deployed revision
  - missing response body
`))
	if !ok {
		t.Fatal("safe legacy result was not recovered")
	}
	var result InvestigationResult
	if err := json.Unmarshal(recovered, &result); err != nil {
		t.Fatal(err)
	}
	if result.InvestigationStatus != "insufficient_info" || strings.Join(result.Gaps, "|") != "missing deployed revision|missing response body" {
		t.Fatalf("recovered result = %+v", result)
	}

	if projection, ok := SafeLegacyInvestigationProjection([]byte(`
investigation_status: root_cause_ready
environment: test
root_cause: "Authorization: Bearer abcdefghijklmnopqrstuvwx"
confidence: medium
call_chain: []
evidence: []
gaps: [missing logs]
`)); ok || projection != nil {
		t.Fatalf("sensitive legacy result was exposed: %s", projection)
	}
}

func TestStructuredInvestigationPromptExplainsRootCauseReadinessGate(t *testing.T) {
	prompt := buildStructuredInvestigationPrompt(Bug{}, BotRef{})
	for _, rule := range []string{
		"只有 confidence: high 且 validation_gaps: [] 且 gaps: []",
		"不得重新操作浏览器复现",
		"validation_gaps",
		"自动交回验证 Agent 补采",
		"不得索要或持久化原始 response body",
		"response_assertions",
		"response_facts",
		"不得仅因缺少原始 response body",
		"gaps 只允许记录必须由用户提供",
		"unchecked_scopes",
		"root_cause_ready 时 unchecked_scopes 必须为 []",
		"本 Studio 阶段契约优先于 incident-investigator",
		"service-to-datastore-source 空映射不代表 MCP 不存在",
		"未真实调用工具及其只读 fallback 前",
		"最终 YAML 必须显式输出 validation_gaps、gaps、unchecked_scopes",
		"deployment revision/image digest/rollout",
		"investigation_status: insufficient_info",
		"source_mapped 必须同时提供 repo、实际部署 revision、file、正数 line 和 evidence",
		"绝不能在 revision 为空时输出 source_mapped",
		"call_chain 定位精度与根因就绪度必须分开判断",
		"不得仅因此降低 confidence、输出 insufficient_info",
		"repositories 必须只列出修复建议实际要求修改的代码仓库",
		"repositories: [] # code_change 时列出实际需要修改的仓库",
	} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("prompt does not contain %q", rule)
		}
	}
	if strings.Contains(prompt, "不要回退到验证流程") {
		t.Fatal("prompt still forbids automatic validation evidence refresh")
	}
}

func TestInvestigationDatastoreReceiptGateUsesFrozenRequestFacts(t *testing.T) {
	input := mustJSON(InitialInvestigationInput{
		ValidationAttemptID: "validation-1",
		Evidence:            []InvestigationEvidenceReference{{Kind: "request_facts"}},
	})
	if !investigationInputRequiresDatastoreRead(input) {
		t.Fatal("request facts did not require a datastore read")
	}
	if investigationInputRequiresDatastoreRead(mustJSON(InitialInvestigationInput{ValidationAttemptID: "validation-1", Evidence: []InvestigationEvidenceReference{{Kind: "network"}}})) {
		t.Fatal("network metadata alone incorrectly required a datastore read")
	}
	for _, event := range []InvestigationEvent{
		{Type: "mcp_tool_call", Message: "query", Raw: map[string]any{"server": "mongodb-test", "tool": "query"}},
		{Type: "command_execution", Message: "mongosh --quiet"},
	} {
		if !eventProvesDatastoreRead(event) {
			t.Fatalf("datastore receipt was not recognized: %+v", event)
		}
	}
	if eventProvesDatastoreRead(InvestigationEvent{Type: "mcp_tool_call", Message: "query", Raw: map[string]any{"server": "grafana-test"}}) {
		t.Fatal("observability query was accepted as datastore evidence")
	}
}

func TestParseInvestigationResultRejectsMisleadingCallChainPrecision(t *testing.T) {
	_, err := ParseInvestigationResult([]byte(`
investigation_status: insufficient_info
environment: test
confidence: low
call_chain:
  - kind: service
    name: user-api
    precision: exact
evidence: []
gaps: [missing deployed revision]
`))
	if err == nil || !strings.Contains(err.Error(), "precision") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseInvestigationResultRoutesNonCodeRootCauseToOperatorAction(t *testing.T) {
	result, err := ParseInvestigationResult(nonCodeRootCauseOutput(RootCauseConfiguration, RemediationOperatorAction))
	if err != nil {
		t.Fatal(err)
	}
	if result.RootCauseType != RootCauseConfiguration || result.Remediation.Mode != RemediationOperatorAction || result.UsesCodeFixWorkflow() {
		t.Fatalf("result=%+v", result)
	}
	if _, err := ParseInvestigationResult(nonCodeRootCauseOutput(RootCauseNetwork, RemediationCodeChange)); err == nil || !strings.Contains(err.Error(), "operator_action") {
		t.Fatalf("invalid root-cause/remediation mapping err=%v", err)
	}
}

func TestAgentPhaseRunnerCoordinatesBrowserAndRegistersCurrentAttemptArtifacts(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-runner", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML(), Usage: AgentUsage{InputTokens: 10, OutputTokens: 5}},
		{FinalYAML: reproducedValidationYAML("browser/final.png"), Usage: AgentUsage{InputTokens: 7, OutputTokens: 3}},
	}}
	screenshot := append([]byte("\x89PNG\r\n\x1a\n"), []byte("rendered")...)
	network := []byte(`[{"method":"GET","url":"https://app.example.com/users","status":200,"duration_ms":12,"content_type":"application/json","content_length":42,"request_id":"req-browser","trace_id":""}]`)
	verifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
		browserDir := filepath.Join(request.StagingDir, "browser")
		if err := os.MkdirAll(browserDir, 0o700); err != nil {
			return BrowserVerificationResult{}, err
		}
		if err := os.WriteFile(filepath.Join(browserDir, "final.png"), screenshot, 0o600); err != nil {
			return BrowserVerificationResult{}, err
		}
		if err := os.WriteFile(filepath.Join(browserDir, "network.json"), network, 0o600); err != nil {
			return BrowserVerificationResult{}, err
		}
		if request.Emit != nil {
			request.Emit(BrowserProgress{Code: "action_started", Message: "执行 1/1：打开页面", ActionID: "open-users", Current: 1, Total: 1})
		}
		return BrowserVerificationResult{
			Status: "completed", FinalScreenshotPath: "browser/final.png",
			Artifacts: []BrowserArtifactReference{
				verifiedBrowserArtifact("screenshot", "browser/final.png", "test", screenshot),
				func() BrowserArtifactReference {
					artifact := verifiedBrowserArtifact("network", "browser/network.json", "test", network)
					artifact.RequestID = "req-browser"
					return artifact
				}(),
			},
		}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	events := make(chan InvestigationEvent, 4)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return nil
	})
	runner.SetEventSink(func(_ InvestigationRun, event InvestigationEvent) { events <- event })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(_ context.Context, got IncidentCase, bug Bug) (BrowserSecurityPolicy, error) {
		if got.ID != incident.ID || bug.FrontendURL == "" {
			t.Fatalf("policy context incident=%+v bug=%+v", got, bug)
		}
		return testBrowserApplicationPolicy("https://app.example.com"), nil
	}))
	bug := Bug{ID: incident.BugID, SystemID: "stale-system", Env: "prod", FrontendURL: "https://app.example.com/users", Steps: "打开用户页"}
	if err := runner.Start(context.Background(), attempt, bug, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.Outcome != PhaseOutcomeReproduced || command.ErrorCode != "" || command.Usage.InputTokens != 17 || command.Usage.OutputTokens != 8 {
		t.Fatalf("completion=%+v", command)
	}
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].AttemptID != attempt.ID || artifacts[1].AttemptID != attempt.ID {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	foundAction := false
	deadline := time.After(time.Second)
	for !foundAction {
		select {
		case event := <-events:
			foundAction = event.Type == "browser_progress" && event.Meta["attempt_id"] == attempt.ID && event.Meta["browser_code"] == "action_started"
		case <-deadline:
			t.Fatal("browser progress event was not projected")
		}
	}
}

func TestAgentPhaseRunnerRejectsBrowserArtifactReplacementBeforeFreeze(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-freeze-mismatch", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	original := append([]byte("\x89PNG\r\n\x1a\n"), []byte("original")...)
	replacement := append([]byte("\x89PNG\r\n\x1a\n"), []byte("tampered")...)
	verifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
		path := filepath.Join(request.StagingDir, "browser", "final.png")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return BrowserVerificationResult{}, err
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			return BrowserVerificationResult{}, err
		}
		artifact := verifiedBrowserArtifact("screenshot", "browser/final.png", "test", original)
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			return BrowserVerificationResult{}, err
		}
		return BrowserVerificationResult{Status: "completed", FinalScreenshotPath: artifact.Path, Artifacts: []BrowserArtifactReference{artifact}}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		return testBrowserApplicationPolicy("https://app.example.com"), nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if command.ErrorCode != "browser_artifact_freeze_failed" || executor.Calls != 1 || len(artifacts) != 0 {
		t.Fatalf("agent=%d artifacts=%+v command=%+v", executor.Calls, artifacts, command)
	}
}

func TestAgentPhaseRunnerFreezesBrowserArtifactBeforeEvaluatorMutation(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-freeze-before-evaluator", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	original := append([]byte("\x89PNG\r\n\x1a\n"), []byte("original")...)
	replacement := append([]byte("\x89PNG\r\n\x1a\n"), []byte("tampered")...)
	network := []byte(`[{"method":"GET","url":"https://app.example.com/users","status":200,"duration_ms":12,"content_type":"application/json","content_length":42,"request_id":"req-frozen-original","trace_id":"trace-1"}]`)
	console := []byte(`{"type":"log","text":"console-frozen-original","timestamp":"2026-07-16T10:00:00Z"}` + "\n")
	actions := []byte(`[{"id":"open-users","action":"click","locator_kind":"role","started_at":"2026-07-16T10:00:00Z","duration_ms":21,"result":"completed","error_code":""}]`)
	var stagedPaths []string
	var frozenScreenshotPath string
	var evaluatorFailure error
	var agentCalls int
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		agentCalls++
		if agentCalls == 1 {
			return PhaseExecutionResult{FinalYAML: validBrowserPlanYAML()}, nil
		}
		for _, path := range stagedPaths {
			if err := os.WriteFile(path, []byte(`{"tampered":true}`), 0o600); err != nil {
				evaluatorFailure = err
				return PhaseExecutionResult{}, err
			}
		}
		if err := os.WriteFile(stagedPaths[0], replacement, 0o600); err != nil {
			evaluatorFailure = err
			return PhaseExecutionResult{}, err
		}
		const screenshotPrefix = "Frozen final screenshot local path (read-only, original bytes): "
		start := strings.Index(prompt, screenshotPrefix)
		if start < 0 {
			evaluatorFailure = errors.New("evaluator prompt does not contain a frozen screenshot path")
			return PhaseExecutionResult{}, evaluatorFailure
		}
		frozenScreenshotPath = strings.TrimSpace(strings.SplitN(prompt[start+len(screenshotPrefix):], "\n", 2)[0])
		if !filepath.IsAbs(frozenScreenshotPath) || frozenScreenshotPath == stagedPaths[0] {
			evaluatorFailure = fmt.Errorf("evaluator screenshot path is not a frozen absolute path: %q", frozenScreenshotPath)
			return PhaseExecutionResult{}, evaluatorFailure
		}
		content, err := os.ReadFile(frozenScreenshotPath)
		if err != nil || !bytes.Equal(content, original) {
			evaluatorFailure = fmt.Errorf("evaluator screenshot=%q err=%v", content, err)
			return PhaseExecutionResult{}, evaluatorFailure
		}
		viewInfo, err := os.Lstat(frozenScreenshotPath)
		if err != nil || viewInfo.Mode().Perm() != 0o400 {
			evaluatorFailure = fmt.Errorf("evaluator screenshot mode=%v err=%v, want 400", viewInfo, err)
			return PhaseExecutionResult{}, evaluatorFailure
		}
		if err := os.WriteFile(frozenScreenshotPath, replacement, 0o600); err == nil {
			evaluatorFailure = errors.New("evaluator could write the read-only screenshot view")
			return PhaseExecutionResult{}, evaluatorFailure
		}
		for _, feature := range []string{"req-frozen-original", "console-frozen-original", `"id":"open-users"`} {
			if !strings.Contains(prompt, feature) {
				evaluatorFailure = fmt.Errorf("evaluator prompt is missing frozen structured evidence %q", feature)
				return PhaseExecutionResult{}, evaluatorFailure
			}
		}
		return PhaseExecutionResult{FinalYAML: reproducedValidationYAML("browser/final.png")}, nil
	})
	verifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
		browserDir := filepath.Join(request.StagingDir, "browser")
		if err := os.MkdirAll(browserDir, 0o700); err != nil {
			return BrowserVerificationResult{}, err
		}
		fixtures := []struct {
			kind    string
			name    string
			content []byte
		}{{"screenshot", "final.png", original}, {"network", "network.json", network}, {"console", "console.jsonl", console}, {"browser_actions", "browser-actions.json", actions}}
		artifacts := make([]BrowserArtifactReference, 0, len(fixtures))
		for _, fixture := range fixtures {
			path := filepath.Join(browserDir, fixture.name)
			if err := os.WriteFile(path, fixture.content, 0o600); err != nil {
				return BrowserVerificationResult{}, err
			}
			stagedPaths = append(stagedPaths, path)
			artifacts = append(artifacts, verifiedBrowserArtifact(fixture.kind, "browser/"+fixture.name, "test", fixture.content))
		}
		return BrowserVerificationResult{Status: "completed", FinalScreenshotPath: artifacts[0].Path, Artifacts: artifacts}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		return testBrowserApplicationPolicy("https://app.example.com"), nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || command.ErrorCode != "" || len(artifacts) != 4 {
		t.Fatalf("artifacts=%+v command=%+v err=%v evaluator=%v", artifacts, command, err, evaluatorFailure)
	}
	if frozenScreenshotPath == "" || bytes.Contains(command.OutputJSON, []byte(frozenScreenshotPath)) {
		t.Fatalf("frozen evaluator path leaked into final output: path=%q output=%s", frozenScreenshotPath, command.OutputJSON)
	}
	if _, err := os.Lstat(frozenScreenshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evaluator screenshot view remains after evaluator return: %v", err)
	}
	var screenshotArtifact EvidenceArtifact
	for _, artifact := range artifacts {
		if artifact.Kind == "screenshot" {
			screenshotArtifact = artifact
			break
		}
	}
	published, err := os.ReadFile(screenshotArtifact.PathOrReference)
	if err != nil || !bytes.Equal(published, original) {
		t.Fatalf("published=%q want original=%q err=%v", published, original, err)
	}
}

func TestAgentPhaseRunnerFreezesNestedBrowserExecutionArtifacts(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-nested-freeze", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	artifactsRoot := phaseArtifactsRoot(t)
	staging, err := openAttemptEvidenceStaging(artifactsRoot, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	defer staging.Cleanup()

	files := []struct {
		kind    string
		path    string
		content []byte
	}{
		{kind: "screenshot", path: "browser-executions/primary/browser/final.png", content: append([]byte("\x89PNG\r\n\x1a\n"), []byte("nested")...)},
		{kind: "network", path: "browser-executions/primary/browser/network.json", content: []byte(`[{"status":200,"code":200,"key":"result"}]`)},
	}
	references := make([]BrowserArtifactReference, 0, len(files))
	for _, file := range files {
		path := filepath.Join(staging.Path(), filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.content, 0o600); err != nil {
			t.Fatal(err)
		}
		references = append(references, verifiedBrowserArtifact(file.kind, file.path, "test", file.content))
	}

	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, artifactsRoot, nil)
	frozen, err := runner.freezeBrowserArtifacts(context.Background(), attempt, staging, references)
	if err != nil {
		t.Fatalf("freeze nested execution artifacts: %v", err)
	}
	if err := validateFrozenBrowserArtifacts(references, frozen); err != nil {
		t.Fatalf("validate nested frozen artifacts: %v", err)
	}
}

func TestAgentPhaseRunnerRegressionBrowserFreezeAllowsFreshReplayWithIdenticalBytes(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-identical-replay", CaseRegressionValidating)
	root := phaseArtifactsRoot(t)
	previous := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	content := append([]byte("\x89PNG\r\n\x1a\n"), []byte("deterministic-result")...)
	previousPath := filepath.Join(t.TempDir(), "previous.png")
	if err := os.WriteFile(previousPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterArtifact(context.Background(), store, ArtifactInput{
		ArtifactsRoot: root, SourcePath: previousPath, CaseID: incident.ID, AttemptID: previous.ID,
		Kind: "screenshot", Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	previous.Status = AttemptStatusSucceeded
	previous.FinishedAt = &finished
	if err := store.FinishAttempt(context.Background(), previous); err != nil {
		t.Fatal(err)
	}

	regression := previous
	regression.ID = "attempt-browser-identical-replay-regression"
	regression.Phase, regression.Mode = PhaseRegression, AttemptRegression
	regression.Status = AttemptStatusRunning
	regression.StartedAt = time.Now().UTC()
	regression.FinishedAt = nil
	if err := store.CreateAttempt(context.Background(), regression); err != nil {
		t.Fatal(err)
	}
	staging, err := openAttemptEvidenceStaging(root, regression.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	defer staging.Cleanup()
	relativePath := "browser-executions/primary/browser/final.png"
	stagedPath := filepath.Join(staging.Path(), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagedPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil)
	reference := verifiedBrowserArtifact("screenshot", relativePath, "test", content)
	frozen, err := runner.freezeBrowserArtifacts(context.Background(), regression, staging, []BrowserArtifactReference{reference})
	if err != nil {
		t.Fatalf("fresh browser replay with deterministic bytes was rejected: %v", err)
	}
	if err := validateFrozenBrowserArtifacts([]BrowserArtifactReference{reference}, frozen); err != nil {
		t.Fatalf("validate frozen replay artifacts: %v", err)
	}
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("artifacts=%+v err=%v, want one artifact per attempt", artifacts, err)
	}
}

func TestAgentPhaseRunnerEvaluatesBrowserBusinessStopAndKeepsFailureScreenshot(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-stop", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/failed.png")},
	}}
	failureScreenshot := append([]byte("\x89PNG\r\n\x1a\n"), []byte("failed")...)
	verifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
		browserDir := filepath.Join(request.StagingDir, "browser")
		if err := os.MkdirAll(browserDir, 0o700); err != nil {
			return BrowserVerificationResult{}, err
		}
		if err := os.WriteFile(filepath.Join(browserDir, "failed.png"), failureScreenshot, 0o600); err != nil {
			return BrowserVerificationResult{}, err
		}
		return BrowserVerificationResult{
			Status: "assertion_failed", FailedActionID: "wait-results", ErrorMessage: "Authorization: Bearer raw-worker-secret",
			FinalScreenshotPath: "browser/failed.png",
			Artifacts:           []BrowserArtifactReference{verifiedBrowserArtifact("screenshot", "browser/failed.png", "test", failureScreenshot)},
		}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		return testBrowserApplicationPolicy("https://app.example.com"), nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, Env: "test", FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.Outcome != PhaseOutcomeReproduced || command.ErrorCode != "" || strings.Contains(string(command.OutputJSON), "raw-worker-secret") || strings.Contains(command.ErrorMessage, "raw-worker-secret") {
		t.Fatalf("completion=%+v output=%s", command, command.OutputJSON)
	}
	var output map[string]any
	if err := json.Unmarshal(command.OutputJSON, &output); err != nil || output["verification_status"] != "reproduced" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	artifacts, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Kind != "screenshot" {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
}

func TestAgentPhaseRunnerBrowserLoginStopPersistsOriginalApplicationURLAndAuthenticationOrigin(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-login-origins", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	planYAML := strings.Replace(validBrowserPlanYAML(), "https://app.example.com/users", "https://app.example.com/oauth/start?state=opaque", 1)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: planYAML}}}
	verifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
		return BrowserVerificationResult{Status: "login_required", LoginOrigin: "https://login.example.com"}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		policy := testBrowserApplicationPolicy("https://app.example.com", "https://login.example.com")
		policy.AuthOrigins = []string{"https://login.example.com"}
		return policy, nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, Env: "test", FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	var envelope struct {
		ApplicationURL string `json:"application_url"`
		LoginOrigin    string `json:"login_origin"`
	}
	if err := json.Unmarshal(command.OutputJSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if command.ErrorCode != "browser_login_required" || envelope.ApplicationURL != "https://app.example.com/oauth/start?state=opaque" || envelope.LoginOrigin != "https://login.example.com" {
		t.Fatalf("command=%+v envelope=%+v", command, envelope)
	}
}

func TestBrowserFailureOutcomeSeparatesSystemFailuresFromEvidenceGaps(t *testing.T) {
	for _, code := range []string{
		"browser_runtime_broken", "browser_policy_unavailable", "browser_policy_changed",
		"browser_verifier_failed", "browser_execution_interrupted", "browser_validator_plan_invalid", "browser_locator_repair_plan_invalid", "browser_locator_failed",
		"browser_worker_protocol_invalid", "browser_artifact_invalid", "browser_artifact_staging_invalid", "browser_artifact_identity_changed",
		"browser_artifact_manifest_invalid", "browser_artifact_digest_changed", "browser_artifact_sensitive", "browser_artifact_freeze_failed",
		"browser_artifact_frozen_invalid", "browser_artifact_repair_evidence_invalid", "browser_artifact_repair_cleanup_failed",
		"browser_artifact_evaluator_evidence_invalid", "browser_artifact_evaluator_cleanup_failed", "browser_artifact_response_assertion_invalid",
		"browser_validator_failed", "browser_validator_timeout",
		"browser_validator_attachment_failed", "browser_validator_no_output", "browser_validator_process_failed", "browser_validator_configuration_invalid",
	} {
		if got := browserFailureOutcome(PhaseValidation, code); got != PhaseOutcomeSystemFailed {
			t.Errorf("code=%s outcome=%s", code, got)
		}
	}
	for _, code := range []string{"browser_login_required", "browser_login_failed", "browser_assertion_failed", "browser_url_required"} {
		if got := browserFailureOutcome(PhaseValidation, code); got != PhaseOutcomeNeedsEvidence {
			t.Errorf("code=%s outcome=%s", code, got)
		}
	}
}

func TestAgentPhaseRunnerExplicitWebWithoutURLDoesNotCallAgentOrBrowser(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-no-url", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	attempt.InputJSON = []byte(`{"mode":"reproduce","target_environment":"test","user_input":"请用浏览器复现页面"}`)
	if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json=? WHERE id=?`, string(attempt.InputJSON), attempt.ID); err != nil {
		t.Fatal(err)
	}
	executor := &scriptedPhaseExecutor{}
	verifierCalls := 0
	verifier := browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
		verifierCalls++
		return BrowserVerificationResult{}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(verifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		t.Fatal("policy resolver called without a usable URL")
		return BrowserSecurityPolicy{}, nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, Env: "test"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.ErrorCode != "browser_url_required" || executor.Calls != 0 || verifierCalls != 0 {
		t.Fatalf("agent=%d browser=%d command=%+v", executor.Calls, verifierCalls, command)
	}
}

func TestAgentPhaseRunnerBrowserRouteIsDurableAcrossBugURLChanges(t *testing.T) {
	for _, test := range []struct {
		name            string
		initialURL      string
		recoveryURL     string
		wantBrowser     bool
		firstResults    []PhaseExecutionResult
		recoveryResults []PhaseExecutionResult
	}{
		{
			name: "browser URL cleared during recovery", initialURL: "https://app.example.com/users", recoveryURL: "", wantBrowser: true,
			firstResults:    []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}, {FinalYAML: reproducedValidationYAML("browser/final.png")}},
			recoveryResults: []PhaseExecutionResult{{FinalYAML: reproducedValidationYAML("browser/final.png")}},
		},
		{
			name: "URL added after non browser start", initialURL: "", recoveryURL: "https://app.example.com/users", wantBrowser: false,
			firstResults:    []PhaseExecutionResult{{FinalYAML: "verification_status: not_reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}},
			recoveryResults: []PhaseExecutionResult{{FinalYAML: "verification_status: not_reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-durable-route-"+strings.ReplaceAll(test.name, " ", "-"), CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			root := phaseArtifactsRoot(t)
			trigger := "fail_route_intent_" + strings.ReplaceAll(strings.ReplaceAll(test.name, " ", "_"), "-", "_")
			if _, err := store.db.Exec(`CREATE TRIGGER ` + trigger + ` BEFORE UPDATE OF output_json ON phase_attempts
				WHEN NEW.id = '` + attempt.ID + `' BEGIN SELECT RAISE(ABORT, 'injected route completion intent failure'); END`); err != nil {
				t.Fatal(err)
			}
			screenshot := append([]byte("\x89PNG\r\n\x1a\n"), []byte("route")...)
			firstHostCalls := 0
			firstVerifier := browserVerifierFunc(func(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
				firstHostCalls++
				path := filepath.Join(request.StagingDir, "browser", "final.png")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return BrowserVerificationResult{}, err
				}
				if err := os.WriteFile(path, screenshot, 0o600); err != nil {
					return BrowserVerificationResult{}, err
				}
				artifact := verifiedBrowserArtifact("screenshot", "browser/final.png", "test", screenshot)
				return BrowserVerificationResult{Status: "completed", FinalScreenshotPath: artifact.Path, Artifacts: []BrowserArtifactReference{artifact}}, nil
			})
			firstExecutor := &scriptedPhaseExecutor{Results: append([]PhaseExecutionResult(nil), test.firstResults...)}
			firstRunner := NewAgentPhaseRunner(store, firstExecutor, nil, root, func(context.Context, CompleteAttemptCommand) error {
				t.Error("completion callback called despite injected intent failure")
				return nil
			})
			firstPolicyCalls := 0
			firstRunner.SetBrowserVerifier(firstVerifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
				firstPolicyCalls++
				return testBrowserApplicationPolicy("https://app.example.com"), nil
			}))
			if err := firstRunner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: test.initialURL}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
				t.Fatal(err)
			}
			waitForAgentPhaseRunnerInactive(t, firstRunner, attempt.ID)
			stagingPath := findAttemptStagingPath(t, root, attempt.ID)
			if _, err := os.Stat(filepath.Join(stagingPath, "browser-route.json")); err != nil {
				t.Fatalf("durable browser route is missing: %v", err)
			}
			if _, err := store.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}

			recoveryHostCalls := 0
			recoveryVerifier := browserVerifierFunc(func(ctx context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
				recoveryHostCalls++
				return firstVerifier.Execute(ctx, request)
			})
			recoveryExecutor := &scriptedPhaseExecutor{Results: append([]PhaseExecutionResult(nil), test.recoveryResults...)}
			completed := make(chan CompleteAttemptCommand, 1)
			recoveryRunner := NewAgentPhaseRunner(store, recoveryExecutor, nil, root, func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
			recoveryPolicyCalls := 0
			recoveryRunner.SetBrowserVerifier(recoveryVerifier, browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
				recoveryPolicyCalls++
				return testBrowserApplicationPolicy("https://app.example.com"), nil
			}))
			if err := recoveryRunner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: test.recoveryURL}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
				t.Fatal(err)
			}
			command := <-completed
			if command.ErrorCode != "" || recoveryExecutor.Calls != 1 || (recoveryHostCalls == 1) != test.wantBrowser || (recoveryPolicyCalls == 1) != test.wantBrowser {
				t.Fatalf("agent=%d host=%d policy=%d command=%+v", recoveryExecutor.Calls, recoveryHostCalls, recoveryPolicyCalls, command)
			}
			for _, prompt := range recoveryExecutor.Prompts {
				if test.recoveryURL != "" && test.recoveryURL != test.initialURL && strings.Contains(prompt, test.recoveryURL) {
					t.Fatalf("recovery prompt used mutable Bug URL %q:\n%s", test.recoveryURL, prompt)
				}
			}
			wantTotalHostCalls := 0
			if test.wantBrowser {
				wantTotalHostCalls = 2
			}
			if firstHostCalls != wantTotalHostCalls || (firstPolicyCalls == 1) != test.wantBrowser {
				t.Fatalf("initial host=%d policy=%d", firstHostCalls, firstPolicyCalls)
			}
		})
	}
}

func TestAgentPhaseRunnerBrowserRouteRejectsPolicyChangeBeforeAgentOrHost(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-route-policy-change", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	root := phaseArtifactsRoot(t)
	staging, err := openOrCreateBrowserAttemptStaging(root, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy := canonicalBrowserSecurityPolicy(testBrowserApplicationPolicy("https://app.example.com"))
	policySHA, err := browserPolicySHA256(policy)
	if err != nil {
		t.Fatal(err)
	}
	route := browserRouteJournal{Kind: browserRouteJournalKind, Version: browserRouteJournalVersion, CaseID: incident.ID, CycleNumber: attempt.CycleNumber, AttemptID: attempt.ID, Assisted: true, FrontendURL: "https://app.example.com/users", SystemID: incident.SystemID, Environment: incident.Environment, PolicyResolved: true, PolicySHA256: policySHA, Policy: policy}
	if err := persistBrowserRouteJournal(staging.Path(), route); err != nil {
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	executor := &scriptedPhaseExecutor{}
	hostCalls := 0
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, root, func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
	runner.SetBrowserVerifier(browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
		hostCalls++
		return BrowserVerificationResult{}, nil
	}), browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		return testBrowserApplicationPolicy("https://changed.example.com"), nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if command.Outcome != PhaseOutcomeSystemFailed || command.ErrorCode != "browser_policy_changed" || executor.Calls != 0 || hostCalls != 0 {
		t.Fatalf("agent=%d host=%d command=%+v", executor.Calls, hostCalls, command)
	}
	var output map[string]any
	if err := json.Unmarshal(command.OutputJSON, &output); err != nil || output["system_failure"] != true || output["evidence_limitation"] != nil {
		t.Fatalf("system failure output=%+v err=%v", output, err)
	}
}

func TestAgentPhaseRunnerBrowserRouteRejectsBadOrMissingMarkerBeforeAgentOrHost(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "malformed", setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, browserRouteJournalName), []byte(`{"kind":"wrong"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing with browser slot", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "browser-executions", "primary"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-browser-route-invalid-"+test.name, CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			root := phaseArtifactsRoot(t)
			staging, err := openOrCreateBrowserAttemptStaging(root, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, staging.Path())
			if err := staging.Close(); err != nil {
				t.Fatal(err)
			}
			executor := &scriptedPhaseExecutor{}
			hostCalls := 0
			completed := make(chan CompleteAttemptCommand, 1)
			runner := NewAgentPhaseRunner(store, executor, nil, root, func(_ context.Context, command CompleteAttemptCommand) error { completed <- command; return nil })
			runner.SetBrowserVerifier(browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
				hostCalls++
				return BrowserVerificationResult{}, nil
			}), browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
				return testBrowserApplicationPolicy("https://app.example.com"), nil
			}))
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
				t.Fatal(err)
			}
			command := <-completed
			if command.ErrorCode != "browser_execution_interrupted" || executor.Calls != 0 || hostCalls != 0 {
				t.Fatalf("agent=%d host=%d command=%+v", executor.Calls, hostCalls, command)
			}
		})
	}
}

func TestAgentPhaseRunnerBrowserDurabilitySyncFailurePreventsAgentAndHost(t *testing.T) {
	originalSync := browserDurabilitySync
	browserDurabilitySync = func(string) error { return errors.New("injected parent directory fsync failure") }
	defer func() { browserDurabilitySync = originalSync }()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-browser-parent-fsync", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	hostCalls := 0
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	runner.SetBrowserVerifier(browserVerifierFunc(func(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error) {
		hostCalls++
		return BrowserVerificationResult{}, nil
	}), browserPolicyResolverFunc(func(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error) {
		return testBrowserApplicationPolicy("https://app.example.com"), nil
	}))
	startErr := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID, FrontendURL: "https://app.example.com/users"}, installedPhaseRunnerBot(t, "bot", "codex"))
	if startErr == nil {
		waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
	}
	if executor.Calls != 0 || hostCalls != 0 {
		t.Fatalf("start err=%v agent=%d host=%d", startErr, executor.Calls, hostCalls)
	}
}

func waitForAgentPhaseRunnerInactive(t *testing.T, runner *AgentPhaseRunner, attemptID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		runner.mu.Lock()
		_, active := runner.active[attemptID]
		runner.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("agent phase runner did not become inactive")
		}
		time.Sleep(time.Millisecond)
	}
}

func findAttemptStagingPath(t *testing.T, root, attemptID string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), attemptID+"-") {
			if found != "" {
				t.Fatal("multiple staging directories found for one attempt")
			}
			found = filepath.Join(root, ".staging", entry.Name())
		}
	}
	if found == "" {
		t.Fatal("attempt staging directory not found")
	}
	return found
}

func TestRegressionEvidenceCorrelationMayComeFromAnyCurrentArtifact(t *testing.T) {
	attempt := PhaseAttempt{Phase: PhaseRegression, InputJSON: mustJSON(RegressionValidationInput{OriginalScenarioHash: "scenario", TargetEnvironment: "test", ObservedDeploymentVersion: "version-1"})}
	validation := ValidationResult{VerificationStatus: "fixed_verified", Environment: "test", ScenarioHash: "scenario"}
	output, _ := json.Marshal(validation)
	result := PhaseResult{Outcome: PhaseOutcomeFixedVerified, OutputJSON: output, ArtifactInputs: []ArtifactReference{
		{Kind: "screenshot", Path: "browser/final.png", Environment: "test", Version: "version-1"},
		{Kind: "network", Path: "browser/network.json", Environment: "test", Version: "version-1", RequestID: "req-1"},
	}}
	runner := &AgentPhaseRunner{}
	if err := runner.validateRegressionEvidence(context.Background(), attempt, result); err != nil {
		t.Fatalf("correlation on one current artifact was rejected: %v", err)
	}
}

func TestRegressionEvidenceAllowsVersionToBeAbsentWhenRuntimeDidNotExposeIt(t *testing.T) {
	attempt := PhaseAttempt{Phase: PhaseRegression, InputJSON: mustJSON(RegressionValidationInput{OriginalScenarioHash: "scenario", TargetEnvironment: "test"})}
	validation := ValidationResult{VerificationStatus: "fixed_verified", Environment: "test", ScenarioHash: "scenario"}
	output, _ := json.Marshal(validation)
	result := PhaseResult{Outcome: PhaseOutcomeFixedVerified, OutputJSON: output, ArtifactInputs: []ArtifactReference{{
		Kind: "network", Path: "browser/network.json", Environment: "test", RequestID: "req-optional-version",
	}}}
	if err := (&AgentPhaseRunner{}).validateRegressionEvidence(context.Background(), attempt, result); err != nil {
		t.Fatalf("versionless regression evidence was rejected: %v", err)
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
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
		if len(entries) != 1 || entries[0].Name() != browserRouteJournalName {
			return PhaseExecutionResult{}, fmt.Errorf("staging did not contain exactly its durable route marker: %v", entries)
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
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
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
				t.Fatal(err)
			}
			command := <-completed
			waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
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

func TestAgentPhaseRunnerPreservesBlockedFixWhenOptionalEvidenceIsInvalid(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-fix-invalid-optional-evidence", CaseFixing)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseFix, "")
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		staging := stagingPathFromPrompt(prompt)
		if staging == "" {
			return PhaseExecutionResult{}, errors.New("missing Studio staging path")
		}
		if err := os.WriteFile(filepath.Join(staging, "fix-blocked.yaml"), []byte("blocked: git metadata unavailable\n"), 0o600); err != nil {
			return PhaseExecutionResult{}, err
		}
		return PhaseExecutionResult{FinalYAML: `fix_status: blocked
environment: test
branches:
  - repo: api
    base_branch: feature/work
    fix_branch: ""
    commit: ""
    pushed: false
    target_environment_branch: test
    push_remote: origin
changes: []
tests:
  - repo: api
    commit: ""
    command: git status --short
    result: skipped
    skipped_reason: Git metadata unavailable
deployment_notice: no branch was pushed
risks: [Bug remains unfixed]
blocked_reason: Git metadata unavailable
evidence:
  - path: fix-blocked.yaml
`}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return nil
	})
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
	if command.Outcome != PhaseOutcomeFixFailed || command.ErrorCode != "" {
		t.Fatalf("blocked fix was overwritten by optional evidence: %+v", command)
	}
	var result FixResult
	if err := json.Unmarshal(command.OutputJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.FixStatus != "blocked" || result.BlockedReason != "Git metadata unavailable" || len(result.Evidence) != 0 {
		t.Fatalf("blocked result was not preserved safely: %+v", result)
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	runner.run(context.Background(), attempt, incident, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"), "prompt", staging, nil, incident.Version, claimToken, func(context.Context, CompleteAttemptCommand) error { return nil }, nil, nil, nil)
	if staging.calls != 2 {
		t.Fatalf("cleanup calls = %d, want initial failure plus deferred retry", staging.calls)
	}
	if _, err := os.Stat(owned.Path()); !os.IsNotExist(err) {
		t.Fatalf("staging retained after deferred retry: %v", err)
	}
}

func TestAgentPhaseRunnerPreservesStagingWhenCompletionIntentSaveFails(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-intent-save-staging", CaseValidating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
	staging := &lifecycleStaging{path: filepath.Join(t.TempDir(), "owned")}
	executor := &phaseExecutorStub{result: PhaseExecutionResult{FinalYAML: "verification_status: not_reproduced\nenvironment: test\nevidence: []\ngaps: []\n"}}
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), nil)
	claimToken := "intent-save-staging-claim"
	if err := store.ClaimRunnableAttempt(context.Background(), AttemptRunClaim{Attempt: attempt, ClaimToken: claimToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_browser_intent_save BEFORE UPDATE OF output_json ON phase_attempts
		WHEN NEW.id = '` + attempt.ID + `' BEGIN SELECT RAISE(ABORT, 'injected completion intent failure'); END`); err != nil {
		t.Fatal(err)
	}
	completionCalled := false
	runner.run(context.Background(), attempt, incident, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"), "prompt", staging, nil, incident.Version, claimToken, func(context.Context, CompleteAttemptCommand) error {
		completionCalled = true
		return nil
	}, nil, nil, nil)
	cleanups, closes := staging.lifecycle()
	if completionCalled || cleanups != 0 || closes != 1 {
		t.Fatalf("completion=%v staging cleanup=%d close=%d", completionCalled, cleanups, closes)
	}
	persisted, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := parseCompletionIntent(persisted.OutputJSON); err != nil || found {
		t.Fatalf("completion intent found=%v err=%v", found, err)
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
	if phase == PhaseFix {
		now := time.Now().UTC()
		parent := PhaseAttempt{
			ID: "root-" + incident.ID, CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation,
			Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`),
			OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"verified code defect","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"affected repository","summary":"apply the minimal code correction","verification":"run the original scenario"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
			StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
		}
		if err := store.CreateAttempt(context.Background(), parent); err != nil {
			t.Fatal(err)
		}
		attempt.ParentAttemptID = parent.ID
	}
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
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
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
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
				t.Fatal("Start succeeded")
			}
			if cleanups, closes := staging.lifecycle(); cleanups != 0 || closes != 0 {
				t.Fatalf("staging lifecycle cleanup=%d close=%d", cleanups, closes)
			}
		})
	}
}

func TestAgentPhaseRunnerPreflightFailureNeverDeletesReusedBrowserStaging(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *AgentPhaseRunner, *CaseStore, PhaseAttempt, context.CancelFunc)
	}{
		{name: "cancel after reopen", setup: func(t *testing.T, runner *AgentPhaseRunner, _ *CaseStore, _ PhaseAttempt, cancel context.CancelFunc) {
			runner.openStaging = func(root, attemptID string) (attemptEvidenceStaging, error) {
				staging, err := openOrCreateBrowserAttemptStaging(root, attemptID)
				cancel()
				return staging, err
			}
		}},
		{name: "claim conflict", setup: func(t *testing.T, _ *AgentPhaseRunner, store *CaseStore, attempt PhaseAttempt, _ context.CancelFunc) {
			if _, err := store.db.Exec(`UPDATE phase_attempts SET run_claim_token='another-runner' WHERE id=?`, attempt.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "claim storage error", setup: func(t *testing.T, _ *AgentPhaseRunner, store *CaseStore, attempt PhaseAttempt, _ context.CancelFunc) {
			if _, err := store.db.Exec(`CREATE TRIGGER fail_reused_browser_claim BEFORE UPDATE OF run_claim_token ON phase_attempts WHEN NEW.id='` + attempt.ID + `' BEGIN SELECT RAISE(ABORT, 'injected claim failure'); END`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-reused-preflight-"+strings.ReplaceAll(test.name, " ", "-"), CaseValidating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseValidation, AttemptReproduce)
			root := phaseArtifactsRoot(t)
			staging, err := openOrCreateBrowserAttemptStaging(root, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			sentinels := []string{
				browserRouteJournalName,
				filepath.Join("browser-executions", "primary", browserCoordinatorPlanJournalName),
				filepath.Join("browser-executions", "primary", "browser", "result.json"),
			}
			for _, relative := range sentinels {
				path := filepath.Join(staging.Path(), relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("durable-journal"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stagingPath := staging.Path()
			if err := staging.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, func(context.Context, CompleteAttemptCommand) error { return nil })
			test.setup(t, runner, store, attempt, cancel)
			if err := runner.Start(ctx, attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
				t.Fatal("Start succeeded")
			}
			for _, relative := range sentinels {
				content, err := os.ReadFile(filepath.Join(stagingPath, relative))
				if err != nil || string(content) != "durable-journal" {
					t.Fatalf("reused journal %q was not preserved: content=%q err=%v", relative, content, err)
				}
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
		if err := os.MkdirAll(staging.path, 0o700); err != nil {
			return nil, err
		}
		created <- staging
		<-release
		return staging, nil
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"))
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
		startErr <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"))
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
		startErr <- runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"))
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
	if err := runner.Start(schedulingCtx, attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
			if err := orchestrator.startPhase(attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(ctx, attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); !errors.Is(err, context.Canceled) {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "validator", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	incident := createWorkflowCase(t, store, "case-fix-prompt-handoff", CaseWaitingFixApproval)
	now := time.Now().UTC()
	rootCause := PhaseAttempt{
		ID: "root-cause-fix-prompt", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation,
		Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"cache write race","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"internal/cache/store.go","summary":"serialize cache writes","verification":"run cache race tests"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
		StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(context.Background(), rootCause); err != nil {
		t.Fatal(err)
	}
	attempt := PhaseAttempt{ID: "fix-prompt", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseFix, ParentAttemptID: rootCause.ID, InputJSON: []byte(`{"user_requirement":"authorized-race-in-cache"}`)}
	prompt, err := runner.promptForAttempt(attempt, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"authorized-race-in-cache", "结构化阶段输入", "已批准的排障交接", "cache write race", "serialize cache writes", "run cache race tests"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("fix prompt lost %q:\n%s", required, prompt)
		}
	}
}

func TestAgentPhaseRunnerFixPromptRejectsMissingRootCauseHandoff(t *testing.T) {
	store := newOrchestratorStore(t)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	_, err := runner.promptForAttempt(PhaseAttempt{Phase: PhaseFix, InputJSON: []byte(`{}`)}, Bug{ID: "bug"}, BotRef{})
	if err == nil || !strings.Contains(err.Error(), "approved root-cause attempt") {
		t.Fatalf("missing root-cause handoff err=%v", err)
	}
}

func TestAgentPhaseRunnerInvestigationPromptConsumesFrozenEvidenceAndPublishesSevenSteps(t *testing.T) {
	store := newOrchestratorStore(t)
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	attempt := PhaseAttempt{Phase: PhaseInvestigation, InputJSON: []byte(`{"validation_attempt_id":"validation-1","scenario_hash":"scenario-1"}`)}
	prompt, err := runner.promptForAttempt(attempt, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"验证 Agent 完成复现并冻结证据",
		"不得调用 bug-verifier、api-verifier、attachment-evidence-verifier",
		"不得重新操作浏览器复现",
		"[[TSHOOT_STEP phase=investigation index=1 key=evidence_handoff]]",
		"[[TSHOOT_STEP phase=investigation index=7 key=knowledge_sink]]",
		"validation-1",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("investigation prompt missing %q:\n%s", required, prompt)
		}
	}
}

func TestAgentPhaseRunnerRetriesUnknownRemediationRepositoryWithConfiguredNames(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-investigation-repository-scope", CaseInvestigating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
	repositoryPath := t.TempDir()
	var calls int
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		calls++
		repo := "truss-base"
		if calls == 2 {
			if !strings.Contains(prompt, `"base-backend"`) || !strings.Contains(prompt, `"truss-base"`) {
				t.Fatalf("retry prompt did not identify configured and rejected repositories:\n%s", prompt)
			}
			repo = "base-backend"
		}
		return PhaseExecutionResult{FinalYAML: fmt.Sprintf(`investigation_status: root_cause_ready
environment: test
root_cause: backend response mapper uses the wrong field
confidence: high
root_cause_type: code
remediation:
  mode: code_change
  repositories: [%s]
  target: response mapper
  summary: map the correct field
  verification: rerun the original scenario
call_chain: []
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes: []
`, repo)}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return nil
	})
	runner.SetRepositoryAccessResolver(RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
		return map[string]string{"base-backend": repositoryPath}, nil
	}))

	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if calls != 2 {
		t.Fatalf("executor calls=%d, want one bounded repository correction retry", calls)
	}
	if command.Outcome != PhaseOutcomeRootCauseReady || command.ErrorCode != "" {
		t.Fatalf("completion=%+v", command)
	}
	result, err := ParseInvestigationResult(command.OutputJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := remediationFixRepositories(result); !reflect.DeepEqual(got, []string{"base-backend"}) {
		t.Fatalf("repositories=%v", got)
	}
}

func TestAgentPhaseRunnerValidationPromptIncludesDurableContinuationContext(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-validation-continuation-prompt", CaseWaitingEvidence)
	now := time.Now().UTC()
	first := PhaseAttempt{
		ID:          "attempt-validation-first",
		CaseID:      incident.ID,
		CycleNumber: incident.CycleNumber,
		Phase:       PhaseValidation,
		Mode:        AttemptReproduce,
		Status:      AttemptStatusFailed,
		AgentTarget: "codex",
		BotKey:      "bot",
		InputJSON:   []byte(`{"mode":"reproduce","user_input":"打开 Web 用户搜索页"}`),
		OutputJSON:  []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["missing-first-route"]}`),
		StartedAt:   now.Add(-2 * time.Minute),
		FinishedAt:  &now,
	}
	if err := store.CreateAttempt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := PhaseAttempt{
		ID:              "attempt-validation-second",
		CaseID:          incident.ID,
		CycleNumber:     incident.CycleNumber,
		Phase:           PhaseValidation,
		Mode:            AttemptReproduce,
		Status:          AttemptStatusFailed,
		AgentTarget:     "codex",
		BotKey:          "bot",
		InputJSON:       []byte(`{"mode":"reproduce","user_input":"测试账号已在安全凭据中配置"}`),
		OutputJSON:      []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["latest-gap-from-parent"]}`),
		ParentAttemptID: first.ID,
		StartedAt:       now.Add(-time.Minute),
		FinishedAt:      &now,
	}
	if err := store.CreateAttempt(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	current := PhaseAttempt{
		ID:              "attempt-validation-current",
		CaseID:          incident.ID,
		CycleNumber:     incident.CycleNumber,
		Phase:           PhaseValidation,
		Mode:            AttemptReproduce,
		Status:          AttemptStatusRunning,
		AgentTarget:     "codex",
		BotKey:          "bot",
		InputJSON:       []byte(`{"mode":"reproduce","target_environment":"test","frontend_url":"https://test.example.invalid/users","user_input":"请用 Web 环境复现"}`),
		OutputJSON:      []byte(`{}`),
		ParentAttemptID: second.ID,
		StartedAt:       now,
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, phaseArtifactsRoot(t), func(context.Context, CompleteAttemptCommand) error { return nil })
	prompt, err := runner.promptForAttempt(current, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	clarifications, err := runner.browserUserClarifications(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if diff := strings.Join(clarifications, " | "); diff != "打开 Web 用户搜索页 | 测试账号已在安全凭据中配置 | 请用 Web 环境复现" {
		t.Fatalf("browser clarifications lost durable order: %q", diff)
	}
	for _, required := range []string{
		"打开 Web 用户搜索页",
		"测试账号已在安全凭据中配置",
		"请用 Web 环境复现",
		"https://test.example.invalid/users",
		"latest-gap-from-parent",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("validation continuation prompt lost %q:\n%s", required, prompt)
		}
	}
}

func TestAgentPhaseRunnerCarriesUploadedScreenshotsIntoValidationRetry(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-validation-upload", CaseNotReproduced)
	now := time.Now().UTC()
	blocked := PhaseAttempt{
		ID: "attempt-validation-blocked", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed,
		AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
		StartedAt: now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(context.Background(), blocked); err != nil {
		t.Fatal(err)
	}
	root := phaseArtifactsRoot(t)
	image := append([]byte(nil), browserPNGSignature...)
	image = append(image, []byte("safe-image")...)
	artifact, err := RegisterArtifactBytes(context.Background(), store, ArtifactInput{
		ArtifactsRoot: root, CaseID: incident.ID, AttemptID: blocked.ID,
		Kind: "user_screenshot", Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}, image)
	if err != nil {
		t.Fatal(err)
	}
	fileArtifact, err := RegisterArtifactBytes(context.Background(), store, ArtifactInput{
		ArtifactsRoot: root, CaseID: incident.ID, AttemptID: blocked.ID,
		Kind: "user_browser_file_xlsx", Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}, []byte("xlsx-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	retry := PhaseAttempt{
		ID: "attempt-validation-retry", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning,
		AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
		ParentAttemptID: blocked.ID, StartedAt: now,
	}
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, root, nil)
	got, err := runner.withSupplementalValidationScreenshots(context.Background(), retry, Bug{ID: incident.BugID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	byID := make(map[string]Attachment, len(got.Attachments))
	for _, attachment := range got.Attachments {
		byID[attachment.ID] = attachment
	}
	if byID[artifact.ID].Type != "image/png" || byID[artifact.ID].LocalPath != artifact.PathOrReference {
		t.Fatalf("screenshot attachment = %+v", byID[artifact.ID])
	}
	if byID[fileArtifact.ID].Type != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		!strings.HasSuffix(byID[fileArtifact.ID].Name, ".xlsx") ||
		byID[fileArtifact.ID].LocalPath != fileArtifact.PathOrReference {
		t.Fatalf("file attachment = %+v", byID[fileArtifact.ID])
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
		if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
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
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err == nil {
		t.Fatal("started regression for an environment different from Case")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d", executor.calls)
	}
}
