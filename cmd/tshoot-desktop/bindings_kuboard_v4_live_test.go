package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
)

// Explicit opt-in; the fixture must reference a disposable Kuboard + Kubernetes
// instance containing acceptance/audit-config with fixture_key=fixture-value.
func TestKuboardV4Real(t *testing.T) {
	path := os.Getenv("TSHOOT_LIVE_KUBOARD_FIXTURE")
	if path == "" {
		t.Skip("disposable Kuboard fixture required")
	}
	var fixture struct{ URL, Username, Password, AccessKey string }
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background()}
	for _, useKey := range []bool{false, true} {
		user, pass, key := fixture.Username, fixture.Password, ""
		if useKey {
			user, pass, key = "", "", fixture.AccessKey
		}
		res, err := app.KuboardListResources(fixture.URL, user, pass, key, "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, cluster := range res.Clusters {
			for _, ns := range cluster.Namespaces {
				for _, name := range ns.ConfigMaps {
					if ns.Name == "acceptance" && name == "audit-config" {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatal("seeded ConfigMap missing from desktop resource tree")
		}
		if useKey && res.MCPURL == "" {
			t.Fatal("native MCP discovery failed")
		}
	}
	if _, err := app.KuboardListResources(fixture.URL, fixture.Username, "invalid-password", "", ""); err == nil {
		t.Fatal("invalid login accepted")
	}
	if endpoint := agent.DiscoverKuboardMCP(context.Background(), fixture.URL, "invalid.key"); endpoint != "" {
		t.Fatal("invalid MCP credential accepted")
	}
	t.Log("desktop login/key resource reads and native MCP discovery passed; invalid credentials rejected")
}
