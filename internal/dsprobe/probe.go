// Package dsprobe 给数据层做"真账密登录"连通性测试。
//
// 跟纯 TCP dial 的区别:这里要验证用户/密码/库名是否真能 auth + 完成最小操作,
// 才能确定连接配置真正可用 —— 部署时机器人 MCP server 会用同样凭证连,
// 这里测不通的部署后也通不了。
//
// 实现:
//   redis      → go-redis Client.Ping(ctx)              (含 AUTH)
//   mysql      → database/sql + go-sql-driver/mysql .Ping()  (含登录 + 选库)
//   postgresql → database/sql + lib/pq .Ping()
//   mongodb    → mongo-driver Connect + Ping()         (含 SCRAM auth)
//   elasticsearch → HTTP GET / + basic auth
//   clickhouse → HTTP GET /ping(可带 user/password)
//   kafka / rocketmq / rabbitmq → 暂时只 TCP dial(SDK 太重 / 协议复杂,先做基本可达;
//                                                   未来可补 metadata 请求)
//
// 全部 5 秒超时。失败返回人话错误(给 UI 展示)。
package dsprobe

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
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

// ── Redis:go-redis ParseURL + Ping ────────────────────────────────────
func probeRedis(f map[string]string) (bool, string, error) {
	urlStr := strings.TrimSpace(f["url"])
	var opts *redis.Options
	if urlStr != "" {
		us := urlStr
		if !strings.Contains(us, "://") {
			us = "redis://" + us
		}
		o, err := redis.ParseURL(us)
		if err != nil {
			return false, "", fmt.Errorf("解析 url 失败: %w", err)
		}
		opts = o
	} else {
		host := strings.TrimSpace(f["host"])
		port := strings.TrimSpace(f["port"])
		if host == "" {
			return false, "", errors.New("缺 host 或 url")
		}
		if port == "" {
			port = "6379"
		}
		opts = &redis.Options{
			Addr:     net.JoinHostPort(host, port),
			Password: f["password"],
		}
	}
	opts.DialTimeout = probeTimeout
	opts.ReadTimeout = probeTimeout
	opts.WriteTimeout = probeTimeout
	cli := redis.NewClient(opts)
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		return false, "", redisErrorMsg(err)
	}
	// 顺便取个 server version 显示
	info, _ := cli.Info(ctx, "server").Result()
	return true, "PING OK · " + extractRedisVersion(info), nil
}

func extractRedisVersion(info string) string {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			return "Redis " + strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
		}
	}
	return "Redis"
}

func redisErrorMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "invalid password"):
		return fmt.Errorf("密码错误: %s", msg)
	case strings.Contains(msg, "NOAUTH"):
		return fmt.Errorf("需要密码但未提供: %s", msg)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒(端口未开?): %s", msg)
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return fmt.Errorf("超时(网络/防火墙?): %s", msg)
	}
	return err
}

// ── MySQL:database/sql + go-sql-driver/mysql Ping ─────────────────────
func probeMySQL(f map[string]string) (bool, string, error) {
	dsn, err := buildMySQLDSN(f)
	if err != nil {
		return false, "", err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, "", fmt.Errorf("dsn 格式错: %w", err)
	}
	defer db.Close()
	db.SetConnMaxLifetime(probeTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return false, "", mysqlErrorMsg(err)
	}
	var version string
	_ = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	return true, "登录 OK · MySQL " + version, nil
}

