// install_native_mcp_common_test.go —— mysql DSN / 通用 URL 拆字段 parser 的护栏测试。
//
// 这两个 parser 没引外部依赖(故意 — 整个工程没用 mysql client,引 go-sql-driver/mysql
// 不划算;net/url 标准库够用)。所以更需要单测把住:
//   - mysql DSN 各种 user/pass/db/缺省 port 组合的边界
//   - URL 解析 redis/clickhouse 这种带 path-as-db / scheme 拆出的 secure 判断
//
// regression 风险点:有人把 strings.Cut 改回 strings.Index 切片可能算错下标。

package agent

import (
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestParseMySQLDSN(t *testing.T) {
	cases := []struct {
		name                       string
		dsn                        string
		host, port, user, pass, db string
	}{
		{
			name: "标准 user:pass@tcp(host:port)/db",
			dsn:  "root:secret@tcp(10.0.0.5:3306)/orders",
			host: "10.0.0.5", port: "3306", user: "root", pass: "secret", db: "orders",
		},
		{
			name: "带查询参数",
			dsn:  "u:p@tcp(db.local:3307)/inv?charset=utf8mb4&parseTime=True",
			host: "db.local", port: "3307", user: "u", pass: "p", db: "inv",
		},
		{
			name: "无密码:user@tcp(...)",
			dsn:  "readonly@tcp(host:3306)/app",
			host: "host", port: "3306", user: "readonly", db: "app",
		},
		{
			name: "无端口:tcp(host)",
			dsn:  "u:p@tcp(host)/db",
			host: "host", user: "u", pass: "p", db: "db",
			// port 留空,调用方默认 3306
		},
		{
			name: "无 db:tcp(host:3306)/",
			dsn:  "u:p@tcp(host:3306)/",
			host: "host", port: "3306", user: "u", pass: "p",
		},
		{
			name: "密码含 @(LastIndex 兜底)",
			dsn:  "u:p@ss@tcp(h:3306)/d",
			host: "h", port: "3306", user: "u", pass: "p@ss", db: "d",
		},
		{name: "空串"},
		{name: "纯垃圾", dsn: "not-a-dsn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, p, u, pw, d := parseMySQLDSN(c.dsn)
			if h != c.host || p != c.port || u != c.user || pw != c.pass || d != c.db {
				t.Errorf("parseMySQLDSN(%q) = host=%q port=%q user=%q pass=%q db=%q;\n  want host=%q port=%q user=%q pass=%q db=%q",
					c.dsn, h, p, u, pw, d, c.host, c.port, c.user, c.pass, c.db)
			}
		})
	}
}

