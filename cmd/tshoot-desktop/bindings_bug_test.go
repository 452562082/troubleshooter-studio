package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

func TestSyncBugPlatformStoresAssignedBugs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong","files":[{"id":"101","title":"screen.png","extension":"png"}]}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}

	got, err := (&App{}).SyncBugPlatform("zentao-main")
	if err != nil {
		t.Fatalf("SyncBugPlatform: %v", err)
	}
	if got.Stored != 1 || got.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", got)
	}
	items, err := bugStore().List()
	if err != nil {
		t.Fatalf("List bugs: %v", err)
	}
	if len(items) != 1 || items[0].ID != "zentao-1842" {
		t.Fatalf("items = %+v", items)
	}
	if len(items[0].Attachments) != 1 || items[0].Attachments[0].Name != "screen.png" {
		t.Fatalf("attachments = %+v", items[0].Attachments)
	}
}

func TestSyncBugPlatformArchivesStaleBugAndPreservesAttachmentCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	if _, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-old", Source: "zentao", PlatformID: "zentao-main", Title: "已关闭旧 Bug",
	}); err != nil {
		t.Fatalf("Upsert stale bug: %v", err)
	}
	cacheDir := filepath.Join(bughub.DefaultRoot(), "attachments", "zentao-old")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "screen.png"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := (&App{}).SyncBugPlatform("zentao-main")
	if err != nil {
		t.Fatalf("SyncBugPlatform: %v", err)
	}
	if got.Pruned != 1 || len(got.PrunedIDs) != 1 || got.PrunedIDs[0] != "zentao-old" {
		t.Fatalf("result = %+v", got)
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("historical attachment cache was not preserved: %v", err)
	}
	archived, ok, err := bugStore().Get("zentao-old")
	if err != nil || !ok || archived.InboxState != bughub.BugInboxHistory {
		t.Fatalf("archived bug=%+v ok=%v err=%v", archived, ok, err)
	}
}

func TestSyncBugPlatformStoresConfiguredPlatformAndBotEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong","keywords":"prod"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong","keywords":"prod"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Env: "stage", BotEnv: "test", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}

	if _, err := (&App{}).SyncBugPlatform("zentao-main"); err != nil {
		t.Fatalf("SyncBugPlatform: %v", err)
	}
	items, err := bugStore().List()
	if err != nil {
		t.Fatalf("List bugs: %v", err)
	}
	if len(items) != 1 || items[0].Env != "stage" || items[0].BotEnv != "test" {
		t.Fatalf("items = %+v", items)
	}
}

func TestSyncBugPlatformClearsExpiredCapturedSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID:            "zentao-main",
		Name:          "禅道",
		Type:          "zentao",
		BaseURL:       srv.URL,
		Account:       "xiaolong",
		AuthMode:      "feishu_sso",
		SessionHeader: "Cookie: zentaosid=expired",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	oldRecapture := recaptureZentaoLoginSession
	recaptureZentaoLoginSession = func(baseURL string) (string, int, error) {
		return "", 0, errors.New("sso expired")
	}
	t.Cleanup(func() { recaptureZentaoLoginSession = oldRecapture })

	_, err = (&App{}).SyncBugPlatform("zentao-main")
	if err == nil {
		t.Fatal("SyncBugPlatform succeeded with expired session")
	}
	if !strings.Contains(err.Error(), "禅道登录授权已失效") {
		t.Fatalf("error = %v", err)
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "" {
		t.Fatalf("session header should be cleared: %q", platform.SessionHeader)
	}
}

