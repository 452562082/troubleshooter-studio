// install_native_mcp_prune_test.go —— writeMCPServersWithVerify 的 agentPrefix 死 key 清理回归。
//
// 重灌场景四类:
//   - env 缩容(prod 删了)
//   - 数据层启用列表缩(mongodb 不要了)
//   - multi-source nacos 删源
//   - system.id 改名 → 老前缀整套
// 都该被这条逻辑收掉,且不动用户手加的别名(其它前缀)。
//
// mergeOnlyNew=true(无 creds 兜底)路径**不该**清,因为没 creds 无法判断完整意图。

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path string, m map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestWriteMCPServers_PrunesDeadKeysByPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// 模拟上次部署留下的 settings,含:
	//  - bot-grafana-dev / bot-grafana-prod(老 cfg 有 prod env)
	//  - bot-mongodb-dev(老 cfg 启了 mongodb)
	//  - bot-nacos-old-dev(老 multi-source nacos 删了 source)
	//  - other-mcp(用户手加的不同前缀别名,该保留)
	//  - oldsystem-grafana-dev(更老的 system.id 改名前留下,前缀不匹配,保留 — 用户手清)
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"bot-grafana-dev":    map[string]any{"command": "npx"},
			"bot-grafana-prod":   map[string]any{"command": "npx"},
			"bot-mongodb-dev":    map[string]any{"command": "npx"},
			"bot-nacos-old-dev":  map[string]any{"command": "uvx"},
			"other-mcp":          map[string]any{"command": "user-defined"},
			"oldsystem-grafana-dev": map[string]any{"command": "npx"},
		},
	})

	// 本次重灌只生成 bot-grafana-dev(prod / mongodb / nacos-old 都该被清)
	servers := map[string]any{
		"bot-grafana-dev": map[string]any{"command": "npx", "args": []any{"-y", "mcp-grafana-npx"}},
	}

	if err := writeMCPServersWithVerify(path, servers, 1, false, "bot-"); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := readSettings(t, path)["mcpServers"].(map[string]any)

	// 该留的:本次新 spec + 不同前缀的别名
	for _, k := range []string{"bot-grafana-dev", "other-mcp", "oldsystem-grafana-dev"} {
		if _, ok := got[k]; !ok {
			t.Errorf("expected %q to be kept, got %v", k, settingsKeys(got))
		}
	}
	// 该清的:同前缀但本次不再生成的死 key
	for _, k := range []string{"bot-grafana-prod", "bot-mongodb-dev", "bot-nacos-old-dev"} {
		if _, ok := got[k]; ok {
			t.Errorf("expected %q to be pruned (前缀属于本系统但 cfg 不再生成), got %v", k, settingsKeys(got))
		}
	}
}

// TestWriteMCPServers_MergeOnlyNew_PreservesEverything 验证 mergeOnlyNew=true(无 creds)
// 路径**不**清死 key — 没完整意图时清就是误删。
func TestWriteMCPServers_MergeOnlyNew_PreservesEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"bot-grafana-dev":  map[string]any{"command": "npx"},
			"bot-mongodb-dev":  map[string]any{"command": "npx"}, // 该保留 — 没 creds 无法判断意图
		},
	})
	servers := map[string]any{
		"bot-newdb-dev": map[string]any{"command": "npx"},
	}
	if err := writeMCPServersWithVerify(path, servers, 1, true, "bot-"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readSettings(t, path)["mcpServers"].(map[string]any)
	for _, k := range []string{"bot-grafana-dev", "bot-mongodb-dev", "bot-newdb-dev"} {
		if _, ok := got[k]; !ok {
			t.Errorf("mergeOnlyNew=true 下 %q 该保留(没 creds 不该清),got %v", k, settingsKeys(got))
		}
	}
}

// TestWriteMCPServers_NoPrefix_NoPruning 空 prefix 关闭清理(向后兼容,以防未来有需要)。
func TestWriteMCPServers_NoPrefix_NoPruning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"bot-grafana-prod": map[string]any{"command": "npx"},
		},
	})
	servers := map[string]any{
		"bot-grafana-dev": map[string]any{"command": "npx"},
	}
	if err := writeMCPServersWithVerify(path, servers, 1, false, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readSettings(t, path)["mcpServers"].(map[string]any)
	if _, ok := got["bot-grafana-prod"]; !ok {
		t.Errorf("agentPrefix='' 下不该清死 key,got %v", settingsKeys(got))
	}
}

func settingsKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
