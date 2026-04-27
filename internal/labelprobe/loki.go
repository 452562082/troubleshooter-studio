// Package labelprobe 拉 Loki 标签 / 值,给 wizard 可观测性 step 的"环境×服务 → Loki 标签映射"用。
//
// 两条路径:
//   a) 通过 Grafana datasource proxy(推荐):统一鉴权(grafana url + api key)
//      GET /api/datasources                              列 datasources(挑 loki UID)
//      GET /api/datasources/proxy/uid/<uid>/loki/api/v1/labels
//      GET /api/datasources/proxy/uid/<uid>/loki/api/v1/label/<key>/values
//   b) 直连 Loki:
//      GET /loki/api/v1/labels
//      GET /loki/api/v1/label/<key>/values
//
// 5 秒超时,401/403/404 各自人话提示。结果原样返前端,前端做 env / service 启发式匹值。
package labelprobe

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const probeTimeout = 5 * time.Second

// Datasource Grafana 一条 datasource 的简化形态(只挑 UI 关心的字段)。
type Datasource struct {
	UID     string `json:"uid"`
	Name    string `json:"name"`
	Type    string `json:"type"` // "loki" / "prometheus" / ...
	URL     string `json:"url,omitempty"`
	IsLoki  bool   `json:"is_loki"`  // type=="loki" 简化标志,UI 用
	Default bool   `json:"default,omitempty"`
}

// LabelsResult /labels 响应。
type LabelsResult struct {
	Labels []string `json:"labels"`
	Notes  []string `json:"notes,omitempty"`
}

// ValuesResult /label/<key>/values 响应。
type ValuesResult struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	Notes  []string `json:"notes,omitempty"`
}

// ── Grafana 入口 ──────────────────────────────────────────────────────

// ListGrafanaDatasources 列 grafana 里所有 datasource;UI 用来让用户挑哪个是 loki。
// auth: grafana 必须有 token(API key 或 service account token,Bearer 头);
// basic auth(user/pass)也支持作为 fallback —— 但 grafana 推荐 token。
func ListGrafanaDatasources(grafanaURL, apiKey, user, pass string) ([]Datasource, error) {
	u := strings.TrimRight(normalize(grafanaURL), "/") + "/api/datasources"
	body, status, err := httpGet(u, apiKey, user, pass)
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 403 {
		return nil, fmt.Errorf("grafana 鉴权失败 (HTTP %d):检查 api key / 账密", status)
	}
	if status != 200 {
		return nil, fmt.Errorf("grafana /api/datasources HTTP %d: %s", status, snippet(body))
	}
	var raw []struct {
		UID       string `json:"uid"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		URL       string `json:"url"`
		IsDefault bool   `json:"isDefault"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("grafana datasources 解析失败: %w(body: %s)", err, snippet(body))
	}
	out := make([]Datasource, 0, len(raw))
	for _, d := range raw {
		out = append(out, Datasource{
			UID: d.UID, Name: d.Name, Type: d.Type, URL: d.URL,
			IsLoki: d.Type == "loki", Default: d.IsDefault,
		})
	}
	return out, nil
}

// ListLokiLabelsViaGrafana 通过 grafana proxy 拉 loki labels。
func ListLokiLabelsViaGrafana(grafanaURL, dsUID, apiKey, user, pass string) (*LabelsResult, error) {
	u := fmt.Sprintf("%s/api/datasources/proxy/uid/%s/loki/api/v1/labels",
		strings.TrimRight(normalize(grafanaURL), "/"), url.PathEscape(dsUID))
	body, status, err := httpGet(u, apiKey, user, pass)
	return decodeLabelsResp(body, status, err)
}

// ListLokiLabelValuesViaGrafana 通过 grafana proxy 拉某 label 的 values。
// query 是可选的 LogQL 选择器(如 `{namespace="go-truss-dev"}`),用于只列匹配它的 values
// —— 例:选完 namespace 之后再拉 app 时,只返回该 namespace 下确实出现过的 app。
func ListLokiLabelValuesViaGrafana(grafanaURL, dsUID, labelKey, query, apiKey, user, pass string) (*ValuesResult, error) {
	u := fmt.Sprintf("%s/api/datasources/proxy/uid/%s/loki/api/v1/label/%s/values",
		strings.TrimRight(normalize(grafanaURL), "/"),
		url.PathEscape(dsUID), url.PathEscape(labelKey))
	if query != "" {
		u += "?query=" + url.QueryEscape(query)
	}
	body, status, err := httpGet(u, apiKey, user, pass)
	return decodeValuesResp(labelKey, body, status, err)
}

// ── 直连 Loki ─────────────────────────────────────────────────────────

func ListLokiLabelsDirect(lokiURL, user, pass string) (*LabelsResult, error) {
	u := strings.TrimRight(normalize(lokiURL), "/") + "/loki/api/v1/labels"
	body, status, err := httpGet(u, "", user, pass)
	return decodeLabelsResp(body, status, err)
}

func ListLokiLabelValuesDirect(lokiURL, labelKey, query, user, pass string) (*ValuesResult, error) {
	u := fmt.Sprintf("%s/loki/api/v1/label/%s/values",
		strings.TrimRight(normalize(lokiURL), "/"), url.PathEscape(labelKey))
	if query != "" {
		u += "?query=" + url.QueryEscape(query)
	}
	body, status, err := httpGet(u, "", user, pass)
	return decodeValuesResp(labelKey, body, status, err)
}

// ── helpers ───────────────────────────────────────────────────────────

func normalize(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		return "http://" + addr
	}
	return addr
}

func httpGet(rawURL, apiKey, user, pass string) ([]byte, int, error) {
	cli := &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("URL 格式错: %w", err)
	}
	req.Header.Set("User-Agent", "tshoot-studio-labelprobe/1.0")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, 0, errMsg(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB 限流
	return body, resp.StatusCode, nil
}

func errMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("DNS 解析失败,域名不存在")
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒(端口未开?)")
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return fmt.Errorf("超时(网络/防火墙?)")
	case strings.Contains(msg, "x509"), strings.Contains(msg, "tls"):
		return fmt.Errorf("TLS 错: %s", msg)
	}
	return err
}

// Loki 响应结构:{"status":"success","data":["label1","label2",...]}
func decodeLabelsResp(body []byte, status int, err error) (*LabelsResult, error) {
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 403 {
		return nil, fmt.Errorf("Loki 鉴权失败 (HTTP %d):检查凭证 / Loki tenant 是否需要 X-Scope-OrgID", status)
	}
	if status != 200 {
		return nil, fmt.Errorf("Loki /labels HTTP %d: %s", status, snippet(body))
	}
	var doc struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("Loki labels 响应解析失败: %w(body: %s)", err, snippet(body))
	}
	notes := []string{fmt.Sprintf("拉到 %d 个 label key", len(doc.Data))}
	return &LabelsResult{Labels: doc.Data, Notes: notes}, nil
}

func decodeValuesResp(key string, body []byte, status int, err error) (*ValuesResult, error) {
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 403 {
		return nil, fmt.Errorf("Loki 鉴权失败 (HTTP %d)", status)
	}
	if status != 200 {
		return nil, fmt.Errorf("Loki /label/%s/values HTTP %d: %s", key, status, snippet(body))
	}
	var doc struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("Loki label values 响应解析失败: %w(body: %s)", err, snippet(body))
	}
	notes := []string{fmt.Sprintf("label=%s 共 %d 个 value", key, len(doc.Data))}
	return &ValuesResult{Key: key, Values: doc.Data, Notes: notes}, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
