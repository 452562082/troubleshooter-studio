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
//                openclaw 不要(保留全 schema 让 agent 自决)。
//   - IncludeRawObsCurl:openclaw 额外写 jaeger / elk 的 curl 占位条目(无独立 MCP,只记 URL
//                       让 agent 直查 ES API);IDE 不写(IDE 没"代理 curl 调 API"的运行时)。
//
// 命名:统一走 mcpKeyForAgent(agentID, prefix, sourceID, envID),单源走 "<prefix>-<env>",
// 多源走 "<prefix>-<sourceID>-<env>",IDE 共享 settings 池下加 agentID 前缀防撞名。
package agent

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// normalizeMongoURI 修复 mongodb URI 密码段含保留字符但未 URL-encode 的常见情况。
//
// MongoDB 官方文档明确要求 username/password 里的 `@ / ? # [ ] %` 必须 URL-encode,
// 但 driver 实际是按 RFC3986 严格解析,其他保留字 / unsafe char(如 `< > ^ " \ { | }`)
// 也会触发 parse error。用户在 wizard 直接粘贴明文密码极常见(mongosh / Compass 容忍
// 未编码 → 用户以为不需要),代码侧主动修一遍,免得 mcp 启动报"invalid connection string"。
//
// 算法:scheme:// 之后找最后一个 @ 作 host 起点,该 @ 之前的第一个 : 作 user/pass 分割,
//      pass 段每字符过一遍:已编码的 %xx 整体跳,其他保留字 / 非 ASCII / 控制字符 → %XX 编码。
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
	idx := strings.Index(uri, "://")
	if idx < 0 {
		return uri
	}
	rest := uri[idx+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return uri // 没 userinfo → 没认证场景,不动
	}
	hostAndAfter := rest[at+1:] // host[:port][/path][?query]
	slashIdx := strings.Index(hostAndAfter, "/")
	if slashIdx < 0 {
		return uri // 没 path 段(mongodb://user:pass@host) → 没指定 db,默认走 admin,不用加
	}
	pathAndQuery := hostAndAfter[slashIdx+1:]
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

