package bughub

import (
	"strings"
	"testing"
)

func TestGenerateContextIncludesBugAndBot(t *testing.T) {
	bug := Bug{
		Source: "zentao", SourceID: "1842", Title: "支付页提交后 500",
		Env: "stage", BotEnv: "prod", FrontendURL: "https://mall.example.com/checkout",
		Steps:       "1. 打开支付页\n2. 点击提交",
		APIPaths:    []string{"/api/pay/submit"},
		TraceIDs:    []string{"trace-1"},
		Attachments: []Attachment{{Name: "network.har", LocalPath: "/tmp/network.har"}},
	}
	bot := BotRef{Key: "/bots/shop|openclaw", SystemID: "shop", Target: "openclaw", Path: "/bots/shop", Env: "test"}

	ctx := GenerateContext(bug, bot)

	for _, want := range []string{
		"# Bug 排障上下文",
		"zentao:1842",
		"支付页提交后 500",
		"stage",
		"test",
		"/api/pay/submit",
		"trace-1",
		"network.har",
		"shop",
		"openclaw",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx)
		}
	}
}

func TestGenerateContextUsesBotEnvWhenBugEnvMissing(t *testing.T) {
	ctx := GenerateContext(Bug{Title: "分类计数错误"}, BotRef{Target: "codex", Env: "test"})
	if !strings.Contains(ctx, "- 环境: test") || !strings.Contains(ctx, "- 排障机器人环境: test") {
		t.Fatalf("context should use bot env fallback:\n%s", ctx)
	}
}
