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
	"strconv"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

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
	b.buildNacos(servers)
	b.buildGrafana(servers)
	b.buildJaeger(servers)
	b.buildELK(servers)
	b.buildDataStores(servers)
	b.buildLark(servers)
	b.buildFeishuProject(servers)
	return servers
}

// buildNacos:nacos per (source × env),多源 + 每 env 一个独立 MCP 实例
func (b *mcpBuilder) buildNacos(servers map[string]any) {
	for _, cc := range b.cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range b.cfg.Environments {
			servers[b.keyFor("nacos", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env": b.envBlock(map[string]any{
					"NACOS_ADDR":     b.get(envVar("CC_ADDR", cc.ID, e.ID)),
					"NACOS_USERNAME": b.get(envVar("CC_USER", cc.ID, e.ID)),
					"NACOS_PASSWORD": b.get(envVar("CC_PASS", cc.ID, e.ID)),
				}),
			}
		}
	}
}

// buildGrafana:grafana / loki / prom 都走 mcp-grafana-npx(社区 wrapper,首次跑时自动下
// grafana/mcp-grafana 官方 Go 二进制到 npm 缓存,exec 同款进程;stdout 干净不污染 stdio)。
// loki 跟 grafana 共用同一个底层二进制,只是多 `--disable-search/dashboard/datasource` 把它
// 收成"只剩 Loki/Prom 查询"。
//
// 历史:之前我们自己下 grafana 官方 Go 二进制到 <root>/bin/mcp-grafana(占位 sentinel
// 替换路径),200 行 ensure_mcp_grafana.go + 4 平台各 30MiB 冗余。换 npx wrapper 后:
//   - 跟其他 7 家 npx MCP 同款代码路径,统一
//   - npm 缓存跨 4 IDE 共享(~/.npm/_npx/<hash>/),消除冗余
//   - 删 200 行 ensure 逻辑 + 占位 / placeholder 替换 / npx fallback / uninstall codex bin 清理
//
// 代价:依赖第三方 6.7KB wrapper(animalnots,Apache-2.0),只是"下载+exec"几十行,风险小。
//
// 上游 mcp-grafana README:GRAFANA_API_KEY **已标 deprecated**,推 GRAFANA_SERVICE_ACCOUNT_TOKEN
// (Grafana 9.1+ 用 service account token 替代 API key,值跟 token 字符串完全兼容,
// 改名是为强调"用新 token API 创建,不用老 admin API key")。我们 wizard 字段叫
// GRAFANA_API_KEY_<env>(用户语境上更直白),发到 mcp 时换成现行规范名 SERVICE_ACCOUNT_TOKEN。
func (b *mcpBuilder) buildGrafana(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.Grafana.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		servers[b.keyFor("grafana", "", e.ID)] = map[string]any{
			"command": "npx",
			"args": []any{"-y", "mcp-grafana-npx",
				"--disable-incident", "--disable-alerting", "--disable-oncall",
				"--disable-admin", "--disable-sift", "--disable-pyroscope",
			},
			"env": b.envBlock(b.grafanaAuthEnv(up)),
		}
	}
}

// grafanaAuthEnv 二选一:有 API key/SAT 时走 GRAFANA_SERVICE_ACCOUNT_TOKEN,空则回落 basic auth。
func (b *mcpBuilder) grafanaAuthEnv(up string) map[string]any {
	if k := b.get("GRAFANA_API_KEY_" + up); k != "" {
		return map[string]any{
			"GRAFANA_URL":                   b.get("GRAFANA_URL_" + up),
			"GRAFANA_SERVICE_ACCOUNT_TOKEN": k,
		}
	}
	return map[string]any{
		"GRAFANA_URL":      b.get("GRAFANA_URL_" + up),
		"GRAFANA_USERNAME": b.get("GRAFANA_USER_" + up),
		"GRAFANA_PASSWORD": b.get("GRAFANA_PASS_" + up),
	}
}

// 历史上这里有过单独的 loki MCP(同款 mcp-grafana 二进制只是多 --disable-search/dashboard/
// datasource 把工具集瘦身)。但本质是 grafana MCP 的严格子集 — query_loki_logs/patterns/stats 等
// 工具 grafana MCP 都已暴露,起两份相同进程纯属浪费 spawn + zod schema 注册时间。
// 已删,保持 loki/prom 永远走 grafana MCP 单一路径。yaml 里 observability.loki.enabled
// 仅决定 routing skill 模板里的 LOKI_URL_<env> CLI fallback 提示(当 mcp 不可用时)。
// 同款理由:prometheus 一直没独立 MCP(社区无成熟 prom-only mcp 包),也走 grafana MCP。
// validate 阶段强制 Loki/Prom 启用 ⇒ Grafana 必启用,见 validate_observability_grafana_required.go。

