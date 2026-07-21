// install_native_mcp_data_stores.go —— 数据层 9 家 MCP builder + 总分发。
// 2026-05-15 从 install_native_mcp_common.go 拆出,纯重构。
//
// 调用入口:BuildMCPServers → buildDataStores 分发到具体 build<DS>。
// 共享 helper(dsEndpointsUnique / deriveSourceID / parseConnURL / parseMySQLDSN /
// normalizeMongoURI 等)留在 install_native_mcp_common.go,跨 builder 复用。
//
// 涉及包(2026-05-15 runtime probe 最新事实):
//
//	mongodb        npx mcp-mongo-server --read-only       8 tools(--read-only 运行时拦截写)
//	postgresql     npx @henkey/postgres-mcp-server        19 tools(2 写 + 1 任意 SQL 软约束禁)
//	elasticsearch  npx @elastic/mcp-server-elasticsearch@0.1.1  4 tools(ES 8 client)
//	redis          npx @gongrzhe/server-redis-mcp@1.0.0   4 tools(无 scan/TTL,需 redis-cli fallback)
//	mysql          npx @benborla29/mcp-server-mysql       1 tool(单 mysql_query 入口,内部 env 限制写)
//	doris          npx @benborla29/mcp-server-mysql       1 tool(走 Doris FE MySQL 协议)
//	clickhouse     uvx mcp-clickhouse                     3 tools(run_query 主用,内置 destructive protection)
//	kafka          binary kafka-mcp-server (tuannvm)      9 tools(8 读 + 1 produce_message 软约束禁)
//	rabbitmq       禁用注册(两个 PyPI 候选都 broken,SKILL 走 HTTP Management API 主路径)
package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// buildDataStores 数据层 MCP per (data_store_type, env)。wizard 用 DS_TOOL_SPECS 收集每家 +
// 每环境的连接串 env vars(如 MONGODB_URI_DEV / POSTGRES_DSN_DEV / DORIS_DSN_DEV / ES_URL_DEV ...),
// useDeployFlow.buildOpenclawCreds 把这些 env vars 写到 install creds map。
// 这里读对应 env var,注册成预启动 mcp server,让 AI 能直接 tool_use 调而不用读 SKILL.md
// 跑 mongosh / psql 这种"AI 不一定会主动跑"的 CLI。
//
// PruneEmpty=true 模式下空 env 段会被剔,如果用户没填 endpoint(env-vars 模式没填 /
// 走 from_config_center 模式),mcp server 启动时拿不到 URI 直接退出 — 不会污染 IDE。
func (b *mcpBuilder) buildDataStores(servers map[string]any) {
	typeCounts := map[string]int{}
	for _, ds := range b.cfg.Infrastructure.DataStores {
		if ds.Enabled {
			typeCounts[ds.Type]++
		}
	}
	for _, ds := range b.cfg.Infrastructure.DataStores {
		if !ds.Enabled {
			continue
		}
		instanceSourceID := ""
		if typeCounts[ds.Type] > 1 || (ds.ID != "" && ds.ID != ds.Type) {
			instanceSourceID = ds.ID
		}
		for _, e := range b.cfg.Environments {
			// 按连接串 dedupe:同一 (env, type) 下,同 URI 视为同 cluster,共享一个 MCP;
			// 不同 URI 注册成多个 MCP(支持"一个 env 里多个 mongodb cluster"场景)。
			// dedupe 后只有 1 个 unique → sourceID 留空,MCP key 退化成无 source 段(跟老用户行为一致)。
			unique := dsEndpointsUnique(ds, e.ID)
			single := len(unique) <= 1
			for _, ep := range unique {
				sourceID := instanceSourceID
				if !single {
					if sourceID == "" {
						sourceID = ep.sourceID
					} else {
						sourceID += "-" + ep.sourceID
					}
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
				case "doris":
					b.buildDoris(servers, ep.endpoint, sourceID, e.ID)
				case "clickhouse":
					b.buildClickHouse(servers, ep.endpoint, sourceID, e.ID)
				case "kafka":
					b.buildKafka(servers, ep.endpoint, sourceID, e.ID)
				case "rabbitmq":
					b.buildRabbitMQ(servers, ep.endpoint, sourceID, e.ID)
				}
			}
			// env-vars 模式 / 用户没扫到 endpoints → unique 空,但还得跑一遍(让 buildXxx
			// 从 install creds env 拿连接串;PruneEmpty 模式下空 env 段会被剔)
			if len(unique) == 0 {
				switch ds.Type {
				case "mongodb":
					b.buildMongoDB(servers, nil, instanceSourceID, e.ID)
				case "postgresql":
					b.buildPostgreSQL(servers, nil, instanceSourceID, e.ID)
				case "elasticsearch":
					b.buildDataES(servers, nil, instanceSourceID, e.ID)
				case "redis":
					b.buildRedis(servers, nil, instanceSourceID, e.ID)
				case "kafka":
					b.buildKafka(servers, nil, instanceSourceID, e.ID)
				case "rabbitmq":
					b.buildRabbitMQ(servers, nil, instanceSourceID, e.ID)
				case "mysql":
					b.buildMySQL(servers, nil, instanceSourceID, e.ID)
				case "doris":
					b.buildDoris(servers, nil, instanceSourceID, e.ID)
				case "clickhouse":
					b.buildClickHouse(servers, nil, instanceSourceID, e.ID)
				}
			}
		}
	}
}

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
	uri = ensureDirectConnection(uri)
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

