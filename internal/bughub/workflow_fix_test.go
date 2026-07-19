package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func prepareFixApprovalCase(t *testing.T, output string) (*CaseStore, IncidentCase, PhaseAttempt, *recordingPhaseRunner, *CaseOrchestrator) {
	t.Helper()
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-fix-approval", CaseRootCauseReady)
	attempt := PhaseAttempt{
		ID: "investigation-root", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseInvestigation, Status: AttemptStatusSucceeded,
		InputJSON: []byte(`{}`), OutputJSON: []byte(output),
	}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	incident, _, err := store.TransitionWithUpdate(ctx, incident.ID, incident.Version, CaseWaitingFixApproval,
		CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(attempt.ID)},
		TransitionEvent{ID: "root-ready", IdempotencyKey: "root-ready", EventType: "root_cause_ready", ActorType: "agent", ActorID: "agent", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPhaseRunner{}
	return store, incident, attempt, runner, NewCaseOrchestrator(store, runner, nil, nil)
}

func validRootCauseOutput() string {
	return `{"investigation_status":"root_cause_ready","environment":"test","root_cause":"race","confidence":"high","evidence":[],"gaps":[]}`
}

func TestApproveFixRequiresExactSnapshotBoundKey(t *testing.T) {
	_, incident, root, runner, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	base := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)}

	base.IdempotencyKey = "approve-fix"
	if _, err := orchestrator.ApproveFix(context.Background(), base); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("non-canonical approval key err=%v", err)
	}
	if runner.startCount() != 0 {
		t.Fatalf("stale dialog scheduled %d fix attempts", runner.startCount())
	}

	base.IdempotencyKey = StartFixApprovalKey(incident.ID, root.ID, incident.Version)
	base.ExpectedVersion++
	base.IdempotencyKey = StartFixApprovalKey(incident.ID, root.ID, base.ExpectedVersion)
	if _, err := orchestrator.ApproveFix(context.Background(), base); !errors.Is(err, ErrCaseVersionConflict) {
		t.Fatalf("stale dialog version err=%v", err)
	}
	if runner.startCount() != 0 {
		t.Fatalf("stale dialog scheduled %d fix attempts", runner.startCount())
	}
}

func TestApproveFixRequiresExplicitSourceBaselines(t *testing.T) {
	_, incident, root, runner, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	command := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)}
	if _, err := orchestrator.ApproveFix(context.Background(), command); err == nil || !strings.Contains(err.Error(), "source_baselines") {
		t.Fatalf("missing source baseline approval err=%v", err)
	}
	if runner.startCount() != 0 {
		t.Fatalf("runner started without a source baseline: %d", runner.startCount())
	}
}

func TestApproveFixRejectsStaleOrUnsafeRootCause(t *testing.T) {
	tests := []struct {
		name   string
		output string
		stale  bool
	}{
		{name: "medium confidence", output: `{"investigation_status":"root_cause_ready","environment":"test","root_cause":"guess","confidence":"medium","evidence":[],"gaps":[]}`},
		{name: "critical evidence unresolved", output: `{"investigation_status":"root_cause_ready","environment":"test","root_cause":"guess","confidence":"high","evidence":[],"gaps":["critical: missing production trace"]}`},
		{name: "not latest investigation", output: validRootCauseOutput(), stale: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, incident, root, runner, orchestrator := prepareFixApprovalCase(t, test.output)
			if test.stale {
				newer := PhaseAttempt{ID: "investigation-root-z", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation, Status: AttemptStatusFailed, InputJSON: []byte(`{}`), OutputJSON: []byte(`{"investigation_status":"insufficient_info","environment":"test","confidence":"low","evidence":[],"gaps":["trace"]}`)}
				if err := store.CreateAttempt(context.Background(), newer); err != nil {
					t.Fatal(err)
				}
			}
			cmd := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)}
			if _, err := orchestrator.ApproveFix(context.Background(), cmd); !errors.Is(err, ErrApprovalScope) {
				t.Fatalf("ApproveFix err=%v", err)
			}
			if runner.startCount() != 0 {
				t.Fatalf("unsafe root cause scheduled %d fix attempts", runner.startCount())
			}
		})
	}
}

