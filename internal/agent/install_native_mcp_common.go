// install_native_mcp_common.go —— Claude Code / Cursor / Codex / Openclaw 四家共享的
// MCP server 派生逻辑。
//
// 之前 install_native_mcp.go::buildMCPServersForCfg(IDE 用)和 install_native_openclaw_mcp.go::
// injectMCPServers(openclaw 用)两套实现长得几乎一样:都按 cfg.Infrastructure.ConfigCenters
// 跑 nacos × env、grafana per env、loki per env、lark messaging、feishu_project tracking。
// 改一处忘改另一处的事故已经踩过,抽一个 BuildMCPServers 共用。
//
// 区别用 MCPBuildOptions 控制:
//   - PruneEmpty:IDE 要(避免 settings.json 里把 "" 喂给后端,触发"无效连接"重试风暴);
//     openclaw 不要(保留全 schema 让 agent 自决)。
//
// 老的 IncludeRawObsCurl(原本控制 jaeger/elk 走 curl 占位)在两家分别迁到真 MCP 后
// 就没人用了 — 2026-05 jaeger 走 uvx opentelemetry-mcp,2026-05 elk 走
// @elastic/mcp-server-elasticsearch,两家 IDE / openclaw 都注册,选项已删。
//
// 命名:统一走 mcpKeyForAgent(agentID, prefix, sourceID, envID),单源走 "<prefix>-<env>",
// 多源走 "<prefix>-<sourceID>-<env>",IDE 共享 settings 池下加 agentID 前缀防撞名。
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// v0.1.1 is the last Elastic MCP release backed by @elastic/elasticsearch 8.x.
// v0.2.0+ moved to the 9.x client, which sends compatible-with=9 and is rejected
// by the ES 7/8 clusters this project supports.
const elasticsearchMCPPackage = "@elastic/mcp-server-elasticsearch@0.1.1"

// normalizeMongoURI 修复 mongodb URI 密码段含保留字符但未 URL-encode 的常见情况。
//
// MongoDB 官方文档明确要求 username/password 里的 `@ / ? # [ ] %` 必须 URL-encode,
// 但 driver 实际是按 RFC3986 严格解析,其他保留字 / unsafe char(如 `< > ^ " \ { | }`)
// 也会触发 parse error。用户在 wizard 直接粘贴明文密码极常见(mongosh / Compass 容忍
// 未编码 → 用户以为不需要),代码侧主动修一遍,免得 mcp 启动报"invalid connection string"。
//
// 算法:scheme:// 之后找最后一个 @ 作 host 起点,该 @ 之前的第一个 : 作 user/pass 分割,
//
//	pass 段每字符过一遍:已编码的 %xx 整体跳,其他保留字 / 非 ASCII / 控制字符 → %XX 编码。
//
// 已编码的 %xx 检测:`%` + 2 个 hex digit。用户密码含字面 `%` 而忘记编码 = 极罕见 corner,
// 不在本函数兜底范围(MongoDB 官方文档已明确说 % 必须编码,这部分用户责任)。
func normalizeMongoURI(uri string) string {
	idx := strings.Index(uri, "://")
	if idx < 0 {
		return uri
	}
	prefix := uri[:idx+3] // 含 "://"
	rest := uri[idx+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return uri // 无 userinfo
	}
	userinfo := rest[:at]
	afterAt := rest[at:] // 含 "@"
	user, pass, ok := strings.Cut(userinfo, ":")
	if !ok {
		return uri // 只有 user 没 pass,跳过
	}
	encoded := encodeMongoPass(pass)
	out := uri
	if encoded != pass {
		out = prefix + user + ":" + encoded + afterAt
	}
	return ensureAuthSource(out)
}

