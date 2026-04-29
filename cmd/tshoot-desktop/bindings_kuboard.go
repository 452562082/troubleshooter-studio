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
	"sort"
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

// ── Kuboard ConfigMap 内容批量拉取(给 Step 6 数据层自动识别用)─────────────
// 跟 cchub.FetchContentBatch 平行:N 个 (cluster, namespace, configmap) 一次 RPC,
// 后端 login/tree 只跑一次,N 个 cm get 共享同一个 token,省 N-1 次开销。

type KuboardFetchBatchInput struct {
	URL       string                  `json:"url"`
	AccessKey string                  `json:"access_key,omitempty"`
	Username  string                  `json:"username,omitempty"`
	Password  string                  `json:"password,omitempty"`
	Items     []KuboardFetchBatchItem `json:"items"`
}

type KuboardFetchBatchItem struct {
	Key       string `json:"key"`       // 前端用来回填,如 "dev::user"
	Cluster   string `json:"cluster"`   // cluster 名(UI 选的;后端用 tree 解析成 ID)
	Namespace string `json:"namespace"`
	ConfigMap string `json:"configmap"`
}

type KuboardFetchBatchItemResult struct {
	Key     string `json:"key"`
	OK      bool   `json:"ok"`
	Content string `json:"content,omitempty"` // 所有 data 字段拼成多 doc YAML,前端 yaml.loadAll 解析
	Format  string `json:"format,omitempty"`  // 固定 "yaml-multi" —— 前端识别多 doc
	Error   string `json:"error,omitempty"`
}

type KuboardFetchBatchResult struct {
	Items []KuboardFetchBatchItemResult `json:"items"`
	Notes []string                      `json:"notes,omitempty"`
}

// KuboardFetchConfigMaps 批量拉取多个 ConfigMap 的 data 字段。
// 用途:Step 6 数据层自动识别,挂在 kuboard 源的服务通过这个 binding 把 cm 内容拉回来,
// 前端跟 nacos/apollo/consul 一样的 DS_MATCHERS 流程匹 redis/mysql/...。
func (a *App) KuboardFetchConfigMaps(in KuboardFetchBatchInput) (*KuboardFetchBatchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("kuboard URL 必填")
	}
	if !strings.HasPrefix(base, "http") {
		return nil, fmt.Errorf("kuboard URL 必须以 http:// 或 https:// 开头,得到 %q", in.URL)
	}
	accessKey := strings.TrimSpace(in.AccessKey)
	if accessKey == "" && (in.Username == "" || in.Password == "") {
		return nil, fmt.Errorf("鉴权:填 accessKey 或 用户名+密码")
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()

	// 1) token:accessKey 直接用,否则 /login 拿临时 accessToken
	var token string
	if accessKey != "" {
		token = accessKey
	} else {
		t, err := kuboardLoginV4(ctx, client, base, in.Username, in.Password)
		if err != nil {
			return nil, fmt.Errorf("登录 Kuboard 失败: %w", err)
		}
		token = t
	}

	// 2) cluster name → ID 映射(direct 接口需要 clusterId,但 UI 选的是 name)
	tree, err := kuboardClusterNamespaceTree(ctx, client, base, token)
	if err != nil {
		return nil, fmt.Errorf("列集群失败: %w", err)
	}
	nameToID := map[string]string{}
	for _, c := range tree {
		nameToID[c.Name] = c.ID
	}

	// 3) 逐条拉 cm.data,按 multi-doc YAML 拼内容
	res := &KuboardFetchBatchResult{}
	for _, item := range in.Items {
		out := KuboardFetchBatchItemResult{Key: item.Key}
		clusterUID := nameToID[item.Cluster]
		if clusterUID == "" {
			out.OK = false
			out.Error = fmt.Sprintf("集群 %q 在 Kuboard 里找不到(可能权限不足或缓存未就绪)", item.Cluster)
			res.Items = append(res.Items, out)
			continue
		}
		data, err := kuboardFetchConfigMapDataV4(ctx, client, base, token, clusterUID, item.Namespace, item.ConfigMap)
		if err != nil {
			out.OK = false
			out.Error = err.Error()
			res.Items = append(res.Items, out)
			continue
		}
		if len(data) == 0 {
			out.OK = true
			out.Content = "{}"
			out.Format = "k8s-env-flat"
			res.Items = append(res.Items, out)
			continue
		}
		// K8s ConfigMap 的 data 是 map[string]string,典型 Laravel/Spring .env 用法:
		// 每个 data 字段名(DB_HOST / REDIS_PORT / APP_KEY ...)即 env 变量名,值是字符串。
		// 直接 JSON 编码这份 map,前端按 "k8s-env-flat" 格式解析 + 按前缀重塑成
		// {redis:{...}, mysql:{...}, ...} 喂 DS_MATCHERS。
		blob, err := json.Marshal(data)
		if err != nil {
			out.OK = false
			out.Error = fmt.Sprintf("序列化 cm.data 失败: %v", err)
			res.Items = append(res.Items, out)
			continue
		}
		out.OK = true
		out.Content = string(blob)
		out.Format = "k8s-env-flat"
		res.Items = append(res.Items, out)
	}
	return res, nil
}

