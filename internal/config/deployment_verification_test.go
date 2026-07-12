package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func deploymentVerificationConfig(t *testing.T, block string) SystemConfig {
	t.Helper()
	raw := `system: {id: shop, name: Shop}
agent: {name: bot}
environments:
  - id: test
` + block + `
repos:
  - {name: admin-web, url: git@example.com:admin-web.git, stack: node, env_branches: {test: test}}
generation: {targets: [codex]}
meta: {schema_version: "0.1"}
`
	var cfg SystemConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDeploymentVerificationHTTPRoundTrip(t *testing.T) {
	cfg := deploymentVerificationConfig(t, `    deployment_verification:
      provider: http
      http:
        url: "https://admin-test.example.com/version"
        json_pointer: "/git/commit"`)
	if err := Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip SystemConfig
	if err := yaml.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	got := roundTrip.Environments[0].DeploymentVerification
	if got.Provider != "http" || got.HTTP.URL != "https://admin-test.example.com/version" || got.HTTP.JSONPointer != "/git/commit" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestDeploymentVerificationK8sRoundTrip(t *testing.T) {
	cfg := deploymentVerificationConfig(t, `    deployment_verification:
      provider: k8s
      k8s:
        cluster: "test-cluster"
        namespace: "admin-test"
        deployments_by_repo:
          admin-web: "admin-web"
        commit_annotation: "app.example.com/git-commit"`)
	if err := Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Environments[0].DeploymentVerification.K8s
	if got.Cluster != "test-cluster" || got.Namespace != "admin-test" || got.DeploymentsByRepo["admin-web"] != "admin-web" || got.CommitAnnotation != "app.example.com/git-commit" {
		t.Fatalf("k8s = %+v", got)
	}
}

func TestDeploymentVerificationValidation(t *testing.T) {
	tests := map[string]struct{ block, want string }{
		"mixed blocks":     {`    deployment_verification: {provider: http, http: {url: https://x/version, json_pointer: /commit}, k8s: {cluster: c, namespace: n, deployments_by_repo: {admin-web: d}, commit_annotation: commit}}`, "must not include k8s"},
		"http url":         {`    deployment_verification: {provider: http, http: {json_pointer: /commit}}`, "http.url required"},
		"http pointer":     {`    deployment_verification: {provider: http, http: {url: https://x/version}}`, "http.json_pointer required"},
		"k8s cluster":      {`    deployment_verification: {provider: k8s, k8s: {namespace: n, deployments_by_repo: {admin-web: d}, commit_annotation: commit}}`, "k8s.cluster required"},
		"k8s namespace":    {`    deployment_verification: {provider: k8s, k8s: {cluster: c, deployments_by_repo: {admin-web: d}, commit_annotation: commit}}`, "k8s.namespace required"},
		"k8s map":          {`    deployment_verification: {provider: k8s, k8s: {cluster: c, namespace: n, commit_annotation: commit}}`, "deployments_by_repo required"},
		"unknown repo":     {`    deployment_verification: {provider: k8s, k8s: {cluster: c, namespace: n, deployments_by_repo: {ghost: d}, commit_annotation: commit}}`, "unknown repo"},
		"missing selector": {`    deployment_verification: {provider: k8s, k8s: {cluster: c, namespace: n, deployments_by_repo: {admin-web: d}}}`, "commit_annotation or image_label required"},
		"provider":         {`    deployment_verification: {provider: magic}`, "invalid"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := deploymentVerificationConfig(t, tt.block)
			err := Validate(&cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v, want %q", err, tt.want)
			}
		})
	}
}

func TestDeploymentVerificationManualAndLegacyAreSemanticallyEquivalent(t *testing.T) {
	legacy := deploymentVerificationConfig(t, "")
	manual := deploymentVerificationConfig(t, `    deployment_verification:
      provider: manual`)
	if err := Validate(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := Validate(&manual); err != nil {
		t.Fatal(err)
	}
	if legacy.Environments[0].DeploymentVerification.EffectiveProvider() != "manual" || manual.Environments[0].DeploymentVerification.EffectiveProvider() != "manual" {
		t.Fatal("empty and manual providers must be semantically equivalent")
	}
	data, err := yaml.Marshal(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "deployment_verification") {
		t.Fatalf("legacy YAML gained a deployment_verification block:\n%s", data)
	}
}

func TestDeploymentVerificationRejectsAutomaticLookupOutsideSystem(t *testing.T) {
	cfg := deploymentVerificationConfig(t, `    deployment_verification:
      provider: http
      http: {url: "https://admin-test.example.com/version", json_pointer: "/git/commit"}`)
	if _, err := cfg.DeploymentVerificationForEnvironment("ghost"); err == nil || !strings.Contains(err.Error(), "unknown environment") {
		t.Fatalf("unknown environment error=%v", err)
	}
}
