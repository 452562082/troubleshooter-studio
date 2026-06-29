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

	DataStoresEnabled map[string]bool // redis/mongodb/elasticsearch/mysql/doris/kafka → bool

	LarkEnabled    bool
	LarkAttachment bool

	FeishuProjectEnabled bool

	// Targets 是 generation.targets 的显式列表：openclaw / claude-code / cursor / embedded
	// 空表示 fallback 到 [openclaw]
	Targets []string
}

// WriteYAML 手写 YAML，避免引入 yaml.Encoder 的字段顺序与 omitempty 复杂度
// 输出结构与 examples/*-troubleshooter.yaml 保持一致
func (a *Answers) WriteYAML(out io.Writer) error {
	buf := &strings.Builder{}
	p := func(format string, args ...any) { fmt.Fprintf(buf, format, args...) }

	p("# 由 tshoot init 生成，可手工调整。字段说明：schema/troubleshooter.schema.yaml\n")
	p("# 以下行尾 # 注释仅为提示，YAML 解析时会被忽略。\n")
	p("system:\n")
	p("  id: %s                    # 机器可读标识，仅 [a-z0-9-]；用作 output_dir / agent id 前缀\n", a.SystemID)
	p("  name: %q          # 用户可见名称（中/英均可）\n", a.SystemName)
	if a.SystemDescription != "" {
		p("  description: %q\n", a.SystemDescription)
	}
	p("\nagent:\n")
	p("  name: %q\n", a.AgentName)
	p("  workspace_name: %q    # OpenClaw 工作区目录名（~/.openclaw/workspace/<这里>）\n", a.WorkspaceName)
	p("  model: %s              # LLM model id;前缀决定 provider(anthropic/openai/deepseek/qwen/minimax/moonshot/zhipu/ollama)\n", a.AgentModel)

	p("\n# environments：声明系统的所有环境。每个 env 会注册一套独立的 MCP 实例\n")
	p("# （如 nacos-mcp-server-dev / -prod），机器人按 is_prod 调整谨慎度。\n")
	p("environments:\n")
	for _, e := range a.Envs {
		p("  - id: %s\n", e.ID)
		if e.APIDomain != "" {
			p("    api_domain: %s     # 本 env 的对外访问域名，用于接口实测\n", e.APIDomain)
		}
		p("    is_prod: %v         # 生产环境标记：true 时机器人默认更保守、查询前二次确认\n", e.IsProd)
	}

	if len(a.Repos) > 0 {
		p("\n# repos：所有纳入排障范围的代码仓库。role/stack 决定 analyzer 与 skill 策略。\n")
		p("repos:\n")
		for _, r := range a.Repos {
			p("  - name: %s\n", r.Name)
			p("    url: %s\n", r.URL)
			p("    stack: %s             # go/java/node/php/python，决定用哪种配置扫描器\n", r.Stack)
			if r.Framework != "" {
				p("    framework: %q\n", r.Framework)
			}
			if len(r.ServiceNames) > 0 {
				p("    service_names:      # 本 repo 实际部署出来的 service 名（config-map 以此为 key）\n")
				for _, s := range r.ServiceNames {
					p("      - %s\n", s)
				}
			}
			p("    env_branches:       # 每个 env 对应的长期分支；routing skill 据此切换代码\n")
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
	p("  config_center:          # 配置中心：nacos/apollo/consul/env-vars/kuboard/one2all/none\n")
	switch a.ConfigCenterType {
	case "", "none":
		p("    type: none            # 无配置中心；机器人不会尝试解析配置连接串\n")
	case "one2all":
		p("    type: one2all         # 走 one2all-remote MCP server(streamable-http),读 ConfigMap/Secret\n")
		p("    # 单一 MCP 实例,不分 env;凭据由 install 阶段写入 MCP headers\n")
		p("    endpoints:\n")
		p("      - url: \"{{ONE2ALL_MCP_URL}}\"    # MCP server 完整 URL(含路径 hash)\n")
	default:
		p("    type: %s\n", a.ConfigCenterType)
		p("    endpoints:            # 每 env 的配置中心地址（{{...}} 会在 install.sh 交互时替换）\n")
		for _, e := range a.Envs {
			p("      - env: %s\n", e.ID)
			p("        addr: \"{{CONFIG_CENTER_ADDR_%s}}\"\n", strings.ToUpper(e.ID))
			p("        namespace_hint: %s\n", e.ID)
		}
		p("    auth:                 # 用户名/密码占位，install.sh 会让用户输入并写入 MCP env\n")
		p("      username_placeholder: \"{{CONFIG_CENTER_USERNAME}}\"\n")
		p("      password_placeholder: \"{{CONFIG_CENTER_PASSWORD}}\"\n")
	}

	p("  observability:          # 可观测性组件；Loki/Prometheus 若 via_grafana=true 则复用 Grafana MCP\n")
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
	p("      via_grafana: %v   # true = 通过 Grafana MCP 的 LogQL，避免再装 loki-mcp\n", a.LokiEnabled && a.GrafanaEnabled)
	p("    prometheus:\n")
	p("      enabled: %v\n", a.PrometheusEnabled)
	p("      via_grafana: %v\n", a.PrometheusEnabled && a.GrafanaEnabled)

	p("  data_stores:            # 启用的数据层：每个会生成对应 runtime-query skill（只读）\n")
	for _, typ := range []string{"redis", "mongodb", "elasticsearch", "mysql", "doris", "kafka"} {
		enabled := a.DataStoresEnabled[typ]
		p("    - type: %s\n", typ)
		p("      enabled: %v\n", enabled)
		if enabled {
			if a.ConfigCenterType != "" && a.ConfigCenterType != "none" {
				p("      discovery: from_config_center   # 运行时通过配置中心拿连接串\n")
			} else {
				p("      discovery: static               # install.sh 交互时直接收集连接串\n")
			}
			p("      readonly_enforced: true           # 强制只读；generator 拒绝写操作\n")
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
		// 2026-05-15 B 方案:即便用户在 wizard 答 Y,也渲染 enabled: false + 注释提示。
		// 理由:@lark-project/mcp v0.0.1 是字节内部 prototype,mcp 没接入(buildFeishuProject
		// warn skip)且无 SKILL / Python OpenAPI 脚本,enabled: true 没意义。yaml schema
		// 保留 platform=feishu_project 作合法值,等字节发正式版 + 我们补完 SKILL 后改回 true。
		p("  project_tracking:\n")
		p("    # ⚠ 实验性:feishu_project mcp 暂未启用,SKILL 也未接入。yaml 保留 platform 占位\n")
		p("    # 等字节发 @lark-project/mcp 正式版 + 我们补完 SKILL 后改 enabled: true。详见:\n")
		p("    # internal/agent/install_native_mcp_common.go::buildFeishuProject 大段注释。\n")
		p("    - platform: feishu_project\n")
		p("      enabled: false\n")
	} else {
		p("  project_tracking: []\n")
	}

	p("\ngeneration:\n")
	// output_dir 故意不写:CLI `tshoot gen` 才会读它,默认 ./dist;wizard 用户不需要,
	// 真要 CLI 跑 gen 时手动加这一行覆盖默认即可。
	targets := a.Targets
	if len(targets) == 0 {
		targets = []string{"openclaw"}
	}
	p("  targets:                             # 每个 target 产出一份机器人产物（同一份 troubleshooter.yaml）\n")
	for _, t := range targets {
		p("    - %s\n", t)
	}
	p("  skills_whitelist:                    # 只有列出的 skill 会进工作区；未列入的模板不会渲染\n")
	p("    - routing\n")
	if a.ConfigCenterType != "" && a.ConfigCenterType != "none" {
		p("    - config-executor\n")
	}
	for _, typ := range []string{"redis", "mongodb", "elasticsearch", "mysql", "doris", "kafka"} {
		if a.DataStoresEnabled[typ] {
			alias := typ
			if typ == "elasticsearch" {
				alias = "es"
			}
			p("    - %s-runtime-query\n", alias)
		}
	}
	p("    - diagram-generator\n")

	p("\nmeta:\n")
	p("  schema_version: \"0.1\"\n")

	_, err := io.WriteString(out, buf.String())
	return err
}