// TestBuildMCPServers_DataStores 端到端验证 6 家数据层在 IDE 模式 (PruneEmpty=true)
// 下输出的 server keys、命令行、关键 env 字段。
//
// 这一层是 wizard env vars(MONGODB_URI_<env> 等)→ install creds map → BuildMCPServers
// 拼到 settings.json mcpServers 的最后一公里。protected against:
//   - 改 ds case 时漏写一家
//   - mcp 包名手抖打错(commit 卡这里)
//   - mysql DSN / clickhouse URL 拆字段 → env 名字漂移
//   - PruneEmpty 把非空字段误剔
func TestBuildMCPServers_DataStores(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{
				{Type: "mongodb", Enabled: true},
				{Type: "postgresql", Enabled: true},
				{Type: "elasticsearch", Enabled: true},
				{Type: "redis", Enabled: true},
				{Type: "mysql", Enabled: true},
				{Type: "clickhouse", Enabled: true},
			},
		},
	}
	creds := map[string]string{
		"MONGODB_URI_DEV":    "mongodb://u:p@m.local:27017/app",
		"POSTGRES_DSN_DEV":   "postgres://u:p@pg.local:5432/app",
		"ES_URL_DEV":         "https://es.local:9200",
		"ES_USER_DEV":        "elastic",
		"ES_PASS_DEV":        "espw",
		"REDIS_URL_DEV":      "redis://default:rpw@r.local:6379/0",
		"MYSQL_DSN_DEV":      "myu:mypw@tcp(my.local:3307)/orders",
		"CLICKHOUSE_URL_DEV": "https://chu:chpw@ch.local:8443/analytics",
	}
	get := func(k string) string { return creds[k] }

	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:    "bot",
		PruneEmpty: true,
	}, get)

	// ── Key 形态:6 家 + IDE mode 加 AgentID 前缀 ──
	wantKeys := []string{
		"bot-mongodb-dev",
		"bot-postgresql-dev",
		"bot-elasticsearch-dev",
		"bot-redis-dev",
		"bot-mysql-dev",
		"bot-clickhouse-dev",
	}
	for _, k := range wantKeys {
		if _, ok := servers[k]; !ok {
			t.Errorf("missing mcp server key %q in output;\n  got keys: %v", k, keysOf(servers))
		}
	}

	// ── mongodb:走 npx mcp-mongo-server,凭据用 MCP_MONGODB_URI env(v2+ 支持) ──
	// 自动 normalize URI 补 authSource=admin。
	mongoSpec := servers["bot-mongodb-dev"].(map[string]any)
	if mongoSpec["command"] != "npx" {
		t.Errorf("mongodb command 应为 npx,实际 %v", mongoSpec["command"])
	}
	if got := argString(mongoSpec); got != "[-y mcp-mongo-server --read-only]" {
		t.Errorf("mongodb args mismatch: %s", got)
	}
	if envOf(mongoSpec)["MCP_MONGODB_URI"] != "mongodb://u:p@m.local:27017/app?authSource=admin" {
		t.Errorf("mongodb MCP_MONGODB_URI env mismatch: %v", envOf(mongoSpec))
	}

	// ── postgres:位置参数接 connection string(server-postgres 默认 readonly transaction)──
	// 上游包不接 env,凭据落 args(已知 trade-off,见 BuildMCPServers 注释)。
	if got := argString(servers["bot-postgresql-dev"]); got != "[-y @modelcontextprotocol/server-postgres postgres://u:p@pg.local:5432/app]" {
		t.Errorf("postgres args mismatch: %s", got)
	}

	// ── redis:钉死 1.0.0 + URL 位置参数(防 @latest 漂移)──
	// 上游包不接 env,凭据落 args。
	if got := argString(servers["bot-redis-dev"]); got != "[-y @gongrzhe/server-redis-mcp@1.0.0 redis://default:rpw@r.local:6379/0]" {
		t.Errorf("redis args mismatch: %s", got)
	}

	// ── elasticsearch:env 段 ES_URL/USERNAME/PASSWORD + 必须禁 OTel(否则 stdout 污染) ──
	esEnv := envOf(servers["bot-elasticsearch-dev"])
	if esEnv["ES_URL"] != "https://es.local:9200" || esEnv["ES_USERNAME"] != "elastic" || esEnv["ES_PASSWORD"] != "espw" {
		t.Errorf("elasticsearch env mismatch: %v", esEnv)
	}
	if esEnv["OTEL_SDK_DISABLED"] != "true" {
		t.Errorf("elasticsearch 必须禁 OTel 防 stdout 污染 mcp 协议 (got OTEL_SDK_DISABLED=%q)", esEnv["OTEL_SDK_DISABLED"])
	}

	// ── mysql:DSN 拆 MYSQL_HOST/PORT/USER/PASS/DB ──
	myEnv := envOf(servers["bot-mysql-dev"])
	if myEnv["MYSQL_HOST"] != "my.local" || myEnv["MYSQL_PORT"] != "3307" ||
		myEnv["MYSQL_USER"] != "myu" || myEnv["MYSQL_PASS"] != "mypw" ||
		myEnv["MYSQL_DB"] != "orders" {
		t.Errorf("mysql env mismatch: %v", myEnv)
	}

	// ── clickhouse:https URL → SECURE=true + 拆字段 ──
	chEnv := envOf(servers["bot-clickhouse-dev"])
	if chEnv["CLICKHOUSE_HOST"] != "ch.local" || chEnv["CLICKHOUSE_PORT"] != "8443" ||
		chEnv["CLICKHOUSE_USER"] != "chu" || chEnv["CLICKHOUSE_PASSWORD"] != "chpw" ||
		chEnv["CLICKHOUSE_DATABASE"] != "analytics" || chEnv["CLICKHOUSE_SECURE"] != "true" {
		t.Errorf("clickhouse env mismatch: %v", chEnv)
	}
}

// TestBuildMCPServers_DataStores_PruneEmpty 验证 PruneEmpty=true 下没填连接串的
// 数据层不被注册(避免 IDE 启动一堆永远连不通的 mcp,污染 settings)。
func TestBuildMCPServers_DataStores_PruneEmpty(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{
				{Type: "mongodb", Enabled: true},
				{Type: "redis", Enabled: true},
			},
		},
	}
	// 只填 dev 的 mongodb,prod 的 mongodb + dev/prod 的 redis 都没填
	creds := map[string]string{"MONGODB_URI_DEV": "mongodb://m:27017/a"}
	get := func(k string) string { return creds[k] }

	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, get)
	if _, ok := servers["mongodb-dev"]; !ok {
		t.Errorf("expected mongodb-dev to be registered")
	}
	for _, k := range []string{"mongodb-prod", "redis-dev", "redis-prod"} {
		if _, ok := servers[k]; ok {
			t.Errorf("expected %q to be pruned (no creds), got registered", k)
		}
	}
}

