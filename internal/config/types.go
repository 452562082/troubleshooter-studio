package config

type System struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type AgentStyle struct {
	Tone      string `yaml:"tone"`
	Verbosity string `yaml:"verbosity"`
}

type Agent struct {
	Name          string     `yaml:"name"`
	WorkspaceName string     `yaml:"workspace_name"`
	Model         string     `yaml:"model"`
	Style         AgentStyle `yaml:"style"`
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
	Role         string            `yaml:"role"`
	Stack        string            `yaml:"stack"`
	Framework    string            `yaml:"framework"`
	ServiceNames []string          `yaml:"service_names"`
	EnvBranches  map[string]string `yaml:"env_branches"`
	Analysis     RepoAnalysis      `yaml:"analysis"`
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
	Type              string                 `yaml:"type"`
	Endpoints         []ConfigCenterEndpoint `yaml:"endpoints"`
	Auth              CredentialAuth         `yaml:"auth"`
	DataIDPatterns    []string               `yaml:"dataid_patterns"`
	PerEnvCredentials bool                   `yaml:"per_env_credentials"` // true = install.sh 按 env 单独问凭证
}

type Grafana struct {
	Enabled           bool              `yaml:"enabled"`
	URLByEnv          map[string]string `yaml:"url_by_env"`
	Auth              CredentialAuth    `yaml:"auth"`
	PerEnvCredentials bool              `yaml:"per_env_credentials"` // true = install.sh 按 env 单独问凭证
}

type Loki struct {
	Enabled    bool `yaml:"enabled"`
	ViaGrafana bool `yaml:"via_grafana"`
}

type Prometheus struct {
	Enabled          bool     `yaml:"enabled"`
	ViaGrafana       bool     `yaml:"via_grafana"`
	PreferredMetrics []string `yaml:"preferred_metrics"`
}

type Jaeger struct {
	Enabled  bool              `yaml:"enabled"`
	URLByEnv map[string]string `yaml:"url_by_env"` // env → Jaeger UI URL
}

type ELK struct {
	Enabled      bool              `yaml:"enabled"`
	KibanaByEnv  map[string]string `yaml:"kibana_by_env"` // env → Kibana URL
	ESByEnv      map[string]string `yaml:"es_by_env"`     // env → Elasticsearch URL（直查）
	DefaultIndex string            `yaml:"default_index"` // 默认日志索引 pattern
	Auth         CredentialAuth    `yaml:"auth"`
}

type SkyWalking struct {
	Enabled  bool              `yaml:"enabled"`
	URLByEnv map[string]string `yaml:"url_by_env"` // env → SkyWalking OAP GraphQL URL
}

type Tempo struct {
	Enabled    bool              `yaml:"enabled"`
	URLByEnv   map[string]string `yaml:"url_by_env"` // env → Tempo API URL
	ViaGrafana bool              `yaml:"via_grafana"`
}

type Observability struct {
	Grafana    Grafana    `yaml:"grafana"`
	Loki       Loki       `yaml:"loki"`
	Prometheus Prometheus `yaml:"prometheus"`
	Jaeger     Jaeger     `yaml:"jaeger"`
	ELK        ELK        `yaml:"elk"`
	SkyWalking SkyWalking `yaml:"skywalking"`
	Tempo      Tempo      `yaml:"tempo"`
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
	ConfigCenter    ConfigCenter      `yaml:"config_center"`
	Observability   Observability     `yaml:"observability"`
	DataStores      []DataStore       `yaml:"data_stores"`
	Messaging       []Messaging       `yaml:"messaging"`
	ProjectTracking []ProjectTracking `yaml:"project_tracking"`
}

type Generation struct {
	TargetHost           string   `yaml:"target_host"` // 向后兼容单目标
	Targets              []string `yaml:"targets"`     // 多目标：openclaw / claude-code / ...
	OutputDir            string   `yaml:"output_dir"`
	SkillsWhitelist      []string `yaml:"skills_whitelist"`
	VerifiedOnly         bool     `yaml:"verified_only"`
	MappingReviewMode    string   `yaml:"mapping_review_mode"`
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

type FactoryTemplateRef struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

type Meta struct {
	SchemaVersion      string             `yaml:"schema_version"`
	FactoryTemplateRef FactoryTemplateRef `yaml:"factory_template_ref"`
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
