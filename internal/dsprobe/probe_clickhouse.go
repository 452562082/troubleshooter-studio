package dsprobe

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// probeClickHouse 走 GET /?query=SELECT+1 + 可选 Basic Auth —— /ping 不验证账密,SELECT 1 才能。
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
	q := rawURL + "/?query=SELECT+1"
	req, _ := http.NewRequest(http.MethodGet, q, nil)
	if user := f["user"]; user != "" {
		req.SetBasicAuth(user, f["pass"])
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("HTTP GET /?query 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, "", fmt.Errorf("ClickHouse 认证失败 (%d): %s", resp.StatusCode, snippet(body))
	}
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if !strings.Contains(string(body), "1") {
		return false, "", fmt.Errorf("SELECT 1 响应不对: %s", snippet(body))
	}
	return true, "SELECT 1 → 1,登录 OK", nil
}
