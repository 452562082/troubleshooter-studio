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

// mkCfg 单源场景:用 "default" id 模拟"老 yaml 自动迁移"产物。
// 这样产生的 prompt 名跟老形态一致(无 source 前缀),保证旧测试断言依然有效。
func mkCfg(cc string, envIDs []string) *config.SystemConfig {
	cfg := &config.SystemConfig{
		System: config.System{ID: "test-sys", Name: "Test System"},
		Agent:  config.Agent{Name: "Test Bot", WorkspaceName: "test-bot", Model: "openai/gpt-4"},
	}
	for _, id := range envIDs {
		cfg.Environments = append(cfg.Environments, config.Environment{ID: id})
	}
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{{
		ID:   "default",
		Type: cc,
	}}
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

func TestDerivePrompts_Nacos(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev", "prod"})
	got := promptNames(derive(t, cfg))

	for _, want := range []string{
		"CC_ADDR_DEV", "CC_USER_DEV", "CC_PASS_DEV",
		"CC_ADDR_PROD", "CC_USER_PROD", "CC_PASS_PROD",
		"MODEL",
	} {
		if !contains(got, want) {
			t.Errorf("missing per-env prompt %s; got=%v", want, got)
		}
	}
	for _, banned := range []string{"CONFIG_CENTER_USERNAME", "CONFIG_CENTER_PASSWORD"} {
		if contains(got, banned) {
			t.Errorf("shared CC creds removed, should NOT have %s; got=%v", banned, got)
		}
	}
}

func TestDerivePrompts_Apollo(t *testing.T) {
	cfg := mkCfg("apollo", []string{"dev"})
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"APOLLO_META_DEV", "APOLLO_TOKEN_DEV"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
	if contains(got, "APOLLO_TOKEN") {
		t.Errorf("shared APOLLO_TOKEN removed; got=%v", got)
	}
}

func TestDerivePrompts_Consul(t *testing.T) {
	cfg := mkCfg("consul", []string{"dev"})
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"CONSUL_HOST_DEV", "CONSUL_TOKEN_DEV"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
	if contains(got, "CONSUL_TOKEN") {
		t.Errorf("shared CONSUL_TOKEN removed; got=%v", got)
	}
}

func TestDerivePrompts_EnvVars(t *testing.T) {
	cfg := mkCfg("env-vars", []string{"dev"})
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

func TestDerivePrompts_Kuboard(t *testing.T) {
	cfg := mkCfg("kuboard", []string{"prod"})
	got := promptNames(derive(t, cfg))
	// cluster/namespace/configmap 不再在 install 时问,走 service_map per-service 读
	for _, want := range []string{"KUBOARD_URL_PROD", "KUBOARD_USER_PROD", "KUBOARD_PASS_PROD"} {
		if !contains(got, want) {
			t.Errorf("missing %s; got=%v", want, got)
		}
	}
	for _, banned := range []string{"KUBOARD_CLUSTER_PROD", "KUBOARD_NAMESPACE_PROD", "KUBOARD_CONFIGMAP_PROD"} {
		if contains(got, banned) {
			t.Errorf("kuboard locator 字段已迁移到 service_map,不应再 prompt %s", banned)
		}
	}
}

func TestDerivePrompts_Observability(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev", "prod"})
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true}
	cfg.Infrastructure.Observability.Jaeger = config.Jaeger{Enabled: true}
	cfg.Infrastructure.Observability.ELK = config.ELK{Enabled: true}

	got := promptNames(derive(t, cfg))
	for _, want := range []string{
		"GRAFANA_URL_DEV", "GRAFANA_API_KEY_DEV", "GRAFANA_USER_DEV", "GRAFANA_PASS_DEV",
		"GRAFANA_URL_PROD", "GRAFANA_API_KEY_PROD", "GRAFANA_USER_PROD", "GRAFANA_PASS_PROD",
		"JAEGER_URL_DEV", "JAEGER_URL_PROD",
		"ELK_USERNAME", "ELK_PASSWORD", "KIBANA_URL_DEV", "ELK_ES_URL_DEV",
	} {
		if !contains(got, want) {
			t.Errorf("missing observability prompt %s; got=%v", want, got)
		}
	}
	if contains(got, "GRAFANA_USERNAME") {
		t.Errorf("shared GRAFANA_USERNAME removed; got=%v", got)
	}
}

func TestDerivePrompts_Messaging(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev"})
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
	cfg := mkCfg("nacos", []string{"dev"})
	cfg.Infrastructure.Messaging = []config.Messaging{
		{Platform: "lark", Enabled: false},
	}
	got := promptNames(derive(t, cfg))
	if contains(got, "LARK_APP_ID") {
		t.Errorf("disabled lark should NOT prompt")
	}
}

func TestDerivePrompts_SecretFlags(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev"})
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true}
	got := DerivePrompts(cfg)

	// 密码类必须 secret=true,URL/用户名类必须 false
	want := map[string]bool{
		"CC_USER_DEV":      false,
		"CC_PASS_DEV":      true,
		"GRAFANA_URL_DEV":  false,
		"GRAFANA_PASS_DEV": true,
	}
	for _, p := range got {
		if expected, ok := want[p.Name]; ok && p.Secret != expected {
			t.Errorf("%s: secret want=%v got=%v", p.Name, expected, p.Secret)
		}
	}
}