func buildMySQLDSN(f map[string]string) (string, error) {
	dsn := strings.TrimSpace(f["dsn"])
	if dsn != "" {
		// "mysql://user:pass@host:port/db" 形式 → 转成 driver 认识的 "user:pass@tcp(host:port)/db"
		if strings.Contains(dsn, "://") {
			us := dsn
			if !strings.HasPrefix(us, "mysql://") {
				us = "mysql://" + strings.TrimPrefix(us, "mysql://")
			}
			parsed, err := url.Parse(us)
			if err != nil {
				return "", fmt.Errorf("解析 dsn url 失败: %w", err)
			}
			user := ""
			pass := ""
			if parsed.User != nil {
				user = parsed.User.Username()
				pass, _ = parsed.User.Password()
			}
			port := parsed.Port()
			if port == "" {
				port = "3306"
			}
			db := strings.TrimPrefix(parsed.Path, "/")
			return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?timeout=5s&readTimeout=5s",
				user, pass, parsed.Hostname(), port, db), nil
		}
		// 已经是 "user:pass@tcp(host:port)/db" 风格;补 timeout 参数
		if strings.Contains(dsn, "?") {
			return dsn + "&timeout=5s&readTimeout=5s", nil
		}
		return dsn + "?timeout=5s&readTimeout=5s", nil
	}
	host := strings.TrimSpace(f["host"])
	if host == "" {
		return "", errors.New("缺 dsn 或 host")
	}
	port := strings.TrimSpace(f["port"])
	if port == "" {
		port = "3306"
	}
	user := strings.TrimSpace(f["user"])
	if user == "" {
		user = strings.TrimSpace(f["username"])
	}
	pass := f["password"]
	db := strings.TrimSpace(f["database"])
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?timeout=5s&readTimeout=5s",
		user, pass, host, port, db), nil
}

func mysqlErrorMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Access denied"):
		return fmt.Errorf("账号密码错: %s", msg)
	case strings.Contains(msg, "Unknown database"):
		return fmt.Errorf("数据库名不存在: %s", msg)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒(端口未开?): %s", msg)
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return fmt.Errorf("超时: %s", msg)
	}
	return err
}

// ── PostgreSQL:database/sql + lib/pq Ping ─────────────────────────────
func probePostgres(f map[string]string) (bool, string, error) {
	dsn, err := buildPgDSN(f)
	if err != nil {
		return false, "", err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return false, "", fmt.Errorf("dsn 格式错: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return false, "", pgErrorMsg(err)
	}
	var version string
	_ = db.QueryRowContext(ctx, "SELECT version()").Scan(&version)
	if i := strings.Index(version, " on "); i > 0 {
		version = version[:i]
	}
	return true, "登录 OK · " + version, nil
}

func buildPgDSN(f map[string]string) (string, error) {
	dsn := strings.TrimSpace(f["dsn"])
	if dsn != "" {
		if !strings.Contains(dsn, "://") && !strings.Contains(dsn, "=") {
			return "", fmt.Errorf("dsn 格式不像 postgres URL 也不像 key=value 串")
		}
		// 加超时参数
		if strings.Contains(dsn, "://") {
			if strings.Contains(dsn, "?") {
				return dsn + "&connect_timeout=5", nil
			}
			return dsn + "?connect_timeout=5", nil
		}
		return dsn + " connect_timeout=5", nil
	}
	host := strings.TrimSpace(f["host"])
	if host == "" {
		return "", errors.New("缺 dsn 或 host")
	}
	port := strings.TrimSpace(f["port"])
	if port == "" {
		port = "5432"
	}
	user := strings.TrimSpace(f["user"])
	if user == "" {
		user = strings.TrimSpace(f["username"])
	}
	pass := f["password"]
	db := strings.TrimSpace(f["database"])
	if db == "" {
		db = "postgres"
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		host, port, user, pass, db), nil
}

func pgErrorMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "password authentication failed"):
		return fmt.Errorf("密码错误: %s", msg)
	case strings.Contains(msg, "role") && strings.Contains(msg, "does not exist"):
		return fmt.Errorf("用户不存在: %s", msg)
	case strings.Contains(msg, "database") && strings.Contains(msg, "does not exist"):
		return fmt.Errorf("数据库不存在: %s", msg)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒: %s", msg)
	case strings.Contains(msg, "timeout"):
		return fmt.Errorf("超时: %s", msg)
	}
	return err
}

