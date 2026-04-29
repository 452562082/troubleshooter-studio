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

// ── Go ─────────────────────────────────────────────────────────────────
var (
	// http.Get / http.Post / client.Get / client.Do(req with URL) — 取 URL 字面量
	reGoHTTPCall = regexp.MustCompile(`(?i)http\.(Get|Post|Head|Do)\(\s*"([^"]+)"`)
	// grpc.Dial("host:port", ...)
	reGoGRPCDial = regexp.MustCompile(`grpc\.Dial\(\s*"([^"]+)"`)
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
)

func scanGoDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
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
	}
	return dedupeCalls(calls), dedupeUsages(usages)
}
