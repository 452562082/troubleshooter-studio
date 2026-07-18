package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type workflowE2EGit struct {
	inner  *GitIntegrationService
	mu     sync.Mutex
	pushes int
}

func (g *workflowE2EGit) Inspect(ctx context.Context, request MergeRequest) (MergeInspection, error) {
	return g.inner.Inspect(ctx, request)
}
func (g *workflowE2EGit) InspectFix(ctx context.Context, request FixInspectionRequest) (FixInspection, error) {
	return g.inner.InspectFix(ctx, request)
}
func (g *workflowE2EGit) ResumePush(ctx context.Context, request MergeRequest) (MergeResult, error) {
	return g.inner.ResumePush(ctx, request)
}
func (g *workflowE2EGit) MergeAndPush(ctx context.Context, request MergeRequest) (MergeResult, error) {
	g.mu.Lock()
	g.pushes++
	g.mu.Unlock()
	return g.inner.MergeAndPush(ctx, request)
}
func (g *workflowE2EGit) pushCount() int { g.mu.Lock(); defer g.mu.Unlock(); return g.pushes }

type workflowE2EVerifier struct {
	inner DeploymentVerifier
	mu    sync.Mutex
	calls int
}

func (v *workflowE2EVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	v.mu.Lock()
	v.calls++
	v.mu.Unlock()
	return v.inner.Verify(ctx, request)
}
func (v *workflowE2EVerifier) callCount() int { v.mu.Lock(); defer v.mu.Unlock(); return v.calls }

