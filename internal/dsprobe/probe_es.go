package dsprobe

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// probeElasticsearch HTTP GET / + 可选 Basic Auth;响应 body 必须含 cluster_name / lucene_version
// 才算真 ES,不然反向代理也会 200。
func probeElasticsearch(f map[string]string) (bool, string, error) {
	rawURL := strings.TrimSpace(f["url"])
	if rawURL == "" {
		return false, "", errors.New("缺 url")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	rawURL = strings.TrimRight(rawURL, "/")
	hasCreds := f["user"] != ""
	cli := &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: TLSConfigForProbe(hasCreds)},
	}
	req, _ := http.NewRequest(http.MethodGet, rawURL+"/", nil)
	if hasCreds {
		req.SetBasicAuth(f["user"], f["pass"])
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("HTTP GET 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusUnauthorized {
		return false, "", fmt.Errorf("ES 认证失败 (401):账号密码错")
	}
	if resp.StatusCode == http.StatusForbidden {
		return false, "", fmt.Errorf("ES 权限不足 (403)")
	}
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	bs := string(body)
	if !strings.Contains(bs, "cluster_name") && !strings.Contains(bs, "lucene_version") {
		return false, "", fmt.Errorf("响应不像 ES (body: %s)", snippet(body))
	}
	return true, "登录 OK · " + snippet(body), nil
}
