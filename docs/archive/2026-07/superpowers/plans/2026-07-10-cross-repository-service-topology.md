# Cross-Repository Service Topology Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an explainable cross-repository service topology that connects frontend requests, gateway rewrites, backend routes, and repository-local CodeGraph analysis, with deterministic matching and YAML-persisted human overrides.

**Architecture:** Language scanners emit repository-local inbound/outbound endpoint facts into `internal/topology` types. A deterministic matcher scores cross-repository edges, merges `confirm/reject/add` overrides, and projects the result into a service graph plus endpoint evidence. Deployment runs this through the existing auto-analyze cache; the workspace receives stable routing artifacts and a query skill, while the desktop workbench exposes candidate review and writes only human decisions back to YAML.

**Tech Stack:** Go 1.x, Go templates, YAML v3, Vue 3 + TypeScript + Vitest, Python 3 + PyYAML for generated skill scripts, existing Wails v2 bindings.

## Global Constraints

- Auto-discovery runs only when at least two runnable service nodes have local repository paths; single-repository and common-lib/infra-only systems skip it.
- Automatic scanner output is rebuildable cache data; only human `confirm/reject/add` decisions persist in `troubleshooter.yaml`.
- Matching is deterministic. No LLM call may create, promote, or score a formal edge.
- Merge priority is `reject > add/confirm > high-confidence automatic > candidate`.
- Confidence thresholds are exact: `>=0.85` formal automatic edge, `0.60–0.849999` candidate, `<0.60` hint-only and excluded from the formal graph.
- Default traversal depth is 3 with cycle detection.
- Repository scan failure never blocks deployment; it must produce an explicit partial-status result.
- Existing `service-dependency-map.yaml` remains the cascade/runtime compatibility view and must be projected from the same formal graph.
- CodeGraph remains repository-local and is invoked only after topology selects a concrete repository/endpoint.
- First release supports synchronous HTTP/HTTPS, Feign and gRPC only; Kafka/RabbitMQ event topology is out of scope.
- Tests use local fixtures and fake command seams only; no default test may download, clone, or access a business environment.

---

### Task 1: Add the YAML override contract and validation

**Files:**
- Create: `internal/config/types_service_topology.go`
- Create: `internal/config/service_topology_test.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/validate.go`
- Modify: `schema/troubleshooter.schema.yaml`
- Modify: `api/handler_test.go`

**Interfaces:**
- Produces: `config.ServiceTopology`, `config.ServiceTopologyOverride`, and `ServiceTopologyOverride.SemanticKey() string`.
- Consumes later: analyzer pipeline, matcher merge, wizard YAML import/export.

- [ ] **Step 1: Write failing config tests**

Add tests that load a valid confirm/reject/add set, reject an unknown action, reject a service absent from all effective service names, normalize method case, and reject an HTTP path without `/`:

```go
func TestServiceTopologyOverridesValidateAndNormalize(t *testing.T) {
    cfg := minimalValid()
    cfg.Repos = []Repo{
        {Name:"web", URL:"https://example.test/web.git", Stack:"node", ServiceNames:[]string{"mall-web"}},
        {Name:"bff", URL:"https://example.test/bff.git", Stack:"php", ServiceNames:[]string{"mall-bff"}},
    }
    cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{{
        Action: "confirm", FromService: "mall-web", ToService: "mall-bff",
        Protocol: "http", Method: "get", Path: "/api/orders/:id",
    }}
    if err := Validate(&cfg); err != nil { t.Fatal(err) }
    got := cfg.ServiceTopology.Overrides[0]
    if got.Method != "GET" || got.Path != "/api/orders/{param}" {
        t.Fatalf("normalized override = %#v", got)
    }
}

func TestServiceTopologyOverridesRejectInvalidContract(t *testing.T) {
    cases := []ServiceTopologyOverride{
        {Action: "guess", FromService: "mall-web", ToService: "mall-bff", Protocol: "http", Method: "GET", Path: "/x"},
        {Action: "add", FromService: "missing", ToService: "mall-bff", Protocol: "http", Method: "GET", Path: "/x"},
        {Action: "add", FromService: "mall-web", ToService: "mall-bff", Protocol: "http", Method: "GET", Path: "x"},
    }
    for _, override := range cases {
        cfg := minimalValid()
        cfg.Repos = []Repo{
            {Name:"web", URL:"https://example.test/web.git", Stack:"node", ServiceNames:[]string{"mall-web"}},
            {Name:"bff", URL:"https://example.test/bff.git", Stack:"php", ServiceNames:[]string{"mall-bff"}},
        }
        cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{override}
        if err := Validate(&cfg); err == nil { t.Fatalf("expected rejection for %#v", override) }
    }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/config/ -run TestServiceTopology -count=1`

Expected: compile failure because the service-topology types and field do not exist.

- [ ] **Step 3: Implement types and semantic validation**

Create the exact public contract:

```go
type ServiceTopology struct {
    Overrides []ServiceTopologyOverride `yaml:"overrides,omitempty" json:"overrides,omitempty"`
}

type ServiceTopologyOverride struct {
    Action      string `yaml:"action" json:"action"`
    FromService string `yaml:"from_service" json:"from_service"`
    ToService   string `yaml:"to_service" json:"to_service"`
    Protocol    string `yaml:"protocol" json:"protocol"`
    Method      string `yaml:"method,omitempty" json:"method,omitempty"`
    Path        string `yaml:"path,omitempty" json:"path,omitempty"`
    RPCMethod   string `yaml:"rpc_method,omitempty" json:"rpc_method,omitempty"`
}

func (o ServiceTopologyOverride) SemanticKey() string {
    return strings.Join([]string{o.FromService, o.ToService, o.Protocol, o.Method, o.Path, o.RPCMethod}, "\x1f")
}
```

Add a `ServiceTopology ServiceTopology` field with YAML tag `service_topology,omitempty` to `SystemConfig`. In validation, build the valid service set from every service-node repo's non-empty `ServiceNames`, falling back to `Repo.Name` when the slice is empty; normalize protocol/method/path, require exactly HTTP method+path for HTTP and `rpc_method` for gRPC, reject duplicate semantic keys, and mutate the slice with normalized values. For this first independent commit, put colon/brace/bracket parameter normalization in an unexported `normalizeServiceTopologyPath`; Task 2 replaces it with the shared topology normalizer without changing behavior.

- [ ] **Step 4: Document the schema and API validation**

Add a commented `service_topology.overrides` block after `code_intelligence` in the schema and add an API handler test that POSTs a valid three-service YAML and receives HTTP 200/`valid:true`; mutate `action` to `guess` and assert HTTP 400 contains `service_topology.overrides`.

- [ ] **Step 5: Run focused and package tests**

