package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const runtimeCodeManifestName = "runtime-code-manifest.json"

// frontendRuntimeManifestName remains an internal compatibility alias for
// tests and older callers. New investigation prompts only expose the unified
// runtime-code manifest.
const frontendRuntimeManifestName = runtimeCodeManifestName

const (
	FrontendPrecisionDeployedRevision = "deployed_revision"
	FrontendPrecisionImageDigest      = "image_digest"
	FrontendPrecisionImageTag         = "image_tag"
	FrontendPrecisionRepository       = "repository"
	FrontendPrecisionUnavailable      = "unavailable"
)

// FrontendRuntimeManifest is the historical name of Studio's unified runtime
// code manifest. It now covers every runtime repository, not only frontends.
// The type name stays stable for compatibility while the on-disk contract is
// runtime-code-manifest.json.
type FrontendRuntimeManifest struct {
	Environment     string                      `json:"environment"`
	Precision       string                      `json:"precision"`
	SourceMapStatus string                      `json:"source_map_status"`
	Repositories    []FrontendRuntimeRepository `json:"repositories"`
	Limitations     []string                    `json:"limitations,omitempty"`
}

type FrontendRuntimeRepository struct {
	Repo           string   `json:"repo"`
	Role           string   `json:"role"`
	SubPath        string   `json:"sub_path,omitempty"`
	Deployment     string   `json:"deployment,omitempty"`
	Cluster        string   `json:"cluster,omitempty"`
	Namespace      string   `json:"namespace,omitempty"`
	Revision       string   `json:"revision,omitempty"`
	RevisionSource string   `json:"revision_source,omitempty"`
	Images         []string `json:"images,omitempty"`
	Precision      string   `json:"precision"`
	Limitations    []string `json:"limitations,omitempty"`
}

type FrontendRuntimeResolver interface {
	ResolveFrontendRuntime(context.Context, IncidentCase) (FrontendRuntimeManifest, error)
}

type FrontendRuntimeResolverFunc func(context.Context, IncidentCase) (FrontendRuntimeManifest, error)

func (f FrontendRuntimeResolverFunc) ResolveFrontendRuntime(ctx context.Context, incident IncidentCase) (FrontendRuntimeManifest, error) {
	return f(ctx, incident)
}

func (r *AgentPhaseRunner) SetFrontendRuntimeResolver(resolver FrontendRuntimeResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frontendRuntimeResolver = resolver
}

func (r *AgentPhaseRunner) materializeFrontendRuntime(ctx context.Context, attempt PhaseAttempt, incident IncidentCase, staging attemptEvidenceStaging, resolver FrontendRuntimeResolver) (string, error) {
	if attempt.Phase != PhaseInvestigation || resolver == nil {
		return "", nil
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	manifest, err := resolver.ResolveFrontendRuntime(resolveCtx, incident)
	if err != nil {
		manifest = FrontendRuntimeManifest{
			Environment:     incident.Environment,
			Precision:       FrontendPrecisionUnavailable,
			SourceMapStatus: "not_registered",
			Limitations:     []string{"runtime code metadata unavailable"},
		}
	}
	normalizeFrontendRuntimeManifest(&manifest, incident.Environment)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode runtime code manifest: %w", err)
	}
	if containsSensitiveData(encoded) {
		return "", fmt.Errorf("runtime code manifest contains sensitive data")
	}
	path := filepath.Join(staging.Path(), frontendRuntimeManifestName)
	if err := writeImmutableInvestigationInput(path, append(encoded, '\n')); err != nil {
		return "", err
	}
	return "\n## Runtime service-to-code correlation (mandatory)\n\nRead `STUDIO_EVIDENCE_STAGING_DIR/" + runtimeCodeManifestName + "` together with the frozen Network evidence. For every implicated service, bind service → Deployment → deployed revision/image → repository/sub_path before reading code. When `revision` exists, do not checkout or mutate the repository: verify it with `git cat-file -e <revision>^{commit}`, search with `git grep -n -e <anchor> <revision> -- <sub_path>`, and read with `git show <revision>:<path>`. A current-HEAD CodeGraph index may only prove the deployed code when HEAD equals that revision; otherwise it is a candidate and every cited line must come from `git show` at the deployed revision. For browser frames, first inspect already-frozen `source_map_url`/`.map` requests, then same-name `.map` and version-matched local build artifacts; only a source map matching both deployed revision/build and initiator bundle URL gives exact source file/line. If revision is unavailable, degrade through image digest/tag and finally repository/sub_path. Do not rerun browser interaction merely to rediscover these facts, and never present bundle coordinates, image tags, static topology, or current-HEAD text matches as runtime-exact code. State every hop's precision and limitation in `call_chain`. Missing revision, image digest, or source map is a location-precision limitation, not automatically a root-cause evidence gap: when frozen runtime evidence and code behavior already close the causal chain, keep `root_cause_ready` with high confidence and do not copy that optional limitation into `validation_gaps`, `gaps`, or `unchecked_scopes`.\n", nil
}

func normalizeFrontendRuntimeManifest(manifest *FrontendRuntimeManifest, environment string) {
	manifest.Environment = boundedFrontendValue(firstNonEmpty(manifest.Environment, environment), 120)
	manifest.Precision = normalizedFrontendPrecision(manifest.Precision)
	if strings.TrimSpace(manifest.SourceMapStatus) == "" {
		manifest.SourceMapStatus = "not_registered"
	}
	manifest.SourceMapStatus = boundedFrontendValue(manifest.SourceMapStatus, 80)
	manifest.Limitations = normalizedFrontendStrings(manifest.Limitations, 12, 300)
	if len(manifest.Repositories) > 32 {
		manifest.Repositories = manifest.Repositories[:32]
	}
	for index := range manifest.Repositories {
		repo := &manifest.Repositories[index]
		repo.Repo = boundedFrontendValue(repo.Repo, 160)
		repo.Role = boundedFrontendValue(repo.Role, 40)
		repo.SubPath = boundedFrontendValue(repo.SubPath, 300)
		repo.Deployment = boundedFrontendValue(repo.Deployment, 253)
		repo.Cluster = boundedFrontendValue(repo.Cluster, 253)
		repo.Namespace = boundedFrontendValue(repo.Namespace, 253)
		repo.Revision = boundedFrontendValue(repo.Revision, 240)
		repo.RevisionSource = boundedFrontendValue(repo.RevisionSource, 240)
		repo.Precision = normalizedFrontendPrecision(repo.Precision)
		repo.Images = normalizedFrontendStrings(repo.Images, 12, 500)
		repo.Limitations = normalizedFrontendStrings(repo.Limitations, 12, 300)
	}
}

func normalizedFrontendPrecision(value string) string {
	switch strings.TrimSpace(value) {
	case FrontendPrecisionDeployedRevision, FrontendPrecisionImageDigest, FrontendPrecisionImageTag, FrontendPrecisionRepository:
		return strings.TrimSpace(value)
	default:
		return FrontendPrecisionUnavailable
	}
}

func normalizedFrontendStrings(values []string, limit, maxLength int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = boundedFrontendValue(value, maxLength); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func boundedFrontendValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}