// 多源场景:每个源独立 prompt 集合,显式 source.id 进入 var 名作为命名空间。
func TestDerivePrompts_MultiSource(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev"})
	// 第二个源:kuboard
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{
		{ID: "main-nacos", Type: "nacos"},
		{ID: "legacy-kuboard", Type: "kuboard"},
	}
	got := promptNames(derive(t, cfg))

	// 多源:每个源 var 名带 source 前缀,不再裸 CC_ADDR_DEV
	for _, want := range []string{
		"CC_ADDR_MAIN_NACOS_DEV", "CC_USER_MAIN_NACOS_DEV", "CC_PASS_MAIN_NACOS_DEV",
		"KUBOARD_URL_LEGACY_KUBOARD_DEV", "KUBOARD_USER_LEGACY_KUBOARD_DEV", "KUBOARD_PASS_LEGACY_KUBOARD_DEV",
	} {
		if !contains(got, want) {
			t.Errorf("missing multi-source prompt %s; got=%v", want, got)
		}
	}
	// 老 single-source 形态的 var 名(无 source 前缀)在多源场景下不应再出现
	for _, banned := range []string{"CC_ADDR_DEV", "KUBOARD_URL_DEV"} {
		if contains(got, banned) {
			t.Errorf("multi-source 不应回落到老 var 名 %s; got=%v", banned, got)
		}
	}
	// kuboard 的 cluster/namespace/configmap 已迁移到 service_map,不再 install 时 prompt
	for _, banned := range []string{"KUBOARD_CLUSTER_LEGACY_KUBOARD_DEV", "KUBOARD_NAMESPACE_LEGACY_KUBOARD_DEV", "KUBOARD_CONFIGMAP_LEGACY_KUBOARD_DEV"} {
		if contains(got, banned) {
			t.Errorf("kuboard locator 字段不应再 prompt %s", banned)
		}
	}
}

// 单源迁移路径(source.id == "default"):prompt 名应跟老形态完全一致(向后兼容)
func TestDerivePrompts_LegacyDefaultIDKeepsOldNames(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev"})
	// id="default" 是单源迁移 sentinel
	got := promptNames(derive(t, cfg))
	for _, want := range []string{"CC_ADDR_DEV", "CC_USER_DEV", "CC_PASS_DEV"} {
		if !contains(got, want) {
			t.Errorf("default id 应保留老 var 名 %s,got=%v", want, got)
		}
	}
}

// MODEL prompt 总是出现,且 prompt 文案带 cfg.Agent.Model 默认值
func TestDerivePrompts_ModelDefaultEmbedded(t *testing.T) {
	cfg := mkCfg("nacos", []string{"dev"})
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