// ensureAuthSource 给 mongodb URI 自动补 ?authSource=admin(如果没有)。
//
// 最常见 mongodb 部署:root / admin 用户建在 admin db,业务用这个 root 跨 db 访问业务库
// (`mongodb://root:pass@host/business_db`)。MongoDB driver 默认把 path 段当 authSource —
// 在 business_db 找 root 找不到 → "Authentication failed"。其他工具(mongosh / Compass)
// 多数会自动 fallback 试 admin,driver 不会 → mcp 启动失败。
//
// 规则:
//   - path 已经是 /admin 或为空(/) → 不加(用户显式连 admin / 没指定默认 db)
//   - query 里已经有 authSource= → 不加(用户显式指定了)
//   - 否则 → 自动追加 ?authSource=admin
//
// 如果用户的 mongodb 不是这个模式(authSource 在 myauth 等其他 db),他在 wizard URI 末尾
// 显式加 ?authSource=myauth 即可,本函数检测到 query 里有 authSource= 会跳过不动。
func ensureAuthSource(uri string) string {
	_, rest, found := strings.Cut(uri, "://")
	if !found {
		return uri
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return uri // 没 userinfo → 没认证场景,不动
	}
	hostAndAfter := rest[at+1:] // host[:port][/path][?query]
	_, pathAndQuery, hasSlash := strings.Cut(hostAndAfter, "/")
	if !hasSlash {
		return uri // 没 path 段(mongodb://user:pass@host) → 没指定 db,默认走 admin,不用加
	}
	path, query, hasQuery := strings.Cut(pathAndQuery, "?")
	if path == "" || path == "admin" {
		return uri // 用户已经连 admin / 没指定默认 db
	}
	if hasQuery && containsParam(query, "authSource") {
		return uri // 已显式指定 authSource(无论值是什么,尊重用户)
	}
	if hasQuery {
		return uri + "&authSource=admin"
	}
	return uri + "?authSource=admin"
}

// ensureDirectConnection 给单节点 mongodb URI 自动补 directConnection=true,
// 绕过 Node mongodb driver 在 MongoDB 8.x(wire version 27)下的 SDAM 兼容 bug。
//
// 现象:mongod 报 maxWireVersion=27,Node driver(npm `mongodb@6.x` 和 `@7.x` 都验过)
// 心跳成功识别 RSPrimary、compatible:true,但拓扑级 commonWireVersion 卡在 0,
// selectServer 全部候选拒掉 → MongoClient.connect() 干等 serverSelectionTimeoutMS
// (默认 30s)超时,MCP stdio 握手永远不成立。mongosh 走另一套 driver 实现,同 URI 秒回。
//
// 单节点副本集(本地 dev、单实例 prod、单 endpoint AWS DocumentDB)是这个坑的密集区。
// `directConnection=true` 让 driver 跳过副本集 SDAM 分支,按 single server 路径走 ——
// 实战验证可绕(2026-05-12 用户实例)。
//
// 安全条件:directConnection=true 只在单 host 时合法,多 host 副本集 / SRV / 用户显式
// replicaSet= 时强行套会让 driver 忽略其他 member。规则:
//   - mongodb+srv:// → 不动(SRV 是 DNS 多端点发现)
//   - host 段含 `,`(多 host)→ 不动
//   - query 已有 directConnection= → 不动(尊重用户)
//   - query 已有 replicaSet= → 不动(用户显式跑 SDAM,不要破坏)
//   - 其余(单 host)→ 自动补 directConnection=true
//
// 长期方案:等 npm `mongodb` driver 修 wire 27 兼容 / 上游 mcp-mongo-server pin
// 兼容版本,本 helper 可下线;短期此修复值得保留(下个用户撞坑代价 4 小时)。
func ensureDirectConnection(uri string) string {
	if strings.HasPrefix(uri, "mongodb+srv://") {
		return uri
	}
	_, rest, found := strings.Cut(uri, "://")
	if !found {
		return uri
	}
	hostAndAfter := rest
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		hostAndAfter = rest[at+1:]
	}
	hostPart := hostAndAfter
	if cut := strings.IndexAny(hostAndAfter, "/?"); cut >= 0 {
		hostPart = hostAndAfter[:cut]
	}
	if strings.Contains(hostPart, ",") {
		return uri
	}
	_, query, hasQ := strings.Cut(hostAndAfter, "?")
	if !hasQ {
		return uri + "?directConnection=true"
	}
	if containsParam(query, "directConnection") || containsParam(query, "replicaSet") {
		return uri
	}
	return uri + "&directConnection=true"
}

