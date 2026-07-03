package analyzerpipe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestRunScansAPIRoutesIntoReport(t *testing.T) {
	reposRoot := t.TempDir()
	repoDir := filepath.Join(reposRoot, "order-service")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module order-service\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "handler.go"), []byte(`
package main

func main() {
	r.GET("/api/orders/:id", handler)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Repos: []config.Repo{{
			Name:         "order-service",
			Stack:        "go",
			Role:         config.RoleBackend,
			ServiceNames: []string{"order-service"},
		}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
		},
	}

	result, err := Run(context.Background(), cfg, Options{ReposRoot: reposRoot})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Report.Repos) != 1 {
		t.Fatalf("report repos = %#v", result.Report.Repos)
	}
	routes := result.Report.Repos[0].APIRoutes
	if len(routes) != 1 {
		t.Fatalf("APIRoutes = %#v", routes)
	}
	if routes[0].Path != "/api/orders/:id" || routes[0].Method != "GET" {
		t.Fatalf("route = %#v", routes[0])
	}
	if len(result.PerRepo) != 1 || result.PerRepo[0].Status != "analyzed" {
		t.Fatalf("per repo = %#v", result.PerRepo)
	}
}
