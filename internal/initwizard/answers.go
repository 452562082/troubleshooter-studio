package initwizard

import (
	"fmt"
	"io"
	"strings"
)

type EnvAnswer struct {
	ID        string
	APIDomain string
	IsProd    bool
}

type RepoAnswer struct {
	Name         string
	URL          string
	Role         string
	Stack        string
	Framework    string
	ServiceNames []string
	EnvBranches  map[string]string // env id → branch
}

type Answers struct {
	SystemID, SystemName, SystemDescription string
	AgentName, AgentModel, WorkspaceName    string

	Envs  []EnvAnswer
	Repos []RepoAnswer

	ConfigCenterType string // nacos / apollo / consul / none

	GrafanaEnabled    bool
	LokiEnabled       bool
	PrometheusEnabled bool

	DataStoresEnabled map[string]bool // redis/mongodb/elasticsearch/mysql/kafka → bool

	LarkEnabled    bool
	LarkAttachment bool

	FeishuProjectEnabled bool

	OutputDir string
}

// WriteYAML 手写 YAML，避免引入 yaml.Encoder 的字段顺序与 omitempty 复杂度
// 输出结构与 examples/*-system.yaml 保持一致
func (a *Answers) WriteYAML(out io.Writer) error {
	buf := &strings.Builder{}
	p := func(format string, args ...any) { fmt.Fprintf(buf, format, args...) }

	p("# 由 factory init 生成，可手工调整\n")
	p("system:\n")
	p("  id: %s\n", a.SystemID)
	p("  name: %q\n", a.SystemName)
	if a.SystemDescription != "" {
		p("  description: %q\n", a.SystemDescription)
	}
	p("\nagent:\n")
	p("  name: %q\n", a.AgentName)
	p("  workspace_name: %q\n", a.WorkspaceName)
	p("  model: %s\n", a.AgentModel)

	p("\nenvironments:\n")
	for _, e := range a.Envs {
		p("  - id: %s\n", e.ID)
		if e.APIDomain != "" {
			p("    api_domain: %s\n", e.APIDomain)
		}
		p("    is_prod: %v\n", e.IsProd)
	}

	if len(a.Repos) > 0 {
		p("\nrepos:\n")
		for _, r := range a.Repos {
			p("  - name: %s\n", r.Name)
			p("    url: %s\n", r.URL)
			p("    role: %s\n", r.Role)
			p("    stack: %s\n", r.Stack)
			if r.Framework != "" {
				p("    framework: %q\n", r.Framework)
			}
			if len(r.ServiceNames) > 0 {
				p("    service_names:\n")
				for _, s := range r.ServiceNames {
					p("      - %s\n", s)
				}
			}
			p("    env_branches:\n")
			for _, env := range a.Envs {
				branch := r.EnvBranches[env.ID]
				if branch == "" {
					branch = "main"
				}
				p("      %s: %s\n", env.ID, branch)
			}
		}
	}

	p("\ninfrastructure:\n")
	p("  config_center:\n")
	if a.ConfigCenterType == "" || a.ConfigCenterType == "none" {
		p("    type: none\n")
	} else {
		p("    type: %s\n", a.ConfigCenterType)
		p("    endpoints:\n")
		for _, e := range a.Envs {
			p("      - env: %s\n", e.ID)
			p("        addr: \"{{CONFIG_CENTER_ADDR_%s}}\"\n", strings.ToUpper(e.ID))
			p("        namespace_hint: %s\n", e.ID)
		}
		p("    auth:\n")
		p("      username_placeholder: \"{{CONFIG_CENTER_USERNAME}}\"\n")
		p("      password_placeholder: \"{{CONFIG_CENTER_PASSWORD}}\"\n")
	}

	p("  observability:\n")
	p("    grafana:\n")
	p("      enabled: %v\n", a.GrafanaEnabled)
	if a.GrafanaEnabled {
		p("      url_by_env:\n")
		for _, e := range a.Envs {
			p("        %s: \"{{GRAFANA_%s_URL}}\"\n", e.ID, strings.ToUpper(e.ID))
		}
		p("      auth:\n")
		p("        username_placeholder: \"{{GRAFANA_USERNAME}}\"\n")
		p("        password_placeholder: \"{{GRAFANA_PASSWORD}}\"\n")
	}
	p("    loki:\n")
	p("      enabled: %v\n", a.LokiEnabled)
	p("      via_grafana: %v\n", a.LokiEnabled && a.GrafanaEnabled)
	p("    prometheus:\n")
	p("      enabled: %v\n", a.PrometheusEnabled)
	p("      via_grafana: %v\n", a.PrometheusEnabled && a.GrafanaEnabled)

	p("  data_stores:\n")
	for _, typ := range []string{"redis", "mongodb", "elasticsearch", "mysql", "kafka"} {
		enabled := a.DataStoresEnabled[typ]
		p("    - type: %s\n", typ)
		p("      enabled: %v\n", enabled)
		if enabled {
			if a.ConfigCenterType != "" && a.ConfigCenterType != "none" {
				p("      discovery: from_config_center\n")
			} else {
				p("      discovery: static\n")
			}
			p("      readonly_enforced: true\n")
		}
	}

	if a.LarkEnabled {
		p("  messaging:\n")
		p("    - platform: lark\n")
		p("      enabled: true\n")
		p("      credentials:\n")
		p("        app_id_placeholder: \"{{MESSAGING_APP_ID}}\"\n")
		p("        app_secret_placeholder: \"{{MESSAGING_APP_SECRET}}\"\n")
		p("      attachment_send: %v\n", a.LarkAttachment)
	} else {
		p("  messaging: []\n")
	}

	if a.FeishuProjectEnabled {
		p("  project_tracking:\n")
		p("    - platform: feishu_project\n")
		p("      enabled: true\n")
		p("      credentials:\n")
		p("        user_token_placeholder: \"{{MCP_USER_TOKEN}}\"\n")
	} else {
		p("  project_tracking: []\n")
	}

	p("\ngeneration:\n")
	p("  target_host: openclaw\n")
	p("  output_dir: %s\n", a.OutputDir)
	p("  skills_whitelist:\n")
	p("    - routing\n")
	if a.ConfigCenterType != "" && a.ConfigCenterType != "none" {
		p("    - config-executor\n")
	}
	for _, typ := range []string{"redis", "mongodb", "elasticsearch"} {
		if a.DataStoresEnabled[typ] {
			alias := typ
			if typ == "elasticsearch" {
				alias = "es"
			}
			p("    - %s-runtime-query\n", alias)
		}
	}
	p("    - diagram-generator\n")
	p("  preserve_on_regenerate:\n")
	p("    - SOUL.md\n")
	p("    - USER.md\n")
	p("    - CHECKLIST.md\n")

	p("\nmeta:\n")
	p("  schema_version: \"0.1\"\n")

	_, err := io.WriteString(out, buf.String())
	return err
}
