package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 把上一个 install 测试 setup 的"先 install 再 self-test"流程链起来,
// 确认装完即过自检。HOST 探活用空 NACOS_ADDR / GRAFANA_URL → 都给 WARN,不算 FAIL。
func TestSelfTestOpenclaw_AfterInstallAllPass(t *testing.T) {
	cfg := nacosCfg()
	staging, _ := setupOpenclawStaging(t, cfg)

	// 不传 creds → addr/url 为空,后面 nacos TCP / grafana HTTP 都会 WARN 跳过,不影响 OK
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Errorf("self-test 不应有 FAIL,got: %+v", res.Checks)
	}
	// 至少要 PASS 三项关键检查
	statusByName := map[string]string{}
	for _, c := range res.Checks {
		statusByName[c.Name] = c.Status
	}
	wantPass := []string{
		"workspace 目录",
		"agents.list 含 shop-troubleshooter",
		"mcp.servers 齐全(4 项)", // nacos x2 + grafana x2
	}
	for _, name := range wantPass {
		if statusByName[name] != "PASS" {
			t.Errorf("expected %q PASS, got %q", name, statusByName[name])
		}
	}
}

func TestSelfTestOpenclaw_MissingWorkspace(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 删掉 workspace 目录
	if err := os.RemoveAll(filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("缺 workspace 应当 FAIL")
	}
	found := false
	for _, c := range res.Checks {
		if c.Name == "workspace 目录" && c.Status == "FAIL" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("应有 'workspace 目录' FAIL 项, got: %+v", res.Checks)
	}
}

func TestSelfTestOpenclaw_MissingMCPServer(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 手动从 openclaw.json 删掉一个 MCP server
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	servers := getMap(data, "mcp", "servers")
	delete(servers, "grafana-mcp-server-prod")
	mb, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(cfgPath, mb, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("缺 MCP server 应当 FAIL")
	}
	for _, c := range res.Checks {
		if c.Name == "mcp.servers 齐全" && c.Status == "FAIL" {
			return
		}
	}
	t.Errorf("应有 'mcp.servers 齐全' FAIL 项, got: %+v", res.Checks)
}

func TestSelfTestOpenclaw_AgentMissing(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 把 agents.list 清空
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	agents, _ := data["agents"].(map[string]any)
	agents["list"] = []any{}
	mb, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(cfgPath, mb, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("agents.list 缺 agent 应当 FAIL")
	}
}