// TestBuildMCPServers_OTelDisabledUniversal 验证所有有 env 段的 mcp server 都带
// OTEL_SDK_DISABLED=true(防 npm/pip 包间接依赖 elastic-otel-node / @sentry/node /
// Python OTel 自动注入往 stdout 打 banner 污染 JSON-RPC 协议)。这个回归是 envBlock
// 默认注入实现的 —— 加 case 时漏写 envBlock 包裹会立刻露馅。
func TestBuildMCPServers_OTelDisabledUniversal(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{Type: "nacos", ID: "primary"}},
			Observability: config.Observability{
				Grafana: config.Grafana{Enabled: true},
				Loki:    config.Loki{Enabled: true},
				Jaeger:  config.Jaeger{Enabled: true},
				ELK:     config.ELK{Enabled: true},
			},
			DataStores: []config.DataStore{
				{Type: "mongodb", Enabled: true},
				{Type: "postgresql", Enabled: true},
				{Type: "redis", Enabled: true},
				{Type: "elasticsearch", Enabled: true},
				{Type: "mysql", Enabled: true},
				{Type: "clickhouse", Enabled: true},
			},
			Messaging:       []config.Messaging{{Platform: "lark", Enabled: true}},
			ProjectTracking: []config.ProjectTracking{{Platform: "feishu_project", Enabled: true}},
		},
	}
	// 给所有家最少一个连接串,免得 PruneEmpty 把空字段连同 OTEL 一起剔(envBlock 先注入
	// 再 prune,但整段空 map 在 PruneEmpty 下还是会留 OTEL 一项 — 那种边界对生产没意义,
	// 测真实有连接串场景就够)。
	creds := map[string]string{
		"NACOS_ADDR_PRIMARY_DEV": "nacos:8848", "NACOS_USER_PRIMARY_DEV": "u", "NACOS_PASS_PRIMARY_DEV": "p",
		"GRAFANA_URL_DEV": "http://g:3000", "GRAFANA_USER_DEV": "u", "GRAFANA_PASS_DEV": "p",
		"JAEGER_URL_DEV":     "http://j:16686",
		"ELK_ES_URL_DEV":     "http://es:9200",
		"MONGODB_URI_DEV":    "mongodb://m/d",
		"POSTGRES_DSN_DEV":   "postgres://u:p@p/d",
		"REDIS_URL_DEV":      "redis://r/0",
		"ES_URL_DEV":         "http://e:9200",
		"MYSQL_DSN_DEV":      "u:p@tcp(m:3306)/d",
		"CLICKHOUSE_URL_DEV": "https://c:8443/d",
		"LARK_APP_ID":        "app", "LARK_APP_SECRET": "sec",
		"MCP_USER_TOKEN": "tok",
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true},
		func(k string) string { return creds[k] })
	if len(servers) == 0 {
		t.Fatalf("expected servers,got empty")
	}
	for k, v := range servers {
		spec := v.(map[string]any)
		env, _ := spec["env"].(map[string]any)
		if env == nil {
			// 没 env 段的 spec 不需要 OTEL 防御 — 当前所有 case 都有 env,这条是未来 case
			// 漏写 envBlock 时的兜底跳过(避免误报)。
			continue
		}
		if env["OTEL_SDK_DISABLED"] != "true" {
			t.Errorf("%s 缺 OTEL_SDK_DISABLED=true 防御(env=%v)", k, env)
		}
	}
}

