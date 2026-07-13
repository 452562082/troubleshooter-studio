package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type deploymentIntentE2EVerifier struct{}

func (deploymentIntentE2EVerifier) Verify(_ context.Context, request bughub.DeploymentVerificationRequest) (bughub.DeploymentObservation, error) {
	now := time.Now().UTC()
	return bughub.DeploymentObservation{Environment: request.Environment, ExpectedCommits: request.ExpectedCommits, ObservedVersion: request.ObservedVersion, ObservedCommits: request.ObservedCommits, VerificationSource: request.Source, Result: bughub.DeploymentResultMatched, ObservedAt: now, VerifiedAt: &now}, nil
}

func newDeploymentIntentE2EApp(t *testing.T, caseID string) (*App, *bughub.CaseStore, *workflowBindingRunner, bughub.IncidentCase) {
	t.Helper()
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "workflow.db"))
	verifier := deploymentIntentE2EVerifier{}
	app.workflowOrchestrator = bughub.NewCaseOrchestrator(store, runner, nil, bughub.NewCompositeDeploymentVerifier(map[string]bughub.DeploymentVerifier{"manual": verifier, "deployment-config": verifier}))
	now := time.Now().UTC().Add(-time.Minute)
	incident := bughub.IncidentCase{ID: caseID, BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test", Status: bughub.CaseWaitingDeployment, CycleNumber: 1, CurrentAttemptID: "fix-" + caseID, SelectedBotKey: "base|codex"}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	validation := bughub.PhaseAttempt{ID: "validation-" + caseID, CaseID: caseID, CycleNumber: 1, Phase: bughub.PhaseValidation, Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{"reproduction_steps":["submit"],"expected_behavior":"ok"}`), OutputJSON: []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"timeout","expected_behavior":"ok","evidence":[],"gaps":[]}`), StartedAt: now, FinishedAt: &now}
	fix := bughub.PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: caseID, CycleNumber: 1, Phase: bughub.PhaseFix, Status: bughub.AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now, FinishedAt: &now}
	for _, attempt := range []bughub.PhaseAttempt{validation, fix} {
		if err := store.CreateAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	sourceRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceRoot, "evidence.json")
	if err := os.WriteFile(source, []byte(`{"status":"timeout"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactsParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bughub.RegisterArtifact(context.Background(), store, bughub.ArtifactInput{ArtifactsRoot: filepath.Join(artifactsParent, "artifacts"), SourcePath: source, CaseID: caseID, AttemptID: validation.ID, Kind: "api", CapturedAt: now.Add(time.Second), Environment: "test", Version: "before", RequestID: "request-original-" + caseID, RedactionStatus: bughub.RedactionStatusNotRequired}); err != nil {
		t.Fatal(err)
	}
	change := bughub.CodeChange{ID: "change-" + caseID, CaseID: caseID, AttemptID: fix.ID, Repo: "api", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: "fix-api", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", MergeCommit: "merge-api", PushStatus: "pushed"}
	if err := store.RecordCodeChange(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	scope, _ := json.Marshal(bughub.MergeApprovalScope{CycleNumber: 1, FixAttemptID: fix.ID, CodeChanges: []bughub.ApprovedCodeChange{{ID: change.ID, Repo: "api", FixCommit: change.FixCommit, TargetBranch: "test"}}})
	if err := store.RecordApproval(context.Background(), bughub.Approval{ID: "approval-" + caseID, CaseID: caseID, Kind: bughub.ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: 1, ScopeJSON: scope, FixCommits: map[string]string{"api": "fix-api"}, TargetBranches: map[string]string{"api": "test"}}, "approval:"+caseID); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCase(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	return app, store, runner, loaded
}

func deploymentIntentInput(incident bughub.IncidentCase, key, text string) NotifyIncidentDeployedInput {
	return NotifyIncidentDeployedInput{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", ObservedVersion: "build-42", ObservedCommits: map[string]string{"api": "merge-api"}, NotificationText: text, InputJSON: map[string]any{}}
}

func TestIncidentDeploymentNotificationE2EButtonAndLanguageShareVerifierGate(t *testing.T) {
	t.Run("button duplicate is exactly once", func(t *testing.T) {
		app, store, runner, incident := newDeploymentIntentE2EApp(t, "case-button")
		input := deploymentIntentInput(incident, "notify:button", "")
		first, err := app.NotifyIncidentDeployed(input)
		if err != nil || first.Status != bughub.CaseRegressionValidating {
			t.Fatalf("first=%+v err=%v", first, err)
		}
		second, err := app.NotifyIncidentDeployed(input)
		if err != nil || second != first || runner.count() != 1 {
			t.Fatalf("second=%+v starts=%d err=%v", second, runner.count(), err)
		}
		observations, _ := store.ListDeploymentObservations(context.Background(), incident.ID)
		attempts, _ := store.ListAttempts(context.Background(), bughub.AttemptFilter{CaseID: incident.ID})
		regressions := 0
		for _, attempt := range attempts {
			if attempt.Phase == bughub.PhaseRegression {
				regressions++
			}
		}
		if len(observations) != 1 || regressions != 1 {
			t.Fatalf("observations=%+v regressions=%d", observations, regressions)
		}
	})

	t.Run("explicit language reaches the same durable reservation", func(t *testing.T) {
		app, store, runner, incident := newDeploymentIntentE2EApp(t, "case-language")
		got, err := app.NotifyIncidentDeployed(deploymentIntentInput(incident, "notify:language", "已经部署到 test"))
		if err != nil || got.Status != bughub.CaseRegressionValidating || runner.count() != 1 {
			t.Fatalf("case=%+v starts=%d err=%v", got, runner.count(), err)
		}
		if _, found, err := store.GetEventByIdempotencyKey(context.Background(), "deployment-reserve:"+incident.ID+":v1"); err != nil || !found {
			t.Fatalf("reservation found=%v err=%v", found, err)
		}
	})

	t.Run("negative language has no verifier side effect", func(t *testing.T) {
		app, store, runner, incident := newDeploymentIntentE2EApp(t, "case-negative")
		_, err := app.NotifyIncidentDeployed(deploymentIntentInput(incident, "notify:negative", "还没部署"))
		if !errors.Is(err, bughub.ErrDeploymentNotificationIntent) || runner.count() != 0 {
			t.Fatalf("starts=%d err=%v", runner.count(), err)
		}
		observations, _ := store.ListDeploymentObservations(context.Background(), incident.ID)
		current, _ := store.GetCase(context.Background(), incident.ID)
		if len(observations) != 0 || current.Status != bughub.CaseWaitingDeployment || current.Version != incident.Version {
			t.Fatalf("case=%+v observations=%+v", current, observations)
		}
	})
}
