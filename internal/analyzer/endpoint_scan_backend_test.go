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