func TestWorkflowE2EFixedVerifiedSurvivesSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	fixture := newGitFixture(t)
	fixCommit := fixture.makeFix(t, "repair checkout race\n")
	git := &workflowE2EGit{inner: fixture.service(t)}
	runner := &recordingPhaseRunner{}
	verifier := &workflowE2EVerifier{inner: ManualVersionVerifier{Environment: "test"}}
	orchestrator := NewCaseOrchestrator(store, runner, git, verifier)
	orchestrator.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: t.TempDir(), Env: incident.Environment}, nil
	}))
	bug := Bug{ID: "bug-e2e", Source: "zentao", SystemID: "shop", Env: "test", Expected: "checkout succeeds"}
	validator := BotRef{Key: "validator", Target: "codex", Path: t.TempDir(), Env: "test"}

	incident, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-e2e", IdempotencyKey: "e2e:create", ActorID: "alice", Bug: bug, Bot: validator, InputJSON: []byte(`{"reproduction_steps":["submit checkout"],"expected_behavior":"checkout succeeds"}`)})
	if err != nil || incident.Status != CaseValidating {
		t.Fatalf("start=%+v err=%v", incident, err)
	}
	validation, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
	originalArtifact := EvidenceArtifact{ID: "e2e-original-evidence", CaseID: incident.ID, AttemptID: validation.ID, Kind: "api", PathOrReference: "/artifacts/e2e/original", SHA256: strings.Repeat("a", 64), CapturedAt: validation.StartedAt.Add(time.Second), Environment: "test", Version: "before-fix", RequestID: "request-e2e-original", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, originalArtifact, nil); err != nil {
		t.Fatal(err)
	}
	validationOutput := []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"checkout succeeds","evidence":[{"kind":"api","path":"response.json","environment":"test","redaction_status":"not_required"}],"gaps":[]}`)
	incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: validation.ID, ExpectedVersion: incident.Version, IdempotencyKey: "e2e:validation", ActorID: "validator", Outcome: PhaseOutcomeReproduced, OutputJSON: validationOutput})
	if err != nil || incident.Status != CaseInvestigating {
		t.Fatalf("validation=%+v err=%v", incident, err)
	}
	investigation, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
	var handoff InitialInvestigationInput
	if err := json.Unmarshal(investigation.InputJSON, &handoff); err != nil {
		t.Fatalf("decode validation evidence handoff: %v", err)
	}
	if handoff.ValidationAttemptID != validation.ID || handoff.ObservedBehavior != "timeout" || handoff.ExpectedBehavior != "checkout succeeds" || len(handoff.Evidence) != 1 || handoff.Evidence[0].ArtifactID != originalArtifact.ID || handoff.Evidence[0].SHA256 != originalArtifact.SHA256 {
		t.Fatalf("validation evidence handoff = %+v", handoff)
	}
	rootOutput := []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"checkout race","confidence":"high","evidence":[],"gaps":[]}`)
	incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: investigation.ID, ExpectedVersion: incident.Version, IdempotencyKey: "e2e:investigation", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: rootOutput})
	if err != nil || incident.Status != CaseWaitingFixApproval {
		t.Fatalf("investigation=%+v err=%v", incident, err)
	}

	fixKey := StartFixApprovalKey(incident.ID, investigation.ID, incident.Version)
	incident, err = orchestrator.ApproveFix(ctx, ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: fixKey, ActorID: "alice", RootCauseAttemptID: investigation.ID, Bug: bug, Bot: BotRef{Key: "fixer", Target: "codex", Path: t.TempDir(), Env: "test"}, InputJSON: []byte(`{}`)})
	if err != nil || incident.Status != CaseFixing {
		t.Fatalf("fix approval=%+v err=%v", incident, err)
	}
	fixAttempt, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
	tests := []FixTestResult{{Repo: "api", Commit: fixCommit, Command: "go test ./...", Result: "passed"}}
	testJSON, _ := json.Marshal(tests)
	fixOutput := mustJSON(FixResult{FixStatus: "fixed_pushed", Environment: "test", Branches: []FixBranchResult{{Repo: "api", BaseBranch: "test", FixBranch: "fix/bug", Commit: fixCommit, Pushed: true, TargetEnvironmentBranch: "test", PushRemote: "origin"}}, Changes: []FixChangeResult{{Repo: "api", Summary: "guard checkout race"}}, Tests: tests, DeploymentNotice: "deploy api to test", Risks: []string{}, Evidence: []ArtifactReference{}})
	change := CodeChange{ID: "change-api", CaseID: incident.ID, AttemptID: fixAttempt.ID, Repo: "api", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: fixCommit, TestEvidence: testJSON, TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: fixAttempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "e2e:fix", ActorID: "fixer", Outcome: PhaseOutcomeFixPushed, OutputJSON: fixOutput, CodeChanges: []CodeChange{change}})
	if err != nil || incident.Status != CaseWaitingMergeApproval {
		t.Fatalf("fix=%+v err=%v", incident, err)
	}

	inspection, err := git.Inspect(ctx, MergeRequest{CaseID: incident.ID, FixCommits: map[string]string{"api": fixCommit}, TargetBranches: map[string]string{"api": "test"}, Changes: []CodeChange{change}})
	if err != nil {
		t.Fatal(err)
	}
	mergeKey := "e2e:merge-approval"
	incident, err = orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: mergeKey, ActorID: "alice", TargetHeads: map[string]string{"api": inspection.Repositories["api"].TargetHead}})
	if err != nil || incident.Status != CaseWaitingDeployment || git.pushCount() != 1 {
		t.Fatalf("merge=%+v pushes=%d err=%v", incident, git.pushCount(), err)
	}
	changes, _ := store.ListCodeChanges(ctx, incident.ID)
	if len(changes) != 1 || changes[0].FixCommit != fixCommit || changes[0].MergeCommit != fixCommit || strings.TrimSpace(runGitTest(t, fixture.repo, "ls-remote", "origin", "refs/heads/test")) != fixCommit+"\trefs/heads/test" {
		t.Fatalf("changes=%+v", changes)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reopened, err := store.GetCase(ctx, incident.ID)
	if err != nil || reopened.Status != CaseWaitingDeployment {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	runnerAfterRestart := &recordingPhaseRunner{}
	orchestrator = NewCaseOrchestrator(store, runnerAfterRestart, git, verifier)
	notify := NotifyDeployedCommand{CaseID: reopened.ID, ExpectedVersion: reopened.Version, IdempotencyKey: "e2e:deployed", ActorID: "alice", ObservedVersion: "build-e2e", ObservedCommits: map[string]string{"api": fixCommit}, Source: "manual", Bug: bug, Bot: validator, InputJSON: []byte(`{}`)}
	incident, err = orchestrator.NotifyDeployed(ctx, notify)
	if err != nil || incident.Status != CaseRegressionValidating || verifier.callCount() != 1 {
		t.Fatalf("deployed=%+v verifier=%d err=%v", incident, verifier.callCount(), err)
	}
	regression, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
	recordRegressionArtifact(t, store, regression, "request-e2e-fresh", time.Now().UTC().Add(time.Second))
	incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: regression.ID, ExpectedVersion: incident.Version, IdempotencyKey: "e2e:regression", ActorID: "validator", Outcome: PhaseOutcomeFixedVerified, OutputJSON: regressionOutput(t, regression, "fixed_verified", "checkout succeeds")})
	if err != nil || incident.Status != CaseFixedVerified || incident.ClosedAt == nil {
		t.Fatalf("regression=%+v err=%v", incident, err)
	}

	approvals, _ := store.ListApprovals(ctx, incident.ID)
	observations, _ := store.ListDeploymentObservations(ctx, incident.ID)
	events, _ := store.ListEvents(ctx, incident.ID)
	if len(approvals) != 2 || approvals[0].Kind == approvals[1].Kind || len(observations) != 1 || runnerAfterRestart.startCount() != 1 {
		t.Fatalf("approvals=%+v observations=%+v starts=%d", approvals, observations, runnerAfterRestart.startCount())
	}
	keys := map[string]struct{}{}
	visited := make([]CaseStatus, 0, len(events))
	for _, event := range events {
		if _, duplicate := keys[event.IdempotencyKey]; duplicate {
			t.Fatalf("duplicate event key %q", event.IdempotencyKey)
		}
		keys[event.IdempotencyKey] = struct{}{}
		visited = append(visited, event.ToStatus)
	}
	wantPath := []CaseStatus{CaseValidating, CaseReproduced, CaseInvestigating, CaseRootCauseReady, CaseWaitingFixApproval, CaseFixing, CaseFixPushed, CaseWaitingMergeApproval, CaseMerging, CaseWaitingDeployment, CaseDeploymentVerified, CaseRegressionValidating, CaseFixedVerified}
	next := 0
	for _, status := range visited {
		if next < len(wantPath) && status == wantPath[next] {
			next++
		}
	}
	if next != len(wantPath) {
		t.Fatalf("success path stopped at %d/%d: visited=%v", next, len(wantPath), visited)
	}
}

