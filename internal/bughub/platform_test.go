package bughub

import "testing"

func TestPlatformStoreUpsertGeneratesSecret(t *testing.T) {
	store := NewPlatformStore(t.TempDir())

	got, err := store.Upsert(PlatformConfig{Name: "禅道", Type: "zentao", Account: "xiaolong", Enabled: true})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.ID == "" {
		t.Fatal("id was not generated")
	}
	if got.HookSecret == "" {
		t.Fatal("hook secret was not generated")
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Account != "xiaolong" {
		t.Fatalf("items = %+v", items)
	}
}

func TestPlatformStoreUpsertDefaultsBackgroundSyncOff(t *testing.T) {
	store := NewPlatformStore(t.TempDir())

	got, err := store.Upsert(PlatformConfig{Name: "禅道", Type: "zentao", Account: "xiaolong", Enabled: true})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.PollEnabled {
		t.Fatalf("poll_enabled = true, want false")
	}
	if got.PollIntervalMinutes != 0 {
		t.Fatalf("poll interval = %d, want 0", got.PollIntervalMinutes)
	}
}

func TestPlatformStoreUpsertNormalizesBotMappings(t *testing.T) {
	store := NewPlatformStore(t.TempDir())

	got, err := store.Upsert(PlatformConfig{
		Name: "禅道", Type: "zentao", Enabled: true,
		BotMappings: []PlatformBotMapping{
			{BotKey: "  /repo/base|codex  ", Env: " prod "},
			{BotKey: "/repo/base|codex", Env: "test"},
			{BotKey: "  ", Env: "dev"},
			{BotKey: "/repo/base|cursor", Env: "test"},
			{BotKey: "/repo/base|claude-code"},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(got.BotMappings) != 2 {
		t.Fatalf("bot mappings = %+v", got.BotMappings)
	}
	if got.BotMappings[0].BotKey != "/repo/base|codex" || got.BotMappings[0].Env != "prod" {
		t.Fatalf("first mapping = %+v", got.BotMappings[0])
	}
	if got.BotMappings[1].BotKey != "/repo/base|claude-code" || got.BotMappings[1].Env != "" {
		t.Fatalf("second mapping = %+v", got.BotMappings[1])
	}
}

func TestPlatformStoreUpsertPreservesSecretWhenInputBlank(t *testing.T) {
	store := NewPlatformStore(t.TempDir())
	first, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", Account: "xiaolong", Token: "token-1", HookSecret: "secret-1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}

	second, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道主库", Type: "zentao", Account: "xiaolong", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if second.Token != "token-1" || second.HookSecret != "secret-1" {
		t.Fatalf("secrets not preserved: %+v", second)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at = %s, want %s", second.CreatedAt, first.CreatedAt)
	}
}

func TestPlatformStoreUpsertPreservesPasswordWhenInputBlank(t *testing.T) {
	store := NewPlatformStore(t.TempDir())
	_, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", Account: "xiaolong", Password: "pw-1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}

	second, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道主库", Type: "zentao", Account: "xiaolong", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if second.Password != "pw-1" {
		t.Fatalf("password not preserved: %+v", second)
	}
}

func TestPlatformStoreUpsertPreservesSessionHeaderWhenInputBlank(t *testing.T) {
	store := NewPlatformStore(t.TempDir())
	_, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", Account: "xiaolong",
		AuthMode: "session_header", SessionHeader: "Cookie: sid=1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}

	second, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道主库", Type: "zentao", Account: "xiaolong",
		AuthMode: "session_header", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if second.SessionHeader != "Cookie: sid=1" {
		t.Fatalf("session header not preserved: %+v", second)
	}
}

func TestPlatformStoreDeleteRemovesOnlySelectedPlatform(t *testing.T) {
	store := NewPlatformStore(t.TempDir())
	_, err := store.Upsert(PlatformConfig{ID: "zentao-main", Name: "禅道", Type: "zentao", Enabled: true})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	_, err = store.Upsert(PlatformConfig{ID: "tapd-main", Name: "TAPD", Type: "generic", Enabled: true})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}

	if err := store.Delete("zentao-main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != "tapd-main" {
		t.Fatalf("items = %+v", items)
	}
	if err := store.Delete("missing"); err == nil {
		t.Fatal("Delete missing platform succeeded")
	}
}

func TestPlatformStoreSetAndClearSessionHeader(t *testing.T) {
	store := NewPlatformStore(t.TempDir())
	_, err := store.Upsert(PlatformConfig{
		ID: "zentao-main", Name: "禅道", Type: "zentao", Account: "xiaolong", AuthMode: "feishu_sso", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}

	withSession, err := store.SetSessionHeader("zentao-main", "feishu_sso", "Cookie: zentaosid=abc")
	if err != nil {
		t.Fatalf("SetSessionHeader: %v", err)
	}
	if withSession.AuthMode != "feishu_sso" || withSession.SessionHeader != "Cookie: zentaosid=abc" {
		t.Fatalf("with session = %+v", withSession)
	}

	cleared, err := store.ClearSessionHeader("zentao-main")
	if err != nil {
		t.Fatalf("ClearSessionHeader: %v", err)
	}
	if cleared.SessionHeader != "" {
		t.Fatalf("session header not cleared: %+v", cleared)
	}
}

func TestBugFromWebhookFiltersAssignee(t *testing.T) {
	platform := PlatformConfig{ID: "zentao-main", Type: "zentao", Account: "xiaolong", Enabled: true}
	payload := []byte(`{"bug":{"id":"1842","title":"支付页 500","assignedTo":"other"}}`)

	_, result, err := BugFromWebhook(platform, payload)
	if err != nil {
		t.Fatalf("BugFromWebhook: %v", err)
	}
	if result.Accepted {
		t.Fatalf("accepted assignee mismatch: %+v", result)
	}
}

func TestBugFromWebhookNormalizesZentaoPayload(t *testing.T) {
	platform := PlatformConfig{ID: "zentao-main", Type: "zentao", Account: "xiaolong", Enabled: true}
	payload := []byte(`{"bug":{"id":"1842","title":"支付页 500","assignedTo":"xiaolong","openedBy":"qa","keywords":"prod,mall-web,pay-service"}}`)

	got, result, err := BugFromWebhook(platform, payload)
	if err != nil {
		t.Fatalf("BugFromWebhook: %v", err)
	}
	if !result.Accepted || result.StoredID != "zentao-main-1842" {
		t.Fatalf("result = %+v", result)
	}
	if got.ID != "zentao-main-1842" || got.Source != "zentao" || got.Assignee != "xiaolong" {
		t.Fatalf("bug = %+v", got)
	}
	if got.Env != "prod" || got.FrontendRepo != "mall-web" || len(got.ServiceHints) != 1 {
		t.Fatalf("derived fields = %+v", got)
	}
}

func TestBugFromWebhookUsesConfiguredPlatformAndBotEnv(t *testing.T) {
	platform := PlatformConfig{ID: "zentao-main", Type: "zentao", Account: "xiaolong", Env: "stage", BotEnv: "test", Enabled: true}
	payload := []byte(`{"bug":{"id":"1842","title":"支付页 500","assignedTo":"xiaolong","keywords":"prod,mall-web"}}`)

	got, result, err := BugFromWebhook(platform, payload)
	if err != nil {
		t.Fatalf("BugFromWebhook: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("result = %+v", result)
	}
	if got.Env != "stage" || got.BotEnv != "test" {
		t.Fatalf("env fields = %+v", got)
	}
}