// buildJaeger:用 traceloop/opentelemetry-mcp(uvx)真 mcp,4 家平台都注册(跟数据层 mcp 同款思路 —
// 让 AI 直接 tool_use 调,不用让 AI 自己拼 jaeger /api/traces HTTP curl)。
// 老路径(opts.IncludeRawObsCurl 控制 jaeger 走 curl 占位)被替换。
// stdio 干净,BACKEND_TYPE=jaeger / BACKEND_URL=<JAEGER_URL_<env>> 指向 jaeger query 端口(默认 16686)。
// PruneEmpty 模式下:JAEGER_URL_<env> 没填则 BACKEND_URL 空 → 整个 env block 被剔 → mcp 启动失败被 IDE 自动跳。
func (b *mcpBuilder) buildJaeger(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.Jaeger.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		jurl := b.get("JAEGER_URL_" + up)
		if jurl == "" && b.opts.PruneEmpty {
			continue
		}
		servers[b.keyFor("jaeger", "", e.ID)] = map[string]any{
			"command": "uvx",
			"args":    []any{"opentelemetry-mcp"},
			"env": b.envBlock(map[string]any{
				"BACKEND_TYPE": "jaeger",
				"BACKEND_URL":  jurl,
			}),
		}
	}
}

// buildELK 走 Elastic 官方 @elastic/mcp-server-elasticsearch(跟数据层 elasticsearch 同款,
// 区别只在 env vars 命名空间:ELK_* 防跟数据层 ES 字段串)。Kibana UI 由 agent 通过
// SKILL.md 拼 deeplink,不进 MCP env(本 MCP 只接 ES API)。
// OTEL_SDK_DISABLED=true 防 elastic-otel-node 自动注入往 stdout 打 banner JSON 污染
// stdio JSON-RPC(同数据层 ES 那条注释)。
func (b *mcpBuilder) buildELK(servers map[string]any) {
	if !b.cfg.Infrastructure.Observability.ELK.Enabled {
		return
	}
	for _, e := range b.cfg.Environments {
		up := strings.ToUpper(e.ID)
		esURL := b.get("ELK_ES_URL_" + up)
		if esURL == "" && b.opts.PruneEmpty {
			continue // 没填 ES URL → 跳过(避免注册一条永远启动失败的 mcp)
		}
		servers[b.keyFor("elk", "", e.ID)] = map[string]any{
			"command": "npx",
			"args":    []any{"-y", "@elastic/mcp-server-elasticsearch"},
			"env": b.envBlock(map[string]any{
				"ES_URL":            esURL,
				"ES_USERNAME":       b.get("ELK_USERNAME"),
				"ES_PASSWORD":       b.get("ELK_PASSWORD"),
				"OTEL_SDK_DISABLED": "true",
			}),
		}
	}
}

