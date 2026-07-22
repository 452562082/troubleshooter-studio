package bughub

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestResolveFrontendEntryUsesExplicitDeepLink(t *testing.T) {
	entries := []config.FrontendEntry{
		{ID: "consumer", Name: "C 端", URL: "https://m.test/", Repo: "consumer-web"},
		{ID: "admin", Name: "管理端", URL: "https://admin.test/console", Repo: "admin-web"},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://admin.test/console/users/42"}, "")
	if err != nil || resolution.Selected == nil {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	if resolution.Selected.ID != "admin" || resolution.Selected.URL != "https://admin.test/console/users/42" || resolution.Selected.ResolutionSource != "ticket_url" {
		t.Fatalf("selected=%+v", resolution.Selected)
	}
}

func TestResolveFrontendEntryCanonicalizesSafeTicketQuery(t *testing.T) {
	entries := []config.FrontendEntry{{ID: "consumer", Name: "C 端", URL: "https://m.test/"}}
	resolution, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "HTTPS://M.TEST:443/search?q=chengzi"}, "")
	if err != nil || resolution.Selected == nil || resolution.Selected.URL != "https://m.test/search?q=chengzi" {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	if _, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://token@m.test/search"}, ""); err == nil {
		t.Fatal("expected credential-bearing ticket URL to be rejected")
	}
}

func TestResolveFrontendEntryDoesNotAuthorizeUnconfiguredTicketOrigin(t *testing.T) {
	entries := []config.FrontendEntry{{ID: "consumer", Name: "C 端", URL: "https://m.test/"}}
	resolution, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://unconfigured.test/search"}, "")
	if err != nil || resolution.Status != FrontendResolutionAmbiguous || resolution.Selected != nil || len(resolution.Candidates) != 1 {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	selected, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://unconfigured.test/search"}, "consumer")
	if err != nil || selected.Selected == nil || selected.Selected.URL != "https://m.test/" || selected.Selected.ResolutionSource != "user" {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}

func TestResolveFrontendEntryUsesMostSpecificConfiguredPath(t *testing.T) {
	entries := []config.FrontendEntry{
		{ID: "consumer", Name: "C 端", URL: "https://portal.test/", Repo: "consumer-web"},
		{ID: "admin", Name: "管理端", URL: "https://portal.test/admin", Repo: "admin-web"},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://portal.test/admin/users/42"}, "")
	if err != nil || resolution.Selected == nil {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	if resolution.Selected.ID != "admin" || resolution.Selected.ResolutionSource != "ticket_url" {
		t.Fatalf("selected=%+v", resolution.Selected)
	}
}

func TestResolveFrontendEntryUsesDeclaredPathPrefixOnSharedOrigin(t *testing.T) {
	entries := []config.FrontendEntry{
		{ID: "consumer", Name: "C 端", URL: "https://portal.test/", PathPrefixes: []string{"/shop", "/account"}},
		{ID: "admin", Name: "管理端", URL: "https://portal.test/", PathPrefixes: []string{"/admin", "/console"}},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{FrontendURL: "https://portal.test/console/users"}, "")
	if err != nil || resolution.Selected == nil {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	if resolution.Selected.ID != "admin" || resolution.Selected.ResolutionSource != "ticket_signals" || resolution.Selected.Score != 60 {
		t.Fatalf("selected=%+v", resolution.Selected)
	}
}

func TestResolveFrontendEntryUsesConfiguredTicketSignals(t *testing.T) {
	entries := []config.FrontendEntry{
		{ID: "consumer", Name: "C 端 H5", URL: "https://m.test", Repo: "consumer-web", DeviceProfile: "mobile", Aliases: []string{"用户端"}, ProductHints: []string{"商城"}},
		{ID: "admin", Name: "管理端", URL: "https://admin.test", Repo: "admin-web", DeviceProfile: "desktop", Aliases: []string{"运营后台"}, ProductHints: []string{"运营"}},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{Title: "运营后台用户管理异常", Product: "运营"}, "")
	if err != nil || resolution.Selected == nil || resolution.Selected.ID != "admin" || resolution.Selected.ResolutionSource != "ticket_signals" {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
}

func TestResolveFrontendEntryRequiresChoiceWhenSignalsTie(t *testing.T) {
	entries := []config.FrontendEntry{
		{ID: "consumer", Name: "C 端", URL: "https://m.test"},
		{ID: "admin", Name: "管理端", URL: "https://admin.test"},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{Title: "用户名展示错误"}, "")
	if err != nil || resolution.Status != FrontendResolutionAmbiguous || len(resolution.Candidates) != 2 {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
	selected, err := ResolveFrontendEntry(entries, Bug{Title: "用户名展示错误"}, "admin")
	if err != nil || selected.Selected == nil || selected.Selected.ID != "admin" || selected.Selected.ResolutionSource != "user" {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}

func TestResolveFrontendEntryUsesScreenshotOrientationOnlyAsSupportingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 400, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 400; x++ {
			canvas.Set(x, y, color.White)
		}
	}
	if err := png.Encode(file, canvas); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	entries := []config.FrontendEntry{
		{ID: "mobile", Name: "手机端", URL: "https://m.test", DeviceProfile: "mobile"},
		{ID: "desktop", Name: "桌面端", URL: "https://admin.test", DeviceProfile: "desktop"},
	}
	resolution, err := ResolveFrontendEntry(entries, Bug{Attachments: []Attachment{{Name: "截图.png", LocalPath: path}}}, "")
	if err != nil || resolution.Status != FrontendResolutionAmbiguous || resolution.Candidates[0].Binding.ID != "mobile" || resolution.Candidates[0].Score != 12 {
		t.Fatalf("resolution=%+v err=%v", resolution, err)
	}
}
