package config

type RepoAnalysis struct {
	// Enabled is tri-state for backward compatibility: omitted repositories were
	// historically generated without an analysis block and must remain enabled
	// when the system explicitly opts in to code intelligence. Only an explicit
	// false excludes a repository.
	Enabled      *bool    `yaml:"enabled,omitempty"`
	ShallowDepth int      `yaml:"shallow_depth"`
	IncludePaths []string `yaml:"include_paths"`
}

func (a RepoAnalysis) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// RepoRole 仓库在系统里担的角色,给 incident-investigator 决定"沿依赖追"的方向 + 范围。
//
//	backend     — 后端业务服务(典型 microservice),双向依赖图都有
//	frontend    — 前端 / web app,只有 downstream(调 gateway / api),无 upstream
//	gateway     — API 网关 / BFF,downstream 是后端,upstream 是 frontend / 移动端
//	middleware  — 接入层 / 公共中间件(消息队列 worker / cron 调度器),双向依赖
//	common-lib  — 公共库/SDK,自身不在依赖图节点上 —— 它被各个服务 import,
//	              但不接受运行时调用(没有 service endpoint)。排障时不当主角看,
//	              而是作为"哪些服务依赖了某 lib 的某版本"做横向比对。
//	mobile      — 移动端 app(iOS/Android),只调 gateway,没 upstream
//	admin       — 管理后台,典型只有 downstream(调 backend admin 接口)
//	infra       — 基础设施配置仓库(k8s manifest / terraform),不在运行依赖图
//	docs        — 文档仓库,仅作背景资料
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
	Name      string `yaml:"name"`
	URL       string `yaml:"url"`
	Stack     string `yaml:"stack"`
	Framework string `yaml:"framework"`
	// Role 决定 incident-investigator 看本 repo 的视角:common-lib / docs / infra
	// 不进服务依赖图(它们没运行 endpoint);frontend / mobile / admin 只算 upstream
	// 边的发起方;backend / middleware 双向都参与;gateway 居中。
	// 空字符串视为 backend(老 yaml 兼容)。
	Role RepoRole `yaml:"role,omitempty"`
	// SubPath 是 monorepo 场景:多个服务在同一个 git 仓库的不同子目录,本字段指定本条目对应
	// 的子目录(相对仓库根,如 "services/commerce" / "packages/admin-web")。
	// 用法:在 repos[] 里添加多个条目共用相同 url + 不同 sub_path,name 各取服务名。
	// 例:
	//   - {name: commerce, url: <truss>, stack: go, role: backend, sub_path: services/commerce}
	//   - {name: admin-web, url: <truss>, stack: node, role: admin, sub_path: web/admin}
	//   - {name: shared,    url: <truss>, stack: go, role: common-lib, sub_path: shared}
	// clone 时按 url dedup(同 url 只 clone 一次),analyzer / role-hint / dep-scan
	// 都在 <clone-root>/<sub_path> 这个子目录里跑。空时整 repo 当一个服务对待(默认行为)。
	SubPath      string   `yaml:"sub_path,omitempty"`
	ServiceNames []string `yaml:"service_names"`
	// ServiceEntries records source ownership for multiple deployable services in
	// one repository. Keys are service_names and values are repository-relative
	// entry directories (for example "." and "packages/document").
	ServiceEntries map[string]string `yaml:"service_entries,omitempty"`
	EnvBranches    map[string]string `yaml:"env_branches"`
	Analysis       RepoAnalysis      `yaml:"analysis"`
	// ConfigSource 引用 infrastructure.config_centers[].id,标明本仓库的配置走哪个源。
	// 多源场景必填(运行时 routing skill 据此选 MCP);空时 LoadFromBytes 自动绑到
	// config_centers[0],兼容只有一个源的老 yaml。
	ConfigSource string `yaml:"config_source,omitempty"`
	// ParentRepo 引用 repos[].name,声明本仓库是从某 umbrella(主仓)切出去的独立 git 仓。
	// 典型场景:truss umbrella + commerce/admin/payment 三个被切出去的独立仓。
	// 部署时编排:
	//   1. umbrella(parent_repo 为空、被别人引用的)先 clone 到 <reposRoot>/<name>
	//   2. 子模块默认 clone 到 <umbrella-clone>/<parent_path 或 name>(继承模式)
	//   3. 用户也可以**单独配 clone 父目录**(wizard 里给 _cloneTarget,运行时通过
	//      RepoPaths 覆盖),走独立模式 clone 到自己的位置,跟普通仓库完全一样
	// 跟 SubPath 的区别:SubPath 是"本 URL 的 clone 根 → 服务代码"导航(同 URL 多服务
	// monorepo 用);ParentPath 是"umbrella clone 根 → 本子模块挂载点"导航。两者正交,
	// 同时设也合法(几乎不用,但语义清晰)。
	ParentRepo string `yaml:"parent_repo,omitempty"`
	// ParentPath 在 umbrella clone 内的相对路径,只在 ParentRepo 非空时生效。
	// 空 = 用 repo.name 兜底(典型:.gitmodules 里 path=name 的常规约定)。
	// 非空场景:.gitmodules 里 path=services/commerce 但 name=commerce(路径跟仓名不一致)。
	ParentPath string `yaml:"parent_path,omitempty"`
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
// "业务服务"四类(backend / gateway / middleware / admin)必须提供配置服务名 —— 它们
// 对应 nacos 配置 key 和数据层归属。frontend 可选填写 service_names 作为独立的运行时
// 身份（仓库 ↔ Deployment/日志/调用链），但不进入配置中心或数据层扫描；mobile 用
// repo.name 标识调用发起方；common-lib / infra / docs 干脆不进图。
//
// 健康检查和配置/数据扫描按这个判定；运行时身份由调用方单独处理。
func (r Repo) RequiresServiceNames() bool {
	switch r.EffectiveRole() {
	case RoleBackend, RoleGateway, RoleMiddleware, RoleAdmin:
		return true
	}
	return false
}
