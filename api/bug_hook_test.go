package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestHandleBugHookStoresAssignedBug(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	platformStore := bughub.NewPlatformStore(bughub.DefaultRoot())
	platform, err := platformStore.Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", Account: "xiaolong", HookSecret: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	srv := httptest.NewServer(NewRouter(&Server{}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/bug-hooks/"+platform.ID+"?secret=secret", "application/json", bytes.NewBufferString(
		`{"bug":{"id":"1842","title":"支付页 500","assignedTo":"xiaolong"}}`,
	))
	if err != nil {
		t.Fatalf("POST hook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var result bughub.WebhookResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Accepted || result.StoredID != "zentao-main-1842" {
		t.Fatalf("result = %+v", result)
	}
	items, err := bughub.NewStore(bughub.DefaultRoot()).List()
	if err != nil {
		t.Fatalf("List bugs: %v", err)
	}
	if len(items) != 1 || items[0].Title != "支付页 500" {
		t.Fatalf("items = %+v", items)
	}
}

func TestHandleBugHookRejectsBadSecret(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	_, err := bughub.NewPlatformStore(bughub.DefaultRoot()).Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", HookSecret: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	srv := httptest.NewServer(NewRouter(&Server{}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/bug-hooks/zentao-main?secret=bad", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST hook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