// kuboardFetchConfigMapDataV4 取单个 (cluster, ns, name) 的 ConfigMap.data 字段。
// /cluster-cache/direct 同时支持 ?name=<cm>(过滤单条)和无 name(列全部),响应均是 list 形;
// 同时兼容老形态(单 .data.data)以防部分 Kuboard 版本返回不一样。
func kuboardFetchConfigMapDataV4(ctx context.Context, c *http.Client, base, token, clusterUID, namespace, name string) (map[string]string, error) {
	q := fmt.Sprintf("?clusterId=%s&apiVersion=v1&resource=configmaps&namespace=%s&name=%s",
		clusterUID, urlQueryEscape(namespace), urlQueryEscape(name))
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
	// list 形:{data:{list:[{data:{metadata:{name},data:{...}}}]}}
	var asList struct {
		Data struct {
			List []struct {
				Data struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
					Data map[string]string `json:"data"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &asList); err == nil && len(asList.Data.List) > 0 {
		for _, it := range asList.Data.List {
			if it.Data.Metadata.Name == name {
				return it.Data.Data, nil
			}
		}
		// 没精确匹到 name 取首个(direct 在 ?name= 过滤后通常只返回 1 条)
		return asList.Data.List[0].Data.Data, nil
	}
	// 单条形:{data:{data:{metadata,data}}}
	var asSingle struct {
		Data struct {
			Data struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Data map[string]string `json:"data"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &asSingle); err == nil && asSingle.Data.Data.Metadata.Name != "" {
		return asSingle.Data.Data.Data, nil
	}
	return nil, fmt.Errorf("解析 cm 内容失败,原始:%s", snippet(raw))
}

// ── K8s Runtime Query 系列 binding ───────────────────────────────────────
// 让机器人能直接通过 Kuboard 查 pod / service / deployment / events / logs,
// 排障时不必让用户手动开 kubectl。底层都走 /api/cluster.kuboard.cn/v4/cluster-cache/direct
// (resource=pods/services/...),跟 ConfigMap 的查询同套路。
//
// 鉴权 + 集群 ID 解析复用 KuboardFetchConfigMaps 那套(token + nameToID map),
// 但每个 binding 独立暴露给前端,避免一个超大入参 struct 难用。

// kuboardSetup 是所有 K8s 查询 binding 共享的前置:校验 URL/鉴权 + 拿 token +
// 解析 cluster name → ID。失败直接 error 出去,调用方 wrap 消息透给前端。
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

// KuboardPodInfo 单个 pod 的精简快照,UI / agent 可直接消费,不用再解 K8s 完整 pod schema。
type KuboardPodInfo struct {
	Name         string                  `json:"name"`
	Namespace    string                  `json:"namespace"`
	Status       string                  `json:"status"`        // Running / Pending / CrashLoopBackOff / Succeeded / Failed / Unknown
	Phase        string                  `json:"phase"`         // 原始 spec 的 phase
	NodeName     string                  `json:"node_name"`     // 调度到哪个 node
	PodIP        string                  `json:"pod_ip"`
	StartTime    string                  `json:"start_time"`    // RFC3339
	RestartCount int                     `json:"restart_count"` // 主容器累计 restart
	Containers   []KuboardContainerStat  `json:"containers"`
	Reason       string                  `json:"reason,omitempty"`  // OOMKilled / Error / Completed 等
	Message      string                  `json:"message,omitempty"`
}

// KuboardContainerStat 容器级状态(不含日志,日志另调 KuboardGetPodLogs)
type KuboardContainerStat struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restart_count"`
	State        string `json:"state"`         // running / waiting / terminated
	WaitReason   string `json:"wait_reason,omitempty"`   // ImagePullBackOff / CrashLoopBackOff / ContainerCreating
	TermReason   string `json:"term_reason,omitempty"`   // OOMKilled / Error / Completed
	TermExitCode int    `json:"term_exit_code,omitempty"`
}

// KuboardListPodsInput 查 pod 列表的入参。labelSelector 可选(如 "app=order-service");
// 全留空就拉这个 ns 的全部 pod。
type KuboardListPodsInput struct {
	URL            string `json:"url"`
	AccessKey      string `json:"access_key,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Cluster        string `json:"cluster"`
	Namespace      string `json:"namespace"`
	LabelSelector  string `json:"label_selector,omitempty"`
	PodNameFilter  string `json:"pod_name_filter,omitempty"` // 子串匹配,空 = 不过滤
}

func (a *App) KuboardListPods(in KuboardListPodsInput) ([]KuboardPodInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()

	q := fmt.Sprintf("resource=pods&namespace=%s", url.QueryEscape(in.Namespace))
	if in.LabelSelector != "" {
		q += "&labelSelector=" + url.QueryEscape(in.LabelSelector)
	}
	raw, err := kuboardDirectGET(s, q)
	if err != nil {
		return nil, err
	}
	// pods list 形:{data:{list:[{data:{metadata,spec,status}}]}}
	var v struct {
		Data struct {
			List []struct {
				Data k8sPod `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 pod 列表失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardPodInfo, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		p := summarizePod(it.Data)
		if in.PodNameFilter != "" && !strings.Contains(p.Name, in.PodNameFilter) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// k8sPod 对应 K8s Pod 资源的最小子集,只取 summarize 用得到的字段
type k8sPod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase     string `json:"phase"`
		Reason    string `json:"reason"`
		Message   string `json:"message"`
		PodIP     string `json:"podIP"`
		StartTime string `json:"startTime"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Image        string `json:"image"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting    *struct{ Reason, Message string } `json:"waiting,omitempty"`
				Running    *struct{ StartedAt string }       `json:"running,omitempty"`
				Terminated *struct {
					Reason   string `json:"reason"`
					ExitCode int    `json:"exitCode"`
				} `json:"terminated,omitempty"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// summarizePod 把 k8s pod 缩成排障最常看的字段。
// status 字段优先从 containerStatuses[].state.waiting.reason 取(那才是 CrashLoopBackOff
// 这种"机器人最关心"的状态),为空再用 status.phase。
func summarizePod(p k8sPod) KuboardPodInfo {
	out := KuboardPodInfo{
		Name:      p.Metadata.Name,
		Namespace: p.Metadata.Namespace,
		Phase:     p.Status.Phase,
		NodeName:  p.Spec.NodeName,
		PodIP:     p.Status.PodIP,
		StartTime: p.Status.StartTime,
		Reason:    p.Status.Reason,
		Message:   p.Status.Message,
	}
	displayStatus := p.Status.Phase
	totalRestart := 0
	for _, c := range p.Status.ContainerStatuses {
		stat := KuboardContainerStat{
			Name: c.Name, Image: c.Image, Ready: c.Ready, RestartCount: c.RestartCount,
		}
		switch {
		case c.State.Waiting != nil:
			stat.State = "waiting"
			stat.WaitReason = c.State.Waiting.Reason
			// CrashLoopBackOff / ImagePullBackOff 这类比 phase=Pending 更具体,提前到 displayStatus
			if displayStatus == "Pending" || displayStatus == "Running" {
				if c.State.Waiting.Reason != "" {
					displayStatus = c.State.Waiting.Reason
				}
			}
		case c.State.Terminated != nil:
			stat.State = "terminated"
			stat.TermReason = c.State.Terminated.Reason
			stat.TermExitCode = c.State.Terminated.ExitCode
		case c.State.Running != nil:
			stat.State = "running"
		}
		totalRestart += c.RestartCount
		out.Containers = append(out.Containers, stat)
	}
	out.Status = displayStatus
	out.RestartCount = totalRestart
	return out
}

// KuboardGetPodLogsInput 查 pod logs 入参。previous=true 拉上一次容器的日志(CrashLoopBackOff 排障关键)
type KuboardGetPodLogsInput struct {
	URL          string `json:"url"`
	AccessKey    string `json:"access_key,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	Cluster      string `json:"cluster"`
	Namespace    string `json:"namespace"`
	PodName      string `json:"pod_name"`
	Container    string `json:"container,omitempty"` // 多容器 pod 必填,单容器可省
	TailLines    int    `json:"tail_lines,omitempty"` // 默认 200
	Previous     bool   `json:"previous,omitempty"`
}

// KuboardGetPodLogs 拉容器日志。Kuboard v4 暴露 /pod-logs 接口(直接代理到 K8s API)。
func (a *App) KuboardGetPodLogs(in KuboardGetPodLogsInput) (string, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return "", err
	}
	defer s.cancel()

	tail := in.TailLines
	if tail <= 0 {
		tail = 200
	}
	// Kuboard v4 logs:走 cluster-cache/direct 的子路径或专用 /pod-logs;
	// 兼容性:实测 cluster-cache/direct 不返 logs,需 /pod-logs 端点
	q := fmt.Sprintf("clusterId=%s&namespace=%s&podName=%s&tailLines=%d&previous=%v",
		s.clusterUID, url.QueryEscape(in.Namespace), url.QueryEscape(in.PodName), tail, in.Previous)
	if in.Container != "" {
		q += "&container=" + url.QueryEscape(in.Container)
	}
	u := s.base + "/api/cluster.kuboard.cn/v4/pod-logs?" + q
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Kb-Access-Key", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d:%s", resp.StatusCode, snippet(raw))
	}
	// Kuboard 可能用 {data:"<logs>"} 包一层,也可能直返 plain text
	var v struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err == nil && v.Data != "" {
		return v.Data, nil
	}
	return string(raw), nil
}

