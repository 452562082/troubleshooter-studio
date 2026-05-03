// fetch_nacos.go —— Nacos 拉单条配置 + Batch + 共享 connect/retry helpers。
//
// 跟 nacos.go 的 Preload(列表)分工:本文件管"按 dataId 拉具体内容"。
// connectNacos + fetchOneConfig 给 batch 复用同一个 client + token,省 N-1 次 probe+login。
package cchub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// connectNacos 建立一个 nacos client 并完成 probe + login(若需要)。
// 返回一个可直接多次 fetchOneConfig 的 client —— 对 batch 拉取复用同一个 token。
func connectNacos(addr, username, password string) (*nacosClient, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("nacos: addr 必填")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")

	cli := &nacosClient{
		base: addr,
		// 20s:一次 fetch 实际会做 probe(4 candidate)+login+get 三步,总耗时可能接近 10s;
		// 给充裕 headroom 避免正常响应被 context deadline 误杀成 transient 错误。
		httpCli:  &http.Client{Timeout: 20 * time.Second},
		username: username,
		password: password,
	}
	flavor, probeNote, err := cli.probeFlavor()
	if err != nil {
		return nil, err
	}
	cli.flavor = flavor
	cli.probeNote = probeNote
	if cli.username != "" {
		if err := cli.login(); err != nil {
			return nil, fmt.Errorf("nacos 登录失败: %w", err)
		}
	}
	return cli, nil
}

// fetchOneConfig 拉单条配置;需要 client 已完成 connect(flavor + token 就绪)。
// 这是 batch / 单条 的共享底层实现。
func (cli *nacosClient) fetchOneConfig(namespace, group, dataID string) (*FetchContentResult, error) {
	if dataID == "" {
		return nil, fmt.Errorf("data_id 必填")
	}
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	return cli.fetchOneConfigInternal(namespace, group, dataID)
}

func fetchNacos(req FetchContentRequest) (*FetchContentResult, error) {
	if req.DataID == "" {
		return nil, fmt.Errorf("nacos: data_id 必填")
	}
	// 复用连接池 —— 同凭证已登录过直接拿 token
	cli, err := getOrConnectNacos(req.Addr, req.Username, req.Password)
	if err != nil {
		return nil, err
	}
	return cli.fetchOneConfig(req.Namespace, req.Group, req.DataID)
}