func TestApproveFixPersistsRootCauseAndSnapshotScope(t *testing.T) {
	store, incident, root, runner, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	key := StartFixApprovalKey(incident.ID, root.ID, incident.Version)
	command := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)}
	updated, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil || updated.Status != CaseFixing || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d err=%v", updated, runner.startCount(), err)
	}
	approvals, err := store.ListApprovals(context.Background(), incident.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	if approvals[0].CaseVersion != incident.Version || string(approvals[0].ScopeJSON) != `{"root_cause_attempt_id":"investigation-root","source_baselines":{"api":"feature/work"}}` {
		t.Fatalf("approval=%+v", approvals[0])
	}
	replayed, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil || replayed != updated || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
}

func TestApproveFixReplaySurvivesLaterCycleAndRejectsDivergentPayload(t *testing.T) {
	store, incident, root, runner, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	command := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"root_cause":"race","source_baselines":{"api":"feature/work"}}`)}
	committed, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.Transition(context.Background(), committed.ID, committed.Version, CaseFixFailed, TransitionEvent{ID: "later-failed", IdempotencyKey: "later-failed", EventType: "later_failed", ActorType: "agent", ActorID: "fixer", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	cycle2 := 2
	later, _, err := store.TransitionWithUpdate(context.Background(), failed.ID, failed.Version, CaseFixing, CaseSnapshotUpdate{CycleNumber: &cycle2}, TransitionEvent{ID: "later-cycle", IdempotencyKey: "later-cycle", EventType: "later_cycle", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: []byte(`{}`)})
	if err != nil || later.CycleNumber != 2 {
		t.Fatalf("later=%+v err=%v", later, err)
	}

	replayed, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil || replayed != committed || runner.startCount() != 1 {
		t.Fatalf("replay=%+v committed=%+v starts=%d err=%v", replayed, committed, runner.startCount(), err)
	}
	divergent := command
	divergent.InputJSON = []byte(`{"root_cause":"different","source_baselines":{"api":"feature/work"}}`)
	if _, err := orchestrator.ApproveFix(context.Background(), divergent); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("divergent replay err=%v", err)
	}
}

func TestApproveFixConcurrentExactCommandSchedulesOnce(t *testing.T) {
	store, incident, root, runner, _ := prepareFixApprovalCase(t, validRootCauseOutput())
	command := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)}
	orchestrators := []*CaseOrchestrator{NewCaseOrchestrator(store, runner, nil, nil), NewCaseOrchestrator(store, runner, nil, nil)}
	results := make([]IncidentCase, len(orchestrators))
	errs := make([]error, len(orchestrators))
	var wait sync.WaitGroup
	for index := range orchestrators {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = orchestrators[index].ApproveFix(context.Background(), command)
		}(index)
	}
	wait.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] != results[1] || runner.startCount() != 1 {
		t.Fatalf("results=%+v errs=%v starts=%d", results, errs, runner.startCount())
	}
}

func TestParseFixResultRequiresCompletePerRepositoryEvidence(t *testing.T) {
	valid := `
fix_status: fixed_pushed
environment: test
branches:
  - repo: api
    base_branch: test
    fix_branch: fix/race
    commit: abc123
    pushed: true
    target_environment_branch: test
    push_remote: origin
changes:
  - repo: api
    summary: "internal/api: guard concurrent write"
tests:
  - repo: api
    commit: abc123
    command: go test ./...
    result: passed
deployment_notice: deploy api/fix/race to test
risks: []
evidence: []
`
	if _, err := ParseFixResult([]byte(valid)); err != nil {
		t.Fatalf("valid fix result: %v", err)
	}
	invalid := map[string]string{
		"missing changes":     replaceFixLine(valid, "changes:\n  - repo: api\n    summary: \"internal/api: guard concurrent write\"\n", ""),
		"missing deployment":  replaceFixLine(valid, "deployment_notice: deploy api/fix/race to test\n", ""),
		"missing risks":       replaceFixLine(valid, "risks: []\n", ""),
		"push not successful": replaceFixLine(valid, "pushed: true", "pushed: false"),
		"wrong test commit":   replaceFixLine(valid, "commit: abc123\n    command", "commit: stale\n    command"),
	}
	for name, document := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFixResult([]byte(document)); err == nil {
				t.Fatal("ParseFixResult accepted incomplete result")
			}
		})
	}
}

func TestParseFixResultAcceptsExplicitTestSkipReason(t *testing.T) {
	document := `
