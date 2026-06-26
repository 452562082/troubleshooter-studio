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

// TestLoginNacos_V1Only 模拟 nacos 2.x:只有 /v1/auth/login,/v3 全 404。
// 用户 2026-05-15 实测部署就是这种(nacos-server:v2.3.0,API 端口 8848)。
// 验证 LoginNacos 公开入口能 probe + login 拿到 (token, tokenTtl, note),
// 这是 install 端 doLoginNacos 复用的同一条路径 — 直接保护 install 阶段不撞 v3 死路。
func TestLoginNacos_V1Only(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		// v3 探测全 404 → probeFlavor 自动回退 v1
		case "/nacos/v3/console/server/state", "/v3/console/server/state", "/v1/console/server/state":
			http.NotFound(w, r)
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.3.0"})
		case "/nacos/v1/auth/login":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("username") != "nacos" || r.FormValue("password") != "nacos" {
				http.Error(w, "bad creds", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "tok-v1-abc",
				"tokenTtl":    18000, // nacos 默认 5h
				"username":    "nacos",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	token, ttl, note, err := LoginNacos(srv.URL, "nacos", "nacos")
	if err != nil {
		t.Fatalf("LoginNacos v1: %v", err)
	}
	if token != "tok-v1-abc" {
		t.Errorf("token = %q, want tok-v1-abc", token)
	}
	if ttl != 18000 {
		t.Errorf("ttl = %d, want 18000", ttl)
	}
	if !strings.Contains(note, "v1") {
		t.Errorf("note 应提及 v1,got %q", note)
	}
}

// TestLoginNacos_V3Path 模拟 nacos 3.x:/v3/auth/user/login 通,/v1 全 404。
func TestLoginNacos_V3Path(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v3/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "3.1.0"})
		case "/nacos/v3/auth/user/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "tok-v3-xyz",
				"tokenTtl":    315360000, // 10y — 推荐值
				"username":    "nacos",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	token, ttl, note, err := LoginNacos(srv.URL, "nacos", "nacos")
	if err != nil {
		t.Fatalf("LoginNacos v3: %v", err)
	}
	if token != "tok-v3-xyz" {
		t.Errorf("token = %q, want tok-v3-xyz", token)
	}
	if ttl != 315360000 {
		t.Errorf("ttl = %d, want 315360000", ttl)
	}
	if !strings.Contains(note, "v3") {
		t.Errorf("note 应提及 v3,got %q", note)
	}
}

// TestFetchNacos_RedactsToken 验证拉配置失败时,错误信息里的明文 accessToken 被脱敏。
// 防回归:token 经 URL query 传给 nacos,失败路径会把 candidate URL(含 *url.Error)写进
// error/Notes 回到桌面 UI / 代理日志 —— 必须替换成 REDACTED。
func TestFetchNacos_RedactsToken(t *testing.T) {
	const secret = "super-secret-accesstoken-xyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 任何 config 请求都 403 → 触发错误路径(把含 token 的 candidate URL 写进 attempts/error)
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	cli := &nacosClient{
		base:    strings.TrimRight(srv.URL, "/"),
		httpCli: srv.Client(),
		flavor:  apiFlavor{ContextPath: "", Version: "v1"},
		token:   secret,
	}
	_, err := cli.fetchOneConfigInternal("public", "DEFAULT_GROUP", "app.yaml")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error 泄露了明文 accessToken: %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("token 应被替换成 REDACTED: %v", err)
	}
}

// TestLoginNacos_BadCreds 凭据错 → 返 error,install 端会 warn skip 注册。
func TestLoginNacos_BadCreds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nacos/v3/console/server/state", "/v3/console/server/state", "/v1/console/server/state":
			http.NotFound(w, r)
		case "/nacos/v1/console/server/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.3.0"})
		case "/nacos/v1/auth/login":
			http.Error(w, `{"status":403,"message":"unknown user"}`, http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	_, _, _, err := LoginNacos(srv.URL, "nacos", "wrong-pass")
	if err == nil {
		t.Fatal("bad creds 应返 error,got nil")
	}
}
