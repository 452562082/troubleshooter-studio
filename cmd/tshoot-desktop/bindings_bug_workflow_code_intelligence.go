package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

type caseCodeIntelligenceResolver struct{ app *App }

var ensureIncidentCodeGraph = agent.EnsureCodeGraphInstalled
var prepareIncidentCodeGraph = agent.PrepareCodeGraphIndexes
var invalidateIncidentCodeGraph = agent.InvalidateCodeGraphIndexCache

func (r caseCodeIntelligenceResolver) ResolveCodeIntelligence(ctx context.Context, incident bughub.IncidentCase) (bughub.CodeIntelligenceManifest, error) {
	manifest := bughub.CodeIntelligenceManifest{Version: 1, Provider: "codegraph", PreparedAt: time.Now().UTC()}
	if r.app == nil {
		return manifest, errors.New("Studio workflow is unavailable")
	}
	loader := r.app.workflowLoadDeploymentConfig
	if loader == nil {
		loader = r.app.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || cfg.System.ID != incident.SystemID {
		return manifest, errors.New("Case robot configuration is unavailable")
	}
	if !cfg.CodeIntelligence.UsesCodeGraph() {
		manifest.Provider = ""
		return manifest, nil
	}
	manifest.Enabled = true
	storedPaths := userconfig.GetRepoPathsForSystem(incident.SystemID)
	paths := make(map[string]string, len(storedPaths))
	for repository, path := range storedPaths {
		if path = strings.TrimSpace(path); path != "" {
			paths[repository] = userconfig.ExpandHome(path)
		}
	}
	targets := agent.BuildCodeGraphRepoTargets(cfg, paths)
	manifest.Total = len(targets)
	if len(targets) == 0 {
		manifest.Limitations = []string{"no analyzable repository is configured"}
		return manifest, nil
	}

	binary, err := ensureIncidentCodeGraph(nil)
	if err != nil {
		manifest.Limitations = []string{"CodeGraph binary unavailable: " + err.Error()}
		return manifest, nil
	}
	// A failed deploy-time report must not permanently poison later Cases. The
	// host retries once per investigation while the Agent remains read-only.
	invalidateIncidentCodeGraph(cfg.System.ID)
	prepareCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	report := prepareIncidentCodeGraph(prepareCtx, agent.CodeGraphIndexOptions{
		BinaryPath: binary, SystemID: cfg.System.ID, Repos: targets,
		InitTimeout: 120 * time.Second, SyncTimeout: 30 * time.Second, MaxConcurrency: 2,
	})

	targetsByName := make(map[string]agent.CodeGraphRepoTarget, len(targets))
	for _, target := range targets {
		targetsByName[target.Name] = target
	}
	targetBranches := make(map[string]string, len(cfg.Repos))
	for _, repository := range cfg.Repos {
		targetBranches[repository.Name] = strings.TrimSpace(repository.EnvBranches[incident.Environment])
	}
	for _, result := range report.Repos {
		target := targetsByName[result.Name]
		status := strings.TrimSpace(result.Status)
		detail := strings.TrimSpace(result.Detail)
		if status == "ready" && target.Branch != "" && targetBranches[result.Name] != "" && target.Branch != targetBranches[result.Name] {
			detail = fmt.Sprintf("index is from current branch %s; target environment branch is %s, so graph results are static candidates", target.Branch, targetBranches[result.Name])
		}
		manifest.Repositories = append(manifest.Repositories, bughub.CodeIntelligenceRepository{
			Repo: result.Name, ProjectPath: result.Path, Status: status, Detail: detail,
			CurrentBranch: target.Branch, TargetBranch: targetBranches[result.Name], Head: target.Head,
			FileCount: result.FileCount, NodeCount: result.NodeCount, EdgeCount: result.EdgeCount,
		})
	}
	if len(manifest.Repositories) == 0 {
		manifest.Limitations = []string{"CodeGraph did not return a repository index report"}
	}
	return manifest, nil
}