// fetchOneConfigInternal —— 实现拉取单条配置(tenant/dataId/group 三元组)。
// 路径按 v3 优先顺序逐个试,任一 200 就用。
func (cli *nacosClient) fetchOneConfigInternal(namespace, group, dataID string) (*FetchContentResult, error) {
	addr := cli.base
	flavor := cli.flavor
	req := FetchContentRequest{DataID: dataID, Group: group, Namespace: namespace}

	// Nacos 不同版本 / 不同部署下,config 详情 API 路径不一 —— 我们按 "v3 console → v3 client → v1"
	// 的顺序依次试,首个返 2xx 就用。v3 console 是管理面路径(跟 list 用的一致),
	// v3 client 是 SDK 路径(某些只开 client API 的部署有),v1 是老版本兜底。
	var candidates []string
	q := url.Values{}
	q.Set("dataId", req.DataID)
	if cli.token != "" {
		q.Set("accessToken", cli.token)
	}
	if flavor.Version == "v3" {
		q3 := cloneValues(q)
		q3.Set("groupName", group)
		q3.Set("namespaceId", req.Namespace)
		enc := q3.Encode()
		candidates = []string{
			addr + flavor.ContextPath + "/v3/console/cs/config?" + enc, // console 主路径(跟 list 一致)
			addr + flavor.ContextPath + "/v3/admin/cs/config?" + enc,   // admin 路径(某些 3.x)
			addr + flavor.ContextPath + "/v3/cs/config?" + enc,         // client SDK 路径
		}
	}
	q1 := cloneValues(q)
	q1.Set("group", group)
	if req.Namespace != "" {
		q1.Set("tenant", req.Namespace)
	}
	candidates = append(candidates, addr+flavor.ContextPath+"/v1/cs/configs?"+q1.Encode())

	var lastErr string
	var attempts []string
	for _, u := range candidates {
		resp, err := cli.httpCli.Get(u)
		if err != nil {
			lastErr = err.Error()
			attempts = append(attempts, fmt.Sprintf("  GET %s → 网络错误: %v", u, err))
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			// v3 响应可能包一层 {code, data:{content, ...}};v1 直接是原文
			content := string(body)
			format := guessFormat(req.DataID, content)
			if strings.HasPrefix(content, "{") {
				var doc struct {
					Code int `json:"code"`
					Data struct {
						Content string `json:"content"`
						Type    string `json:"type"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &doc); err == nil && doc.Data.Content != "" {
					content = doc.Data.Content
					if doc.Data.Type != "" {
						format = doc.Data.Type
					}
				}
			}
			return &FetchContentResult{
				Content: content, Format: format,
				Notes: []string{fmt.Sprintf("从 %s 取得(len=%d, format=%s)", shortPath(u), len(content), format)},
			}, nil
		}
		attempts = append(attempts, fmt.Sprintf("  GET %s → %d %s", u, resp.StatusCode, snippet(body)))
		lastErr = fmt.Sprintf("status=%d: %s", resp.StatusCode, snippet(body))
	}
	return nil, fmt.Errorf(
		"nacos 拉配置 dataId=%s group=%s namespace=%s 失败(试过 %d 个路径)。\n最后:%s\n详细:\n%s",
		req.DataID, group, req.Namespace, len(candidates), lastErr, strings.Join(attempts, "\n"))
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

func shortPath(u string) string {
	// 把完整 URL 剪成 "/path" 便于日志;不做太复杂
	if i := strings.Index(u, "/"); i >= 0 {
		if j := strings.Index(u[i+2:], "/"); j >= 0 {
			return u[i+2+j:]
		}
	}
	return u
}

// fetchNacosBatch:probe + login 只做一次,然后对每 item 直接 GET 复用 token。
// 单 item 失败不会让整批 abort —— 失败者写 Errors[key],其他继续。
// transient 错误(timeout / 5xx / TCP reset)每 item inline retry 1 次。
func fetchNacosBatch(req FetchBatchRequest) (*FetchBatchResult, error) {
	// 连接池命中 → 跳过 probe+login;未命中 → 建一次(后续同凭证的调用也复用)
	cli, err := getOrConnectNacos(req.Addr, req.Username, req.Password)
	if err != nil {
		return nil, err
	}
	out := &FetchBatchResult{
		Items: make([]FetchBatchItemResult, 0, len(req.Items)),
		Notes: []string{fmt.Sprintf("nacos batch: 共 %d 条,已复用 connpool 里的 probe+login", len(req.Items))},
	}
	for i, item := range req.Items {
		// 小憩 50ms 间隔,避免对反代层持续打压(第一个不等)
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		res, fetchErr := cli.fetchOneConfigWithRetry(item.Namespace, item.Group, item.DataID, 2)
		if fetchErr != nil {
			out.Items = append(out.Items, FetchBatchItemResult{
				Key: item.Key, OK: false, Error: fetchErr.Error(),
			})
			continue
		}
		out.Items = append(out.Items, FetchBatchItemResult{
			Key: item.Key, OK: true, Result: res,
		})
	}
	return out, nil
}

// fetchOneConfigWithRetry:拉单条,失败若 transient 自动重试 maxRetries 次。
func (cli *nacosClient) fetchOneConfigWithRetry(namespace, group, dataID string, maxRetries int) (*FetchContentResult, error) {
	var lastErr error
	for i := 0; i <= maxRetries; i++ {
		r, err := cli.fetchOneConfig(namespace, group, dataID)
		if err == nil {
			return r, nil
		}
		lastErr = err
		msg := err.Error()
		// transient:timeout / 5xx / reset / EOF
		transient := strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "deadline") ||
			strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "reset") ||
			strings.Contains(msg, "502") ||
			strings.Contains(msg, "503") ||
			strings.Contains(msg, "504")
		if !transient || i >= maxRetries {
			break
		}
		// 指数退避:300ms, 900ms
		time.Sleep(time.Duration(300*(1<<i)) * time.Millisecond)
	}
	return nil, lastErr
}
