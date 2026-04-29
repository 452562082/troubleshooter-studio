package config

type System struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type Agent struct {
	// ID 是机器人在各 AI 平台里的稳定标识(OpenClaw `agents.list[*].id` /
	// Claude Code / Cursor subagent 名)。空时由 ResolveID 推导成 "<system.id>-troubleshooter"
	// 兼容老 yaml,保持产物文件名(`<id>-creds.json` / agents.list 等)不破。
	ID            string `yaml:"id,omitempty"`
	Name          string `yaml:"name"`
	WorkspaceName string `yaml:"workspace_name"`
	// Model 是"默认"模型 id(provider/modelID 格式);
	// 只有 openclaw 和 embedded 两个 target 实际消费模型:
	//   - openclaw:写进 install.sh 的 MODEL 默认值,gateway 读它路由
	//   - embedded:Studio 内嵌对话直连这个 provider / model
	// claude-code / cursor 不消费 —— 用户在这两家客户端自己挑模型,本字段仅作文档
	Model string `yaml:"model"`
	// TargetModels 为 target 级别的模型覆盖(可选)。同时勾 openclaw + embedded
	// 但想给它俩用不同 provider / 不同模型时用:
	//   target_models:
	//     openclaw: anthropic/claude-sonnet-4-6
	//     embedded: deepseek/deepseek-chat
	// 没指定时该 target 回落到上面的 agent.model。
	// claude-code / cursor 写在这里也会被忽略(那俩 target 不消费模型)。
	TargetModels map[string]string `yaml:"target_models,omitempty"`
}

// ResolveID 返回 agent 在各 AI 平台的最终 ID:
//   - 优先 yaml 显式指定的 agent.id
//   - 空时回落到 agent.workspace_name(兼容老 yaml:那时候 workspace_name 同时承担"目录名"
//     和"AI 平台标识"两职,典型值 shop-bot)
//   - 再空回落到 "<system.id>-troubleshooter"(全新 wizard 默认命名)
//
// 这条链让"删了 workspace_name 字段、改用 agent.id"的迁移对老 yaml 透明:
// shop-system.yaml(workspace_name=shop-bot, 无 agent.id)→ ID 仍 = shop-bot,
// 不会突然变成 shop-troubleshooter 让旧的 ~/.claude/agents/shop-bot.md 成孤儿。
func (s *SystemConfig) ResolveID() string {
	if id := s.Agent.ID; id != "" {
		return id
	}
	if w := s.Agent.WorkspaceName; w != "" {
		return w
	}
	return s.System.ID + "-troubleshooter"
}

// ResolveWorkspaceName 返回 OpenClaw workspace 目录名(~/.openclaw/workspace/<这里>):
//   - 优先 yaml 显式指定的 agent.workspace_name(老 yaml 兼容)
//   - 空时跟 ResolveID() 共享同一标识(新 wizard 不再单独 emit workspace_name)
func (s *SystemConfig) ResolveWorkspaceName() string {
	if w := s.Agent.WorkspaceName; w != "" {
		return w
	}
	return s.ResolveID()
}

// ModelForTarget 给 target-aware 消费点(install.sh 模板 / llmchat)提供的便捷访问:
// 优先 target_models[target],回落到 agent.model。target 为空串时直接返回 agent.model。
func (a Agent) ModelForTarget(target string) string {
	if target != "" {
		if m, ok := a.TargetModels[target]; ok && m != "" {
			return m
		}
	}
	return a.Model
}

type Environment struct {
	ID        string   `yaml:"id"`
	Aliases   []string `yaml:"aliases"`
	APIDomain string   `yaml:"api_domain"`
	WebDomain string   `yaml:"web_domain"`
	IsProd    bool     `yaml:"is_prod"`
}

type RepoAnalysis struct {
	Enabled      bool     `yaml:"enabled"`
	ShallowDepth int      `yaml:"shallow_depth"`
	IncludePaths []string `yaml:"include_paths"`
}

type Repo struct {
	Name         string            `yaml:"name"`
	URL          string            `yaml:"url"`
	Stack        string            `yaml:"stack"`
	Framework    string            `yaml:"framework"`
	ServiceNames []string          `yaml:"service_names"`
	EnvBranches  map[string]string `yaml:"env_branches"`
	Analysis     RepoAnalysis      `yaml:"analysis"`
	// ConfigSource 引用 infrastructure.config_centers[].id,标明本仓库的配置走哪个源。
	// 多源场景必填(运行时 routing skill 据此选 MCP);空时 LoadFromBytes 自动绑到
	// config_centers[0],兼容只有一个源的老 yaml。
	ConfigSource string `yaml:"config_source,omitempty"`
}

type ConfigCenterEndpoint struct {
	Env           string `yaml:"env"`
	Addr          string `yaml:"addr"`
	NamespaceHint string `yaml:"namespace_hint"`
}