// containsParam 检查 query string 里是否含名为 name 的参数(`name=...` 或 `name&` 形式)。
func containsParam(query, name string) bool {
	for pair := range strings.SplitSeq(query, "&") {
		k, _, _ := strings.Cut(pair, "=")
		if k == name {
			return true
		}
	}
	return false
}

func encodeMongoPass(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		// 已编码 %xx 整体跳过
		if c == '%' && i+2 < n && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
			b.WriteByte('%')
			b.WriteByte(s[i+1])
			b.WriteByte(s[i+2])
			i += 2
			continue
		}
		if needsEscape(c) {
			b.WriteByte('%')
			b.WriteByte(hexUpper(c >> 4))
			b.WriteByte(hexUpper(c & 0x0f))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexUpper(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + n - 10
}

// needsEscape 字符是否要在 mongodb URI userinfo:password 里 URL-encode。
// 包含:RFC3986 gen-delims + sub-delims + 常见 unsafe 字符 + 非 ASCII / 控制字符。
// 排除字符:unreserved (alphanum + `- _ . ~`)。
func needsEscape(c byte) bool {
	// unreserved:不需编码
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return false
	}
	switch c {
	case '-', '_', '.', '~':
		return false
	}
	// 非 ASCII / 控制字符 / 空格 → 编
	if c < 0x21 || c > 0x7e {
		return true
	}
	// 其余 ASCII 可见字符全编(覆盖 gen-delims `: / ? # [ ] @`、sub-delims `! $ & ' ( ) * + , ; =`、
	// 和 unsafe `< > " \ ^ ` { | } %`)。我们已经在上层处理了 % + 2 hex 的免疫,这里 % 也编。
	return true
}

// parseConnURL 把 redis:// / clickhouse:// / http:// / postgres:// 等 URL 拆成
// host/port/user/pass/path 字段,供"npm mcp 包要拆字段不接整 URL"的场景用。
// 解析失败 / 没填整段为空,所有返回值置空 — 调用方按需自取,空字段交 envBlock 决定保留还是 prune。
//
// 注意:不支持 mysql go-sql-driver DSN(`user:pass@tcp(host:port)/db`),那个走 parseMySQLDSN。
func parseConnURL(s string) (host, port, user, pass, path string) {
	if s == "" {
		return
	}
	u, err := url.Parse(s)
	if err != nil {
		return
	}
	host = u.Hostname()
	port = u.Port()
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	path = strings.TrimPrefix(u.Path, "/")
	return
}

// parseMySQLDSN 解析 go-sql-driver/mysql 风格 DSN:
//
//	user:pass@tcp(host:port)/dbname?param=val
//
// 故意不引 go-sql-driver/mysql(整个工程没用 mysql client,引这一处不划算),
// 走最小字符串切分:tcp() 段提 host/port,@ 前提 user/pass,)/ 后 ? 前提 db。
// DSN 形如 unix(/path) / cloudsql(...) 等罕见 protocol 解析失败时全空 — 用户场景里
// 几乎都是 tcp(),其他 case mcp 启动失败时按 hint 让用户重填即可。
func parseMySQLDSN(dsn string) (host, port, user, pass, db string) {
	if dsn == "" {
		return
	}
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return
	}
	cred := dsn[:at]
	rest := dsn[at+1:]

	if u, p, ok := strings.Cut(cred, ":"); ok {
		user, pass = u, p
	} else {
		user = cred
	}

	// rest 形如 "tcp(host:port)/dbname?params"
	lp, rp := strings.Index(rest, "("), strings.Index(rest, ")")
	if lp < 0 || rp <= lp {
		return
	}
	hp := rest[lp+1 : rp]
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		host, port = hp[:i], hp[i+1:]
	} else {
		host = hp
	}

	// 跳过 ")/" 取 db,截 ? 之前
	if rp+1 < len(rest) && rest[rp+1] == '/' {
		after := rest[rp+2:]
		if d, _, ok := strings.Cut(after, "?"); ok {
			db = d
		} else {
			db = after
		}
	}
	return
}

