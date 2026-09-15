package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func officialFixture() *config.SystemConfig {
	cfg := &config.SystemConfig{Environments: []config.Environment{{ID: "dev"}, {ID: "prod"}}}
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{{ID: "ops", Type: "consul", Endpoints: []config.ConfigCenterEndpoint{{Env: "dev", Host: "consul.example:8500", Token: "fixture-token"}}}}
	cfg.Infrastructure.Observability.SkyWalking = config.SkyWalking{Enabled: true, Endpoints: []config.ObsEndpoint{{Env: "dev", URL: "https://oap.example/base", User: "audit", Pass: "fixture-password"}}}
	return cfg
}
func TestBuildOfficialMCPBindings(t *testing.T) {
	cfg := officialFixture()
	opts := MCPBuildOptions{AgentID: "shop", PruneEmpty: true, OfficialMCPBinaryPaths: map[string]string{"consul": "/managed/consul", "skywalking": "/managed/swmcp"}}
	creds := PrefillCredsFromYAML(cfg)
	if creds["SKYWALKING_PASS_DEV"] != "fixture-password" || creds["SKYWALKING_URL_DEV"] != "https://oap.example/base" {
		t.Fatal("missing SkyWalking prefill")
	}
	servers := BuildMCPServers(cfg, opts, func(k string) string { return creds[k] })
	if len(servers) != 2 {
		t.Fatalf("missing endpoints should skip: %v", servers)
	}
	consul := servers["shop-consul-ops-dev"].(map[string]any)
	env := consul["env"].(map[string]any)
	if env["CONSUL_HTTP_ADDR"] != "http://consul.example:8500" || env["CONSUL_HTTP_TOKEN"] != "fixture-token" {
		t.Fatal("Consul source/env mismatch")
	}
	sw := servers["shop-skywalking-dev"].(map[string]any)
	if strings.Contains(argString(sw), "fixture-password") || strings.Contains(argString(sw), "--read-only") {
		t.Fatal("password or hard write restriction in args")
	}
	if sw["env"].(map[string]any)["SKYWALKING_PASS"] != "fixture-password" {
		t.Fatal("password not bound")
	}
	if !strings.Contains(renderCodexMCPSection(servers), "SKYWALKING_PASS") {
		t.Fatal("Codex dropped credentials")
	}
	if _, err := openCodeMCPServers(servers); err != nil {
		t.Fatal(err)
	}
	opts.OfficialMCPBinaryPaths = nil
	if len(BuildMCPServers(cfg, opts, func(string) string { return "" })) != 0 {
		t.Fatal("failed install must keep HTTP fallback without invalid MCP")
	}
	if !usesOfficialMCP(cfg, "consul") || !usesOfficialMCP(cfg, "skywalking") {
		t.Fatal("enabled capabilities not detected")
	}
	cfg.Infrastructure.ConfigCenters = nil
	cfg.Infrastructure.Observability.SkyWalking.Enabled = false
	if usesOfficialMCP(cfg, "consul") || usesOfficialMCP(cfg, "skywalking") || usesOfficialMCP(cfg, "other") {
		t.Fatal("disabled capability detected")
	}
}
func TestOfficialMCPBadEndpointsAndOverrides(t *testing.T) {
	cfg := officialFixture()
	opts := MCPBuildOptions{PruneEmpty: true, OfficialMCPBinaryPaths: map[string]string{"consul": "/managed/consul", "skywalking": "/managed/swmcp"}}
	cfg.Infrastructure.ConfigCenters[0].Endpoints[0].Host = "https://user:secret@example.com"
	cfg.Infrastructure.Observability.SkyWalking.Endpoints[0].URL = "file:///tmp/fixture"
	if len(BuildMCPServers(cfg, opts, func(string) string { return "" })) != 0 {
		t.Fatal("invalid URLs accepted")
	}
	servers := BuildMCPServers(cfg, opts, func(k string) string {
		switch k {
		case "CONSUL_HOST_OPS_DEV":
			return "https://consul.example:8501"
		case "SKYWALKING_URL_DEV":
			return "https://audit:p%40ss@oap.example/graphql"
		}
		return ""
	})
	if len(servers) != 2 {
		t.Fatal("explicit deployment overrides ignored")
	}
	sw := servers["skywalking-dev"].(map[string]any)
	if strings.Contains(argString(sw), "p%40ss") || sw["env"].(map[string]any)["SKYWALKING_PASS"] != "p@ss" {
		t.Fatal("URL credentials leaked or not decoded")
	}
}

