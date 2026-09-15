package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/hujson"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func TestOpenCodeNativeInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "custom-config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	staging := setupClaudeStaging(t, "shop-bot")
	if err := os.WriteFile(filepath.Join(staging, "agents/shop-bot.md"), []byte("---\nmode: all\ndescription: Shop\n---\n"+generator.OpenCodeSkillsRoot+"/shop-bot"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := InstallNative(staging, "opencode"); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(xdg, "opencode")
	data, err := os.ReadFile(filepath.Join(root, "agents/shop-bot.md"))
	if err != nil || !strings.Contains(string(data), filepath.ToSlash(filepath.Join(root, "skills/shop-bot"))) {
		t.Fatalf("profile=%s err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills/shop-bot/example-skill/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallNative(filepath.Join(root, "skills/shop-bot"), "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents/shop-bot.md")); !os.IsNotExist(err) {
		t.Fatal("profile remains")
	}
}

func TestOpenCodeMCPMergePreservesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	original := `{ // my settings
 "model":"provider/model", "provider":{"custom":{"name":"Mine"}},
 "mcp":{ /* personal MCP comment */ "personal":{"type":"remote","url":"https://example.invalid"},"shop-old":{"type":"local","command":["old"]},"shop-db":{"type":"local","command":["db"],"environment":{"TOKEN":"saved"}}},
 }`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	converted, err := openCodeMCPServers(map[string]any{"shop-db": map[string]any{"command": "db", "args": []string{"--stdio"}, "env": map[string]any{"TOKEN": "new"}}, "shop-api": map[string]any{"url": "https://api.invalid/mcp", "headers": map[string]any{"Authorization": "new"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateOpenCodeMCP(path, "shop-", converted, true); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "my settings") || !strings.Contains(string(data), "personal MCP comment") {
			t.Fatal("unrelated comment lost")
		}
		data, err = hujson.Standardize(data)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	root := read()
	mcp := root["mcp"].(map[string]any)
	if mcp["shop-db"].(map[string]any)["environment"].(map[string]any)["TOKEN"] != "saved" {
		t.Fatal("credentials replaced")
	}
	removed, err := updateOpenCodeMCP(path, "shop-", converted, false)
	if err != nil || len(removed) != 1 || removed[0] != "shop-old" {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	root = read()
	if root["model"] != "provider/model" || root["provider"] == nil {
		t.Fatal("user settings lost")
	}
	if _, err := updateOpenCodeMCP(path, "shop-", nil, false); err != nil {
		t.Fatal(err)
	}
	if len(read()["mcp"].(map[string]any)) != 1 {
		t.Fatal("uninstall removed unrelated MCP or left owned MCP")
	}
}

func TestOpenCodeMCPInvalidConfigUntouched(t *testing.T) {
	for _, input := range []string{"{broken", `[]`, `null`, `{"mcp":[]}`} {
		path := filepath.Join(t.TempDir(), "opencode.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := updateOpenCodeMCP(path, "shop-", map[string]any{}, false); err == nil {
			t.Fatal("bad config accepted")
		}
		data, _ := os.ReadFile(path)
		if string(data) != input {
			t.Fatal("invalid config overwritten")
		}
	}
	if _, err := openCodeMCPServers(map[string]any{"bad": map[string]any{}}); err == nil {
		t.Fatal("missing command accepted")
	}
}

func TestOpenCodeMCPStoredRuntimeProbe(t *testing.T) {
	old := probeMCPFunc
	t.Cleanup(func() { probeMCPFunc = old })
	calls := 0
	probeMCPFunc = func(_ context.Context, command string, args, env []string, _ time.Duration) MCPProbeResult {
		calls++
		if command != "probe" || len(args) != 1 || args[0] != "--stdio" || !strings.Contains(strings.Join(env, "\n"), "TEST_TOKEN=saved") {
			t.Errorf("wrong stored runtime values")
		}
		return MCPProbeResult{Tools: []string{"read_status"}}
	}
	path := filepath.Join(t.TempDir(), "opencode.json")
	entries := map[string]any{"shop-probe": map[string]any{"type": "local", "command": []string{"probe", "--stdio"}, "environment": map[string]string{"TEST_TOKEN": "saved"}}}
	if _, err := updateOpenCodeMCP(path, "shop-", entries, false); err != nil {
		t.Fatal(err)
	}
	if err := probeOpenCodeMCP(path, "shop-", func(string) {}); err != nil || calls != 1 {
		t.Fatalf("probe calls=%d err=%v", calls, err)
	}
	probeMCPFunc = func(context.Context, string, []string, []string, time.Duration) MCPProbeResult {
		return MCPProbeResult{Err: errors.New("bad secret credential")}
	}
	var logs []string
	err := probeOpenCodeMCP(path, "shop-", func(s string) { logs = append(logs, s) })
	if err == nil || strings.Contains(strings.Join(logs, " ")+err.Error(), "secret") {
		t.Fatal("probe failure missing or sensitive details exposed")
	}
}

func TestOpenCodeMCPConsolidatesOverlappingConfigFiles(t *testing.T) {
	root := t.TempDir()
	lower := filepath.Join(root, "opencode.json")
	higher := filepath.Join(root, "opencode.jsonc")
	if err := os.WriteFile(lower, []byte(`{"model":"keep/model","mcp":{"shop-db":{"type":"local","command":["db"],"environment":{"TOKEN":"preserved"}},"shop-old":{"type":"local","command":["old"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(higher, []byte(`{// keep comment
 "mcp":{"personal":{"type":"local","command":["personal"]},"shop-db":{"environment":{"EXTRA":"overlay"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	fresh := map[string]any{"shop-db": map[string]any{"type": "local", "command": []string{"new-db"}}}
	if _, err := mergeOpenCodeMCP(higher, "shop-", fresh, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(higher)
	standard, _ := hujson.Standardize(data)
	var got map[string]any
	if err := json.Unmarshal(standard, &got); err != nil {
		t.Fatal(err)
	}
	db := got["mcp"].(map[string]any)["shop-db"].(map[string]any)
	if db["command"].([]any)[0] != "db" || len(db["environment"].(map[string]any)) != 2 {
		t.Fatal("effective credential overlay not preserved")
	}
	removed, err := mergeOpenCodeMCP(higher, "shop-", nil, false)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	for _, file := range []string{lower, higher} {
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), "shop-") {
			t.Fatal("old config resurrects removed MCP")
		}
	}
}
