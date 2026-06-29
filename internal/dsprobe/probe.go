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
//	doris         → probe_mysql.go      复用 MySQL 协议 Ping(),默认端口 9030
//	postgresql    → probe_postgres.go   database/sql + lib/pq .Ping()
//	mongodb       → probe_mongo.go      mongo-driver Connect + Ping()         (含 SCRAM auth)
//	elasticsearch → probe_es.go         HTTP GET / + basic auth
//	clickhouse    → probe_clickhouse.go HTTP GET /?query=SELECT+1 (验账密)
//	kafka/rabbitmq → probe_mq.go  TCP/AMQP 握手(SDK 重,先做基础)
//	通用 HTTP probe (env / Grafana 等) → probe_http.go
//
// 全部 5 秒超时。失败返回人话错误(给 UI 展示)。
package dsprobe

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
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

// InsecureTLSEnv 是放行跳过 TLS 校验的环境变量名。内网自签证书 + 带鉴权的探活场景下,
// 用户显式 export 它即可恢复旧的"跳过校验"行为(见 TLSConfigForProbe)。
const InsecureTLSEnv = "TSHOOT_INSECURE_TLS"

// TLSConfigForProbe 返回探活 HTTP client 用的 TLS 配置,在"防 MITM 偷凭据"和
// "内网自签证书很常见"之间取平衡:
//   - hasCreds=false(纯连通性探测,不发任何 basic auth / token):跳过校验无所谓
//     —— 没秘密可被中间人偷,且内网自签很普遍,校验只会徒增误报。
//   - hasCreds=true(要发凭据):默认校验证书,防中间人用伪造证书截获 basic auth / Bearer。
//   - 逃生口:确属内网自签 + 带鉴权,用户 export TSHOOT_INSECURE_TLS=1 显式放行。
//
// 注意 //nolint:gosec —— InsecureSkipVerify 仅在无凭据或用户显式 opt-in 时为 true,是有意为之。
func TLSConfigForProbe(hasCreds bool) *tls.Config {
	if !hasCreds || insecureTLSOptIn() {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 见上:无凭据 or 用户显式放行
	}
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// insecureTLSOptIn 读环境变量判断用户是否显式放行跳过校验。
func insecureTLSOptIn() bool {
	v := strings.TrimSpace(os.Getenv(InsecureTLSEnv))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

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
	case "doris":
		return probeDoris(req.Fields)
	case "postgresql":
		return probePostgres(req.Fields)
	case "elasticsearch":
		return probeElasticsearch(req.Fields)
	case "kafka":
		return probeKafka(req.Fields)
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