func TestWorkflowE2E_ResetStartsFreshAuditedCase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "workflow-reset.db")
	store, err := OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	bug := Bug{ID: "840", Source: "zentao", SystemID: "shop", Env: "test", Expected: "checkout succeeds"}
	bot := BotRef{Key: "validator", Target: "codex", Path: t.TempDir(), Env: "test"}
	first, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-840-candidate-a", IdempotencyKey: "e2e:840:start:a", ActorID: "alice", Bug: bug, Bot: bot, InputJSON: []byte(`{"reproduction_steps":["submit checkout"]}`)})
	if err != nil || first.Status != CaseValidating {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	reused, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-840-candidate-b", IdempotencyKey: "e2e:840:start:b", ActorID: "alice", Bug: bug, Bot: bot, InputJSON: []byte(`{"reproduction_steps":["retry checkout"]}`)})
	if err != nil || reused.ID != first.ID || reused.Status != CaseValidating || runner.startCount() != 1 {
		t.Fatalf("first=%+v reused=%+v starts=%d err=%v", first, reused, runner.startCount(), err)
	}
	if _, err := store.GetCase(ctx, "case-840-candidate-b"); !errors.Is(err, ErrCaseNotFound) {
		t.Fatalf("unused candidate exists: err=%v", err)
	}

	validation, err := store.GetAttempt(ctx, first.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC()
	artifact := EvidenceArtifact{ID: "e2e-840-evidence", CaseID: first.ID, AttemptID: validation.ID, Kind: "api", PathOrReference: "/artifacts/e2e/840-response", SHA256: strings.Repeat("a", 64), CapturedAt: observedAt, Environment: "test", Version: "before-reset", RequestID: "request-e2e-840", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, artifact, nil); err != nil {
		t.Fatal(err)
	}
	approval := Approval{ID: "e2e-840-approval", CaseID: first.ID, Kind: ApprovalStartFix, Actor: "alice", ApprovedAt: observedAt, CaseVersion: first.Version, ScopeJSON: mustJSON(map[string]string{"root_cause_attempt_id": validation.ID})}
	if err := store.RecordApproval(ctx, approval, "e2e:840:approval"); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: "e2e-840-change", CaseID: first.ID, AttemptID: validation.ID, Repo: "api", BaseBranch: "test", FixBranch: "fix/840", FixCommit: "commit-840", TestEvidence: []byte(`{"command":"go test ./...","result":"passed"}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	notifiedAt := observedAt
	observation := DeploymentObservation{ID: "e2e-840-observation", CaseID: first.ID, Environment: "test", ExpectedCommits: map[string]string{"api": "commit-840"}, UserNotifiedAt: &notifiedAt, VerificationSource: "manual", ObservedVersion: "build-before-reset", ObservedCommits: map[string]string{"api": "commit-840"}, ObservedAt: observedAt, Result: DeploymentResultUnavailable}
	if err := store.RecordDeploymentObservation(ctx, observation, "e2e:840:observation"); err != nil {
		t.Fatal(err)
	}
	artifactsBefore, err := store.ListEvidenceArtifacts(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	approvalsBefore, err := store.ListApprovals(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	changesBefore, err := store.ListCodeChanges(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	observationsBefore, err := store.ListDeploymentObservations(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	reset := ResetCaseCommand{CaseID: first.ID, NewCaseID: "case-840-reset", ExpectedVersion: first.Version, IdempotencyKey: "e2e:840:reset", ActorID: "alice", Bug: bug, Bot: bot, InputJSON: []byte(`{"reason":"retry from validation"}`)}
	replacement, err := orchestrator.ResetCase(ctx, reset)
	if err != nil || replacement.ID != reset.NewCaseID || replacement.Status != CaseValidating || replacement.ResetFromCaseID != first.ID || runner.startCount() != 2 {
		t.Fatalf("replacement=%+v starts=%d err=%v", replacement, runner.startCount(), err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	archived, err := store.GetCase(ctx, first.ID)
	if err != nil || archived.Status != CaseResetArchived || archived.SupersededByCaseID != replacement.ID || archived.CurrentAttemptID != "" || archived.ClosedAt == nil {
		t.Fatalf("archived=%+v err=%v", archived, err)
	}
	reopenedReplacement, err := store.GetCase(ctx, replacement.ID)
	if err != nil || reopenedReplacement.Status != CaseValidating || reopenedReplacement.ResetFromCaseID != archived.ID || reopenedReplacement.CurrentAttemptID == "" || reopenedReplacement.ClosedAt != nil {
		t.Fatalf("replacement=%+v err=%v", reopenedReplacement, err)
	}
	oldAttempt, err := store.GetAttempt(ctx, validation.ID)
	if err != nil || oldAttempt.Status != AttemptStatusCancelled || oldAttempt.FinishedAt == nil {
		t.Fatalf("old attempt=%+v err=%v", oldAttempt, err)
	}
	artifactsAfter, err := store.ListEvidenceArtifacts(ctx, archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	approvalsAfter, err := store.ListApprovals(ctx, archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	changesAfter, err := store.ListCodeChanges(ctx, archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	observationsAfter, err := store.ListDeploymentObservations(ctx, archived.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(artifactsBefore, artifactsAfter) || !reflect.DeepEqual(approvalsBefore, approvalsAfter) || !reflect.DeepEqual(changesBefore, changesAfter) || !reflect.DeepEqual(observationsBefore, observationsAfter) {
		t.Fatalf("archived audit records changed: artifacts=%+v approvals=%+v changes=%+v observations=%+v", artifactsAfter, approvalsAfter, changesAfter, observationsAfter)
	}

	immutable := archived.Clone()
	if _, err := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil).CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: archived.ID, AttemptID: validation.ID, ExpectedVersion: archived.Version, IdempotencyKey: "e2e:840:late-completion", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["trace"]}`)}); err == nil {
		t.Fatal("late completion mutated reset archive")
	}
	archivedAfterLateCompletion, err := store.GetCase(ctx, archived.ID)
	if err != nil || !reflect.DeepEqual(immutable, archivedAfterLateCompletion) {
		t.Fatalf("archive changed after late completion: before=%+v after=%+v err=%v", immutable, archivedAfterLateCompletion, err)
	}

	restartedRunner := &recordingPhaseRunner{}
	restarted := NewCaseOrchestrator(store, restartedRunner, nil, nil)
	restarted.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment, Expected: bug.Expected}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: bot.Path, Env: incident.Environment}, nil
	}))
	if err := restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetCase(ctx, replacement.ID)
	if err != nil || recovered.Status != CaseValidating || recovered.CurrentAttemptID == reopenedReplacement.CurrentAttemptID || restartedRunner.startCount() != 1 {
		t.Fatalf("recovered=%+v starts=%d err=%v", recovered, restartedRunner.startCount(), err)
	}
	recoveredAttempt, err := store.GetAttempt(ctx, recovered.CurrentAttemptID)
	if err != nil || recoveredAttempt.Status != AttemptStatusRunning || recoveredAttempt.Phase != PhaseValidation {
		t.Fatalf("recovered attempt=%+v err=%v", recoveredAttempt, err)
	}

	startsBeforeReplay := restartedRunner.startCount()
	replayed, err := restarted.ResetCase(ctx, reset)
	if err != nil || replayed.ID != replacement.ID || restartedRunner.startCount() != startsBeforeReplay {
		t.Fatalf("replay=%+v starts before=%d after=%d err=%v", replayed, startsBeforeReplay, restartedRunner.startCount(), err)
	}
	replacementArtifact := EvidenceArtifact{ID: "e2e-840-replacement-evidence", CaseID: recovered.ID, AttemptID: recoveredAttempt.ID, Kind: "api", PathOrReference: "/artifacts/e2e/840-retry-response", SHA256: strings.Repeat("b", 64), CapturedAt: time.Now().UTC().Add(time.Second), Environment: "test", Version: "after-reset", RequestID: "request-e2e-840-retry", RedactionStatus: RedactionStatusNotRequired}
	if _, _, err := store.recordEvidenceArtifact(ctx, replacementArtifact, nil); err != nil {
		t.Fatal(err)
	}
	progressed, err := restarted.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: recovered.ID, AttemptID: recoveredAttempt.ID, ExpectedVersion: recovered.Version, IdempotencyKey: "e2e:840:replacement-validation", ActorID: "validator", Outcome: PhaseOutcomeReproduced, OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"checkout succeeds","evidence":[{"kind":"api","path":"response.json","environment":"test","redaction_status":"not_required"}],"gaps":[]}`)})
	if err != nil || progressed.Status != CaseInvestigating || progressed.ID != replacement.ID {
		t.Fatalf("progressed=%+v err=%v", progressed, err)
	}
	archivedAfterProgress, err := store.GetCase(ctx, archived.ID)
	if err != nil || !reflect.DeepEqual(immutable, archivedAfterProgress) {
		t.Fatalf("replacement changed archive: before=%+v after=%+v err=%v", immutable, archivedAfterProgress, err)
	}
}

func TestWorkflowE2EFailureAndRecoveryBoundaries(t *testing.T) {
	t.Run("target change invalidates approval without overwriting environment", func(t *testing.T) {
		fixture := newGitFixture(t)
		fixCommit := fixture.makeFix(t, "fix\n")
		service := fixture.service(t)
		request := fixture.request(fixCommit)
		inspection, err := service.Inspect(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		request.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
		runGitTest(t, fixture.repo, "switch", "test")
		if err := os.WriteFile(filepath.Join(fixture.repo, "advanced.txt"), []byte("advanced\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, fixture.repo, "add", "advanced.txt")
		runGitTest(t, fixture.repo, "commit", "-m", "advance environment")
		runGitTest(t, fixture.repo, "push", "origin", "test")
		advanced := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "HEAD"))
		if _, err := service.MergeAndPush(context.Background(), request); !errors.Is(err, ErrMergeApprovalStale) {
			t.Fatalf("err=%v", err)
		}
		remote := strings.Fields(runGitTest(t, fixture.repo, "ls-remote", "origin", "refs/heads/test"))[0]
		if remote != advanced {
			t.Fatalf("remote=%s advanced=%s", remote, advanced)
		}
	})

	t.Run("conflict leaves environment branch unchanged", func(t *testing.T) {
		fixture := newGitFixture(t)
		runGitTest(t, fixture.repo, "switch", "-c", "fix/bug")
		if err := os.WriteFile(filepath.Join(fixture.repo, "app.txt"), []byte("fix\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, fixture.repo, "commit", "-am", "fix")
		fixCommit := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "HEAD"))
		runGitTest(t, fixture.repo, "push", "-u", "origin", "fix/bug")
		runGitTest(t, fixture.repo, "switch", "test")
		if err := os.WriteFile(filepath.Join(fixture.repo, "app.txt"), []byte("target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, fixture.repo, "commit", "-am", "target")
		runGitTest(t, fixture.repo, "push", "origin", "test")
		before := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "HEAD"))
		inspection, err := fixture.service(t).Inspect(context.Background(), fixture.request(fixCommit))
		if err != nil || !inspection.Conflict {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
		after := strings.Fields(runGitTest(t, fixture.repo, "ls-remote", "origin", "refs/heads/test"))[0]
		if after != before {
			t.Fatalf("environment changed: before=%s after=%s", before, after)
		}
	})

	t.Run("ssh push failure preserves local merge commit", func(t *testing.T) {
		fixture := newGitFixture(t)
		fixCommit := fixture.makeFix(t, "fix\n")
		service := fixture.service(t)
		request := fixture.request(fixCommit)
		inspection, err := service.Inspect(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		request.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
		rejectAllPushes(t, fixture.remote)
		result, err := service.MergeAndPush(context.Background(), request)
		if err == nil || result.Repositories["api"].MergeCommit == "" || result.Repositories["api"].Pushed {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		remote := strings.Fields(runGitTest(t, fixture.repo, "ls-remote", "origin", "refs/heads/test"))[0]
		if remote != inspection.Repositories["api"].TargetHead {
			t.Fatalf("remote=%s approved=%s", remote, inspection.Repositories["api"].TargetHead)
		}
	})

	t.Run("missing evidence pauses before investigation", func(t *testing.T) {
		store := newOrchestratorStore(t)
		runner := &recordingPhaseRunner{}
		o := NewCaseOrchestrator(store, runner, nil, nil)
		incident := createWorkflowCase(t, store, "e2e-missing", CasePendingValidation)
		incident, _ = o.StartCase(context.Background(), StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "missing:start", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{}`)})
		attempt, _ := store.GetAttempt(context.Background(), incident.CurrentAttemptID)
		incident, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "missing:result", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["trace"]}`)})
		if err != nil || incident.Status != CaseWaitingEvidence || runner.startCount() != 1 {
			t.Fatalf("case=%+v starts=%d err=%v", incident, runner.startCount(), err)
		}
	})

	t.Run("stale fix authorization is rejected", func(t *testing.T) {
		_, incident, root, runner, o := prepareFixApprovalCase(t, validRootCauseOutput())
		cmd := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version + 1, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{}`)}
		cmd.IdempotencyKey = StartFixApprovalKey(cmd.CaseID, cmd.RootCauseAttemptID, cmd.ExpectedVersion)
		if _, err := o.ApproveFix(context.Background(), cmd); !errors.Is(err, ErrCaseVersionConflict) || runner.startCount() != 0 {
			t.Fatalf("starts=%d err=%v", runner.startCount(), err)
		}
	})

	t.Run("mismatch and partial multi repo deployment stay unverified", func(t *testing.T) {
		request := DeploymentVerificationRequest{CaseID: "case", Environment: "test", Source: "manual", ExpectedCommits: map[string]string{"api": "a", "web": "b"}, ObservedVersion: "old", ObservedCommits: map[string]string{"api": "a"}}
		got, err := (ManualVersionVerifier{Environment: "test"}).Verify(context.Background(), request)
		if err != nil || got.Result != DeploymentResultMismatched || got.VerifiedAt != nil {
			t.Fatalf("observation=%+v err=%v", got, err)
		}
	})

	t.Run("still reproduces increments cycle and investigates", func(t *testing.T) {
		store, incident, _, _ := prepareRegressionCase(t, 1)
		runner := &recordingPhaseRunner{}
		o := NewCaseOrchestrator(store, runner, nil, nil)
		o.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, c IncidentCase, a PhaseAttempt) (Bug, BotRef, error) {
			return Bug{ID: c.BugID}, BotRef{Key: a.BotKey, Target: a.AgentTarget, Path: t.TempDir()}, nil
		}))
		attempt, err := o.StartRegression(context.Background(), incident.ID, incident.Version)
		if err != nil {
			t.Fatal(err)
		}
		recordRegressionArtifact(t, store, attempt, "request-e2e-still", time.Now().UTC().Add(time.Second))
		current, _ := store.GetCase(context.Background(), incident.ID)
		next, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: current.ID, AttemptID: attempt.ID, ExpectedVersion: current.Version, IdempotencyKey: "e2e:still", ActorID: "validator", Outcome: PhaseOutcomeStillReproduces, OutputJSON: regressionOutput(t, attempt, "still_reproduces", "timeout remains")})
		if err != nil || next.Status != CaseInvestigating || next.CycleNumber != 2 {
			t.Fatalf("case=%+v err=%v", next, err)
		}
		if runner.startCount() != 2 {
			t.Fatalf("runner starts=%d, want regression plus next-cycle investigation", runner.startCount())
		}
		nextAttempt, getErr := store.GetAttempt(context.Background(), next.CurrentAttemptID)
		if getErr != nil || nextAttempt.Phase != PhaseInvestigation || nextAttempt.CycleNumber != 2 || nextAttempt.ParentAttemptID != attempt.ID {
			t.Fatalf("next attempt=%+v err=%v", nextAttempt, getErr)
		}
	})

	t.Run("legacy import stays archived until explicit restart", func(t *testing.T) {
		store := newOrchestratorStore(t)
		now := time.Now().UTC()
		runsPath := filepath.Join(t.TempDir(), "runs.json")
		encoded, _ := json.Marshal([]InvestigationRun{{ID: "old-run", BugID: "old-bug", Status: InvestigationSucceeded, StartedAt: now, FinalMessage: "fixed"}})
		if err := os.WriteFile(runsPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportLegacyRuns(context.Background(), store, runsPath); err != nil {
			t.Fatal(err)
		}
		cases, _ := store.ListCases(context.Background())
		if len(cases) != 1 || cases[0].Status != CaseLegacyArchived {
			t.Fatalf("cases=%+v", cases)
		}
		runner := &recordingPhaseRunner{}
		o := NewCaseOrchestrator(store, runner, nil, nil)
		continued, err := o.CreateAndStartCase(context.Background(), CreateAndStartCaseCommand{CaseID: cases[0].ID, ExpectedVersion: cases[0].Version, IdempotencyKey: "legacy:restart", ActorID: "alice", Bug: Bug{ID: cases[0].BugID, Env: "test"}, Bot: BotRef{Key: "validator", Target: "codex", Path: t.TempDir()}, InputJSON: []byte(`{}`)})
		archived, _ := store.GetCase(context.Background(), cases[0].ID)
		if err != nil || archived.Status != CaseLegacyArchived || continued.ID == archived.ID || continued.Status != CaseValidating {
			t.Fatalf("archived=%+v continued=%+v err=%v", archived, continued, err)
		}
	})
}