// ── MongoDB:mongo-driver Connect + Ping ───────────────────────────────
//
// 关键陷阱:mongo-driver 的 ApplyURI() 内部会对 user/password 做 URL 解码,
// 用户密码若含 `< ] ^ @` 等特殊字符且未在 URI 里编码,会被截断 / 破坏 → 服务端报
// "Authentication failed" 但密码其实是对的。
//
// 解法:**手工拆 URI** 把 host/db/authSource 喂给 ApplyURI(不带凭证),
// 用户名密码用 SetAuth(Credential{...}) 单独传 —— SDK 不再对密码做编码 / 解码,
// 原文 SCRAM。这样无论密码多怪都能通。
func probeMongoDB(f map[string]string) (bool, string, error) {
	uri := strings.TrimSpace(f["uri"])
	if uri == "" {
		uri = strings.TrimSpace(f["url"])
	}

	var rawUser, rawPass, hostPart, dbPart, authSource string

	if uri != "" {
		// 手工拆,不走 url.Parse(它对密码也会做解码)
		s := uri
		if i := strings.Index(s, "://"); i >= 0 {
			s = s[i+3:]
		}
		// userinfo 与 host 用最后一个 @ 分隔(host 部分不会含 @)
		hostAndPath := s
		if at := strings.LastIndex(s, "@"); at >= 0 {
			ui := s[:at]
			hostAndPath = s[at+1:]
			// user:pass —— 第一个 : 之前是 user
			if c := strings.Index(ui, ":"); c >= 0 {
				rawUser = ui[:c]
				rawPass = ui[c+1:]
			} else {
				rawUser = ui
			}
		}
		// host[:port] / db ? query
		if slash := strings.Index(hostAndPath, "/"); slash >= 0 {
			hostPart = hostAndPath[:slash]
			rest := hostAndPath[slash+1:]
			if q := strings.Index(rest, "?"); q >= 0 {
				dbPart = rest[:q]
				for _, kv := range strings.Split(rest[q+1:], "&") {
					if strings.HasPrefix(kv, "authSource=") {
						authSource = strings.TrimPrefix(kv, "authSource=")
					}
				}
			} else {
				dbPart = rest
			}
		} else {
			hostPart = hostAndPath
		}
	} else {
		// 用 host/port/user/password/database 字段拼
		host := strings.TrimSpace(f["host"])
		if host == "" {
			return false, "", errors.New("缺 uri 或 host")
		}
		port := strings.TrimSpace(f["port"])
		if port == "" {
			port = "27017"
		}
		hostPart = host + ":" + port
		rawUser = strings.TrimSpace(f["user"])
		if rawUser == "" {
			rawUser = strings.TrimSpace(f["username"])
		}
		rawPass = f["password"]
		dbPart = strings.TrimSpace(f["database"])
	}

	if hostPart == "" {
		return false, "", errors.New("无法从 uri 解析出 host")
	}
	// 拼"安全 URI"(只含 host + 可选 db),凭证走 SetAuth 不经过 URL 解析
	safeURI := "mongodb://" + hostPart
	if dbPart != "" {
		safeURI += "/" + dbPart
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	opts := options.Client().ApplyURI(safeURI).
		SetConnectTimeout(probeTimeout).
		SetServerSelectionTimeout(probeTimeout)
	if rawUser != "" {
		cred := options.Credential{Username: rawUser, Password: rawPass}
		// authSource 优先用 URI 里 query;没指定时 mongo-driver 默认走 connect db
		// 这里不强加 admin —— 若用户的 root 真建在 connect db 里,加 admin 反而会错
		if authSource != "" {
			cred.AuthSource = authSource
		}
		opts.SetAuth(cred)
	}
	cli, err := mongo.Connect(ctx, opts)
	if err != nil {
		return false, "", mongoErrorMsg(err)
	}
	defer func() { _ = cli.Disconnect(context.Background()) }()
	if err := cli.Ping(ctx, readpref.Primary()); err != nil {
		return false, "", mongoErrorMsg(err)
	}
	var doc map[string]any
	_ = cli.Database("admin").RunCommand(ctx, map[string]any{"buildInfo": 1}).Decode(&doc)
	ver := "MongoDB"
	if v, ok := doc["version"].(string); ok {
		ver = "MongoDB " + v
	}
	return true, "登录 OK · " + ver, nil
}

func mongoErrorMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Authentication failed"):
		return fmt.Errorf("账号密码错: %s", msg)
	case strings.Contains(msg, "Unauthorized"):
		return fmt.Errorf("权限不足: %s", msg)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒: %s", msg)
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "context deadline"):
		return fmt.Errorf("连接 / 选举超时(地址错 / VPC 不通?): %s", msg)
	}
	return err
}

