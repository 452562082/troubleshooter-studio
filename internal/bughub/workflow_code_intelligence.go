package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const codeIntelligenceManifestName = "code-intelligence-manifest.json"

// CodeIntelligenceManifest is prepared by the Studio host before an
// investigation. The Agent consumes it instead of trying to execute the
// CodeGraph CLI from its restricted filesystem sandbox.
type CodeIntelligenceManifest struct {
	Version      int                          `json:"version"`
	Enabled      bool                         `json:"enabled"`
	Provider     string                       `json:"provider,omitempty"`
	PreparedAt   time.Time                    `json:"prepared_at"`
	Ready        int                          `json:"ready"`
	Total        int                          `json:"total"`
	Repositories []CodeIntelligenceRepository `json:"repositories"`
	Limitations  []string                     `json:"limitations,omitempty"`
}

type CodeIntelligenceRepository struct {
	Repo          string `json:"repo"`
	ProjectPath   string `json:"project_path,omitempty"`
	Status        string `json:"status"`
	Detail        string `json:"detail,omitempty"`
	CurrentBranch string `json:"current_branch,omitempty"`
	TargetBranch  string `json:"target_branch,omitempty"`
	Head          string `json:"head,omitempty"`
	FileCount     int    `json:"file_count,omitempty"`
	NodeCount     int    `json:"node_count,omitempty"`
	EdgeCount     int    `json:"edge_count,omitempty"`
}

func (m CodeIntelligenceManifest) HasReadyRepository() bool {
	if !m.Enabled || m.Ready < 1 {
		return false
	}
	for _, repository := range m.Repositories {
		if repository.Status == "ready" && filepath.IsAbs(strings.TrimSpace(repository.ProjectPath)) {
			return true
		}
	}
	return false
}

type CodeIntelligenceResolver interface {
	ResolveCodeIntelligence(context.Context, IncidentCase) (CodeIntelligenceManifest, error)
}

type CodeIntelligenceResolverFunc func(context.Context, IncidentCase) (CodeIntelligenceManifest, error)

func (f CodeIntelligenceResolverFunc) ResolveCodeIntelligence(ctx context.Context, incident IncidentCase) (CodeIntelligenceManifest, error) {
	return f(ctx, incident)
}

func (r *AgentPhaseRunner) SetCodeIntelligenceResolver(resolver CodeIntelligenceResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codeIntelligenceResolver = resolver
}

func (r *AgentPhaseRunner) materializeCodeIntelligence(ctx context.Context, attempt PhaseAttempt, incident IncidentCase, staging attemptEvidenceStaging, resolver CodeIntelligenceResolver) (CodeIntelligenceManifest, string, error) {
	if attempt.Phase != PhaseInvestigation {
		return CodeIntelligenceManifest{}, "", nil
	}
	manifest := CodeIntelligenceManifest{Version: 1, PreparedAt: time.Now().UTC()}
	if resolver == nil {
		manifest.Limitations = []string{"Studio has no CodeGraph host resolver for this run"}
	} else {
		resolved, err := resolver.ResolveCodeIntelligence(ctx, incident)
		if err != nil {
			manifest.Limitations = []string{"Studio could not prepare CodeGraph indexes: " + boundedCodeIntelligenceValue(err.Error(), 500)}
		} else {
			manifest = resolved
		}
	}
	normalizeCodeIntelligenceManifest(&manifest)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return CodeIntelligenceManifest{}, "", fmt.Errorf("encode code intelligence manifest: %w", err)
	}
	if containsSensitiveData(encoded) {
		return CodeIntelligenceManifest{}, "", fmt.Errorf("code intelligence manifest contains sensitive data")
	}
	if err := writeImmutableInvestigationInput(filepath.Join(staging.Path(), codeIntelligenceManifestName), append(encoded, '\n')); err != nil {
		return CodeIntelligenceManifest{}, "", err
	}
	prompt := "\n## Studio-prepared code intelligence (mandatory)\n\nRead `STUDIO_EVIDENCE_STAGING_DIR/" + codeIntelligenceManifestName + "` before source investigation. Studio has already prepared and synchronized every repository whose status is `ready`; do not repeat host setup and **不得执行 CodeGraph CLI**. For each implicated ready repository, call the MCP tool `codegraph_explore` with its exact `project_path` and `maxFiles=4` before concluding a code root cause. A current branch or HEAD that differs from the target environment is only a static candidate: verify every decisive line against the deployed revision with read-only `git show`/`git grep` in the approved source repository. For non-ready repositories, use the recorded `detail` as the fallback reason and continue with `rg`/Read. Never claim CodeGraph was used unless a real `codegraph_explore` MCP call completed.\n"
	return manifest, prompt, nil
}

func normalizeCodeIntelligenceManifest(manifest *CodeIntelligenceManifest) {
	manifest.Version = 1
	if manifest.PreparedAt.IsZero() {
		manifest.PreparedAt = time.Now().UTC()
	} else {
		manifest.PreparedAt = manifest.PreparedAt.UTC()
	}
	manifest.Provider = boundedCodeIntelligenceValue(manifest.Provider, 80)
	if len(manifest.Repositories) > 32 {
		manifest.Repositories = manifest.Repositories[:32]
	}
	ready := 0
	for index := range manifest.Repositories {
		repository := &manifest.Repositories[index]
		repository.Repo = boundedCodeIntelligenceValue(repository.Repo, 160)
		repository.ProjectPath = strings.TrimSpace(repository.ProjectPath)
		if repository.ProjectPath != "" {
			repository.ProjectPath = filepath.Clean(repository.ProjectPath)
		}
		repository.Status = boundedCodeIntelligenceValue(repository.Status, 40)
		repository.Detail = boundedCodeIntelligenceValue(repository.Detail, 500)
		repository.CurrentBranch = boundedCodeIntelligenceValue(repository.CurrentBranch, 240)
		repository.TargetBranch = boundedCodeIntelligenceValue(repository.TargetBranch, 240)
		repository.Head = boundedCodeIntelligenceValue(repository.Head, 240)
		if repository.Status == "ready" && filepath.IsAbs(repository.ProjectPath) {
			ready++
		}
	}
	manifest.Ready = ready
	manifest.Total = len(manifest.Repositories)
	if len(manifest.Limitations) > 16 {
		manifest.Limitations = manifest.Limitations[:16]
	}
	for index := range manifest.Limitations {
		manifest.Limitations[index] = boundedCodeIntelligenceValue(manifest.Limitations[index], 500)
	}
}

func boundedCodeIntelligenceValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func eventProvesCodeGraphQuery(event InvestigationEvent) bool {
	if event.Type != "mcp_tool_call" {
		return false
	}
	if state, _ := event.Meta["state"].(string); state != "" && state != "completed" {
		return false
	}
	encoded, _ := json.Marshal(event.Raw)
	text := strings.ToLower(event.Message + " " + string(encoded))
	return strings.Contains(text, "codegraph_explore")
}

func investigationResultRequiresCodeGraph(finalYAML string) bool {
	result, err := ParseInvestigationResult([]byte(finalYAML))
	return err == nil && result.RootCauseType == RootCauseCode
}
