package agent

import (
	"slices"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestRequiredMCPKeysPreservesSharedRegistry(t *testing.T) {
	cfg, err := config.Load("../../examples/shop-troubleshooter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID: "shop", NacosMCPScriptPath: "/test/nacos_mcp.py",
	}, func(string) string { return "fixture" })
	for _, key := range requiredMCPKeys(cfg, "shop") {
		if _, ok := servers[key]; !ok {
			t.Errorf("required MCP %s missing from shared builder", key)
		}
	}
	if !slices.Contains(requiredMCPKeys(cfg, "shop"), "shop-grafana-dev") {
		t.Fatal("shared observability requirements were dropped")
	}
	for _, typ := range []string{"rabbitmq", "feishu_project", "unknown"} {
		if dataStoreRegistersMCP(typ) {
			t.Errorf("retired or unsupported MCP %s unexpectedly required", typ)
		}
	}
}
