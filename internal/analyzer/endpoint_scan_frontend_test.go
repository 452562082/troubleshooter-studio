package analyzer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestScanEndpoints_RejectsInvalidRepoRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{RepoPath: missing, Stack: "node"}); err == nil {
		t.Fatal("missing repository root returned nil error")
	}

	file := filepath.Join(t.TempDir(), "repo.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{RepoPath: file, Stack: "node"}); err == nil {
		t.Fatal("file repository root returned nil error")
	}
}

func TestScanEndpoints_LaravelEnvDefaultAndSuffix(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"app/Clients/Order.php": `Http::post(env('ORDER_SERVICE_URL', 'https://fallback.example').'/internal/orders', $body);`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-bff", Stack: "php", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/orders", "ORDER_SERVICE_URL", "laravel-http")
}

func TestScanEndpoints_NextRewritesIgnoreRedirectsAndArbitraryObjects(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"next.config.js": `
const unrelated = {source:'/not-a-rewrite', destination:'http://wrong/unrelated'}
module.exports = {
  async redirects() { return [{source:'/old', destination:'http://wrong/new', permanent:true}] },
  async rewrites() { return [{source:'/api/:path*', destination:'http://mall-bff/internal/:path*'}] },
}
`,
		"src/fixture.ts": `export const fixture = {source:'/also-not-a-rewrite', destination:'http://wrong/fixture'}`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", Framework: "next.js", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/{wildcard}", "mall-bff", "next-rewrite")
	for _, endpoint := range got {
		if endpoint.Source == "next-rewrite" && endpoint.Path != "/api/{wildcard}" {
			t.Fatalf("non-rewrite source/destination object was scanned: %#v", endpoint)
		}
	}
}

func TestScanEndpoints_NginxRegexModifierNestedBlockAssociation(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"deploy/nginx.conf": `
location ~* ^/api/(.*)$ {
  if ($request_method = OPTIONS) {
    add_header Access-Control-Allow-Origin *;
  }
  rewrite ^/api/(.*)$ /internal/$1 break;
  proxy_pass http://mall-bff/internal;
}
location ~ ^/admin/(.+)$ {
  rewrite ^/admin/(.+)$ /ops/$1 break;
  proxy_pass http://admin-bff;
}
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-gateway", Stack: "nginx", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	api := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/{wildcard}", "mall-bff", "nginx-location")
	assertEndpointTransform(t, api, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
	admin := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/admin/{wildcard}", "admin-bff", "nginx-location")
	assertEndpointTransform(t, admin, "rewrite", "/admin/{wildcard}", "/ops/{wildcard}")
}

func TestScanEndpoints_NestRoutesUseContainingController(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/controllers.ts": `
@Controller('/cats')
class CatsController {
  @Get('/:id')
  getCat() {}
}

@Controller('/dogs')
class DogsController {
  @Post('/')
  addDog() {}
}
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "pets-api", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/cats/{param}", "", "nest-route")
	assertEndpoint(t, got, topology.DirectionInbound, "POST", "/dogs", "", "nest-route")
}

func TestScanEndpoints_HostOnlyHTTPURLsUseRootPath(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/client.ts":  `fetch('https://api.example.com')`,
		"next.config.js": `module.exports={async rewrites(){return [{source:'/api',destination:'https://mall-bff'}]}}`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/", "api.example.com", "fetch")
	rewrite := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api", "mall-bff", "next-rewrite")
	assertEndpointTransform(t, rewrite, "rewrite", "/api", "/")
}

func TestNormalizeEndpoints_DeduplicatesExactFacts(t *testing.T) {
	endpoint := httpEndpoint(topology.DirectionOutbound, "GET", "/api/orders", "orders", "src/client.ts:3", "fetch")
	got := normalizeEndpoints(EndpointScanOptions{Repo: "mall-web"}, []topology.Endpoint{endpoint, endpoint})
	if len(got) != 1 {
		t.Fatalf("normalized endpoint count = %d, want 1: %#v", len(got), got)
	}
}

func TestScanEndpoints_RecordsSourceLineEvidence(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/client.ts": "// setup\n\naxios.get('/api/orders')\n",
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := requireEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/orders", "", "axios")
	if endpoint.Location != "src/client.ts:3" {
		t.Fatalf("location = %q, want src/client.ts:3", endpoint.Location)
	}
}

func TestNormalizeEndpoints_ContextCancellationDuringNormalization(t *testing.T) {
	ctx := &cancelAfterErrChecks{remaining: 1}
	endpoint := httpEndpoint(topology.DirectionOutbound, "GET", "/api/orders", "orders", "src/client.ts:1", "fetch")
	if _, err := normalizeEndpointsContext(ctx, EndpointScanOptions{Repo: "mall-web"}, []topology.Endpoint{endpoint, endpoint}); !errors.Is(err, context.Canceled) {
		t.Fatalf("normalization error = %v, want context.Canceled", err)
	}
}

func TestExtractNodeCalls_ContextCancellationDuringSourceExtraction(t *testing.T) {
	ctx := &cancelAfterErrChecks{remaining: 1}
	source := endpointSource{rel: "src/client.ts", text: "fetch('/api/one'); fetch('/api/two')"}
	if _, err := extractNodeCallsContext(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("source extraction error = %v, want context.Canceled", err)
	}
}