// ── Elasticsearch:HTTP GET / + Basic Auth ──────────────────────────────
func probeElasticsearch(f map[string]string) (bool, string, error) {
	rawURL := strings.TrimSpace(f["url"])
	if rawURL == "" {
		return false, "", errors.New("缺 url")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	rawURL = strings.TrimRight(rawURL, "/")
	cli := &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	req, _ := http.NewRequest("GET", rawURL+"/", nil)
	if user := f["user"]; user != "" {
		req.SetBasicAuth(user, f["pass"])
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("HTTP GET 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == 401 {
		return false, "", fmt.Errorf("ES 认证失败 (401):账号密码错")
	}
	if resp.StatusCode == 403 {
		return false, "", fmt.Errorf("ES 权限不足 (403)")
	}
	if resp.StatusCode != 200 {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	bs := string(body)
	if !strings.Contains(bs, "cluster_name") && !strings.Contains(bs, "lucene_version") {
		return false, "", fmt.Errorf("响应不像 ES (body: %s)", snippet(body))
	}
	return true, "登录 OK · " + snippet(body), nil
}

// ── ClickHouse:HTTP GET /ping(可选 user/pass) ────────────────────────
func probeClickHouse(f map[string]string) (bool, string, error) {
	rawURL := strings.TrimSpace(f["url"])
	if rawURL == "" {
		return false, "", errors.New("缺 url")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	rawURL = strings.TrimRight(rawURL, "/")
	cli := &http.Client{Timeout: probeTimeout}
	// /ping 不需要 auth,但 SELECT 1 需要 —— 我们走 SELECT 1 才能验证账密
	// GET /?query=SELECT+1 + basic auth(默认 8123)
	q := rawURL + "/?query=SELECT+1"
	req, _ := http.NewRequest("GET", q, nil)
	if user := f["user"]; user != "" {
		req.SetBasicAuth(user, f["pass"])
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("HTTP GET /?query 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return false, "", fmt.Errorf("ClickHouse 认证失败 (%d): %s", resp.StatusCode, snippet(body))
	}
	if resp.StatusCode != 200 {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if !strings.Contains(string(body), "1") {
		return false, "", fmt.Errorf("SELECT 1 响应不对: %s", snippet(body))
	}
	return true, "SELECT 1 → 1,登录 OK", nil
}

// ── Kafka / RocketMQ / RabbitMQ:暂时只 TCP dial ────────────────────────
// SDK 比较重(kafka-go / rabbitmq-amqp091),且 SASL/PLAIN 鉴权细节多;先做 TCP 通,
// 后续如果用户需要再补完整 metadata + SASL 握手。
func probeKafka(f map[string]string) (bool, string, error) {
	brokers := strings.TrimSpace(f["brokers"])
	if brokers == "" {
		return false, "", errors.New("缺 brokers")
	}
	parts := splitCSV(brokers)
	var lastErr error
	for _, p := range parts {
		host, port, err := splitHostPort(p, "9092")
		if err != nil {
			lastErr = err
			continue
		}
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), probeTimeout); err == nil {
			conn.Close()
			return true, fmt.Sprintf("TCP 通 %s(SASL 鉴权未验证)", host+":"+port), nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return false, "", lastErr
	}
	return false, "", errors.New("所有 broker 都不通")
}

func probeRocketMQ(f map[string]string) (bool, string, error) {
	srv := strings.TrimSpace(f["namesrv"])
	if srv == "" {
		return false, "", errors.New("缺 namesrv")
	}
	srv = strings.NewReplacer(";", ",", " ", "").Replace(srv)
	parts := splitCSV(srv)
	var lastErr error
	for _, p := range parts {
		host, port, err := splitHostPort(p, "9876")
		if err != nil {
			lastErr = err
			continue
		}
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), probeTimeout); err == nil {
			conn.Close()
			return true, fmt.Sprintf("TCP 通 %s", host+":"+port), nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return false, "", lastErr
	}
	return false, "", errors.New("所有 namesrv 都不通")
}

func probeRabbitMQ(f map[string]string) (bool, string, error) {
	rawURL := strings.TrimSpace(f["url"])
	if rawURL == "" {
		return false, "", errors.New("缺 url")
	}
	us := rawURL
	if !strings.Contains(us, "://") {
		us = "amqp://" + us
	}
	parsed, err := url.Parse(us)
	if err != nil {
		return false, "", fmt.Errorf("解析 url 失败: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return false, "", errors.New("缺 host")
	}
	port := parsed.Port()
	if port == "" {
		port = "5672"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), probeTimeout)
	if err != nil {
		return false, "", fmt.Errorf("TCP dial 失败: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("AMQP\x00\x00\x09\x01")); err != nil {
		return false, "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(probeTimeout))
	buf := make([]byte, 16)
	n, _ := conn.Read(buf)
	if n == 0 {
		return false, "", errors.New("AMQP 握手无响应(端口对吗?)")
	}
	return true, fmt.Sprintf("AMQP 握手 OK · %d 字节响应(账密未验证)", n), nil
}

// ── helpers ───────────────────────────────────────────────────────────

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

// ProbeHTTPURL 给 Step 3 环境列表的 api_domain / web_domain 用 —— 不带 auth。
func ProbeHTTPURL(rawURL string) Result {
	return ProbeHTTPURLAuth(rawURL, "", "", "")
}

// ProbeHTTPURLAuth 给 Step 7 可观测性组件用 —— 可选 basic auth + 可选 Bearer / API Key。
// 简单 GET URL,HTTP 任何 < 500 都算"可达"(401/403/404 也算,至少 server 在);
// 不带凭证返 401 → 拦下报"需要鉴权";带凭证 401 → "鉴权失败"。
func ProbeHTTPURLAuth(rawURL, user, pass, apiKey string) Result {
	start := time.Now()
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return Result{OK: false, Error: "URL 为空"}
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	cli := &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return Result{OK: false, Error: fmt.Sprintf("URL 格式错: %v", err)}
	}
	req.Header.Set("User-Agent", "tshoot-studio-probe/1.0")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	if apiKey != "" {
		// Grafana glsa_ 风格通常用 Bearer
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := cli.Do(req)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "no such host"):
			return Result{OK: false, Error: "DNS 解析失败,域名不存在"}
		case strings.Contains(msg, "connection refused"):
			return Result{OK: false, Error: "连接被拒(端口未开?)"}
		case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
			return Result{OK: false, Error: "超时(网络/防火墙?)"}
		case strings.Contains(msg, "x509") || strings.Contains(msg, "tls"):
			return Result{OK: false, Error: fmt.Sprintf("TLS 错: %s", msg)}
		}
		return Result{OK: false, Error: msg}
	}
	defer resp.Body.Close()
	latency := time.Since(start).Round(time.Millisecond).String()
	if resp.StatusCode == 401 {
		if user == "" && apiKey == "" {
			return Result{OK: false, Latency: latency, Error: "HTTP 401:需要鉴权(user/pass 或 api_key)"}
		}
		return Result{OK: false, Latency: latency, Error: "HTTP 401:鉴权失败(账密错或 api_key 无效)"}
	}
	if resp.StatusCode == 403 {
		return Result{OK: false, Latency: latency, Error: "HTTP 403:权限不足"}
	}
	if resp.StatusCode >= 500 {
		return Result{OK: false, Latency: latency, Error: fmt.Sprintf("HTTP %d (服务端错)", resp.StatusCode)}
	}
	return Result{
		OK: true, Latency: latency,
		Detail: fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