// TestBuildMCPServers_GrafanaAuth 验证两套鉴权:
//   - 填了 GRAFANA_API_KEY_<env> → 只发 GRAFANA_API_KEY,USER/PASS 不进 env(避免双套凭据并排误导)
//   - 没填 API key → 回落 USER/PASS basic auth
//
// 这里曾是个静默 bug:wizard 给 auth_mode=api_key 选项收 GRAFANA_API_KEY_<env>,
// 但 BuildMCPServers 只读 USER/PASS,导致 API key 模式部署后 mcp 401。
func TestBuildMCPServers_GrafanaAuth(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}},
		Infrastructure: config.Infrastructure{
			Observability: config.Observability{
				Grafana: config.Grafana{Enabled: true},
			},
		},
	}
	creds := map[string]string{
		"GRAFANA_URL_DEV":     "http://g-dev:3000",
		"GRAFANA_API_KEY_DEV": "glsa_xxx",
		// dev 给 token + user/pass(看 token 是否吃掉 user/pass)
		"GRAFANA_USER_DEV": "ignored",
		"GRAFANA_PASS_DEV": "ignored",
		// prod 只给 user/pass(回落 basic auth)
		"GRAFANA_URL_PROD":  "http://g-prod:3000",
		"GRAFANA_USER_PROD": "admin",
		"GRAFANA_PASS_PROD": "p",
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true},
		func(k string) string { return creds[k] })

	devEnv := envOf(servers["grafana-dev"])
	if devEnv["GRAFANA_SERVICE_ACCOUNT_TOKEN"] != "glsa_xxx" {
		t.Errorf("dev 应发 SERVICE_ACCOUNT_TOKEN(GRAFANA_API_KEY 上游已 deprecated),got env=%v", devEnv)
	}
	if _, has := devEnv["GRAFANA_USERNAME"]; has {
		t.Errorf("dev 有 token 时不该再发 GRAFANA_USERNAME(避免双套凭据并排), got env=%v", devEnv)
	}
	if _, has := devEnv["GRAFANA_API_KEY"]; has {
		t.Errorf("dev 不该再发 deprecated GRAFANA_API_KEY,got env=%v", devEnv)
	}

	prodEnv := envOf(servers["grafana-prod"])
	if _, has := prodEnv["GRAFANA_SERVICE_ACCOUNT_TOKEN"]; has {
		t.Errorf("prod 没填 token 时不该出现 GRAFANA_SERVICE_ACCOUNT_TOKEN(回落路径不该写 token), got env=%v", prodEnv)
	}
	if prodEnv["GRAFANA_USERNAME"] != "admin" || prodEnv["GRAFANA_PASSWORD"] != "p" {
		t.Errorf("prod 应回落 basic auth,got env=%v", prodEnv)
	}
}

// TestBuildMCPServers_LokiNeverRegisters 验证 loki MCP 已被合并进 grafana MCP,
// 任何 cfg(只启 Loki / Loki+Grafana 双启)都不再单独注册 loki-<env>。
// query_loki_logs / query_loki_patterns / query_loki_stats 等工具由 grafana MCP 提供。
func TestBuildMCPServers_LokiNeverRegisters(t *testing.T) {
	for _, c := range []struct {
		name           string
		grafanaEnabled bool
	}{
		{"only loki enabled", false},
		{"loki + grafana both enabled", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.SystemConfig{
				Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}},
				Infrastructure: config.Infrastructure{
					Observability: config.Observability{
						Loki:    config.Loki{Enabled: true},
						Grafana: config.Grafana{Enabled: c.grafanaEnabled},
					},
				},
			}
			creds := map[string]string{
				"GRAFANA_URL_DEV": "http://g:3000", "GRAFANA_USER_DEV": "u", "GRAFANA_PASS_DEV": "p",
				"GRAFANA_URL_PROD": "http://gp:3000", "GRAFANA_USER_PROD": "u", "GRAFANA_PASS_PROD": "p",
			}
			servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true},
				func(k string) string { return creds[k] })
			for _, k := range []string{"loki-dev", "loki-prod"} {
				if _, ok := servers[k]; ok {
					t.Errorf("%s: 不该再注册 %q(loki 工具已并入 grafana MCP), got keys=%v",
						c.name, k, keysOf(servers))
				}
			}
		})
	}
}

// TestBuildMCPServers_ELK 验证 elk 走 npx @elastic/mcp-server-elasticsearch(2026-05 升级)
// 而不是老的 curl 占位。所有 target 都该注册(以前只 openclaw 走 curl 占位,IDE 完全没)。
// 共享凭据 ELK_USERNAME / ELK_PASSWORD(不带 env 后缀,所有 env 共用)。
func TestBuildMCPServers_ELK(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}},
		Infrastructure: config.Infrastructure{
			Observability: config.Observability{
				ELK: config.ELK{Enabled: true},
			},
		},
	}
	creds := map[string]string{
		"ELK_ES_URL_DEV":  "http://es-dev:9200",
		"ELK_ES_URL_PROD": "https://es-prod:9200",
		"ELK_USERNAME":    "elastic",
		"ELK_PASSWORD":    "espw",
	}
	get := func(k string) string { return creds[k] }
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, get)
	for _, k := range []string{"elk-dev", "elk-prod"} {
		spec, ok := servers[k].(map[string]any)
		if !ok {
			t.Fatalf("expected %q registered, got keys: %v", k, keysOf(servers))
		}
		if spec["command"] != "npx" {
			t.Errorf("%s expected command=npx, got %v", k, spec["command"])
		}
		args := spec["args"].([]any)
		if len(args) != 2 || args[0] != "-y" || args[1] != "@elastic/mcp-server-elasticsearch" {
			t.Errorf("%s expected args=[-y @elastic/mcp-server-elasticsearch], got %v", k, args)
		}
		env := envOf(spec)
		if env["ES_USERNAME"] != "elastic" || env["ES_PASSWORD"] != "espw" {
			t.Errorf("%s elk shared creds 未透传: %v", k, env)
		}
		if env["OTEL_SDK_DISABLED"] != "true" {
			t.Errorf("%s 必须禁 OTel 防 stdout 污染 mcp 协议", k)
		}
	}
	if envOf(servers["elk-dev"])["ES_URL"] != "http://es-dev:9200" {
		t.Errorf("elk-dev ES_URL 未透传")
	}

	// PruneEmpty=true 没填 ES URL → 不注册(避免启动一条永远连不通的 mcp)
	servers2 := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(k string) string { return "" })
	if _, ok := servers2["elk-dev"]; ok {
		t.Errorf("expected elk-dev pruned when ELK_ES_URL_DEV missing under PruneEmpty=true")
	}
}

