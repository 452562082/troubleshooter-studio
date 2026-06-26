package config

type ConfigCenterEndpoint struct {
	Env           string `yaml:"env"`
	Addr          string `yaml:"addr,omitempty"`
	NamespaceHint string `yaml:"namespace_hint,omitempty"`

	// GUI wizard 写出来的 per-env URL + 凭证(每个 type 用其中一个子集):
	//   nacos:    addr + user + pass
	//   apollo:   meta_url + token
	//   consul:   host + token
	//   kuboard:  url + (access_key | user + pass)
	// install 阶段从这些字段抽出来当 .env 默认值,免得用户在 GUI 表单里再填一遍。
	URL       string `yaml:"url,omitempty"`
	User      string `yaml:"user,omitempty"`
	Pass      string `yaml:"pass,omitempty"`
	Token     string `yaml:"token,omitempty"`
	MetaURL   string `yaml:"meta_url,omitempty"`
	Host      string `yaml:"host,omitempty"`
	AccessKey string `yaml:"access_key,omitempty"`
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
	// TeamID / ProjectID:one2all MCP 工具调用所需的团队和项目标识(非 one2all 类型留空)。
	TeamID    string `yaml:"team_id,omitempty"`
	ProjectID string `yaml:"project_id,omitempty"`
	// ServiceMap 是向导 Step 5 用户通过下拉挑出来的"每个环境每个服务对应哪条配置"
	// 外层 key = env.id,内层 key = service 名称(跟 repos[].service_names 对齐)。
	// 生成时作为 prior override 注入到 config-map.yaml —— 优先级: analyzer finding > ServiceMap > inferred。
	ServiceMap map[string]map[string]ServiceMapEntry `yaml:"service_map,omitempty"`
}

// ServiceMapEntry 一条 (env, service) → 配置中心定位的映射(nacos / apollo / consul / kuboard / one2all 共用字段,不相关留空)。
type ServiceMapEntry struct {
	// Namespace = nacos namespaceId / apollo envId / consul kv prefix / kuboard k8s namespace
	Namespace string `yaml:"namespace,omitempty"`
	Group     string `yaml:"group,omitempty"`     // nacos 独有
	DataID    string `yaml:"data_id,omitempty"`   // nacos dataId / consul full kv key
	AppID     string `yaml:"app_id,omitempty"`    // apollo 独有
	Cluster   string `yaml:"cluster,omitempty"`   // kuboard 独有:k8s 集群名
	ClusterID string `yaml:"cluster_id,omitempty"` // one2all 独有:k8s 集群 ID(数字)
	ConfigMap string `yaml:"configmap,omitempty"` // kuboard/one2all:k8s ConfigMap 名
}