fix_status: fixed_pushed
environment: test
branches:
  - repo: web
    base_branch: test
    fix_branch: fix/css
    commit: def456
    pushed: true
    target_environment_branch: test
    push_remote: origin
changes:
  - repo: web
    summary: "web/button: repair layout"
tests:
  - repo: web
    commit: def456
    command: npm test
    result: skipped
    skipped_reason: browser runtime unavailable
deployment_notice: deploy web/fix/css to test
risks:
  - browser regression remains for human verification
evidence: []
`
	if _, err := ParseFixResult([]byte(document)); err != nil {
		t.Fatalf("explicit skip rejected: %v", err)
	}
}

func TestParseFixResultRejectsUnsafeBranchTopologyAndNormalizesNames(t *testing.T) {
	base := `
fix_status: fixed_pushed
environment: test
branches:
  - {repo: " api ", base_branch: " test ", fix_branch: " fix/api ", commit: " aaa111 ", pushed: true, target_environment_branch: " test ", push_remote: " origin "}
changes:
  - {repo: " api ", summary: api change}
tests:
  - {repo: " api ", commit: " aaa111 ", command: go test ./..., result: passed}
deployment_notice: deploy api
risks: []
evidence: []
`
	parsed, err := ParseFixResult([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	branch := parsed.Branches[0]
	if branch.Repo != "api" || branch.BaseBranch != "test" || branch.FixBranch != "fix/api" || branch.Commit != "aaa111" || branch.TargetEnvironmentBranch != "test" || branch.PushRemote != "origin" || parsed.Changes[0].Repo != "api" || parsed.Tests[0].Repo != "api" || parsed.Tests[0].Commit != "aaa111" {
		t.Fatalf("result was not normalized: %+v", parsed)
	}
	differentBaseline := strings.Replace(base, `base_branch: " test "`, `base_branch: " feature/new-ui "`, 1)
	if parsed, err := ParseFixResult([]byte(differentBaseline)); err != nil || parsed.Branches[0].BaseBranch != "feature/new-ui" || parsed.Branches[0].TargetEnvironmentBranch != "test" {
		t.Fatalf("independent source baseline rejected: %+v, %v", parsed, err)
	}
	for name, document := range map[string]string{
		"fix equals base":   strings.Replace(base, `fix_branch: " fix/api "`, `fix_branch: " test "`, 1),
		"fix equals target": strings.Replace(base, `fix_branch: " fix/api "`, `fix_branch: "test"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFixResult([]byte(document)); err == nil {
				t.Fatal("unsafe branch topology accepted")
			}
		})
	}
}

func TestParseFixResultBuildsPerRepositoryCodeChangeScope(t *testing.T) {
	document := `
fix_status: fixed_pushed
environment: test
branches:
  - {repo: api, base_branch: test, fix_branch: fix/api, commit: aaa111, pushed: true, target_environment_branch: test, push_remote: origin}
  - {repo: web, base_branch: test, fix_branch: fix/web, commit: bbb222, pushed: true, target_environment_branch: test, push_remote: origin}
changes:
  - {repo: api, summary: api change}
  - {repo: web, summary: web change}
tests:
  - {repo: api, commit: aaa111, command: go test ./..., result: passed}
  - {repo: web, commit: bbb222, command: npm test, result: passed}
deployment_notice: deploy both repositories
risks: []
evidence: []
`
	result, err := ParsePhaseResult(PhaseAttempt{ID: "fix-attempt", CaseID: "case", Phase: PhaseFix}, []byte(document))
	if err != nil || len(result.CodeChanges) != 2 {
		t.Fatalf("changes=%+v err=%v", result.CodeChanges, err)
	}
	for _, change := range result.CodeChanges {
		var tests []FixTestResult
		if err := json.Unmarshal(change.TestEvidence, &tests); err != nil {
			t.Fatal(err)
		}
		if len(tests) != 1 || tests[0].Repo != change.Repo || tests[0].Commit != change.FixCommit {
			t.Fatalf("change %s contains unscoped tests %+v", change.Repo, tests)
		}
	}
}