// TestBuildMCPServers_Jaeger 验证 jaeger 走 uvx opentelemetry-mcp(2026-05 升级)而不是
// 老的 curl 占位。所有 target 都该注册(以前只 openclaw 走 curl 占位,IDE 完全没)。
func TestBuildMCPServers_Jaeger(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}},
		Infrastructure: config.Infrastructure{
			Observability: config.Observability{
				Jaeger: config.Jaeger{Enabled: true},
			},
		},
	}
	creds := map[string]string{
		"JAEGER_URL_DEV":  "http://jaeger-dev:16686",
		"JAEGER_URL_PROD": "http://jaeger-prod:16686",
	}
	get := func(k string) string { return creds[k] }
	// IDE 模式(PruneEmpty=true)— 以前 jaeger 完全不注册,现在该 2 个 env 各一份
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, get)
	for _, k := range []string{"jaeger-dev", "jaeger-prod"} {
		v, ok := servers[k]
		if !ok {
			t.Fatalf("expected %q registered, got keys: %v", k, keysOf(servers))
		}
		spec := v.(map[string]any)
		if spec["command"] != "uvx" {
			t.Errorf("%s expected command=uvx (opentelemetry-mcp), got %v", k, spec["command"])
		}
		args := spec["args"].([]any)
		if len(args) != 1 || args[0] != "opentelemetry-mcp" {
			t.Errorf("%s expected args=[opentelemetry-mcp], got %v", k, args)
		}
		env := envOf(v)
		if env["BACKEND_TYPE"] != "jaeger" {
			t.Errorf("%s missing BACKEND_TYPE=jaeger, got %v", k, env)
		}
	}
	if envOf(servers["jaeger-dev"])["BACKEND_URL"] != "http://jaeger-dev:16686" {
		t.Errorf("jaeger-dev BACKEND_URL not propagated correctly")
	}

	// PruneEmpty=true 没填 URL → 不注册(避免启动一条永远连不通的 mcp)
	servers2 := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(k string) string { return "" })
	if _, ok := servers2["jaeger-dev"]; ok {
		t.Errorf("expected jaeger-dev pruned when JAEGER_URL_DEV missing under PruneEmpty=true")
	}
}