// containsParam 检查 query string 里是否含名为 name 的参数(`name=...` 或 `name&` 形式)。
func containsParam(query, name string) bool {
	for _, pair := range strings.Split(query, "&") {
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

	// IncludeRawObsCurl:写入 elk 的 "curl 占位" 条目(elk 无独立 MCP,只记 URL 让 agent 通过
	// curl/HTTP 直查 Kibana/ES)。openclaw 走这条;IDE 没"代理 curl 调 API"的运行时,所以不写。
	// 注:jaeger 已在 2026-05 升级到 traceloop/opentelemetry-mcp(uvx),不再走 curl 占位,
	// 4 家 target 都注册;本开关只剩 elk 还在用。
	IncludeRawObsCurl bool
}

// BuildMCPServers 按 cfg.Infrastructure 派生 {server_key: spec} 扁平 map。
// 调用方:
//   - install_native_mcp.go(IDE)→ 把返回值 merge 进 settings["mcpServers"]
//   - install_native_openclaw_mcp.go → 把返回值 merge 进 root["mcp"]["servers"]
//
// get(envVarName) 由调用方提供:从 creds map / 老 .env merge 后的合并视图取值。返回 ""
// 表示该字段没填,IDE 模式下整条字段会被 prune(见 PruneEmpty)。
func BuildMCPServers(cfg *config.SystemConfig, opts MCPBuildOptions, get func(string) string) map[string]any {
	servers := map[string]any{}
	envs := cfg.Environments

	keyFor := func(prefix, sourceID, envID string) string {
		return mcpKeyForAgent(opts.AgentID, prefix, sourceID, envID)
	}
	keyFixed := func(name string) string {
		if opts.AgentID == "" {
			return name
		}
		return opts.AgentID + "-" + name
	}

	// envBlock 处理 PruneEmpty:opts.PruneEmpty=true 时把 value=="" 的 entry 删掉。
	envBlock := func(m map[string]any) map[string]any {
		if !opts.PruneEmpty {
			return m
		}
		for k, v := range m {
			if s, ok := v.(string); ok && s == "" {
				delete(m, k)
			}
		}
		return m
	}

	// nacos per (source × env):多源 + 每 env 一个独立 MCP 实例
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range envs {
			servers[keyFor("nacos", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env": envBlock(map[string]any{
					"NACOS_ADDR":     get(envVar("CC_ADDR", cc.ID, e.ID)),
					"NACOS_USERNAME": get(envVar("CC_USER", cc.ID, e.ID)),
					"NACOS_PASSWORD": get(envVar("CC_PASS", cc.ID, e.ID)),
				}),
			}
		}
	}

	// grafana / loki 共用同一个 mcp-grafana 二进制(loki 走 grafana datasource API)。
	// command 写占位 sentinel,IDE install 时替换成 <root>/bin/mcp-grafana 绝对路径;
	// 详见 ensure_mcp_grafana.go 顶部的"为什么不用 npx"说明。
	grafanaBin := generator.CodexPlaceholderGrafanaBin
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[keyFor("grafana", "", e.ID)] = map[string]any{
				"command": grafanaBin,
				"args": []any{
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": envBlock(map[string]any{
					"GRAFANA_URL":      get("GRAFANA_URL_" + up),
					"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
					"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
				}),
			}
		}
	}

	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[keyFor("loki", "", e.ID)] = map[string]any{
				"command": grafanaBin,
				"args": []any{
					"--disable-search", "--disable-dashboard", "--disable-datasource",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": envBlock(map[string]any{
					"GRAFANA_URL":      get("GRAFANA_URL_" + up),
					"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
					"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
				}),
			}
		}
	}

	// jaeger:用 traceloop/opentelemetry-mcp(uvx)真 mcp,4 家平台都注册(跟数据层 mcp 同款思路 —
	// 让 AI 直接 tool_use 调,不用让 AI 自己拼 jaeger /api/traces HTTP curl)。
	// 老路径(opts.IncludeRawObsCurl 控制 jaeger 走 curl 占位)被替换。
	// stdio 干净,BACKEND_TYPE=jaeger / BACKEND_URL=<JAEGER_URL_<env>> 指向 jaeger query 端口(默认 16686)。
	// PruneEmpty 模式下:JAEGER_URL_<env> 没填则 BACKEND_URL 空 → 整个 env block 被剔 → mcp 启动失败被 IDE 自动跳。
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			jurl := get("JAEGER_URL_" + up)
			if jurl == "" && opts.PruneEmpty {
				continue
			}
			servers[keyFor("jaeger", "", e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"opentelemetry-mcp"},
				"env": envBlock(map[string]any{
					"BACKEND_TYPE": "jaeger",
					"BACKEND_URL":  jurl,
				}),
			}
		}
	}

	// elk 仍走 curl 占位(无成熟独立 mcp,Elastic 官方 mcp-server-elasticsearch 走 ES API 不接 Kibana)
	if opts.IncludeRawObsCurl {
		if cfg.Infrastructure.Observability.ELK.Enabled {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				servers[keyFor("elk", "", e.ID)] = map[string]any{
					"command": "curl",
					"args":    []any{},
					"env": map[string]any{
						"KIBANA_URL":  get("KIBANA_URL_" + up),
						"ES_URL":      get("ELK_ES_URL_" + up),
						"ES_USERNAME": get("ELK_USERNAME"),
						"ES_PASSWORD": get("ELK_PASSWORD"),
					},
					"_note": "ELK 无独立 MCP；此条目仅记录 URL 供 agent 直查 ES API",
				}
			}
		}
	}

	// 数据层 MCP per (data_store_type, env):wizard 用 DS_TOOL_SPECS 收集每家 + 每环境的
	// 连接串 env vars(如 MONGODB_URI_DEV / POSTGRES_DSN_DEV / ES_URL_DEV ...),
	// useDeployFlow.buildOpenclawCreds 把这些 env vars 写到 install creds map。
	// 这里读对应 env var,注册成预启动 mcp server,让 AI 能直接 tool_use 调而不用读 SKILL.md
	// 跑 mongosh / psql 这种"AI 不一定会主动跑"的 CLI。
	//
	// 阶段 1 覆盖 6 家(分两路写法):
	//   接整 URI 直接传入位置参数 / env:
	//     - mongodb:        npx mcp-mongo-server <URI>       (位置参数)
	//     - postgresql:     npx server-postgres <PG_URL>     (位置参数)
	//     - elasticsearch:  npx mcp-server-elasticsearch     (env: ES_URL/USERNAME/PASSWORD)
	//     - redis:          npx server-redis-mcp <URL>       (位置参数)
	//   要拆字段(npm/pip 包不接整 URL,只接 host/port/user/pass):
	//     - mysql:          parseMySQLDSN → MYSQL_HOST/PORT/USER/PASS/DB env
	//     - clickhouse:     parseConnURL  → CLICKHOUSE_HOST/PORT/USER/PASSWORD/DATABASE env
	//
	// 阶段 2 待做(无成熟 npm mcp,要自己写 binary):
	//   - kafka / rabbitmq / rocketmq
	//
	// PruneEmpty=true 模式下空 env 段会被剔,如果用户没填 endpoint(env-vars 模式没填 /
	// 走 from_config_center 模式),mcp server 启动时拿不到 URI 直接退出 — 不会污染 IDE。
	// dsEndpointFor 在 install creds 拿不到该 env var 时,fallback 到 yaml endpoints[]
	// 派生该 (ds, env) 的代表连接串。同一 env 下若有多个 service 共用同一数据层,取第一条
	// 非空的 — 大多数项目里多个 service 走同一 ES/Mongo 集群,代表 endpoint 即可。
	// 用户走"代码扫描自动填 endpoints[]"路径而没单独在 wizard 输 env vars 时,这条 fallback
	// 让老 yaml 直接能用,不用重跑 wizard。
	dsEndpointFor := func(ds config.DataStore, envID string) *config.DataStoreEndpoint {
		for i := range ds.Endpoints {
			ep := &ds.Endpoints[i]
			if ep.Env == envID && (ep.URL != "" || ep.URI != "" || ep.DSN != "" || ep.Brokers != "") {
				return ep
			}
		}
		return nil
	}
	// firstNonEmpty 串联多源取第一个非空。install creds 优先(env-vars 模式),fallback yaml。
	firstNonEmpty := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}

	for _, ds := range cfg.Infrastructure.DataStores {
		if !ds.Enabled {
			continue
		}
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			ep := dsEndpointFor(ds, e.ID) // 可能 nil(env-vars 模式 / 用户没扫到 endpoints)
			switch ds.Type {
			case "mongodb":
				var epURI string
				if ep != nil {
					epURI = ep.URI
				}
				uri := firstNonEmpty(get("MONGODB_URI_"+up), epURI)
				if uri == "" && opts.PruneEmpty {
					continue // 没填连接串 → 跳过(避免注册一条永远启动失败的 mcp)
				}
				// 修密码段未 URL-encode 的保留字符 — mcp-mongo-server 严格按 RFC3986
				// 解析,密码含 < ] ^ % @ : / ? # [ ] 等字面字符 → connection string parse error。
				uri = normalizeMongoURI(uri)
				servers[keyFor("mongodb", "", e.ID)] = map[string]any{
					"command": "npx",
					"args":    []any{"-y", "mcp-mongo-server", uri, "--read-only"},
				}
			case "postgresql":
				// FIXME: @modelcontextprotocol/server-postgres 已于 2025-07 deprecated
				// (官方维护者明确 archive,不再修)。功能仍在(READ ONLY transaction
				// 包裹所有查询,readonly 默认),近期能跑 — 后续要迁到社区活跃 fork,
				// 候选:@henkey/postgres-mcp-server 或 @ahmedmustahid/postgres-mcp-server。
				var epDSN string
				if ep != nil {
					epDSN = ep.DSN
				}
				dsn := firstNonEmpty(get("POSTGRES_DSN_"+up), epDSN)
				if dsn == "" && opts.PruneEmpty {
					continue
				}
				servers[keyFor("postgresql", "", e.ID)] = map[string]any{
					"command": "npx",
					"args":    []any{"-y", "@modelcontextprotocol/server-postgres", dsn},
				}
			case "elasticsearch":
				var epURL, epUser, epPass string
				if ep != nil {
					epURL, epUser, epPass = ep.URL, ep.User, ep.Pass
				}
				esURL := firstNonEmpty(get("ES_URL_"+up), epURL)
				if esURL == "" && opts.PruneEmpty {
					continue
				}
				servers[keyFor("elasticsearch", "", e.ID)] = map[string]any{
					"command": "npx",
					"args":    []any{"-y", "@elastic/mcp-server-elasticsearch"},
					"env": envBlock(map[string]any{
						"ES_URL":      esURL,
						"ES_USERNAME": firstNonEmpty(get("ES_USER_"+up), epUser),
						"ES_PASSWORD": firstNonEmpty(get("ES_PASS_"+up), epPass),
						// 禁用 elastic-otel-node 自动监控 — 否则它启动时往 stdout 打 banner JSON
						// (`{"name":"elastic-otel-node",...}`),污染 mcp stdio JSON-RPC 协议 →
						// "handshaking with MCP server failed: connection closed: initialize response"。
						// 实测设这个 env 后 stdout 干净,mcp client 能正常收 initialize response。
						"OTEL_SDK_DISABLED": "true",
					}),
				}
			case "redis":
				// @gongrzhe/server-redis-mcp 接 URL 位置参数,不用拆字段。
				// 钉死 1.0.0:这个包目前只发过 1.0.0 一个版本(2024-12);如果作者将来发
				// 不兼容版本(arg 顺序变 / 改 env-only),@latest 会无声 break,钉版本更稳。
				var epURL string
				if ep != nil {
					epURL = ep.URL
				}
				redisURL := firstNonEmpty(get("REDIS_URL_"+up), epURL)
				if redisURL == "" && opts.PruneEmpty {
					continue
				}
				servers[keyFor("redis", "", e.ID)] = map[string]any{
					"command": "npx",
					"args":    []any{"-y", "@gongrzhe/server-redis-mcp@1.0.0", redisURL},
				}
			case "mysql":
				// @benborla29/mcp-server-mysql 接 env(MYSQL_HOST/PORT/USER/PASS),
				// 用户填的是 go-sql-driver DSN(`user:pass@tcp(host:port)/db`)→ 拆字段喂 env。
				var epDSN string
				if ep != nil {
					epDSN = ep.DSN
				}
				dsn := firstNonEmpty(get("MYSQL_DSN_"+up), epDSN)
				if dsn == "" && opts.PruneEmpty {
					continue
				}
				host, port, user, pass, db := parseMySQLDSN(dsn)
				if port == "" {
					port = "3306"
				}
				servers[keyFor("mysql", "", e.ID)] = map[string]any{
					"command": "npx",
					"args":    []any{"-y", "@benborla29/mcp-server-mysql"},
					"env": envBlock(map[string]any{
						"MYSQL_HOST": host,
						"MYSQL_PORT": port,
						"MYSQL_USER": user,
						"MYSQL_PASS": pass,
						"MYSQL_DB":   db,
					}),
				}
			case "clickhouse":
				// uvx mcp-clickhouse(python pip 包)接 env(CLICKHOUSE_HOST/PORT/USER/PASSWORD)。
				// URL 形如 http(s)://[user:pass@]host:port/[db] → 拆字段。https → secure=true。
				var epURL, epUser, epPass string
				if ep != nil {
					epURL, epUser, epPass = ep.URL, ep.User, ep.Pass
				}
				chURL := firstNonEmpty(get("CLICKHOUSE_URL_"+up), epURL)
				if chURL == "" && opts.PruneEmpty {
					continue
				}
				host, port, urlUser, urlPass, db := parseConnURL(chURL)
				secure := strings.HasPrefix(strings.ToLower(chURL), "https://")
				if port == "" {
					if secure {
						port = "8443"
					} else {
						port = "8123"
					}
				}
				// URL 没带凭证就 fallback 到独立字段(用户大概率走 USER/PASS 表单填)。
				// 优先级:URL 内嵌 > install creds CLICKHOUSE_USER_<env> > yaml endpoint user 字段。
				user := urlUser
				if user == "" {
					user = firstNonEmpty(get("CLICKHOUSE_USER_"+up), epUser)
				}
				pass := urlPass
				if pass == "" {
					pass = firstNonEmpty(get("CLICKHOUSE_PASS_"+up), epPass)
				}
				servers[keyFor("clickhouse", "", e.ID)] = map[string]any{
					"command": "uvx",
					"args":    []any{"mcp-clickhouse"},
					"env": envBlock(map[string]any{
						"CLICKHOUSE_HOST":     host,
						"CLICKHOUSE_PORT":     port,
						"CLICKHOUSE_USER":     user,
						"CLICKHOUSE_PASSWORD": pass,
						"CLICKHOUSE_DATABASE": db,
						"CLICKHOUSE_SECURE":   strconv.FormatBool(secure),
					}),
				}
			}
		}
	}

	// messaging:lark
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			servers[keyFixed("lark-openapi")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuite/lark-openapi-mcp"},
				"env": envBlock(map[string]any{
					"APP_ID":     get("LARK_APP_ID"),
					"APP_SECRET": get("LARK_APP_SECRET"),
				}),
			}
			break
		}
	}

	// project tracking:feishu_project
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			servers[keyFixed("FeishuProjectMcp")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@lark-project/mcp", "--domain", "https://project.feishu.cn"},
				"env": envBlock(map[string]any{
					"MCP_USER_TOKEN": get("MCP_USER_TOKEN"),
				}),
			}
			break
		}
	}

	return servers
}