Run: `go test ./internal/config/ ./api/ -run 'TestServiceTopology|TestHandleValidate_ServiceTopology' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the configuration contract**

```bash
git add internal/config/types_service_topology.go internal/config/service_topology_test.go internal/config/types.go internal/config/validate.go schema/troubleshooter.schema.yaml api/handler_test.go
git commit -m "feat: add service topology override contract"
```

---

### Task 2: Implement endpoint types and canonical normalization

**Files:**
- Create: `internal/topology/types.go`
- Create: `internal/topology/normalize.go`
- Create: `internal/topology/normalize_test.go`
- Modify: `internal/config/validate.go`

**Interfaces:**
- Produces: `topology.Endpoint`, `CandidateEdge`, `RepositoryStatus`, `Snapshot`, `NormalizePath`, `NormalizeHTTPMethod`, and `Endpoint.SemanticID()`.
- Consumes later: all endpoint scanners, matcher, generator and Wails bridge.

- [ ] **Step 1: Write normalization tests**

Use a table covering colon, brace, Next.js, wildcard, duplicate slash, query and trailing slash forms:

```go
func TestNormalizePath(t *testing.T) {
    cases := map[string]string{
        "/orders/:id": "/orders/{param}",
        "/orders/{orderId}": "/orders/{param}",
        "/orders/[id]": "/orders/{param}",
        "/files/*path": "/files/{wildcard}",
        "https://api.example.com//orders/1?full=true": "/orders/1",
        "/": "/",
    }
    for in, want := range cases {
        if got := NormalizePath(in); got != want { t.Errorf("NormalizePath(%q)=%q want %q", in, got, want) }
    }
}

func TestEndpointSemanticIDIsStable(t *testing.T) {
    e := Endpoint{Repo: "mall-web", Service: "mall-web", Direction: DirectionOutbound, Protocol: "http", Method: "get", Path: "/orders/:id"}
    if got, want := e.SemanticID(), "mall-web:http:outbound:GET:/orders/{param}"; got != want { t.Fatalf("id=%q want %q", got, want) }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/topology/ -run 'TestNormalize|TestEndpointSemantic' -count=1`

Expected: compile failure because package/types are absent.

- [ ] **Step 3: Implement the topology model**

Define exact JSON/YAML-safe types:

```go
type Direction string
const (
    DirectionInbound Direction = "inbound"
    DirectionOutbound Direction = "outbound"
)

type Endpoint struct {
    ID         string    `json:"id" yaml:"id"`
    Repo       string    `json:"repo" yaml:"repo"`
    Service    string    `json:"service" yaml:"service"`
    Direction  Direction `json:"direction" yaml:"direction"`
    Protocol   string    `json:"protocol" yaml:"protocol"`
    Method     string    `json:"method,omitempty" yaml:"method,omitempty"`
    Path       string    `json:"path,omitempty" yaml:"path,omitempty"`
    RPCMethod  string    `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
    TargetHint string    `json:"target_hint,omitempty" yaml:"target_hint,omitempty"`
    Location   string    `json:"location" yaml:"location"`
    Source     string    `json:"source" yaml:"source"`
    Transforms []Transform `json:"transforms,omitempty" yaml:"transforms,omitempty"`
}

type Transform struct {
    Kind string `json:"kind" yaml:"kind"`
    From string `json:"from" yaml:"from"`
    To string `json:"to" yaml:"to"`
}

type CandidateEdge struct {
    FromEndpoint string   `json:"from_endpoint" yaml:"from_endpoint"`
    ToEndpoint   string   `json:"to_endpoint" yaml:"to_endpoint"`
    FromService  string   `json:"from_service" yaml:"from_service"`
    ToService    string   `json:"to_service" yaml:"to_service"`
    Protocol     string   `json:"protocol" yaml:"protocol"`
    Method       string   `json:"method,omitempty" yaml:"method,omitempty"`
    Path         string   `json:"path,omitempty" yaml:"path,omitempty"`
    RPCMethod    string   `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
    Confidence   float64  `json:"confidence" yaml:"confidence"`
    Status       string   `json:"status" yaml:"status"`
    Reasons      []string `json:"reasons" yaml:"reasons"`
    Conflicts    []string `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`
}

type RepositoryStatus struct {
    Repo string `json:"repo" yaml:"repo"`
    State string `json:"state" yaml:"state"`
    Error string `json:"error,omitempty" yaml:"error,omitempty"`
    EndpointCount int `json:"endpoint_count" yaml:"endpoint_count"`
}
type Snapshot struct {
    SchemaVersion string `json:"schema_version" yaml:"schema_version"`
    Endpoints []Endpoint `json:"endpoints" yaml:"endpoints"`
    Edges []CandidateEdge `json:"edges" yaml:"edges"`
    Repositories []RepositoryStatus `json:"repositories" yaml:"repositories"`
}
```

Normalize before ID construction; use only stable semantic fields, not line numbers or hashes. Replace Task 1's `normalizeServiceTopologyPath` call in config validation with `topology.NormalizePath` and delete the local duplicate. `internal/topology` must remain independent of `internal/config`, so the import direction cannot cycle.

- [ ] **Step 4: Run topology tests**

Run: `go test ./internal/topology/ -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the core model**

```bash
git add internal/topology internal/config/validate.go
git commit -m "feat: define normalized topology endpoints"
```

---

### Task 3: Scan frontend, PHP gateway and Nginx endpoint facts

**Files:**
- Create: `internal/analyzer/endpoint_scan.go`
- Create: `internal/analyzer/endpoint_scan_node.go`
- Create: `internal/analyzer/endpoint_scan_php.go`
- Create: `internal/analyzer/endpoint_scan_nginx.go`
- Create: `internal/analyzer/endpoint_scan_frontend_test.go`
- Modify: `internal/analyzer/types.go`

**Interfaces:**
- Consumes: `topology.Endpoint` and normalization from Task 2.
- Produces: `ScanEndpointsContext(ctx, EndpointScanOptions) ([]topology.Endpoint, error)` for node/php/nginx and `RepoAnalysis.Endpoints`.

- [ ] **Step 1: Add minimal offline fixture tests**

The test creates files with `t.TempDir()` and asserts exact endpoint facts:

```go
func TestScanEndpoints_NodeFrontendAndNextRewrite(t *testing.T) {
    root := fixtureRepo(t, map[string]string{
        "src/orders.ts": `axios.get("${API_BASE_URL}/api/orders/123")`,
        "next.config.js": `module.exports={async rewrites(){return [{source:'/api/:path*',destination:'http://mall-bff/internal/:path*'}]}}`,
    })
    got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo:"mall-web", Services:[]string{"mall-web"}, Stack:"node", Framework:"next.js", RepoPath:root})
    if err != nil { t.Fatal(err) }
    assertEndpoint(t, got, topology.DirectionOutbound, "GET", "/api/orders/123", "${API_BASE_URL}", "axios")
    assertTransform(t, got, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
}

func TestScanEndpoints_LaravelAndNginx(t *testing.T) {
    root := fixtureRepo(t, map[string]string{
        "routes/api.php": `Route::get('/api/orders/{id}', [OrderController::class, 'show']);`,
        "app/Clients/Order.php": `Http::post(env('ORDER_SERVICE_URL').'/internal/orders', $body);`,
        "nginx.conf": `location /api/ { rewrite ^/api/(.*)$ /internal/$1 break; proxy_pass http://mall-bff; }`,
    })
    got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo:"mall-bff", Services:[]string{"mall-bff"}, Stack:"php", Framework:"laravel", RepoPath:root})
    if err != nil { t.Fatal(err) }
    assertEndpoint(t, got, topology.DirectionInbound, "GET", "/api/orders/{param}", "", "laravel-route")
    assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/orders", "ORDER_SERVICE_URL", "laravel-http")
    assertTransform(t, got, "rewrite", "/api/{wildcard}", "/internal/{wildcard}")
}
```

Define the shared test helpers in `endpoint_scan_frontend_test.go`; backend tests in Task 4 reuse them because both files use package `analyzer`:

```go
func fixtureRepo(t *testing.T, files map[string]string) string {
    t.Helper()
    root := t.TempDir()
    for rel, body := range files {
        path := filepath.Join(root, filepath.FromSlash(rel))
        if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
        if err := os.WriteFile(path, []byte(body), 0o644); err != nil { t.Fatal(err) }
    }
    return root
}

