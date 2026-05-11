// fetch.go —— 拉配置中心某条具体配置的**完整内容**(yaml / properties / json 等原文)。
// 给 wizard "数据层自动识别" 步骤用:Step 5 用户挑了每个 (env, service) 对应的 dataId,
// 这里把那些 dataId 的内容拉回来,前端 js-yaml 解析识别 redis / mysql / mongodb 等数据层配置。
//
// 跟 Preload 不一样:Preload 是"列表",Fetch 是"拿单条内容"。各平台拉取实现已按域拆:
//
//	fetch_nacos.go    Nacos(含 connect/retry/batch,batch 复用同一 token)
//	fetch_apollo.go   Apollo(token header,batch 仅复用 http.Client)
//	fetch_consul.go   Consul(KV ?raw,无状态)
package cchub

import (
	"fmt"
	"strings"
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
	Content string   `json:"content"`
	Format  string   `json:"format,omitempty"` // "yaml" / "json" / "properties" / ""
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
	Type     string           `json:"type"`
	Addr     string           `json:"addr"`
	Username string           `json:"username,omitempty"`
	Password string           `json:"password,omitempty"`
	Token    string           `json:"token,omitempty"`
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

// guessFormat 三家共用的格式探测;按 dataId 后缀优先,fallback 看正文首字符。
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
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return "json"
	}
	return "yaml"
}