// buildDataStores 数据层 MCP per (data_store_type, env)。wizard 用 DS_TOOL_SPECS 收集每家 +
// 每环境的连接串 env vars(如 MONGODB_URI_DEV / POSTGRES_DSN_DEV / ES_URL_DEV ...),
// useDeployFlow.buildOpenclawCreds 把这些 env vars 写到 install creds map。
// 这里读对应 env var,注册成预启动 mcp server,让 AI 能直接 tool_use 调而不用读 SKILL.md
// 跑 mongosh / psql 这种"AI 不一定会主动跑"的 CLI。
//
// 阶段 1 覆盖 6 家:
//
//	接整 URI:
//	  - mongodb:        npx mcp-mongo-server --read-only           (env: MCP_MONGODB_URI)
//	  - postgresql:     npx server-postgres <DSN>                  (位置参数,包不接 env)
//	  - redis:          npx server-redis-mcp <URL>                 (位置参数,包不接 env)
//	  - elasticsearch:  npx mcp-server-elasticsearch               (env: ES_URL/USERNAME/PASSWORD)
//	要拆字段(npm/pip 包不接整 URL,只接 host/port/user/pass):
//	  - mysql:          parseMySQLDSN → MYSQL_HOST/PORT/USER/PASS/DB env
//	  - clickhouse:     parseConnURL  → CLICKHOUSE_HOST/PORT/USER/PASSWORD/DATABASE env
//
// 历史:本会话曾尝试给 pg/redis(只接位置参数)套一层 `tshoot mcp-launch` launcher
// 把凭据藏 env 里,但 desktop 二进制被 install 选作 launcher 路径时会让 Claude 启动
// MCP 时打开一堆 wails 窗口("启动一堆工作台"),且 launcher 多一层 fork 没解决根本问题
// (上游包不接 env)。改回直接传位置参数 — pg/redis 凭据落 IDE config args 字段是已知
// trade-off,直到上游包支持 env 或换包(@henkey/postgres-mcp-server 等)再迁。
//
// 阶段 2 待做:
//   - kafka(社区没 npx/uvx 分发,候选 brew 装 CefBoud/kafka-mcp-server,带 --read-only flag)
//   - rabbitmq(可走 uvx amazon-mq/mcp-server-rabbitmq,默认只读)
//
// PruneEmpty=true 模式下空 env 段会被剔,如果用户没填 endpoint(env-vars 模式没填 /
// 走 from_config_center 模式),mcp server 启动时拿不到 URI 直接退出 — 不会污染 IDE。
func (b *mcpBuilder) buildDataStores(servers map[string]any) {
	for _, ds := range b.cfg.Infrastructure.DataStores {
		if !ds.Enabled {
			continue
		}
		for _, e := range b.cfg.Environments {
			// 按连接串 dedupe:同一 (env, type) 下,同 URI 视为同 cluster,共享一个 MCP;
			// 不同 URI 注册成多个 MCP(支持"一个 env 里多个 mongodb cluster"场景)。
			// dedupe 后只有 1 个 unique → sourceID 留空,MCP key 退化成无 source 段(跟老用户行为一致)。
			unique := dsEndpointsUnique(ds, e.ID)
			single := len(unique) <= 1
			for _, ep := range unique {
				sourceID := ""
				if !single {
					sourceID = ep.sourceID
				}
				switch ds.Type {
				case "mongodb":
					b.buildMongoDB(servers, ep.endpoint, sourceID, e.ID)
				case "postgresql":
					b.buildPostgreSQL(servers, ep.endpoint, sourceID, e.ID)
				case "elasticsearch":
					b.buildDataES(servers, ep.endpoint, sourceID, e.ID)
				case "redis":
					b.buildRedis(servers, ep.endpoint, sourceID, e.ID)
				case "mysql":
					b.buildMySQL(servers, ep.endpoint, sourceID, e.ID)
				case "clickhouse":
					b.buildClickHouse(servers, ep.endpoint, sourceID, e.ID)
				}
			}
			// env-vars 模式 / 用户没扫到 endpoints → unique 空,但还得跑一遍(让 buildXxx
			// 从 install creds env 拿连接串;PruneEmpty 模式下空 env 段会被剔)
			if len(unique) == 0 {
				switch ds.Type {
				case "mongodb":
					b.buildMongoDB(servers, nil, "", e.ID)
				case "postgresql":
					b.buildPostgreSQL(servers, nil, "", e.ID)
				case "elasticsearch":
					b.buildDataES(servers, nil, "", e.ID)
				case "redis":
					b.buildRedis(servers, nil, "", e.ID)
				case "mysql":
					b.buildMySQL(servers, nil, "", e.ID)
				case "clickhouse":
					b.buildClickHouse(servers, nil, "", e.ID)
				}
			}
		}
	}
}

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
func (b *mcpBuilder) buildMongoDB(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epURI string
	if ep != nil {
		epURI = ep.URI
	}
	uri := firstNonEmpty(b.get(envVar("MONGODB_URI", sourceID, envID)), epURI)
	if uri == "" && b.opts.PruneEmpty {
		return // 没填连接串 → 跳过(避免注册一条永远启动失败的 mcp)
	}
	// 修密码段未 URL-encode 的保留字符 — mcp-mongo-server 严格按 RFC3986
	// 解析,密码含 < ] ^ % @ : / ? # [ ] 等字面字符 → connection string parse error。
	uri = normalizeMongoURI(uri)
	// mcp-mongo-server v2+ 支持 MCP_MONGODB_URI env(2.x 起);凭据走 env IDE
	// config args 字段不残留。
	servers[b.keyFor("mongodb", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "mcp-mongo-server", "--read-only"},
		"env": b.envBlock(map[string]any{
			"MCP_MONGODB_URI": uri,
		}),
	}
}

