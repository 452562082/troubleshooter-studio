package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const repositoryAccessManifestName = "repository-access-manifest.json"

// RepositoryAccessResolver returns the machine-local repository paths saved by
// Studio for a system. These paths deliberately do not live in the shareable
// troubleshooter.yaml.
type RepositoryAccessResolver interface {
	ResolveRepositoryAccess(context.Context, IncidentCase) (map[string]string, error)
}

type RepositoryAccessResolverFunc func(context.Context, IncidentCase) (map[string]string, error)

func (f RepositoryAccessResolverFunc) ResolveRepositoryAccess(ctx context.Context, incident IncidentCase) (map[string]string, error) {
	return f(ctx, incident)
}

type repositoryAccessManifest struct {
	Version     int                    `json:"version"`
	Phase       Phase                  `json:"phase"`
	Roots       []repositoryAccessRoot `json:"roots"`
	Limitations []string               `json:"limitations,omitempty"`
}

type repositoryAccessRoot struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Access string `json:"access"`
}

func (r *AgentPhaseRunner) SetRepositoryAccessResolver(resolver RepositoryAccessResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repositoryAccessResolver = resolver
}

// materializeRepositoryAccess creates a host-owned allowlist consumed both by
// the Agent prompt and by the Codex filesystem permission profile. Missing
// paths are reported as a gap; they are never replaced by a home-directory
// search, which would trigger macOS App Data privacy prompts.
func (r *AgentPhaseRunner) materializeRepositoryAccess(ctx context.Context, attempt PhaseAttempt, incident IncidentCase, staging attemptEvidenceStaging, resolver RepositoryAccessResolver, fixWorkspace *FixWorkspaceLease) (string, error) {
	// Validation and regression own a stricter browser staging protocol: the
	// durable browser route must be the first published entry. Those phases do
	// not inspect source code, so do not create an empty repository manifest
	// before the route journal. Other non-source phases likewise need no
	// repository permission boundary.
	if attempt.Phase != PhaseInvestigation && attempt.Phase != PhaseFix {
		return "", nil
	}
	manifest := repositoryAccessManifest{Version: 1, Phase: attempt.Phase}
	switch attempt.Phase {
	case PhaseInvestigation:
		if resolver == nil {
			manifest.Limitations = append(manifest.Limitations, "Studio has no repository path resolver for this run")
			break
		}
		paths, err := resolver.ResolveRepositoryAccess(ctx, incident)
		if err != nil {
			manifest.Limitations = append(manifest.Limitations, "Studio could not load configured repository paths")
			break
		}
		repos := make([]string, 0, len(paths))
		for repo := range paths {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		for _, repo := range repos {
			path := filepath.Clean(strings.TrimSpace(paths[repo]))
			if path == "." || !filepath.IsAbs(path) {
				manifest.Limitations = append(manifest.Limitations, fmt.Sprintf("repository %s has no valid absolute local path", repo))
				continue
			}
			info, statErr := os.Stat(path)
			if statErr != nil || !info.IsDir() {
				manifest.Limitations = append(manifest.Limitations, fmt.Sprintf("repository %s local path is unavailable", repo))
				continue
			}
			manifest.Roots = append(manifest.Roots, repositoryAccessRoot{Repo: repo, Path: path, Access: "read"})
		}
		if len(manifest.Roots) == 0 && len(manifest.Limitations) == 0 {
			manifest.Limitations = append(manifest.Limitations, "No local repository paths are configured for this system")
		}
	case PhaseFix:
		for _, binding := range fixWorkspace.filesystemRoots() {
			manifest.Roots = append(manifest.Roots, repositoryAccessRoot{Repo: binding.Repo, Path: binding.Path, Access: "write"})
		}
		if len(manifest.Roots) == 0 {
			manifest.Limitations = append(manifest.Limitations, "No Studio-locked fix worktree is available")
		}
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode repository access manifest: %w", err)
	}
	if err := writeImmutableInvestigationInput(filepath.Join(staging.Path(), repositoryAccessManifestName), append(encoded, '\n')); err != nil {
		return "", err
	}
	return "\n## Studio repository access boundary (mandatory)\n\nRead `STUDIO_EVIDENCE_STAGING_DIR/" + repositoryAccessManifestName + "` before accessing source code. Only paths in `roots` are approved repository roots, and `access` is the maximum allowed operation. Never enumerate or search `/`, `/Users`, the user home directory, or their ancestors to discover repositories. Never inspect `Library`, Desktop, Documents, Downloads, Pictures, Photos, Movies, Music, or another App's data. If a required repository is absent, report the exact configuration gap; do not run `find`, `fd`, `locate`, globbing, or recursive `ls` outside the approved roots.\n", nil
}
