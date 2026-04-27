// bindings_cchub.go —— "预加载配置中心"真实 HTTP 调用的桌面端入口。
// Step 5 用户填完 nacos/apollo/consul 凭证后,点"🔍 预加载"触发这个 binding,
// Studio 用 net/http 连目标配置中心拉实际 dataId / namespace / kv 列表,
// 前端展示给用户手工映射到每个服务。
package main

import (
	"github.com/xiaolong/troubleshooter-studio/internal/cchub"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// CCHubPreloadInput 前端传来的预加载请求。
// 跟 cchub.Request 结构同步;独立一层让 Wails 生成的 TS 类型清爽一些。
type CCHubPreloadInput struct {
	Type           string `json:"type"`      // "nacos" / "apollo" / "consul"
	Addr           string `json:"addr"`      // host:port 或 full URL
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Token          string `json:"token,omitempty"`
	Namespace      string `json:"namespace,omitempty"`     // nacos tenant / consul prefix / apollo env
	AppID          string `json:"app_id,omitempty"`        // apollo only
	NamespacesOnly bool   `json:"namespaces_only,omitempty"` // true = 只列 namespaces 不拉 configs
}

// PreloadConfigCenter 连目标配置中心拉配置项清单。失败返完整 error message,
// UI 直接展示给用户(通常是网络不可达 / 账号密码错 / namespace 不存在)。
//
// 所有 addr 过一道 ExpandHome(虽然配置中心地址一般是 host:port 不是本地路径,
// 但用户万一写 ~/... 也不至于直接报 unreachable)。
func (a *App) PreloadConfigCenter(in CCHubPreloadInput) (*cchub.Result, error) {
	return cchub.Preload(cchub.Request{
		Type:           in.Type,
		Addr:           userconfig.ExpandHome(in.Addr),
		Username:       in.Username,
		Password:       in.Password,
		Token:          in.Token,
		Namespace:      in.Namespace,
		AppID:          in.AppID,
		NamespacesOnly: in.NamespacesOnly,
	})
}

// CCHubFetchContentInput 拉单条配置内容的入参,跟 cchub.FetchContentRequest 同步。
type CCHubFetchContentInput struct {
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

// FetchConfigContent 拉某条具体配置的完整原文(yaml / properties / json)。
// 给 Step 7 "数据层自动识别" 用:遍历 Step 5 挑好的 (env, service) → dataId 把内容拉回来,
// 前端 js-yaml 解析识别 redis / mysql / mongodb 等数据层。
func (a *App) FetchConfigContent(in CCHubFetchContentInput) (*cchub.FetchContentResult, error) {
	return cchub.FetchContent(cchub.FetchContentRequest{
		Type:      in.Type,
		Addr:      userconfig.ExpandHome(in.Addr),
		Username:  in.Username,
		Password:  in.Password,
		Token:     in.Token,
		Namespace: in.Namespace,
		Group:     in.Group,
		DataID:    in.DataID,
		AppID:     in.AppID,
	})
}

// CCHubFetchBatchInput 批量拉取入参。共享凭证 + N 条 (namespace, group, data_id) 精确定位。
type CCHubFetchBatchInput struct {
	Type     string                  `json:"type"`
	Addr     string                  `json:"addr"`
	Username string                  `json:"username,omitempty"`
	Password string                  `json:"password,omitempty"`
	Token    string                  `json:"token,omitempty"`
	Items    []cchub.FetchBatchItem  `json:"items"`
}

// FetchConfigContentBatch 批量拉配置内容。对 nacos 会复用一次 probe + login,
// 后续 N 个 get 直接带同一个 token,省 N-1 次登录开销。单条失败不会让整批中止。
func (a *App) FetchConfigContentBatch(in CCHubFetchBatchInput) (*cchub.FetchBatchResult, error) {
	return cchub.FetchContentBatch(cchub.FetchBatchRequest{
		Type:     in.Type,
		Addr:     userconfig.ExpandHome(in.Addr),
		Username: in.Username,
		Password: in.Password,
		Token:    in.Token,
		Items:    in.Items,
	})
}