// ── Events 查询 ──────────────────────────────────────────────────────

// KuboardEvent 事件简化表示
type KuboardEvent struct {
	Type           string `json:"type"`           // Normal / Warning
	Reason         string `json:"reason"`         // FailedScheduling / OOMKilled / BackOff ...
	Message        string `json:"message"`
	InvolvedObject string `json:"involved_object"` // <Kind>/<name>
	Count          int    `json:"count"`
	FirstTimestamp string `json:"first_timestamp"`
	LastTimestamp  string `json:"last_timestamp"`
}

// KuboardListEventsInput 查 events 入参
type KuboardListEventsInput struct {
	URL          string `json:"url"`
	AccessKey    string `json:"access_key,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	Cluster      string `json:"cluster"`
	Namespace    string `json:"namespace"`
	FieldSelector string `json:"field_selector,omitempty"` // 例:"involvedObject.name=order-pod-xxx"
	OnlyWarnings  bool   `json:"only_warnings,omitempty"`
	Limit         int    `json:"limit,omitempty"` // 默认 20
}

func (a *App) KuboardListEvents(in KuboardListEventsInput) ([]KuboardEvent, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	q := fmt.Sprintf("resource=events&namespace=%s", url.QueryEscape(in.Namespace))
	if in.FieldSelector != "" {
		q += "&fieldSelector=" + url.QueryEscape(in.FieldSelector)
	}
	raw, err := kuboardDirectGET(s, q)
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			List []struct {
				Data struct {
					Type    string `json:"type"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
					Count   int    `json:"count"`
					InvolvedObject struct {
						Kind, Name string
					} `json:"involvedObject"`
					FirstTimestamp string `json:"firstTimestamp"`
					LastTimestamp  string `json:"lastTimestamp"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 events 失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardEvent, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		if in.OnlyWarnings && it.Data.Type != "Warning" {
			continue
		}
		out = append(out, KuboardEvent{
			Type: it.Data.Type, Reason: it.Data.Reason, Message: it.Data.Message,
			InvolvedObject: it.Data.InvolvedObject.Kind + "/" + it.Data.InvolvedObject.Name,
			Count: it.Data.Count,
			FirstTimestamp: it.Data.FirstTimestamp, LastTimestamp: it.Data.LastTimestamp,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ── Service / Deployment 查询(精简版) ─────────────────────────────────

// KuboardServiceInfo Service + Endpoints 复合视图(用户最关心"后面挂了几个 pod")
type KuboardServiceInfo struct {
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace"`
	ClusterIP  string   `json:"cluster_ip"`
	Type       string   `json:"type"`
	Ports      []string `json:"ports"`     // "tcp/8080" / "tcp/80→8080"
	Selector   map[string]string `json:"selector,omitempty"`
}

