package bughub

import "testing"

func TestMatchBotsScoresSystemAndEnv(t *testing.T) {
	bug := Bug{SystemID: "shop", Env: "stage", BotEnv: "prod", FrontendRepo: "mall-web", ServiceHints: []string{"pay-service"}}
	bots := []BotRef{
		{Key: "shop", SystemID: "shop", Envs: []string{"test", "prod"}, Name: "shop-troubleshooter"},
		{Key: "crm", SystemID: "crm", Env: "test", Name: "crm-troubleshooter"},
	}

	matches := MatchBots(bug, bots)

	if len(matches) != 2 {
		t.Fatalf("matches len = %d", len(matches))
	}
	if matches[0].Bot.Key != "shop" {
		t.Fatalf("top match = %s", matches[0].Bot.Key)
	}
	if matches[0].Score <= matches[1].Score {
		t.Fatalf("top score %d <= second %d", matches[0].Score, matches[1].Score)
	}
	if len(matches[0].Reasons) == 0 {
		t.Fatal("top match has no reasons")
	}
}

func TestMatchBotsExcludesValidatorByDefault(t *testing.T) {
	bug := Bug{ID: "b1", SystemID: "shop", Env: "test"}
	bots := []BotRef{{Key: "t", SystemID: "shop", Target: "codex", Env: "test"}}

	got := MatchBots(bug, bots)

	if len(got) != 1 || got[0].Bot.Key != "t" {
		t.Fatalf("want bot match, got %+v", got)
	}
}

func TestValidatorBotFor(t *testing.T) {
	selected := BotRef{
		Key:      "/root/skills/shop-troubleshooter|codex",
		SystemID: "shop",
		Target:   "codex",
		Path:     "/root/skills/shop-troubleshooter",
		Env:      "test",
		InternalAgents: []BotInternalAgent{
			{ID: "shop-troubleshooter", Role: "troubleshooter"},
			{ID: "shop-validator", Role: "validator"},
		},
	}

	got := ValidatorBotFor(selected)

	if got.AgentID != "shop-validator" || got.Path != "/root/skills/shop-validator" || got.Env != "test" {
		t.Fatalf("validator bot wrong: %+v", got)
	}
}
