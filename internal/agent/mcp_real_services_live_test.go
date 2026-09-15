package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// The fixture and report directory are explicit: this test never installs
// services or reads the user's deployed credentials. Data assertions are made
// separately by calling tools from the exported production builder output.
func TestMCPRealServicesProbe(t *testing.T) {
	path, out := os.Getenv("TSHOOT_LIVE_MCP_FIXTURE"), os.Getenv("TSHOOT_LIVE_MCP_REPORT_DIR")
	if path == "" || out == "" {
		t.Skip("explicit disposable services fixture and private report directory required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Config      config.SystemConfig
		Binaries    map[string]string
		Credentials map[string]string
		Expected    []string
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	servers := BuildMCPServers(&fixture.Config, MCPBuildOptions{PruneEmpty: true, OfficialMCPBinaryPaths: fixture.Binaries}, func(k string) string { return fixture.Credentials[k] })
	if len(servers) != len(fixture.Expected) {
		t.Fatalf("got %d servers, want %d", len(servers), len(fixture.Expected))
	}
	for _, name := range fixture.Expected {
		t.Run(name, func(t *testing.T) {
			spec, ok := servers[name].(map[string]any)
			if !ok {
				t.Fatal("expected server not built")
			}
			var result MCPProbeResult
			if endpoint, ok := spec["url"].(string); ok {
				result = doProbeMCPHTTPServer(context.Background(), endpoint, spec["headers"].(map[string]string), 15*time.Second)
			} else {
				args := []string{}
				for _, v := range spec["args"].([]any) {
					args = append(args, v.(string))
				}
				env := []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
				for _, k := range []string{"UV_CACHE_DIR", "UV_PYTHON", "UV_PYTHON_DOWNLOADS", "UV_OFFLINE", "npm_config_cache", "npm_config_offline"} {
					if v := os.Getenv(k); v != "" {
						env = append(env, k+"="+v)
					}
				}
				for k, v := range spec["env"].(map[string]any) {
					env = append(env, k+"="+v.(string))
				}
				result = doProbeMCPServer(context.Background(), spec["command"].(string), args, env, 45*time.Second)
			}
			if result.Err != nil || len(result.Tools) == 0 {
				t.Fatalf("runtime probe failed: %v", result.Err)
			}
			t.Logf("runtime tools/list: %d tools", len(result.Tools))
		})
	}
	if t.Failed() {
		return
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err = json.MarshalIndent(servers, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "servers.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