func (a *App) KuboardListServices(in KuboardListPodsInput) ([]KuboardServiceInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()
	raw, err := kuboardDirectGET(s, "resource=services&namespace="+url.QueryEscape(in.Namespace))
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			List []struct {
				Data struct {
					Metadata struct {
						Name, Namespace string
					} `json:"metadata"`
					Spec struct {
						Type      string            `json:"type"`
						ClusterIP string            `json:"clusterIP"`
						Selector  map[string]string `json:"selector"`
						Ports     []struct {
							Port       int    `json:"port"`
							TargetPort any    `json:"targetPort"`
							Protocol   string `json:"protocol"`
						} `json:"ports"`
					} `json:"spec"`
				} `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 services 失败:%v;原始:%s", err, snippet(raw))
	}
	out := make([]KuboardServiceInfo, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		svc := KuboardServiceInfo{
			Name: it.Data.Metadata.Name, Namespace: it.Data.Metadata.Namespace,
			ClusterIP: it.Data.Spec.ClusterIP, Type: it.Data.Spec.Type,
			Selector: it.Data.Spec.Selector,
		}
		for _, p := range it.Data.Spec.Ports {
			tag := strings.ToLower(p.Protocol) + "/" + fmt.Sprintf("%d", p.Port)
			if tp, ok := p.TargetPort.(float64); ok && int(tp) != p.Port {
				tag += fmt.Sprintf("→%d", int(tp))
			}
			svc.Ports = append(svc.Ports, tag)
		}
		out = append(out, svc)
	}
	return out, nil
}

// KuboardDeploymentInfo Deployment 精简视图,排障最关心 "在滚动吗 / 副本到位吗"
type KuboardDeploymentInfo struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Replicas          int      `json:"replicas"`
	UpdatedReplicas   int      `json:"updated_replicas"`
	ReadyReplicas     int      `json:"ready_replicas"`
	AvailableReplicas int      `json:"available_replicas"`
	Strategy          string   `json:"strategy"`             // RollingUpdate / Recreate
	Conditions        []string `json:"conditions,omitempty"` // ["Available=True", "Progressing=True (ReplicaSetUpdated)"]
	// Selector 是 spec.selector.matchLabels 拼成 "k=v,k=v"。向导用它给 service_map 自动
	// 回填 label_selector,运行时排障时 routing skill 也直接读这个值给 KuboardListPods。
	Selector string `json:"selector,omitempty"`
}

func (a *App) KuboardListDeployments(in KuboardListPodsInput) ([]KuboardDeploymentInfo, error) {
	s, err := kuboardSetup(a.ctx, in.URL, in.AccessKey, in.Username, in.Password, in.Cluster)
	if err != nil {
		return nil, err
	}
	defer s.cancel()
	// Kuboard v4 列非 core 资源的正确端点(swagger 文档确认):
	//   GET /api/cluster.kuboard.cn/v4/cluster-cache
	//     ?pageNum=1&pageSize=N
	//     &apiGroup=apps                     # core 资源不传
	//     &resource=deployments
	//     &namespaced=true
	//     &clusterIdNamespaces=<uid>/<ns>    # 同时支持多个 = 跨 ns 查
	//     &orderBy=name
	// 注:不是 cluster-cache/direct(那是按 name 取单条) / 也不是 cluster-cache/list。
	hitURL := fmt.Sprintf("%s/api/cluster.kuboard.cn/v4/cluster-cache"+
		"?pageNum=1&pageSize=500&apiGroup=apps&resource=deployments&namespaced=true"+
		"&clusterIdNamespaces=%s%%2F%s&orderBy=name",
		s.base, url.QueryEscape(s.clusterUID), url.QueryEscape(in.Namespace))
	req, _ := http.NewRequestWithContext(s.ctx, http.MethodGet, hitURL, nil)
	req.Header.Set("Kb-Access-Key", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Kuboard 失败: %v;URL=%s", err, hitURL)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d;URL=%s;响应=%s", resp.StatusCode, hitURL, snippet(raw))
	}
	// 兼容两种 list item 形态:
	//   1) cluster-cache 分页接口:list[i] = { metadata, spec, status, ... }(平铺)
	//   2) cluster-cache/direct:    list[i] = { data: { metadata, spec, status } }(嵌套)
	// 各 binding 用 json.RawMessage 二阶段解 —— 先取 data.list,再尝试 .data 包一层 / 不包一层。
	type k8sDep struct {
		Metadata struct {
			Name, Namespace string
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
			Strategy struct {
				Type string `json:"type"`
			} `json:"strategy"`
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
		} `json:"spec"`
		Status struct {
			UpdatedReplicas   int `json:"updatedReplicas"`
			ReadyReplicas     int `json:"readyReplicas"`
			AvailableReplicas int `json:"availableReplicas"`
			Conditions        []struct {
				Type, Status, Reason string
			} `json:"conditions"`
		} `json:"status"`
	}
	// 兼容 Kuboard v4 多种分页字段命名:list / items / records / content / rows
	var outer struct {
		Data struct {
			List    []json.RawMessage `json:"list"`
			Items   []json.RawMessage `json:"items"`
			Records []json.RawMessage `json:"records"`
			Content []json.RawMessage `json:"content"`
			Rows    []json.RawMessage `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("解析 deployments 失败:%v;URL=%s;原始=%s", err, hitURL, snippet(raw))
	}
	listItems := outer.Data.List
	if len(listItems) == 0 {
		listItems = outer.Data.Items
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Records
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Content
	}
	if len(listItems) == 0 {
		listItems = outer.Data.Rows
	}
	if len(listItems) == 0 {
		// 五种命名都空 —— 把 Kuboard 实际响应的前 600 字节灌进 error,让用户能看到字段名
		return nil, fmt.Errorf("Kuboard 返回里没识别出 list 字段(试过 list/items/records/content/rows);URL=%s;响应前 600 字节=%s",
			hitURL, snippetN(raw, 600))
	}
	out := make([]KuboardDeploymentInfo, 0, len(listItems))
	for _, item := range listItems {
		var dep k8sDep
		// 先按平铺解;若 metadata.name 是空,fallback 到嵌套 .data 形态
		if err := json.Unmarshal(item, &dep); err != nil || dep.Metadata.Name == "" {
			var wrapped struct {
				Data k8sDep `json:"data"`
			}
			if err2 := json.Unmarshal(item, &wrapped); err2 == nil && wrapped.Data.Metadata.Name != "" {
				dep = wrapped.Data
			} else {
				continue
			}
		}
		info := KuboardDeploymentInfo{
			Name: dep.Metadata.Name, Namespace: dep.Metadata.Namespace,
			Replicas: dep.Spec.Replicas, Strategy: dep.Spec.Strategy.Type,
			UpdatedReplicas:   dep.Status.UpdatedReplicas,
			ReadyReplicas:     dep.Status.ReadyReplicas,
			AvailableReplicas: dep.Status.AvailableReplicas,
		}
		for _, c := range dep.Status.Conditions {
			tag := c.Type + "=" + c.Status
			if c.Reason != "" {
				tag += " (" + c.Reason + ")"
			}
			info.Conditions = append(info.Conditions, tag)
		}
		if len(dep.Spec.Selector.MatchLabels) > 0 {
			parts := make([]string, 0, len(dep.Spec.Selector.MatchLabels))
			keys := make([]string, 0, len(dep.Spec.Selector.MatchLabels))
			for k := range dep.Spec.Selector.MatchLabels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k+"="+dep.Spec.Selector.MatchLabels[k])
			}
			info.Selector = strings.Join(parts, ",")
		}
		out = append(out, info)
	}
	return out, nil
}

