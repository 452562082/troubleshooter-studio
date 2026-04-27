package agent

import (
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// promptNames 把派生出的 prompts 拍成 name 切片,方便断言"该出现的 var 出现了"。
func promptNames(prompts []deployLikePrompt) []string {
	out := make([]string, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Name)
	}
	return out
}

// deployLikePrompt 是 deploy.Prompt 的别名(避免循环 import 时手动引用),
// 这里直接用 type alias 让 promptNames 跟实际函数对接。
type deployLikePrompt = struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	Secret bool   `json:"secret"`
}

func mkCfg(cc string, perEnvCC bool, envIDs []string) *config.SystemConfig {
	cfg := &config.SystemConfig{
		System: config.System{ID: "test-sys", Name: "Test System"},
		Agent:  config.Agent{Name: "Test Bot", WorkspaceName: "test-bot", Model: "openai/gpt-4"},
	}
	for _, id := range envIDs {
		cfg.Environments = append(cfg.Environments, config.Environment{ID: id})
	}
	cfg.Infrastructure.ConfigCenter = config.ConfigCenter{
		Type:              cc,
		PerEnvCredentials: perEnvCC,
	}
	return cfg
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// 把 agent.DerivePrompts 转成 deployLikePrompt 列表(类型对齐)。
func derive(t *testing.T, cfg *config.SystemConfig) []deployLikePrompt {
	t.Helper()
	raw := DerivePrompts(cfg)
	out := make([]deployLikePrompt, len(raw))
	for i, p := range raw {
		out[i] = deployLikePrompt{Name: p.Name, Prompt: p.Prompt, Secret: p.Secret}
	}
	return out
}

func TestDerivePrompts_NacosShared(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev", "prod"})
	got := promptNames(derive(t, cfg))

	for _, want := range []string{
		"CONFIG_CENTER_USERNAME", "CONFIG_CENTER_PASSWORD",
		"CC_ADDR_DEV", "CC_ADDR_PROD",
		"MODEL",
	} {
		if !contains(got, want) {
			t.Errorf("missing prompt %s; got=%v", want, got)
		}
	}
	for _, banned := range []string{"CC_USER_DEV", "CC_PASS_DEV"} {
		if contains(got, banned) {
			t.Errorf("shared mode should NOT have %s; got=%v", banned, got)
		}
	}
}

func TestDerivePrompts_NacosPerEnv(t *testing.T) {
	cfg := mkCfg("nacos", true, []string{"dev", "prod"})
	got := promptNames(derive(t, cfg))

	for _, want := range []string{
		"CC_ADDR_DEV", "CC_USER_DEV", "CC_PASS_DEV",
		"CC_ADDR_PROD", "CC_USER_PROD", "CC_PASS_PROD",
	} {
		if !contains(got, want) {
			t.Errorf("missing per-env prompt %s; got=%v", want, got)
		}
	}
	if contains(got, "CONFIG_CENTER_USERNAME") {
		t.Errorf("per-env mode should NOT emit shared CONFIG_CENTER_USERNAME")
	}
}

func TestDerivePrompts_Apollo(t *testing.T) {
	t.Run("shared", func(t *testing.T) {
		cfg := mkCfg("apollo", false, []string{"dev"})
		got := promptNames(derive(t, cfg))
		for _, want := range []string{"APOLLO_TOKEN", "APOLLO_META_DEV"} {
			if !contains(got, want) {
				t.Errorf("missing %s; got=%v", want, got)
			}
		}
	})
	t.Run("per-env", func(t *testing.T) {
		cfg := mkCfg("apollo", true, []string{"dev"})
		got := promptNames(derive(t, cfg))
		for _, want := range []string{"APOLLO_META_DEV", "APOLLO_TOKEN_DEV"} {
			if !contains(got, want) {
				t.Errorf("missing %s; got=%v", want, got)
			}
		}
		if contains(got, "APOLLO_TOKEN") {
			t.Errorf("per-env mode should not emit shared APOLLO_TOKEN")
		}
	})
}

func TestDerivePrompts_Consul(t *testing.T) {
	cfg := mkCfg("consul", true, []string{"dev"})
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"CONSUL_HOST_DEV", "CONSUL_TOKEN_DEV"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
}

func TestDerivePrompts_EnvVars(t *testing.T) {
	cfg := mkCfg("env-vars", false, []string{"dev"})
	cfg.Infrastructure.DataStores = []config.DataStore{
		{Type: "redis", Enabled: true},
		{Type: "mysql", Enabled: true},
		{Type: "kafka", Enabled: false}, // disabled,应被跳过
	}
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"STATIC_REDIS_DEV", "STATIC_MYSQL_DEV"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
	if contains(got, "STATIC_KAFKA_DEV") {
		t.Errorf("disabled data store should NOT be prompted")
	}
}

func TestDerivePrompts_Kubernetes(t *testing.T) {
	cfg := mkCfg("kubernetes", false, []string{"prod"})
	got := promptNames(derive(t, cfg))
	for _, want := range []string{
		"K8S_CONTEXT_PROD", "K8S_NAMESPACE_PROD",
		"K8S_CONFIGMAP_PROD", "K8S_SECRET_PROD",
	} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
}

func TestDerivePrompts_Observability(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev", "prod"})
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true, PerEnvCredentials: false}
	cfg.Infrastructure.Observability.Jaeger = config.Jaeger{Enabled: true}
	cfg.Infrastructure.Observability.ELK = config.ELK{Enabled: true}

	got := promptNames(derive(t, cfg))
	for _, want := range []string{
		"GRAFANA_USERNAME", "GRAFANA_PASSWORD", "GRAFANA_URL_DEV", "GRAFANA_URL_PROD",
		"JAEGER_URL_DEV", "JAEGER_URL_PROD",
		"ELK_USERNAME", "ELK_PASSWORD", "KIBANA_URL_DEV", "ELK_ES_URL_DEV",
	} {
		if !contains(got, want) {
			t.Errorf("missing observability prompt %s; got=%v", want, got)
		}
	}
}

