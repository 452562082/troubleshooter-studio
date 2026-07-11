package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestScanEndpoints_GoHTTPAndGRPC(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"internal/http/order.go":  `r.GET("/internal/orders/:id", showOrder)`,
		"internal/client/user.go": `req, _ := http.NewRequest("POST", "http://user-service/internal/users", body)`,
		"proto/order.proto":       `service OrderService { rpc GetOrder (GetOrderRequest) returns (Order); }`,
	})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-order", Services: []string{"mall-order"}, Stack: "go", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/internal/orders/{param}", "", "gin-route")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/users", "user-service", "go-http")
	assertRPC(t, got, topology.DirectionInbound, "OrderService/GetOrder")
}

func TestScanEndpoints_GoFrameworkPatternsAndGRPCClient(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"internal/http/routes.go": `package http
e.POST("/echo/orders/:id", handle)
http.HandleFunc("GET /health/{name}", health)
mux.Handle("/ready", ready)
`,
		"internal/client/client.go": `package client
func call(ctx context.Context) {
	http.Get("http://catalog-service/internal/catalog")
	http.Post("https://order-service/internal/orders", "application/json", body)
	http.NewRequestWithContext(ctx, "PATCH", "http://user-service/internal/users/7", body)
	cc.Invoke(ctx, "DELETE", "http://inventory-service/internal/stock/3", nil, nil)
	grpcConn.Invoke(ctx, "/shop.inventory.v1.Inventory/GetStock", in, out)
}`,
		"proto/inventory.proto": `package shop.inventory.v1;
service Inventory {
  rpc GetStock (GetStockRequest) returns (GetStockReply);
}`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "inventory", Stack: "go", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "POST", "/echo/orders/{param}", "", "echo-route")
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/health/{param}", "", "go-net-http")
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/ready", "", "go-net-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/internal/catalog", "catalog-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/orders", "order-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "PATCH", "/internal/users/7", "user-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "DELETE", "/internal/stock/3", "inventory-service", "kratos-http")
	clientRPC := requireRPCEndpoint(t, got, topology.DirectionOutbound, "shop.inventory.v1.Inventory/GetStock")
	if clientRPC.TargetHint != "shop.inventory.v1.Inventory" || clientRPC.Location != "internal/client/client.go:7" {
		t.Fatalf("gRPC client evidence not preserved: %#v", clientRPC)
	}
	serverRPC := requireRPCEndpoint(t, got, topology.DirectionInbound, "shop.inventory.v1.Inventory/GetStock")
	if serverRPC.Location != "proto/inventory.proto:3" {
		t.Fatalf("proto location = %q, want proto/inventory.proto:3", serverRPC.Location)
	}
}

func TestScanEndpoints_GoKratosRouteUsesFrameworkEvidence(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"internal/server/http.go": `r.PUT("/v1/orders/{id}", update)`})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "order", Stack: "go", Framework: "kratos", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "PUT", "/v1/orders/{param}", "", "kratos-route")
}

func TestScanEndpoints_JavaFeignAndPythonFastAPI(t *testing.T) {
	javaRoot := fixtureRepo(t, map[string]string{"src/OrderClient.java": `@FeignClient(name="order-service") interface C { @PostMapping("/internal/orders") void create(); }`})
	javaGot, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-bff", Services: []string{"mall-bff"}, Stack: "java", RepoPath: javaRoot})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, javaGot, topology.DirectionOutbound, "POST", "/internal/orders", "order-service", "feign")

	pyRoot := fixtureRepo(t, map[string]string{"app.py": `@app.get('/profiles/{user_id}')
def profile(user_id): pass
requests.get(PROFILE_URL + '/internal/profile')`})
	pyGot, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "profile", Services: []string{"profile"}, Stack: "python", RepoPath: pyRoot})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, pyGot, topology.DirectionInbound, "GET", "/profiles/{param}", "", "fastapi-route")
	assertEndpoint(t, pyGot, topology.DirectionOutbound, "GET", "/internal/profile", "PROFILE_URL", "python-requests")
}

