// bindings_labelprobe.go —— Step 7 可观测性"环境×服务 → Loki 标签映射"绑定。
package main

import "github.com/xiaolong/troubleshooter-studio/internal/labelprobe"

// LokiAuthInput 共享凭证字段。
type LokiAuthInput struct {
	GrafanaURL string `json:"grafana_url,omitempty"` // 走 grafana 代理时填
	LokiURL    string `json:"loki_url,omitempty"`    // 直连 loki 时填
	DSUID      string `json:"ds_uid,omitempty"`      // grafana 模式下,loki datasource 的 UID
	APIKey     string `json:"api_key,omitempty"`     // grafana token / loki bearer
	User       string `json:"user,omitempty"`        // basic auth(grafana / 直连 loki 都支持)
	Pass       string `json:"pass,omitempty"`
}

// ListGrafanaDatasources 列 Grafana 所有 datasource(给 UI 挑哪个是 loki)。
// 同时也是"Grafana 凭证可用吗"的连通性校验入口。
func (a *App) ListGrafanaDatasources(in LokiAuthInput) ([]labelprobe.Datasource, error) {
	return labelprobe.ListGrafanaDatasources(in.GrafanaURL, in.APIKey, in.User, in.Pass)
}

// ListLokiLabels 列 Loki 所有 label key。优先走 grafana proxy(凭证统一),
// 没 grafana 信息时回直连 loki。
func (a *App) ListLokiLabels(in LokiAuthInput) (*labelprobe.LabelsResult, error) {
	if in.GrafanaURL != "" && in.DSUID != "" {
		return labelprobe.ListLokiLabelsViaGrafana(in.GrafanaURL, in.DSUID, in.APIKey, in.User, in.Pass)
	}
	if in.LokiURL != "" {
		return labelprobe.ListLokiLabelsDirect(in.LokiURL, in.User, in.Pass)
	}
	return nil, errMissingLoki
}

// ListLokiLabelValues 列某 label 下所有 value。query 可选 LogQL 选择器,
// 用于只列匹配它的 values(如选完 namespace 后再拉 app 限定到该 namespace 内)。
func (a *App) ListLokiLabelValues(in LokiAuthInput, labelKey, query string) (*labelprobe.ValuesResult, error) {
	if in.GrafanaURL != "" && in.DSUID != "" {
		return labelprobe.ListLokiLabelValuesViaGrafana(in.GrafanaURL, in.DSUID, labelKey, query, in.APIKey, in.User, in.Pass)
	}
	if in.LokiURL != "" {
		return labelprobe.ListLokiLabelValuesDirect(in.LokiURL, labelKey, query, in.User, in.Pass)
	}
	return nil, errMissingLoki
}

var errMissingLoki = errLokiMissing("缺 grafana_url+ds_uid 或 loki_url")

type errLokiMissing string

func (e errLokiMissing) Error() string { return string(e) }