func TestDerivePrompts_GrafanaPerEnv(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev"})
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true, PerEnvCredentials: true}
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"GRAFANA_URL_DEV", "GRAFANA_USER_DEV", "GRAFANA_PASS_DEV"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
	if contains(got, "GRAFANA_USERNAME") {
		t.Errorf("per-env grafana should not emit shared GRAFANA_USERNAME")
	}
}

func TestDerivePrompts_Messaging(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev"})
	cfg.Infrastructure.Messaging = []config.Messaging{
		{Platform: "lark", Enabled: true},
	}
	cfg.Infrastructure.ProjectTracking = []config.ProjectTracking{
		{Platform: "feishu_project", Enabled: true},
	}
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"LARK_APP_ID", "LARK_APP_SECRET", "MCP_USER_TOKEN"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
}

func TestDerivePrompts_DisabledMessagingSkipped(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev"})
	cfg.Infrastructure.Messaging = []config.Messaging{
		{Platform: "lark", Enabled: false},
	}
	got := promptNames(derive(t, cfg))
	if contains(got, "LARK_APP_ID") {
		t.Errorf("disabled lark should NOT prompt")
	}
}

func TestDerivePrompts_SecretFlags(t *testing.T) {
	cfg := mkCfg("nacos", true, []string{"dev"})
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true, PerEnvCredentials: true}
	got := DerivePrompts(cfg)

	// 密码类必须 secret=true,URL/用户名类必须 false
	want := map[string]bool{
		"CC_USER_DEV":     false,
		"CC_PASS_DEV":     true,
		"GRAFANA_URL_DEV": false,
		"GRAFANA_PASS_DEV": true,
	}
	for _, p := range got {
		if expected, ok := want[p.Name]; ok && p.Secret != expected {
			t.Errorf("%s: secret want=%v got=%v", p.Name, expected, p.Secret)
		}
	}
}

// MODEL prompt 总是出现,且 prompt 文案带 cfg.Agent.Model 默认值
func TestDerivePrompts_ModelDefaultEmbedded(t *testing.T) {
	cfg := mkCfg("nacos", false, []string{"dev"})
	got := DerivePrompts(cfg)
	for _, p := range got {
		if p.Name == "MODEL" {
			if !strings.Contains(p.Prompt, cfg.Agent.Model) {
				t.Errorf("MODEL prompt should embed default %q; got %q", cfg.Agent.Model, p.Prompt)
			}
			return
		}
	}
	t.Errorf("MODEL prompt missing")
}
