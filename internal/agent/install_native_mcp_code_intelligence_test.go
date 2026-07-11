package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestMergeMCPIntoIDESettings_CodeGraphEnsureFailureIsNonBlocking(t *testing.T) {
	oldGOOS := codeGraphGOOS
	codeGraphGOOS = "unsupported"
	t.Cleanup(func() { codeGraphGOOS = oldGOOS })

	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"shop-codegraph": map[string]any{"command": "/stale/codegraph"},
		},
	})
	cfg := &config.SystemConfig{
		System:           config.System{ID: "shop"},
		CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: "codegraph"},
	}
	var logs []string
	if err := MergeMCPIntoIDESettings("cursor", cfg, nil, func(line string) {
		logs = append(logs, line)
	}); err != nil {
		t.Fatalf("MergeMCPIntoIDESettings() error = %v", err)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "CodeGraph 安装失败,跳过 MCP 注册并启用 rg/read fallback") {
		t.Fatalf("missing CodeGraph fallback warning in logs: %q", got)
	}
	root := readJSON(t, settingsPath)
	servers, _ := root["mcpServers"].(map[string]any)
	if _, exists := servers["shop-codegraph"]; exists {
		t.Fatalf("CodeGraph server registered after ensure failure: %#v", servers)
	}
}

func TestMergeMCPIntoIDESettings_CodeGraphDisabledRemovesStaleServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, settingsPath, map[string]any{
		"mcpServers": map[string]any{
			"shop-codegraph": map[string]any{"command": "/stale/codegraph"},
		},
	})

	cfg := &config.SystemConfig{System: config.System{ID: "shop"}}
	if err := MergeMCPIntoIDESettings("cursor", cfg, nil, nil); err != nil {
		t.Fatalf("MergeMCPIntoIDESettings() error = %v", err)
	}
	root := readJSON(t, settingsPath)
	servers, _ := root["mcpServers"].(map[string]any)
	if _, exists := servers["shop-codegraph"]; exists {
		t.Fatalf("disabled CodeGraph left stale server registered: %#v", servers)
	}
}
