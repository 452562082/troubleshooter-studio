package bughub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeRepositoryAccessUsesConfiguredPathsWithoutHomeDiscovery(t *testing.T) {
	repository := t.TempDir()
	stagingPath := t.TempDir()
	staging := &lifecycleStaging{path: stagingPath}
	runner := &AgentPhaseRunner{}
	resolver := RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
		return map[string]string{"base-backend": repository}, nil
	})
	prompt, err := runner.materializeRepositoryAccess(context.Background(), PhaseAttempt{Phase: PhaseInvestigation}, IncidentCase{SystemID: "base"}, staging, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Never enumerate or search `/`, `/Users`", "do not run `find`"} {
		if !strings.Contains(prompt, forbidden) {
			t.Fatalf("repository boundary prompt missing %q:\n%s", forbidden, prompt)
		}
	}
	data, err := os.ReadFile(filepath.Join(stagingPath, repositoryAccessManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest repositoryAccessManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Roots) != 1 || manifest.Roots[0].Repo != "base-backend" || manifest.Roots[0].Path != repository || manifest.Roots[0].Access != "read" {
		t.Fatalf("repository access manifest = %+v", manifest)
	}
}

func TestMaterializeRepositoryAccessDoesNotReplaceMissingPathWithHomeScan(t *testing.T) {
	stagingPath := t.TempDir()
	staging := &lifecycleStaging{path: stagingPath}
	runner := &AgentPhaseRunner{}
	resolver := RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
		return map[string]string{"missing": filepath.Join(t.TempDir(), "absent")}, nil
	})
	if _, err := runner.materializeRepositoryAccess(context.Background(), PhaseAttempt{Phase: PhaseInvestigation}, IncidentCase{}, staging, resolver, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stagingPath, repositoryAccessManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest repositoryAccessManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Roots) != 0 || len(manifest.Limitations) == 0 {
		t.Fatalf("missing path unexpectedly granted access: %+v", manifest)
	}
}

func TestMaterializeRepositoryAccessLeavesBrowserPhaseStagingEmpty(t *testing.T) {
	for _, phase := range []Phase{PhaseValidation, PhaseRegression} {
		t.Run(string(phase), func(t *testing.T) {
			stagingPath := t.TempDir()
			staging := &lifecycleStaging{path: stagingPath}
			resolverCalled := false
			resolver := RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
				resolverCalled = true
				return map[string]string{"base-backend": t.TempDir()}, nil
			})

			prompt, err := (&AgentPhaseRunner{}).materializeRepositoryAccess(
				context.Background(),
				PhaseAttempt{Phase: phase},
				IncidentCase{SystemID: "base"},
				staging,
				resolver,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if prompt != "" {
				t.Fatalf("browser phase repository prompt = %q, want empty", prompt)
			}
			if resolverCalled {
				t.Fatal("browser phase unexpectedly resolved repository paths")
			}
			entries, err := os.ReadDir(stagingPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("browser phase staging entries = %v, want empty", entries)
			}
		})
	}
}
