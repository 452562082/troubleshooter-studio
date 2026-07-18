package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeFrontendRuntimeWritesAuditableCascadeManifest(t *testing.T) {
	root := resolvedTempDir(t)
	staging, err := openAttemptEvidenceStaging(root, "frontend-runtime-attempt")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Cleanup()
	defer staging.Close()
	resolver := FrontendRuntimeResolverFunc(func(context.Context, IncidentCase) (FrontendRuntimeManifest, error) {
		return FrontendRuntimeManifest{
			Environment: "test", Precision: FrontendPrecisionDeployedRevision, SourceMapStatus: "not_registered",
			Repositories: []FrontendRuntimeRepository{{Repo: "admin-web", Role: "frontend", SubPath: "apps/admin", Deployment: "admin-web", Revision: "abc123", RevisionSource: "annotation:git-commit", Precision: FrontendPrecisionDeployedRevision}},
		}, nil
	})
	prompt, err := (&AgentPhaseRunner{}).materializeFrontendRuntime(context.Background(), PhaseAttempt{Phase: PhaseInvestigation}, IncidentCase{Environment: "test"}, staging, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, runtimeCodeManifestName) || !strings.Contains(prompt, "never present") || !strings.Contains(prompt, "source map") || !strings.Contains(prompt, "git show") {
		t.Fatalf("prompt does not describe the precision cascade: %q", prompt)
	}
	data, err := os.ReadFile(filepath.Join(staging.Path(), frontendRuntimeManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest FrontendRuntimeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Precision != FrontendPrecisionDeployedRevision || len(manifest.Repositories) != 1 || manifest.Repositories[0].Revision != "abc123" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestMaterializeFrontendRuntimeDegradesResolverFailure(t *testing.T) {
	root := resolvedTempDir(t)
	staging, err := openAttemptEvidenceStaging(root, "frontend-runtime-fallback")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Cleanup()
	defer staging.Close()
	resolver := FrontendRuntimeResolverFunc(func(context.Context, IncidentCase) (FrontendRuntimeManifest, error) {
		return FrontendRuntimeManifest{}, errors.New("K8s temporarily unavailable")
	})
	if _, err := (&AgentPhaseRunner{}).materializeFrontendRuntime(context.Background(), PhaseAttempt{Phase: PhaseInvestigation}, IncidentCase{Environment: "test"}, staging, resolver); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(staging.Path(), frontendRuntimeManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest FrontendRuntimeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Precision != FrontendPrecisionUnavailable || manifest.Environment != "test" || len(manifest.Limitations) != 1 {
		t.Fatalf("fallback manifest = %+v", manifest)
	}
}
