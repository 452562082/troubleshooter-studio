// bindings_kuboard.go —— Kuboard v4 资源探测 binding。
//
// 用途:Step 5 配置源选 kuboard 时,用户填了 URL+账密之后点"📥 拉取资源",
// 走 Kuboard v4 HTTP API 把 集群 / namespace / configmap 三级目录拉回来,
// UI 渲染成级联下拉(免去手填集群名 / namespace / cm 名)。
//
// API 路径(对照 https://kb.guadd.fun/swagger-ui/index.html):
//   1. POST /api/login.kuboard.cn/v4/login         body {username,password}  → {data:{accessToken}}
//   2. GET  /api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree?apiGroupName=&resource=configmaps&namespaced=true
//          → {data:{treeItems:[{id,name,children:[{name (ns)}]}]}}     一次拿全部 cluster→ns 树
//   3. GET  /api/cluster.kuboard.cn/v4/cluster-cache?apiGroup=&resource=configmaps&namespaced=true&clusterId=<uid>&pageSize=5000
//          → {data:{list:[{data:{metadata:{namespace,name}}}]}}        per cluster 拉全部 cm,客户端按 ns 分组
//
// 鉴权 header 是 Kb-Access-Key(不是标准 Authorization Bearer),value 可以是:
//   - 登录返回的 accessToken
//   - 用户在 Kuboard 后台 个人中心 → API 访问凭证 创建的 user-key-secret(免账密)
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// KuboardCluster:一个集群下的命名空间树
type KuboardCluster struct {
	Name       string             `json:"name"`
	Namespaces []KuboardNamespace `json:"namespaces"`
}

// KuboardNamespace:一个 namespace 下的 ConfigMap 列表
type KuboardNamespace struct {
	Name       string   `json:"name"`
	ConfigMaps []string `json:"configmaps"`
}

// KuboardResources:Kuboard 一份完整的"集群→ns→cm"资源树
type KuboardResources struct {
	Clusters []KuboardCluster `json:"clusters"`
	Notes    []string         `json:"notes,omitempty"`
}

// KuboardListResources 登 Kuboard v4 + 列资源树。
//   - kuboardURL:Kuboard 后台基地址,如 https://kuboard.example.com(末尾斜杠会去掉)
//   - username / password / accessKey:鉴权三选一(优先级 accessKey > 账密)
//     accessKey:Kuboard 后台"个人中心 → API 访问凭证"创建的 user-key-secret,免账密直连
//     username+password:走 /login 拿临时 accessToken
//   - loginPath:保留参数(v4 路径已固定),已忽略
func (a *App) KuboardListResources(kuboardURL, username, password, accessKey, loginPath string) (*KuboardResources, error) {
	_ = loginPath
	base := strings.TrimRight(strings.TrimSpace(kuboardURL), "/")
	if base == "" {
		return nil, fmt.Errorf("kuboard URL 必填")
	}
	if !strings.HasPrefix(base, "http") {
		return nil, fmt.Errorf("kuboard URL 必须以 http:// 或 https:// 开头,得到 %q", kuboardURL)
	}
	accessKey = strings.TrimSpace(accessKey)
	if accessKey == "" && (username == "" || password == "") {
		return nil, fmt.Errorf("鉴权:填 accessKey,或 用户名+密码(二选一)")
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()

	// 1) 拿到鉴权 token:优先 accessKey 直接用,否则 /login 换一个
	var token string
	if accessKey != "" {
		token = accessKey
	} else {
		t, err := kuboardLoginV4(ctx, client, base, username, password)
		if err != nil {
			return nil, fmt.Errorf("登录 Kuboard 失败: %w", err)
		}
		token = t
	}

	// 2) cluster-namespace-tree 一次拿 cluster→ns 树
	tree, err := kuboardClusterNamespaceTree(ctx, client, base, token)
	if err != nil {
		return nil, fmt.Errorf("列集群+namespace 失败: %w", err)
	}
	if len(tree) == 0 {
		return &KuboardResources{Notes: []string{"账号在 Kuboard 里没拿到 cluster-namespace-tree(可能权限不足或缓存还没就绪)"}}, nil
	}

	// 3) per-(cluster, ns):逐个 ns 调 direct 拉 cm 列表(direct 不带 namespace 返空,
	//    必须 per-ns 调用)。多个 HTTP 请求并行也可,先简单串行,够用了。
	res := &KuboardResources{}
	for _, item := range tree {
		entry := KuboardCluster{Name: item.Name}
		// 过滤系统 ns,收集真正要拉 cm 的 ns 列表
		var nsToFetch []string
		for _, child := range item.Children {
			if strings.HasPrefix(child.Name, "kube-") || child.Name == "kube-system" || strings.HasPrefix(child.Name, "kuboard-") {
				continue
			}
			nsToFetch = append(nsToFetch, child.Name)
		}
		// 逐个 ns 拉 cm
		for _, ns := range nsToFetch {
			cms, err := kuboardListConfigMapsV4(ctx, client, base, token, item.ID, ns)
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("集群 %s ns %s 列 cm 失败: %v", item.Name, ns, err))
				entry.Namespaces = append(entry.Namespaces, KuboardNamespace{Name: ns})
				continue
			}
			entry.Namespaces = append(entry.Namespaces, KuboardNamespace{Name: ns, ConfigMaps: cms})
		}
		res.Clusters = append(res.Clusters, entry)
	}
	return res, nil
}