func TestParseFixResultCompletionPersistsChangesBeforeMergeApprovalWait(t *testing.T) {
	store, incident, root, _, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	approved, err := orchestrator.ApproveFix(context.Background(), ApproveFixCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetAttempt(context.Background(), approved.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`
fix_status: fixed_pushed
environment: test
branches:
  - {repo: api, base_branch: test, fix_branch: fix/api, commit: aaa111, pushed: true, target_environment_branch: test, push_remote: origin}
changes:
  - {repo: api, summary: api change}
tests:
  - {repo: api, commit: aaa111, command: go test ./..., result: passed}
deployment_notice: deploy api
risks: []
evidence: []
`)
	parsed, err := ParsePhaseResult(attempt, document)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.git = &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: parsed.CodeChanges}}
	waiting, err := orchestrator.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: approved.ID, AttemptID: attempt.ID, ExpectedVersion: approved.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "fixer", Outcome: parsed.Outcome, OutputJSON: parsed.OutputJSON, CodeChanges: parsed.CodeChanges})
	if err != nil || waiting.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", waiting, err)
	}
	changes, err := store.ListCodeChanges(context.Background(), incident.ID)
	if err != nil || len(changes) != 1 || changes[0].FixCommit != "aaa111" || changes[0].PushStatus != "pushed" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	events, err := store.ListEvents(context.Background(), incident.ID)
	if err != nil || len(events) < 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	last := events[len(events)-2:]
	if last[0].EventType != "fix_pushed" || last[0].ToStatus != CaseFixPushed || last[1].EventType != "merge_approval_requested" || last[1].FromStatus != CaseFixPushed || last[1].ToStatus != CaseWaitingMergeApproval {
		t.Fatalf("completion events=%+v", last)
	}
}

func validFixCompletion(t *testing.T, incident IncidentCase, attempt PhaseAttempt) CompleteAttemptCommand {
	t.Helper()
	document := []byte(`
fix_status: fixed_pushed
environment: test
branches:
  - {repo: api, base_branch: test, fix_branch: fix/api, commit: aaa111, pushed: true, target_environment_branch: test, push_remote: origin}
  - {repo: web, base_branch: test, fix_branch: fix/web, commit: bbb222, pushed: true, target_environment_branch: test, push_remote: origin}
changes:
  - {repo: api, summary: api change}
  - {repo: web, summary: web change}
tests:
  - {repo: api, commit: aaa111, command: go test ./..., result: passed}
  - {repo: web, commit: bbb222, command: npm test, result: passed}
deployment_notice: deploy both
risks: []
evidence: []
`)
	parsed, err := ParsePhaseResult(attempt, document)
	if err != nil {
		t.Fatal(err)
	}
	return CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "agent-phase:" + attempt.ID, ActorID: "fixer", Outcome: parsed.Outcome, OutputJSON: parsed.OutputJSON, CodeChanges: parsed.CodeChanges}
}

