package dsprobe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
)

// probePostgres 走 lib/pq 驱动 Ping;成功后查 SELECT version() 拿版本展示。
func probePostgres(f map[string]string) (bool, string, error) {
	dsn, err := buildPgDSN(f)
	if err != nil {
		return false, "", err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return false, "", fmt.Errorf("dsn 格式错: %w", err)
	}
	defer func() { _ = db.Close() }()
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
