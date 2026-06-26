package config

// ObsEndpoint 是 GUI wizard 写出来的 per-env 端点(grafana/loki/prom/jaeger/elk/sw/tempo/k8s_runtime
// 共用一份字段超集)。每个 obs 组件实际用其中的一个子集:URL 必有,鉴权字段按组件类型挑用。
//
// 解析后由 migrateObservabilityEndpoints 走一遍,把 URL / Kibana / ES / DSUID 抽到对应的
// `*_by_env` map(老 schema)。Endpoints 自身保留(往后真要 per-env 凭证时还能读),
// 但模板渲染目前只看 *_by_env map。
type ObsEndpoint struct {
	Env string `yaml:"env"`
	URL string `yaml:"url,omitempty"`

	// Grafana / ELK 通用账密
	User string `yaml:"user,omitempty"`
	Pass string `yaml:"pass,omitempty"`
	// Grafana API Key 鉴权
	APIKey string `yaml:"api_key,omitempty"`

	// ELK 专属:Kibana 与 Elasticsearch 直连
	KibanaURL string `yaml:"kibana_url,omitempty"`
	ESURL     string `yaml:"es_url,omitempty"`

	// K8s Runtime (Kuboard) 专属:API 凭证 / 用户名密码
	AccessKey string `yaml:"access_key,omitempty"`
	Username  string `yaml:"username,omitempty"`
	Password  string `yaml:"password,omitempty"`
}

type Grafana struct {
	Enabled  bool              `yaml:"enabled"`
	URLByEnv map[string]string `yaml:"url_by_env,omitempty"`
	Auth     CredentialAuth    `yaml:"auth"`
	// Endpoints 是 GUI wizard 的 per-env 端点输出形式(含凭证)。loader 负责把
	// 它的 url 抽到 URLByEnv,模板暂时仍读 URLByEnv 保持兼容。
	Endpoints []ObsEndpoint `yaml:"endpoints,omitempty"`
}

// LokiLabelMappingPerEnv 对应 wizard 输出的 loki.label_mapping_by_env.<env> 整块,
// 给 routing skill 在运行时拼 LogQL 用。loader 还会把 grafana_ds_uid 字段抽到
// Loki.DatasourceUIDByEnv,确保走 Grafana 代理的 selector_chain 拿得到 ds uid。
type LokiLabelMappingPerEnv struct {
	EnvLabel     string                       `yaml:"env_label,omitempty"`
	ServiceLabel string                       `yaml:"service_label,omitempty"`
	GrafanaDSUID string                       `yaml:"grafana_ds_uid,omitempty"`
	Namespace    string                       `yaml:"namespace,omitempty"`
	ServiceMap   map[string]map[string]string `yaml:"service_map,omitempty"`
}

type Loki struct {
	Enabled            bool                              `yaml:"enabled"`
	ViaGrafana         bool                              `yaml:"via_grafana"`
	DatasourceUIDByEnv map[string]string                 `yaml:"datasource_uid_by_env,omitempty"` // 走 Grafana 代理时本 env 用哪个 ds
	Endpoints          []ObsEndpoint                     `yaml:"endpoints,omitempty"`
	LabelMappingByEnv  map[string]LokiLabelMappingPerEnv `yaml:"label_mapping_by_env,omitempty"`
}

type Prometheus struct {
	Enabled            bool              `yaml:"enabled"`
	ViaGrafana         bool              `yaml:"via_grafana"`
	PreferredMetrics   []string          `yaml:"preferred_metrics"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
	Endpoints          []ObsEndpoint     `yaml:"endpoints,omitempty"`
}

type Jaeger struct {
	Enabled            bool              `yaml:"enabled"`
	URLByEnv           map[string]string `yaml:"url_by_env,omitempty"` // env → Jaeger UI URL
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
	Endpoints          []ObsEndpoint     `yaml:"endpoints,omitempty"`
	// SamplingRate:Jaeger 头采样率(0.0-1.0)。agent 看到"trace_id 找不到"时按这个判断
	// "采样率 X%,大量 trace 没采到很正常,从日志找 trace_id="还是"trace 真不存在"。
	// 0 视为未设置(模板侧 fallback 0.1 = 10%);常见值:1.0 全采 / 0.1 头采样 / 0.01 重负载。
	SamplingRate float64 `yaml:"sampling_rate,omitempty"`
}

type ELK struct {
	Enabled            bool              `yaml:"enabled"`
	KibanaByEnv        map[string]string `yaml:"kibana_by_env,omitempty"` // env → Kibana URL
	ESByEnv            map[string]string `yaml:"es_by_env,omitempty"`     // env → Elasticsearch URL(直查)
	DefaultIndex       string            `yaml:"default_index"`           // 默认日志索引 pattern
	Auth               CredentialAuth    `yaml:"auth"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
	Endpoints          []ObsEndpoint     `yaml:"endpoints,omitempty"`
}

type SkyWalking struct {
	Enabled   bool              `yaml:"enabled"`
	URLByEnv  map[string]string `yaml:"url_by_env,omitempty"` // env → SkyWalking OAP GraphQL URL
	Endpoints []ObsEndpoint     `yaml:"endpoints,omitempty"`
}

type Tempo struct {
	Enabled            bool              `yaml:"enabled"`
	URLByEnv           map[string]string `yaml:"url_by_env,omitempty"` // env → Tempo API URL
	ViaGrafana         bool              `yaml:"via_grafana"`
	DatasourceUIDByEnv map[string]string `yaml:"datasource_uid_by_env,omitempty"`
	Endpoints          []ObsEndpoint     `yaml:"endpoints,omitempty"`
}

// K8sRuntime 查 pod / events / 日志 / deployment 运行时状态。
// Provider 决定后端:"kuboard"(默认,走 Kuboard v4 HTTP API + k8s_query.py 脚本)或
// "one2all"(走 one2all-remote MCP server 工具)。不填按 kuboard 处理。
// 跟配置源 kuboard/one2all 的连接信息可以重合(同一个实例),但开关独立 ——
// 用户也可能从 nacos 读配置、同时只用 Kuboard/one2all 查运行时。
type K8sRuntime struct {
	Enabled    bool                        `yaml:"enabled"`
	Provider   string                      `yaml:"provider,omitempty"`   // "kuboard"(默认)或 "one2all"
	URLByEnv   map[string]string           `yaml:"url_by_env,omitempty"` // env → Kuboard URL(kuboard provider 用)
	Auth       CredentialAuth              `yaml:"auth"`
	Endpoints  []ObsEndpoint               `yaml:"endpoints,omitempty"`
	ServiceMap []K8sRuntimeServiceMapEntry `yaml:"service_map,omitempty"` // (env, service) → cluster/ns/workload/selector
}

// K8sRuntimeServiceMapEntry 让 routing skill 把"env + 服务名"解析到 K8s 上的具体定位。
// workload 与 label_selector 至少有一个;routing 优先用 label_selector,没有就退到 workload 名匹配。
type K8sRuntimeServiceMapEntry struct {
	Env           string `yaml:"env"`
	Service       string `yaml:"service"`
	Cluster       string `yaml:"cluster"`
	ClusterID     string `yaml:"cluster_id,omitempty"` // one2all 用 cluster_id 数字
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
