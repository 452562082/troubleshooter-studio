package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAPIRoutesFromGoAndNodeSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "handler.go"), []byte(`
package main
func main() {
  r.GET("/api/orders/:id", handler)
  r.POST("/api/payments/submit", handler)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "routes.ts"), []byte(`
router.get('/api/users/:id', handler)
app.post("/graphql", handler)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	routes := ScanAPIRoutes("go", root, nil)
	routes = append(routes, ScanAPIRoutes("node", root, nil)...)
	got := map[string]bool{}
	for _, route := range routes {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/orders/:id",
		"POST /api/payments/submit",
		"GET /api/users/:id",
		"POST /graphql",
	} {
		if !got[want] {
			t.Fatalf("missing route %s in %#v", want, routes)
		}
	}
}

func TestExtractAPIRoutesIgnoresClientCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "client.ts"), []byte(`
const path = "/api/client"
client.request({ path: "/api/client" })
axios.get('/api/client')
cy.get('/api/client')
e.get('/api/client-event', handler)
router.get('/api/server', handler)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "client.go"), []byte(`
package main
import "net/http"
func main() {
  http.Get("/api/client")
  r.GET("/api/server-go", handler)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	routes := ScanAPIRoutes("node", root, nil)
	routes = append(routes, ScanAPIRoutes("go", root, nil)...)

	if hasRoute(routes, "GET", "/api/client") {
		t.Fatalf("client call was misclassified as route: %#v", routes)
	}
	if hasRoute(routes, "GET", "/api/client-event") {
		t.Fatalf("generic e.get was misclassified as route: %#v", routes)
	}
	if !hasRoute(routes, "GET", "/api/server") {
		t.Fatalf("missing node server route in %#v", routes)
	}
	if !hasRoute(routes, "GET", "/api/server-go") {
		t.Fatalf("missing go server route in %#v", routes)
	}
}

func TestExtractAPIRoutesIgnoresArbitraryPathAssignments(t *testing.T) {
	routes := extractRoutesFromSource(`
const path = "/api/client"
const route = "/api/also-client"
client.request({ path: "/api/client" })
service.call({ route: "/api/also-client" })
`, "client.ts")
	if len(routes) != 0 {
		t.Fatalf("arbitrary path/route values should not be classified as routes: %#v", routes)
	}
}

func TestExtractAPIRoutesComposesSpringClassPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "OrderController.java"), []byte(`
package demo;

@RestController
@RequestMapping("/api")
class OrderController {
  @GetMapping("/orders")
  public String orders() {
    return "ok";
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	routes := ScanAPIRoutes("java", root, nil)
	if !hasRoute(routes, "GET", "/api/orders") {
		t.Fatalf("missing composed spring route in %#v", routes)
	}
}

func TestExtractAPIRoutesSpringNamedArgsOrderVariants(t *testing.T) {
	routes := extractRoutesFromSource(`
package demo;

@RestController
@RequestMapping(path = "/api")
class OrderController {
  @RequestMapping(method = RequestMethod.GET, value = "/orders")
  public String requestMappingOrders() {
    return "ok";
  }

  @GetMapping(produces = "application/json", path = "/orders")
  public String getMappingOrders() {
    return "ok";
  }
}
`, "OrderController.java")

	if !hasRoute(routes, "GET", "/api/orders") {
		t.Fatalf("missing spring route from named argument variants in %#v", routes)
	}
}

func TestScanAPIRoutesIgnoresDependencyDirectories(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "node_modules", "pkg", "routes.ts"),
		filepath.Join(root, "vendor", "pkg", "routes.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "routes.ts"), []byte(`
router.get('/api/from-node-modules', handler)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor", "pkg", "routes.go"), []byte(`
package pkg
func init() {
  r.GET("/api/from-vendor", handler)
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	routes := ScanAPIRoutes("", root, nil)
	if len(routes) != 0 {
		t.Fatalf("dependency directories should not be scanned: %#v", routes)
	}
}

func TestScanAPIRoutesIgnoresTestAndFixtureSources(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"routes_test.go": `
package main
func TestRoute(t *testing.T) {
  r.GET("/api/from-go-test", handler)
}
`,
		"routes.test.ts": `
router.get('/api/from-ts-test', handler)
`,
		filepath.Join("fixtures", "routes.ts"): `
router.get('/api/from-fixture', handler)
`,
		filepath.Join("mock", "routes.ts"): `
router.get('/api/from-mock', handler)
`,
		"routes.ts": `
router.get('/api/real', handler)
`,
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	routes := ScanAPIRoutes("", root, nil)
	if !hasRoute(routes, "GET", "/api/real") {
		t.Fatalf("missing real route: %#v", routes)
	}
	for _, bad := range []string{"/api/from-go-test", "/api/from-ts-test", "/api/from-fixture", "/api/from-mock"} {
		if hasRoute(routes, "GET", bad) {
			t.Fatalf("test/fixture route %s should not be scanned: %#v", bad, routes)
		}
	}
}

func TestExtractAPIRoutesLineNumberIsNonZero(t *testing.T) {
	routes := extractRoutesFromSource(`

router.get('/api/line-number', handler)
`, "routes.ts")
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1: %#v", len(routes), routes)
	}
	if routes[0].Line == 0 {
		t.Fatalf("line number should be non-zero: %#v", routes[0])
	}
}

func TestRouteMatchStrength(t *testing.T) {
	cases := []struct {
		route string
		path  string
		want  string
	}{
		{"/api/orders/:id", "/api/orders/42", "pattern"},
		{"/api/payments/submit", "/api/payments/submit", "exact"},
		{"/api/orders", "/api/orders/42", "prefix"},
		{"/api/order", "/api/orders/42", ""},
		{"/api/users/:id", "/api/orders/42", ""},
	}
	for _, tc := range cases {
		if got := routeMatchStrength(tc.route, tc.path); got != tc.want {
			t.Fatalf("routeMatchStrength(%q,%q)=%q, want %q", tc.route, tc.path, got, tc.want)
		}
	}
}

func hasRoute(routes []APIRoute, method, path string) bool {
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