func TestScanEndpoints_JavaSpringGatewayAndProto(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"src/OrderController.java": `@RequestMapping("/internal/orders")
public class OrderController {
  @GetMapping("/{id}")
  Order get() { return null; }
}`,
		"src/CatalogClient.java": `@FeignClient(value="catalog-service")
interface CatalogClient {
  @GetMapping("/internal/catalog/{id}") Item get();
}`,
		"src/PaymentClient.java": `@FeignClient(url="${PAYMENT_URL}")
interface PaymentClient {
  @RequestMapping(path="/internal/payments", method=RequestMethod.PUT) void put();
}`,
		"src/Gateway.java": `builder.routes().route("orders", r -> r.path("/api/orders/**").filters(f -> f.stripPrefix(1)).uri("lb://order-service"));`,
		"src/main/resources/application.yml": `spring:
  cloud:
    gateway:
      routes:
        - id: users
          uri: lb://user-service
          predicates:
            - Path=/api/users/**
          filters:
            - RewritePath=/api/users/(?<segment>.*), /internal/users/${segment}
`,
		"proto/payment.proto": `package shop.payment.v1;
service PaymentService { rpc Pay (PayRequest) returns (PayReply); }`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "mall-bff", Stack: "java", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/internal/orders/{param}", "", "spring-route")
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/internal/catalog/{param}", "catalog-service", "feign")
	assertEndpoint(t, got, topology.DirectionOutbound, "PUT", "/internal/payments", "${PAYMENT_URL}", "feign")
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/orders/{wildcard}", "order-service", "spring-gateway")
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/users/{wildcard}", "user-service", "spring-gateway")
	assertRPC(t, got, topology.DirectionInbound, "shop.payment.v1.PaymentService/Pay")

	for _, endpoint := range got {
		if endpoint.Direction == topology.DirectionInbound && endpoint.Path == "/internal/catalog/{param}" {
			t.Fatalf("Feign method mapping was also emitted as inbound: %#v", endpoint)
		}
	}
	javaGateway := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/orders/{wildcard}", "order-service", "spring-gateway")
	assertEndpointTransform(t, javaGateway, "strip_prefix", "/api/orders/{wildcard}", "/orders/{wildcard}")
	yamlGateway := requireEndpoint(t, got, topology.DirectionInbound, "ANY", "/api/users/{wildcard}", "user-service", "spring-gateway")
	assertEndpointTransform(t, yamlGateway, "rewrite", "/api/users/{wildcard}", "/internal/users/{wildcard}")
}

func TestScanEndpoints_PythonFrameworksClientsAndDynamicGuard(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"app.py": `@app.get('/profiles/{user_id}')
def profile(user_id): pass

@bp.route('/admin/jobs/<job_id>', methods=['POST'])
def job(job_id): pass

urlpatterns = [path('accounts/<int:user_id>/', account)]

requests.get('https://profile-service/internal/profile')
httpx.post(PAYMENT_URL + '/internal/payments')
requests.delete(build_url('/dynamic'))
`,
	})

	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "profile", Stack: "python", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/profiles/{param}", "", "fastapi-route")
	assertEndpoint(t, got, topology.DirectionInbound, "POST", "/admin/jobs/{param}", "", "flask-route")
	assertEndpoint(t, got, topology.DirectionInbound, "ANY", "/accounts/{param}", "", "django-route")
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/internal/profile", "profile-service", "python-requests")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/payments", "PAYMENT_URL", "python-httpx")
	for _, endpoint := range got {
		if endpoint.Direction == topology.DirectionOutbound && strings.Contains(endpoint.Path, "dynamic") {
			t.Fatalf("dynamic Python expression emitted a formal endpoint: %#v", endpoint)
		}
	}
	profile := requireEndpoint(t, got, topology.DirectionInbound, "GET", "/profiles/{param}", "", "fastapi-route")
	if profile.Location != "app.py:1" {
		t.Fatalf("FastAPI location = %q, want app.py:1", profile.Location)
	}
}