func TestCompleteAttemptRejectsDivergentFixPayloadAndCodeChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompleteAttemptCommand)
	}{
		{name: "zero changes", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges = nil }},
		{name: "repository mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].Repo = "other" }},
		{name: "branch mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].BaseBranch = "main" }},
		{name: "fix branch mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].FixBranch = "fix/other" }},
		{name: "target mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].TargetEnvironmentBranch = "staging" }},
		{name: "commit mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].FixCommit = "stale" }},
		{name: "push mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].PushStatus = "failed" }},
		{name: "remote mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].PushRemote = "upstream" }},
		{name: "tests mismatch", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[0].TestEvidence = []byte(`[]`) }},
		{name: "duplicate repo", mutate: func(command *CompleteAttemptCommand) { command.CodeChanges[1].Repo = command.CodeChanges[0].Repo }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident, attempt := createRunningPhase(t, store, "completion-"+strings.ReplaceAll(test.name, " ", "-"), CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
			command := validFixCompletion(t, incident, attempt)
			test.mutate(&command)
			orchestrator := NewCaseOrchestrator(store, nil, nil, nil)
			if _, err := orchestrator.CompleteAttempt(context.Background(), command); err == nil {
				t.Fatal("divergent fix completion accepted")
			}
		})
	}
}

func TestCompleteAttemptRejectsFixChangesOnWrongPhaseOrOutcome(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "completion-wrong-phase", CaseReproduced, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
	command := validFixCompletion(t, incident, PhaseAttempt{ID: attempt.ID, CaseID: attempt.CaseID, CycleNumber: attempt.CycleNumber, Phase: PhaseFix})
	if _, err := NewCaseOrchestrator(store, nil, nil, nil).CompleteAttempt(context.Background(), command); err == nil {
		t.Fatal("FixPushed accepted for investigation attempt")
	}

	store = newOrchestratorStore(t)
	incident, attempt = createRunningPhase(t, store, "completion-failed-changes", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	command = validFixCompletion(t, incident, attempt)
	command.Outcome = PhaseOutcomeFixFailed
	if _, err := NewCaseOrchestrator(store, nil, nil, nil).CompleteAttempt(context.Background(), command); err == nil {
		t.Fatal("FixFailed accepted CodeChanges")
	}
}

func TestCompletionIntentUsesStrictFixBoundaryOnSaveAndReplay(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "completion-intent-strict", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	command := validFixCompletion(t, incident, attempt)
	invalid := command
	invalid.CodeChanges = append([]CodeChange(nil), command.CodeChanges...)
	invalid.CodeChanges[0] = invalid.CodeChanges[0].Clone()
	invalid.CodeChanges[0].FixCommit = "stale"
	if err := store.SaveCompletionIntentIfRunning(context.Background(), invalid); err == nil {
		t.Fatal("divergent completion intent was persisted")
	}
	if err := store.SaveCompletionIntentIfRunning(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCompletionIntentIfRunning(context.Background(), command); err != nil {
		t.Fatalf("exact intent replay: %v", err)
	}
	finished, err := NewCaseOrchestrator(store, nil, &recordingGitIntegration{fixInspection: FixInspection{Complete: true, Changes: command.CodeChanges}}, nil).CompleteAttempt(context.Background(), command)
	if err != nil || finished.Status != CaseWaitingMergeApproval {
		t.Fatalf("case=%+v err=%v", finished, err)
	}
}

func TestFixCompletionRemoteMismatchFailsAttemptWithoutWaitingForRestart(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "fix-remote-mismatch", CaseWaitingFixApproval, CaseFixing, PhaseFix, "", []byte(`{}`))
	command := validFixCompletion(t, incident, attempt)
	failed, err := NewCaseOrchestrator(store, nil, &recordingGitIntegration{err: ErrFixRemoteMismatch}, nil).CompleteAttempt(context.Background(), command)
	if !errors.Is(err, ErrFixRemoteMismatch) || failed.Status != CaseFixFailed {
		t.Fatalf("case=%+v err=%v", failed, err)
	}
	persisted, loadErr := store.GetAttempt(context.Background(), attempt.ID)
	if loadErr != nil || persisted.Status != AttemptStatusFailed || persisted.ErrorCode != "fix_recovery_failed" {
		t.Fatalf("attempt=%+v err=%v", persisted, loadErr)
	}
}

func completeFixForReplayTest(t *testing.T) (*CaseStore, CompleteAttemptCommand, IncidentCase, gitFixture) {
	t.Helper()
	fixture := newGitFixture(t)
	commit := fixture.makeFix(t, "replay\n")
	store, incident, root, _, orchestrator := prepareFixApprovalCase(t, validRootCauseOutput())
	approved, err := orchestrator.ApproveFix(context.Background(), ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetAttempt(context.Background(), approved.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf(`fix_status: fixed_pushed
environment: test
branches:
  - {repo: api, base_branch: test, fix_branch: fix/bug, commit: %s, pushed: true, target_environment_branch: test, push_remote: origin}
changes:
  - {repo: api, summary: replay-safe fix}
tests:
  - {repo: api, commit: %s, command: go test ./..., result: passed}
deployment_notice: deploy api
risks: []
evidence: []
`, commit, commit)
	parsed, err := ParsePhaseResult(attempt, []byte(document))
	if err != nil {
		t.Fatal(err)
	}
	command := CompleteAttemptCommand{CaseID: approved.ID, AttemptID: attempt.ID, ExpectedVersion: approved.Version, IdempotencyKey: "replay-fix:" + attempt.ID, ActorID: "fixer", Outcome: parsed.Outcome, OutputJSON: parsed.OutputJSON, Usage: AgentUsage{InputTokens: 11, OutputTokens: 7, Duration: 3 * time.Second}, CodeChanges: parsed.CodeChanges}
	orchestrator.git = fixture.service(t)
	completed, err := orchestrator.CompleteAttempt(context.Background(), command)
	if err != nil || completed.Status != CaseWaitingMergeApproval {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	return store, command, completed, fixture
}

func TestCompleteAttemptExactFixReplayUsesImmutableIdentityBeforeGit(t *testing.T) {
	store, command, completed, fixture := completeFixForReplayTest(t)
	runGitTest(t, fixture.repo, "push", "origin", "--delete", "fix/bug")
	git := &recordingGitIntegration{err: errors.New("Git must not run during exact replay")}
	replayed, err := NewCaseOrchestrator(store, nil, git, nil).CompleteAttempt(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, completed) {
		t.Fatalf("replayed=%+v want=%+v err=%v", replayed, completed, err)
	}
	git.mu.Lock()
	calls := git.fixCalls
	git.mu.Unlock()
	if calls != 0 {
		t.Fatalf("exact replay called Git %d times", calls)
	}
}

func TestCompleteAttemptFixReplayRejectsEveryIdentityDifferenceBeforeGit(t *testing.T) {
	store, command, _, _ := completeFixForReplayTest(t)
	mutations := []struct {
		name   string
		mutate func(*CompleteAttemptCommand)
	}{
		{name: "actor", mutate: func(c *CompleteAttemptCommand) { c.ActorID = "other" }},
		{name: "usage", mutate: func(c *CompleteAttemptCommand) { c.Usage.InputTokens++ }},
		{name: "payload", mutate: func(c *CompleteAttemptCommand) {
			c.OutputJSON = []byte(strings.Replace(string(c.OutputJSON), "deploy api", "deploy other", 1))
		}},
		{name: "code changes", mutate: func(c *CompleteAttemptCommand) { c.CodeChanges[0].FixCommit = strings.Repeat("b", 40) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := command
			changed.OutputJSON = CloneRawMessage(command.OutputJSON)
			changed.CodeChanges = []CodeChange{command.CodeChanges[0].Clone()}
			test.mutate(&changed)
			git := &recordingGitIntegration{err: errors.New("Git must not run for divergent replay")}
			if _, err := NewCaseOrchestrator(store, nil, git, nil).CompleteAttempt(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
			git.mu.Lock()
			calls := git.fixCalls
			git.mu.Unlock()
			if calls != 0 {
				t.Fatalf("divergent replay called Git %d times", calls)
			}
		})
	}
}

func TestCompleteAttemptExactFixReplayIsConcurrentAndSurvivesLaterWorkflowMutation(t *testing.T) {
	store, command, completed, fixture := completeFixForReplayTest(t)
	change := command.CodeChanges[0]
	if _, err := store.db.ExecContext(context.Background(), `UPDATE code_changes SET merge_base_head=?,merge_commit=?,push_status=? WHERE id=?`, "later-head", "later-merge", "merged", change.ID); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repo, "push", "origin", "--delete", "fix/bug")
	git := &recordingGitIntegration{err: errors.New("Git must not run during concurrent replay")}
	const workers = 12
	errs := make(chan error, workers)
	for range workers {
		go func() {
			got, err := NewCaseOrchestrator(store, nil, git, nil).CompleteAttempt(context.Background(), command)
			if err == nil && !reflect.DeepEqual(got, completed) {
				err = fmt.Errorf("replay result changed: %+v", got)
			}
			errs <- err
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	git.mu.Lock()
	calls := git.fixCalls
	git.mu.Unlock()
	if calls != 0 {
		t.Fatalf("concurrent replay called Git %d times", calls)
	}
}

func TestCompletionIntentRejectsFixPushedForNonFixAttempt(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, attempt := createRunningPhase(t, store, "completion-intent-wrong-phase", CaseReproduced, CaseInvestigating, PhaseInvestigation, "", []byte(`{}`))
	command := validFixCompletion(t, incident, PhaseAttempt{ID: attempt.ID, CaseID: attempt.CaseID, CycleNumber: attempt.CycleNumber, Phase: PhaseFix})
	if err := store.SaveCompletionIntentIfRunning(context.Background(), command); err == nil {
		t.Fatal("fix-pushed intent was saved for investigation attempt")
	}
}

func replaceFixLine(document, old, replacement string) string {
	return strings.Replace(document, old, replacement, 1)
}