// ── 一站式快照:KuboardPodSnapshot ─────────────────────────────────────
// 排障最常用的入口 binding:一次拿"pod 列表 + 最近 events + 主 pod 的当前/历史 logs"。
// agent 拿到这份就能直接判断 pod 是否健康 + 给出根因方向。

type KuboardPodSnapshotInput struct {
	URL           string `json:"url"`
	AccessKey     string `json:"access_key,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"label_selector,omitempty"`
	PodNameFilter string `json:"pod_name_filter,omitempty"`
	TailLines     int    `json:"tail_lines,omitempty"` // 默认 200
}

type KuboardPodSnapshotEntry struct {
	Pod          KuboardPodInfo `json:"pod"`
	Events       []KuboardEvent `json:"events,omitempty"`        // 该 pod 相关事件
	LogsCurrent  string         `json:"logs_current,omitempty"`  // 主容器当前日志 tail
	LogsPrevious string         `json:"logs_previous,omitempty"` // 主容器上次日志(restartCount>0 才查)
}

type KuboardPodSnapshotResult struct {
	Pods  []KuboardPodSnapshotEntry `json:"pods"`
	Notes []string                  `json:"notes,omitempty"` // 部分 pod 取日志失败的原因
}

func (a *App) KuboardPodSnapshot(in KuboardPodSnapshotInput) (*KuboardPodSnapshotResult, error) {
	pods, err := a.KuboardListPods(KuboardListPodsInput{
		URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
		Cluster: in.Cluster, Namespace: in.Namespace,
		LabelSelector: in.LabelSelector, PodNameFilter: in.PodNameFilter,
	})
	if err != nil {
		return nil, err
	}
	res := &KuboardPodSnapshotResult{}
	for _, p := range pods {
		entry := KuboardPodSnapshotEntry{Pod: p}
		// events: 用 fieldSelector 精确到这个 pod
		evts, err := a.KuboardListEvents(KuboardListEventsInput{
			URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
			Cluster: in.Cluster, Namespace: in.Namespace,
			FieldSelector: "involvedObject.name=" + p.Name,
			OnlyWarnings:  false,
			Limit:         20,
		})
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取 events 失败: %v", p.Name, err))
		} else {
			entry.Events = evts
		}
		// 当前日志 tail
		mainContainer := ""
		if len(p.Containers) > 0 {
			mainContainer = p.Containers[0].Name
		}
		curLogs, err := a.KuboardGetPodLogs(KuboardGetPodLogsInput{
			URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
			Cluster: in.Cluster, Namespace: in.Namespace,
			PodName: p.Name, Container: mainContainer, TailLines: in.TailLines, Previous: false,
		})
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取当前日志失败: %v", p.Name, err))
		} else {
			entry.LogsCurrent = curLogs
		}
		// 历史日志:仅 restartCount>0 才拉(不然 K8s 会返 400 "previous terminated container not found")
		if p.RestartCount > 0 {
			prevLogs, err := a.KuboardGetPodLogs(KuboardGetPodLogsInput{
				URL: in.URL, AccessKey: in.AccessKey, Username: in.Username, Password: in.Password,
				Cluster: in.Cluster, Namespace: in.Namespace,
				PodName: p.Name, Container: mainContainer, TailLines: in.TailLines, Previous: true,
			})
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("pod %s 取上次日志失败: %v", p.Name, err))
			} else {
				entry.LogsPrevious = prevLogs
			}
		}
		res.Pods = append(res.Pods, entry)
	}
	return res, nil
}