// MCPBuildOptions 控制 BuildMCPServers 的行为差异。
type MCPBuildOptions struct {
	// AgentID:MCP server key 前缀(如 "truss-bot")。空字符串 = 不加前缀(单 agent 项目级)。
	// IDE 共享 settings.json 池必须设非空,避免多 system 同名 mcp 互相覆盖。
	AgentID string

	// PruneEmpty:env block 里 value=="" 的 entry 丢掉(IDE 走这条,避免 IDE 把字面 "" 当
	// 真值传给后端进程造成无效连接);openclaw 留着等 agent 自决,所以 false。
	PruneEmpty bool

	// KafkaMCPBinaryPath:kafka-mcp-server binary 绝对路径。
	//
	// **隐式契约**:production 调用方(install_native_mcp.go / install_native_openclaw_mcp.go)
	// 必须先调 EnsureKafkaMCPInstalled 拿绝对路径再传进来。绝对路径关键 — mac launchd GUI
	// 启动子进程 PATH 不含 brew prefix,字面 "kafka-mcp-server" 找不到 ENOENT 静默挂(同
	// findOpenclawCLI 修过的坑,commit e44c74d)。
	//
	// 空字符串 = 回落 PATH 形式字面 "kafka-mcp-server"。仅两种场景用空:
	//  (a) ensure 失败 fallback(用户装好后重跑 install 会拿到绝对路径)
	//  (b) 单元测试只验证 builder 逻辑不验证启动可达性
	KafkaMCPBinaryPath string

	// NacosMCPScriptPath:~/.tshoot/scripts/nacos_mcp.py 的绝对路径(EnsureNacosMCPScript 返回)。
	// buildNacos 用它拼 `uv run --script <path>`。空字符串 = ensure 失败,buildNacos 跳过注册,
	// nacos 走 config-executor SKILL 的 HTTP fallback 兜底。
	NacosMCPScriptPath string

	// CodeGraphBinaryPath:EnsureCodeGraphInstalled 返回的稳定绝对命令路径。
	// 空字符串表示 ensure 失败或能力未启用,buildCodeGraph 跳过 MCP 注册。
	CodeGraphBinaryPath string
}

// BuildMCPServers 按 cfg.Infrastructure 派生 {server_key: spec} 扁平 map。
// 调用方:
//   - install_native_mcp.go(IDE)→ 把返回值 merge 进 settings["mcpServers"]
//   - install_native_openclaw_mcp.go → 把返回值 merge 进 root["mcp"]["servers"]
//
// get(envVarName) 由调用方提供:从 creds map / 老 .env merge 后的合并视图取值。返回 ""
// 表示该字段没填,IDE 模式下整条字段会被 prune(见 PruneEmpty)。
// mcpBuilder 把原 BuildMCPServers 函数体里的闭包(keyFor/keyFixed/envBlock)抽成方法,
// 各 MCP 派生段(nacos/grafana/jaeger/elk/datastores/lark/feishu)拆成独立方法,
// BuildMCPServers 变成单纯 dispatch。
//
// 拆这个的动机:原函数 420 行单 func 太胖,加新 MCP 类型要在巨型 switch / sequential block
// 里找位置,每次改回归压力大;拆完后每段独立,test 用例也能按段聚焦。
type mcpBuilder struct {
	cfg  *config.SystemConfig
	opts MCPBuildOptions
	get  func(string) string
}

func (b *mcpBuilder) keyFor(prefix, sourceID, envID string) string {
	return mcpKeyForAgent(b.opts.AgentID, prefix, sourceID, envID)
}

func (b *mcpBuilder) keyFixed(name string) string {
	if b.opts.AgentID == "" {
		return name
	}
	return b.opts.AgentID + "-" + name
}