// Opt-in protocol check against reviewed binaries, never downloads in unit tests.
func TestOfficialMCPRuntimeLive(t *testing.T) {
	dir := os.Getenv("TSHOOT_LIVE_OFFICIAL_MCP_BIN_DIR")
	if dir == "" {
		t.Skip("explicit local binaries required")
	}
	cfg := officialFixture()
	opts := MCPBuildOptions{PruneEmpty: true, OfficialMCPBinaryPaths: map[string]string{"consul": filepath.Join(dir, "consul-mcp-server"), "skywalking": filepath.Join(dir, "swmcp")}}
	for name, raw := range BuildMCPServers(cfg, opts, func(string) string { return "" }) {
		t.Run(name, func(t *testing.T) {
			spec := raw.(map[string]any)
			args := []string{}
			for _, v := range spec["args"].([]any) {
				args = append(args, v.(string))
			}
			env := []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
			for k, v := range spec["env"].(map[string]any) {
				env = append(env, k+"="+v.(string))
			}
			got := doProbeMCPServer(context.Background(), spec["command"].(string), args, env, 15*time.Second)
			if got.Err != nil || len(got.Tools) == 0 {
				t.Fatalf("protocol failed: %v", got.Err)
			}
			t.Logf("%d tools", len(got.Tools))
		})
	}
}

func TestOfficialMCPInstallFailureAndCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := officialFixture()
	cfg.System.ID = "shop"
	original := ensureOfficialMCP
	t.Cleanup(func() { ensureOfficialMCP = original })
	calls := 0
	ensureOfficialMCP = func(officialMCPRelease, func(string)) (string, error) {
		calls++
		return "", errors.New("fixture download unavailable")
	}
	var logs []string
	creds := PrefillCredsFromYAML(cfg)
	if err := MergeMCPIntoIDESettings("cursor", cfg, creds, func(s string) { logs = append(logs, s) }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(strings.Join(logs, "\n"), "HTTP/API") {
		t.Fatal("missing fallback notice")
	}
	settings, err := readJSONOrEmpty(filepath.Join(home, ".cursor/mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if servers, ok := settings["mcpServers"].(map[string]any); ok && len(servers) > 0 {
		t.Fatal("failed install registered invalid MCP")
	}
	if err := WriteIDECredsFile(cfg, creds); err != nil {
		t.Fatal(err)
	}
	data, err := readJSONOrEmpty(filepath.Join(home, ".tshoot", cfg.ResolveID()+"-creds.json"))
	if err != nil {
		t.Fatal(err)
	}
	sw := data["skywalking"].(map[string]any)["dev"].(map[string]any)
	if sw["pass"] != "fixture-password" || sw["url"] != "https://oap.example/base" {
		t.Fatal("authenticated HTTP fallback lost credentials")
	}
	keys := strings.Join(requiredMCPKeys(cfg, "shop"), ",")
	if consulMCPRelease.hashes[runtime.GOOS+"/"+runtime.GOARCH] != "" && !strings.Contains(keys, "shop-consul-ops-dev") {
		t.Fatal("Consul missing from self-test")
	}
	if skywalkingMCPRelease.hashes[runtime.GOOS+"/"+runtime.GOARCH] != "" && !strings.Contains(keys, "shop-skywalking-dev") {
		t.Fatal("SkyWalking missing from self-test")
	}
	cfg.Infrastructure.ConfigCenters = nil
	cfg.Infrastructure.Observability.SkyWalking.Enabled = false
	if len(requiredMCPKeys(cfg, "shop")) != 0 {
		t.Fatal("disabled official MCP still required")
	}
}
