package bughub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchBotsScoresSystemAndEnv(t *testing.T) {
	bug := Bug{SystemID: "shop", Env: "stage", BotEnv: "prod", FrontendRepo: "mall-web", ServiceHints: []string{"pay-service"}}
	bots := []BotRef{
		{Key: "shop", SystemID: "shop", Target: "codex", Envs: []string{"test", "prod"}, Name: "shop-troubleshooter"},
		{Key: "crm", SystemID: "crm", Target: "claude-code", Env: "test", Name: "crm-troubleshooter"},
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

func TestMatchBotsExcludesTargetsThatCannotRunIncidentWorkflow(t *testing.T) {
	bug := Bug{ID: "b1", SystemID: "shop", Env: "test"}
	bots := []BotRef{
		{Key: "codex", SystemID: "shop", Target: "codex", Env: "test"},
		{Key: "cursor", SystemID: "shop", Target: "cursor", Env: "test"},
	}

	got := MatchBots(bug, bots)

	if len(got) != 1 || got[0].Bot.Key != "codex" {
		t.Fatalf("want only incident-capable bot, got %+v", got)
	}
}

func TestValidatorBotFor(t *testing.T) {
	root := t.TempDir()
	troubleshooterPath := filepath.Join(root, "skills", "shop-troubleshooter")
	validatorPath := filepath.Join(root, "skills", "shop-validator")
	if err := os.MkdirAll(validatorPath, 0o755); err != nil {
		t.Fatal(err)
	}
	selected := BotRef{
		Key:      troubleshooterPath + "|codex",
		SystemID: "shop",
		Target:   "codex",
		Path:     troubleshooterPath,
		Env:      "test",
		InternalAgents: []BotInternalAgent{
			{ID: "shop-troubleshooter", Role: "troubleshooter"},
			{ID: "shop-validator", Role: "validator"},
		},
	}

	got := ValidatorBotFor(selected)

	if got.AgentID != "shop-validator" || got.Path != validatorPath || got.Env != "test" {
		t.Fatalf("validator bot wrong: %+v", got)
	}
}

func TestFixerBotForUsesFixerWhenInstalled(t *testing.T) {
	root := t.TempDir()
	troubleshooterPath := filepath.Join(root, "skills", "shop-troubleshooter")
	fixerPath := filepath.Join(root, "skills", "shop-fixer")
	if err := os.MkdirAll(fixerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	selected := BotRef{
		Key:      troubleshooterPath + "|codex",
		SystemID: "shop",
		Target:   "codex",
		Path:     troubleshooterPath,
		Env:      "test",
		InternalAgents: []BotInternalAgent{
			{ID: "shop-troubleshooter", Role: "troubleshooter"},
			{ID: "shop-fixer", Role: "fixer"},
		},
	}

	got := FixerBotFor(selected)

	if got.Role != "fixer" || got.AgentID != "shop-fixer" || got.Path != fixerPath || got.Key != selected.Key+"#fixer" {
		t.Fatalf("fixer bot wrong: %+v", got)
	}
}

func TestFixerBotForFallsBackToSelectedPathWhenOldInstallLacksFixer(t *testing.T) {
	root := t.TempDir()
	troubleshooterPath := filepath.Join(root, "skills", "shop-troubleshooter")
	if err := os.MkdirAll(troubleshooterPath, 0o755); err != nil {
		t.Fatal(err)
	}
	selected := BotRef{Key: "k", SystemID: "shop", Target: "codex", Path: troubleshooterPath}

	got := FixerBotFor(selected)

	if got.Role != "fixer" || got.AgentID != "shop-fixer" || got.Path != troubleshooterPath {
		t.Fatalf("fixer fallback wrong: %+v", got)
	}
}