func assertEndpoint(t *testing.T, got []topology.Endpoint, direction topology.Direction, method, path, hint, source string) {
    t.Helper()
    for _, endpoint := range got {
        if endpoint.Direction == direction && endpoint.Method == method && endpoint.Path == path && endpoint.TargetHint == hint && endpoint.Source == source { return }
    }
    t.Fatalf("endpoint not found: direction=%s method=%s path=%s hint=%s source=%s in %#v", direction, method, path, hint, source, got)
}

func assertTransform(t *testing.T, got []topology.Endpoint, kind, from, to string) {
    t.Helper()
    for _, endpoint := range got { for _, transform := range endpoint.Transforms {
        if transform.Kind == kind && transform.From == from && transform.To == to { return }
    }}
    t.Fatalf("transform %s %s -> %s not found in %#v", kind, from, to, got)
}

func assertRPC(t *testing.T, got []topology.Endpoint, direction topology.Direction, method string) {
    t.Helper()
    for _, endpoint := range got { if endpoint.Direction == direction && endpoint.Protocol == "grpc" && endpoint.RPCMethod == method { return } }
    t.Fatalf("rpc endpoint %s not found in %#v", method, got)
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/analyzer/ -run 'TestScanEndpoints_Node|TestScanEndpoints_Laravel' -count=1`

Expected: compile failure for `EndpointScanOptions` and `ScanEndpointsContext`.

- [ ] **Step 3: Implement the scanner dispatcher**

Define:

```go
type EndpointScanOptions struct {
    Repo, Stack, Framework, RepoPath string
    Services, IncludePaths []string
}

func ScanEndpointsContext(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error)
```

The dispatcher runs the stack scanner plus Nginx scan, fills a single effective service when exactly one exists, normalizes every endpoint, constructs IDs, removes exact duplicates, sorts by semantic ID, and returns context cancellation as an error. Files larger than 512 KiB and ignored dependency/build directories must follow existing analyzer walker rules.

- [ ] **Step 4: Implement exact first-release patterns**

Node: string-literal `fetch`, axios verbs, axios config `{method,url}`, Express/Nest inbound decorators, and Next.js `source/destination` rewrites. PHP: Laravel `Route::<verb>` and `Http::<verb>` with string/env concatenation. Nginx: `location`, `proxy_pass`, `rewrite`; a rewrite produces a `Transform` and `proxy_pass` produces a target hint, not a fabricated endpoint target.

- [ ] **Step 5: Add endpoint results to analysis JSON**

Add:

```go
Endpoints []topology.Endpoint `json:"endpoints,omitempty"`
```

to `RepoAnalysis`, importing `internal/topology` without changing existing JSON fields.

- [ ] **Step 6: Run analyzer tests**

Run: `go test ./internal/analyzer/ -run 'TestScanEndpoints|TestScanAPIRoutes' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit frontend/gateway scanning**

```bash
git add internal/analyzer/endpoint_scan*.go internal/analyzer/types.go
git commit -m "feat: scan frontend and gateway endpoints"
```

---

### Task 4: Scan Go, Java, Python and gRPC endpoint facts

**Files:**
- Create: `internal/analyzer/endpoint_scan_go.go`
- Create: `internal/analyzer/endpoint_scan_java.go`
- Create: `internal/analyzer/endpoint_scan_python.go`
- Create: `internal/analyzer/endpoint_scan_backend_test.go`
- Modify: `internal/analyzer/endpoint_scan.go`

**Interfaces:**
- Consumes: `EndpointScanOptions` and `topology.Endpoint` from Tasks 2–3.
- Produces: backend HTTP/Feign/gRPC inbound and outbound facts through the same `ScanEndpointsContext` entrypoint.

- [ ] **Step 1: Write backend fixture tests**

Use exact source literals and assert both directions:

```go
func TestScanEndpoints_GoHTTPAndGRPC(t *testing.T) {
    root := fixtureRepo(t, map[string]string{
        "internal/http/order.go": `r.GET("/internal/orders/:id", showOrder)`,
        "internal/client/user.go": `req, _ := http.NewRequest("POST", "http://user-service/internal/users", body)`,
        "proto/order.proto": `service OrderService { rpc GetOrder (GetOrderRequest) returns (Order); }`,
    })
    got, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo:"mall-order", Services:[]string{"mall-order"}, Stack:"go", RepoPath:root})
    if err != nil { t.Fatal(err) }
    assertEndpoint(t, got, topology.DirectionInbound, "GET", "/internal/orders/{param}", "", "gin-route")
    assertEndpoint(t, got, topology.DirectionOutbound, "POST", "/internal/users", "user-service", "go-http")
    assertRPC(t, got, topology.DirectionInbound, "OrderService/GetOrder")
}

func TestScanEndpoints_JavaFeignAndPythonFastAPI(t *testing.T) {
    javaRoot := fixtureRepo(t, map[string]string{"src/OrderClient.java": `@FeignClient(name="order-service") interface C { @PostMapping("/internal/orders") void create(); }`})
    javaGot, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo:"mall-bff", Services:[]string{"mall-bff"}, Stack:"java", RepoPath:javaRoot})
    if err != nil { t.Fatal(err) }
    assertEndpoint(t, javaGot, topology.DirectionOutbound, "POST", "/internal/orders", "order-service", "feign")

    pyRoot := fixtureRepo(t, map[string]string{"app.py": `@app.get('/profiles/{user_id}')\ndef profile(user_id): pass\nrequests.get(PROFILE_URL + '/internal/profile')`})
    pyGot, err := ScanEndpointsContext(context.Background(), EndpointScanOptions{Repo:"profile", Services:[]string{"profile"}, Stack:"python", RepoPath:pyRoot})
    if err != nil { t.Fatal(err) }
    assertEndpoint(t, pyGot, topology.DirectionInbound, "GET", "/profiles/{param}", "", "fastapi-route")
    assertEndpoint(t, pyGot, topology.DirectionOutbound, "GET", "/internal/profile", "PROFILE_URL", "python-requests")
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/analyzer/ -run 'TestScanEndpoints_Go|TestScanEndpoints_Java' -count=1`

Expected: tests fail because those stacks return no endpoints.

- [ ] **Step 3: Implement Go scanner**

Support existing Gin/Echo/net-http/Kratos inbound route forms, `http.Get/Post`, `http.NewRequest`, common Kratos HTTP client literals, `.proto service/rpc` inbound declarations, and gRPC client calls whose fully qualified service/method is statically present. Preserve target host/service hint and source file line.

- [ ] **Step 4: Implement Java scanner**

Support Spring mapping annotations including class prefixes, `@FeignClient(name|value|url)` combined with method mappings, Spring Gateway Java/YAML route predicates and filters, and `.proto` service/rpc declarations. A Feign client is outbound even when its method annotation resembles an inbound route.

- [ ] **Step 5: Implement Python scanner**

Support Flask/FastAPI/Django literal routes and requests/httpx verb calls with string literals or one named base variable plus a literal suffix. Dynamic expressions that cannot reveal a method/path produce no formal endpoint.

- [ ] **Step 6: Run analyzer race tests**

Run: `go test ./internal/analyzer/ -race -run 'TestScanEndpoints' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit backend scanning**

```bash
git add internal/analyzer/endpoint_scan_go.go internal/analyzer/endpoint_scan_java.go internal/analyzer/endpoint_scan_python.go internal/analyzer/endpoint_scan_backend_test.go internal/analyzer/endpoint_scan.go
git commit -m "feat: scan backend and grpc endpoints"
```

---

### Task 5: Match cross-repository endpoints with deterministic confidence

**Files:**
- Create: `internal/topology/match.go`
- Create: `internal/topology/match_test.go`

**Interfaces:**
- Produces: `Match(MatchInput) MatchResult`.
- Consumes: normalized endpoints and caller-supplied service descriptors; does not import analyzer/config and does not call an LLM.

- [ ] **Step 1: Write scoring and ambiguity tests**

```go
func TestMatchExactHostMethodPathIsAutomatic(t *testing.T) {
    result := Match(MatchInput{
        Endpoints: []Endpoint{
            endpoint("web", "web", DirectionOutbound, "GET", "/api/orders/{param}", "mall-bff"),
            endpoint("bff", "mall-bff", DirectionInbound, "GET", "/api/orders/{param}", ""),
        },
        Services: []ServiceDescriptor{{Repo:"bff", Service:"mall-bff", Aliases:[]string{"mall-bff", "mall-bff.default.svc"}}},
    })
    edge := onlyEdge(t, result)
    if edge.Status != "automatic" || edge.Confidence < 0.95 { t.Fatalf("edge=%#v", edge) }
}

func TestMatchUniquePathWithUnknownVariableIsCandidate(t *testing.T) {
    result := Match(MatchInput{Endpoints: []Endpoint{
        endpoint("web", "web", DirectionOutbound, "GET", "/api/orders", "API_BASE_URL"),
        endpoint("bff", "mall-bff", DirectionInbound, "GET", "/api/orders", ""),
    }})
    edge := onlyEdge(t, result)
    if edge.Status != "candidate" || edge.Confidence < .60 || edge.Confidence >= .85 { t.Fatalf("edge=%#v", edge) }
}

func TestMatchDuplicateRoutesWithoutHostDoNotPromote(t *testing.T) {
    result := Match(MatchInput{Endpoints: duplicateInboundRoutesWithUnknownOutbound()})
    if len(result.Edges) != 0 || len(result.Hints) != 2 { t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints) }
    for _, edge := range result.Hints { if edge.Confidence >= .60 { t.Fatalf("ambiguous edge promoted: %#v", edge) } }
}
```

Add these local helpers to `match_test.go`:

```go
func endpoint(repo, service string, direction Direction, method, path, hint string) Endpoint {
    value := Endpoint{Repo:repo, Service:service, Direction:direction, Protocol:"http", Method:method, Path:path, TargetHint:hint}
    value.ID = value.SemanticID()
    return value
}
func onlyEdge(t *testing.T, result MatchResult) CandidateEdge {
    t.Helper()
    if len(result.Edges) != 1 { t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints) }
    return result.Edges[0]
}
func duplicateInboundRoutesWithUnknownOutbound() []Endpoint {
    return []Endpoint{
        endpoint("web", "web", DirectionOutbound, "GET", "/health", "API_URL"),
        endpoint("a", "service-a", DirectionInbound, "GET", "/health", ""),
        endpoint("b", "service-b", DirectionInbound, "GET", "/health", ""),
    }
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/topology/ -run TestMatch -count=1`

Expected: compile failure for matcher types/functions.

- [ ] **Step 3: Implement matcher interfaces**

```go
type ServiceDescriptor struct {
    Repo, Service, Role string
    Aliases, Hosts []string
}
type MatchInput struct { Endpoints []Endpoint; Services []ServiceDescriptor }
type MatchResult struct { Edges []CandidateEdge; Hints []CandidateEdge }
func Match(input MatchInput) MatchResult
```

Candidate generation must first match compatible protocol and method, then exact normalized path or an explicit transform chain. Scoring is rule-based: exact target alias + exact route `0.98`; exact host + proven rewrite `0.90`; unresolved variable + unique exact route `0.76`; exact route duplicated across services without target evidence `0.55`; method conflict is excluded. Clamp to `[0,1]`, sort deterministically, and record each applied rule in `Reasons`/`Conflicts`.

- [ ] **Step 4: Add transform and gRPC tests**

Assert a Next/Spring/Nginx prefix rewrite scores `0.90` and a fully qualified gRPC method plus service alias scores `0.98`. Assert an unproven fuzzy path remains in `Hints` below `0.60`.

- [ ] **Step 5: Run topology tests**

Run: `go test ./internal/topology/ -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit matching**

```bash
git add internal/topology/match.go internal/topology/match_test.go
git commit -m "feat: match cross-repository endpoints"
```

---

### Task 6: Merge human overrides and project the service graph

**Files:**
- Create: `internal/topology/merge.go`
- Create: `internal/topology/merge_test.go`
- Create: `internal/topology/project.go`
- Create: `internal/topology/project_test.go`

**Interfaces:**
- Consumes: matcher edges and `config.ServiceTopologyOverride` converted by a small adapter struct.
- Produces: `Merge(edges, overrides) []CandidateEdge`, `ProjectServiceGraph(snapshot) ServiceGraph`, and `FindPaths(graph, Query) []Path`.

- [ ] **Step 1: Write merge-priority and stale tests**

```go
func TestMergeOverridePriorityAndStale(t *testing.T) {
    auto := []CandidateEdge{edge("web", "bff", "GET", "/api/orders", .98, "automatic")}
    got := Merge(auto, []Override{
        {Action:"reject", FromService:"web", ToService:"bff", Protocol:"http", Method:"GET", Path:"/api/orders"},
        {Action:"add", FromService:"web", ToService:"order", Protocol:"http", Method:"GET", Path:"/api/orders"},
        {Action:"confirm", FromService:"web", ToService:"profile", Protocol:"http", Method:"GET", Path:"/profile"},
    })
    assertStatus(t, got, "web", "bff", "rejected")
    assertStatus(t, got, "web", "order", "manual")
    assertStatus(t, got, "web", "profile", "stale")
}
```

- [ ] **Step 2: Write bounded-path tests**

```go
func TestFindPathsBoundsDepthAndCycles(t *testing.T) {
    graph := graphOf("web>bff", "bff>order", "order>user", "user>bff")
    paths := FindPaths(graph, Query{StartService:"web", MaxDepth:3})
    if len(paths) == 0 { t.Fatal("expected a path") }
    for _, path := range paths {
        if len(path.Edges) > 3 { t.Fatalf("depth exceeded: %#v", path) }
        assertNoRepeatedService(t, path)
    }
}
```

Add explicit test helpers in `merge_test.go`/`project_test.go`:

```go
func edge(from, to, method, path string, confidence float64, status string) CandidateEdge {
    return CandidateEdge{FromService:from, ToService:to, Protocol:"http", Method:method, Path:path, Confidence:confidence, Status:status, Reasons:[]string{"fixture:"+method+":"+path}}
}
func assertStatus(t *testing.T, edges []CandidateEdge, from, to, status string) {
    t.Helper()
    for _, candidate := range edges { if candidate.FromService == from && candidate.ToService == to && candidate.Status == status { return } }
    t.Fatalf("edge %s -> %s status=%s not found in %#v", from, to, status, edges)
}
func graphOf(specs ...string) ServiceGraph {
    graph := ServiceGraph{Services:map[string]ServiceNode{}}
    for _, spec := range specs {
        parts := strings.Split(spec, ">")
        graph.Edges = append(graph.Edges, ServiceEdge{From:parts[0], To:parts[1], Status:"automatic", Confidence:.98, Routes:[]RouteRef{{Protocol:"http", Method:"GET", Path:"/fixture", EndpointEdge:spec}}})
        graph.Services[parts[0]] = ServiceNode{Service:parts[0]}
        graph.Services[parts[1]] = ServiceNode{Service:parts[1]}
    }
    return graph
}
func assertNoRepeatedService(t *testing.T, path Path) {
    t.Helper()
    seen := map[string]bool{}
    for _, service := range path.Services { if seen[service] { t.Fatalf("cycle in %#v", path) }; seen[service] = true }
}
```

- [ ] **Step 3: Verify RED**

Run: `go test ./internal/topology/ -run 'TestMerge|TestFindPaths' -count=1`

Expected: compile failure for merge/projection functions.

- [ ] **Step 4: Implement override and graph contracts**

Define topology-local `Override` with the same semantic fields as config. Rejected edges remain in endpoint evidence but are excluded from service projection. Confirm without a matching current edge becomes stale; add becomes manual. Formal graph includes automatic, confirmed and manual only. Deduplicate service edges while retaining all endpoint evidence references.

Define:

```go
type ServiceNode struct { Service, Repo, Role string; Upstream, Downstream []string }
type RouteRef struct { Protocol, Method, Path, RPCMethod, EndpointEdge string }
type ServiceEdge struct { From, To, Status string; Confidence float64; Routes []RouteRef }
type ServiceGraph struct { Services map[string]ServiceNode; Edges []ServiceEdge }
type Query struct { StartService, Protocol, Method, Path string; MaxDepth int }
type Path struct { Services []string; Edges []ServiceEdge; Score float64 }
```

`ProjectServiceGraph` deduplicates each service pair but preserves every protocol route in `Routes`. `FindPaths` defaults `MaxDepth` to 3, filters the first hop by method/path or gRPC method when supplied, detects cycles per path, and ranks confirmed/manual above automatic, then higher confidence, then shorter path, then lexical service sequence.

- [ ] **Step 5: Run topology package tests**

Run: `go test ./internal/topology/ -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit merge and projection**

```bash
git add internal/topology/merge.go internal/topology/merge_test.go internal/topology/project.go internal/topology/project_test.go
git commit -m "feat: merge topology decisions and project paths"
```

---

### Task 7: Integrate topology into analyzer pipeline and HEAD-aware cache

**Files:**
- Modify: `internal/analyzerpipe/pipeline.go`
- Modify: `internal/analyzerpipe/pipeline_test.go`
- Modify: `internal/agent/auto_analyze.go`
- Modify: `internal/agent/auto_analyze_test.go`
- Create: `internal/agent/auto_analyze_topology_test.go`

**Interfaces:**
- Consumes: scanner, matcher, overrides and projection.
- Produces: `analyzerpipe.Result.Topology topology.Snapshot`; cached multi-target deployments reuse the same snapshot.

- [ ] **Step 1: Write a three-repository pipeline test**

Create local Node/PHP/Go fixture repos with a web request, BFF route/client, and backend route. Run `analyzerpipe.Run` with explicit `RepoPaths` and assert:

```go
if len(result.Topology.Edges) != 2 { t.Fatalf("edges=%#v", result.Topology.Edges) }
assertTopologyEdge(t, result.Topology, "mall-web", "mall-bff")
assertTopologyEdge(t, result.Topology, "mall-bff", "mall-order")
for _, edge := range result.Topology.Edges {
    if len(edge.Reasons) == 0 { t.Fatalf("edge lacks evidence: %#v", edge) }
}
```

Add a partial fixture where one repo path is missing and assert the missing repo has state `failed` while valid edges from other repos remain.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/analyzerpipe/ -run TestRun_ServiceTopology -count=1`

Expected: compile failure because `Result.Topology` is absent.

- [ ] **Step 3: Wire scanning and matching into the pipeline**

After existing API/dependency/schema scans, call `ScanEndpointsContext`; record error/status per repo without returning a global error. When at least two runnable service repos have endpoints, build service descriptors from repo name, effective services, role, environment API/web domains, and statically known host aliases; call `Match`, convert config overrides, call `Merge`, and populate snapshot. When activation conditions fail, return schema version plus repository states and no edges.

- [ ] **Step 4: Make cache keys HEAD- and contract-aware**

Extend the auto-analyze cache key with `topology.SchemaVersion` and, for every sorted repo path, `git -C <path> rev-parse HEAD`; use `missing` or `not-git` sentinels when unavailable. Add tests proving same paths/same HEAD hit, one changed HEAD misses, and four target deployments still scan once.

- [ ] **Step 5: Run focused race tests**

Run: `go test ./internal/analyzerpipe/ ./internal/agent/ -race -run 'Topology|AutoAnalyzeCache' -count=1`

Expected: PASS.

- [ ] **Step 6: Run the full Go suite**

Run: `go test ./... -race`

Expected: PASS; the existing macOS linker warning is non-fatal.

- [ ] **Step 7: Commit pipeline integration**

```bash
git add internal/analyzerpipe internal/agent/auto_analyze.go internal/agent/auto_analyze_test.go internal/agent/auto_analyze_topology_test.go
git commit -m "feat: build topology during auto analysis"
```

---

### Task 8: Generate one authoritative topology and compatibility dependency map

**Files:**
- Create: `templates/workspace/skills/routing/references/service-topology.yaml.tmpl`
- Create: `templates/workspace/skills/routing/references/endpoint-evidence.yaml.tmpl`
- Modify: `templates/workspace/skills/routing/references/service-dependency-map.yaml.tmpl`
- Modify: `templates/workspace/skills/routing/SKILL.md.tmpl`
- Modify: `internal/generator/generator.go`
- Modify: `internal/generator/funcs.go`
- Modify: `internal/generator/generator_test.go`
- Modify: `internal/generator/preserve_test.go`

**Interfaces:**
- Consumes: `analyzerpipe.Result.Topology`.
- Produces: two routing artifacts and a `service-dependency-map.yaml` projected from the same formal graph.

- [ ] **Step 1: Write generator golden assertions**

Load a synthetic analysis result with one automatic, one candidate, one rejected and one stale edge. Assert:

```go
serviceTopology := readFile(t, filepath.Join(out, "skills/routing/references/service-topology.yaml"))
for _, want := range []string{`from: "mall-web"`, `status: "automatic"`} {
    if !strings.Contains(serviceTopology, want) { t.Fatalf("service topology missing %q:\n%s", want, serviceTopology) }
}
if strings.Contains(serviceTopology, `to: "legacy-bff"`) { t.Fatalf("formal topology contains rejected edge:\n%s", serviceTopology) }

evidence := readFile(t, filepath.Join(out, "skills/routing/references/endpoint-evidence.yaml"))
for _, want := range []string{`status: "rejected"`, `status: "stale"`, `src/api/order.ts:18`} {
    if !strings.Contains(evidence, want) { t.Fatalf("endpoint evidence missing %q:\n%s", want, evidence) }
}

deps := readFile(t, filepath.Join(out, "skills/routing/references/service-dependency-map.yaml"))
if !strings.Contains(deps, `- "mall-bff"`) || strings.Contains(deps, `legacy-bff`) { t.Fatalf("dependency projection drifted:\n%s", deps) }
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/generator/ -run TestGenerate_ServiceTopology -count=1`

Expected: failure because new artifacts/context data are absent.

- [ ] **Step 3: Load topology into generator context**

Add `Topology topology.Snapshot` and `ServiceGraph topology.ServiceGraph` to `generator.Context`. `LoadAnalysisResult` copies the snapshot and projects the graph once. Empty topology must render valid empty documents, not omit expected routing references.

- [ ] **Step 4: Render formal and evidence views**

`service-topology.yaml` includes only automatic/confirmed/manual service edges, service role/repo and endpoint-edge references. `endpoint-evidence.yaml` includes every endpoint and automatic/candidate/confirmed/rejected/manual/stale edge with confidence and reasons. Sort services, endpoints, edges and reason arrays for reproducible output.

- [ ] **Step 5: Replace dependency-map downstream source**

Add template helpers `topologyDownstreamsForService` and `topologyUpstreamsForService`. When a topology snapshot was attempted, use formal projected edges even if empty; only fall back to old `DownstreamCallsByRepo` when no topology analysis exists, preserving older analysis JSON compatibility.

- [ ] **Step 6: Protect human YAML overrides across generation**

Extend preserve tests to prove `service_topology.overrides` in the source YAML survives generate/apply unchanged and is not replaced by candidate cache data.

- [ ] **Step 7: Run generator tests**

Run: `go test ./internal/generator/ -race -run 'ServiceTopology|ServiceDependency|Preserve' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit generated topology artifacts**

```bash
git add templates/workspace/skills/routing internal/generator
git commit -m "feat: generate service topology evidence"
```

---

### Task 9: Add the service-topology-query skill and robot handoff

**Files:**
- Create: `templates/workspace/skills/service-topology-query/SKILL.md.tmpl`
- Create: `templates/workspace/skills/service-topology-query/scripts/query.py`
- Create: `templates/workspace/skills/service-topology-query/scripts/test_query.py`
- Modify: `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
- Modify: `templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl`
- Modify: `internal/config/validate.go`
- Modify: `internal/generator/render_walk.go`
- Modify: `internal/generator/chain_integrity_test.go`

**Interfaces:**
- Consumes: generated routing references.
- Produces: `query.py --service/--method/--path/--max-depth/--json` and a bounded, read-only robot workflow.

- [ ] **Step 1: Write Python query tests**

Create fixture YAML in the test temp directory and cover URL start, service start, multiple candidates, cycle, default depth 3, explicit depth 1 and missing files:

```python
def test_query_by_failed_request_returns_ranked_three_hop_path(tmp_path):
    write_fixture(tmp_path, edges=[
        edge("mall-web", "mall-bff", "GET", "/api/orders/{param}", 0.98, "confirmed"),
        edge("mall-bff", "mall-order", "POST", "/internal/orders", 0.90, "automatic"),
    ])
    result = run_query(tmp_path, "--service", "mall-web", "--method", "GET", "--path", "/api/orders/123", "--json")
    assert result["paths"][0]["services"] == ["mall-web", "mall-bff", "mall-order"]
    assert result["paths"][0]["edges"][0]["evidence"]

def test_query_marks_missing_topology_as_fallback(tmp_path):
    result = run_query(tmp_path, "--service", "mall-web", "--json")
    assert result["status"] == "unavailable"
    assert result["fallback"] == "routing_rg_read"
    assert result["paths"] == []
    assert result["query"]["service"] == "mall-web"
    assert result["warnings"]
```

Define the fixture/runner helpers in the same test module:

```python
import json
import subprocess
import sys
from pathlib import Path
import yaml

SCRIPT = Path(__file__).with_name("query.py")

def edge(source, target, method, path, confidence, status):
    return {
        "from": source, "to": target, "protocol": "http", "method": method,
        "path": path, "confidence": confidence, "status": status,
        "endpoint_edges": [f"{source}:{method}:{path}>{target}"],
    }

def write_fixture(root, edges):
    refs = root / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    (refs / "service-topology.yaml").write_text(yaml.safe_dump({"services": {}, "edges": edges}), encoding="utf-8")
    evidence = {"edges": [{"id": item["endpoint_edges"][0], "location": "fixture:1", "reasons": ["fixture"]} for item in edges]}
    (refs / "endpoint-evidence.yaml").write_text(yaml.safe_dump(evidence), encoding="utf-8")

def run_query(root, *args):
    proc = subprocess.run([sys.executable, str(SCRIPT), "--workspace", str(root), *args], check=True, text=True, capture_output=True)
    return json.loads(proc.stdout)
```

- [ ] **Step 2: Verify RED**

Run: `python3 -m pytest templates/workspace/skills/service-topology-query/scripts/test_query.py -q`

Expected: failure because `query.py` is absent.

- [ ] **Step 3: Implement the query CLI**

Use `yaml.safe_load`, never shell out and never modify files. Normalize request paths with the same documented parameter rules, read formal paths and endpoint evidence, bound depth to `1..5` with default 3, detect cycles, rank confirmed/manual then confidence then length, and emit a stable JSON object with `status`, `query`, `paths`, `warnings`, and `fallback`.

- [ ] **Step 4: Write skill instructions and fallback**

The skill must state that topology is navigation evidence, not root cause; runtime trace wins on conflict; stale/candidate relationships must be labeled; after selecting a repository/endpoint, use `code-intelligence-query` when available and otherwise `rg/read`. It must not claim async event coverage.

- [ ] **Step 5: Integrate incident/frontend workflows**

In frontend handoff, query topology after extracting failed method/path. In incident Step 4, use topology paths before cascade checks, then verify each hop through trace/log/runtime. Limit recursion to three hops and keep the existing dependency-map fallback.

- [ ] **Step 6: Gate skill generation and integrity**

Add `service-topology-query` to known skills. Generate it when routing is enabled and at least two runnable service repos exist; respect a non-empty whitelist. Add chain integrity tests requiring routing references and the frontend/incident handoff wording.

- [ ] **Step 7: Run skill and generator tests**

Run: `python3 -m pytest templates/workspace/skills/service-topology-query/scripts/test_query.py -q && scripts/test-skill-scripts.sh && go test ./internal/generator/ ./internal/config/ -run 'Topology|ChainIntegrity' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit query skill**

```bash
git add templates/workspace/skills/service-topology-query templates/workspace/skills/incident-investigator/SKILL.md.tmpl templates/workspace/skills/frontend-repro-investigator/SKILL.md.tmpl internal/config/validate.go internal/generator/render_walk.go internal/generator/chain_integrity_test.go
git commit -m "feat: query service topology during incidents"
```

---

### Task 10: Expose topology analysis through Wails and wizard YAML state

**Files:**
- Create: `cmd/tshoot-desktop/bindings_topology.go`
- Create: `cmd/tshoot-desktop/bindings_topology_test.go`
- Modify: `web/src/lib/bridge.ts`
- Modify: `web/src/lib/yamlGenerator.ts`
- Modify: `web/src/lib/yamlGenerator.test.ts`
- Modify: `web/src/lib/yamlImporter.ts`
- Modify: `web/src/lib/yamlImporter.test.ts`
- Modify: `web/src/lib/useWizardDraft.ts`
- Modify generated: `web/wailsjs/go/main/App.js`
- Modify generated: `web/wailsjs/go/main/App.d.ts`
- Modify generated: `web/wailsjs/go/models.ts`

**Interfaces:**
- Produces: Wails `AnalyzeServiceTopology(yamlText string, repoPaths map[string]string) (*topology.Snapshot, error)`; web `analyzeServiceTopology`; `ServiceTopologyState` round-trip.
- Consumes: auto-analyze/pipeline snapshot and config overrides.

- [ ] **Step 1: Write binding tests**

Test nil Wails context with local fixture repos, invalid YAML, fewer than two service repos, missing path partial result, and JSON serialization of confidence/reasons:

```go
yamlText, paths := desktopTopologyFixture(t)
snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, paths)
if err != nil { t.Fatal(err) }
if len(snapshot.Edges) != 2 { t.Fatalf("snapshot=%#v", snapshot) }
if snapshot.SchemaVersion == "" { t.Fatal("missing schema version") }
```

Define the test fixture locally:

```go
func desktopTopologyFixture(t *testing.T) (string, map[string]string) {
    t.Helper()
    data, err := os.ReadFile(filepath.Join("..", "..", "examples", "three-tier-troubleshooter.yaml"))
    if err != nil { t.Fatal(err) }
    root := t.TempDir()
    paths := map[string]string{
        "mall-web": filepath.Join(root, "mall-web"),
        "mall-bff": filepath.Join(root, "mall-bff"),
        "mall-order": filepath.Join(root, "mall-order"),
    }
    writeTopologyFixtureFile(t, paths["mall-web"], "src/orders.ts", `axios.get("http://mall-bff/api/orders/123")`)
    writeTopologyFixtureFile(t, paths["mall-bff"], "routes/api.php", `Route::get('/api/orders/{id}', fn () => null);`)
    writeTopologyFixtureFile(t, paths["mall-bff"], "app/Clients/Order.php", `Http::post('http://mall-order/internal/orders', []);`)
    writeTopologyFixtureFile(t, paths["mall-order"], "internal/http/order.go", `package http\nfunc routes(r Router) { r.POST("/internal/orders", createOrder) }`)
    return string(data), paths
}
func writeTopologyFixtureFile(t *testing.T, root, rel, body string) {
    t.Helper()
    path := filepath.Join(root, filepath.FromSlash(rel))
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(path, []byte(body), 0o644); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./cmd/tshoot-desktop/ -run TestAnalyzeServiceTopology -count=1`

Expected: compile failure because the binding is absent.

- [ ] **Step 3: Implement the binding**

Load/validate YAML, expand repo paths exactly like CodeGraph retry, call `RunAutoAnalyze`, return its topology snapshot, and invalidate the auto-analyze cache only for explicit refresh. Progress events must be guarded when runtime context is nil. Never write YAML or business repositories in the binding.

- [ ] **Step 4: Add web state and YAML round-trip tests**

Define:

```ts
export interface ServiceTopologyOverrideState {
  action: 'confirm' | 'reject' | 'add'
  fromService: string
  toService: string
  protocol: 'http' | 'grpc'
  method?: string
  path?: string
  rpcMethod?: string
}
export interface ServiceTopologyState { overrides: ServiceTopologyOverrideState[] }
```

Assert generated YAML emits only overrides, old YAML defaults to an empty array, import preserves all three actions, draft save/restore retains overrides, and endpoint/candidate scan data never enters YAML.

- [ ] **Step 5: Refresh Wails bindings and keep diffs semantic**

Run `make wails-gen`, then inspect generated changes. Keep only the new method/type additions; do not commit unrelated formatter churn.

- [ ] **Step 6: Run binding and web data tests**

Run: `go test ./cmd/tshoot-desktop/ -run TestAnalyzeServiceTopology -count=1 && cd web && npm test -- --run src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts`

Expected: PASS.

- [ ] **Step 7: Commit binding and state flow**

```bash
git add cmd/tshoot-desktop/bindings_topology.go cmd/tshoot-desktop/bindings_topology_test.go web/src/lib/bridge.ts web/src/lib/yamlGenerator.ts web/src/lib/yamlGenerator.test.ts web/src/lib/yamlImporter.ts web/src/lib/yamlImporter.test.ts web/src/lib/useWizardDraft.ts web/wailsjs/go
git commit -m "feat: expose topology analysis to the wizard"
```

---

### Task 11: Build the topology confirmation workbench

**Files:**
- Create: `web/src/components/ServiceTopologyPanel.vue`
- Create: `web/src/components/ServiceTopologyPanel.test.ts`
- Create: `web/src/lib/useServiceTopology.ts`
- Create: `web/src/lib/useServiceTopology.test.ts`
- Modify: `web/src/pages/InitPage.vue`

**Interfaces:**
- Consumes: `analyzeServiceTopology`, snapshot models, repo paths and YAML override state.
- Produces: service graph summary, candidate evidence panel, confirm/reject/retarget/add actions, explicit refresh and YAML state updates.

- [ ] **Step 1: Write component behavior tests**

Mount with one automatic, one candidate, one rejected and one stale edge. Assert visible text status plus color class, code locations/reasons, and accessible buttons. Test:

```ts
const wrapper = mount(ServiceTopologyPanel, {
  props: {
    snapshot: makeTopologySnapshot({
      edges: [
        makeTopologyEdge('web-bff', 'mall-web', 'mall-bff', 'automatic', 0.98),
        makeTopologyEdge('bff-order', 'mall-bff', 'mall-order', 'candidate', 0.76),
        makeTopologyEdge('web-legacy', 'mall-web', 'legacy-bff', 'rejected', 0.98),
        makeTopologyEdge('bff-profile', 'mall-bff', 'profile', 'stale', 1),
      ],
    }),
    overrides: [],
    loading: false,
  },
})
await wrapper.get('[data-edge="bff-order"]').trigger('click')
expect(wrapper.text()).toContain('app/Clients/OrderClient.php:31')
await wrapper.get('[data-action="confirm"]').trigger('click')
expect(wrapper.emitted('update:overrides')?.[0][0]).toEqual([
  expect.objectContaining({ action: 'confirm', fromService: 'mall-bff', toService: 'mall-order' }),
])
```

Define typed helpers in the test file; do not use `as any`, so binding drift fails the test:

```ts
import { topology } from '../../wailsjs/go/models'

function makeTopologyEdge(id: string, from: string, to: string, status: string, confidence: number): topology.CandidateEdge {
  return topology.CandidateEdge.createFrom({
    from_endpoint: `${id}:out`, to_endpoint: `${id}:in`,
    from_service: from, to_service: to, protocol: 'http', method: 'POST',
    path: '/internal/orders', confidence, status, reasons: ['method_path_exact'], conflicts: [],
  })
}
function makeTopologySnapshot(input: { edges: topology.CandidateEdge[] }): topology.Snapshot {
  const endpoints = input.edges.flatMap((edge) => [
    topology.Endpoint.createFrom({id:edge.from_endpoint, repo:edge.from_service, service:edge.from_service, direction:'outbound', protocol:'http', method:'POST', path:'/internal/orders', location:'app/Clients/OrderClient.php:31', source:'fixture', transforms:[]}),
    topology.Endpoint.createFrom({id:edge.to_endpoint, repo:edge.to_service, service:edge.to_service, direction:'inbound', protocol:'http', method:'POST', path:'/internal/orders', location:'internal/http/order.go:48', source:'fixture', transforms:[]}),
  ])
  return topology.Snapshot.createFrom({schema_version:'1', endpoints, edges:input.edges, repositories:[]})
}
```

Also assert retarget emits reject-old plus add-new, refresh disables all mutation buttons, automatic edges can be rejected, and stale confirmed edges remain visible.

- [ ] **Step 2: Write composable concurrency tests**

`useServiceTopology` must reject overlapping refreshes, ignore stale async completions by generation number, retain current snapshot on refresh failure, and expose an announced error message. Use deferred promises to prove a slow old completion cannot replace a newer result.

- [ ] **Step 3: Verify RED**

Run: `cd web && npm test -- --run src/components/ServiceTopologyPanel.test.ts src/lib/useServiceTopology.test.ts`

Expected: failure because component/composable are absent.

- [ ] **Step 4: Implement the composable**

Return `snapshot`, `loading`, `error`, `refresh`, `confirm`, `reject`, `retarget`, `add` and `clear`. Mutations update only the override array; snapshot statuses are derived for immediate UI feedback and replaced by the server snapshot after refresh.

- [ ] **Step 5: Implement the workbench UI**

Use semantic buttons, visible keyboard focus, text+color status, `aria-live` feedback, 44px minimum controls, responsive wrapping and reduced-motion support. Show service-level nodes/edges on the left and selected endpoint evidence on the right. Candidate review is the primary queue; high-confidence edges remain inspectable/rejectable.

- [ ] **Step 6: Integrate into InitPage**

Show the panel after at least two runnable repos have paths. Reuse the existing repo-path resolver. Persist overrides through draft/import/generate; do not auto-refresh on every keystroke. Disable topology refresh while deploy/CodeGraph retry is active and vice versa.

- [ ] **Step 7: Run web tests and build**

Run: `cd web && npm test -- --run && npm run build`

Expected: all Vitest tests, `vue-tsc --noEmit`, and Vite build PASS.

- [ ] **Step 8: Commit the confirmation workbench**

```bash
git add web/src/components/ServiceTopologyPanel.vue web/src/components/ServiceTopologyPanel.test.ts web/src/lib/useServiceTopology.ts web/src/lib/useServiceTopology.test.ts web/src/pages/InitPage.vue
git commit -m "feat: confirm cross-repository topology"
```

---

### Task 12: Add cross-layer fixtures, documentation, ADR and final verification

**Files:**
- Create: `examples/fake-repos/topology-web/package.json`
- Create: `examples/fake-repos/topology-web/src/orders.ts`
- Create: `examples/fake-repos/topology-bff/routes/api.php`
- Create: `examples/fake-repos/topology-bff/app/Clients/Order.php`
- Create: `examples/fake-repos/topology-order/go.mod`
- Create: `examples/fake-repos/topology-order/internal/http/order.go`
- Create: `internal/agent/topology_e2e_test.go`
- Modify: `examples/three-tier-troubleshooter.yaml`
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Modify: `docs/decisions.md`

**Interfaces:**
- Consumes: complete feature.
- Produces: stable offline regression fixture, user/operator documentation, ADR and final evidence.

- [ ] **Step 1: Add an end-to-end offline fixture**

The fixture must encode this exact chain:

```text
mall-web GET http://mall-bff/api/orders/123
  -> mall-bff GET /api/orders/{id}
  -> mall-order POST /internal/orders
```

Use these exact fixture bodies:

`examples/fake-repos/topology-web/package.json`:

```json
{"name":"topology-web","private":true,"dependencies":{"axios":"1.7.0"}}
```

`examples/fake-repos/topology-web/src/orders.ts`:

```ts
import axios from 'axios'
export const loadOrder = () => axios.get('http://mall-bff/api/orders/123')
```

`examples/fake-repos/topology-bff/routes/api.php`:

```php
<?php
Route::get('/api/orders/{id}', [OrderController::class, 'show']);
```

`examples/fake-repos/topology-bff/app/Clients/Order.php`:

```php
<?php
final class OrderClient { public function create(array $body) { return Http::post('http://mall-order/internal/orders', $body); } }
```

`examples/fake-repos/topology-order/go.mod`:

```go
module example.test/topology-order

go 1.23
```

`examples/fake-repos/topology-order/internal/http/order.go`:

```go
package http
func register(r Router) { r.POST("/internal/orders", createOrder) }
```

The E2E test loads `three-tier-troubleshooter.yaml`, injects `mall-web`, `mall-bff`, and `mall-order` fixture paths, runs auto-analyze and generation, then invokes the Python query script with `GET /api/orders/123`. Assert the returned services are `mall-web`, `mall-bff`, `mall-order`, each edge has locations/reasons, and generated dependency-map downstreams match the same formal graph.

- [ ] **Step 2: Add failure and override E2E cases**

Remove the order repo path and assert deploy/generation succeeds with partial status. Add a confirm override for the BFF/order edge, rerun with unchanged HEADs and assert cache hit plus confirmed status. Change one fixture git HEAD in a temporary copied repo and assert cache miss only for the changed analysis key.

- [ ] **Step 3: Document operation and testing**

README must explain deployment-time scan, workbench confirmation, YAML override ownership, confidence labels, three-hop traversal and CodeGraph handoff. CONTRIBUTING must require offline language fixtures, deterministic reasons, no LLM scoring, partial-failure coverage and synchronized dependency-map projection.

- [ ] **Step 4: Append an ADR**

Append, never rewrite, `跨仓库端点目录作为服务拓扑真源` to `docs/decisions.md`. Record context, selected endpoint-catalog/matcher/override architecture, rejected regex-only/full-graph approaches, service-dependency compatibility projection, and consequences including scanner maintenance and stale override review.

- [ ] **Step 5: Run complete verification**

Run in order:

```bash
go test ./... -race
scripts/check-go-coverage.sh
scripts/test-skill-scripts.sh
cd web && npm test -- --run && npm run build
cd .. && make lint && make build
git diff --check
```

Expected: all commands PASS. The known macOS `LC_DYSYMTAB` linker warning may appear but must not change exit status. If the installer concurrency test flakes, investigate and fix it; do not accept a rerun-only completion claim.

- [ ] **Step 6: Commit final fixtures and documentation**

```bash
git add examples/fake-repos/topology-* examples/three-tier-troubleshooter.yaml internal/agent/topology_e2e_test.go README.md CONTRIBUTING.md docs/decisions.md
git commit -m "test: verify cross-repository service topology"
```

- [ ] **Step 7: Inspect the exact branch diff**

Run:

```bash
BASE=$(git merge-base HEAD feat/codegraph-integration)
git diff --stat "$BASE"..HEAD
git diff --check "$BASE"..HEAD
git status --short
```

Expected: only topology implementation/docs/fixtures are present and the worktree is clean.

- [ ] **Step 8: Request whole-branch review**

Use `superpowers:requesting-code-review` against the merge base. Review must explicitly inspect deterministic scoring, override priority, partial failures, generated-map consistency, YAML persistence, UI race guards and query depth/cycle handling. Address every blocker/high finding, rerun the relevant focused test and then repeat the full verification commands before claiming completion.