// TestBuildMCPServers_DataStores_EndpointsFallback 验证 yaml endpoints[] fallback:
// 用户通过"代码扫描自动填 endpoints[]"路径(没单独跑 wizard 输 env vars)生成的 yaml,
// install creds 里没有 ES_URL_<env> 等 env vars 时,BuildMCPServers 应能从 endpoints
// 派生出连接串注册 mcp。这条路径决定**老 yaml 能不能直接重新部署用上数据层 mcp**。
func TestBuildMCPServers_DataStores_EndpointsFallback(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{
				{
					Type: "elasticsearch", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", Service: "community", URL: "http://10.0.0.1:9200", User: "elastic", Pass: "elastic123"},
						{Env: "dev", Service: "user", URL: "http://10.0.0.1:9200", User: "elastic", Pass: "elastic123"},
					},
				},
				{
					Type: "mongodb", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", Service: "user", URI: "mongodb://m:p@10.0.0.2:27017/users"},
					},
				},
				{
					Type: "redis", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", URL: "redis://:rpw@10.0.0.3:6379/0"},
					},
				},
				{
					Type: "mysql", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", DSN: "u:p@tcp(10.0.0.4:3306)/orders"},
					},
				},
				{
					Type: "postgresql", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", DSN: "postgres://u:p@10.0.0.5:5432/app"},
					},
				},
				{
					Type: "clickhouse", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", URL: "https://chu:chpw@10.0.0.6:8443/analytics"},
					},
				},
			},
		},
	}
	// install creds 完全空 — 模拟用户跑老 yaml(走 endpoints[] 路径,wizard 没收 env vars)
	emptyGet := func(_ string) string { return "" }

	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, emptyGet)

	// 6 家全部应该从 endpoints 派生连接串成功注册
	for _, k := range []string{"elasticsearch-dev", "mongodb-dev", "redis-dev", "mysql-dev", "postgresql-dev", "clickhouse-dev"} {
		if _, ok := servers[k]; !ok {
			t.Errorf("expected %q registered from endpoints fallback (creds empty), got keys: %v", k, keysOf(servers))
		}
	}

	// 关键字段值确认 fallback 正确取自 endpoints
	if envOf(servers["elasticsearch-dev"])["ES_URL"] != "http://10.0.0.1:9200" ||
		envOf(servers["elasticsearch-dev"])["ES_USERNAME"] != "elastic" {
		t.Errorf("es endpoints fallback wrong: %v", envOf(servers["elasticsearch-dev"]))
	}
	if envOf(servers["mysql-dev"])["MYSQL_HOST"] != "10.0.0.4" {
		t.Errorf("mysql endpoints fallback wrong: %v", envOf(servers["mysql-dev"]))
	}
	if envOf(servers["clickhouse-dev"])["CLICKHOUSE_HOST"] != "10.0.0.6" ||
		envOf(servers["clickhouse-dev"])["CLICKHOUSE_SECURE"] != "true" {
		t.Errorf("clickhouse endpoints fallback wrong: %v", envOf(servers["clickhouse-dev"]))
	}
}

// TestBuildMCPServers_DataStores_SingleURI_NoSourceSuffix:
// 老用户兼容路径 — yaml endpoints[] 同 env 多条但 URI 完全一致(或只 1 条),
// dedup 后 unique URI = 1,MCP key 应保持无 source 段(`mongodb-test`,跟改造前一致),
// 老 IDE config 里的 key 不变成孤儿。
func TestBuildMCPServers_DataStores_SingleURI_NoSourceSuffix(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "test"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{{
				Type: "mongodb", Enabled: true,
				Endpoints: []config.DataStoreEndpoint{
					// 5 个 service 全连同 1 个 URI — dedup 后只剩 1 个
					{Env: "test", Service: "commerce", URI: "mongodb://test-mongo.example.com:27017/?authSource=admin"},
					{Env: "test", Service: "user", URI: "mongodb://test-mongo.example.com:27017/?authSource=admin"},
					{Env: "test", Service: "order", URI: "mongodb://test-mongo.example.com:27017/?authSource=admin"},
				},
			}},
		},
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(_ string) string { return "" })

	// 退化命名:不带 source 段
	if _, ok := servers["mongodb-test"]; !ok {
		t.Errorf("expected single-URI dedup → 'mongodb-test' (no source segment), got: %v", keysOf(servers))
	}
	// 不应有任何带 source 段的 key
	for k := range servers {
		if strings.HasPrefix(k, "mongodb-") && k != "mongodb-test" {
			t.Errorf("single-URI dedup should NOT produce sourced key %q", k)
		}
	}
}

// TestBuildMCPServers_DataStores_MultiURI_HostSourceID:
// 多 cluster 场景 — 同 env 多条 endpoint 不同 URI,
// 派生 sourceID = host 第一段,注册多个 MCP。
func TestBuildMCPServers_DataStores_MultiURI_HostSourceID(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "test"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{{
				Type: "mongodb", Enabled: true,
				Endpoints: []config.DataStoreEndpoint{
					{Env: "test", Service: "commerce", URI: "mongodb://mongo-commerce.test.example.com:27017/?..."},
					{Env: "test", Service: "user", URI: "mongodb://mongo-user.test.example.com:27017/?..."},
				},
			}},
		},
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(_ string) string { return "" })

	want := []string{"mongodb-mongo-commerce-test", "mongodb-mongo-user-test"}
	for _, k := range want {
		if _, ok := servers[k]; !ok {
			t.Errorf("expected multi-URI dedup → %q, got: %v", k, keysOf(servers))
		}
	}
	// 不应再有无 source 段的 mongodb-test(因为 unique > 1)
	if _, ok := servers["mongodb-test"]; ok {
		t.Errorf("multi-URI should NOT register fallback 'mongodb-test' (no source) key")
	}
}

