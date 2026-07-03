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

type APIRoute struct {
	Path     string `json:"path"`
	Method   string `json:"method,omitempty"`
	Source   string `json:"source,omitempty"`
	Line     int    `json:"line,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Strength string `json:"strength,omitempty"`
}

// DataStoreUsage 单次数据层使用线索 —— "本仓库初始化了哪个数据层连接"。
type DataStoreUsage struct {
	Type     string `json:"type"`              // mysql / doris / postgresql / redis / mongodb / elasticsearch / kafka / rabbitmq / clickhouse
	Logical  string `json:"logical,omitempty"` // 逻辑名(从配置 key / package 名推),如 "order_db" "session-cache"
	Driver   string `json:"driver"`            // 库/驱动(go-redis / pymongo / @Autowired RedisTemplate)
	Callsite string `json:"callsite,omitempty"`
}

// SchemaTable 单条业务表 / collection / 缓存 prefix 命中——"本仓库代码 / 注解 / SQL 字面量
// 提到了哪个表"。schema_scan.go 各语言多策略扫产出。给 routing data-schema-map.yaml 用,
// agent 看到 "order #xxx" 时知道去哪个表查。
type SchemaTable struct {
	Name       string   `json:"name"`                  // 表名 / collection 名 / redis key 前缀
	Kind       string   `json:"kind"`                  // table / collection / cache_prefix
	Type       string   `json:"type,omitempty"`        // mysql / doris / postgresql / mongodb / redis / 不确定时空
	SourceFile string   `json:"source_file,omitempty"` // 命中的源文件相对路径
	EntityName string   `json:"entity_name,omitempty"` // 对应的代码 struct/class 名(便于人核对)
	Fields     []string `json:"fields,omitempty"`      // 主要字段(best effort,扫到啥算啥)
	Strategy   string   `json:"strategy,omitempty"`    // 命中的扫描策略(orm_annotation / sql_literal / orm_api_call / file_name)
}

// RoleHint 是 RecommendRole 的输出:推荐 role + 命中规则的可读说明,
// 给 wizard UI 显示"📍 推荐 frontend(检测到 react+vite,无后端框架)"。
type RoleHint struct {
	// Role 推荐角色(backend/frontend/gateway/middleware/common-lib/mobile/admin/infra/docs)
	Role string `json:"role"`
	// Reason 命中的判定证据,用人话写,UI 直接展示
	Reason string `json:"reason"`
}

// (SubmoduleHint 在 monorepo_scan.go 里定义,见那个文件)

// RepoAnalysis 单仓库分析产物
type RepoAnalysis struct {
	Name            string           `json:"name"`
	Stack           string           `json:"stack"`
	RepoPath        string           `json:"repo_path"`
	ServiceNames    []string         `json:"service_names,omitempty"`
	Findings        []Finding        `json:"findings,omitempty"`
	DownstreamCalls []DownstreamCall `json:"downstream_calls,omitempty"`
	APIRoutes       []APIRoute       `json:"api_routes,omitempty"`
	DataStoreUsages []DataStoreUsage `json:"data_store_usages,omitempty"`
	SchemaTables    []SchemaTable    `json:"schema_tables,omitempty"`
	RoleHint        *RoleHint        `json:"role_hint,omitempty"` // 自动推断的角色 + 理由(用户可改)
	Warnings        []string         `json:"warnings,omitempty"`
	// Notes 是"信息性扫描发现",非错误也非警告 —— frontend_framework=next、
	// api_url[apps/...]=https://... 这种"顺手扫到的资料"。区别于 Warnings:
	//   Warnings  → "go.mod not found" / "scan failed" 这类需要用户注意的异常
	//   Notes     → 扫到的事实陈述,UI 用中性色展示,不让用户误以为出错
	Notes    []string `json:"notes,omitempty"`
	Verified bool     `json:"verified"`
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