// kuboardClusterNamespaceTree 调 cluster-namespace-tree 拿 cluster→ns 列表(单次 HTTP)。
//
// 防御:Kuboard treeItems 的 children 里每个 namespace 都带 clusterId 冗余字段
// (见 ClusterTreeItemNamespace schema),为防 children 不按 cluster 严格隔离,
// 显式按 child.clusterId == parent.id 过滤;clusterId 缺失时按位置归属 fallback。
type kbV4TreeItem struct {
	ID       string
	Name     string
	Children []struct{ Name string }
}

func kuboardClusterNamespaceTree(ctx context.Context, c *http.Client, base, token string) ([]kbV4TreeItem, error) {
	u := base + "/api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree?apiGroupName=&resource=configmaps&namespaced=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Kb-Access-Key", token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	// schema(ClusterTree.treeItems[].children[])。children 含 clusterId 冗余,显式过滤。
	var v struct {
		Data struct {
			TreeItems []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Children []struct {
					Name      string `json:"name"`
					ClusterID string `json:"clusterId"`
				} `json:"children"`
			} `json:"treeItems"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 cluster-namespace-tree 失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]kbV4TreeItem, 0, len(v.Data.TreeItems))
	for _, it := range v.Data.TreeItems {
		item := kbV4TreeItem{ID: it.ID, Name: it.Name}
		seen := map[string]bool{} // 同 cluster 内 ns 名去重(API 偶尔重复)
		for _, ch := range it.Children {
			// 严格按 clusterId 隔离:child.clusterId 不等于本 cluster.id 就跳
			if ch.ClusterID != "" && ch.ClusterID != it.ID {
				continue
			}
			if ch.Name == "" || seen[ch.Name] {
				continue
			}
			seen[ch.Name] = true
			item.Children = append(item.Children, struct{ Name string }{Name: ch.Name})
		}
		out = append(out, item)
	}
	return out, nil
}

// ── Kuboard v4 API client ─────────────────────────────────────────────

func kuboardLoginV4(ctx context.Context, c *http.Client, base, user, pass string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/login.kuboard.cn/v4/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("账号或密码错(HTTP 401)")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	// schema: {message,exception,code,data:{accessToken},timestamp}
	var v struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("解析登录响应失败:%v;原始:%s", err, snippet(raw))
	}
	if v.Code != 0 && v.Code != 200 && v.Data.AccessToken == "" {
		return "", fmt.Errorf("登录被拒(code=%d):%s", v.Code, v.Message)
	}
	if v.Data.AccessToken == "" {
		return "", fmt.Errorf("登录响应里没有 accessToken;原始:%s", snippet(raw))
	}
	return v.Data.AccessToken, nil
}

// kuboardListConfigMapsV4 拉指定 (cluster, namespace) 下的 ConfigMap 名字列表。
// 走 /cluster-cache/direct(K8s API 直查),不依赖 Kuboard cache 状态。
// 参数:apiVersion=v1(core API)、resource=configmaps、namespace=<ns>(必带,direct
// 没 namespace 过滤时常返空)。
func kuboardListConfigMapsV4(ctx context.Context, c *http.Client, base, token, clusterUID, namespace string) ([]string, error) {
	q := fmt.Sprintf("?clusterId=%s&apiVersion=v1&resource=configmaps&namespace=%s",
		clusterUID, urlQueryEscape(namespace))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/cluster.kuboard.cn/v4/cluster-cache/direct"+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Kb-Access-Key", token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	var v struct {
		Data struct {
			List []struct {
				Data struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 cm 列表失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]string, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		if name := it.Data.Metadata.Name; name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// urlQueryEscape:简单的查询参数 escape(避免 namespace 含特殊字符破 URL)
func urlQueryEscape(s string) string {
	// net/url 已 import,这里用 url.QueryEscape
	return url.QueryEscape(s)
}

// snippet 截一段响应给错误提示
func snippet(b []byte) string {
	return snippetN(b, 200)
}

// snippetN 截前 n 字节(调试用,不固定长度)
func snippetN(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ── K8s 查询通用 setup ───────────────────────────────────────────────
// 所有 K8s 查询 binding(pod / events / service / deployment / logs)共享的前置:
// 校验 URL/鉴权 + 拿 token + 解析 cluster name → ID。
// 失败直接 error 出去,调用方 wrap 消息透给前端。
// 各专项 binding 在 bindings_kuboard_pod.go / bindings_kuboard_workload.go。

type kuboardSetupResult struct {
	ctx        context.Context
	cancel     context.CancelFunc
	client     *http.Client
	base       string
	token      string
	clusterUID string
}

func kuboardSetup(ctx context.Context, kbURL, accessKey, username, password, clusterName string) (*kuboardSetupResult, error) {
	base := strings.TrimRight(strings.TrimSpace(kbURL), "/")
	if base == "" {
		return nil, fmt.Errorf("kuboard URL 必填")
	}
	if !strings.HasPrefix(base, "http") {
		return nil, fmt.Errorf("kuboard URL 必须以 http:// 或 https:// 开头")
	}
	accessKey = strings.TrimSpace(accessKey)
	if accessKey == "" && (username == "" || password == "") {
		return nil, fmt.Errorf("鉴权:填 accessKey 或 用户名+密码")
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	var token string
	if accessKey != "" {
		token = accessKey
	} else {
		t, err := kuboardLoginV4(rctx, client, base, username, password)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("登录 Kuboard 失败: %w", err)
		}
		token = t
	}
	tree, err := kuboardClusterNamespaceTree(rctx, client, base, token)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("列集群失败: %w", err)
	}
	var clusterUID string
	for _, c := range tree {
		if c.Name == clusterName {
			clusterUID = c.ID
			break
		}
	}
	if clusterUID == "" {
		cancel()
		return nil, fmt.Errorf("集群 %q 在 Kuboard 里找不到(可能权限不足或缓存未就绪)", clusterName)
	}
	return &kuboardSetupResult{
		ctx: rctx, cancel: cancel, client: client,
		base: base, token: token, clusterUID: clusterUID,
	}, nil
}

// kuboardDirectGET 通用 direct 接口 GET 请求。
// path 例:"resource=pods&namespace=default" / "resource=pods&namespace=default&name=xxx"
func kuboardDirectGET(s *kuboardSetupResult, query string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/cluster.kuboard.cn/v4/cluster-cache/direct?clusterId=%s&apiVersion=v1&%s",
		s.base, s.clusterUID, query)
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Kb-Access-Key", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	return raw, nil
}

// ── Pod 查询 ─────────────────────────────────────────────────────────

