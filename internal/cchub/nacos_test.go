package cchub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPreloadNacos_LoginThenList 跑一个假 nacos:/auth/login 换 token,/cs/configs 返回 3 条。
// 验证:登录成功、token 被带进 list 请求、pageItems 被正确转成 Entry。
func TestPreloadNacos_LoginThenList(t *testing.T) {
	var gotList *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		// 新 probe 先试 v3 → 这里让 v3 路径 404,自动回退到 v1
		case "/nacos/v3/console/server/state", "/v3/console/server/state", "/v1/console/server/state":
			http.NotFound(w, r)
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.3.x"})
		case "/nacos/v1/auth/login":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("username") != "nacos" || r.FormValue("password") != "nacos" {
				http.Error(w, "invalid creds", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "tok-abc",
				"tokenTtl":    18000,
			})
		case "/nacos/v1/cs/configs":
			gotList = r
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalCount":     3,
				"pagesAvailable": 1,
				"pageItems": []map[string]any{
					{"dataId": "commerce.yaml", "group": "DEFAULT_GROUP", "tenant": "shop-dev", "type": "yaml"},
					{"dataId": "user.yaml", "group": "DEFAULT_GROUP", "tenant": "shop-dev", "type": "yaml"},
					{"dataId": "content.yaml", "group": "APP", "tenant": "shop-dev", "type": "yaml"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := PreloadNacos(Request{
		Type:      "nacos",
		Addr:      srv.URL,
		Username:  "nacos",
		Password:  "nacos",
		Namespace: "shop-dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(r.Entries))
	}
	if r.Entries[0].Locator != "commerce.yaml" || r.Entries[0].Group != "DEFAULT_GROUP" {
		t.Errorf("entry 0: %+v", r.Entries[0])
	}
	// 验证 list 请求带了 accessToken + tenant
	if gotList == nil {
		t.Fatal("list request not received")
	}
	if gotList.URL.Query().Get("accessToken") != "tok-abc" {
		t.Error("accessToken 没带进 list 请求")
	}
	if gotList.URL.Query().Get("tenant") != "shop-dev" {
		t.Error("tenant 没带")
	}
}

// 无账号开放模式:不发 login,直接 list。
func TestPreloadNacos_NoAuth(t *testing.T) {
	var loginHit, listHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "x"})
		case "/nacos/v1/auth/login":
			loginHit = true
			http.Error(w, "should not login", http.StatusInternalServerError)
		case "/nacos/v1/cs/configs":
			listHit = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalCount": 0, "pagesAvailable": 0, "pageItems": []any{},
			})
		default:
			// 未覆盖路径都 404,避免 v3 probe 误吃 200(这里的 probe 顺序是 v3 先试)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := PreloadNacos(Request{Type: "nacos", Addr: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if loginHit {
		t.Error("no username 时不应登录")
	}
	if !listHit {
		t.Error("应 list 才对")
	}
	// 空 tenant 下 0 条 → 有 note
	if len(r.Notes) == 0 {
		t.Error("应有 '没有任何配置' note")
	}
}

