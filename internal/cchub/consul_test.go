package cchub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreloadConsul_ListKeys(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Consul-Token")
		switch r.URL.Path {
		case "/v1/kv/": // 根下 top-level prefix 探测
			_ = json.NewEncoder(w).Encode([]string{"config/", "infra/"})
		case "/v1/kv/config":
			_ = json.NewEncoder(w).Encode([]string{
				"config/commerce/dev",
				"config/user/dev",
				"config/content/prod",
				"config/", // 目录节点,测试被过滤
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r, err := PreloadConsul(Request{
		Type:      "consul",
		Addr:      srv.URL,
		Token:     "secret-xxx",
		Namespace: "config",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 3 {
		t.Fatalf("expected 3 keys(目录 config/ 被过滤掉), got %d: %+v", len(r.Entries), r.Entries)
	}
	if gotToken != "secret-xxx" {
		t.Errorf("X-Consul-Token: got %q", gotToken)
	}
	// Namespaces 也应带回来(top-level prefix 供前端下拉)
	if len(r.Namespaces) < 2 {
		t.Errorf("expected >=2 namespaces(config, infra),got %d", len(r.Namespaces))
	}
}

// NamespacesOnly:只列 top-level prefix,不拉 keys
func TestPreloadConsul_NamespacesOnly(t *testing.T) {
	var kvConfigHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/kv/":
			_ = json.NewEncoder(w).Encode([]string{"config/", "appconf/", "infra/"})
		case "/v1/kv/config":
			kvConfigHit = true
			_ = json.NewEncoder(w).Encode([]string{"config/a", "config/b"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, err := PreloadConsul(Request{Type: "consul", Addr: srv.URL, NamespacesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if kvConfigHit {
		t.Error("NamespacesOnly 不应拉 /v1/kv/config 的 keys")
	}
	if len(r.Namespaces) != 3 {
		t.Errorf("期望 3 个 top-level prefix,got %d", len(r.Namespaces))
	}
	if len(r.Entries) != 0 {
		t.Error("NamespacesOnly 不应有 Entries")
	}
}

// prefix 下无 key → 404 被视为"空结果",不抛错
func TestPreloadConsul_404NotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	r, err := PreloadConsul(Request{Type: "consul", Addr: srv.URL, Namespace: "nonexistent"})
	if err != nil {
		t.Fatalf("404 不应抛 error: %v", err)
	}
	if len(r.Entries) != 0 {
		t.Error("应 0 条")
	}
	if len(r.Notes) == 0 {
		t.Error("应有一条 '404 prefix 下无 key' note")
	}
}
