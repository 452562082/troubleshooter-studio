// bindings_dsprobe.go —— Step 7 数据层"测试连通性"按钮的桌面端入口。
package main

import "github.com/xiaolong/troubleshooter-studio/internal/dsprobe"

// DSProbeInput 跟 dsprobe.Request 同步;独立一层让 wails 生成的 TS 干净。
type DSProbeInput struct {
	Type   string            `json:"type"`
	Fields map[string]string `json:"fields"`
}

// ProbeDataStore 给定数据层类型 + 字段(url / dsn / host / brokers...),做轻量连通测试。
// 不读不写实际数据,5 秒超时,失败返回人话错误给 UI 展示。
func (a *App) ProbeDataStore(in DSProbeInput) dsprobe.Result {
	return dsprobe.Probe(dsprobe.Request{
		Type:   in.Type,
		Fields: in.Fields,
	})
}

// ProbeURL 给 Step 3 环境列表的 api_domain / web_domain 自动连通性检测用。
// 简单 GET 一下 URL,< 500 都算可达(401/403/404 这类业务错也算通)。
func (a *App) ProbeURL(rawURL string) dsprobe.Result {
	return dsprobe.ProbeHTTPURL(rawURL)
}

// ProbeURLAuth 给 Step 7 可观测性工具用 —— 带可选 basic auth(grafana/elk)
// 或 Bearer API key(grafana glsa_)。
func (a *App) ProbeURLAuth(rawURL, user, pass, apiKey string) dsprobe.Result {
	return dsprobe.ProbeHTTPURLAuth(rawURL, user, pass, apiKey)
}