// envBlock 处理两件事:
//  1. 默认注入 OTEL_SDK_DISABLED=true(防 elastic-otel-node / @sentry/node / Python OTel
//     等被 npm/pip 包透传依赖自动启用,启动时往 stdout 打 banner JSON 污染 stdio JSON-RPC
//     协议 → IDE 报"connection closed: initialize response"。已知 ES MCP 必踩,其它包
//     难穷举,默认全开防御 — 跨语言通用 OTel 规范变量,单纯关掉自动 telemetry,不影响
//     业务功能)。callsite 显式设了别的值会覆盖这个默认。
//  2. PruneEmpty=true 时把 value=="" 的 entry 删掉(IDE 走这条,避免字面 "" 喂给后端
//     进程触发"无效连接"重试风暴);openclaw 留全等 agent 自决。
func (b *mcpBuilder) envBlock(m map[string]any) map[string]any {
	if _, has := m["OTEL_SDK_DISABLED"]; !has {
		m["OTEL_SDK_DISABLED"] = "true"
	}
	if !b.opts.PruneEmpty {
		return m
	}
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

func BuildMCPServers(cfg *config.SystemConfig, opts MCPBuildOptions, get func(string) string) map[string]any {
	b := &mcpBuilder{cfg: cfg, opts: opts, get: get}
	servers := map[string]any{}
	// nacos 走自研本地 MCP 脚本(templates/.../scripts/nacos_mcp.py,装到 ~/.tshoot/scripts/)。
	//
	// 决策演进(为什么绕了一大圈最后回到 MCP):
	//   5d5a139  HTTP 主路径(SKILL 内 Python 脚本,临时绕路)
	//   23d503a  MCP 主路径,bake token:install 一次性 login 拿 access_token 烧进 mcp 启动参数
	//   8d05068  推翻 23d503a 回 HTTP(方案 B 终局)。坍塌原因:官方 nacos-mcp-server 只接
	//            --access_token CLI 且进程内固定不 refresh;truss 现场 nacos 2.3.0 + 运维不调
	//            tokenTtl → token 5h 过期 mcp 401,装一次满血几小时然后默默降级,跟"机器人长期跑"冲突
	//   本次     方案 D,自研 nacos_mcp.py:脚本自己拿 username/password 跑 login + 后台 refresh
	//            (tokenTtl*0.8 周期 + 401 强制 re-login),token 短 TTL 完全无所谓;且只用 nacos
	//            /v1 endpoint(2.x/3.x 都支持),绕开上游 /v3 admin API 要 nacos 3.0+ 的限制。
	//            凭据走 env(NACOS_USERNAME/PASSWORD)不进 args,不暴露在 ps。
	//
	// 跟 23d503a 的本质区别:token 生命周期管理从 install 阶段(一次性、会过期)挪进 MCP 进程
	// 运行时(持续 refresh)。这是当年坍塌的真根因,方案 D 才真正修掉。
	//
	// ensure 失败(NacosMCPScriptPath 空)时 buildNacos 跳过注册,nacos 回落到 config-executor
	// SKILL 的 HTTP fallback(scripts/nacos_config.py)—— fallback 始终保留,降级不致盲。
	b.buildNacos(servers)
	b.buildGrafana(servers)
	b.buildJaeger(servers)
	b.buildELK(servers)
	b.buildDataStores(servers)
	b.buildLark(servers)
	b.buildFeishuProject(servers)
	b.buildOne2All(servers)
	b.buildCodeGraph(servers)
	return servers
}

// buildNacos:nacos per (source × env),多源 + 每 env 一个独立本地 MCP 实例。
//
// 命令走 `uv run --script <~/.tshoot/scripts/nacos_mcp.py>`。凭据 host/port/username/password
// 全走 env(脚本 argparse 默认从 NACOS_* env 读),不进 args —— 不暴露在 ps、不被 shell history
// 记录。脚本运行时自己 login + 后台 refresh,install 阶段不碰 token(跟 23d503a 的 bake 模型
// 根本区别,详见 BuildMCPServers 决策注释)。
//
// 跳过条件:
//   - NacosMCPScriptPath 空(EnsureNacosMCPScript 失败)→ 整段跳过,nacos 回落 SKILL HTTP fallback
//   - PruneEmpty(IDE)且 addr/user/pass 任一缺 → 跳过该 env,避免注册一个起不来的死 mcp
//     (openclaw PruneEmpty=false 时保留空 schema 让 agent 自决填)
func (b *mcpBuilder) buildNacos(servers map[string]any) {
	scriptPath := b.opts.NacosMCPScriptPath
	if scriptPath == "" {
		return // ensure 失败,回落 HTTP fallback
	}
	for _, cc := range b.cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range b.cfg.Environments {
			addr := b.get(envVar("CC_ADDR", cc.ID, e.ID))
			user := b.get(envVar("CC_USER", cc.ID, e.ID))
			pass := b.get(envVar("CC_PASS", cc.ID, e.ID))

			if b.opts.PruneEmpty && (addr == "" || user == "" || pass == "") {
				continue // IDE:凭据不全不注册死 mcp
			}
			host, port := splitNacosAddr(addr)
			servers[b.keyFor("nacos", cc.ID, e.ID)] = map[string]any{
				"command": "uv",
				"args":    []any{"run", "--script", scriptPath},
				// 凭据走 env(脚本从 NACOS_* 读),不进 args 防 ps 泄漏。OTEL_SDK_DISABLED 由
				// envBlock 注入,防 python 包间接依赖的 OTel 自动 instrument 往 stdout 打 banner
				// 污染 stdio JSON-RPC。
				"env": b.envBlock(map[string]any{
					"NACOS_HOST":     host,
					"NACOS_PORT":     port,
					"NACOS_USERNAME": user,
					"NACOS_PASSWORD": pass,
				}),
			}
		}
	}
}