// 登录账号错 → 返 error,不假装成功
func TestPreloadNacos_LoginFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// v3 probe 先走,这里让它 404 强制回退到 v1
		switch r.URL.Path {
		case "/nacos/v3/console/server/state", "/v3/console/server/state", "/v1/console/server/state":
			http.NotFound(w, r)
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "x"})
		default:
			http.Error(w, `{"message":"invalid"}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	_, err := PreloadNacos(Request{Type: "nacos", Addr: srv.URL, Username: "x", Password: "y"})
	if err == nil {
		t.Fatal("expected error on login 403")
	}
	if !strings.Contains(err.Error(), "登录") {
		t.Errorf("error message should mention 登录: %v", err)
	}
}

// addr 自动补 scheme
func TestPreloadNacos_AddrNormalizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "x"})
		case "/nacos/v1/cs/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalCount": 0, "pagesAvailable": 0, "pageItems": []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// 去掉 http:// 前缀
	bare := strings.TrimPrefix(srv.URL, "http://")
	_, err := PreloadNacos(Request{Type: "nacos", Addr: bare})
	if err != nil {
		t.Errorf("should normalize 'host:port' → 'http://host:port', got %v", err)
	}
}

// 新:nacos 部署在根路径(非 /nacos)→ 自动回退到第二个 candidate。
func TestPreloadNacos_RootContextPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v1/console/server/state":
			// 第一个 candidate:服务端不认,返 404
			http.NotFound(w, r)
		case "/v1/console/server/state":
			// 根路径版本认
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.x"})
		case "/v1/cs/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalCount": 1, "pagesAvailable": 1,
				"pageItems": []map[string]any{
					{"dataId": "app.yaml", "group": "DEFAULT_GROUP"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := PreloadNacos(Request{Type: "nacos", Addr: srv.URL})
	if err != nil {
		t.Fatalf("should fallback to root context, got %v", err)
	}
	if len(r.Entries) != 1 {
		t.Errorf("entries: %d", len(r.Entries))
	}
	// 验证 note 提到"根路径"
	found := false
	for _, n := range r.Notes {
		if strings.Contains(n, "根路径") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected note about 根路径, got %v", r.Notes)
	}
}

// 两个 context path 都 502 → 完整诊断信息
func TestPreloadNacos_Both502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := PreloadNacos(Request{Type: "nacos", Addr: srv.URL})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	// 诊断信息至少要提到 502 + 可能原因
	if !strings.Contains(msg, "502") {
		t.Errorf("error should include 502 status: %v", err)
	}
	if !strings.Contains(msg, "反向代理") && !strings.Contains(msg, "Nacos") {
		t.Errorf("error should give diagnostic hints: %v", err)
	}
}

// Nacos 3.x 根路径部署:login 走 /v3/auth/user/login,list 走 /v3/console/cs/config/list,
// 响应结构嵌套在 data 下,字段名 groupName/namespaceId(而非 v1 的 group/tenant)。
func TestPreloadNacos_V3RootPath(t *testing.T) {
	var listHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v3/console/server/state", "/nacos/v1/console/server/state":
			// /nacos/ 前缀不生效(用户这台 Nacos 部署在根)
			http.NotFound(w, r)
		case "/v3/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "3.0"})
		case "/v3/auth/user/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "tok-xyz", "tokenTtl": 18000, "username": "nacos",
			})
		case "/v3/console/cs/config/list":
			if r.URL.Query().Get("accessToken") != "tok-xyz" {
				http.Error(w, "missing token", http.StatusForbidden)
				return
			}
			listHit = true
			// 模拟真实 v3 返回结构(user 的 43.206.141.191 就是这个形状)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "message": "success",
				"data": map[string]any{
					"totalCount":     2,
					"pageNumber":     1,
					"pagesAvailable": 1,
					"pageItems": []map[string]any{
						{"dataId": "test_config", "groupName": "DEFAULT_GROUP", "namespaceId": "public", "type": "text"},
						{"dataId": "hyperf-service-config", "groupName": "DEFAULT_GROUP", "namespaceId": "public", "type": "json"},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := PreloadNacos(Request{
		Type: "nacos", Addr: srv.URL,
		Username: "nacos", Password: "nacos",
		Namespace: "public",
	})
	if err != nil {
		t.Fatalf("preload: %v", err)
	}
	if !listHit {
		t.Error("list 请求未命中")
	}
	if len(r.Entries) != 2 {
		t.Fatalf("entries: %d", len(r.Entries))
	}
	if r.Entries[0].Locator != "test_config" || r.Entries[0].Group != "DEFAULT_GROUP" {
		t.Errorf("entry 0: %+v", r.Entries[0])
	}
	// note 应提到 v3 + 根路径
	found := false
	for _, n := range r.Notes {
		if strings.Contains(n, "v3") && strings.Contains(n, "根路径") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("note 应包含 'v3' + '根路径',got %v", r.Notes)
	}
}
