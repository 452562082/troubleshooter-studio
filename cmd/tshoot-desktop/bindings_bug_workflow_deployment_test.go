package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type productionFakeK8sReader struct{ calls int }

func (r *productionFakeK8sReader) ReadDeployment(context.Context, string, string, string) (bughub.K8sDeploymentVersion, error) {
	r.calls++
	return bughub.K8sDeploymentVersion{Annotations: map[string]string{"commit": "old"}}, nil
}

func newProductionDeploymentApp(t *testing.T, yamlText, caseEnvironment string, factory func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error)) (*App, bughub.IncidentCase) {
	t.Helper()
	root := t.TempDir()
	botRoot := t.TempDir()
	meta, _ := json.Marshal(discover.Meta{SystemID: "base", Target: "codex", TroubleshooterYAML: yamlText})
	if err := os.WriteFile(filepath.Join(botRoot, discover.MetaFilename), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{
		workflowRoot: root,
		workflowLoadBug: func(id string) (bughub.Bug, error) {
			return bughub.Bug{ID: id, SystemID: "base", Env: caseEnvironment}, nil
		},
		workflowLoadBot: func(key string) (bughub.BotRef, error) {
			return bughub.BotRef{Key: key, SystemID: "base", Target: "codex", Path: botRoot, Env: caseEnvironment}, nil
		},
		workflowK8sReaderFactory: factory,
	}
	if err := app.initializeIncidentWorkflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.closeIncidentWorkflow() })
	now := time.Now().UTC()
	incident := bughub.IncidentCase{ID: "case-" + caseEnvironment, BugID: "bug", Source: "test", SystemID: "base", Environment: caseEnvironment, Status: bughub.CaseWaitingDeployment, CycleNumber: 1, CurrentAttemptID: "fix", SelectedBotKey: "base|codex", Version: 1}
	if err := app.workflowStore.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if err := app.workflowStore.CreateAttempt(context.Background(), bughub.PhaseAttempt{ID: "fix", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseFix, Status: bughub.AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	change := bughub.CodeChange{ID: "change", CaseID: incident.ID, AttemptID: "fix", Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "fix-1", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: caseEnvironment, MergeCommit: "merge-1", PushStatus: "pushed"}
	if err := app.workflowStore.RecordCodeChange(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	scope, _ := json.Marshal(bughub.MergeApprovalScope{CycleNumber: 1, FixAttemptID: "fix", CodeChanges: []bughub.ApprovedCodeChange{{ID: "change", Repo: "repo", FixCommit: "fix-1", TargetBranch: caseEnvironment}}})
	if err := app.workflowStore.RecordApproval(context.Background(), bughub.Approval{ID: "approval", CaseID: incident.ID, Kind: bughub.ApprovalMergeEnvironmentBranch, Actor: "alice", CaseVersion: 1, ScopeJSON: scope, FixCommits: map[string]string{"repo": "fix-1"}, TargetBranches: map[string]string{"repo": caseEnvironment}}, "approval"); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.workflowStore.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	return app, loaded
}

func deploymentYAML(block string) string {
	return `system: {id: base, name: Base}
agent: {name: bot}
environments:
  - id: test
` + block + `
repos:
  - {name: repo, url: git@example.com:repo.git, stack: go, env_branches: {test: test}}
generation: {targets: [codex]}
meta: {schema_version: "0.1"}
`
}

func notifyProductionDeployment(t *testing.T, app *App, incident bughub.IncidentCase, forgedSource string) ([]bughub.DeploymentObservation, error) {
	t.Helper()
	_, err := app.NotifyIncidentDeployed(NotifyIncidentDeployedInput{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "notify-" + incident.ID, ActorID: "alice", ObservedVersion: "caller", ObservedCommits: map[string]string{"repo": "caller"}, VersionSource: forgedSource, InputJSON: map[string]any{}})
	observations, listErr := app.workflowStore.ListDeploymentObservations(context.Background(), incident.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	return observations, err
}

func assertReservedProvider(t *testing.T, app *App, incident bughub.IncidentCase, want string) {
	t.Helper()
	event, found, err := app.workflowStore.GetEventByIdempotencyKey(context.Background(), "deployment-reserve:"+incident.ID+":v1")
	if err != nil || !found {
		t.Fatalf("reservation found=%v err=%v", found, err)
	}
	var reservation bughub.DeploymentReservation
	if err := json.Unmarshal(event.PayloadJSON, &reservation); err != nil || reservation.VerifierInput.Source != want {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
}

func TestProductionWorkflowSelectsDeploymentProviderFromInstalledCaseConfig(t *testing.T) {
	t.Run("http ignores forged k8s source", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte(`{"commit":"old"}`)) }))
		defer server.Close()
		app, incident := newProductionDeploymentApp(t, deploymentYAML("    deployment_verification:\n      provider: http\n      http: {url: \""+server.URL+"\", json_pointer: /commit}"), "test", nil)
		observations, err := notifyProductionDeployment(t, app, incident, "k8s")
		if err != nil || calls != 1 || len(observations) != 1 || observations[0].VerificationSource != "http" {
			t.Fatalf("calls=%d observations=%+v err=%v", calls, observations, err)
		}
		assertReservedProvider(t, app, incident, "http")
	})

	t.Run("k8s ignores forged manual source", func(t *testing.T) {
		reader := &productionFakeK8sReader{}
		factoryCalls := 0
		factory := func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
			factoryCalls++
			return reader, nil
		}
		app, incident := newProductionDeploymentApp(t, deploymentYAML("    deployment_verification:\n      provider: k8s\n      k8s: {cluster: c, namespace: n, deployments_by_repo: {repo: deploy}, commit_annotation: commit}"), "test", factory)
		observations, err := notifyProductionDeployment(t, app, incident, "manual")
		if err != nil || factoryCalls != 1 || reader.calls != 1 || len(observations) != 1 || observations[0].VerificationSource != "k8s" {
			t.Fatalf("factory=%d reader=%d observations=%+v err=%v", factoryCalls, reader.calls, observations, err)
		}
		assertReservedProvider(t, app, incident, "k8s")
	})

	t.Run("legacy config remains manual", func(t *testing.T) {
		app, incident := newProductionDeploymentApp(t, deploymentYAML(""), "test", nil)
		observations, err := notifyProductionDeployment(t, app, incident, "http")
		if err != nil || len(observations) != 1 || observations[0].VerificationSource != "manual" {
			t.Fatalf("observations=%+v err=%v", observations, err)
		}
		assertReservedProvider(t, app, incident, "manual")
	})

	t.Run("unknown environment invokes no provider", func(t *testing.T) {
		factoryCalls := 0
		factory := func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
			factoryCalls++
			return &productionFakeK8sReader{}, nil
		}
		app, incident := newProductionDeploymentApp(t, deploymentYAML("    deployment_verification:\n      provider: http\n      http: {url: \"https://must-not-run.invalid\", json_pointer: /commit}"), "ghost", factory)
		observations, err := notifyProductionDeployment(t, app, incident, "http")
		if err == nil || factoryCalls != 0 || len(observations) != 1 || observations[0].DiagnosticCode != "environment_unknown" {
			t.Fatalf("factory=%d observations=%+v err=%v", factoryCalls, observations, err)
		}
	})

	t.Run("config change between reservation and execution fails closed", func(t *testing.T) {
		serverCalls, factoryCalls, loads := 0, 0, 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { serverCalls++ }))
		defer server.Close()
		httpCfg, _ := config.LoadFromBytes([]byte(deploymentYAML("    deployment_verification:\n      provider: http\n      http: {url: \"" + server.URL + "\", json_pointer: /commit}")))
		k8sCfg, _ := config.LoadFromBytes([]byte(deploymentYAML("    deployment_verification:\n      provider: k8s\n      k8s: {cluster: c, namespace: n, deployments_by_repo: {repo: deploy}, commit_annotation: commit}")))
		factory := func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
			factoryCalls++
			return &productionFakeK8sReader{}, nil
		}
		app, incident := newProductionDeploymentApp(t, deploymentYAML(""), "test", factory)
		app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
			loads++
			if loads == 1 {
				return httpCfg, nil
			}
			return k8sCfg, nil
		}
		observations, err := notifyProductionDeployment(t, app, incident, "manual")
		if err == nil || serverCalls != 0 || factoryCalls != 0 || len(observations) != 1 || observations[0].DiagnosticCode != "config_changed" {
			t.Fatalf("loads=%d http=%d factory=%d observations=%+v err=%v", loads, serverCalls, factoryCalls, observations, err)
		}
		assertReservedProvider(t, app, incident, "http")
	})
}
