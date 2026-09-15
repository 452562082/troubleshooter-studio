package main

import (
	"context"
	"errors"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type frontendRuntimeReader struct {
	version bughub.K8sDeploymentVersion
	err     error
	calls   int
}

func (r *frontendRuntimeReader) ReadDeployment(context.Context, string, string, string) (bughub.K8sDeploymentVersion, error) {
	r.calls++
	return r.version, r.err
}

func frontendRuntimeConfig(t *testing.T) *config.SystemConfig {
	t.Helper()
	cfg, err := config.LoadFromBytes([]byte(`
system: {id: base, name: Base}
agent: {name: bot}
environments:
  - id: test
    deployment_verification:
      provider: k8s
      k8s:
        cluster: test-cluster
        namespace: web-test
        deployments_by_repo: {admin-web: admin-web}
        commit_annotation: app.example.com/git-commit
infrastructure:
  observability:
    k8s_runtime:
      provider: kuboard
      endpoints:
        - {env: test, url: "https://kuboard.example.com"}
repos:
  - {name: admin-web, url: "git@example.com:admin-web.git", stack: node, role: frontend, sub_path: apps/admin, env_branches: {test: test}}
  - {name: backend, url: "git@example.com:backend.git", stack: go, role: backend, service_names: [backend], env_branches: {test: test}}
generation: {targets: [codex]}
meta: {schema_version: "0.1"}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCaseFrontendRuntimeResolverUsesConfiguredDeploymentRevision(t *testing.T) {
	cfg := frontendRuntimeConfig(t)
	reader := &frontendRuntimeReader{version: bughub.K8sDeploymentVersion{
		Annotations: map[string]string{"app.example.com/git-commit": "abc123"},
		Images:      []string{"registry.example.com/admin-web:build-42"},
	}}
	app := &App{
		workflowLoadDeploymentConfig: func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) { return cfg, nil },
		workflowK8sReaderFactory: func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
			return reader, nil
		},
	}
	manifest, err := (caseFrontendRuntimeResolver{app: app}).ResolveFrontendRuntime(context.Background(), bughub.IncidentCase{SystemID: "base", Environment: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || manifest.Precision != bughub.FrontendPrecisionDeployedRevision || len(manifest.Repositories) != 2 {
		t.Fatalf("calls=%d manifest=%+v", reader.calls, manifest)
	}
	repository := manifest.Repositories[0]
	if repository.Repo != "admin-web" || repository.SubPath != "apps/admin" || repository.Deployment != "admin-web" || repository.Revision != "abc123" || repository.RevisionSource != "annotation:app.example.com/git-commit" {
		t.Fatalf("repository = %+v", repository)
	}
	if backend := manifest.Repositories[1]; backend.Repo != "backend" || backend.Precision != bughub.FrontendPrecisionRepository || len(backend.Limitations) == 0 {
		t.Fatalf("backend repository = %+v", backend)
	}
}

func TestCaseFrontendRuntimeResolverFallsBackFromDigestToRepository(t *testing.T) {
	t.Run("image digest", func(t *testing.T) {
		cfg := frontendRuntimeConfig(t)
		reader := &frontendRuntimeReader{version: bughub.K8sDeploymentVersion{Images: []string{"registry.example.com/admin-web@sha256:abcdef"}}}
		app := &App{
			workflowLoadDeploymentConfig: func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) { return cfg, nil },
			workflowK8sReaderFactory: func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
				return reader, nil
			},
		}
		manifest, err := (caseFrontendRuntimeResolver{app: app}).ResolveFrontendRuntime(context.Background(), bughub.IncidentCase{SystemID: "base", Environment: "test"})
		if err != nil || manifest.Precision != bughub.FrontendPrecisionImageDigest || manifest.Repositories[0].Revision != "sha256:abcdef" {
			t.Fatalf("manifest=%+v err=%v", manifest, err)
		}
	})

	t.Run("repository after K8s read failure", func(t *testing.T) {
		cfg := frontendRuntimeConfig(t)
		reader := &frontendRuntimeReader{err: errors.New("unavailable")}
		app := &App{
			workflowLoadDeploymentConfig: func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) { return cfg, nil },
			workflowK8sReaderFactory: func(context.Context, *config.SystemConfig, config.Environment) (bughub.K8sDeploymentReader, error) {
				return reader, nil
			},
		}
		manifest, err := (caseFrontendRuntimeResolver{app: app}).ResolveFrontendRuntime(context.Background(), bughub.IncidentCase{SystemID: "base", Environment: "test"})
		if err != nil || manifest.Precision != bughub.FrontendPrecisionRepository || len(manifest.Repositories[0].Limitations) == 0 {
			t.Fatalf("manifest=%+v err=%v", manifest, err)
		}
	})
}
