package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func readyCaseForRemediationReassessment(t *testing.T, store *CaseStore, id string) (IncidentCase, PhaseAttempt) {
	t.Helper()
	incident := createWorkflowCase(t, store, id, CaseWaitingFixApproval)
	now := time.Now().UTC()
	root := PhaseAttempt{
		ID: id + "-root", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation,
		Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator", InputJSON: []byte(`{"validation_attempt_id":"validation-1","scenario_hash":"scenario-1","validation_evidence":[{"artifact_id":"artifact-1","kind":"request_facts","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment":"test"}]}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"frontend renders nickname and text as separate titles","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"frontend card","summary":"deduplicate equal labels","verification":"run the original search"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
		StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(context.Background(), CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: id + ":bind-root", RequestJSON: []byte(`{}`),
		Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(root.ID)},
		Steps:    []CaseMutationStep{{To: CaseWaitingFixApproval, AuditOnly: true, Event: TransitionEvent{ID: id + "-bind-event", EventType: "root_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return bound.Case, root
}

func TestReconsiderRemediationStartsReadOnlyInvestigationAndReplays(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, root := readyCaseForRemediationReassessment(t, store, "case-reconsider")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	command := ReconsiderRemediationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ReconsiderRemediationKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID,
		Proposal: "优先在后端兼容字段语义，前端只保留兜底去重",
		Bug:      Bug{ID: incident.BugID}, Bot: BotRef{Key: "investigator", Target: "codex"},
	}
	updated, err := orchestrator.ReconsiderRemediation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != CaseInvestigating || updated.CurrentAttemptID == root.ID {
		t.Fatalf("unexpected reassessment Case: %+v", updated)
	}
	started, err := store.GetAttempt(context.Background(), updated.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase != PhaseInvestigation || started.ParentAttemptID != root.ID || started.BotKey != "investigator" {
		t.Fatalf("unexpected reassessment attempt: %+v", started)
	}
	input, ok := remediationReassessmentFromInput(started.InputJSON)
	if !ok || input.Proposal != command.Proposal || input.PreviousResult.RootCause != "frontend renders nickname and text as separate titles" {
		t.Fatalf("reassessment handoff lost context: %+v ok=%v", input, ok)
	}
	if !strings.Contains(string(started.InputJSON), `"validation_attempt_id":"validation-1"`) || !strings.Contains(string(started.InputJSON), `"artifact_id":"artifact-1"`) {
		t.Fatalf("reassessment lost frozen validation handoff: %s", started.InputJSON)
	}
	if runner.startCount() != 1 {
		t.Fatalf("runner starts=%d, want 1", runner.startCount())
	}
	event, found, err := store.GetEventByIdempotencyKey(context.Background(), command.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("load reassessment audit event: found=%v err=%v", found, err)
	}
	if strings.Contains(string(event.PayloadJSON), command.Proposal) || !strings.Contains(string(event.PayloadJSON), `"proposal_sha256"`) {
		t.Fatalf("audit event must retain only the proposal digest: %s", event.PayloadJSON)
	}

	replayed, err := orchestrator.ReconsiderRemediation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != updated.ID || replayed.Version != updated.Version || replayed.CurrentAttemptID != updated.CurrentAttemptID || runner.startCount() != 1 {
		t.Fatalf("unsafe replay: replayed=%+v starts=%d", replayed, runner.startCount())
	}
	revisedOutput := []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"frontend and backend disagree on field semantics","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"backend response adapter","summary":"normalize the compatibility field in the backend and keep a frontend guard","verification":"run the original user search and API contract tests"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`)
	completed, err := orchestrator.CompleteAttempt(context.Background(), CompleteAttemptCommand{
		CaseID: updated.ID, AttemptID: started.ID, ExpectedVersion: updated.Version,
		IdempotencyKey: "complete-reassessment", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: revisedOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CaseWaitingFixApproval || completed.CurrentAttemptID != started.ID {
		t.Fatalf("reassessment did not return to fix approval: %+v", completed)
	}
}

func TestReconsiderCompletedFixCreatesReworkAssessmentAndFreshFixAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-rework-pushed-fix", CaseWaitingMergeApproval)
	now := time.Now().UTC()
	rootResult := InvestigationResult{
		InvestigationStatus: "root_cause_ready", Environment: "test", RootCause: "backend maps signature to nickname",
		Confidence: "high", RootCauseType: RootCauseCode,
		Remediation: RemediationPlan{Mode: RemediationCodeChange, Repositories: []string{"backend"}, Target: "response mapper", Summary: "map signature independently", Verification: "rerun original search"},
		CallChain:   []CallChainHop{}, Evidence: []ArtifactReference{}, ValidationGaps: []string{}, Gaps: []string{}, UncheckedScopes: []string{},
	}
	root := PhaseAttempt{
		ID: incident.ID + "-root", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation,
		Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator", InputJSON: []byte(`{"validation_attempt_id":"validation-1"}`),
		OutputJSON: mustJSON(rootResult), StartedAt: now.Add(-2 * time.Minute), FinishedAt: &now,
	}
	previousFix := FixResult{
		FixStatus: "fixed_pushed", Environment: "test",
		Branches:         []FixBranchResult{{Repo: "backend", BaseBranch: "feature/work", FixBranch: "fix/old", Commit: "old111", Pushed: true, TargetEnvironmentBranch: "test", PushRemote: "origin"}},
		Changes:          []FixChangeResult{{Repo: "backend", Summary: "changed the wrong mapper"}},
		Tests:            []FixTestResult{{Repo: "backend", Commit: "old111", Command: "go test ./...", Result: "passed"}},
		DeploymentNotice: "deploy backend", Risks: []string{}, Evidence: []ArtifactReference{},
	}
	fix := PhaseAttempt{
		ID: incident.ID + "-fix", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseFix,
		Status: AttemptStatusSucceeded, ParentAttemptID: root.ID, AgentTarget: "codex", BotKey: "investigator",
		InputJSON: []byte(`{"source_baselines":{"backend":"feature/work"}}`), OutputJSON: mustJSON(previousFix),
		StartedAt: now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, fix); err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: incident.ID + ":bind-fix", RequestJSON: []byte(`{}`),
		Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(fix.ID), SelectedBotKey: workflowStringPtr(fix.BotKey)},
		Steps: []CaseMutationStep{{To: CaseWaitingMergeApproval, AuditOnly: true, Event: TransitionEvent{
			ID: incident.ID + "-bind-event", EventType: "fix_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	incident = bound.Case
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	command := ReconsiderRemediationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ReconsiderRemediationKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID,
		Proposal: "旧修复改错位置；保留根因，但改为兼容 Mongo fallback 和 ES refresh 两条路径",
		Bug:      Bug{ID: incident.BugID}, Bot: BotRef{Key: "investigator", Target: "codex"},
	}
	reassessing, err := orchestrator.ReconsiderRemediation(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if reassessing.Status != CaseInvestigating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d", reassessing, runner.startCount())
	}
	reassessmentAttempt, err := store.GetAttempt(ctx, reassessing.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	reassessment, ok := remediationReassessmentFromInput(reassessmentAttempt.InputJSON)
	wantSuffix := fmt.Sprintf("-rework-v%d", incident.Version)
	if !ok || reassessment.SourceFixAttemptID != fix.ID || reassessment.PreviousFixResult == nil ||
		reassessment.PreviousFixResult.Branches[0].FixBranch != "fix/old" || reassessment.RequiredFixBranchSuffix != wantSuffix {
		t.Fatalf("rework handoff=%+v ok=%v", reassessment, ok)
	}
	event, found, err := store.GetEventByIdempotencyKey(ctx, command.IdempotencyKey)
	if err != nil || !found || event.EventType != "fix_rework_requested" ||
		!strings.Contains(string(event.PayloadJSON), `"source_fix_attempt_id":"`+fix.ID+`"`) ||
		strings.Contains(string(event.PayloadJSON), command.Proposal) {
		t.Fatalf("event=%+v found=%v err=%v", event, found, err)
	}
	replayed, err := orchestrator.ReconsiderRemediation(ctx, command)
	if err != nil || replayed != reassessing || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}

	revised := rootResult
	revised.Remediation = RemediationPlan{
		Mode: RemediationCodeChange, Repositories: []string{"backend"}, Target: "fallback and refresh response mapping",
		Summary: "map signature on both paths without changing nickname", Verification: "rerun the original search and contract tests",
	}
	waitingApproval, err := orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{
		CaseID: reassessing.ID, AttemptID: reassessmentAttempt.ID, ExpectedVersion: reassessing.Version,
		IdempotencyKey: "complete-rework-assessment", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: mustJSON(revised),
	})
	if err != nil || waitingApproval.Status != CaseWaitingFixApproval {
		t.Fatalf("case=%+v err=%v", waitingApproval, err)
	}
	approved, err := orchestrator.ApproveFix(ctx, ApproveFixCommand{
		CaseID: waitingApproval.ID, ExpectedVersion: waitingApproval.Version,
		IdempotencyKey: StartFixApprovalKey(waitingApproval.ID, reassessmentAttempt.ID, waitingApproval.Version),
		ActorID:        "alice", RootCauseAttemptID: reassessmentAttempt.ID, Bug: Bug{ID: waitingApproval.BugID},
		Bot:       BotRef{Key: "investigator", Target: "codex", Path: writeFixWorkspaceBranchMap(t, "test", "backend", "test")},
		InputJSON: []byte(`{"source_baselines":{"backend":"feature/work"}}`),
	})
	if err != nil || approved.Status != CaseFixing || runner.startCount() != 2 {
		t.Fatalf("case=%+v starts=%d err=%v", approved, runner.startCount(), err)
	}
	newFix, err := store.GetAttempt(ctx, approved.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	rework, ok := fixReworkFromInput(newFix.InputJSON)
	if !ok || rework.SourceFixAttemptID != fix.ID || rework.RequiredFixBranchSuffix != wantSuffix ||
		rework.UserFeedback != command.Proposal {
		t.Fatalf("approved rework input=%s parsed=%+v ok=%v", newFix.InputJSON, rework, ok)
	}
	approvals, err := store.ListApprovals(ctx, incident.ID)
	if err != nil || len(approvals) != 1 ||
		!strings.Contains(string(approvals[0].ScopeJSON), `"rework_of_fix_attempt_id":"`+fix.ID+`"`) ||
		!strings.Contains(string(approvals[0].ScopeJSON), `"required_fix_branch_suffix":"`+wantSuffix+`"`) {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
}

func TestReconsiderRemediationRejectsInvalidScopeAndProposal(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, root := readyCaseForRemediationReassessment(t, store, "case-reconsider-invalid")
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	base := ReconsiderRemediationCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, ActorID: "alice", RootCauseAttemptID: root.ID, Proposal: "改由后端修复", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "investigator", Target: "codex"}}
	base.IdempotencyKey = "wrong"
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("scope err=%v", err)
	}
	base.IdempotencyKey = ReconsiderRemediationKey(base.CaseID, base.RootCauseAttemptID, base.ExpectedVersion)
	base.Proposal = "   "
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); err == nil || !strings.Contains(err.Error(), "proposal is required") {
		t.Fatalf("empty proposal err=%v", err)
	}
	base.Proposal = "authorization: Bearer abcdefghijklmnopqrstuvwxyz"
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("sensitive proposal err=%v", err)
	}
}

func TestParseReworkedFixRequiresFreshBranchSuffix(t *testing.T) {
	rework := fixReworkContext{
		SourceFixAttemptID: "fix-old", UserFeedback: "implement the revised mapper",
		PreviousFixResult: FixResult{
			FixStatus: "fixed_pushed",
			Branches:  []FixBranchResult{{Repo: "backend", FixBranch: "fix/old"}},
		},
		RequiredFixBranchSuffix: "-rework-v12",
	}
	attempt := PhaseAttempt{
		ID: "fix-new", CaseID: "case", Phase: PhaseFix,
		InputJSON: mustJSON(map[string]any{"fix_rework": rework}),
	}
	document := `
fix_status: fixed_pushed
environment: test
branches:
  - {repo: backend, base_branch: feature/work, fix_branch: fix/new-rework-v12, commit: new111, pushed: true, target_environment_branch: test, push_remote: origin}
changes:
  - {repo: backend, summary: implement revised mapping}
tests:
  - {repo: backend, commit: new111, command: go test ./..., result: passed}
deployment_notice: deploy backend
risks: []
evidence: []
`
	if _, err := ParsePhaseResult(attempt, []byte(document)); err != nil {
		t.Fatalf("fresh rework branch rejected: %v", err)
	}
	for _, branch := range []string{"fix/old", "fix/new"} {
		invalid := strings.Replace(document, "fix/new-rework-v12", branch, 1)
		if _, err := ParsePhaseResult(attempt, []byte(invalid)); err == nil {
			t.Fatalf("accepted unsafe rework branch %q", branch)
		}
	}
}

func TestInvestigationPromptExplainsRemediationReassessmentBoundary(t *testing.T) {
	input := remediationReassessmentInput{
		Kind: "user_remediation_proposal", Proposal: "后端统一字段语义", SourceRootCauseAttemptID: "root-1",
		PreviousResult: InvestigationResult{
			InvestigationStatus: "root_cause_ready", Environment: "test", RootCause: "field mismatch", Confidence: "high", RootCauseType: RootCauseCode,
			Remediation: RemediationPlan{Mode: RemediationCodeChange, Repositories: []string{"frontend"}, Target: "frontend card", Summary: "deduplicate labels", Verification: "rerun search"},
		},
	}
	prompt, err := (&AgentPhaseRunner{}).promptForAttempt(PhaseAttempt{Phase: PhaseInvestigation, InputJSON: mustJSON(remediationReassessmentEnvelope{RemediationReassessment: input})}, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"修复方案评估 Agent", "不是重新排障", "不可变事实", "没有授权任何写操作", "root-1", "后端统一字段语义", "previous_result"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{"incident-investigator", "[[TSHOOT_STEP", "code-intelligence-manifest", "codegraph_explore", "validation-evidence-manifest", "Studio evidence staging"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("reassessment prompt unexpectedly contains %q:\n%s", forbidden, prompt)
		}
	}
}

func TestParseRemediationReassessmentMergesOnlyRemediation(t *testing.T) {
	previous := InvestigationResult{
		InvestigationStatus: "root_cause_ready",
		Environment:         "test",
		RootCause:           "backend response maps text to nickname",
		Confidence:          "high",
		RootCauseType:       RootCauseCode,
		Remediation:         RemediationPlan{Mode: RemediationCodeChange, Repositories: []string{"frontend"}, Target: "result card", Summary: "deduplicate labels", Verification: "rerun search"},
		CallChain:           []CallChainHop{{Kind: "service", Name: "search adapter", Repo: "backend", Precision: "static_candidate", Evidence: "frozen response and source agree"}},
		Evidence:            []ArtifactReference{{Kind: "response_facts", Path: "response-facts.json", Environment: "test"}},
		ValidationGaps:      []string{},
		Gaps:                []string{},
		UncheckedScopes:     []string{},
	}
	attempt := PhaseAttempt{
		Phase: PhaseInvestigation,
		InputJSON: mustJSON(remediationReassessmentEnvelope{RemediationReassessment: remediationReassessmentInput{
			Kind:                     "user_remediation_proposal",
			Proposal:                 "在后端恢复 signature 字段语义",
			SourceRootCauseAttemptID: "root-1",
			PreviousResult:           previous,
		}}),
	}
	output := []byte(`remediation:
  mode: code_change
  repositories: [backend]
  target: user search response mapper
  summary: map signature independently and preserve nickname compatibility
  rollback: revert the response mapping commit
  verification: rerun the original search and response contract tests
`)
	parsed, err := ParsePhaseResult(attempt, output)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Outcome != PhaseOutcomeRootCauseReady || len(parsed.ArtifactInputs) != 0 {
		t.Fatalf("unexpected reassessment phase result: %+v", parsed)
	}
	var merged InvestigationResult
	if err := json.Unmarshal(parsed.OutputJSON, &merged); err != nil {
		t.Fatal(err)
	}
	if merged.RootCause != previous.RootCause || merged.Confidence != previous.Confidence || merged.Environment != previous.Environment ||
		len(merged.CallChain) != 1 || merged.CallChain[0] != previous.CallChain[0] ||
		len(merged.Evidence) != 1 || merged.Evidence[0].Path != previous.Evidence[0].Path {
		t.Fatalf("reassessment changed immutable investigation facts: %+v", merged)
	}
	if len(merged.Remediation.Repositories) != 1 || merged.Remediation.Repositories[0] != "backend" || merged.Remediation.Target != "user search response mapper" {
		t.Fatalf("reassessment did not replace remediation: %+v", merged.Remediation)
	}

	if _, err := ParsePhaseResult(attempt, append(output, []byte("root_cause: rewritten\n")...)); err == nil {
		t.Fatal("accepted reassessment output that rewrites root cause")
	}
	if _, err := ParsePhaseResult(attempt, []byte(`remediation:
  mode: operator_action
  repositories: []
  target: runtime
  summary: restart service
  rollback: restart again
  verification: rerun search
`)); err == nil || !strings.Contains(err.Error(), "code root cause requires code_change") {
		t.Fatalf("accepted remediation mode incompatible with immutable root cause: %v", err)
	}
}

func TestRemediationReassessmentRunnerSkipsInvestigationToolPreparation(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-reassessment-runner", CaseInvestigating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
	attempt.InputJSON = mustJSON(remediationReassessmentEnvelope{RemediationReassessment: remediationReassessmentInput{
		Kind:                     "user_remediation_proposal",
		Proposal:                 "在后端修复字段映射",
		SourceRootCauseAttemptID: "root-1",
		PreviousResult: InvestigationResult{
			InvestigationStatus: "root_cause_ready",
			Environment:         "test",
			RootCause:           "response field mismatch",
			Confidence:          "high",
			RootCauseType:       RootCauseCode,
			Remediation:         RemediationPlan{Mode: RemediationCodeChange, Repositories: []string{"frontend"}, Target: "result card", Summary: "deduplicate labels", Verification: "rerun search"},
			CallChain:           []CallChainHop{},
			Evidence:            []ArtifactReference{},
			ValidationGaps:      []string{},
			Gaps:                []string{},
			UncheckedScopes:     []string{},
		},
	}})
	if _, err := store.db.Exec(`UPDATE phase_attempts SET input_json=? WHERE id=?`, string(attempt.InputJSON), attempt.ID); err != nil {
		t.Fatal(err)
	}

	var frontendCalls, repositoryCalls, codeGraphCalls int
	executorCalls := 0
	executedPrompt := make(chan string, 1)
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		executorCalls++
		executedPrompt <- prompt
		return PhaseExecutionResult{FinalYAML: `remediation:
  mode: code_change
  repositories: [backend]
  target: response mapper
  summary: map signature independently
  verification: rerun original search
`}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return nil
	})
	runner.SetFrontendRuntimeResolver(FrontendRuntimeResolverFunc(func(context.Context, IncidentCase) (FrontendRuntimeManifest, error) {
		frontendCalls++
		return FrontendRuntimeManifest{}, nil
	}))
	runner.SetRepositoryAccessResolver(RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
		repositoryCalls++
		return map[string]string{"backend": t.TempDir()}, nil
	}))
	runner.SetCodeIntelligenceResolver(CodeIntelligenceResolverFunc(func(context.Context, IncidentCase) (CodeIntelligenceManifest, error) {
		codeGraphCalls++
		return CodeIntelligenceManifest{Enabled: true, Ready: 1, Total: 1}, nil
	}))

	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if executorCalls != 1 || frontendCalls != 0 || repositoryCalls != 0 || codeGraphCalls != 0 {
		t.Fatalf("calls executor=%d frontend=%d repository=%d codegraph=%d", executorCalls, frontendCalls, repositoryCalls, codeGraphCalls)
	}
	if command.Outcome != PhaseOutcomeRootCauseReady || command.ErrorCode != "" {
		t.Fatalf("unexpected reassessment completion: %+v", command)
	}
	prompt := <-executedPrompt
	for _, forbidden := range []string{"[[TSHOOT_STEP", "code-intelligence-manifest", "codegraph_explore", "STUDIO_EVIDENCE_STAGING_DIR", "repository-access-manifest", "runtime-code-manifest"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("reassessment executor prompt contains investigation handoff %q:\n%s", forbidden, prompt)
		}
	}
}
