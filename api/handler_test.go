// handler_test.go —— HTTP API 端到端测试。
//
// 之前 0% coverage 是产品定位"三入口并列(CLI / 桌面 / HTTP)"里的真盲区:CLI 和 desktop
// 都有 e2e 覆盖,HTTP server 改 router 路径或 handler 行为没人发现。本测试覆盖 6 个端点
// 的 happy path + 主要错误路径(空 body / 错误 yaml / 404),不深入测业务逻辑(那是
// internal/generator / internal/doctor 等的事)。
package api

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// minimalYAML 是测试用的最小合法 yaml,够过 config.LoadFromBytes 不引爆 schema 校验。
// 不试图穷举字段 — 那是 internal/config / internal/generator 测试覆盖的事。
const minimalYAML = `
meta:
  schema_version: "0.1"
system:
  id: test-bot
  name: "Test Bot"
agent:
  id: test-troubleshooter
  name: "test bot"
  model: openai/gpt-5
environments:
  - id: dev
    api_domain: "https://api.example.com"
    web_domain: "https://web.example.com"
    is_prod: false
repos:
  - name: example
    url: https://github.com/example/example
    stack: go
    framework: gin
    service_names: [example]
infrastructure:
  config_centers:
    - type: nacos
      id: default
generation:
  targets: [openclaw]
`

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	// templateRoot 指 repo 的 templates/,从 cwd 反推。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tplRoot := filepath.Join(wd, "..", "templates")
	if _, err := os.Stat(tplRoot); err != nil {
		t.Skipf("templates root not found at %s (CI 下跑测试需要 repo 完整 clone): %v", tplRoot, err)
	}
	srv := &Server{TemplateRoot: tplRoot}
	return httptest.NewServer(NewRouter(srv, nil))
}

// post 是简化的 helper:对 server.URL+path POST yaml body,返回 status + body 字节。
func post(t *testing.T, server *httptest.Server, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(server.URL+path, "text/yaml", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := readAll(resp.Body)
	return resp.StatusCode, data
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, nil
		}
	}
}

// TestHandleValidate_HappyPath: POST yaml → 200 + valid:true + system id
func TestHandleValidate_HappyPath(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	status, body := post(t, srv, "/api/validate", minimalYAML)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, body)
	}
	if out["valid"] != true {
		t.Errorf("valid should be true,got %v", out["valid"])
	}
	if out["system"] != "test-bot" {
		t.Errorf("system should be test-bot,got %v", out["system"])
	}
}

func TestHandleValidate_CodeIntelligence(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	enabledYAML := strings.Replace(minimalYAML, "infrastructure:\n", `code_intelligence:
  enabled: true
  provider: codegraph
infrastructure:
`, 1)
	status, body := post(t, srv, "/api/validate", enabledYAML)
	if status != http.StatusOK {
		t.Fatalf("enabled code intelligence status = %d, body=%s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal enabled response: %v\nbody=%s", err, body)
	}
	if out["valid"] != true {
		t.Fatalf("enabled code intelligence should be valid, got %v", out["valid"])
	}

	invalidYAML := strings.Replace(enabledYAML, "provider: codegraph", "provider: lsp", 1)
	status, body = post(t, srv, "/api/validate", invalidYAML)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d, body=%s", status, body)
	}
	if !strings.Contains(string(body), "code_intelligence.provider") {
		t.Fatalf("unknown provider error missing field name: %s", body)
	}
}

func TestHandleValidate_ServiceTopology(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	topologyYAML := strings.Replace(minimalYAML, "infrastructure:\n", `  - name: bff
    url: https://github.com/example/bff
    stack: php
    service_names: [mall-bff]
  - name: orders
    url: https://github.com/example/orders
    stack: go
    service_names: [order-service]
service_topology:
  overrides:
    - action: confirm
      from_service: example
      to_service: mall-bff
      protocol: http
      method: get
      path: /api/orders/:id
    - action: reject
      from_service: mall-bff
      to_service: order-service
      protocol: grpc
      rpc_method: orders.v1.OrderService/GetOrder
    - action: add
      from_service: example
      to_service: order-service
      protocol: http
      method: POST
      path: /api/orders
infrastructure:
`, 1)

	status, body := post(t, srv, "/api/validate", topologyYAML)
	if status != http.StatusOK {
		t.Fatalf("valid topology status = %d, body=%s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal valid topology response: %v\nbody=%s", err, body)
	}
	if out["valid"] != true {
		t.Fatalf("valid topology should return valid:true, got %v", out["valid"])
	}

	invalidYAML := strings.Replace(topologyYAML, "action: confirm", "action: guess", 1)
	status, body = post(t, srv, "/api/validate", invalidYAML)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid topology status = %d, body=%s", status, body)
	}
	if !strings.Contains(string(body), "service_topology.overrides") {
		t.Fatalf("invalid topology error missing field name: %s", body)
	}
}

