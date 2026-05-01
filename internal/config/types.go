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

// MCPKeyPrefix 返回 MCP server key 的前缀。
//
// 跟 ResolveID() 区别:ResolveID() 返回完整 agent 标识(如 "<system.id>-troubleshooter"),
// 用于 agent.md 文件名 / skills 目录名 / openclaw agents.list[i].id 这些"用户可见标识符";
// MCPKeyPrefix() 返回**短**标识(优先 system.id),用于 mcpServers 的 key 前缀,避免
// "agent_id + server_type + env" 拼起来超过 IDE 60 字符 tool name 限制。
//
// 例:system.id=truss → MCP key 形如 "truss-grafana-mcp-server-dev"(20 字),
// 留 40 字给 tool 名;若用 ResolveID() = "truss-troubleshooter" → 33 字 prefix,
// grafana 的 get_dashboard_panel_queries(27 字)拼起来 60+ 超限。
//
// system.id 为空(理论上不该出现,Loader 强制必填)时回退 ResolveID,保证非空。
func (s *SystemConfig) MCPKeyPrefix() string {
	if id := s.System.ID; id != "" {
		return id
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

// RepoRole 仓库在系统里担的角色,给 incident-investigator 决定"沿依赖追"的方向 + 范围。
//   backend     — 后端业务服务(典型 microservice),双向依赖图都有
//   frontend    — 前端 / web app,只有 downstream(调 gateway / api),无 upstream
//   gateway     — API 网关 / BFF,downstream 是后端,upstream 是 frontend / 移动端
//   middleware  — 接入层 / 公共中间件(消息队列 worker / cron 调度器),双向依赖
//   common-lib  — 公共库/SDK,自身不在依赖图节点上 —— 它被各个服务 import,
//                 但不接受运行时调用(没有 service endpoint)。排障时不当主角看,
//                 而是作为"哪些服务依赖了某 lib 的某版本"做横向比对。
//   mobile      — 移动端 app(iOS/Android),只调 gateway,没 upstream
//   admin       — 管理后台,典型只有 downstream(调 backend admin 接口)
//   infra       — 基础设施配置仓库(k8s manifest / terraform),不在运行依赖图
//   docs        — 文档仓库,仅作背景资料
//
// 留空时模板按 "backend" 兜底。GUI wizard 给一个下拉,默认按 stack 推荐:
// go/java/python → backend, node 含 server → backend, node 纯前端 → frontend, php → backend...
type RepoRole = string

const (
	RoleBackend    RepoRole = "backend"
	RoleFrontend   RepoRole = "frontend"
	RoleGateway    RepoRole = "gateway"
	RoleMiddleware RepoRole = "middleware"
	RoleCommonLib  RepoRole = "common-lib"
	RoleMobile     RepoRole = "mobile"
	RoleAdmin      RepoRole = "admin"
	RoleInfra      RepoRole = "infra"
	RoleDocs       RepoRole = "docs"
)

type Repo struct {
	Name         string            `yaml:"name"`
	URL          string            `yaml:"url"`
	Stack        string            `yaml:"stack"`
	Framework    string            `yaml:"framework"`
	// Role 决定 incident-investigator 看本 repo 的视角:common-lib / docs / infra
	// 不进服务依赖图(它们没运行 endpoint);frontend / mobile / admin 只算 upstream
	// 边的发起方;backend / middleware 双向都参与;gateway 居中。
	// 空字符串视为 backend(老 yaml 兼容)。
	Role         RepoRole          `yaml:"role,omitempty"`
	// SubPath 是 monorepo 场景:多个服务在同一个 git 仓库的不同子目录,本字段指定本条目对应
	// 的子目录(相对仓库根,如 "services/commerce" / "packages/admin-web")。
	// 用法:在 repos[] 里添加多个条目共用相同 url + 不同 sub_path,name 各取服务名。
	// 例:
	//   - {name: commerce, url: <truss>, stack: go, role: backend, sub_path: services/commerce}
	//   - {name: admin-web, url: <truss>, stack: node, role: admin, sub_path: web/admin}
	//   - {name: shared,    url: <truss>, stack: go, role: common-lib, sub_path: shared}
	// clone 时按 url dedup(同 url 只 clone 一次),analyzer / role-hint / dep-scan
	// 都在 <clone-root>/<sub_path> 这个子目录里跑。空时整 repo 当一个服务对待(默认行为)。
	SubPath      string            `yaml:"sub_path,omitempty"`
	ServiceNames []string          `yaml:"service_names"`
	EnvBranches  map[string]string `yaml:"env_branches"`
	Analysis     RepoAnalysis      `yaml:"analysis"`
	// ConfigSource 引用 infrastructure.config_centers[].id,标明本仓库的配置走哪个源。
	// 多源场景必填(运行时 routing skill 据此选 MCP);空时 LoadFromBytes 自动绑到
	// config_centers[0],兼容只有一个源的老 yaml。
	ConfigSource string `yaml:"config_source,omitempty"`
}

// EffectiveRole 返回 repo 实际的 role:Role 显式给了就用,否则 fallback "backend"。
// 给模板和 generator 用,统一处理空值。
func (r Repo) EffectiveRole() RepoRole {
	if r.Role != "" {
		return r.Role
	}
	return RoleBackend
}

// IsServiceNode 返回 repo 是否进服务依赖图。common-lib / infra / docs 不进 ——
// 它们没有运行 endpoint,排障时不会作为"主角服务"或"上下游节点"。
func (r Repo) IsServiceNode() bool {
	switch r.EffectiveRole() {
	case RoleCommonLib, RoleInfra, RoleDocs:
		return false
	}
	return true
}

// RequiresServiceNames 返回是否需要 service_names。比 IsServiceNode 更严:只有
// "业务服务"四类(backend / gateway / middleware / admin)需要 —— 它们对应 nacos
// 配置 key、k8s deployment、loki app 标签。frontend / mobile 是"调用发起方"进图但
// 没自己后端 endpoint(不挂在配置中心、不进 k8s 部署/不进日志聚合),没有 service_names
// 概念;common-lib / infra / docs 干脆不进图。
//
// 健康检查 / wizard auto-fill / 模板 emit 都按这个判定决定要不要为该 repo 喂 service_names。
func (r Repo) RequiresServiceNames() bool {
	switch r.EffectiveRole() {
	case RoleBackend, RoleGateway, RoleMiddleware, RoleAdmin:
		return true
	}
	return false
}

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
	EnvLabel      string                       `yaml:"env_label,omitempty"`
	ServiceLabel  string                       `yaml:"service_label,omitempty"`
	GrafanaDSUID  string                       `yaml:"grafana_ds_uid,omitempty"`
	Namespace     string                       `yaml:"namespace,omitempty"`
	ServiceMap    map[string]map[string]string `yaml:"service_map,omitempty"`
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

// K8sRuntime 走 Kuboard v4 API 查 pod / events / 日志 / deployment 状态。
// 跟配置源 kuboard 的连接信息可以重合(同一个 Kuboard 实例),但开关独立 ——
// 用户也可能从 nacos 读配置、同时只用 Kuboard 查运行时。
type K8sRuntime struct {
	Enabled    bool                        `yaml:"enabled"`
	URLByEnv   map[string]string           `yaml:"url_by_env,omitempty"` // env → Kuboard URL
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

// DataStoreEndpoint 一条 (env, service) → 真实连接串。GUI wizard 写出来的形式。
// 字段是各 type 的并集:redis/es 用 URL,mongodb 用 URI,mysql 用 DSN,kafka 用 Brokers。
// generator 用这份数据反推 service-dependency-map 的 data_stores 字段(谁用了哪些数据层),
// 不再让用户手填。
type DataStoreEndpoint struct {
	Env     string `yaml:"env"`
	Service string `yaml:"service,omitempty"`
	URL     string `yaml:"url,omitempty"`     // redis / elasticsearch / clickhouse
	URI     string `yaml:"uri,omitempty"`     // mongodb
	DSN     string `yaml:"dsn,omitempty"`     // mysql / postgresql
	Brokers string `yaml:"brokers,omitempty"` // kafka / rocketmq
	User    string `yaml:"user,omitempty"`
	Pass    string `yaml:"pass,omitempty"`
}

type DataStore struct {
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
