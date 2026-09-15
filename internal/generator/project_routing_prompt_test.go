package generator

import (
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func testProjectRoutingContext() *Context {
	cfg := &config.SystemConfig{}
	cfg.System.ID = "shop"
	cfg.System.Name = "商城"
	cfg.Agent.ID = "shop-troubleshooter"
	return &Context{SystemConfig: cfg, AgentID: cfg.ResolveID()}
}

func TestCodexBusinessAgentsRequireProjectRouter(t *testing.T) {
	ctx := testProjectRoutingContext()
	for _, role := range internalAgentRoles() {
		agentName := agentIDForRole(ctx, role)
		description := buildCodexAgentDescription(ctx, 0, role)
		if !strings.Contains(description, "tshoot-router") || !strings.Contains(description, agentName) {
			t.Fatalf("%s description lacks exact router binding: %s", role, description)
		}
		if strings.Contains(description, "触发词:") {
			t.Fatalf("%s still advertises generic keyword routing: %s", role, description)
		}

		instructions := buildCodexDeveloperInstructions("", ctx, agentName, role)
		for _, want := range []string{"tshoot-router/scripts/resolve.py", "--expect-agent", agentName, "allowed=true", "--system \"shop\""} {
			if !strings.Contains(instructions, want) {
				t.Fatalf("%s instructions missing %q:\n%s", role, want, instructions)
			}
		}
	}
}

func TestOtherIDEAgentsCarryProjectOwnershipGate(t *testing.T) {
	ctx := testProjectRoutingContext()
	claude := claudeProjectOwnershipGate(ctx, "shop-troubleshooter")
	for _, want := range []string{"~/.claude/skills/tshoot-router", "--expect-agent", "shop-troubleshooter", "unmatched"} {
		if !strings.Contains(claude, want) {
			t.Fatalf("Claude gate missing %q:\n%s", want, claude)
		}
	}
	cursor := cursorProjectOwnershipGate(ctx, "shop-troubleshooter")
	for _, want := range []string{"~/.cursor/skills/tshoot-router", "--expect-agent", "不得猜选"} {
		if !strings.Contains(cursor, want) {
			t.Fatalf("Cursor gate missing %q:\n%s", want, cursor)
		}
	}
}
