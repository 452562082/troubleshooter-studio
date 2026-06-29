// dependency_scan_node.go —— Node/TypeScript 仓库 axios / fetch / mongoose / ioredis / typeorm 扫描。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// axios.get("http://x") / fetch("http://x") / got.get("http://x") / superagent.get("http://x")
	reJsHTTPCall   = regexp.MustCompile(`(?:axios|fetch|got|superagent)\.?(?:get|post|put|delete|patch)?\(\s*["']([^"']+)["']`)
	reJsMongo      = regexp.MustCompile(`(?:mongoose|MongoClient)\.?(?:connect|connection)`)
	reJsRedis      = regexp.MustCompile(`(?:new\s+(?:Redis|IORedis)|require\s*\(\s*["']ioredis|redis\.createClient)`)
	reJsSQL        = regexp.MustCompile(`(?:typeorm|prisma|sequelize|mysql2|pg\.Pool)`)
	reJsDoris      = regexp.MustCompile(`(?i)(?:doris[_-]?fe|jdbc:doris|DORIS_)`)
	reJsKafka      = regexp.MustCompile(`(?:kafkajs|node-rdkafka)`)
	reJsES         = regexp.MustCompile(`(?:@elastic/elasticsearch|@opensearch-project)`)
	reJsRabbitMQ   = regexp.MustCompile(`(?:amqplib|amqp-connection-manager)`)
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
		if reJsDoris.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "doris", Driver: "mysql-protocol", Callsite: rel})
		}
		if reJsKafka.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "kafka", Driver: "kafkajs", Callsite: rel})
		}
		if reJsES.MatchString(text) {
			usages = append(usages, DataStoreUsage{Type: "elasticsearch", Driver: "@elastic/elasticsearch", Callsite: rel})
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
