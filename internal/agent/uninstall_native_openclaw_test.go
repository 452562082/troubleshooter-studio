package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallNativeOpenclaw_HappyPath(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	wsDir := filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")
	if _, err := os.Stat(wsDir); err != nil {
		t.Fatal("pre-condition: workspace should exist after install")
	}

	res, err := UninstallNativeOpenclaw(staging)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// workspace 目录不存在了
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace 应已被搬走, err=%v", err)
	}
	// 应被搬到 ~/.Trash/<id>-troubleshooter-workspace-uninstall-<ts>/
	if !strings.Contains(res.WorkspaceMovedTo, "shop-troubleshooter-workspace-uninstall-") {
		t.Errorf("WorkspaceMovedTo unexpected: %s", res.WorkspaceMovedTo)
	}
	if _, err := os.Stat(res.WorkspaceMovedTo); err != nil {
		t.Errorf("Trash 备份不存在: %v", err)
	}

	// agents.list 里的 shop-troubleshooter 已摘
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	for _, a := range getList(data, "agents", "list") {
		if m, ok := a.(map[string]any); ok && m["id"] == "shop-troubleshooter" {
			t.Errorf("agents.list 里 shop-troubleshooter 应被摘掉, got %+v", a)
		}
	}
	if !res.OpenclawJSONClean {
		t.Errorf("OpenclawJSONClean want true(我们刚 install 过)")
	}

	// MCP servers 不动(可能跨 agent 共享)
	servers := getMap(data, "mcp", "servers")
	if _, ok := servers["shop-nacos-mcp-server-dev"]; !ok {
		t.Errorf("MCP servers 不应被卸载清掉(共享资源),但 nacos-mcp-server-dev 没了")
	}
}

func TestUninstallNativeOpenclaw_NoWorkspaceStillCleansJSON(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 提前手动删 workspace,模拟"用户已经 rm 了"
	wsDir := filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")
	if err := os.RemoveAll(wsDir); err != nil {
		t.Fatal(err)
	}

	res, err := UninstallNativeOpenclaw(staging)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.WorkspaceMovedTo != "" {
		t.Errorf("workspace 不存在时不该有 WorkspaceMovedTo, got %s", res.WorkspaceMovedTo)
	}
	if !res.OpenclawJSONClean {
		t.Errorf("openclaw.json 应仍被清理")
	}
}

func TestUninstallNativeOpenclaw_AgentNotInJSONIsSkip(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	// 不调 install,直接装一个不含本 agent 的 openclaw.json
	cfgDir := filepath.Join(fakeHome, ".openclaw")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "openclaw.json"),
		[]byte(`{"agents":{"list":[{"id":"someone-else"}]},"mcp":{"servers":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := UninstallNativeOpenclaw(staging)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.OpenclawJSONClean {
		t.Errorf("agent 不在 openclaw.json 里时,OpenclawJSONClean 应为 false(语义=没动)")
	}
	// 别人的 agent 不动
	data := readJSON(t, filepath.Join(cfgDir, "openclaw.json"))
	agents := getList(data, "agents", "list")
	if len(agents) != 1 {
		t.Errorf("不该乱删别的 agent, agents.list len want 1 got %d", len(agents))
	}
}

func TestUninstallNativeOpenclaw_RemovesCredsJSON(t *testing.T) {
	// apollo 类型会写 creds.json,卸载应清掉
	cfg := nacosCfg()
	cfg.Infrastructure.ConfigCenter.Type = "apollo"
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{
		SkipGatewayRestart: true,
		Creds: map[string]string{
			"APOLLO_META_DEV": "http://x", "APOLLO_TOKEN": "tk",
			"APOLLO_META_PROD": "http://y",
		},
	}); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(fakeHome, ".openclaw", "shop-troubleshooter-creds.json")
	if _, err := os.Stat(credsPath); err != nil {
		t.Fatalf("pre: creds.json should exist after install: %v", err)
	}

	res, err := UninstallNativeOpenclaw(staging)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CredsRemoved {
		t.Errorf("CredsRemoved want true")
	}
	if _, err := os.Stat(credsPath); !os.IsNotExist(err) {
		t.Errorf("creds.json 应被删掉, err=%v", err)
	}
}
