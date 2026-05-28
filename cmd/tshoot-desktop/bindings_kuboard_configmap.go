// bindings_kuboard_configmap.go —— Kuboard ConfigMap 批量拉取 binding。
//
// 跟 cchub.FetchContentBatch 平行:N 个 (cluster, namespace, configmap) 一次 RPC,
// 后端 login/tree 只跑一次,N 个 cm get 共享同一个 token,省 N-1 次开销。
//
// 用途:Step 6 数据层自动识别,挂在 kuboard 源的服务通过这个 binding 把 cm 内容拉回来,
// 前端跟 nacos/apollo/consul 一样的 DS_MATCHERS 流程匹 redis/mysql/...。
//
// 鉴权 / login / cluster name→ID 映射的实现复用 bindings_kuboard.go 里的 helper。
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type KuboardFetchBatchInput struct {
	URL       string                  `json:"url"`
	AccessKey string                  `json:"access_key,omitempty"`
	Username  string                  `json:"username,omitempty"`
	Password  string                  `json:"password,omitempty"`
	Items     []KuboardFetchBatchItem `json:"items"`
}

type KuboardFetchBatchItem struct {
	Key       string `json:"key"`     // 前端用来回填,如 "dev::user"
	Cluster   string `json:"cluster"` // cluster 名(UI 选的;后端用 tree 解析成 ID)
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

	// v3 探测:access-key 试 v4 tree 404 → v3,走 k8s-api 读 cm。
	if accessKey != "" && kuboardDetectVersion(ctx, client, base, accessKey) == "v3" {
		return kuboardFetchConfigMapsV3(ctx, client, base, in.Username, accessKey, in.Items)
	}

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

	// 3) 逐条拉 cm.data,按 K8s ConfigMap 平铺 .env 风格序列化
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
