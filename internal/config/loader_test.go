package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalValid 返回一份最小可通过校验的 SystemConfig 副本，便于测试中微调
func minimalValid() SystemConfig {
	return SystemConfig{
		System: System{ID: "demo", Name: "Demo"},
		Agent:  Agent{Name: "AI Agent", WorkspaceName: "AI Agent", Model: "openai-codex/gpt-5.3-codex"},
		Environments: []Environment{
			{ID: "dev", APIDomain: "api-dev.example.com", IsProd: false},
		},
		Generation: Generation{TargetHost: "openclaw"},
		Meta:       Meta{SchemaVersion: "0.1"},
	}
}

func TestValidate_Minimal(t *testing.T) {
	c := minimalValid()
	if err := Validate(&c); err != nil {
		t.Fatalf("expected minimal config to be valid, got: %v", err)
	}
}

func TestValidate_MissingRequired(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SystemConfig)
		errSub string
	}{
		{"no_system_id", func(c *SystemConfig) { c.System.ID = "" }, "system.id required"},
		{"no_system_name", func(c *SystemConfig) { c.System.Name = "" }, "system.name required"},
		{"no_agent_name", func(c *SystemConfig) { c.Agent.Name = "" }, "agent.name required"},
		{"no_workspace_name", func(c *SystemConfig) { c.Agent.WorkspaceName = "" }, "agent.workspace_name required"},
		{"no_agent_model", func(c *SystemConfig) { c.Agent.Model = "" }, "agent.model required"},
		{"empty_environments", func(c *SystemConfig) { c.Environments = nil }, "environments must have"},
		{"invalid_target", func(c *SystemConfig) { c.Generation.Targets = []string{"invalid"} }, "not supported"},
		{"no_schema_version", func(c *SystemConfig) { c.Meta.SchemaVersion = "" }, "meta.schema_version required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValid()
			tc.mutate(&c)
			err := Validate(&c)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSub)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

func TestValidate_BadSystemID(t *testing.T) {
	cases := []string{"Demo", "demo!", "_demo", "demo_x"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			c := minimalValid()
			c.System.ID = id
			if err := Validate(&c); err == nil {
				t.Fatalf("expected invalid id %q to be rejected", id)
			}
		})
	}
}

func TestValidate_DuplicateEnvID(t *testing.T) {
	c := minimalValid()
	c.Environments = append(c.Environments, Environment{ID: "dev"})
	err := Validate(&c)
	if err == nil || !strings.Contains(err.Error(), "duplicate environment id") {
		t.Fatalf("expected duplicate-env error, got: %v", err)
	}
}

func TestValidate_RepoReferencesUnknownEnv(t *testing.T) {
	c := minimalValid()
	c.Repos = []Repo{{
		Name: "svc", URL: "git@x:y.git", Role: "backend", Stack: "go",
		EnvBranches: map[string]string{"staging": "main"},
	}}
	err := Validate(&c)
	if err == nil || !strings.Contains(err.Error(), "unknown env: staging") {
		t.Fatalf("expected unknown-env error, got: %v", err)
	}
}

func TestValidate_UnsupportedTargetHost(t *testing.T) {
	c := minimalValid()
	c.Generation.Targets = []string{"nonexistent-platform"}
	err := Validate(&c)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected not-supported error, got: %v", err)
	}
}

func TestValidate_ConfigCenterEnvCheck(t *testing.T) {
	c := minimalValid()
	c.Infrastructure.ConfigCenter = ConfigCenter{
		Type: "nacos",
		Endpoints: []ConfigCenterEndpoint{
			{Env: "ghost", Addr: "x:8848"},
		},
	}
	err := Validate(&c)
	if err == nil || !strings.Contains(err.Error(), "env unknown: ghost") {
		t.Fatalf("expected config-center env-unknown error, got: %v", err)
	}
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	yaml := `
system: {id: d, name: D}
agent: {name: a, workspace_name: a, model: m}
environments:
  - id: dev
    api_domain: x
    is_prod: false
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
repos:
  - name: r
    url: git@x:y.git
    role: backend
    stack: go
    env_branches: {dev: main}
`
	p := filepath.Join(dir, "sys.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Generation.OutputDir != "./dist" {
		t.Errorf("default output_dir expected ./dist, got %q", cfg.Generation.OutputDir)
	}
	if cfg.Generation.MappingReviewMode != "strict" {
		t.Errorf("default review mode expected strict, got %q", cfg.Generation.MappingReviewMode)
	}
	if cfg.Repos[0].Analysis.ShallowDepth != 50 {
		t.Errorf("default shallow_depth expected 50, got %d", cfg.Repos[0].Analysis.ShallowDepth)
	}
}
