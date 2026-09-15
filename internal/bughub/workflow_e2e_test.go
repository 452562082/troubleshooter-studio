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

func TestWorkflowE2ESubmissionSurvivesSQLiteReopen(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
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
			orchestrator := NewCaseOrchestrator(store, runner, git)
			orchestrator.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
				return Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: t.TempDir(), Env: incident.Environment}, nil
			}))
			bug := Bug{ID: "bug-e2e", Source: "zentao", SystemID: "shop", Env: "test", Expected: "checkout succeeds"}
			validator := BotRef{Key: "validator", Target: target, Path: t.TempDir(), Env: "test"}

			incident, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-e2e", IdempotencyKey: "e2e:create", ActorID: "alice", Bug: bug, Bot: validator, InputJSON: []byte(`{"reproduction_steps":["submit checkout"],"expected_behavior":"checkout succeeds"}`)})
			if err != nil || incident.Status != CaseInvestigating {
				t.Fatalf("start=%+v err=%v", incident, err)
			}
			investigation, _ := store.GetAttempt(ctx, incident.CurrentAttemptID)
			rootOutput := []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"checkout race","confidence":"high","evidence":[],"gaps":[]}`)
			incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: incident.ID, AttemptID: investigation.ID, ExpectedVersion: incident.Version, IdempotencyKey: "e2e:investigation", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: rootOutput})
			if err != nil || incident.Status != CaseWaitingFixApproval {
				t.Fatalf("investigation=%+v err=%v", incident, err)
			}

			fixKey := StartFixApprovalKey(incident.ID, investigation.ID, incident.Version)
			incident, err = orchestrator.ApproveFix(ctx, ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: fixKey, ActorID: "alice", RootCauseAttemptID: investigation.ID, Bug: bug, Bot: BotRef{Key: "fixer", Target: target, Path: t.TempDir(), Env: "test"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)})
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
			if err != nil || incident.Status != CaseSubmitted || git.pushCount() != 1 {
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
			if err != nil || reopened.Status != CaseSubmitted {
				t.Fatalf("reopened=%+v err=%v", reopened, err)
			}
			runnerAfterRestart := &recordingPhaseRunner{}
			orchestrator = NewCaseOrchestrator(store, runnerAfterRestart, git)
			approvals, _ := store.ListApprovals(ctx, incident.ID)
			observations, _ := store.ListDeploymentObservations(ctx, incident.ID)
			events, _ := store.ListEvents(ctx, incident.ID)
			if len(approvals) != 2 || approvals[0].Kind == approvals[1].Kind || len(observations) != 0 || runnerAfterRestart.startCount() != 0 {
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
			wantPath := []CaseStatus{CaseInvestigating, CaseRootCauseReady, CaseWaitingFixApproval, CaseFixing, CaseFixPushed, CaseWaitingMergeApproval, CaseMerging, CaseSubmitted}
			next := 0
			for _, status := range visited {
				if next < len(wantPath) && status == wantPath[next] {
					next++
				}
			}
			if next != len(wantPath) {
				t.Fatalf("success path stopped at %d/%d: visited=%v", next, len(wantPath), visited)
			}
		})
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
	orchestrator := NewCaseOrchestrator(store, runner, nil)
	bug := Bug{ID: "840", Source: "zentao", SystemID: "shop", Env: "test", Expected: "checkout succeeds"}
	bot := BotRef{Key: "validator", Target: "codex", Path: t.TempDir(), Env: "test"}
	first, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-840-candidate-a", IdempotencyKey: "e2e:840:start:a", ActorID: "alice", Bug: bug, Bot: bot, InputJSON: []byte(`{"reproduction_steps":["submit checkout"]}`)})
	if err != nil || first.Status != CaseInvestigating {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	reused, err := orchestrator.CreateAndStartCase(ctx, CreateAndStartCaseCommand{CaseID: "case-840-candidate-b", IdempotencyKey: "e2e:840:start:b", ActorID: "alice", Bug: bug, Bot: bot, InputJSON: []byte(`{"reproduction_steps":["retry checkout"]}`)})
	if err != nil || reused.ID != first.ID || reused.Status != CaseInvestigating || runner.startCount() != 1 {
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
	if err != nil || replacement.ID != reset.NewCaseID || replacement.Status != CaseInvestigating || replacement.ResetFromCaseID != first.ID || runner.startCount() != 2 {
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
	if err != nil || reopenedReplacement.Status != CaseInvestigating || reopenedReplacement.ResetFromCaseID != archived.ID || reopenedReplacement.CurrentAttemptID == "" || reopenedReplacement.ClosedAt != nil {
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
	if _, err := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil).CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: archived.ID, AttemptID: validation.ID, ExpectedVersion: archived.Version, IdempotencyKey: "e2e:840:late-completion", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["trace"]}`)}); err == nil {
		t.Fatal("late completion mutated reset archive")
	}
	archivedAfterLateCompletion, err := store.GetCase(ctx, archived.ID)
	if err != nil || !reflect.DeepEqual(immutable, archivedAfterLateCompletion) {
		t.Fatalf("archive changed after late completion: before=%+v after=%+v err=%v", immutable, archivedAfterLateCompletion, err)
	}

	restartedRunner := &recordingPhaseRunner{}
	restarted := NewCaseOrchestrator(store, restartedRunner, nil)
	restarted.SetRecoveryContextResolver(RecoveryContextResolverFunc(func(_ context.Context, incident IncidentCase, attempt PhaseAttempt) (Bug, BotRef, error) {
		return Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment, Expected: bug.Expected}, BotRef{Key: attempt.BotKey, Target: attempt.AgentTarget, Path: bot.Path, Env: incident.Environment}, nil
	}))
	if err := restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetCase(ctx, replacement.ID)
	if err != nil || recovered.Status != CaseInvestigating || recovered.CurrentAttemptID == reopenedReplacement.CurrentAttemptID || restartedRunner.startCount() != 1 {
		t.Fatalf("recovered=%+v starts=%d err=%v", recovered, restartedRunner.startCount(), err)
	}
	recoveredAttempt, err := store.GetAttempt(ctx, recovered.CurrentAttemptID)
	if err != nil || recoveredAttempt.Status != AttemptStatusRunning || recoveredAttempt.Phase != PhaseInvestigation {
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
	progressed, err := restarted.CompleteAttempt(ctx, CompleteAttemptCommand{CaseID: recovered.ID, AttemptID: recoveredAttempt.ID, ExpectedVersion: recovered.Version, IdempotencyKey: "e2e:840:replacement-validation", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"investigation_status":"insufficient_info","environment":"test","evidence":[],"gaps":["request id"]}`)})
	if err != nil || progressed.Status != CaseWaitingEvidence || progressed.ID != replacement.ID {
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
		o := NewCaseOrchestrator(store, runner, nil)
		incident := createWorkflowCase(t, store, "e2e-missing", CasePendingInvestigation)
		incident, _ = o.StartCase(context.Background(), StartCaseCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "missing:start", ActorID: "alice", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{}`)})
		attempt, _ := store.GetAttempt(context.Background(), incident.CurrentAttemptID)
		incident, err := o.CompleteAttempt(context.Background(), CompleteAttemptCommand{CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version, IdempotencyKey: "missing:result", ActorID: "validator", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"verification_status":"insufficient_info","environment":"test","evidence":[],"gaps":["trace"]}`)})
		if err != nil || incident.Status != CaseWaitingEvidence || runner.startCount() != 1 {
			t.Fatalf("case=%+v starts=%d err=%v", incident, runner.startCount(), err)
		}
	})

	t.Run("stale fix authorization is rejected", func(t *testing.T) {
		_, incident, root, runner, o := prepareFixApprovalCase(t, validRootCauseOutput())
		cmd := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version + 1, ActorID: "alice", RootCauseAttemptID: root.ID, Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "fixer", Target: "codex"}, InputJSON: []byte(`{"source_baselines":{"api":"feature/work"}}`)}
		cmd.IdempotencyKey = StartFixApprovalKey(cmd.CaseID, cmd.RootCauseAttemptID, cmd.ExpectedVersion)
		if _, err := o.ApproveFix(context.Background(), cmd); !errors.Is(err, ErrCaseVersionConflict) || runner.startCount() != 0 {
			t.Fatalf("starts=%d err=%v", runner.startCount(), err)
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
		o := NewCaseOrchestrator(store, runner, nil)
		continued, err := o.CreateAndStartCase(context.Background(), CreateAndStartCaseCommand{CaseID: cases[0].ID, ExpectedVersion: cases[0].Version, IdempotencyKey: "legacy:restart", ActorID: "alice", Bug: Bug{ID: cases[0].BugID, Env: "test"}, Bot: BotRef{Key: "validator", Target: "codex", Path: t.TempDir()}, InputJSON: []byte(`{}`)})
		archived, _ := store.GetCase(context.Background(), cases[0].ID)
		if err != nil || archived.Status != CaseLegacyArchived || continued.ID == archived.ID || continued.Status != CaseInvestigating {
			t.Fatalf("archived=%+v continued=%+v err=%v", archived, continued, err)
		}
	})
}
