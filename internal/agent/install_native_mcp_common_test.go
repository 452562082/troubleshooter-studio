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
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestParseMySQLDSN(t *testing.T) {
	cases := []struct {
		name                            string
		dsn                             string
		host, port, user, pass, db      string
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
		"MONGODB_URI_DEV":  "mongodb://u:p@m.local:27017/app",
		"POSTGRES_DSN_DEV": "postgres://u:p@pg.local:5432/app",
		"ES_URL_DEV":       "https://es.local:9200",
		"ES_USER_DEV":      "elastic",
		"ES_PASS_DEV":      "espw",
		"REDIS_URL_DEV":    "redis://default:rpw@r.local:6379/0",
		"MYSQL_DSN_DEV":    "myu:mypw@tcp(my.local:3307)/orders",
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

	// ── mongodb:位置参数接 URI + --read-only ──
	if got := argString(servers["bot-mongodb-dev"]); got != "[-y mcp-mongo-server mongodb://u:p@m.local:27017/app --read-only]" {
		t.Errorf("mongodb args mismatch: %s", got)
	}

	// ── postgres:位置参数接 connection string(server-postgres 默认 readonly transaction)──
	if got := argString(servers["bot-postgresql-dev"]); got != "[-y @modelcontextprotocol/server-postgres postgres://u:p@pg.local:5432/app]" {
		t.Errorf("postgres args mismatch: %s", got)
	}

	// ── redis:钉死 1.0.0 + URL 位置参数(防 @latest 漂移)──
	if got := argString(servers["bot-redis-dev"]); got != "[-y @gongrzhe/server-redis-mcp@1.0.0 redis://default:rpw@r.local:6379/0]" {
		t.Errorf("redis args mismatch: %s", got)
	}

	// ── elasticsearch:env 段 ES_URL/USERNAME/PASSWORD ──
	esEnv := envOf(servers["bot-elasticsearch-dev"])
	if esEnv["ES_URL"] != "https://es.local:9200" || esEnv["ES_USERNAME"] != "elastic" || esEnv["ES_PASSWORD"] != "espw" {
		t.Errorf("elasticsearch env mismatch: %v", esEnv)
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

func TestParseConnURL(t *testing.T) {
	cases := []struct {
		name                            string
		s                               string
		host, port, user, pass, path    string
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
