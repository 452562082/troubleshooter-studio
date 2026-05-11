package dsprobe

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
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
	if resp.StatusCode == http.StatusUnauthorized {
		if user == "" && apiKey == "" {
			return Result{OK: false, Latency: latency, Error: "HTTP 401:需要鉴权(user/pass 或 api_key)"}
		}
		return Result{OK: false, Latency: latency, Error: "HTTP 401:鉴权失败(账密错或 api_key 无效)"}
	}
	if resp.StatusCode == http.StatusForbidden {
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
