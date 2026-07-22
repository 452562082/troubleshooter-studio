package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func TestCaseCodeIntelligenceResolverPreparesIndexesOnStudioHost(t *testing.T) {
	requireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "api")
	initGitRepoWithBranch(t, repo, "feature/local")
	runGit(t, repo, "checkout", "feature/local")
	if err := userconfig.SetRepoPathsForSystem("base", map[string]string{"api": repo}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.SystemConfig{
		System:           config.System{ID: "base"},
		CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: config.CodeIntelligenceProviderCodeGraph},
		Repos:            []config.Repo{{Name: "api", Analysis: config.RepoAnalysis{Enabled: true}, EnvBranches: map[string]string{"test": "base-test"}}},
	}
	app := &App{workflowLoadDeploymentConfig: func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) { return cfg, nil }}

	oldEnsure := ensureIncidentCodeGraph
	oldPrepare := prepareIncidentCodeGraph
	oldInvalidate := invalidateIncidentCodeGraph
	defer func() {
		ensureIncidentCodeGraph = oldEnsure
		prepareIncidentCodeGraph = oldPrepare
		invalidateIncidentCodeGraph = oldInvalidate
	}()
	ensureIncidentCodeGraph = func(func(string)) (string, error) { return "/host/codegraph", nil }
	invalidated := ""
	invalidateIncidentCodeGraph = func(systemID string) { invalidated = systemID }
	prepareIncidentCodeGraph = func(_ context.Context, opts agent.CodeGraphIndexOptions) agent.CodeGraphIndexReport {
		if opts.BinaryPath != "/host/codegraph" || opts.SystemID != "base" || len(opts.Repos) != 1 {
			t.Fatalf("options=%+v", opts)
		}
		return agent.CodeGraphIndexReport{Ready: 1, Total: 1, Repos: []agent.CodeGraphRepoResult{{Name: "api", Path: repo, Status: "ready", FileCount: 12, NodeCount: 34, EdgeCount: 56}}}
	}

	manifest, err := (caseCodeIntelligenceResolver{app: app}).ResolveCodeIntelligence(context.Background(), bughub.IncidentCase{SystemID: "base", Environment: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if invalidated != "base" || !manifest.Enabled || len(manifest.Repositories) != 1 {
		t.Fatalf("invalidated=%q manifest=%+v", invalidated, manifest)
	}
	repository := manifest.Repositories[0]
	if repository.Status != "ready" || repository.ProjectPath != repo || repository.CurrentBranch != "feature/local" || repository.TargetBranch != "base-test" || repository.Head == "" {
		t.Fatalf("repository=%+v", repository)
	}
	if !strings.Contains(repository.Detail, "static candidates") {
		t.Fatalf("branch mismatch detail=%q", repository.Detail)
	}
}
