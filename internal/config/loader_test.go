package config

import (
	"fmt"
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
		// workspace_name 不再是硬必填:有 system.id / agent.id 就能派生;只在三者都空时才拦。
		{"no_workspace_anything", func(c *SystemConfig) {
			c.Agent.WorkspaceName = ""
			c.Agent.ID = ""
			c.System.ID = ""
		}, "system.id required"},
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

func TestValidateBrowserAuthOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://login.example.com",
		"http://localhost:8080",
	} {
		t.Run("valid_"+origin, func(t *testing.T) {
			cfg := minimalValid()
			cfg.Environments[0].BrowserAuthOrigins = []string{origin}
			if err := Validate(&cfg); err != nil {
				t.Fatal(err)
			}
		})
	}

	invalid := []string{
		"login.example.com",
		"ftp://login.example.com",
		"https://user:pass@login.example.com/path",
		"https://login.example.com/",
		"https://login.example.com/path",
		"https://login.example.com?next=/users",
		"https://login.example.com#form",
	}
	for _, origin := range invalid {
		t.Run("invalid_"+origin, func(t *testing.T) {
			cfg := minimalValid()
			cfg.Environments[0].BrowserAuthOrigins = []string{origin}
			err := Validate(&cfg)
			if err == nil || !strings.Contains(err.Error(), "browser_auth_origins") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestValidate_RepoReferencesUnknownEnv(t *testing.T) {
	c := minimalValid()
	c.Repos = []Repo{{
		Name: "svc", URL: "git@x:y.git", Stack: "go",
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
	// 多源 schema:Validate 直接看 ConfigCenters[]
	c.Infrastructure.ConfigCenters = []ConfigCenter{{
		ID:   "main",
		Type: "nacos",
		Endpoints: []ConfigCenterEndpoint{
			{Env: "ghost", Addr: "x:8848"},
		},
	}}
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
	if cfg.Repos[0].Analysis.ShallowDepth != 50 {
		t.Errorf("default shallow_depth expected 50, got %d", cfg.Repos[0].Analysis.ShallowDepth)
	}
}

// ── 多源 config_centers 迁移 / 校验 ──

// 老 yaml(单数 config_center)应自动迁到 ConfigCenters[0],id 默认 "default",
// 所有 repos.config_source 自动绑到那个 id。
func TestMigrate_LegacySingleConfigCenter(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
repos:
  - {name: r, url: g, role: backend, stack: go, env_branches: {dev: main}}
infrastructure:
  config_center:
    type: nacos
    endpoints: [{env: dev, addr: nacos-dev:8848}]
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Infrastructure.ConfigCenters) != 1 {
		t.Fatalf("应迁成 ConfigCenters[1],got len=%d", len(cfg.Infrastructure.ConfigCenters))
	}
	if cfg.Infrastructure.ConfigCenters[0].ID != "default" {
		t.Errorf("默认 id 应为 'default',got %q", cfg.Infrastructure.ConfigCenters[0].ID)
	}
	if cfg.Infrastructure.ConfigCenters[0].Type != "nacos" {
		t.Errorf("type 应保留,got %q", cfg.Infrastructure.ConfigCenters[0].Type)
	}
	// ConfigCenter 现在保留作为 ConfigCenters[0] 的镜像,供老 template/老代码兼容读
	if cfg.Infrastructure.ConfigCenter.Type != "nacos" {
		t.Errorf("ConfigCenter 应镜像 ConfigCenters[0].Type=nacos,got %q", cfg.Infrastructure.ConfigCenter.Type)
	}
	if cfg.Repos[0].ConfigSource != "default" {
		t.Errorf("repo.config_source 应自动绑 'default',got %q", cfg.Repos[0].ConfigSource)
	}
}

// 显式新 schema:多源 + 各仓库引用不同源,应正常通过 + 不被 migrate 改写
func TestMigrate_NewSchemaMultiSource(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - id: main-nacos
      type: nacos
      endpoints: [{env: dev, addr: nacos-dev:8848}]
    - id: legacy-k8s
      type: kuboard
repos:
  - {name: order, url: g1, role: backend, stack: go, env_branches: {dev: main}, config_source: main-nacos}
  - {name: legacy, url: g2, role: backend, stack: java, env_branches: {dev: main}, config_source: legacy-k8s}
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Infrastructure.ConfigCenters) != 2 {
		t.Fatalf("应保留 2 个源,got %d", len(cfg.Infrastructure.ConfigCenters))
	}
	if cfg.Repos[0].ConfigSource != "main-nacos" || cfg.Repos[1].ConfigSource != "legacy-k8s" {
		t.Errorf("config_source 应保留显式值,got %q / %q", cfg.Repos[0].ConfigSource, cfg.Repos[1].ConfigSource)
	}
}

// one2all 是合法配置源:通过 streamable-http MCP 读 ConfigMap/Secret。
func TestMigrate_One2AllConfigCenterSupported(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - id: default
      type: one2all
      endpoints:
        - url: http://one2all.example.com/mcp/hash
          token: "{{ONE2ALL_TOKEN}}"
      service_map:
        dev:
          order-service:
            cluster_id: "1"
            namespace: default
            configmap: order-config
repos:
  - {name: order, url: g, role: backend, stack: go, service_names: [order-service], env_branches: {dev: main}, config_source: default}
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("one2all config center should be valid: %v", err)
	}
	if got := cfg.Infrastructure.ConfigCenters[0].Type; got != "one2all" {
		t.Fatalf("config center type = %q, want one2all", got)
	}
}

// 新 schema 多源,但某仓库没显式 config_source 应自动绑到第一个(降级行为)
func TestMigrate_MultiSourceMissingRefDefaultsFirst(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - {id: alpha, type: nacos, endpoints: [{env: dev, addr: a:8848}]}
    - {id: beta,  type: kuboard}
repos:
  - {name: order, url: g, role: backend, stack: go, env_branches: {dev: main}}
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Repos[0].ConfigSource != "alpha" {
		t.Errorf("缺 config_source 应绑首个 'alpha',got %q", cfg.Repos[0].ConfigSource)
	}
}

// 仓库引用了不存在的 source id,应报错
func TestMigrate_RepoReferencesUnknownSource(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - {id: real, type: nacos, endpoints: [{env: dev, addr: a:8848}]}
repos:
  - {name: order, url: g, role: backend, stack: go, env_branches: {dev: main}, config_source: ghost}
`
	_, err := LoadFromBytes([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "config_source") {
		t.Fatalf("引用了不存在 source 应报错,got: %v", err)
	}
}

// 多源 id 重复应报错
func TestMigrate_DuplicateSourceID(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - {id: same, type: nacos,      endpoints: [{env: dev, addr: a:8848}]}
    - {id: same, type: kuboard}
`
	_, err := LoadFromBytes([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "duplicate config_center id") {
		t.Fatalf("重复 id 应报错,got: %v", err)
	}
}

// 多源中某项 id 为空应报错(单源迁移路径会自动补 default,这里是用户显式声明 ConfigCenters 但漏写)
func TestMigrate_EmptyIDInMultiSource(t *testing.T) {
	yaml := `
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - {id: ok, type: nacos, endpoints: [{env: dev, addr: a:8848}]}
    - {type: kuboard}
`
	_, err := LoadFromBytes([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "config_centers[1].id required") {
		t.Fatalf("缺 id 应报错,got: %v", err)
	}
}

// id 格式校验
func TestMigrate_BadSourceID(t *testing.T) {
	for _, bad := range []string{"Main", "main_x", "-main", "main!"} {
		yaml := fmt.Sprintf(`
system: {id: shop, name: Shop}
agent: {name: a, workspace_name: a, model: m}
environments:
  - {id: dev, api_domain: x, is_prod: false}
generation: {target_host: openclaw}
meta: {schema_version: "0.1"}
infrastructure:
  config_centers:
    - {id: %q, type: nacos, endpoints: [{env: dev, addr: a:8848}]}
`, bad)
		_, err := LoadFromBytes([]byte(yaml))
		if err == nil || !strings.Contains(err.Error(), "must match") {
			t.Errorf("%q 应被拒(格式不合法),got: %v", bad, err)
		}
	}
}

// PrimaryConfigCenter helper:旧代码兼容访问点
func TestPrimaryConfigCenter(t *testing.T) {
	c := minimalValid()
	if got := c.Infrastructure.PrimaryConfigCenter().Type; got != "" {
		t.Errorf("无源时应返回零值,got %q", got)
	}
	c.Infrastructure.ConfigCenters = []ConfigCenter{{ID: "x", Type: "nacos"}}
	if got := c.Infrastructure.PrimaryConfigCenter().Type; got != "nacos" {
		t.Errorf("有源时应返回首个,got %q", got)
	}
}
