package dsprobe

// 消息队列(Kafka / RabbitMQ)目前只 TCP / 协议握手验证可达性 ——
// SDK 重(kafka-go / rabbitmq-amqp091)、SASL/PLAIN 鉴权细节多;先做基础探活,
// 用户后续真要验证账密再补完整 metadata 请求。

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

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
			_ = conn.Close()
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

// probeRabbitMQ 不止 TCP dial:发完 AMQP 协议头("AMQP\x00\x00\x09\x01")再读响应,
// 真 broker 才会回 16+ 字节的 Connection.Start 帧。账密仍未验证。
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
	defer func() { _ = conn.Close() }()
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
