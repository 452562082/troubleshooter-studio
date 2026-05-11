// Package dsprobe 给数据层做"真账密登录"连通性测试。
//
// 跟纯 TCP dial 的区别:这里要验证用户/密码/库名是否真能 auth + 完成最小操作,
// 才能确定连接配置真正可用 —— 部署时机器人 MCP server 会用同样凭证连,
// 这里测不通的部署后也通不了。
//
// 实现按数据源分到子文件:
//
//	redis         → probe_redis.go      go-redis Client.Ping(ctx)              (含 AUTH)
//	mysql         → probe_mysql.go      database/sql + go-sql-driver/mysql .Ping()
//	postgresql    → probe_postgres.go   database/sql + lib/pq .Ping()
//	mongodb       → probe_mongo.go      mongo-driver Connect + Ping()         (含 SCRAM auth)
//	elasticsearch → probe_es.go         HTTP GET / + basic auth
//	clickhouse    → probe_clickhouse.go HTTP GET /?query=SELECT+1 (验账密)
//	kafka/rocketmq/rabbitmq → probe_mq.go  TCP/AMQP 握手(SDK 重,先做基础)
//	通用 HTTP probe (env / Grafana 等) → probe_http.go
//
// 全部 5 秒超时。失败返回人话错误(给 UI 展示)。
package dsprobe

import (
	"fmt"
	"net"
	"strings"
	"time"
)

type Request struct {
	Type   string            `json:"type"`
	Fields map[string]string `json:"fields"`
}

type Result struct {
	OK      bool   `json:"ok"`
	Latency string `json:"latency,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
}

const probeTimeout = 5 * time.Second

func Probe(req Request) Result {
	start := time.Now()
	ok, detail, err := probe(req)
	r := Result{OK: ok, Detail: detail}
	if err != nil {
		r.Error = err.Error()
	}
	if ok {
		r.Latency = time.Since(start).Round(time.Millisecond).String()
	}
	return r
}

func probe(req Request) (bool, string, error) {
	switch req.Type {
	case "redis":
		return probeRedis(req.Fields)
	case "mongodb":
		return probeMongoDB(req.Fields)
	case "mysql":
		return probeMySQL(req.Fields)
	case "postgresql":
		return probePostgres(req.Fields)
	case "elasticsearch":
		return probeElasticsearch(req.Fields)
	case "kafka":
		return probeKafka(req.Fields)
	case "rocketmq":
		return probeRocketMQ(req.Fields)
	case "rabbitmq":
		return probeRabbitMQ(req.Fields)
	case "clickhouse":
		return probeClickHouse(req.Fields)
	}
	return false, "", fmt.Errorf("不支持的类型: %s", req.Type)
}

// ── 跨数据源共享 helpers(probe_mq / probe_es / probe_clickhouse 都用) ──

func splitHostPort(hp, defaultPort string) (string, string, error) {
	if !strings.Contains(hp, ":") {
		return hp, defaultPort, nil
	}
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return "", "", fmt.Errorf("解析 host:port %q 失败: %w", hp, err)
	}
	if port == "" {
		port = defaultPort
	}
	return host, port, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
