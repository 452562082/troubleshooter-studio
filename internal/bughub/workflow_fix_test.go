package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
	base := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)}

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
			cmd := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, root.ID, incident.Version), ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)}
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
	command := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)}
	updated, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil || updated.Status != CaseFixing || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d err=%v", updated, runner.startCount(), err)
	}
	approvals, err := store.ListApprovals(context.Background(), incident.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	if approvals[0].CaseVersion != incident.Version || string(approvals[0].ScopeJSON) != `{"root_cause_attempt_id":"investigation-root"}` {
		t.Fatalf("approval=%+v", approvals[0])
	}
	replayed, err := orchestrator.ApproveFix(context.Background(), command)
	if err != nil || replayed != updated || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
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
		ActorID:        "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`),
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

func replaceFixLine(document, old, replacement string) string {
	return strings.Replace(document, old, replacement, 1)
}
