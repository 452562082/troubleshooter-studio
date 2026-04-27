// fetch.go —— 拉配置中心某条具体配置的**完整内容**(yaml / properties / json 等原文)。
// 给 wizard "数据层自动识别" 步骤用:Step 5 用户挑了每个 (env, service) 对应的 dataId,
// 这里把那些 dataId 的内容拉回来,前端 js-yaml 解析识别 redis / mysql / mongodb 等数据层配置。
//
// 跟 Preload 不一样:Preload 是"列表",Fetch 是"拿单条内容"。单独分文件免得污染 nacos.go。
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

// FetchContentRequest 拉单条配置的参数。
// (type, addr, username, password, token) 跟 Preload 共用凭证语义。
// (namespace, group, data_id, app_id) 是目标配置的定位 4 元组(各家含义略异):
//
//	nacos:  namespace(UUID 或空=public) + group + data_id
//	apollo: namespace=env(DEV/UAT), app_id, group=cluster, data_id=namespaceName
//	consul: namespace=kv prefix, data_id=完整 kv key
type FetchContentRequest struct {
	Type      string `json:"type"`
	Addr      string `json:"addr"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	Token     string `json:"token,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Group     string `json:"group,omitempty"`
	DataID    string `json:"data_id"`
	AppID     string `json:"app_id,omitempty"`
}

// FetchContentResult 配置原文 + format 提示(前端据此选 yaml/json/properties 解析器)。
type FetchContentResult struct {
	Content string `json:"content"`
	Format  string `json:"format,omitempty"` // "yaml" / "json" / "properties" / ""
	Notes   []string `json:"notes,omitempty"`
}

// ── Batch 版本 ─────────────────────────────────────────────────────────
// 给"数据层自动识别"用:前端一次 RPC 传 N 个 (namespace, group, data_id),
// 后端 probe/login 各一次,然后对每 item 复用同一个 token 直接拉,省 N-1 次 probe + login。
//
// 对 10 个服务的场景:老做法 10 * (4 probe + 1 login + 1 get) = 60 次 HTTP,
// 新做法 4 probe + 1 login + 10 get = 15 次,快 4 倍 + 对反代层友好。

type FetchBatchItem struct {
	Key       string `json:"key"` // 前端用来映射结果,如 "dev::user"
	Namespace string `json:"namespace,omitempty"`
	Group     string `json:"group,omitempty"`
	DataID    string `json:"data_id"`
	AppID     string `json:"app_id,omitempty"`
}

type FetchBatchRequest struct {
	Type     string `json:"type"`
	Addr     string `json:"addr"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
	Items    []FetchBatchItem `json:"items"`
}

type FetchBatchItemResult struct {
	Key    string              `json:"key"`
	OK     bool                `json:"ok"`
	Result *FetchContentResult `json:"result,omitempty"`
	Error  string              `json:"error,omitempty"`
}

type FetchBatchResult struct {
	Items []FetchBatchItemResult `json:"items"`
	Notes []string               `json:"notes,omitempty"`
}

// FetchContentBatch 按 type 分派;nacos 版本复用同一个 client。
func FetchContentBatch(req FetchBatchRequest) (*FetchBatchResult, error) {
	switch req.Type {
	case "nacos":
		return fetchNacosBatch(req)
	case "apollo":
		return fetchApolloBatch(req)
	case "consul":
		return fetchConsulBatch(req)
	}
	return nil, fmt.Errorf("unsupported type: %q", req.Type)
}

// FetchContent 按 type 分派。nacos 必要时自动 login(复用 Preload 的探测逻辑)。
func FetchContent(req FetchContentRequest) (*FetchContentResult, error) {
	switch req.Type {
	case "nacos":
		return fetchNacos(req)
	case "apollo":
		return fetchApollo(req)
	case "consul":
		return fetchConsul(req)
	}
	return nil, fmt.Errorf("unsupported type: %q", req.Type)
}

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

	// GET /v1/kv/<key>?raw 直接返原文
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

func guessFormat(dataID, content string) string {
	lower := strings.ToLower(dataID)
	switch {
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "yaml"
	case strings.HasSuffix(lower, ".json"):
		return "json"
	case strings.HasSuffix(lower, ".properties"):
		return "properties"
	}
	// 看开头几个非空字符猜
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return "json"
	}
	return "yaml"
}

// ── Batch 实现 ────────────────────────────────────────────────────────

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

// fetchApolloBatch:HTTP 无状态(仅 token header),但仍然复用一个 http.Client 和头,省 TLS handshake。
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

// fetchConsulBatch:同样无状态,单纯串行。
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
