// dependency_scan_java.go —— Java/Kotlin 仓库的 @FeignClient + Spring Data 模板扫描。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
	reJavaDoris       = regexp.MustCompile(`(?i)(?:doris-jdbc|org\.apache\.doris|DorisDataSource|jdbc:doris|doris[_-]?fe)`)
	reJavaES          = regexp.MustCompile(`(?:ElasticsearchClient|RestHighLevelClient|ElasticsearchOperations)`)
	// RabbitMQ:RabbitTemplate(spring) / Connection/Channel(amqp-client)
	reJavaRabbitMQ = regexp.MustCompile(`(?:RabbitTemplate|com\.rabbitmq\.client\.Connection|@RabbitListener)`)
	// ClickHouse:JDBC + clickhouse-jdbc
	reJavaClickHouse = regexp.MustCompile(`(?:clickhouse-jdbc|ClickHouseDataSource|com\.clickhouse)`)
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
		if reJavaDoris.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "doris", Driver: "doris-jdbc/mysql-protocol", Callsite: rel})
		}
		if reJavaES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "spring-data-es", Callsite: rel})
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
