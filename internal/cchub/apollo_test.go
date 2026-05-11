package cchub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NamespacesOnly:列 envs 作 Namespaces,不拉具体 clusters/namespaces
func TestPreloadApollo_NamespacesOnly(t *testing.T) {
	var clustersHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/v1/envs" {
			_ = json.NewEncoder(w).Encode([]string{"DEV", "UAT", "PRO"})
			return
		}
		if r.URL.Path == "/openapi/v1/envs/DEV/apps/foo/clusters" {
			clustersHit = true
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "default"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	r, err := PreloadApollo(Request{
		Type: "apollo", Addr: srv.URL, Token: "t",
		NamespacesOnly: true, AppID: "foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if clustersHit {
		t.Error("NamespacesOnly 不应访问 clusters 接口")
	}
	if len(r.Namespaces) != 3 {
		t.Fatalf("expected 3 envs, got %d: %+v", len(r.Namespaces), r.Namespaces)
	}
	if r.Namespaces[0].ID != "DEV" {
		t.Errorf("first env should be DEV, got %+v", r.Namespaces[0])
	}
}

// /openapi/v1/envs 不可用 → fallback 到硬编码 DEV/FAT/UAT/PRO
func TestPreloadApollo_EnvFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // 全 404,包括 /openapi/v1/envs
	}))
	defer srv.Close()
	r, err := PreloadApollo(Request{
		Type: "apollo", Addr: srv.URL, Token: "t",
		NamespacesOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Namespaces) != 4 { // DEV / FAT / UAT / PRO
		t.Errorf("fallback 应给 4 个常用 env, got %d", len(r.Namespaces))
	}
	// note 里该说"不支持列 envs"
	found := false
	for _, n := range r.Notes {
		if contains(n, "不支持列 envs") {
			found = true
		}
	}
	if !found {
		t.Errorf("note 应提示 fallback,got %+v", r.Notes)
	}
}

// token 无权限 → 401/403 带明确诊断
func TestPreloadApollo_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/v1/envs" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer srv.Close()
	_, err := PreloadApollo(Request{
		Type: "apollo", Addr: srv.URL, Token: "bad",
		Namespace: "DEV", AppID: "foo",
	})
	if err == nil {
		t.Fatal("401 应返错")
	}
	if !contains(err.Error(), "无权限") {
		t.Errorf("错误消息应含 '无权限':%v", err)
	}
}

// 正常:指定 env + app_id → 列 clusters × namespaces,每条 entry.tenant=env
func TestPreloadApollo_FullApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v1/envs":
			_ = json.NewEncoder(w).Encode([]string{"DEV", "UAT"})
		case "/openapi/v1/envs/DEV/apps/foo/clusters":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "default"}})
		case "/openapi/v1/envs/DEV/apps/foo/clusters/default/namespaces":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"namespaceName": "application", "format": "properties"},
				{"namespaceName": "db.yaml", "format": "yaml"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, err := PreloadApollo(Request{
		Type: "apollo", Addr: srv.URL, Token: "t",
		Namespace: "DEV", AppID: "foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 2 {
		t.Fatalf("want 2 ns, got %d: %+v", len(r.Entries), r.Entries)
	}
	if r.Entries[0].Tenant != "DEV" || r.Entries[0].Group != "default" {
		t.Errorf("entry 0 tenant/group: %+v", r.Entries[0])
	}
	if len(r.Namespaces) != 2 { // Namespaces 同时带回来给 UI 下拉
		t.Errorf("Namespaces 应带 2 个 env,got %d", len(r.Namespaces))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
