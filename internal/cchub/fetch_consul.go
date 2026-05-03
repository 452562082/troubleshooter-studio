// fetch_consul.go —— Consul KV 单条配置拉取 + Batch。GET /v1/kv/<key>?raw 直接返原文。
package cchub

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func fetchConsul(req FetchContentRequest) (*FetchContentResult, error) {
	if req.DataID == "" {
		return nil, fmt.Errorf("consul: data_id(完整 kv key)必填")
	}
	addr := strings.TrimSpace(req.Addr)
	if addr == "" {
		return nil, fmt.Errorf("consul: addr 必填")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")

	u := fmt.Sprintf("%s/v1/kv/%s?raw", addr, req.DataID)
	r, _ := http.NewRequest("GET", u, nil)
	if req.Token != "" {
		r.Header.Set("X-Consul-Token", req.Token)
	}
	httpCli := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpCli.Do(r)
	if err != nil {
		return nil, fmt.Errorf("consul get kv: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("consul 找不到 key=%s(404)", req.DataID)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("consul get kv status=%d: %s", resp.StatusCode, snippet(body))
	}
	content := string(body)
	return &FetchContentResult{Content: content, Format: guessFormat(req.DataID, content)}, nil
}

func fetchConsulBatch(req FetchBatchRequest) (*FetchBatchResult, error) {
	out := &FetchBatchResult{Items: make([]FetchBatchItemResult, 0, len(req.Items))}
	for i, item := range req.Items {
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		r, err := fetchConsul(FetchContentRequest{
			Type:      "consul",
			Addr:      req.Addr,
			Token:     req.Token,
			Namespace: item.Namespace,
			DataID:    item.DataID,
		})
		if err != nil {
			out.Items = append(out.Items, FetchBatchItemResult{Key: item.Key, OK: false, Error: err.Error()})
			continue
		}
		out.Items = append(out.Items, FetchBatchItemResult{Key: item.Key, OK: true, Result: r})
	}
	return out, nil
}
