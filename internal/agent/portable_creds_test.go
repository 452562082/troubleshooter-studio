package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestPrefillCredsFromYAMLConfigCenterOne2AllGlobalEndpoint(t *testing.T) {
	cfg := &config.SystemConfig{
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{
				ID: "one2all", Type: "one2all",
				Endpoints: []config.ConfigCenterEndpoint{{
					URL: "http://one2all/mcp/hash", Token: "portable-token",
				}},
			}},
		},
	}

	got := PrefillCredsFromYAML(cfg)
	if got["ONE2ALL_MCP_URL"] != "http://one2all/mcp/hash" || got["ONE2ALL_TOKEN"] != "portable-token" {
		t.Fatalf("global one2all endpoint was not restored: %#v", got)
	}
}

func TestPortableK8sRuntimeKuboardCredentialsReachInstallAndCredsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential file permission and home lookup semantics are Unix-specific")
	}
	cfg := &config.SystemConfig{
		System:       config.System{ID: "portable"},
		Agent:        config.Agent{ID: "portable-bot"},
		Environments: []config.Environment{{ID: "test"}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
			Observability: config.Observability{
				K8sRuntime: config.K8sRuntime{
					Enabled:  true,
					Provider: "kuboard",
					Endpoints: []config.ObsEndpoint{{
						Env: "test", URL: "https://kuboard.test",
						AccessKey: "access-key", Username: "admin", Password: "secret",
					}},
				},
			},
		},
	}

	prefill := PrefillCredsFromYAML(cfg)
	for key, want := range map[string]string{
		"KUBOARD_URL_TEST":        "https://kuboard.test",
		"KUBOARD_ACCESS_KEY_TEST": "access-key",
		"KUBOARD_USER_TEST":       "admin",
		"KUBOARD_PASS_TEST":       "secret",
	} {
		if prefill[key] != want {
			t.Fatalf("%s = %q, want %q; all=%#v", key, prefill[key], want, prefill)
		}
	}

	promptNames := map[string]bool{}
	for _, prompt := range DerivePrompts(cfg) {
		promptNames[prompt.Name] = true
	}
	for _, key := range []string{"KUBOARD_URL_TEST", "KUBOARD_ACCESS_KEY_TEST", "KUBOARD_USER_TEST", "KUBOARD_PASS_TEST"} {
		if !promptNames[key] {
			t.Fatalf("missing independent k8s runtime prompt %s", key)
		}
	}

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	if err := WriteIDECredsFile(cfg, prefill); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fakeHome, ".tshoot", "portable-bot-creds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]map[string]map[string]string
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if got := root["kuboard"]["test"]; got["url"] != "https://kuboard.test" || got["access_key"] != "access-key" {
		t.Fatalf("runtime kuboard creds file mismatch: %#v", root)
	}
}