type CredentialAuth struct {
	UsernamePlaceholder string `yaml:"username_placeholder"`
	PasswordPlaceholder string `yaml:"password_placeholder"`
}

type ConfigCenter struct {
	// ID:多源场景下的唯一标识(repos[].config_source 引用它)。空时 LoadFromBytes
	// 会按 type 自动派生(单源迁移路径)。要求 ASCII kebab-case,跟 system.id 同规则。
	ID             string                 `yaml:"id,omitempty"`
	Type           string                 `yaml:"type"`
	Endpoints      []ConfigCenterEndpoint `yaml:"endpoints"`
	Auth           CredentialAuth         `yaml:"auth"`
	DataIDPatterns []string               `yaml:"dataid_patterns"`
	// ServiceMap 是向导 Step 5 用户通过下拉挑出来的"每个环境每个服务对应哪条配置"
	// 外层 key = env.id,内层 key = service 名称(跟 repos[].service_names 对齐)。
	// 生成时作为 prior override 注入到 config-map.yaml —— 优先级: analyzer finding > ServiceMap > inferred。
	ServiceMap map[string]map[string]ServiceMapEntry `yaml:"service_map,omitempty"`
}

// ServiceMapEntry 一条 (env, service) → 配置中心定位的映射(nacos / apollo / consul / kuboard 共用字段,不相关留空)。
type ServiceMapEntry struct {
	// Namespace = nacos namespaceId / apollo envId / consul kv prefix / kuboard k8s namespace
	Namespace string `yaml:"namespace,omitempty"`
	Group     string `yaml:"group,omitempty"`   // nacos 独有
	DataID    string `yaml:"data_id,omitempty"` // nacos dataId / consul full kv key
	AppID     string `yaml:"app_id,omitempty"`  // apollo 独有
	Cluster   string `yaml:"cluster,omitempty"` // kuboard 独有:k8s 集群名
	ConfigMap string `yaml:"configmap,omitempty"` // kuboard 独有:k8s ConfigMap 名
}

type Grafana struct {
	Enabled  bool              `yaml:"enabled"`
	URLByEnv map[string]string `yaml:"url_by_env"`
	Auth     CredentialAuth    `yaml:"auth"`
}

type Loki struct {
	Enabled            bool              `yaml:"enabled"`
	ViaGrafana         bool              `yaml:"via_grafana"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"` // 走 Grafana 代理时本 env 用哪个 ds
}

type Prometheus struct {
	Enabled            bool              `yaml:"enabled"`
	ViaGrafana         bool              `yaml:"via_grafana"`
	PreferredMetrics   []string          `yaml:"preferred_metrics"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
}

type Jaeger struct {
	Enabled            bool              `yaml:"enabled"`
	URLByEnv           map[string]string `yaml:"url_by_env"` // env → Jaeger UI URL
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
}

type ELK struct {
	Enabled            bool              `yaml:"enabled"`
	KibanaByEnv        map[string]string `yaml:"kibana_by_env"` // env → Kibana URL
	ESByEnv            map[string]string `yaml:"es_by_env"`     // env → Elasticsearch URL（直查）
	DefaultIndex       string            `yaml:"default_index"` // 默认日志索引 pattern
	Auth               CredentialAuth    `yaml:"auth"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
}

type SkyWalking struct {
	Enabled  bool              `yaml:"enabled"`
	URLByEnv map[string]string `yaml:"url_by_env"` // env → SkyWalking OAP GraphQL URL
}

