package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestBuildMCPServersKuboard(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, key string
		want                bool
	}{
		{"native", "https://kuboard.example/mcp", "key.secret", true},
		{"legacy", "", "legacy-key", false},
		{"missing-key", "https://kuboard.example/mcp", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.SystemConfig{Environments: []config.Environment{{ID: "dev"}}}
			cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{{ID: "ops", Type: "kuboard", Endpoints: []config.ConfigCenterEndpoint{{Env: "dev", URL: "https://kuboard.example", MCPURL: tc.endpoint, AccessKey: tc.key}}}}
			servers := BuildMCPServers(cfg, MCPBuildOptions{AgentID: "shop", PruneEmpty: true}, func(string) string { return "" })
			spec, ok := servers["shop-kuboard-ops-dev"].(map[string]any)
			if ok != tc.want {
				t.Fatalf("registered=%v, want %v", ok, tc.want)
			}
			if ok {
				if spec["url"] != tc.endpoint || spec["headers"].(map[string]string)["Authorization"] != "Bearer "+tc.key {
					t.Fatal("wrong native endpoint or credentials")
				}
				if !reflect.DeepEqual(requiredMCPKeys(cfg, "shop"), []string{"shop-kuboard-ops-dev"}) {
					t.Fatal("missing self-test expectation")
				}
				creds := PrefillCredsFromYAML(cfg)
				if creds["KUBOARD_MCP_URL_OPS_DEV"] != tc.endpoint {
					t.Fatal("endpoint missing from deployment credentials")
				}
			}
		})
	}
}

func TestDiscoverKuboardMCP(t *testing.T) {
	old := probeMCPHTTPFunc
	t.Cleanup(func() { probeMCPHTTPFunc = old })
	for _, tc := range []struct {
		name   string
		result MCPProbeResult
		want   bool
	}{
		{"native", MCPProbeResult{Tools: []string{"list_k8s"}}, true},
		{"http-only", MCPProbeResult{Err: errors.New("HTTP 404")}, false},
		{"unauthorized", MCPProbeResult{Err: errors.New("HTTP 401")}, false},
		{"empty-tools", MCPProbeResult{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			probeMCPHTTPFunc = func(_ context.Context, u string, h map[string]string, _ time.Duration) MCPProbeResult {
				called = true
				if u != "https://kb.example/base/mcp" || h["Authorization"] != "Bearer key.secret" {
					t.Fatal("wrong probe request")
				}
				return tc.result
			}
			got := DiscoverKuboardMCP(context.Background(), "https://kb.example/base/", "key.secret")
			if !called || (got != "") != tc.want {
				t.Fatalf("discovery=%q", got)
			}
			called = false
			for _, u := range []string{"file:///tmp/server", "https://user:password@kb.example", "invalid"} {
				if DiscoverKuboardMCP(context.Background(), u, "key.secret") != "" || called {
					t.Fatal("invalid URL must not send credentials")
				}
			}
			if DiscoverKuboardMCP(context.Background(), "https://kb.example/base", "") != "" || called {
				t.Fatal("must not discover without persistent key")
			}
		})
	}
}

func TestOfficialMCPPackages(t *testing.T) {
	cfg := &config.SystemConfig{Environments: []config.Environment{{ID: "dev"}}}
	cfg.Infrastructure.Observability.Grafana.Enabled = true
	cfg.Infrastructure.DataStores = []config.DataStore{{Type: "redis", Enabled: true}}
	servers := BuildMCPServers(cfg, MCPBuildOptions{PruneEmpty: true}, func(k string) string {
		if k == "REDIS_URL_DEV" {
			return "rediss://user:p%40ss@redis.example:6380/2?ssl_ca_certs=/tmp/ca.pem"
		}
		return ""
	})
	if !CfgUsesUvx(cfg) {
		t.Fatal("official packages need uv runtime check")
	}
	for _, name := range []string{"redis-dev", "grafana-dev"} {
		s := servers[name].(map[string]any)
		if s["command"] != "uvx" || strings.Contains(argString(s), "p%40ss") {
			t.Fatalf("invalid command for %s", name)
		}
	}
	cfg.Infrastructure.Observability.Grafana.Enabled = false
	cfg.Infrastructure.DataStores[0].Enabled = false
	if CfgUsesUvx(cfg) {
		t.Fatal("disabled packages should not require uv")
	}
}

func TestKuboardRuntimeKeepsIndependentCredentials(t *testing.T) {
	cfg := &config.SystemConfig{Environments: []config.Environment{{ID: "dev"}}}
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{{ID: "runtime", Type: "kuboard", Endpoints: []config.ConfigCenterEndpoint{{Env: "dev", MCPURL: "https://config.example/mcp", AccessKey: "config-key"}}}}
	cfg.Infrastructure.Observability.K8sRuntime = config.K8sRuntime{Enabled: true, Provider: "kuboard", Endpoints: []config.ObsEndpoint{{Env: "dev", MCPURL: "https://runtime.example/mcp", AccessKey: "runtime-key"}}}
	creds := PrefillCredsFromYAML(cfg)
	if creds["KUBOARD_MCP_URL_DEV"] != "https://runtime.example/mcp" || creds["KUBOARD_ACCESS_KEY_DEV"] != "runtime-key" {
		t.Fatal("runtime credentials lost when a named config source exists")
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{AgentID: "shop", PruneEmpty: true}, func(k string) string { return creds[k] })
	if len(servers) != 2 {
		t.Fatalf("config and runtime must not collide: %v", servers)
	}
	for name, want := range map[string]string{"shop-kuboard-runtime-dev": "https://config.example/mcp", "shop-k8s-kuboard-dev": "https://runtime.example/mcp"} {
		if servers[name].(map[string]any)["url"] != want {
			t.Fatalf("wrong endpoint for %s", name)
		}
	}
	cfg.Infrastructure.Observability.K8sRuntime.Provider = "one2all"
	for _, ep := range kuboardMCPEndpoints(cfg) {
		if ep.runtime {
			t.Fatal("one2all must not register Kuboard")
		}
	}
	cfg.Infrastructure.Observability.K8sRuntime.Provider = "kuboard"
	cfg.Infrastructure.ConfigCenters[0].ID = "default"
	seen := map[string]bool{}
	for _, p := range DerivePrompts(cfg) {
		if seen[p.Name] {
			t.Fatalf("duplicate prompt %s", p.Name)
		}
		seen[p.Name] = true
	}
}
