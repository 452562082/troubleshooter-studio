package dsprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/redis/go-redis/v9"
)

// probeRedis 用 go-redis ParseURL 或 host/port/password 拼参,Ping 后顺便取 server version。
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
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		return false, "", redisErrorMsg(err)
	}
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
