// dependency_scan.go —— 跨语言扫"本仓库调了哪些下游服务 / 用了哪些数据层"。
//
// 设计:简单 regex 扫,**不是**完整 AST 分析。准确率目标 50-70% 而非 100%——
// 即使扫漏一半,生成的 service-dependency-map.yaml 比 100% 占位空白强 10 倍。
// 用户拿到种子值改比从空白起强 10 倍。
//
// 各语言模式:
//   - Go    : http.Get/Post/Do(URL) / grpc.Dial("host:port") / mongo.Connect / redis.NewClient
//   - Java  : @FeignClient(name="...") / RestTemplate.exchange / WebClient.create / @Autowired Redis/Mongo
//   - Python: requests.get/post(URL) / pymongo.MongoClient / redis.Redis / aiohttp.ClientSession
//   - Node  : axios.get/post(URL) / fetch(URL) / mongoose.connect / new Redis(...)
//
// 输出 RepoAnalysis 的 DownstreamCalls + DataStoreUsages 字段。
package analyzer

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ScanDependencies 给定 repoPath,扫所有 stack 适用文件,产出 downstream calls + data store usages。
// includePaths 跟 walker 一样的过滤语义。
func ScanDependencies(stack, repoPath string, includePaths []string) (calls []DownstreamCall, usages []DataStoreUsage) {
	switch stack {
	case "go":
		return scanGoDeps(repoPath, includePaths)
	case "java":
		return scanJavaDeps(repoPath, includePaths)
	case "python":
		return scanPythonDeps(repoPath, includePaths)
	case "node":
		return scanNodeDeps(repoPath, includePaths)
	default:
		return nil, nil
	}
}

// targetFromURL 从 URL 提目标服务名:host 部分按 - / . 切片去 env 后缀,留主体。
// 例:
//   "http://user-service-dev.svc:8080/api/v1/users" → "user-service"
//   "https://payment.example.com/notify"           → "payment"
//   "user-prod:50051" → "user"
func targetFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// 不带 scheme 时 url.Parse 会把整串当 Path
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || host == "localhost" || host == "127.0.0.1" {
		return ""
	}
	// 取第一段(K8s service 名通常是 host 的第一段),去掉 -dev/-prod/-staging/-test 后缀
	first := strings.SplitN(host, ".", 2)[0]
	for _, suf := range []string{"-dev", "-prod", "-staging", "-stg", "-test", "-uat", "-pre"} {
		first = strings.TrimSuffix(first, suf)
	}
	return first
}

