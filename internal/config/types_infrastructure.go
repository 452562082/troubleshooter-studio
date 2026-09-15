package config

// DataStoreEndpoint 一条 (env, service) → 真实连接串。GUI wizard 写出来的形式。
// 字段是各 type 的并集:redis/es 用 URL,mongodb 用 URI,mysql/doris 用 DSN,kafka 用 Brokers。
// generator 用这份数据反推 service-dependency-map 的 data_stores 字段(谁用了哪些数据层),
// 不再让用户手填。
type DataStoreEndpoint struct {
	Env     string `yaml:"env"`
	Service string `yaml:"service,omitempty"`
	URL     string `yaml:"url,omitempty"`     // redis / elasticsearch / clickhouse
	URI     string `yaml:"uri,omitempty"`     // mongodb
	DSN     string `yaml:"dsn,omitempty"`     // mysql / doris / postgresql
	Brokers string `yaml:"brokers,omitempty"` // kafka
	User    string `yaml:"user,omitempty"`
	Pass    string `yaml:"pass,omitempty"`
}

type DataStore struct {
	// ID is the stable instance identity referenced by resource_catalog. It is
	// optional in legacy YAML; migration derives type, type-2, ... as needed.
	ID               string              `yaml:"id,omitempty"`
	Type             string              `yaml:"type"`
	Enabled          bool                `yaml:"enabled"`
	Discovery        string              `yaml:"discovery,omitempty"` // from_config_center / static / both
	ReadonlyEnforced bool                `yaml:"readonly_enforced"`
	StaticEndpoints  []string            `yaml:"static_endpoints,omitempty"` // legacy flat list
	EndpointsByEnv   map[string]string   `yaml:"endpoints_by_env,omitempty"` // env → "host:port" or "uri"
	Endpoints        []DataStoreEndpoint `yaml:"endpoints,omitempty"`        // GUI 新 schema:per (env, service) 详情
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