// buildPostgreSQL 用 @henkey/postgres-mcp-server(v1.0.5+,env-based,凭据不落 args)。
//
// 2026-05-15 从 @modelcontextprotocol/server-postgres 迁移过来:
//   - 老包于 2025-07 deprecated,官方明确 archive 不再修;近期能跑但长期撞墙
//   - 老包优势(RO transaction 包裹所有查询)在新包没有 — 但跟 mysql/redis/kafka 同理,
//     只读改靠 SKILL 软约束(用户校准过的设计哲学)
//   - @henkey 工具:pg_execute_query(SELECT 主用)/ pg_monitor_database / pg_debug_database /
//     pg_analyze_database 等 17 个;**禁用工具**:pg_execute_mutation / pg_execute_sql(可写)
//   - env 而非位置参数 → 凭据不再落 args,~/.claude.json 不再泄漏 DSN(P3.2 旧 trade-off 顺便解决)
//
// 安全责任建议同时下沉到 PG 端:DSN 里给只读 role,即便 LLM 误调 pg_execute_mutation
// PG 端也会拒。这是软约束的兜底,不强制要求。
func (b *mcpBuilder) buildPostgreSQL(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epDSN string
	if ep != nil {
		epDSN = ep.DSN
	}
	dsn := firstNonEmpty(b.get(envVar("POSTGRES_DSN", sourceID, envID)), epDSN)
	if dsn == "" && b.opts.PruneEmpty {
		return
	}
	servers[b.keyFor("postgresql", sourceID, envID)] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@henkey/postgres-mcp-server"},
		"env": b.envBlock(map[string]any{
			"POSTGRES_CONNECTION_STRING": dsn,
		}),
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
		"args":    []any{"-y", elasticsearchMCPPackage},
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

// buildDoris:Doris FE 对外兼容 MySQL 协议,因此复用 MySQL MCP 包和 DSN 解析。
// 用户填 go-sql-driver DSN,典型端口为 FE query port 9030:
//
//	user:pass@tcp(doris-fe:9030)/warehouse
func (b *mcpBuilder) buildDoris(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epDSN string
	if ep != nil {
		epDSN = ep.DSN
	}
	dsn := firstNonEmpty(b.get(envVar("DORIS_DSN", sourceID, envID)), epDSN)
	if dsn == "" && b.opts.PruneEmpty {
		return
	}
	var host, port, user, pass, db string
	if strings.Contains(dsn, "://") {
		host, port, user, pass, db = parseConnURL(strings.TrimPrefix(dsn, "jdbc:"))
	} else {
		host, port, user, pass, db = parseMySQLDSN(dsn)
	}
	if port == "" {
		port = "9030"
	}
	servers[b.keyFor("doris", sourceID, envID)] = map[string]any{
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

// buildKafka 用 tuannvm/kafka-mcp-server(MIT,franz-go 纯 Go,GoReleaser 5 个 triple 全)。
//
// **跟其它 7 家不一致**:走 binary 安装(brew tap / GitHub Release),不是 npx/uvx 零安装。
// 原因:kafka 这家业界没有靠谱的 npx/uvx 实现 —— 详见 ensure_kafka_mcp.go 头部注释。
// 简言之,Confluent 官方 npm 包(`@confluentinc/mcp-confluent`)依赖 native librdkafka 绑定,
// Node ABI 矩阵滞后 + install scripts 静默失败,跨平台脆弱(2026-05 实战踩坑后回切 binary)。
// franz-go 纯 Go 实现避开了 librdkafka 整条 native binding 路径。
//
// 安全契约:tuannvm 没原生 --read-only flag,但 9 个工具里只 1 个 mutative(`produce_message`),
// 默认靠 LLM prompt 不主动调(produce_message 是显式动作,LLM 排障时不会"恰好"调它发消息)。
// 进一步加固在 bot 的 SKILL.md / system instruction 里写明"kafka MCP 工具只读使用"。
//
// 配置形态(参考上游 README):
//
//	command: "kafka-mcp-server"  # 从 PATH 找 binary
//	args:    []
//	env:     KAFKA_BROKERS=<csv brokers>   # tuannvm 用的 env 名,不是 BOOTSTRAP_SERVERS
//	         MCP_TRANSPORT=stdio
//
// 自托管 Apache Kafka 用户只填 brokers,不需要 SASL/TLS 凭据(没填 KAFKA_SASL_* 即关闭)。
// dedup 跟其它 7 家同款:brokers 字符串作 key,同 brokers 视为同 cluster 共享 1 个 MCP。
//
// install 时安装路径:见 EnsureKafkaMCPInstalled(PATH 命中用绝对路径 / cache 命中复用 /
// 缺失自动从 GitHub Release 拉 tarball 到 ~/.tshoot/bin/,失败 warn 不阻塞)。
func (b *mcpBuilder) buildKafka(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	var epBrokers string
	if ep != nil {
		epBrokers = ep.Brokers
	}
	brokers := firstNonEmpty(b.get(envVar("KAFKA_BROKERS", sourceID, envID)), epBrokers)
	if brokers == "" && b.opts.PruneEmpty {
		return // 没填 brokers → 跳过(避免注册一条永远启动失败的 mcp)
	}
	cmd := b.opts.KafkaMCPBinaryPath
	if cmd == "" {
		cmd = "kafka-mcp-server" // PATH 回落,跟手动装路径兼容
	}
	servers[b.keyFor("kafka", sourceID, envID)] = map[string]any{
		"command": cmd,
		"args":    []any{},
		"env": b.envBlock(map[string]any{
			"KAFKA_BROKERS": brokers,
			"MCP_TRANSPORT": "stdio",
		}),
	}
}

// buildRabbitMQ 2026-05-15 真实 probe 后**禁用 mcp 注册**(yaml schema / wizard / .env 凭据
// 都不动,等社区出能用的包再翻开)。理由:
//
// 跑 stdio probe 实测两个 PyPI 候选都 broken,**不是版本不兼容,是源码 import 路径根本不存在**:
//
//  1. `amq-mcp-server-rabbitmq@latest`(AWS amazon-mq 维护,曾经的首选):
//     源码 line 9 写死 `from fastmcp.server.auth import BearerAuthProvider`,但 fastmcp 任何
//     版本(2.7 / 2.14.7 / 3.3 都验过)的 `fastmcp.server.auth` 都没有 `BearerAuthProvider`
//     这个 export — 大概率是 fastmcp 早期改名为 `JWTVerifier` 等,amazon-mq 没跟。
//     `uvx --with "fastmcp==2.14.7"` 硬钉 2.x 最新也撞同款 ImportError → 死局。
//     GitHub main 分支同款代码、0 issue 反馈,上游没人修。
//  2. `rabbitmq-mcp-server`(guercheLE 社区):
//     依赖声明缺一堆 — 撞 tabulate、tomli、requests 全 ModuleNotFoundError,补丁堆补丁。
//
// 修法走方案 B(**同 nacos / apollo / consul,不同 feishu_project**):
// rabbitmq 主路径走 SKILL 内 HTTP Management API(端口 15672 自带 REST API,极其稳定,
// RabbitMQ 团队官方维护)。**能力完整可用**,只是 mcp 这一层禁了。
//
// 跟 feishu_project 区别:feishu_project 是 3b 真禁用 — mcp 禁 + 凭据停收 + 无替代;
// rabbitmq 是 3a 方案 B — mcp 禁 + **凭据仍收** + HTTP API 完整替代。详见 AGENTS.md
// "不注册 mcp 的两种情况"。
//
// rabbitmq 在排障里调用频次低(看队列长度、consumer lag、alarm),不像 grafana 那种高频,
// 失去 mcp 原生 tool-call 体验代价可接受。SKILL 早就把 HTTP API 当 fallback 写了,这次直接升主路径。
//
// 等条件:社区出**能跑通**的 rabbitmq mcp 包,且工具集对得上排障需求,再翻开下面 if 分支。
// 当前 install 时打 warn 告知用户 mcp 没注册,SKILL 会走 HTTP API。
func (b *mcpBuilder) buildRabbitMQ(servers map[string]any, ep *config.DataStoreEndpoint, sourceID, envID string) {
	for _, ds := range b.cfg.Infrastructure.DataStores {
		if ds.Type != "rabbitmq" || !ds.Enabled {
			continue
		}
		fmt.Fprintf(os.Stderr, "[warn] rabbitmq mcp 暂未启用注册(%s)\n", envID)
		fmt.Fprintf(os.Stderr, "        理由:amq-mcp-server-rabbitmq 源码引用 fastmcp 不存在的 BearerAuthProvider(任何版本都没有),\n")
		fmt.Fprintf(os.Stderr, "             rabbitmq-mcp-server 缺一堆 dep — 两个 PyPI 候选都跑不起来\n")
		fmt.Fprintf(os.Stderr, "        现状:yaml 仍合法,凭据仍收集;主路径走 SKILL HTTP Management API(端口 15672)\n")
		fmt.Fprintf(os.Stderr, "        等条件:社区出能跑通的 mcp 包 — 详见 install_native_mcp_data_stores.go::buildRabbitMQ 注释\n")
		_ = servers // 占位:重启用时改回 servers[b.keyFor("rabbitmq", sourceID, envID)] = ...
		_ = ep
		_ = sourceID
		return
	}
}
