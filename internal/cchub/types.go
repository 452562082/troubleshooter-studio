// Package cchub(Config Center Hub)对接各家配置中心的 HTTP API,
// 给 Studio 向导的"预加载"功能用:连上用户填的 Nacos/Apollo/Consul,
// 列出该 namespace 下所有配置条目,让用户按服务维度挑 dataId 配到 config-map.yaml。
//
// 不依赖厚 SDK —— 都用 net/http + 官方开放的 HTTP API;好处是:
//   - 依赖树干净,编译不膨胀
//   - 超时 / 代理 / TLS 配置全可控
//   - 三家 API 差别大,各自一个 client 更直观
//
// 三家 API 参考:
//
//	Nacos:  GET /nacos/v1/cs/configs?tenant=<ns>&pageNo=1&pageSize=500
//	        (需先 POST /nacos/v1/auth/login 换 accessToken;开放模式可跳过)
//	Apollo: GET /openapi/v1/envs/<env>/apps/<appId>/clusters/<cl>/namespaces
//	        (Authorization: <token>;要用户知道 appId,这里做 appId 列表查询)
//	Consul: GET /v1/kv/<prefix>?recurse=true&keys=true
//	        (X-Consul-Token: <token>;token 可选)
package cchub

// Entry 一条来自配置中心的配置记录(三家共用字段,不相关的留空)。
type Entry struct {
	// Locator 核心定位 key:nacos 的 dataId、apollo 的 namespace 名、consul 的 kv key。
	Locator string `json:"locator"`
	// Group Nacos 独有;apollo/consul 留空
	Group string `json:"group,omitempty"`
	// Tenant/Namespace 所属 namespace(nacos) / cluster(apollo) / 根 prefix(consul)
	Tenant string `json:"tenant,omitempty"`
	// Type 内容类型提示(yaml/properties/json/...),供前端挑匹配服务时参考
	Type string `json:"type,omitempty"`
	// AppID apollo 独有;nacos/consul 留空
	AppID string `json:"app_id,omitempty"`
}

// Request 预加载请求参数 —— 用户在 Step 5 填的那一堆。
type Request struct {
	// Type "nacos" / "apollo" / "consul"
	Type string `json:"type"`
	// Addr 服务端可达地址。nacos "host:port" / apollo "http://meta:8080" /
	// consul "http://consul:8500"。nacos 默认加 http:// 前缀。
	Addr string `json:"addr"`
	// Username/Password nacos 用;apollo 走 Token
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Token apollo / consul 用(apollo 是 Open API token,consul 是 ACL token)
	Token string `json:"token,omitempty"`
	// Namespace nacos 的 tenant id / consul 的 kv 根 prefix / apollo env id
	Namespace string `json:"namespace,omitempty"`
	// AppID 可选,apollo 预加载要:只列某个 app 的 namespaces;留空则列全部 app
	AppID string `json:"app_id,omitempty"`
	// NamespacesOnly true = 轻量模式,只登录 + 列 namespaces,不去各 namespace 下拉 configs。
	// 给向导用:每个 env 先发一次 NamespacesOnly 拿 namespace 列表,前端按 env.id 启发式
	// 匹配到对应 namespace,再发第二次精确 namespace 的拉取请求。避免"点 dev 一下子把
	// test/uat/prod 全扫出来"的浪费。
	NamespacesOnly bool `json:"namespaces_only,omitempty"`
}

// Namespace 给 UI 展示 + 选择的 namespace 结构。
type Namespace struct {
	ID       string `json:"id"`        // UUID(public 为空串)
	ShowName string `json:"show_name"` // 友好名,UI 下拉选项用
}

// Result 预加载结果。
type Result struct {
	Type       string      `json:"type"`
	Entries    []Entry     `json:"entries"`
	Namespaces []Namespace `json:"namespaces,omitempty"` // Nacos/Apollo:env 下拉用
	// Notes 人类可读提示(总数 / tenant / 分页截断 / 部分失败 etc)
	Notes []string `json:"notes,omitempty"`
}
