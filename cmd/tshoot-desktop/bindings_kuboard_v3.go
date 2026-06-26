// bindings_kuboard_v3.go —— Kuboard v3 适配。
//
// Kuboard v3 跟 v4 是两套完全不同的 API:
//   - 鉴权:v3 用 Cookie `KuboardUsername=<user>; KuboardAccessKey=<密钥ID>.<密钥>`;
//     v4 用 header `Kb-Access-Key: <token>`。
//   - 资源访问:v3 把自己当标准 k8s apiserver 代理,前缀 `/k8s-api/{cluster}/` + 原生
//     k8s REST API + 原生 k8s JSON(`{items:[...]}` / 单对象);v4 走私有
//     `/api/cluster.kuboard.cn/v4/cluster-cache/...`,每个对象包一层 `{data:...}`。
//   - 集群:v3 用集群**名**直接进 path;v4 要先 cluster-namespace-tree 把名解析成 UID。
//
// 关键约束(实测 kuboard.guadd.fun v3.5.2.9):**access-key 不能枚举所有集群**
// (全局 `/kuboard-api/kind/KubernetesCluster` 返 500,只认登录 session)。所以 v3 走
// "用户填一次集群名 + access-key 校验它存在",再用 k8s-api 列 ns/cm/pod。详见
// memory project-kuboard-v3-vs-v4 / docs。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// kuboardDetectVersion 正向识别 Kuboard 大版本:只有 v4 的 cluster-namespace-tree 返 200 且
// body 是 v4 形态({data:{treeItems}})才判 v4;其余(404/403/5xx/Cloudflare 拦截/传输错误/
// 非 JSON)一律 v3。guadd 这类 v3 现场该路径恒 404 且套 Cloudflare,旧的"非 404 即 v4"会
// 因首连传输抖动误判 v4。详见 memory project-kuboard-v3-vs-v4。
func kuboardDetectVersion(ctx context.Context, c *http.Client, base, accessKey string) string {
	u := base + "/api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree?apiGroupName=&resource=configmaps&namespaced=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "v3"
	}
	req.Header.Set("User-Agent", kuboardUserAgent)
	if accessKey != "" {
		req.Header.Set("Kb-Access-Key", accessKey)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "v3"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "v3"
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var v struct {
		Data struct {
			TreeItems json.RawMessage `json:"treeItems"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil || len(v.Data.TreeItems) == 0 {
		return "v3"
	}
	return "v4"
}

// kuboardV3Cookie 拼 v3 鉴权 Cookie。accessKey 形态为 "<密钥ID>.<密钥>"。
func kuboardV3Cookie(username, accessKey string) string {
	return fmt.Sprintf("KuboardUsername=%s; KuboardAccessKey=%s", username, accessKey)
}

// kuboardV3GET 用 v3 Cookie 鉴权发 GET,返回 body + status。
func kuboardV3GET(ctx context.Context, c *http.Client, u, cookie string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kuboardUserAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

// kuboardV3ClusterExists 用 access-key 校验某集群存在(per-cluster kind 接口认 access-key,
// 全局列集群接口不认 → 只能逐个校验,详见文件头注释)。
func kuboardV3ClusterExists(ctx context.Context, c *http.Client, base, cookie, cluster string) (bool, error) {
	u := fmt.Sprintf("%s/kuboard-api/cluster/%s/kind/KubernetesCluster", base, url.PathEscape(cluster))
	body, code, err := kuboardV3GET(ctx, c, u, cookie)
	if err != nil {
		return false, err
	}
	if code >= 400 {
		return false, fmt.Errorf("HTTP %d:%s", code, snippet(body))
	}
	var v struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false, fmt.Errorf("解析 KubernetesCluster 失败:%v;原始:%s", err, snippet(body))
	}
	for _, it := range v.Items {
		if it.Metadata.Name == cluster {
			return true, nil
		}
	}
	return false, nil
}

// k8sListNames 从标准 k8s List 响应(`{items:[{metadata:{name}}]}`)抽 metadata.name 列表。
func k8sListNames(body []byte) ([]string, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Items))
	for _, it := range v.Items {
		if it.Metadata.Name != "" {
			out = append(out, it.Metadata.Name)
		}
	}
	return out, nil
}

// isSystemNamespace 过滤掉系统 / Kuboard 自带 ns(列配置源时不展示)。
func isSystemNamespace(ns string) bool {
	return strings.HasPrefix(ns, "kube-") || ns == "kube-system" || strings.HasPrefix(ns, "kuboard-")
}

// kuboardListResourcesV3 v3 列资源:校验集群存在 → 列 ns → per-ns 列 cm。
// 跟 v4 的 KuboardListResources 返回同一个 *KuboardResources,前端 cascade UI 通吃。
func kuboardListResourcesV3(ctx context.Context, c *http.Client, base, username, accessKey, cluster string) (*KuboardResources, error) {
	if username == "" {
		return nil, fmt.Errorf("kuboard v3 鉴权需要用户名(Cookie KuboardUsername);accessKey 形态应为 <密钥ID>.<密钥>")
	}
	if cluster == "" {
		return &KuboardResources{Notes: []string{
			"Kuboard v3 无法用 access-key 枚举集群,请填集群名(如 jw-was-k8s-test)后重试",
		}}, nil
	}
	cookie := kuboardV3Cookie(username, accessKey)
	ok, err := kuboardV3ClusterExists(ctx, c, base, cookie, cluster)
	if err != nil {
		return nil, fmt.Errorf("校验集群失败: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("集群 %q 在 Kuboard 里找不到(检查集群名 / access-key 权限)", cluster)
	}
	nss, err := kuboardV3ListNamespaces(ctx, c, base, cookie, cluster)
	if err != nil {
		return nil, fmt.Errorf("列 namespace 失败: %w", err)
	}
	res := &KuboardResources{}
	entry := KuboardCluster{Name: cluster}
	for _, ns := range nss {
		if isSystemNamespace(ns) {
			continue
		}
		cms, err := kuboardV3ListConfigMapNames(ctx, c, base, cookie, cluster, ns)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("ns %s 列 cm 失败: %v", ns, err))
			entry.Namespaces = append(entry.Namespaces, KuboardNamespace{Name: ns})
			continue
		}
		entry.Namespaces = append(entry.Namespaces, KuboardNamespace{Name: ns, ConfigMaps: cms})
	}
	res.Clusters = append(res.Clusters, entry)
	return res, nil
}

// kuboardV3ListNamespaces 列集群下 namespace(过滤 Kuboard 隐藏 ns)。
func kuboardV3ListNamespaces(ctx context.Context, c *http.Client, base, cookie, cluster string) ([]string, error) {
	u := fmt.Sprintf("%s/k8s-api/%s/api/v1/namespaces?labelSelector=%s",
		base, url.PathEscape(cluster), url.QueryEscape("!k8s.kuboard.cn/hide"))
	body, code, err := kuboardV3GET(ctx, c, u, cookie)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", code, snippet(body))
	}
	names, err := k8sListNames(body)
	if err != nil {
		return nil, fmt.Errorf("解析 namespace 列表失败:%v;原始:%s", err, snippet(body))
	}
	return names, nil
}

// kuboardV3ListConfigMapNames 列 (cluster, ns) 下 ConfigMap 名字。
func kuboardV3ListConfigMapNames(ctx context.Context, c *http.Client, base, cookie, cluster, namespace string) ([]string, error) {
	u := fmt.Sprintf("%s/k8s-api/%s/api/v1/namespaces/%s/configmaps",
		base, url.PathEscape(cluster), url.PathEscape(namespace))
	body, code, err := kuboardV3GET(ctx, c, u, cookie)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", code, snippet(body))
	}
	names, err := k8sListNames(body)
	if err != nil {
		return nil, fmt.Errorf("解析 configmap 列表失败:%v;原始:%s", err, snippet(body))
	}
	return names, nil
}

// kuboardFetchConfigMapsV3 批量读 cm.data(v3)。逐条走 k8s-api,结果格式跟 v4 一致
// (k8s-env-flat:cm.data 这个 map[string]string 直接 JSON 编码,前端按前缀重塑)。
func kuboardFetchConfigMapsV3(ctx context.Context, c *http.Client, base, username, accessKey string, items []KuboardFetchBatchItem) (*KuboardFetchBatchResult, error) {
	if username == "" {
		return nil, fmt.Errorf("kuboard v3 鉴权需要用户名(Cookie KuboardUsername);accessKey 形态应为 <密钥ID>.<密钥>")
	}
	cookie := kuboardV3Cookie(username, accessKey)
	res := &KuboardFetchBatchResult{}
	for _, item := range items {
		out := KuboardFetchBatchItemResult{Key: item.Key}
		data, err := kuboardV3ConfigMapData(ctx, c, base, cookie, item.Cluster, item.Namespace, item.ConfigMap)
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

// kuboardV3ConfigMapData 读单个 ConfigMap 的 .data(标准 k8s ConfigMap)。
func kuboardV3ConfigMapData(ctx context.Context, c *http.Client, base, cookie, cluster, namespace, name string) (map[string]string, error) {
	u := fmt.Sprintf("%s/k8s-api/%s/api/v1/namespaces/%s/configmaps/%s",
		base, url.PathEscape(cluster), url.PathEscape(namespace), url.PathEscape(name))
	body, code, err := kuboardV3GET(ctx, c, u, cookie)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, fmt.Errorf("configmap %s/%s 未找到", namespace, name)
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d:%s", code, snippet(body))
	}
	var v struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("解析 configmap 失败:%v;原始:%s", err, snippet(body))
	}
	return v.Data, nil
}

// ── 给 kuboardSetupResult 的版本无关访问器 ───────────────────────────────
// 把 v3(标准 k8s `{items:[...]}`)和 v4(私有 `{data:{list:[{data:...}]}}`)的差异
// 收敛到这里:调用方拿到的是一组「原始 k8s 对象 JSON」,各自 unmarshal 成需要的结构。

// listK8sObjects 列某命名空间资源,返回规范化的一组原始 k8s 对象。
// rawQuery 是已转义的查询串(如 "labelSelector=app%3Dorder"),v3/v4 都用 k8s 同名参数。
func (s *kuboardSetupResult) listK8sObjects(resource, namespace, rawQuery string) ([]json.RawMessage, error) {
	if s.version == "v3" {
		u := fmt.Sprintf("%s/k8s-api/%s/api/v1/namespaces/%s/%s",
			s.base, url.PathEscape(s.clusterName), url.PathEscape(namespace), resource)
		if rawQuery != "" {
			u += "?" + rawQuery
		}
		body, code, err := kuboardV3GET(s.ctx, s.client, u, s.cookie)
		if err != nil {
			return nil, err
		}
		if code >= 400 {
			return nil, fmt.Errorf("HTTP %d:%s", code, snippet(body))
		}
		var v struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("解析 %s 失败:%v;原始:%s", resource, err, snippet(body))
		}
		return v.Items, nil
	}
	// v4
	q := fmt.Sprintf("resource=%s&namespace=%s", resource, url.QueryEscape(namespace))
	if rawQuery != "" {
		q += "&" + rawQuery
	}
	raw, err := kuboardDirectGET(s, q)
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			List []struct {
				Data json.RawMessage `json:"data"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 %s 失败:%v;原始:%s", resource, err, snippet(raw))
	}
	out := make([]json.RawMessage, 0, len(v.Data.List))
	for _, it := range v.Data.List {
		out = append(out, it.Data)
	}
	return out, nil
}

// listK8sObjectsGroup 列某 apiGroup 下的命名空间资源(如 apps/v1 的 deployments)。
// 跟 listK8sObjects 一样把 v3/v4 差异收敛掉,但支持非 core 的 apiGroup:
//   - apiPath  形如 "apis/apps/v1",v3 直接拼进 /k8s-api/{cluster}/{apiPath}/...;
//   - apiGroup 形如 "apps",v4 cluster-cache 用它(core 资源传空串)。
//
// rawQuery 是已转义的查询串(如 "labelSelector=app%3Dorder")。返回一组标准 k8s 对象 JSON,
// 调用方各自 unmarshal。
func (s *kuboardSetupResult) listK8sObjectsGroup(apiPath, apiGroup, resource, namespace, rawQuery string) ([]json.RawMessage, error) {
	if s.version == "v3" {
		// v3:标准 k8s apiserver 代理,GET {base}/k8s-api/{cluster}/{apiPath}/namespaces/{ns}/{resource}
		u := fmt.Sprintf("%s/k8s-api/%s/%s/namespaces/%s/%s",
			s.base, url.PathEscape(s.clusterName), apiPath, url.PathEscape(namespace), resource)
		if rawQuery != "" {
			u += "?" + rawQuery
		}
		body, code, err := kuboardV3GET(s.ctx, s.client, u, s.cookie)
		if err != nil {
			return nil, fmt.Errorf("请求 Kuboard 失败: %v;URL=%s", err, u)
		}
		if code >= 400 {
			return nil, fmt.Errorf("HTTP %d;URL=%s;响应=%s", code, u, snippet(body))
		}
		var v struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("解析 %s 失败:%v;URL=%s;原始=%s", resource, err, u, snippet(body))
		}
		return v.Items, nil
	}
	// v4:cluster-cache 分页接口,clusterIdNamespaces={uid}/{ns} + apiGroup 参数。
	u := fmt.Sprintf("%s/api/cluster.kuboard.cn/v4/cluster-cache"+
		"?pageNum=1&pageSize=500&apiGroup=%s&resource=%s&namespaced=true"+
		"&clusterIdNamespaces=%s%%2F%s&orderBy=name",
		s.base, url.QueryEscape(apiGroup), url.QueryEscape(resource),
		url.QueryEscape(s.clusterUID), url.QueryEscape(namespace))
	if rawQuery != "" {
		u += "&" + rawQuery
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Kb-Access-Key", s.token)
	req.Header.Set("User-Agent", kuboardUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Kuboard 失败: %v;URL=%s", err, u)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d;URL=%s;响应=%s", resp.StatusCode, u, snippet(raw))
	}
	// cluster-cache 分页 list[i] 可能平铺,也可能包一层 {data:...};两种都取出来。
	var v struct {
		Data struct {
			List []json.RawMessage `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 %s 失败:%v;URL=%s;原始=%s", resource, err, u, snippet(raw))
	}
	out := make([]json.RawMessage, 0, len(v.Data.List))
	for _, item := range v.Data.List {
		var wrapped struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(item, &wrapped); err == nil && len(wrapped.Data) > 0 {
			out = append(out, wrapped.Data) // 嵌套 {data:<obj>}
		} else {
			out = append(out, item) // 平铺 <obj>
		}
	}
	return out, nil
}