// TestBuildMCPServers_DataStores_MultiURI_CollisionHashFallback:
// 边缘 case — 同 host 不同 port,host 抽取的 sourceID 撞名,
// 加 URI hash 短前缀兜底。
func TestBuildMCPServers_DataStores_MultiURI_CollisionHashFallback(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "test"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{{
				Type: "mongodb", Enabled: true,
				Endpoints: []config.DataStoreEndpoint{
					{Env: "test", URI: "mongodb://10.0.0.1:27017/?..."},
					{Env: "test", URI: "mongodb://10.0.0.1:27018/?..."},
				},
			}},
		},
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(_ string) string { return "" })

	// 第一条 sourceID = 10-0-0-1
	if _, ok := servers["mongodb-10-0-0-1-test"]; !ok {
		t.Errorf("expected first source 'mongodb-10-0-0-1-test', got: %v", keysOf(servers))
	}
	// 第二条同 host 撞名,sourceID = 10-0-0-1-<hash6>
	found := false
	for k := range servers {
		if strings.HasPrefix(k, "mongodb-10-0-0-1-") && strings.HasSuffix(k, "-test") && k != "mongodb-10-0-0-1-test" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected second collision-fallback key 'mongodb-10-0-0-1-<hash6>-test', got: %v", keysOf(servers))
	}
}

// TestBuildMCPServers_DataStores_DerivedSourceID 直接单测 deriveSourceID
// 几个典型 URI 形态(域名 / IP / 凭据 / replica set / 异常)。
func TestBuildMCPServers_DataStores_DerivedSourceID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"mongodb://mongo-commerce.test.example.com:27017/?...", "mongo-commerce"},
		{"mongodb://user:pass@mongo-user.test:27017/?...", "mongo-user"},
		{"mongodb://10.0.0.1:27017/?...", "10-0-0-1"},
		{"mongodb://a.dc1,b.dc1,c.dc1/?replicaSet=rs", "a-dc1"},
		{"postgres://u:p@pg-master.test/db", "pg-master"},
		{"redis://:rpw@10.0.0.3:6379/0", "10-0-0-3"},
		{"https://chu:chpw@10.0.0.6:8443/analytics", "10-0-0-6"},
	}
	for _, c := range cases {
		got := deriveSourceID(c.in)
		if got != c.want {
			t.Errorf("deriveSourceID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 异常 URI(完全无法抽 host)应 fallback 到 h- + hash
	bad := deriveSourceID("@!#$%")
	if !strings.HasPrefix(bad, "h-") {
		t.Errorf("deriveSourceID for invalid URI should fallback to h-<hash>, got %q", bad)
	}
}

// TestBuildMCPServers_DataStores_CredsOverridesEndpoints 验证 install creds 优先于 endpoints:
// 用户在 wizard 显式覆盖了某 env 的连接串(env-vars 模式),应以 wizard 输入为准,
// 不要被老 yaml endpoints 的值覆盖。
func TestBuildMCPServers_DataStores_CredsOverridesEndpoints(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{
				{
					Type: "mongodb", Enabled: true,
					Endpoints: []config.DataStoreEndpoint{
						{Env: "dev", URI: "mongodb://OLD@host/db"},
					},
				},
			},
		},
	}
	creds := map[string]string{"MONGODB_URI_DEV": "mongodb://NEW@host/db"}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true},
		func(k string) string { return creds[k] })
	got := envOf(servers["mongodb-dev"])["MCP_MONGODB_URI"]
	if !strings.Contains(got, "mongodb://NEW@host/db") || strings.Contains(got, "OLD") {
		t.Errorf("expected creds override endpoints, got MCP_MONGODB_URI: %s", got)
	}
}

// TestBuildMCPServers_DataStores_MySQLPortDefault 验证 mysql DSN 没带 port
// 时默认 3306(否则 mcp 启动时 host="db" port="" 会连失败)。
func TestBuildMCPServers_DataStores_MySQLPortDefault(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			DataStores: []config.DataStore{{Type: "mysql", Enabled: true}},
		},
	}
	creds := map[string]string{"MYSQL_DSN_DEV": "u:p@tcp(db)/app"}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true},
		func(k string) string { return creds[k] })
	if envOf(servers["mysql-dev"])["MYSQL_PORT"] != "3306" {
		t.Errorf("expected MYSQL_PORT=3306 default, got %q", envOf(servers["mysql-dev"])["MYSQL_PORT"])
	}
}

// 测试辅助:从 server spec 取 args 串 / env map / keys 列表
func argString(spec any) string {
	m, _ := spec.(map[string]any)
	args, _ := m["args"].([]any)
	out := "["
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a.(string)
	}
	return out + "]"
}