// 可观测性 MCP builders(grafana / jaeger / elk)拆到 install_native_mcp_obs.go。
// 数据层 + messaging MCP builders 后续会拆到对应文件。本文件保留 common helpers + 总入口。

// buildDataStores 分发 + 8 家 build<DS> 已拆到 install_native_mcp_data_stores.go。

// dsEndpointUnique 是 dedupe 后的一条 endpoint + 派生 sourceID。
// sourceID 在调用方只在 unique > 1 时才用,= 1 时调用方会传空字符串退化命名。
type dsEndpointUnique struct {
	endpoint *config.DataStoreEndpoint
	sourceID string // host 抽取 + 撞名兜底 + 异常 fallback 后的稳定 ID
}

// dsEndpointsUnique:拉同 (ds, env) 下所有非空 endpoint,按连接串 dedupe,派生 sourceID。
//
// dedupe 规则:同 URI / URL / DSN / Brokers 字符串完全一致视为同 cluster,只保留首次出现那条。
// 不做 normalize(replica set hosts 排序、query params 排序等),实战中用户从 ops 文档复制
// URI 通常前后一致;真撞 normalize 问题再升 (TODO 标记不做)。
//
// sourceID 派生规则(3 层 fallback,详见 deriveSourceID):
//  1. host 第一段(主路径,~95% URI 命中) — `mongo-commerce.test:27017` → `mongo-commerce`
//  2. 撞名时加 URI hash 短前缀 — 同 host 不同 port 场景兜底
//  3. host 抽取完全失败 → URI hash8 完全兜底
//
// 调用方在 unique 数 ≤ 1 时传空 sourceID(MCP key 退化成 `<type>-<env>`,跟老用户行为一致);
// > 1 时用本函数派生的 sourceID(MCP key = `<type>-<source>-<env>`)。
func dsEndpointsUnique(ds config.DataStore, envID string) []dsEndpointUnique {
	type rawEntry struct {
		ep  *config.DataStoreEndpoint
		key string // dedupe key(取首个非空字段)
	}
	var raws []rawEntry
	seen := map[string]bool{}
	for i := range ds.Endpoints {
		ep := &ds.Endpoints[i]
		if ep.Env != envID {
			continue
		}
		key := firstNonEmpty(ep.URI, ep.URL, ep.DSN, ep.Brokers)
		if key == "" {
			continue
		}
		if seen[key] {
			continue // 同连接串已收过,跳
		}
		seen[key] = true
		raws = append(raws, rawEntry{ep: ep, key: key})
	}
	// 派生 sourceID 之后再 dedupe sourceID(撞名加 hash 兜底)
	out := make([]dsEndpointUnique, 0, len(raws))
	usedSource := map[string]bool{}
	for _, r := range raws {
		sid := deriveSourceID(r.key)
		if usedSource[sid] {
			sid = sid + "-" + uriHash(r.key, 6)
		}
		usedSource[sid] = true
		out = append(out, dsEndpointUnique{endpoint: r.ep, sourceID: sid})
	}
	return out
}