func TestScanEndpoints_BackendCommentsAndDocstringsAreIgnored(t *testing.T) {
	t.Run("go and proto", func(t *testing.T) {
		root := fixtureRepo(t, map[string]string{
			"routes.go": `package routes
func register(r Router) {
  // r.GET("/dead-line", dead)
  /* r.POST("/dead-block", dead) */
  r.GET("/live//orders", live)
  http.Get("http://catalog-service/live//items")
}`,
			"service.proto": `package shop.live;
// service DeadLine { rpc Gone (In) returns (Out); }
/* service DeadBlock { rpc Gone (In) returns (Out); } */
service Live { rpc Get (In) returns (Out); }`,
		})
		got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "live", Stack: "go", RepoPath: root})
		if err != nil {
			t.Fatal(err)
		}
		assertEndpoint(t, got, topology.DirectionInbound, "GET", "/live/orders", "", "gin-route")
		assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/live/items", "catalog-service", "go-http")
		assertRPC(t, got, topology.DirectionInbound, "shop.live.Live/Get")
		assertNoEndpointPath(t, got, "/dead-line")
		assertNoEndpointPath(t, got, "/dead-block")
		assertNoRPCMethod(t, got, "shop.live.DeadLine/Gone")
		assertNoRPCMethod(t, got, "shop.live.DeadBlock/Gone")
		live := requireEndpoint(t, got, topology.DirectionInbound, "GET", "/live/orders", "", "gin-route")
		if live.Location != "routes.go:5" {
			t.Fatalf("Go route location = %q, want routes.go:5", live.Location)
		}
	})

	t.Run("java", func(t *testing.T) {
		root := fixtureRepo(t, map[string]string{"Routes.java": `// @GetMapping("/dead-line")
/* @PostMapping("/dead-block") */
@RequestMapping("/live//api")
class Routes {
  @GetMapping("/orders/*marker*/detail")
  Object get() { return null; }
}`})
		got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "live", Stack: "java", RepoPath: root})
		if err != nil {
			t.Fatal(err)
		}
		assertEndpoint(t, got, topology.DirectionInbound, "GET", "/live/api/orders/{wildcard}/detail", "", "spring-route")
		assertNoEndpointPath(t, got, "/dead-line")
		assertNoEndpointPath(t, got, "/dead-block")
		live := requireEndpoint(t, got, topology.DirectionInbound, "GET", "/live/api/orders/{wildcard}/detail", "", "spring-route")
		if live.Location != "Routes.java:5" {
			t.Fatalf("Java route location = %q, want Routes.java:5", live.Location)
		}
	})

	t.Run("python", func(t *testing.T) {
		root := fixtureRepo(t, map[string]string{"app.py": `# @app.get("/dead-line")
"""
@app.post("/dead-docstring")
requests.get("http://dead-service/docstring")
"""
@app.get("/live/orders")
def live(): pass
requests.get("http://profile-service/live//items")
requests.get("http://anchor-service/live#fragment")
`})
		got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "live", Stack: "python", RepoPath: root})
		if err != nil {
			t.Fatal(err)
		}
		assertEndpoint(t, got, topology.DirectionInbound, "GET", "/live/orders", "", "fastapi-route")
		assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/live/items", "profile-service", "python-requests")
		assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/live", "anchor-service", "python-requests")
		assertNoEndpointPath(t, got, "/dead-line")
		assertNoEndpointPath(t, got, "/dead-docstring")
		assertNoEndpointPath(t, got, "/docstring")
		live := requireEndpoint(t, got, topology.DirectionInbound, "GET", "/live/orders", "", "fastapi-route")
		if live.Location != "app.py:6" {
			t.Fatalf("Python route location = %q, want app.py:6", live.Location)
		}
	})
}

func TestScanEndpoints_JavaFeignClassMappingBeforeAnnotation(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"OrderClient.java": `@RequestMapping("/api")
@FeignClient(name="orders")
interface OrderClient {
  @GetMapping("/x") Object get();
}`})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "client", Stack: "java", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/x", "orders", "feign")
	for _, endpoint := range got {
		if endpoint.Direction == topology.DirectionInbound && (endpoint.Path == "/api" || endpoint.Path == "/x" || endpoint.Path == "/api/x") {
			t.Fatalf("Feign mapping emitted as inbound: %#v", endpoint)
		}
	}
}