// dedupeCalls 同 (target, driver) 去重,保留第一次出现的 callsite。
func dedupeCalls(in []DownstreamCall) []DownstreamCall {
	seen := map[string]bool{}
	out := make([]DownstreamCall, 0, len(in))
	for _, c := range in {
		if c.Target == "" {
			continue
		}
		key := c.Target + "|" + c.Driver
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func dedupeUsages(in []DataStoreUsage) []DataStoreUsage {
	seen := map[string]bool{}
	out := make([]DataStoreUsage, 0, len(in))
	for _, u := range in {
		if u.Type == "" {
			continue
		}
		key := u.Type + "|" + u.Logical + "|" + u.Driver
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, u)
	}
	return out
}

// camelToKebab 把 CamelCase / PascalCase 转 kebab-case。给 NewUserClient → "user"、
// NewOrderRpcClient → "order-rpc"、NewUgcClient → "ugc" 这种 fallback 命名用。
// 全大写片段(连续大写)整体保留为小写不拆分,避免 "URL" → "u-r-l" 这种烂结果。
func camelToKebab(s string) string {
	if s == "" {
		return ""
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			// 前面有字母,且 (前一个是小写 或 当前是大写片段最后一个+下一个是小写) → 加 '-'
			if i > 0 {
				prev := s[i-1]
				prevLower := prev >= 'a' && prev <= 'z'
				next := byte(0)
				if i+1 < len(s) {
					next = s[i+1]
				}
				nextLower := next >= 'a' && next <= 'z'
				prevUpper := prev >= 'A' && prev <= 'Z'
				if prevLower || (prevUpper && nextLower) {
					b = append(b, '-')
				}
			}
			b = append(b, c+('a'-'A'))
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// ── Go ─────────────────────────────────────────────────────────────────
var (
	// http.Get / http.Post / client.Get / client.Do(req with URL) — 取 URL 字面量
	reGoHTTPCall = regexp.MustCompile(`(?i)http\.(Get|Post|Head|Do)\(\s*"([^"]+)"`)
	// grpc.Dial("host:port", ...)
	reGoGRPCDial = regexp.MustCompile(`grpc\.Dial\(\s*"([^"]+)"`)
	// 服务发现风格(naming-driven)的 grpc client factory,跨行也算:
	//   userClient, err := client.NewUserClient(
	//       namingClient, UserServiceName, namespaceID, ...
	//   )
	// 锚点是 "<X>ServiceName" 第二参常量名(truss / go-zero / kratos 等微服务的统一约定)。
	// 第 1 组 = 服务大驼峰名(从 NewXxxClient 抽);第 2 组 = ServiceName 常量名(跟
	// services.go 的 const 交叉解析为真实服务名,如 "user-service")。
	// [^)]*? 跨行 lazy 匹配第 1 个参数(namingClient / sd / nc 任意名字都能过)。
	reGoNamingNewClient = regexp.MustCompile(`\.New([A-Z][a-zA-Z0-9]+?)Client\s*\([^)]*?,\s*([A-Z][a-zA-Z0-9_]*ServiceName)\s*,`)
	// services.go / config.go 里 ServiceName 常量定义,**只匹活定义**(忽略 // 注释行):
	//   UserServiceName        = "user-service"
	//   OrderRpcServiceName    = "order-rpc"
	// (?m)^\s*  + 不允许 / 出现 → 排除 "//CommentedConst = ..." 这类被注释的旧值
	reGoServiceConst = regexp.MustCompile(`(?m)^[ \t]*([A-Z][a-zA-Z0-9_]+ServiceName)\s*=\s*"([a-z0-9][a-z0-9._/-]+)"`)
	// mongo.Connect / mongo.NewClient
	reGoMongo = regexp.MustCompile(`mongo\.(Connect|NewClient)`)
	// redis.NewClient(&redis.Options{Addr: "host:port"})
	reGoRedis = regexp.MustCompile(`redis\.(NewClient|NewClusterClient|NewFailoverClient)`)
	// gorm.Open(mysql/postgres/sqlite, dsn)  /  sql.Open("mysql"/"postgres", dsn)
	reGoSQL = regexp.MustCompile(`(?:gorm\.Open|sql\.Open)\(\s*(?:mysql\.|postgres\.|"mysql"|"postgres")`)
	// kafka.NewWriter / sarama.New
	reGoKafka = regexp.MustCompile(`(?:kafka\.New|sarama\.New)`)
	// elasticsearch / olivere/elastic
	reGoES = regexp.MustCompile(`(?:elasticsearch\.NewClient|elastic\.NewClient|opensearch)`)
	// rocketmq-client-go
	reGoRocketMQ = regexp.MustCompile(`(?:rocketmq\.New|primitive\.NewMessage)|"github\.com/apache/rocketmq-client-go`)
	// rabbitmq: amqp.Dial / streadway-amqp
	reGoRabbitMQ = regexp.MustCompile(`(?:amqp\.Dial|streadway/amqp)`)
	// clickhouse-go / clickhouse driver
	reGoClickHouse = regexp.MustCompile(`(?:clickhouse\.Open|ClickHouse\{|"github\.com/ClickHouse/clickhouse-go)`)
)

func scanGoDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
	})
	// 第一遍:扫所有 services.go 风格的常量定义,建 ServiceName const → 实际值 的映射。
	// truss 等服务发现风格里 client.New<X>Client(_, <X>ServiceName, _) 第二参是常量,
	// 真实服务名("user-service" 这种)在 services.go 里。先把这个常量表建好,
	// 第二遍扫到 client factory 调用时用它解析。
	serviceConstMap := map[string]string{}
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		for _, m := range reGoServiceConst.FindAllStringSubmatch(string(data), -1) {
			serviceConstMap[m[1]] = m[2]
		}
	}

	var calls []DownstreamCall
	var usages []DataStoreUsage
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)

		for _, m := range reGoHTTPCall.FindAllStringSubmatch(text, -1) {
			t := targetFromURL(m[2])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "http", Callsite: rel, Hint: m[2]})
			}
		}
		for _, m := range reGoGRPCDial.FindAllStringSubmatch(text, -1) {
			t := targetFromURL(m[1])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "grpc", Callsite: rel, Hint: m[1]})
			}
		}
		// naming-driven NewXxxClient(naming, XxxServiceName, ns):优先用常量映射拿真实服务名;
		// 没命中(常量在另一文件 / 用了字面量字符串 / 缩写名不一致)就回退到驼峰名小写做 target,
		// agent 拿到至少能看出"调了 user 服务"。Hint 字段保留常量名方便用户校对。
		for _, m := range reGoNamingNewClient.FindAllStringSubmatch(text, -1) {
			constName := m[2]
			target := serviceConstMap[constName]
			if target == "" {
				// 常量解析失败 → fallback:驼峰名转 kebab-case 当 target("UserOrder" → "user-order")
				target = camelToKebab(m[1])
			}
			calls = append(calls, DownstreamCall{
				Target:   target,
				Driver:   "grpc-naming",
				Callsite: rel,
				Hint:     constName,
			})
		}
		if reGoMongo.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "mongodb", Driver: "mongo-driver", Callsite: rel})
		}
		if reGoRedis.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "redis", Driver: "go-redis", Callsite: rel})
		}
		if reGoSQL.MatchString(text) {
			driver := "sql"
			if strings.Contains(text, "mysql.") || strings.Contains(text, `"mysql"`) {
				driver = "mysql"
			}
			if strings.Contains(text, "postgres.") || strings.Contains(text, `"postgres"`) {
				driver = "postgresql"
			}
			usages = append(usages, DataStoreUsage{Type: driver, Driver: "gorm/sql", Callsite: rel})
		}
		if reGoKafka.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "kafka", Driver: "sarama/kafka-go", Callsite: rel})
		}
		if reGoES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "go-es", Callsite: rel})
		}
		if reGoRocketMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rocketmq", Driver: "rocketmq-client-go", Callsite: rel})
		}
		if reGoRabbitMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rabbitmq", Driver: "amqp091/streadway", Callsite: rel})
		}
		if reGoClickHouse.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "clickhouse", Driver: "clickhouse-go", Callsite: rel})
		}
	}
	return dedupeCalls(calls), dedupeUsages(usages)
}

