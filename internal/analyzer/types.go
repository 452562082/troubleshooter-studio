package analyzer

// ConfigCenter 枚举
const (
	CenterNacos  = "nacos"
	CenterApollo = "apollo"
	CenterConsul = "consul"
)

// Finding 从代码/配置中抽取的配置中心线索（跨 nacos/apollo/consul 的联合类型）
// 具体使用哪些字段由 ConfigCenter 字段决定
type Finding struct {
	ConfigCenter string `json:"config_center"`         // nacos / apollo / consul
	SourceFile   string `json:"source_file"`           // 相对仓库根目录的路径
	EnvProfile   string `json:"env_profile,omitempty"` // spring profile 或文件名提示的环境
	ServerAddr   string `json:"server_addr,omitempty"` // 若配置里硬编码了地址（meta/host）

	// === Nacos ===
	DataID      string `json:"data_id,omitempty"`
	Group       string `json:"group,omitempty"`
	NamespaceID string `json:"namespace_id,omitempty"`

	// === Apollo ===
	AppID      string   `json:"app_id,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
	Cluster    string   `json:"cluster,omitempty"`

	// === Consul ===
	KVPrefix       string `json:"kv_prefix,omitempty"`
	DefaultContext string `json:"default_context,omitempty"`
}

// DownstreamCall 单次下游调用线索 —— "本仓库里这一行代码调用了哪个下游服务"。
// 由 dependency_scan.go 各语言扫码产出,driver 各不相同(Go http.Get / Java @FeignClient / Python requests / ...)。
// 同一 (caller,target) 多次调用会去重(callsite 取第一个)。
type DownstreamCall struct {
	Target   string `json:"target"`             // 推断出来的目标服务名(从 URL host 或 @FeignClient.name 取)
	Driver   string `json:"driver"`             // http / grpc / mongo / redis / kafka / ...
	Callsite string `json:"callsite,omitempty"` // 形如 "internal/service/user.go:42"
	Hint     string `json:"hint,omitempty"`     // 原始字符串(URL / 调用表达式),给人核对用
}

// DataStoreUsage 单次数据层使用线索 —— "本仓库初始化了哪个数据层连接"。
type DataStoreUsage struct {
	Type     string `json:"type"`               // mysql / postgresql / redis / mongodb / elasticsearch / kafka / rocketmq / rabbitmq / clickhouse
	Logical  string `json:"logical,omitempty"`  // 逻辑名(从配置 key / package 名推),如 "order_db" "session-cache"
	Driver   string `json:"driver"`             // 库/驱动(go-redis / pymongo / @Autowired RedisTemplate)
	Callsite string `json:"callsite,omitempty"`
}

// RepoAnalysis 单仓库分析产物
type RepoAnalysis struct {
	Name            string           `json:"name"`
	Stack           string           `json:"stack"`
	RepoPath        string           `json:"repo_path"`
	ServiceNames    []string         `json:"service_names,omitempty"`
	Findings        []Finding        `json:"findings,omitempty"`
	DownstreamCalls []DownstreamCall `json:"downstream_calls,omitempty"`
	DataStoreUsages []DataStoreUsage `json:"data_store_usages,omitempty"`
	Warnings        []string         `json:"warnings,omitempty"`
	Verified        bool             `json:"verified"`
}

// Report analyze 命令的聚合产物
type Report struct {
	SchemaVersion string         `json:"schema_version"`
	ConfigCenter  string         `json:"config_center"` // 整个系统的配置中心类型
	Repos         []RepoAnalysis `json:"repos"`
}

// Analyzer 单个技术栈的分析器。config-center 类型在构造时注入。
type Analyzer interface {
	Stack() string
	Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error)
}