func TestScanEndpoints_SurfacesWalkErrorsBelowRepoRoot(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"src/client.ts": `fetch('/api/orders')`})
	wantErr := fs.ErrPermission
	original := endpointWalkDir
	endpointWalkDir = func(root string, walkFn fs.WalkDirFunc) error {
		return walkFn(filepath.Join(root, "private"), nil, wantErr)
	}
	t.Cleanup(func() { endpointWalkDir = original })

	if _, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root}); !errors.Is(err, wantErr) {
		t.Fatalf("scan error = %v, want %v", err, wantErr)
	}
}

func TestScanEndpoints_SurfacesSourceStatErrors(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"src/client.ts": `fetch('/api/orders')`})
	wantErr := errors.New("injected endpoint stat failure")
	original := endpointStat
	endpointStat = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(filepath.ToSlash(path), "/src/client.ts") {
			return nil, wantErr
		}
		return original(path)
	}
	t.Cleanup(func() { endpointStat = original })

	if _, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root}); !errors.Is(err, wantErr) {
		t.Fatalf("scan error = %v, want injected stat failure", err)
	}
}

func TestScanEndpoints_SurfacesSourceReadErrors(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"src/client.ts": `fetch('/api/orders')`})
	wantErr := errors.New("injected endpoint read failure")
	original := endpointReadFile
	endpointReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(filepath.ToSlash(path), "/src/client.ts") {
			return nil, wantErr
		}
		return original(path)
	}
	t.Cleanup(func() { endpointReadFile = original })

	if _, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root}); !errors.Is(err, wantErr) {
		t.Fatalf("scan error = %v, want injected read failure", err)
	}
}

func TestScanEndpoints_TemplateVariableHostOnlyURLsUseRootPath(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/client.ts":  "fetch(`${API_BASE_URL}`)",
		"next.config.js": "module.exports={async rewrites(){return [{source:'/api',destination:'${BFF_URL}'}]}}",
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-web", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/", "${API_BASE_URL}", "fetch")
	rewrite := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api", "${BFF_URL}", "next-rewrite")
	assertEndpointTransform(t, rewrite, "rewrite", "/api", "/")
}

func TestScanEndpoints_NestRootControllerWithClassDecorators(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/controllers.ts": `
@Controller()
@UseGuards(AuthGuard)
export class RootController {
  @Get('/health')
  health() {}
}

@Controller('/admin')
@UseInterceptors(AuditInterceptor)
class AdminController {
  @Post('/jobs')
  createJob() {}
}
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "control-api", Stack: "node", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/health", "", "nest-route")
	assertEndpoint(t, got, topology.DirectionInbound, "POST", "/admin/jobs", "", "nest-route")
}

func TestScanEndpoints_NginxParentDoesNotInheritNestedLocationDirectives(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"deploy/nginx.conf": `
location /api {
  location /api/admin {
    rewrite ^/api/admin/(.*)$ /admin/$1 break;
    proxy_pass http://admin-bff;
  }
  if ($request_method = OPTIONS) {
    rewrite ^/api/preflight$ /preflight break;
  }
  rewrite ^/api/(.*)$ /internal/$1 break;
  proxy_pass http://mall-bff;
}
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-gateway", Stack: "nginx", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	parent := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api", "mall-bff", "nginx-location")
	assertEndpointTransform(t, parent, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
	assertEndpointTransform(t, parent, "rewrite", "/api/preflight", "/preflight")
	for _, transform := range parent.Transforms {
		if transform.From == "/api/admin/{wildcard}" || transform.To == "/admin/{wildcard}" {
			t.Fatalf("parent inherited nested location transform: %#v", parent)
		}
	}

	child := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/admin", "admin-bff", "nginx-location")
	assertEndpointTransform(t, child, "rewrite", "/api/admin/{wildcard}", "/admin/{wildcard}")
}

type cancelAfterErrChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterErrChecks) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }
func (c *cancelAfterErrChecks) Done() <-chan struct{}                   { return nil }
func (c *cancelAfterErrChecks) Value(key any) any                       { return nil }
func (c *cancelAfterErrChecks) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
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

func requireEndpoint(t *testing.T, got []topology.Endpoint, direction topology.Direction, method, path, hint, source string) topology.Endpoint {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.Direction == direction && endpoint.Method == method && endpoint.Path == path && endpoint.TargetHint == hint && endpoint.Source == source {
			return endpoint
		}
	}
	t.Fatalf("endpoint not found: direction=%s method=%s path=%s hint=%s source=%s in %#v", direction, method, path, hint, source, got)
	return topology.Endpoint{}
}

func assertEndpointTransform(t *testing.T, endpoint topology.Endpoint, kind, from, to string) {
	t.Helper()
	for _, transform := range endpoint.Transforms {
		if transform.Kind == kind && transform.From == from && transform.To == to {
			return
		}
	}
	t.Fatalf("transform %s %s -> %s not found on %#v", kind, from, to, endpoint)
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
