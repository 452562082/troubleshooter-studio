package analyzerpipe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestRun_ServiceTopologyBuildsThreeRepositoryChain(t *testing.T) {
	repoPaths, cfg := serviceTopologyFixture(t)

	result, err := Run(context.Background(), cfg, Options{RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Topology.SchemaVersion != "1" {
		t.Fatalf("schema version = %q, want 1", result.Topology.SchemaVersion)
	}
	if len(result.Topology.Services) != 3 {
		t.Fatalf("services=%#v", result.Topology.Services)
	}
	assertTopologyService(t, result.Topology, "mall-web", "mall-web", config.RoleFrontend)
	assertTopologyService(t, result.Topology, "mall-bff", "mall-bff", config.RoleGateway)
	assertTopologyService(t, result.Topology, "mall-order", "mall-order", config.RoleBackend)
	assertTopologyServiceMetadata(t, result.Topology, "mall-web",
		[]string{"mall-web", "mall-web.default.svc", "mall-web.svc", "mall-web.svc.cluster.local"},
		[]string{"mall.test"})
	assertTopologyServiceMetadata(t, result.Topology, "mall-bff",
		[]string{"mall-bff", "mall-bff.default.svc", "mall-bff.svc", "mall-bff.svc.cluster.local"},
		[]string{"api.mall.test"})
	if len(result.Topology.Edges) != 2 {
		t.Fatalf("edges=%#v", result.Topology.Edges)
	}
	assertTopologyEdge(t, result.Topology, "mall-web", "mall-bff")
	assertTopologyEdge(t, result.Topology, "mall-bff", "mall-order")
	assertTopologyEdgeStatus(t, result.Topology, "mall-web", "mall-bff", "confirmed")
	for _, edge := range result.Topology.Edges {
		if len(edge.Reasons) == 0 {
			t.Fatalf("edge lacks evidence: %#v", edge)
		}
	}
	for _, repo := range result.Report.Repos {
		if len(repo.Endpoints) == 0 {
			t.Fatalf("report repo %q lost endpoint findings: %#v", repo.Name, repo)
		}
	}
}

func TestRun_ServiceTopologyKeepsValidEdgesWhenRepositoryIsMissing(t *testing.T) {
	repoPaths, cfg := serviceTopologyFixture(t)
	repoPaths["mall-web"] = filepath.Join(t.TempDir(), "missing-web")

	result, err := Run(context.Background(), cfg, Options{RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertTopologyRepositoryState(t, result.Topology, "mall-web", "failed")
	assertTopologyEdge(t, result.Topology, "mall-bff", "mall-order")
}

func serviceTopologyFixture(t *testing.T) (map[string]string, *config.SystemConfig) {
	t.Helper()
	root := t.TempDir()
	web := filepath.Join(root, "mall-web")
	bff := filepath.Join(root, "mall-bff")
	order := filepath.Join(root, "mall-order")
	writeTopologyFixtureFile(t, filepath.Join(web, "package.json"), `{"name":"mall-web","dependencies":{"axios":"1.0.0"}}`)
	writeTopologyFixtureFile(t, filepath.Join(web, "src", "orders.ts"), `axios.get("https://mall-bff/api/orders")`)
	writeTopologyFixtureFile(t, filepath.Join(bff, "composer.json"), `{}`)
	writeTopologyFixtureFile(t, filepath.Join(bff, "routes.php"), `
<?php
Route::get("/api/orders", fn () => []);
Http::post("http://mall-order/internal/orders", []);
`)
	writeTopologyFixtureFile(t, filepath.Join(order, "go.mod"), "module example.test/mall-order\n\ngo 1.22\n")
	writeTopologyFixtureFile(t, filepath.Join(order, "handler.go"), `
package main

func routes(r *Router) {
	r.POST("/internal/orders", handler)
}
`)

	cfg := &config.SystemConfig{
		Environments: []config.Environment{{
			ID: "dev", Aliases: []string{"development"},
			APIDomain: "api.mall.test", WebDomain: "mall.test",
		}},
		Repos: []config.Repo{
			{Name: "mall-web", Stack: "node", Role: config.RoleFrontend, ServiceNames: []string{"mall-web"}},
			{Name: "mall-bff", Stack: "php", Role: config.RoleGateway, ServiceNames: []string{"mall-bff"}},
			{Name: "mall-order", Stack: "go", Role: config.RoleBackend},
			{Name: "architecture-docs", Stack: "markdown", Role: config.RoleDocs},
		},
		ServiceTopology: config.ServiceTopology{Overrides: []config.ServiceTopologyOverride{{
			Action: "confirm", FromService: "mall-web", ToService: "mall-bff",
			Protocol: "http", Method: "GET", Path: "/api/orders",
		}}},
		Infrastructure: config.Infrastructure{ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}}},
	}
	return map[string]string{"mall-web": web, "mall-bff": bff, "mall-order": order}, cfg
}

func writeTopologyFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTopologyService(t *testing.T, snapshot topology.Snapshot, repo, service, role string) {
	t.Helper()
	for _, descriptor := range snapshot.Services {
		if descriptor.Repo == repo && descriptor.Service == service {
			if descriptor.Role != role || len(descriptor.Aliases) == 0 {
				t.Fatalf("service descriptor=%#v", descriptor)
			}
			return
		}
	}
	t.Fatalf("missing service %s/%s in %#v", repo, service, snapshot.Services)
}

func assertTopologyEdge(t *testing.T, snapshot topology.Snapshot, from, to string) {
	t.Helper()
	for _, edge := range snapshot.Edges {
		if edge.FromService == from && edge.ToService == to {
			return
		}
	}
	t.Fatalf("missing edge %s -> %s in %#v", from, to, snapshot.Edges)
}

func assertTopologyServiceMetadata(t *testing.T, snapshot topology.Snapshot, service string, aliases, hosts []string) {
	t.Helper()
	for _, descriptor := range snapshot.Services {
		if descriptor.Service != service {
			continue
		}
		if !reflect.DeepEqual(descriptor.Aliases, aliases) || !reflect.DeepEqual(descriptor.Hosts, hosts) {
			t.Fatalf("service %q metadata=%#v, want aliases=%#v hosts=%#v", service, descriptor, aliases, hosts)
		}
		return
	}
	t.Fatalf("missing service %q in %#v", service, snapshot.Services)
}

func assertTopologyEdgeStatus(t *testing.T, snapshot topology.Snapshot, from, to, status string) {
	t.Helper()
	for _, edge := range snapshot.Edges {
		if edge.FromService == from && edge.ToService == to {
			if edge.Status != status {
				t.Fatalf("edge %s -> %s status=%q, want %q: %#v", from, to, edge.Status, status, edge)
			}
			return
		}
	}
	t.Fatalf("missing edge %s -> %s in %#v", from, to, snapshot.Edges)
}

func assertTopologyRepositoryState(t *testing.T, snapshot topology.Snapshot, repo, state string) {
	t.Helper()
	for _, status := range snapshot.Repositories {
		if status.Repo == repo {
			if status.State != state {
				t.Fatalf("repository %q state=%q, want %q: %#v", repo, status.State, state, status)
			}
			return
		}
	}
	t.Fatalf("missing repository %q in %#v", repo, snapshot.Repositories)
}

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

func TestRunReturnsContextCanceledBeforeRepoScan(t *testing.T) {
	reposRoot := t.TempDir()
	cfg := &config.SystemConfig{
		Repos: []config.Repo{{
			Name:  "order-service",
			Stack: "go",
			Role:  config.RoleBackend,
		}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, cfg, Options{ReposRoot: reposRoot})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
}
