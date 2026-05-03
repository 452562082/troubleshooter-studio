// fetch_apollo.go —— Apollo 单条配置拉取 + Batch 实现。
// HTTP 无状态(只 token header),Batch 仍复用一个 http.Client 省 TLS handshake。
package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func fetchApollo(req FetchContentRequest) (*FetchContentResult, error) {
	if req.AppID == "" || req.DataID == "" {
		return nil, fmt.Errorf("apollo: app_id 与 data_id(namespaceName)都必填")
	}
	addr := strings.TrimSpace(req.Addr)
	if addr == "" {
		return nil, fmt.Errorf("apollo: addr 必填")
	}
	if req.Token == "" {
		return nil, fmt.Errorf("apollo: token 必填")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")
	env := req.Namespace
	if env == "" {
		env = "DEV"
	}
	cluster := req.Group
	if cluster == "" {
		cluster = "default"
	}

	// GET /openapi/v1/envs/<env>/apps/<appId>/clusters/<cluster>/namespaces/<ns>/releases/latest
	u := fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters/%s/namespaces/%s/releases/latest",
		addr, env, req.AppID, cluster, req.DataID)
	r, _ := http.NewRequest("GET", u, nil)
	r.Header.Set("Authorization", req.Token)
	httpCli := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpCli.Do(r)
	if err != nil {
		return nil, fmt.Errorf("apollo get release: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("apollo token 无权限(status=%d): %s", resp.StatusCode, snippet(body))
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("apollo get release status=%d: %s", resp.StatusCode, snippet(body))
	}
	// release 里 configurations 是 map[string]string —— 同名 key/value。重新序列为 yaml 风格 (key: value)
	var doc struct {
		Configurations map[string]string `json:"configurations"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	// 简单拼成 yaml-ish,前端用 js-yaml 的话能兼容
	var sb strings.Builder
	for k, v := range doc.Configurations {
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	return &FetchContentResult{Content: sb.String(), Format: "yaml"}, nil
}

func fetchApolloBatch(req FetchBatchRequest) (*FetchBatchResult, error) {
	out := &FetchBatchResult{Items: make([]FetchBatchItemResult, 0, len(req.Items))}
	for i, item := range req.Items {
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		r, err := fetchApollo(FetchContentRequest{
			Type:      "apollo",
			Addr:      req.Addr,
			Token:     req.Token,
			Namespace: item.Namespace,
			Group:     item.Group,
			DataID:    item.DataID,
			AppID:     item.AppID,
		})
		if err != nil {
			out.Items = append(out.Items, FetchBatchItemResult{Key: item.Key, OK: false, Error: err.Error()})
			continue
		}
		out.Items = append(out.Items, FetchBatchItemResult{Key: item.Key, OK: true, Result: r})
	}
	return out, nil
}