// TestHandleValidate_BadYAML: 错误 yaml → 400 + error message
func TestHandleValidate_BadYAML(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	status, body := post(t, srv, "/api/validate", "not-a-valid:\n  yaml-block:\n[broken")
	if status != http.StatusBadRequest {
		t.Errorf("status should be 400 for bad yaml,got %d body=%s", status, body)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if _, ok := out["error"]; !ok {
		t.Errorf("response should have 'error' field,got %s", body)
	}
}

// TestHandlePrefillCreds_HappyPath: 给 yaml → 返回 env var map(可能为空,但应是 200)
func TestHandlePrefillCreds_HappyPath(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	status, body := post(t, srv, "/api/prefill-creds", minimalYAML)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, body)
	}
	// 返回应是 map(可能空),不应是 null
	if out == nil {
		t.Errorf("response should be map,got null")
	}
}

// TestHandleSchema_HappyPath: GET /api/schema → 200 + yaml content-type + 内容含 schema 关键字
func TestHandleSchema_HappyPath(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/schema")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type should be yaml,got %q", ct)
	}
	data, _ := readAll(resp.Body)
	// schema yaml 应至少含一些显眼字段
	if !strings.Contains(string(data), "schema_version") && !strings.Contains(string(data), "system") {
		t.Errorf("schema content looks empty/wrong: %.200s", data)
	}
}

// TestHandleSchema_EmbeddedFallback: go install / tshoot serve 场景下 templates
// 可能从 embed 解压到临时目录,旁边没有 repo 的 schema/ 目录;此时仍应返回内嵌 schema。
func TestHandleSchema_EmbeddedFallback(t *testing.T) {
	tmp := t.TempDir()
	tplRoot := filepath.Join(tmp, "templates")
	if err := os.MkdirAll(tplRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := &Server{TemplateRoot: tplRoot}
	server := httptest.NewServer(NewRouter(srv, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/schema")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, data)
	}
	data, _ := readAll(resp.Body)
	if !strings.Contains(string(data), "schema_version") {
		t.Fatalf("embedded schema fallback looks wrong: %.200s", data)
	}
}

// TestHandleDoctor_HappyPath: doctor 需要 repos_root query 参数,空给空报告
func TestHandleDoctor_HappyPath(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/doctor?repos_root=/nonexistent",
		"text/yaml", bytes.NewReader([]byte(minimalYAML)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// repos_root 不存在 doctor 不应该 500,应当返回 200 + 列出 issues(missing-repo 等)
	if resp.StatusCode != http.StatusOK {
		data, _ := readAll(resp.Body)
		t.Errorf("doctor should return 200 even with missing repos,got %d body=%s", resp.StatusCode, data)
	}
}

// TestCORS: OPTIONS 请求应返回 204 + CORS headers
func TestCORS(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/validate", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS should return 204,got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS Allow-Origin should be *,got %q", got)
	}
}

// TestSPAHandler_FallsBackToIndex: 命中前端路由(/some/vue/route) → 回退 index.html
// 用 testing/fstest.MapFS 模拟 dist FS(避免依赖真前端 build 产物)。
func TestSPAHandler_FallsBackToIndex(t *testing.T) {
	mockFS := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>SPA index</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("// js bundle")},
	}
	srv := &Server{TemplateRoot: ""}
	server := httptest.NewServer(NewRouter(srv, fs.FS(mockFS)))
	defer server.Close()

	// 命中真实静态文件
	resp, err := http.Get(server.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "js bundle") {
		t.Errorf("static asset should be served,got: %s", body)
	}

	// 未命中(vue route)→ 回退 index.html
	resp, err = http.Get(server.URL + "/some/vue/route")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = readAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "SPA index") {
		t.Errorf("SPA fallback should serve index.html,got: %s", body)
	}
}
