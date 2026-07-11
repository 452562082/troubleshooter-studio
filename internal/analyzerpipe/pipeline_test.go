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
		[]string{"mall-web", "mall-web.web-ns", "mall-web.web-ns.svc", "mall-web.web-ns.svc.cluster.local"},
		[]string{"mall.test"})
	assertTopologyServiceMetadata(t, result.Topology, "mall-bff",
		[]string{"mall-bff", "mall-bff.edge-ns", "mall-bff.edge-ns.svc", "mall-bff.edge-ns.svc.cluster.local"},
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

func TestRun_ServiceTopologyActivationUsesRunnableLocalServiceRepositories(t *testing.T) {
	root := t.TempDir()
	caller := filepath.Join(root, "caller")
	empty := filepath.Join(root, "empty")
	writeTopologyFixtureFile(t, filepath.Join(caller, "package.json"), `{"name":"caller"}`)
	writeTopologyFixtureFile(t, filepath.Join(caller, "client.ts"), `fetch("http://empty/api/orders")`)
	writeTopologyFixtureFile(t, filepath.Join(empty, "go.mod"), "module example.test/empty\n\ngo 1.22\n")

	base := &config.SystemConfig{
		Repos: []config.Repo{
			{Name: "caller", Stack: "node", Role: config.RoleBackend, ServiceNames: []string{"caller"}},
			{Name: "empty", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"empty"}},
		},
		ServiceTopology: config.ServiceTopology{Overrides: []config.ServiceTopologyOverride{
			{Action: "add", FromService: "caller", ToService: "empty", Protocol: "http", Method: "GET", Path: "/manual"},
			{Action: "confirm", FromService: "caller", ToService: "empty", Protocol: "http", Method: "GET", Path: "/stale"},
		}},
		Infrastructure: config.Infrastructure{ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}}},
	}

	t.Run("two runnable repos merge overrides even when one emits no endpoints", func(t *testing.T) {
		result, err := Run(context.Background(), base, Options{RepoPaths: map[string]string{"caller": caller, "empty": empty}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		assertTopologyEdgeWithPathStatus(t, result.Topology, "caller", "empty", "/manual", "manual")
		assertTopologyEdgeWithPathStatus(t, result.Topology, "caller", "empty", "/stale", "stale")
	})

	t.Run("one runnable repo skips topology matching and overrides", func(t *testing.T) {
		cfg := *base
		cfg.Repos = append([]config.Repo(nil), base.Repos[0])
		result, err := Run(context.Background(), &cfg, Options{RepoPaths: map[string]string{"caller": caller}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(result.Topology.Edges) != 0 {
			t.Fatalf("single runnable service repo produced edges: %#v", result.Topology.Edges)
		}
	})
}

func TestRun_ServiceTopologyDuplicatesMultiServiceEndpointsWithoutBlankOwnership(t *testing.T) {
	root := t.TempDir()
	platform := filepath.Join(root, "platform")
	peer := filepath.Join(root, "peer")
	writeTopologyFixtureFile(t, filepath.Join(platform, "go.mod"), "module example.test/platform\n\ngo 1.22\n")
	writeTopologyFixtureFile(t, filepath.Join(platform, "handler.go"), `
package main

func routes(r *Router) {
	r.GET("/health", handler)
}
`)
	writeTopologyFixtureFile(t, filepath.Join(peer, "go.mod"), "module example.test/peer\n\ngo 1.22\n")

	cfg := &config.SystemConfig{
		Repos: []config.Repo{
			{Name: "platform", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"orders", "payments"}},
			{Name: "peer", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"peer"}},
		},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
			Observability: config.Observability{K8sRuntime: config.K8sRuntime{ServiceMap: []config.K8sRuntimeServiceMapEntry{
				{Env: "dev", Service: "orders", Namespace: "orders-ns"},
				{Env: "dev", Service: "payments", Namespace: "payments-ns"},
			}}},
		},
	}

	result, err := Run(context.Background(), cfg, Options{RepoPaths: map[string]string{"platform": platform, "peer": peer}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Topology.Endpoints) != 2 {
		t.Fatalf("multi-service endpoints=%#v, want one copy per configured service", result.Topology.Endpoints)
	}
	services := map[string]int{}
	ids := map[string]string{}
	for _, endpoint := range result.Topology.Endpoints {
		if endpoint.Service == "" {
			t.Fatalf("endpoint kept blank service ownership: %#v", endpoint)
		}
		if endpoint.ID != endpoint.SemanticID() {
			t.Fatalf("endpoint ID was not recomputed after service assignment: %#v", endpoint)
		}
		services[endpoint.Service]++
		if previousService, exists := ids[endpoint.ID]; exists {
			t.Fatalf("multi-service endpoint ID %q is shared by %q and %q", endpoint.ID, previousService, endpoint.Service)
		}
		ids[endpoint.ID] = endpoint.Service
	}
	if !reflect.DeepEqual(services, map[string]int{"orders": 1, "payments": 1}) {
		t.Fatalf("endpoint service copies=%#v", services)
	}
	assertTopologyServiceMetadata(t, result.Topology, "orders",
		[]string{"orders", "orders.orders-ns", "orders.orders-ns.svc", "orders.orders-ns.svc.cluster.local"}, []string{})
	assertTopologyServiceMetadata(t, result.Topology, "payments",
		[]string{"payments", "payments.payments-ns", "payments.payments-ns.svc", "payments.payments-ns.svc.cluster.local"}, []string{})
	for _, descriptor := range result.Topology.Services {
		if descriptor.Repo == "platform" && containsString(descriptor.Aliases, "platform") {
			t.Fatalf("multi-service repo alias leaked to every service: %#v", descriptor)
		}
	}
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
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{ID: "nacos", Type: "nacos"}},
			Observability: config.Observability{K8sRuntime: config.K8sRuntime{ServiceMap: []config.K8sRuntimeServiceMapEntry{
				{Env: "dev", Service: "mall-web", Namespace: "web-ns"},
				{Env: "dev", Service: "mall-bff", Namespace: "edge-ns"},
			}}},
		},
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

func assertTopologyEdgeWithPathStatus(t *testing.T, snapshot topology.Snapshot, from, to, path, status string) {
	t.Helper()
	for _, edge := range snapshot.Edges {
		if edge.FromService == from && edge.ToService == to && edge.Path == path {
			if edge.Status != status {
				t.Fatalf("edge %s -> %s path=%q status=%q, want %q: %#v", from, to, path, edge.Status, status, edge)
			}
			return
		}
	}
	t.Fatalf("missing edge %s -> %s path=%q in %#v", from, to, path, snapshot.Edges)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