func TestScanEndpoints_JavaFeignHeaderIgnoresQuotedBoundaryCharacters(t *testing.T) {
	tests := []struct {
		name       string
		classPath  string
		expected   string
		unexpected string
	}{
		{name: "route parameter", classPath: "/api/{tenant}", expected: "/api/{param}/x", unexpected: "/api/{param}"},
		{name: "property placeholder", classPath: "${api.prefix}", expected: "/${api.prefix}/x", unexpected: "/${api.prefix}"},
		{name: "semicolon", classPath: "/api;v1", expected: "/api;v1/x", unexpected: "/api;v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{"OrderClient.java": `@RequestMapping("` + test.classPath + `")
@FeignClient(name="orders")
interface OrderClient {
  @GetMapping("/x") Object get();
}`})
			got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "client", Stack: "java", RepoPath: root})
			if err != nil {
				t.Fatal(err)
			}
			assertEndpoint(t, got, topology.DirectionOutbound, "GET", test.expected, "orders", "feign")
			if len(got) != 1 {
				t.Fatalf("Feign declaration emitted endpoints besides the combined outbound route: %#v", got)
			}
			for _, endpoint := range got {
				if endpoint.Direction == topology.DirectionInbound || endpoint.Path == test.unexpected {
					t.Fatalf("Feign class mapping emitted as inbound: %#v", endpoint)
				}
			}
		})
	}
}

func TestScanEndpoints_PythonRejectsPartiallyStaticClientExpressions(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"client.py": `requests.get(BASE_URL + "/users/" + user_id)
httpx.post("https://orders-service/orders/" + order_id)
requests.delete(API_URL + "/static", timeout=1)
`})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "client", Stack: "python", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "DELETE", "/static", "API_URL", "python-requests")
	assertNoEndpointPath(t, got, "/users")
	assertNoEndpointPath(t, got, "/orders")
}

func TestScanEndpoints_GoRequestMethodConstants(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"client.go": `package client
func call(ctx context.Context) {
  http.NewRequest(http.MethodGet, "http://catalog-service/items", nil)
  http.NewRequestWithContext(ctx, http.MethodPost, "http://order-service/orders", nil)
  http.NewRequest(http.MethodPut, "http://order-service/orders/1", nil)
  http.NewRequestWithContext(ctx, http.MethodPatch, "http://order-service/orders/2", nil)
  http.NewRequest(http.MethodDelete, "http://order-service/orders/3", nil)
  http.NewRequest(ctxMethod, "http://ignored-service/ignored", nil)
}`})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "client", Stack: "go", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/items", "catalog-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/orders", "order-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "PUT", "/orders/1", "order-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "PATCH", "/orders/2", "order-service", "go-http")
	assertEndpoint(t, got, topology.DirectionOutbound, "DELETE", "/orders/3", "order-service", "go-http")
	assertNoEndpointPath(t, got, "/ignored")
}

func TestScanEndpoints_GoGinAndEchoGroups(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"routes.go": `package routes
func v1(r Router) {
  api := r.Group("/api")
  api.GET("/orders", list)
  r.Group("/admin").POST("/jobs", create)
}
func v2(r Router) {
  api := r.Group("/v2")
  api.GET("/orders", listV2)
}
func unrelated(r Router) {
  api.GET("/health", health)
}
func echo(e Router) {
  api := e.Group("/echo")
  api.GET("/orders", listEcho)
  e.Group("/ops").DELETE("/jobs", deleteJob)
}`})
	got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo: "routes", Stack: "go", RepoPath: root})
	if err != nil {
		t.Fatal(err)
	}
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/api/orders", "", "gin-route")
	assertEndpoint(t, got, topology.DirectionInbound, "POST", "/admin/jobs", "", "gin-route")
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/v2/orders", "", "gin-route")
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/health", "", "gin-route")
	assertEndpoint(t, got, topology.DirectionInbound, "GET", "/echo/orders", "", "echo-route")
	assertEndpoint(t, got, topology.DirectionInbound, "DELETE", "/ops/jobs", "", "echo-route")
	assertNoEndpointPath(t, got, "/api/health")
	assertNoEndpointPath(t, got, "/v2/health")
}

func assertNoEndpointPath(t *testing.T, got []topology.Endpoint, path string) {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.Path == path {
			t.Fatalf("unexpected endpoint path %q: %#v", path, endpoint)
		}
	}
}

func assertNoRPCMethod(t *testing.T, got []topology.Endpoint, method string) {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.RPCMethod == method {
			t.Fatalf("unexpected RPC method %q: %#v", method, endpoint)
		}
	}
}

func requireRPCEndpoint(t *testing.T, got []topology.Endpoint, direction topology.Direction, method string) topology.Endpoint {
	t.Helper()
	for _, endpoint := range got {
		if endpoint.Direction == direction && endpoint.Protocol == "grpc" && endpoint.RPCMethod == method {
			return endpoint
		}
	}
	t.Fatalf("rpc endpoint %s not found in %#v", method, got)
	return topology.Endpoint{}
}