func TestSyncBugPlatformRecapturesExpiredSessionAndRetries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Cookie") == "zentaosid=expired" {
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Cookie") != "zentaosid=fresh" {
			t.Fatalf("Cookie = %q", r.Header.Get("Cookie"))
		}
		switch r.URL.Path {
		case "/api.php/v1/bugs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bugs":[{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}]}`))
		case "/api.php/v1/bugs/1842":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID:            "zentao-main",
		Name:          "禅道",
		Type:          "zentao",
		BaseURL:       srv.URL,
		Account:       "xiaolong",
		AuthMode:      "feishu_sso",
		SessionHeader: "Cookie: zentaosid=expired",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	oldRecapture := recaptureZentaoLoginSession
	recaptureZentaoLoginSession = func(baseURL string) (string, int, error) {
		if baseURL != srv.URL {
			t.Fatalf("baseURL = %q", baseURL)
		}
		return "Cookie: zentaosid=fresh", 1, nil
	}
	t.Cleanup(func() { recaptureZentaoLoginSession = oldRecapture })

	result, err := (&App{}).SyncBugPlatform("zentao-main")
	if err != nil {
		t.Fatalf("SyncBugPlatform: %v", err)
	}
	if result.Stored != 1 || result.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", result)
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "Cookie: zentaosid=fresh" {
		t.Fatalf("session header = %q", platform.SessionHeader)
	}
	if requests < 3 {
		t.Fatalf("requests = %d, want initial list + retried list/detail", requests)
	}
}

func TestLoginBugPlatformStoresCapturedSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Cookie") != "zentaosid=abc; lang=zh-cn" {
			t.Fatalf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bugs":[]}`))
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", AuthMode: "feishu_sso", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	old := captureZentaoLoginSession
	captureZentaoLoginSession = func(baseURL string) (string, int, error) {
		if baseURL != srv.URL {
			t.Fatalf("baseURL = %q", baseURL)
		}
		return "Cookie: zentaosid=abc; lang=zh-cn", 2, nil
	}
	t.Cleanup(func() { captureZentaoLoginSession = old })

	got, err := (&App{}).LoginBugPlatform(BugLoginInput{PlatformID: "zentao-main"})
	if err != nil {
		t.Fatalf("LoginBugPlatform: %v", err)
	}
	if got.CookieCount != 2 || got.AuthMode != "feishu_sso" {
		t.Fatalf("result = %+v", got)
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "Cookie: zentaosid=abc; lang=zh-cn" {
		t.Fatalf("session header = %q", platform.SessionHeader)
	}
}

func TestLoginBugPlatformDoesNotSaveUnverifiedSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "login required", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, AuthMode: "feishu_sso", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	old := captureZentaoLoginSession
	captureZentaoLoginSession = func(baseURL string) (string, int, error) {
		return "Cookie: zentaosid=anonymous", 1, nil
	}
	t.Cleanup(func() { captureZentaoLoginSession = old })

	if _, err := (&App{}).LoginBugPlatform(BugLoginInput{PlatformID: "zentao-main"}); err == nil {
		t.Fatal("LoginBugPlatform succeeded with unverified session")
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "" {
		t.Fatalf("session header should not be saved: %q", platform.SessionHeader)
	}
}

func TestLoginBugPlatformClearsOldSessionWhenCaptureFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: "http://zentao.example.com/",
		AuthMode: "feishu_sso", SessionHeader: "Cookie: old=1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	old := captureZentaoLoginSession
	captureZentaoLoginSession = func(baseURL string) (string, int, error) {
		return "", 0, errors.New("login cancelled")
	}
	t.Cleanup(func() { captureZentaoLoginSession = old })

	if _, err := (&App{}).LoginBugPlatform(BugLoginInput{PlatformID: "zentao-main"}); err == nil {
		t.Fatal("LoginBugPlatform succeeded after cancelled login")
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "" {
		t.Fatalf("old session should be cleared: %q", platform.SessionHeader)
	}
}

func TestClearBugPlatformLoginClearsSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: "https://zentao.example.com", Account: "xiaolong",
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=abc", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}

	got, err := (&App{}).ClearBugPlatformLogin(BugLoginInput{PlatformID: "zentao-main"})
	if err != nil {
		t.Fatalf("ClearBugPlatformLogin: %v", err)
	}
	if got.SessionSaved {
		t.Fatalf("result = %+v", got)
	}
	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if platform.SessionHeader != "" {
		t.Fatalf("session header = %q", platform.SessionHeader)
	}
}