// FIXME: @modelcontextprotocol/server-postgres 已于 2025-07 deprecated
// (官方维护者明确 archive,不再修)。功能仍在(READ ONLY transaction
// 包裹所有查询,readonly 默认),近期能跑。
//
// 迁移调研(2026-05):
//   - @henkey/postgres-mcp-server v1.0.5(env: POSTGRES_CONNECTION_STRING):**没有 read-only 模式**,
//     直接换会丢失原 RO transaction 包裹 → AI 可能误执行 DELETE/UPDATE。
//   - @ahmedmustahid/postgres-mcp-server:接 args 不接 env(走 launcher 还要 sh 转义),不优。
//
// 建议路径:用 henkey 包但在 PG 端建 readonly role,DSN 里只给该 role 的凭据 —
// 安全责任从 mcp 侧移到 PG 侧。yaml schema 要相应改"必填 readonly user"才能换。
// 暂保持现包(archived 但能跑)。
func (b *mcpBuilder) buildPostgreSQL(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epDSN string
	if ep != nil {
		epDSN = ep.DSN
	}
	dsn := firstNonEmpty(b.get(envVar("POSTGRES_DSN", sourceID, envID)), epDSN)
	if dsn == "" && b.opts.PruneEmpty {
		return
	}
	// 上游包只接位置参数,凭据落 args(可在 ~/.claude.json 里看到)— 已知 trade-off。
	// envBlock(空 map) 仍然会被注入 OTEL_SDK_DISABLED=true 防 stdout 污染。
	servers[b.keyFor("postgresql", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@modelcontextprotocol/server-postgres", dsn},
		"env":     b.envBlock(map[string]any{}),
	}
}

// buildDataES 数据层 elasticsearch(跟 ELK obs 子段同款包,但不同 env 命名空间)。
func (b *mcpBuilder) buildDataES(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epURL, epUser, epPass string
	if ep != nil {
		epURL, epUser, epPass = ep.URL, ep.User, ep.Pass
	}
	esURL := firstNonEmpty(b.get(envVar("ES_URL", sourceID, envID)), epURL)
	if esURL == "" && b.opts.PruneEmpty {
		return
	}
	servers[b.keyFor("elasticsearch", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@elastic/mcp-server-elasticsearch"},
		"env": b.envBlock(map[string]any{
			"ES_URL":      esURL,
			"ES_USERNAME": firstNonEmpty(b.get(envVar("ES_USER", sourceID, envID)), epUser),
			"ES_PASSWORD": firstNonEmpty(b.get(envVar("ES_PASS", sourceID, envID)), epPass),
			// 禁用 elastic-otel-node 自动监控 — 否则它启动时往 stdout 打 banner JSON
			// (`{"name":"elastic-otel-node",...}`),污染 mcp stdio JSON-RPC 协议 →
			// "handshaking with MCP server failed: connection closed: initialize response"。
			// 实测设这个 env 后 stdout 干净,mcp client 能正常收 initialize response。
			"OTEL_SDK_DISABLED": "true",
		}),
	}
}

// buildRedis:@gongrzhe/server-redis-mcp 接 URL 位置参数,不用拆字段。
// 钉死 1.0.0:这个包目前只发过 1.0.0 一个版本(2024-12);如果作者将来发
// 不兼容版本(arg 顺序变 / 改 env-only),@latest 会无声 break,钉版本更稳。
func (b *mcpBuilder) buildRedis(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epURL string
	if ep != nil {
		epURL = ep.URL
	}
	redisURL := firstNonEmpty(b.get(envVar("REDIS_URL", sourceID, envID)), epURL)
	if redisURL == "" && b.opts.PruneEmpty {
		return
	}
	// 同 pg:上游 v1.0.0 只接位置参数,凭据落 args。
	servers[b.keyFor("redis", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@gongrzhe/server-redis-mcp@1.0.0", redisURL},
		"env":     b.envBlock(map[string]any{}),
	}
}

// buildMySQL:@benborla29/mcp-server-mysql 接 env(MYSQL_HOST/PORT/USER/PASS),
// 用户填的是 go-sql-driver DSN(`user:pass@tcp(host:port)/db`)→ 拆字段喂 env。
func (b *mcpBuilder) buildMySQL(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epDSN string
	if ep != nil {
		epDSN = ep.DSN
	}
	dsn := firstNonEmpty(b.get(envVar("MYSQL_DSN", sourceID, envID)), epDSN)
	if dsn == "" && b.opts.PruneEmpty {
		return
	}
	host, port, user, pass, db := parseMySQLDSN(dsn)
	if port == "" {
		port = "3306"
	}
	servers[b.keyFor("mysql", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@benborla29/mcp-server-mysql"},
		"env": b.envBlock(map[string]any{
			"MYSQL_HOST": host,
			"MYSQL_PORT": port,
			"MYSQL_USER": user,
			"MYSQL_PASS": pass,
			"MYSQL_DB":   db,
		}),
	}
}

// buildClickHouse:uvx mcp-clickhouse(python pip 包)接 env(CLICKHOUSE_HOST/PORT/USER/PASSWORD)。
// URL 形如 http(s)://[user:pass@]host:port/[db] → 拆字段。https → secure=true。
func (b *mcpBuilder) buildClickHouse(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epURL, epUser, epPass string
	if ep != nil {
		epURL, epUser, epPass = ep.URL, ep.User, ep.Pass
	}
	chURL := firstNonEmpty(b.get(envVar("CLICKHOUSE_URL", sourceID, envID)), epURL)
	if chURL == "" && b.opts.PruneEmpty {
		return
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
	// 优先级:URL 内嵌 > install creds CLICKHOUSE_USER_<sourceID>_<env> > yaml endpoint user 字段。
	user := urlUser
	if user == "" {
		user = firstNonEmpty(b.get(envVar("CLICKHOUSE_USER", sourceID, envID)), epUser)
	}
	pass := urlPass
	if pass == "" {
		pass = firstNonEmpty(b.get(envVar("CLICKHOUSE_PASS", sourceID, envID)), epPass)
	}
	servers[b.keyFor("clickhouse", sourceID, envID)] = map[string]any{
		"command": "uvx",
		"args":    []any{"mcp-clickhouse"},
		"env": b.envBlock(map[string]any{
			"CLICKHOUSE_HOST":     host,
			"CLICKHOUSE_PORT":     port,
			"CLICKHOUSE_USER":     user,
			"CLICKHOUSE_PASSWORD": pass,
			"CLICKHOUSE_DATABASE": db,
			"CLICKHOUSE_SECURE":   strconv.FormatBool(secure),
		}),
	}
}

// buildLark messaging:lark — 上游正式包名是 @larksuiteoapi/lark-mcp(注意 oapi 不是 suite),
// 且 binary 是 commander 多子命令,启动 mcp server 必须显式 `mcp` 子命令(没有就只是
// CLI 工具 exit)。env: process.env.APP_ID / APP_SECRET(在 dist/utils/constants.js
// 里写死,不接 LARK_APP_* 前缀)。
//
// `-t preset.im.default` 把工具集从默认 19 个(preset.default = IM + Bitable + Doc +
// Contact 全套)缩到 5 个(IM 群消息相关)。排障机器人对飞书的真实需求是"发故障快报
// 到群" + 偶尔查群信息,IM 子集足够。19 → 5 工具:启动快、Claude /mcp 面板列表清爽、
// LLM tools[] context 也轻不少(每个工具一份 zod-to-json-schema 描述都不便宜)。
//
// LARK_DOMAIN env(可选):海外用户填 https://open.larksuite.com,留空 → lark-mcp
// 走默认 https://open.feishu.cn(国内飞书 endpoint)。lark-mcp 源码
// `package/dist/utils/constants.js:29` 读 process.env.LARK_DOMAIN,我们透传即可。
func (b *mcpBuilder) buildLark(servers map[string]any) {
	for _, m := range b.cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			servers[b.keyFixed("lark-openapi")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuiteoapi/lark-mcp", "mcp", "-t", "preset.im.default"},
				"env": b.envBlock(map[string]any{
					"APP_ID":      b.get("LARK_APP_ID"),
					"APP_SECRET":  b.get("LARK_APP_SECRET"),
					"LARK_DOMAIN": b.get("LARK_DOMAIN"),
				}),
			}
			return
		}
	}
}

// buildFeishuProject project tracking:feishu_project
func (b *mcpBuilder) buildFeishuProject(servers map[string]any) {
	for _, p := range b.cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			servers[b.keyFixed("FeishuProjectMcp")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@lark-project/mcp", "--domain", "https://project.feishu.cn"},
				"env": b.envBlock(map[string]any{
					"MCP_USER_TOKEN": b.get("MCP_USER_TOKEN"),
				}),
			}
			return
		}
	}
}
