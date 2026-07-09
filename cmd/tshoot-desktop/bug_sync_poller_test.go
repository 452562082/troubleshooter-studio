package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestShouldPollBugPlatformRequiresExplicitPollEnabled(t *testing.T) {
	platform := bughub.PlatformConfig{Enabled: true, Type: "zentao", PollIntervalMinutes: 5}

	if shouldPollBugPlatform(platform, time.Now(), time.Time{}) {
		t.Fatal("poll should be disabled unless poll_enabled is true")
	}
}

func TestShouldPollBugPlatformUsesConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 7, 3, 18, 30, 0, 0, time.UTC)
	platform := bughub.PlatformConfig{
		Enabled: true, Type: "zentao", PollEnabled: true, PollIntervalMinutes: 10,
	}

	if shouldPollBugPlatform(platform, now, now.Add(-9*time.Minute)) {
		t.Fatal("poll ran before configured interval elapsed")
	}
	if !shouldPollBugPlatform(platform, now, now.Add(-10*time.Minute)) {
		t.Fatal("poll did not run after configured interval elapsed")
	}
}

func TestShouldKeepAliveBugPlatformRequiresCapturedSession(t *testing.T) {
	now := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	platform := bughub.PlatformConfig{Enabled: true, Type: "zentao", AuthMode: "feishu_sso"}

	if shouldKeepAliveBugPlatform(platform, now, time.Time{}) {
		t.Fatal("keepalive should require saved captured session")
	}
	platform.SessionHeader = "Cookie: zentaosid=abc"
	if !shouldKeepAliveBugPlatform(platform, now, time.Time{}) {
		t.Fatal("keepalive should run for saved captured session")
	}
	if shouldKeepAliveBugPlatform(platform, now, now.Add(-bugKeepAliveInterval+time.Second)) {
		t.Fatal("keepalive ran before interval elapsed")
	}
}

func TestKeepAliveBugPlatformRecapturesExpiredSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php/v1/user" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Cookie") == "zentaosid=expired" {
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Cookie") != "zentaosid=fresh" {
			t.Fatalf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"profile":{"account":"xiaolong"}}`))
	}))
	defer srv.Close()
	_, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", BaseURL: srv.URL,
		AuthMode: "feishu_sso", SessionHeader: "Cookie: zentaosid=expired", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	oldRecapture := recaptureZentaoLoginSession
	recaptureZentaoLoginSession = func(baseURL string) (string, int, error) {
		return "Cookie: zentaosid=fresh", 1, nil
	}
	t.Cleanup(func() { recaptureZentaoLoginSession = oldRecapture })

	platform, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get platform ok=%v err=%v", ok, err)
	}
	if err := keepAliveBugPlatform(platform); err != nil {
		t.Fatalf("keepAliveBugPlatform: %v", err)
	}
	refreshed, ok, err := bugPlatformStore().Get("zentao-main")
	if err != nil || !ok {
		t.Fatalf("Get refreshed platform ok=%v err=%v", ok, err)
	}
	if refreshed.SessionHeader != "Cookie: zentaosid=fresh" {
		t.Fatalf("session header = %q", refreshed.SessionHeader)
	}
}
