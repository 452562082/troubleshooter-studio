package analyzer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestScanEndpoints_NodeFrontendAndNextRewrite(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/orders.ts":  `axios.get("${API_BASE_URL}/api/orders/123")`,
		"next.config.js": `module.exports={async rewrites(){return [{source:'/api/:path*',destination:'http://mall-bff/internal/:path*'}]}}`,
	})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Services: []string{"mall-web"}, Stack: "node", Framework: "next.js", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/orders/123", "${API_BASE_URL}", "axios")
	assertTransform(t, got, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
}

func TestScanEndpoints_LaravelAndNginx(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"routes/api.php":        `Route::get('/api/orders/{id}', [OrderController::class, 'show']);`,
		"app/Clients/Order.php": `Http::post(env('ORDER_SERVICE_URL').'/internal/orders', $body);`,
		"nginx.conf":            `location /api/ { rewrite ^/api/(.*)$ /internal/$1 break; proxy_pass http://mall-bff; }`,
	})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-bff", Services: []string{"mall-bff"}, Stack: "php", Framework: "laravel", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/api/orders/{param}", "", "laravel-route")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/orders", "ORDER_SERVICE_URL", "laravel-http")
	assertTransform(t, got, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
}

func TestScanEndpoints_NodeFirstReleasePatterns(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/client.ts": `
fetch('/api/catalog')
fetch('/api/catalog/search', {method: 'post'})
axios({method:'patch', url:'/api/orders/123'})
`,
		"src/routes.ts": `
app.get('/api/health', handler)
@Controller('/api')
class OrderController {
  @Delete('/orders/:id')
  remove() {}
}
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Services: []string{"mall-web"}, Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/catalog", "", "fetch")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/api/catalog/search", "", "fetch")
	assertEndpoint(t, got, topology.DirectionOutbound, "PATCH", "/api/orders/123", "", "axios")
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/api/health", "", "express-route")
	assertEndpoint(t, got, topology.DirectionInbound, "DELETE", "/api/orders/{param}", "", "nest-route")
}

func TestScanEndpoints_NginxProxyPassIsTargetHint(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"deploy/nginx.conf": `location /api/ { proxy_pass http://mall-bff; }`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-gateway", Services: []string{"mall-gateway"}, Stack: "nginx", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/api", "mall-bff", "nginx-location")
	for _, endpoint := range got {
		if endpoint.Direction == topology.DirectionOutbound && endpoint.TargetHint == "mall-bff" {
			t.Fatalf("proxy_pass must not fabricate an outbound endpoint: %#v", got)
		}
	}
}

func TestScanEndpoints_NormalizesServiceIDsAndOrder(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/client.ts": `axios.post('/api/z/'); fetch('/api/a/:id')`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Services: []string{"mall-web"}, Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d endpoints, want 2: %#v", len(got), got)
	}
	for _, endpoint := range got {
		if endpoint.Service != "mall-web" {
			t.Fatalf("service = %q, want mall-web: %#v", endpoint.Service, endpoint)
		}
		if endpoint.ID != endpoint.SemanticID() {
			t.Fatalf("id = %q, want semantic id %q", endpoint.ID, endpoint.SemanticID())
		}
	}
	if got[0].ID > got[1].ID {
		t.Fatalf("endpoints are not sorted by semantic id: %#v", got)
	}
}

func TestScanEndpoints_RespectsCancellationAndWalkerGuards(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanEndpointsContext(cancelled, EndpointScanOptions{RepoPath: t.TempDir(), Stack: "node"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error = %v, want context.Canceled", err)
	}

	root := fixtureRepo(t, map[string]string{
		"node_modules/pkg/client.ts": `fetch('/api/ignored-dependency')`,
		"dist/client.ts":             `fetch('/api/ignored-build')`,
		"src/large.ts":               strings.Repeat(" ", 512*1024) + `fetch('/api/ignored-large')`,
		"src/client.ts":              `fetch('/api/kept')`,
	})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/kept", "", "fetch")
	for _, bad := range []string{"/api/ignored-dependency", "/api/ignored-build", "/api/ignored-large"} {
		for _, endpoint := range got {
			if endpoint.Path == bad {
				t.Fatalf("guarded endpoint %q was scanned: %#v", bad, got)
			}
		}
	}
}

func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertEndpoint(t *testing.T, got []topology.Endpoint, direction topology.Direction, method, path, hint, source string) {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.Direction == direction && endpoint.Method == method && endpoint.Path == path && endpoint.TargetHint == hint && endpoint.Source == source {
			return
		}
	}
	t.Fatalf("endpoint not found: direction=%s method=%s path=%s hint=%s source=%s in %#v", direction, method, path, hint, source, got)
}

func assertTransform(t *testing.T, got []topology.Endpoint, kind, from, to string) {
	t.Helper()
	for _, endpoint := range got {
		for _, transform := range endpoint.Transforms {
			if transform.Kind == kind && transform.From == from && transform.To == to {
				return
			}
		}
	}
	t.Fatalf("transform %s %s -> %s not found in %#v", kind, from, to, got)
}

func assertRPC(t *testing.T, got []topology.Endpoint, direction topology.Direction, method string) {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.Direction == direction && endpoint.Protocol == "grpc" && endpoint.RPCMethod == method {
			return
		}
	}
	t.Fatalf("rpc endpoint %s not found in %#v", method, got)
}