// ── Java ───────────────────────────────────────────────────────────────
var (
	// @FeignClient(name = "user-service")  /  @FeignClient(value = "user-service")
	reJavaFeign = regexp.MustCompile(`@FeignClient\s*\(\s*[^)]*?(?:name|value)\s*=\s*"([^"]+)"`)
	// RestTemplate / WebClient: 找 .get(URL) / .exchange(URL) / .post(URL) 字面量 URL
	reJavaRestCall = regexp.MustCompile(`(?:RestTemplate|WebClient|HttpClient)[^;]{0,200}?\.(?:get|post|exchange|put|delete)(?:ForObject|ForEntity)?\(\s*"([^"]+)"`)
	// Spring Data:@Autowired RedisTemplate / MongoTemplate / KafkaTemplate
	reJavaSpringRedis = regexp.MustCompile(`@Autowired\s+(?:private\s+)?(?:RedisTemplate|StringRedisTemplate|RedissonClient)`)
	reJavaSpringMongo = regexp.MustCompile(`@Autowired\s+(?:private\s+)?MongoTemplate`)
	reJavaSpringKafka = regexp.MustCompile(`@Autowired\s+(?:private\s+)?KafkaTemplate`)
	reJavaJpa         = regexp.MustCompile(`(?:JpaRepository|CrudRepository|MybatisPlus|@Mapper)`)
	reJavaES          = regexp.MustCompile(`(?:ElasticsearchClient|RestHighLevelClient|ElasticsearchOperations)`)
	// RocketMQ:DefaultMQProducer / RocketMQTemplate(spring-rocketmq)
	reJavaRocketMQ    = regexp.MustCompile(`(?:DefaultMQ(?:Producer|PushConsumer|PullConsumer)|RocketMQTemplate|@RocketMQMessageListener)`)
	// RabbitMQ:RabbitTemplate(spring) / Connection/Channel(amqp-client)
	reJavaRabbitMQ    = regexp.MustCompile(`(?:RabbitTemplate|com\.rabbitmq\.client\.Connection|@RabbitListener)`)
	// ClickHouse:JDBC + clickhouse-jdbc
	reJavaClickHouse  = regexp.MustCompile(`(?:clickhouse-jdbc|ClickHouseDataSource|com\.clickhouse)`)
)

func scanJavaDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".java") || strings.HasSuffix(p, ".kt")
	})
	var calls []DownstreamCall
	var usages []DataStoreUsage
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)

		for _, m := range reJavaFeign.FindAllStringSubmatch(text, -1) {
			t := strings.TrimSpace(m[1])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "feign", Callsite: rel, Hint: m[1]})
			}
		}
		for _, m := range reJavaRestCall.FindAllStringSubmatch(text, -1) {
			t := targetFromURL(m[1])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "http", Callsite: rel, Hint: m[1]})
			}
		}
		if reJavaSpringRedis.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "redis", Driver: "spring-data-redis", Callsite: rel})
		}
		if reJavaSpringMongo.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "mongodb", Driver: "spring-data-mongodb", Callsite: rel})
		}
		if reJavaSpringKafka.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "kafka", Driver: "spring-kafka", Callsite: rel})
		}
		if reJavaJpa.MatchString(text) {
			// 缺乏区分 mysql / postgres 的依据,标 sql 让用户自己定
			usages = append(usages, DataStoreUsage{Type: "mysql", Driver: "jpa/mybatis", Callsite: rel})
		}
		if reJavaES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "spring-data-es", Callsite: rel})
		}
		if reJavaRocketMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rocketmq", Driver: "spring-rocketmq", Callsite: rel})
		}
		if reJavaRabbitMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rabbitmq", Driver: "spring-amqp", Callsite: rel})
		}
		if reJavaClickHouse.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "clickhouse", Driver: "clickhouse-jdbc", Callsite: rel})
		}
	}
	return dedupeCalls(calls), dedupeUsages(usages)
}

// ── Python ─────────────────────────────────────────────────────────────
var (
	// requests.get("http://x") / aiohttp.ClientSession().get("http://x") / httpx.get("http://x")
	rePyHTTPCall = regexp.MustCompile(`(?:requests|httpx|aiohttp\.\w+)\.(?:get|post|put|delete|patch|head)\(\s*["']([^"']+)["']`)
	rePyMongo    = regexp.MustCompile(`(?:pymongo|motor)\.\w+`)
	rePyRedis    = regexp.MustCompile(`redis\.(?:Redis|StrictRedis|ConnectionPool|Sentinel|RedisCluster)`)
	rePySQL      = regexp.MustCompile(`(?:sqlalchemy|peewee|tortoise|databases\.Database|psycopg|pymysql)`)
	rePyKafka    = regexp.MustCompile(`(?:kafka-python|confluent_kafka|aiokafka)`)
	rePyES       = regexp.MustCompile(`(?:elasticsearch|opensearchpy)\.`)
	rePyRocketMQ = regexp.MustCompile(`(?:rocketmq[-_]client[-_]python|rocketmq\.client\.)`)
	rePyRabbitMQ = regexp.MustCompile(`(?:pika\.|aio_pika|kombu\.)`)
	rePyClickHouse = regexp.MustCompile(`(?:clickhouse[-_]driver|clickhouse[-_]connect)`)
)

func scanPythonDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".py")
	})
	var calls []DownstreamCall
	var usages []DataStoreUsage
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)

		for _, m := range rePyHTTPCall.FindAllStringSubmatch(text, -1) {
			t := targetFromURL(m[1])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "http", Callsite: rel, Hint: m[1]})
			}
		}
		if rePyMongo.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "mongodb", Driver: "pymongo/motor", Callsite: rel})
		}
		if rePyRedis.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "redis", Driver: "redis-py", Callsite: rel})
		}
		if rePySQL.MatchString(text) {
			driver := "sql"
			t := "mysql"
			switch {
			case strings.Contains(text, "psycopg"):
				t = "postgresql"
				driver = "psycopg"
			case strings.Contains(text, "pymysql"):
				driver = "pymysql"
			default:
				driver = "sqlalchemy"
			}
			usages = append(usages, DataStoreUsage{Type: t, Driver: driver, Callsite: rel})
		}
		if rePyKafka.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "kafka", Driver: "kafka-python", Callsite: rel})
		}
		if rePyES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "elasticsearch-py", Callsite: rel})
		}
		if rePyRocketMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rocketmq", Driver: "rocketmq-client-python", Callsite: rel})
		}
		if rePyRabbitMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rabbitmq", Driver: "pika/aio_pika/kombu", Callsite: rel})
		}
		if rePyClickHouse.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "clickhouse", Driver: "clickhouse-driver", Callsite: rel})
		}
	}
	return dedupeCalls(calls), dedupeUsages(usages)
}

// ── Node ───────────────────────────────────────────────────────────────
var (
	// axios.get("http://x") / fetch("http://x") / got.get("http://x") / superagent.get("http://x")
	reJsHTTPCall = regexp.MustCompile(`(?:axios|fetch|got|superagent)\.?(?:get|post|put|delete|patch)?\(\s*["']([^"']+)["']`)
	reJsMongo    = regexp.MustCompile(`(?:mongoose|MongoClient)\.?(?:connect|connection)`)
	reJsRedis    = regexp.MustCompile(`(?:new\s+(?:Redis|IORedis)|require\s*\(\s*["']ioredis|redis\.createClient)`)
	reJsSQL      = regexp.MustCompile(`(?:typeorm|prisma|sequelize|mysql2|pg\.Pool)`)
	reJsKafka    = regexp.MustCompile(`(?:kafkajs|node-rdkafka)`)
	reJsES       = regexp.MustCompile(`(?:@elastic/elasticsearch|@opensearch-project)`)
	reJsRocketMQ = regexp.MustCompile(`(?:rocketmq-client-nodejs|@apache/rocketmq)`)
	reJsRabbitMQ = regexp.MustCompile(`(?:amqplib|amqp-connection-manager)`)
	reJsClickHouse = regexp.MustCompile(`(?:@clickhouse/client|clickhouse)`)
)

func scanNodeDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".ts") ||
			strings.HasSuffix(p, ".jsx") || strings.HasSuffix(p, ".tsx") ||
			strings.HasSuffix(p, ".mjs")
	})
	var calls []DownstreamCall
	var usages []DataStoreUsage
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)

		for _, m := range reJsHTTPCall.FindAllStringSubmatch(text, -1) {
			t := targetFromURL(m[1])
			if t != "" {
				calls = append(calls, DownstreamCall{Target: t, Driver: "http", Callsite: rel, Hint: m[1]})
			}
		}
		if reJsMongo.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "mongodb", Driver: "mongoose", Callsite: rel})
		}
		if reJsRedis.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "redis", Driver: "ioredis", Callsite: rel})
		}
		if reJsSQL.MatchString(text) {
			t := "mysql"
			if strings.Contains(text, "pg.") || strings.Contains(text, "postgres") {
				t = "postgresql"
			}
			usages = append(usages, DataStoreUsage{Type: t, Driver: "node-orm", Callsite: rel})
		}
		if reJsKafka.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "kafka", Driver: "kafkajs", Callsite: rel})
		}
		if reJsES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "@elastic/elasticsearch", Callsite: rel})
		}
		if reJsRocketMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rocketmq", Driver: "rocketmq-client-nodejs", Callsite: rel})
		}
		if reJsRabbitMQ.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "rabbitmq", Driver: "amqplib", Callsite: rel})
		}
		if reJsClickHouse.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "clickhouse", Driver: "@clickhouse/client", Callsite: rel})
		}
	}
	return dedupeCalls(calls), dedupeUsages(usages)
}