type Tempo struct {
	Enabled            bool              `yaml:"enabled"`
	URLByEnv           map[string]string `yaml:"url_by_env"` // env → Tempo API URL
	ViaGrafana         bool              `yaml:"via_grafana"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
}

// K8sRuntime 走 Kuboard v4 API 查 pod / events / 日志 / deployment 状态。
// 跟配置源 kuboard 的连接信息可以重合(同一个 Kuboard 实例),但开关独立 ——
// 用户也可能从 nacos 读配置、同时只用 Kuboard 查运行时。
type K8sRuntime struct {
	Enabled    bool                       `yaml:"enabled"`
	URLByEnv   map[string]string          `yaml:"url_by_env"` // env → Kuboard URL
	Auth       CredentialAuth             `yaml:"auth"`
	ServiceMap []K8sRuntimeServiceMapEntry `yaml:"service_map,omitempty"` // (env, service) → cluster/ns/workload/selector
}

// K8sRuntimeServiceMapEntry 让 routing skill 把"env + 服务名"解析到 K8s 上的具体定位。
// workload 与 label_selector 至少有一个;routing 优先用 label_selector,没有就退到 workload 名匹配。
type K8sRuntimeServiceMapEntry struct {
	Env           string `yaml:"env"`
	Service       string `yaml:"service"`
	Cluster       string `yaml:"cluster"`
	Namespace     string `yaml:"namespace"`
	Workload      string `yaml:"workload,omitempty"`
	LabelSelector string `yaml:"label_selector,omitempty"`
}

type Observability struct {
	Grafana    Grafana    `yaml:"grafana"`
	Loki       Loki       `yaml:"loki"`
	Prometheus Prometheus `yaml:"prometheus"`
	Jaeger     Jaeger     `yaml:"jaeger"`
	ELK        ELK        `yaml:"elk"`
	SkyWalking SkyWalking `yaml:"skywalking"`
	Tempo      Tempo      `yaml:"tempo"`
	K8sRuntime K8sRuntime `yaml:"k8s_runtime"`
}

type DataStore struct {
	Type             string            `yaml:"type"`
	Enabled          bool              `yaml:"enabled"`
	Discovery        string            `yaml:"discovery"` // from_config_center / static / both
	ReadonlyEnforced bool              `yaml:"readonly_enforced"`
	StaticEndpoints  []string          `yaml:"static_endpoints"` // legacy flat list
	EndpointsByEnv   map[string]string `yaml:"endpoints_by_env"` // env → "host:port" or "uri"
}

type MessagingCredentials struct {
	AppIDPlaceholder     string `yaml:"app_id_placeholder"`
	AppSecretPlaceholder string `yaml:"app_secret_placeholder"`
}

type Messaging struct {
	Platform       string               `yaml:"platform"`
	Enabled        bool                 `yaml:"enabled"`
	Credentials    MessagingCredentials `yaml:"credentials"`
	AttachmentSend bool                 `yaml:"attachment_send"`
}

type ProjectTrackingCredentials struct {
	UserTokenPlaceholder string `yaml:"user_token_placeholder"`
}

type ProjectTracking struct {
	Platform    string                     `yaml:"platform"`
	Enabled     bool                       `yaml:"enabled"`
	Credentials ProjectTrackingCredentials `yaml:"credentials"`
}

type Infrastructure struct {
	// ConfigCenters:多源配置(每个源独立 type/endpoints/凭证)。同一系统里不同
	// 服务可能有的在 nacos 有的在 k8s,通过 repos[].config_source 选源。
	// 老 yaml 只有 ConfigCenter(单数)时,LoadFromBytes 自动迁到此处第一个元素。
	ConfigCenters []ConfigCenter `yaml:"config_centers,omitempty"`
	// ConfigCenter:老字段,单源场景。LoadFromBytes 之后会被清空(已迁到 ConfigCenters)。
	// 写回 yaml 时优先 ConfigCenters,本字段 omitempty。
	ConfigCenter    ConfigCenter      `yaml:"config_center,omitempty"`
	Observability   Observability     `yaml:"observability"`
	DataStores      []DataStore       `yaml:"data_stores"`
	Messaging       []Messaging       `yaml:"messaging"`
	ProjectTracking []ProjectTracking `yaml:"project_tracking"`
}

// PrimaryConfigCenter 给老代码用的兼容访问点:
//   - 多源(len>1):返回第一个(legacy 路径,UI 应迁到 ConfigCenters[i])
//   - 单源(len==1):返回那个
//   - 空:返回零值(Type==""),调用方判 Type 走"无配置中心"分支
//
// 注:本方法**不应**用于多源感知逻辑(如 prompts 派生、MCP 注入),
// 那些代码必须遍历 ConfigCenters。仅供尚未多源化的代码做最小改动。
func (i Infrastructure) PrimaryConfigCenter() ConfigCenter {
	if len(i.ConfigCenters) > 0 {
		return i.ConfigCenters[0]
	}
	return i.ConfigCenter
}

type Generation struct {
	// TargetHost 是 v0.x 的单目标遗留字段,新 yaml 用 Targets 数组替代。
	// 仅保留读路径(ResolvedTargets() 兜底);新生成的 yaml 不再写出这个字段。
	TargetHost           string   `yaml:"target_host,omitempty"`
	Targets              []string `yaml:"targets"` // 多目标：openclaw / claude-code / ...
	SkillsWhitelist      []string `yaml:"skills_whitelist"`
	PreserveOnRegenerate []string `yaml:"preserve_on_regenerate"`
}

// ResolvedTargets 返回最终目标列表（兼容 target_host 单值和 targets 数组）
func (g Generation) ResolvedTargets() []string {
	if len(g.Targets) > 0 {
		return g.Targets
	}
	if g.TargetHost != "" {
		return []string{g.TargetHost}
	}
	return []string{"openclaw"}
}

type TshootTemplateRef struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

type Meta struct {
	SchemaVersion     string            `yaml:"schema_version"`
	TshootTemplateRef TshootTemplateRef `yaml:"tshoot_template_ref"`
}

type SystemConfig struct {
	System         System         `yaml:"system"`
	Agent          Agent          `yaml:"agent"`
	Environments   []Environment  `yaml:"environments"`
	Repos          []Repo         `yaml:"repos"`
	Infrastructure Infrastructure `yaml:"infrastructure"`
	Generation     Generation     `yaml:"generation"`
	Meta           Meta           `yaml:"meta"`
}