// deriveSourceID 从连接串提取稳定可读的 sourceID。
//
//	mongodb://user:pass@mongo-commerce.test.example.com:27017/?... → mongo-commerce
//	mongodb://10.0.0.1:27017/?...                                 → 10-0-0-1
//	mongodb://a.dc1,b.dc1/?replicaSet=rs                          → a-dc1
//	postgres://u:p@pg-master.test/db                              → pg-master
//	tcp://broker1.test:9092,broker2.test:9092                     → broker1
//	格式异常 / host 抽不到 → h-<URI sha256 前 8 字符>
//
// 抽出 host 第一段后:小写化 + `.` 换 `-` + 非 `[a-z0-9-]` 字符换 `-` + 头尾 trim `-`。
func deriveSourceID(connStr string) string {
	s := strings.TrimSpace(connStr)
	if s == "" {
		return "h-" + uriHash(connStr, 8)
	}
	// 1. 去掉 scheme://(或 tcp:// / amqp:// 等)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// 2. 去掉凭据段(@ 之前 + 第一个 / 之前的 @ 才算凭据 @)
	if at := strings.LastIndex(s, "@"); at >= 0 {
		// 确认 @ 在 host 段(不在 path 段);path 起点是第一个 '/'
		firstSlash := strings.Index(s, "/")
		if firstSlash == -1 || at < firstSlash {
			s = s[at+1:]
		}
	}
	// 3. 砍掉 path / query / fragment(`/` `?` `#` 之后全丢)
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	// 4. 多 host(replica set / kafka brokers):取第一个 `,` 之前
	if i := strings.Index(s, ","); i >= 0 {
		s = s[:i]
	}
	// 5. 去掉 port(host:port 形式;同时兼容 ipv6 [::1]:port)
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			s = s[1:end] // 取 ipv6 地址本身
		}
	} else if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	// 6. 取 host 第一段(只对域名抽取;ipv4 / ipv6 整体保留)
	if !looksLikeIP(s) {
		if i := strings.Index(s, "."); i >= 0 {
			s = s[:i]
		}
	}
	// 7. sanitize:小写 + 非 [a-z0-9-] 换 -
	s = sanitizeSourceID(s)
	if s == "" {
		return "h-" + uriHash(connStr, 8)
	}
	return s
}

// sanitizeSourceID 把任意字符串转成只含 [a-z0-9-] 的 ID,头尾去 `-`,空串返空。
func sanitizeSourceID(s string) string {
	var sb strings.Builder
	prevDash := true // 前置 true 用于 trim 头部 -
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				sb.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := sb.String()
	return strings.TrimRight(out, "-")
}

// looksLikeIP 粗判:全是数字 / `.` / `:` / a-f / `[` `]` 字符 → 是 IP(v4 或 v6)。
// 用来决定要不要取 host 第一段(域名取第一段,IP 全保留 sanitize)。
func looksLikeIP(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '.' || r == ':' || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			// allowed in IP forms
		default:
			return false
		}
	}
	return hasDigit
}

// uriHash 算连接串的 sha256 短前缀(小写 hex)。用于撞名兜底 / 异常 fallback。
func uriHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	h := hex.EncodeToString(sum[:])
	if n > len(h) {
		n = len(h)
	}
	return h[:n]
}

// firstNonEmpty 串联多源取第一个非空。install creds 优先(env-vars 模式),fallback yaml。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sourceID 在多 cluster 场景由 dsEndpointsUnique 派生(host 第一段);单 cluster 下传空,MCP key 退化无 source 段。
// envVar 命名同步带 source 段(envVar 函数内部 sourceID == "default" / "" 时跳过)。
// buildMongoDB → install_native_mcp_data_stores.go

// (上面 7 个 build<DS> 已全部拆到 install_native_mcp_data_stores.go)

// buildLark / buildFeishuProject 已拆到 install_native_mcp_messaging.go。
