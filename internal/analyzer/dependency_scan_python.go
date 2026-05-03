// dependency_scan_python.go —— Python 仓库 requests / pymongo / redis-py / sqlalchemy 扫描。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// requests.get("http://x") / aiohttp.ClientSession().get("http://x") / httpx.get("http://x")
	rePyHTTPCall   = regexp.MustCompile(`(?:requests|httpx|aiohttp\.\w+)\.(?:get|post|put|delete|patch|head)\(\s*["']([^"']+)["']`)
	rePyMongo      = regexp.MustCompile(`(?:pymongo|motor)\.\w+`)
	rePyRedis      = regexp.MustCompile(`redis\.(?:Redis|StrictRedis|ConnectionPool|Sentinel|RedisCluster)`)
	rePySQL        = regexp.MustCompile(`(?:sqlalchemy|peewee|tortoise|databases\.Database|psycopg|pymysql)`)
	rePyKafka      = regexp.MustCompile(`(?:kafka-python|confluent_kafka|aiokafka)`)
	rePyES         = regexp.MustCompile(`(?:elasticsearch|opensearchpy)\.`)
	rePyRocketMQ   = regexp.MustCompile(`(?:rocketmq[-_]client[-_]python|rocketmq\.client\.)`)
	rePyRabbitMQ   = regexp.MustCompile(`(?:pika\.|aio_pika|kombu\.)`)
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
