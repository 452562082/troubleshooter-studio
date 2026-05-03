// dependency_scan_go.go —— Go 仓库的下游调用 / 数据层使用扫描。
//
// 关键设计:第一遍先扫所有 services.go 风格的 ServiceName 常量定义建表,第二遍扫
// client.New<X>Client(_, <X>ServiceName, _) 时用常量表解析真实服务名(truss / go-zero
// / kratos 等微服务的统一约定)。常量解析失败回退到驼峰转 kebab。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
	reGoMongo        = regexp.MustCompile(`mongo\.(Connect|NewClient)`)
	reGoRedis        = regexp.MustCompile(`redis\.(NewClient|NewClusterClient|NewFailoverClient)`)
	reGoSQL          = regexp.MustCompile(`(?:gorm\.Open|sql\.Open)\(\s*(?:mysql\.|postgres\.|"mysql"|"postgres")`)
	reGoKafka        = regexp.MustCompile(`(?:kafka\.New|sarama\.New)`)
	reGoES           = regexp.MustCompile(`(?:elasticsearch\.NewClient|elastic\.NewClient|opensearch)`)
	reGoRocketMQ     = regexp.MustCompile(`(?:rocketmq\.New|primitive\.NewMessage)|"github\.com/apache/rocketmq-client-go`)
	reGoRabbitMQ     = regexp.MustCompile(`(?:amqp\.Dial|streadway/amqp)`)
	reGoClickHouse   = regexp.MustCompile(`(?:clickhouse\.Open|ClickHouse\{|"github\.com/ClickHouse/clickhouse-go)`)
)

func scanGoDeps(repoPath string, include []string) ([]DownstreamCall, []DataStoreUsage) {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
	})
	// 第一遍:扫所有 services.go 风格的常量定义,建 ServiceName const → 实际值 的映射。
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