func envOf(spec any) map[string]string {
	m, _ := spec.(map[string]any)
	env, _ := m["env"].(map[string]any)
	out := map[string]string{}
	for k, v := range env {
		out[k], _ = v.(string)
	}
	return out
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestNormalizeMongoURI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "用户实际场景:密码含 < ] ^ . — mcp-mongo-server 严格解析必失败",
			in:   "mongodb://root:Xx9<9]Nu^Z]5zq3UD3j.@43.206.141.191:27017/gin_microservice",
			want: "mongodb://root:Xx9%3C9%5DNu%5EZ%5D5zq3UD3j.@43.206.141.191:27017/gin_microservice?authSource=admin",
		},
		{
			name: "已经编码过的不重复编码(自动补 authSource)",
			in:   "mongodb://u:p%3Cw@host:27017/db",
			want: "mongodb://u:p%3Cw@host:27017/db?authSource=admin",
		},
		{
			name: "密码无特殊字符 → 仅补 authSource",
			in:   "mongodb://user:simple123@host:27017/db",
			want: "mongodb://user:simple123@host:27017/db?authSource=admin",
		},
		{
			name: "无 userinfo → 不动",
			in:   "mongodb://host:27017/db",
			want: "mongodb://host:27017/db",
		},
		{
			name: "只 user 没 pass → 不动",
			in:   "mongodb://user@host:27017/db",
			want: "mongodb://user@host:27017/db",
		},
		{
			name: "mongodb+srv 同样适用",
			in:   "mongodb+srv://u:p#a@cluster.mongodb.net/db",
			want: "mongodb+srv://u:p%23a@cluster.mongodb.net/db?authSource=admin",
		},
		{
			name: "密码含 @ — 用 LastIndex 兜底找正确的 host 起点",
			in:   "mongodb://user:p@ss@host:27017/db",
			want: "mongodb://user:p%40ss@host:27017/db?authSource=admin",
		},
		{
			name: "空串 → 原样",
			in:   "",
			want: "",
		},
		// ── ensureAuthSource:root@admin 跨 db 访问场景自动补 authSource=admin ──
		{
			name: "用户实际场景:root 跨 db,自动补 authSource=admin",
			in:   "mongodb://root:simple@host:27017/business_db",
			want: "mongodb://root:simple@host:27017/business_db?authSource=admin",
		},
		{
			name: "已显式 authSource → 尊重用户不动",
			in:   "mongodb://u:p@host:27017/db?authSource=myauth",
			want: "mongodb://u:p@host:27017/db?authSource=myauth",
		},
		{
			name: "已 admin db → 不补",
			in:   "mongodb://u:p@host:27017/admin",
			want: "mongodb://u:p@host:27017/admin",
		},
		{
			name: "无 path(默认 admin)→ 不补",
			in:   "mongodb://u:p@host:27017",
			want: "mongodb://u:p@host:27017",
		},
		{
			name: "已有 query 但无 authSource → & 追加",
			in:   "mongodb://u:p@host:27017/db?retryWrites=true",
			want: "mongodb://u:p@host:27017/db?retryWrites=true&authSource=admin",
		},
		{
			name: "密码编码 + 补 authSource 一并触发",
			in:   "mongodb://root:p<w@host:27017/biz",
			want: "mongodb://root:p%3Cw@host:27017/biz?authSource=admin",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeMongoURI(c.in)
			if got != c.want {
				t.Errorf("\n  in:   %q\n  got:  %q\n  want: %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseConnURL(t *testing.T) {
	cases := []struct {
		name                         string
		s                            string
		host, port, user, pass, path string
	}{
		{
			name: "redis 带凭证 + db",
			s:    "redis://default:pwd123@10.0.0.1:6379/2",
			host: "10.0.0.1", port: "6379", user: "default", pass: "pwd123", path: "2",
		},
		{
			name: "clickhouse https 不带凭证",
			s:    "https://ch.example.com:8443/analytics",
			host: "ch.example.com", port: "8443", path: "analytics",
		},
		{
			name: "redis 无密码",
			s:    "redis://10.0.0.1:6379/0",
			host: "10.0.0.1", port: "6379", path: "0",
		},
		{
			name: "无端口(让调用方默认)",
			s:    "http://ch.local",
			host: "ch.local",
		},
		{name: "空串"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, p, u, pw, path := parseConnURL(c.s)
			if h != c.host || p != c.port || u != c.user || pw != c.pass || path != c.path {
				t.Errorf("parseConnURL(%q) = host=%q port=%q user=%q pass=%q path=%q;\n  want host=%q port=%q user=%q pass=%q path=%q",
					c.s, h, p, u, pw, path, c.host, c.port, c.user, c.pass, c.path)
			}
		})
	}
}
