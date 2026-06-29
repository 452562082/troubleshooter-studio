package dsprobe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

// probeMySQL 用 database/sql + go-sql-driver Ping;成功后查 SELECT VERSION() 拿版本展示。
func probeMySQL(f map[string]string) (bool, string, error) {
	dsn, err := buildSQLDSN(f, "3306")
	if err != nil {
		return false, "", err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, "", fmt.Errorf("dsn 格式错: %w", err)
	}
	defer func() { _ = db.Close() }()
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

// probeDoris 复用 MySQL wire protocol;Doris FE query port 默认 9030。
func probeDoris(f map[string]string) (bool, string, error) {
	dsn, err := buildSQLDSN(f, "9030")
	if err != nil {
		return false, "", err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, "", fmt.Errorf("dsn 格式错: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetConnMaxLifetime(probeTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return false, "", mysqlErrorMsg(err)
	}
	var version string
	_ = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	return true, "登录 OK · Doris " + version, nil
}

// buildMySQLDSN 兼容三种输入:dsn 是 URL 格式 / dsn 是 driver 原生格式 / 走单字段拼。
func buildMySQLDSN(f map[string]string) (string, error) {
	return buildSQLDSN(f, "3306")
}

// buildSQLDSN 兼容 MySQL 协议系输入:dsn 是 URL 格式 / driver 原生格式 / 走单字段拼。
func buildSQLDSN(f map[string]string, defaultPort string) (string, error) {
	dsn := strings.TrimSpace(f["dsn"])
	if dsn != "" {
		// "mysql://user:pass@host:port/db" / "doris://..." 形式 → 转成 driver 认识的 "user:pass@tcp(host:port)/db"
		if strings.Contains(dsn, "://") {
			us := dsn
			if !strings.HasPrefix(us, "mysql://") && !strings.HasPrefix(us, "doris://") {
				us = "mysql://" + strings.TrimPrefix(strings.TrimPrefix(us, "mysql://"), "doris://")
			}
			if strings.HasPrefix(us, "doris://") {
				us = "mysql://" + strings.TrimPrefix(us, "doris://")
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
				port = defaultPort
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
		port = defaultPort
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
