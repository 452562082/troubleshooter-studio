package mcpcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath_AllTargets(t *testing.T) {
	home, _ := os.UserHomeDir()
	project := "/tmp/myproj"

	cases := []struct {
		target     Target
		project    string
		wantPath   string
		wantScope  Scope
		wantNested bool
	}{
		{TargetOpenClaw, "", filepath.Join(home, ".openclaw", "openclaw.json"), ScopeUser, true},
		{TargetOpenClaw, project, filepath.Join(home, ".openclaw", "openclaw.json"), ScopeUser, true}, // projectRoot 忽略
		{TargetClaudeCode, project, filepath.Join(project, ".mcp.json"), ScopeProject, false},
		{TargetClaudeCode, "", filepath.Join(home, ".claude.json"), ScopeUser, false},
		{TargetCursor, project, filepath.Join(project, ".cursor", "mcp.json"), ScopeProject, false},
		{TargetCursor, "", filepath.Join(home, ".cursor", "mcp.json"), ScopeUser, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.target)+"/"+tc.project, func(t *testing.T) {
			r, err := ResolvePath(tc.target, tc.project)
			if err != nil {
				t.Fatal(err)
			}
			if r.Path != tc.wantPath {
				t.Errorf("Path: got %q want %q", r.Path, tc.wantPath)
			}
			if r.Scope != tc.wantScope {
				t.Errorf("Scope: got %q want %q", r.Scope, tc.wantScope)
			}
			if r.NestedUnderMCP != tc.wantNested {
				t.Errorf("NestedUnderMCP: got %v want %v", r.NestedUnderMCP, tc.wantNested)
			}
		})
	}
}

func TestMergeWrite_CreateNew_ClaudeSchema(t *testing.T) {
	dir := t.TempDir()
	resolved, err := ResolvePath(TargetClaudeCode, dir)
	if err != nil {
		t.Fatal(err)
	}
	servers := map[string]Server{
		"nacos-mcp-server": {
			Command: "uvx",
			Args:    []string{"nacos-mcp-router@latest"},
			Env:     map[string]string{"NACOS_ADDR": "1.2.3.4:8848"},
		},
	}
	if err := MergeWrite(resolved, servers); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resolved.Path)
	if err != nil {
		t.Fatal(err)
	}
	// claude-code / cursor:顶层 "mcpServers"
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	mcp, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing; full content:\n%s", string(raw))
	}
	entry, ok := mcp["nacos-mcp-server"].(map[string]any)
	if !ok {
		t.Fatalf("server missing: %v", mcp)
	}
	if entry["command"] != "uvx" {
		t.Errorf("command: %v", entry["command"])
	}
}

func TestMergeWrite_CreateNew_OpenclawSchema(t *testing.T) {
	// openclaw 用 nested mcp.servers,Path 是固定的 ~/.openclaw/openclaw.json;
	// 走 HOME override 写到 tmp 测
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	resolved, err := ResolvePath(TargetOpenClaw, "")
	if err != nil {
		t.Fatal(err)
	}
	servers := map[string]Server{
		"nacos": {
			Command: "uvx",
			Args:    []string{"nacos-mcp-router@latest"},
		},
	}
	if err := MergeWrite(resolved, servers); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resolved.Path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// 顶层 "mcp" 下嵌套 "servers"
	mcpBlock, ok := doc["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing:\n%s", string(raw))
	}
	serversBlock, ok := mcpBlock["servers"].(map[string]any)
	if !ok {
		t.Fatal("mcp.servers missing")
	}
	if _, ok := serversBlock["nacos"]; !ok {
		t.Error("nacos server missing")
	}
}

// 关键:合并时不能破坏用户手配的其它 MCP server / 其它顶层字段
func TestMergeWrite_PreservesOtherEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	// 预置一个文件,已含用户手配的 pencil + 一些无关顶层字段
	preExisting := `{
  "mcpServers": {
    "pencil": {
      "command": "/Applications/Pencil.app/Contents/Resources/app.asar.unpacked/out/mcp-server-darwin-arm64"
    }
  },
  "otherToolConfig": {"theme": "dark"}
}`
	if err := os.WriteFile(path, []byte(preExisting), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved := &Resolved{
		Target:         TargetClaudeCode,
		Scope:          ScopeProject,
		Path:           path,
		NestedUnderMCP: false,
	}
	servers := map[string]Server{
		"nacos-mcp-server": {Command: "uvx", Args: []string{"nacos-mcp-router@latest"}},
	}
	if err := MergeWrite(resolved, servers); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)

	mcp := doc["mcpServers"].(map[string]any)
	if _, ok := mcp["pencil"]; !ok {
		t.Error("pencil was wiped; MergeWrite must preserve user-configured MCP entries")
	}
	if _, ok := mcp["nacos-mcp-server"]; !ok {
		t.Error("new server missing")
	}
	if _, ok := doc["otherToolConfig"]; !ok {
		t.Error("otherToolConfig (unrelated top-level) was wiped")
	}
}

// 同名 server 覆盖(本工具重跑应替换之前写过的)
func TestMergeWrite_OverridesSameKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	pre := `{"mcpServers": {"nacos": {"command": "old-cmd", "args": ["v1"]}}}`
	if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved := &Resolved{Target: TargetClaudeCode, Path: path, NestedUnderMCP: false}
	if err := MergeWrite(resolved, map[string]Server{
		"nacos": {Command: "new-cmd", Args: []string{"v2"}},
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"new-cmd"`) {
		t.Errorf("expected override, got:\n%s", string(raw))
	}
	if strings.Contains(string(raw), `"old-cmd"`) {
		t.Errorf("old value should be gone, got:\n%s", string(raw))
	}
}