func TestDeleteBugPlatformRemovesPlatform(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: "https://zentao.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	_, err = bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "tapd-main", Name: "TAPD", Type: "generic", BaseURL: "https://tapd.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}

	if err := (&App{}).DeleteBugPlatform(BugPlatformDeleteInput{PlatformID: "zentao-main"}); err != nil {
		t.Fatalf("DeleteBugPlatform: %v", err)
	}
	items, err := bugPlatformStore().List()
	if err != nil {
		t.Fatalf("List platforms: %v", err)
	}
	if len(items) != 1 || items[0].ID != "tapd-main" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFetchBugByIDStoresBug(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/bugs/1842" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bug":{"id":"1842","title":"支付页提交后 500","assignedTo":"xiaolong"}}`))
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL, Account: "xiaolong", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}

	got, err := (&App{}).FetchBugByID(BugFetchInput{PlatformID: "zentao-main", BugID: "#1842"})
	if err != nil {
		t.Fatalf("FetchBugByID: %v", err)
	}
	if got.Stored != 1 || got.SelectedBugID != "zentao-1842" {
		t.Fatalf("result = %+v", got)
	}
}

func TestPreviewBugAttachmentReadsLocalImage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	imagePath := filepath.Join(root, "shot.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-718", Source: "zentao", Title: "截图",
		Attachments: []bughub.Attachment{{Name: "shot.png", Type: "image/png", LocalPath: imagePath}},
	}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	got, err := (&App{}).PreviewBugAttachment(BugAttachmentPreviewInput{BugID: "zentao-718", AttachmentIndex: 0})
	if err != nil {
		t.Fatalf("PreviewBugAttachment: %v", err)
	}
	if got.Name != "shot.png" || got.ContentType != "image/png" || !strings.HasPrefix(got.DataURL, "data:image/png;base64,") {
		t.Fatalf("preview = %+v", got)
	}
}

func TestPreviewBugAttachmentCachesRemoteImage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	remoteAvailable := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteAvailable {
			http.Error(w, "gone", http.StatusGone)
			return
		}
		if r.URL.Path != "/api.php/v1/files/101/download" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Cookie") != "zentaosid=abc" {
			t.Fatalf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\ncached"))
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=abc", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-718", Source: "zentao", Title: "截图",
		Attachments: []bughub.Attachment{{ID: "101", Name: "screen.png", Type: "image/png"}},
	}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	got, err := (&App{}).PreviewBugAttachment(BugAttachmentPreviewInput{PlatformID: "zentao-main", BugID: "zentao-718", AttachmentIndex: 0})
	if err != nil {
		t.Fatalf("PreviewBugAttachment: %v", err)
	}
	if got.ContentType != "image/png" || !strings.HasPrefix(got.DataURL, "data:image/png;base64,") {
		t.Fatalf("preview = %+v", got)
	}
	stored, ok, err := bugStore().Get("zentao-718")
	if err != nil || !ok {
		t.Fatalf("Get bug ok=%v err=%v", ok, err)
	}
	localPath := stored.Attachments[0].LocalPath
	if localPath == "" {
		t.Fatalf("attachment was not cached: %+v", stored.Attachments[0])
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("cached file missing: %v", err)
	}

	remoteAvailable = false
	got, err = (&App{}).PreviewBugAttachment(BugAttachmentPreviewInput{PlatformID: "zentao-main", BugID: "zentao-718", AttachmentIndex: 0})
	if err != nil {
		t.Fatalf("PreviewBugAttachment from cache: %v", err)
	}
	if got.ContentType != "image/png" || !strings.HasPrefix(got.DataURL, "data:image/png;base64,") {
		t.Fatalf("cached preview = %+v", got)
	}
}

func TestPreviewBugAttachmentReplacesPoisonedImageCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	poisonedPath := filepath.Join(root, "screen.jpg")
	if err := os.WriteFile(poisonedPath, []byte("<!DOCTYPE html><html>zentao shell</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/files/101" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01cached"))
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "api_token", Token: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-718", Source: "zentao", SourceID: "718", PlatformID: "zentao-main", Title: "截图",
		Attachments: []bughub.Attachment{{
			ID: "101", Name: "screen.jpg", Type: "image/jpeg", LocalPath: poisonedPath,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := (&App{}).PreviewBugAttachment(BugAttachmentPreviewInput{
		PlatformID: "zentao-main", BugID: "zentao-718", AttachmentIndex: 0,
	})
	if err != nil {
		t.Fatalf("PreviewBugAttachment: %v", err)
	}
	if got.ContentType != "image/jpeg" || !strings.HasPrefix(got.DataURL, "data:image/jpeg;base64,") {
		t.Fatalf("preview = %+v", got)
	}
	stored, found, err := bugStore().Get("zentao-718")
	if err != nil || !found {
		t.Fatalf("Get bug found=%v err=%v", found, err)
	}
	if stored.Attachments[0].LocalPath == "" || stored.Attachments[0].LocalPath == poisonedPath {
		t.Fatalf("poisoned cache locator was not replaced: %+v", stored.Attachments[0])
	}
	cached, err := os.ReadFile(stored.Attachments[0].LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(cached, []byte("\xff\xd8\xff")) {
		t.Fatalf("replacement cache is not JPEG: %x", cached[:min(len(cached), 16)])
	}
}

func TestMatchBugBotsUsesConfiguredBotEnvWithoutExpandingBots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	botDir := filepath.Join(root, ".codex", "skills", "base")
	if err := os.MkdirAll(botDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := discover.Meta{
		SchemaVersion: 1,
		SystemID:      "base",
		SystemName:    "Base",
		Target:        "codex",
		TroubleshooterYAML: `system:
  id: base
environments:
  - id: test
  - id: prod
generation:
  targets: [codex]
`,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(botDir, discover.MetaFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-1842", Source: "zentao", SourceID: "1842", Title: "支付页 500", SystemID: "base", Env: "stage", BotEnv: "test"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	matches, err := (&App{}).MatchBugBots("zentao-1842")
	if err != nil {
		t.Fatalf("MatchBugBots: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if len(matches[0].Bot.Envs) != 2 || matches[0].Bot.Envs[0] != "test" || matches[0].Bot.Key == "" {
		t.Fatalf("top bot = %+v", matches[0].Bot)
	}
	if matches[0].Score < 70 {
		t.Fatalf("score = %d, reasons=%+v", matches[0].Score, matches[0].Reasons)
	}
}

func TestMatchBugBotsUsesPerBotPlatformEnvironments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	codexKey := writeDiscoveredBugBot(t, root, "codex")
	claudeKey := writeDiscoveredBugBot(t, root, "claude-code")
	platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
		BotEnv: "legacy-test",
		BotMappings: []bughub.PlatformBotMapping{
			{BotKey: codexKey, Env: "test"},
			{BotKey: claudeKey, Env: "prod"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID: "zentao-1842", PlatformID: platform.ID, Title: "支付页 500", Env: "stage",
	}); err != nil {
		t.Fatal(err)
	}

	matches, err := (&App{}).MatchBugBots("zentao-1842")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, match := range matches {
		got[match.Bot.Key] = match.Bot.Env
	}
	if got[codexKey] != "test" || got[claudeKey] != "prod" {
		t.Fatalf("mapped environments = %+v", got)
	}
}

func TestMatchBugBotsEnvironmentFallbacks(t *testing.T) {
	tests := []struct {
		name            string
		platformPresent bool
		mappingEnv      *string
		botEnv          string
		bugEnv          string
		want            string
	}{
		{
			name: "empty mapping uses Bug.BotEnv", platformPresent: true,
			mappingEnv: stringPointer("  "), botEnv: " legacy-test ", bugEnv: "stage", want: "legacy-test",
		},
		{
			name: "missing mapping uses Bug.Env", platformPresent: true,
			botEnv: "  ", bugEnv: " stage ", want: "stage",
		},
		{
			name: "missing platform uses bug fallback", platformPresent: false,
			botEnv: " test ", bugEnv: "stage", want: "test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			botKey := writeDiscoveredBugBot(t, root, "codex")
			platformID := "zentao-main"
			if tt.platformPresent {
				platform := bughub.PlatformConfig{
					ID: platformID, Name: "Zentao", Type: "zentao", Enabled: true,
				}
				if tt.mappingEnv != nil {
					platform.BotMappings = []bughub.PlatformBotMapping{{BotKey: botKey, Env: *tt.mappingEnv}}
				}
				if _, err := bugPlatformStore().Upsert(platform); err != nil {
					t.Fatal(err)
				}
			}
			if err := bugStore().Upsert(bughub.Bug{
				ID: "zentao-1842", PlatformID: platformID, Title: "支付页 500", BotEnv: tt.botEnv, Env: tt.bugEnv,
			}); err != nil {
				t.Fatal(err)
			}

			matches, err := (&App{}).MatchBugBots("zentao-1842")
			if err != nil {
				t.Fatalf("MatchBugBots: %v", err)
			}
			if len(matches) != 1 || matches[0].Bot.Env != tt.want {
				t.Fatalf("matches = %+v, want env %q", matches, tt.want)
			}
		})
	}
}

func writeDiscoveredBugBot(t *testing.T, root, target string) string {
	t.Helper()
	platformDir := "." + target
	if target == "claude-code" {
		platformDir = ".claude"
	}
	botDir := filepath.Join(root, platformDir, "skills", "base")
	if err := os.MkdirAll(botDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := discover.Meta{
		SchemaVersion: 1,
		SystemID:      "base",
		SystemName:    "Base",
		Target:        target,
		TroubleshooterYAML: `system:
  id: base
environments:
  - id: test
  - id: stage
  - id: prod
generation:
  targets: [` + target + `]
`,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(botDir, discover.MetaFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return botDir + "|" + target
}

func stringPointer(value string) *string {
	return &value
}

func TestSaveBugSelectedBotPersistsChoice(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-1842", Source: "zentao", SourceID: "1842", Title: "支付页 500"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	updated, err := (&App{}).SaveBugSelectedBot(BugSelectedBotInput{
		BugID:  "zentao-1842",
		BotKey: "/Users/me/.codex/agents/base.toml|codex",
	})
	if err != nil {
		t.Fatalf("SaveBugSelectedBot: %v", err)
	}
	if updated.SelectedBotKey != "/Users/me/.codex/agents/base.toml|codex" {
		t.Fatalf("updated = %+v", updated)
	}
	stored, ok, err := bugStore().Get("zentao-1842")
	if err != nil || !ok {
		t.Fatalf("Get stored ok=%v err=%v", ok, err)
	}
	if stored.SelectedBotKey != updated.SelectedBotKey {
		t.Fatalf("stored = %+v", stored)
	}
}
