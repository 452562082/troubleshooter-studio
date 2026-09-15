package bughub

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeCommandUsesInstalledProfileAndStdin(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "skills", "shop-troubleshooter")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "shop-troubleshooter.md"), []byte("---\nmode: all\nmodel: provider/model\ndescription: Shop\n---\nShop-only instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"provider/default","share":"auto","agent":{"other":{"description":"preserved"}}}`)
	prompt := "secret prompt with `quotes` and $HOME"
	cmd, err := BuildOpenCodeInvestigationCommand("/bin/echo", BotRef{Path: work, AgentID: "shop-troubleshooter"}, prompt, []string{"/tmp/with spaces.png"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(cmd.Stdin)
	if string(data) != prompt || strings.Contains(strings.Join(cmd.Args, " "), "secret") {
		t.Fatal("prompt missing or leaked to argv")
	}
	if cmd.Dir != work || !strings.Contains(strings.Join(cmd.Args, " "), "--file /tmp/with spaces.png") {
		t.Fatal(cmd.Args)
	}
	var config map[string]any
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
				t.Fatal(err)
			}
		}
	}
	profile := config["agent"].(map[string]any)["shop-troubleshooter"].(map[string]any)
	if profile["mode"] != "primary" || profile["model"] != "provider/model" || !strings.Contains(profile["prompt"].(string), "Shop-only") || config["share"] != "disabled" || config["model"] != "provider/default" {
		t.Fatal(config)
	}
	if config["agent"].(map[string]any)["other"] == nil {
		t.Fatal("unrelated profile lost")
	}
}

func TestOpenCodeParserRequiresSuccessfulFinalStep(t *testing.T) {
	for _, tc := range []struct {
		name, finish string
		valid        bool
	}{
		{"stop", `{"type":"step_finish","part":{"reason":"stop"}}`, true},
		{"length", `{"type":"step_finish","part":{"reason":"length"}}`, false},
		{"tools", `{"type":"step_finish","part":{"reason":"tool-calls"}}`, false},
		{"error", `{"type":"error","error":{"data":{"message":"provider unavailable"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := newOpenCodeStreamJSONParser()
			_, final, _ := parser([]byte(`{"type":"text","part":{"text":"phase: investigation"}}`))
			if final != "" {
				t.Fatal("premature final")
			}
			_, final, _ = parser([]byte(tc.finish))
			if (final != "") != tc.valid {
				t.Fatalf("final=%q", final)
			}
		})
	}
	parser := newOpenCodeStreamJSONParser()
	e, _, _ := parser([]byte(`{"type":"tool_use","part":{"tool":"read","state":{"status":"completed","input":"secret","output":"secret"}}}`))
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "secret") {
		t.Fatal("tool payload exposed")
	}
	_, _, failure := parser([]byte(`{"type":"step_finish","part":{"reason":"stop"}}`))
	if failure == "" {
		t.Fatal("empty final accepted")
	}
}

func TestOpenCodeCommandRejectsInvalidInputs(t *testing.T) {
	for _, id := range []string{"../other", "/tmp/other", "a\\b"} {
		if _, err := BuildOpenCodeInvestigationCommand("/bin/echo", BotRef{Path: t.TempDir(), AgentID: id}, "prompt", nil); err == nil {
			t.Fatal(id)
		}
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", "{invalid")
	if _, err := BuildOpenCodeInvestigationCommand("/bin/echo", BotRef{Path: t.TempDir()}, "prompt", nil); err == nil {
		t.Fatal("invalid config accepted")
	}
}
