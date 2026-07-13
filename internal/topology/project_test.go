package topology

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProjectServiceGraphKeepsOnlyFormalStatusesAndDeduplicatesPairs(t *testing.T) {
	snapshot := Snapshot{
		Endpoints: []Endpoint{
			{ID: "web:out:orders", Repo: "web-repo", Service: "web"},
			{ID: "bff:in:orders", Repo: "bff-repo", Service: "bff"},
			{ID: "web:out:profile", Repo: "web-repo", Service: "web"},
			{ID: "bff:in:profile", Repo: "bff-repo", Service: "bff"},
		},
		Edges: []CandidateEdge{
			{FromEndpoint: "web:out:orders", ToEndpoint: "bff:in:orders", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders", Status: "automatic", Confidence: .98},
			{FromEndpoint: "web:out:profile", ToEndpoint: "bff:in:profile", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/profile", Status: "confirmed", Confidence: .76},
			{FromService: "web", ToService: "bff", Protocol: "grpc", RPCMethod: "profile.Profile/Get", Status: "manual", Confidence: .70},
			{FromService: "web", ToService: "candidate", Protocol: "http", Method: "GET", Path: "/candidate", Status: "candidate", Confidence: .76},
			{FromService: "web", ToService: "rejected", Protocol: "http", Method: "GET", Path: "/rejected", Status: "rejected", Confidence: .98},
			{FromService: "web", ToService: "stale", Protocol: "http", Method: "GET", Path: "/stale", Status: "stale", Confidence: 0},
		},
	}

	got := ProjectServiceGraph(snapshot)
	if len(got.Edges) != 1 {
		t.Fatalf("formal graph edges=%#v, want one deduplicated pair", got.Edges)
	}
	projected := got.Edges[0]
	if projected.From != "web" || projected.To != "bff" || projected.Status != "manual" || projected.Confidence != .98 {
		t.Fatalf("projected pair=%#v", projected)
	}
	if len(projected.Routes) != 3 {
		t.Fatalf("routes=%#v, want every formal endpoint edge", projected.Routes)
	}
	if got.Services["web"].Repo != "web-repo" || got.Services["bff"].Repo != "bff-repo" {
		t.Fatalf("service repos not projected from endpoint catalog: %#v", got.Services)
	}
	if !reflect.DeepEqual(got.Services["web"].Downstream, []string{"bff"}) {
		t.Fatalf("web downstream=%#v", got.Services["web"].Downstream)
	}
	if !reflect.DeepEqual(got.Services["bff"].Upstream, []string{"web"}) {
		t.Fatalf("bff upstream=%#v", got.Services["bff"].Upstream)
	}

	refs := make([]string, 0, len(projected.Routes))
	for _, route := range projected.Routes {
		refs = append(refs, route.EndpointEdge)
	}
	if !containsString(refs, "web:out:orders>bff:in:orders") || !containsString(refs, "web:out:profile>bff:in:profile") {
		t.Fatalf("endpoint evidence references missing from %#v", refs)
	}
}

func TestProjectServiceGraphCollapsesExactDuplicateRouteReferences(t *testing.T) {
	duplicate := CandidateEdge{
		FromEndpoint: "web:out:orders", ToEndpoint: "bff:in:orders",
		FromService: "web", ToService: "bff", Protocol: "http",
		Method: "GET", Path: "/orders", Status: "automatic", Confidence: .98,
	}

	got := ProjectServiceGraph(Snapshot{Edges: []CandidateEdge{duplicate, duplicate}})
	if len(got.Edges) != 1 {
		t.Fatalf("formal graph edges=%#v, want one service pair", got.Edges)
	}
	if routes := got.Edges[0].Routes; len(routes) != 1 {
		t.Fatalf("duplicate route references were retained: %#v", routes)
	}
}

func TestProjectServiceGraphPreservesSameRouteWithDistinctEndpointEvidence(t *testing.T) {
	first := CandidateEdge{
		FromEndpoint: "web:out:orders:first", ToEndpoint: "bff:in:orders:first",
		FromService: "web", ToService: "bff", Protocol: "http",
		Method: "GET", Path: "/orders", Status: "automatic", Confidence: .98,
	}
	second := first
	second.FromEndpoint = "web:out:orders:second"
	second.ToEndpoint = "bff:in:orders:second"

	got := ProjectServiceGraph(Snapshot{Edges: []CandidateEdge{second, first}})
	if len(got.Edges) != 1 {
		t.Fatalf("formal graph edges=%#v, want one service pair", got.Edges)
	}
	want := []RouteRef{
		{Protocol: "http", Method: "GET", Path: "/orders", EndpointEdge: "web:out:orders:first>bff:in:orders:first"},
		{Protocol: "http", Method: "GET", Path: "/orders", EndpointEdge: "web:out:orders:second>bff:in:orders:second"},
	}
	if !reflect.DeepEqual(got.Edges[0].Routes, want) {
		t.Fatalf("routes=%#v, want distinct endpoint evidence %#v", got.Edges[0].Routes, want)
	}
}

func TestProjectServiceGraphUsesDeterministicRepositoriesAndOrdering(t *testing.T) {
	snapshot := Snapshot{
		Endpoints: []Endpoint{
			{Repo: "z-repo", Service: "shared"},
			{Repo: "a-repo", Service: "shared"},
		},
		Edges: []CandidateEdge{
			{FromService: "shared", ToService: "zeta", Protocol: "http", Method: "GET", Path: "/z", Status: "automatic", Confidence: .90},
			{FromService: "shared", ToService: "alpha", Protocol: "http", Method: "GET", Path: "/a", Status: "automatic", Confidence: .90},
		},
	}

	got := ProjectServiceGraph(snapshot)
	if repo := got.Services["shared"].Repo; repo != "a-repo" {
		t.Fatalf("shared repo=%q want deterministic lexical repo", repo)
	}
	if !reflect.DeepEqual(got.Services["shared"].Downstream, []string{"alpha", "zeta"}) {
		t.Fatalf("downstream order=%#v", got.Services["shared"].Downstream)
	}
	if got.Edges[0].To != "alpha" || got.Edges[1].To != "zeta" {
		t.Fatalf("edge order is not deterministic: %#v", got.Edges)
	}
}

func TestProjectServiceGraphUsesSnapshotServiceMetadataWithoutEndpointFacts(t *testing.T) {
	snapshot := Snapshot{
		Services: []ServiceDescriptor{
			{Repo: "web-repo", Service: "web", Role: "frontend"},
			{Repo: "order-repo", Service: "order", Role: "backend"},
			{Repo: "profile-repo", Service: "profile", Role: "backend"},
		},
		Edges: []CandidateEdge{
			{FromService: "web", ToService: "order", Protocol: "http", Method: "POST", Path: "/orders", Status: "manual", Confidence: 1},
			{FromService: "web", ToService: "profile", Protocol: "http", Method: "GET", Path: "/profile", Status: "stale", Confidence: 0},
		},
	}

	got := ProjectServiceGraph(snapshot)
	if node := got.Services["web"]; node.Repo != "web-repo" || node.Role != "frontend" {
		t.Fatalf("web metadata=%#v", node)
	}
	if node := got.Services["order"]; node.Repo != "order-repo" || node.Role != "backend" {
		t.Fatalf("manual target metadata=%#v", node)
	}
	if node := got.Services["profile"]; node.Repo != "profile-repo" || node.Role != "backend" {
		t.Fatalf("stale target metadata=%#v", node)
	}
	if len(got.Edges) != 1 || got.Edges[0].From != "web" || got.Edges[0].To != "order" {
		t.Fatalf("formal edges=%#v, want only descriptor-backed manual edge", got.Edges)
	}
}

func TestSnapshotServiceMetadataSerializationContract(t *testing.T) {
	payload, err := json.Marshal(Snapshot{Services: []ServiceDescriptor{{
		Repo: "web-repo", Service: "web", Role: "frontend",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(payload)
	if !strings.Contains(serialized, `"services":[{"repo":"web-repo","service":"web","role":"frontend"}]`) {
		t.Fatalf("snapshot service metadata uses the wrong JSON contract: %s", serialized)
	}
	for _, fieldName := range []string{"Repo", "Service", "Role", "Aliases", "Hosts"} {
		field, ok := reflect.TypeOf(ServiceDescriptor{}).FieldByName(fieldName)
		if !ok {
			t.Fatalf("ServiceDescriptor field %s missing", fieldName)
		}
		if field.Tag.Get("json") == "" || field.Tag.Get("yaml") == "" {
			t.Fatalf("ServiceDescriptor.%s lacks JSON/YAML tags: %q", fieldName, field.Tag)
		}
	}
}

func TestFindPathsBoundsDepthAndCycles(t *testing.T) {
	graph := graphOf("web>bff", "bff>order", "order>user", "user>bff")
	paths := FindPaths(graph, Query{StartService: "web", MaxDepth: 3})
	if len(paths) == 0 {
		t.Fatal("expected a path")
	}
	for _, path := range paths {
		if len(path.Edges) > 3 {
			t.Fatalf("depth exceeded: %#v", path)
		}
		assertNoRepeatedService(t, path)
	}
}

func TestFindPathsDefaultsToThreeHops(t *testing.T) {
	graph := graphOf("web>bff", "bff>order", "order>user", "user>inventory")
	paths := FindPaths(graph, Query{StartService: "web"})
	foundThree := false
	for _, path := range paths {
		if len(path.Edges) > 3 {
			t.Fatalf("default depth exceeded: %#v", path)
		}
		if len(path.Edges) == 3 {
			foundThree = true
		}
	}
	if !foundThree {
		t.Fatalf("default depth did not reach three hops: %#v", paths)
	}
}

func TestFindPathsFiltersOnlyTheFirstHTTPHop(t *testing.T) {
	graph := ServiceGraph{
		Services: map[string]ServiceNode{
			"web": {Service: "web"}, "bff": {Service: "bff"}, "profile": {Service: "profile"}, "order": {Service: "order"},
		},
		Edges: []ServiceEdge{
			{From: "web", To: "profile", Status: "automatic", Confidence: .98, Routes: []RouteRef{{Protocol: "http", Method: "GET", Path: "/profile"}}},
			{From: "web", To: "bff", Status: "automatic", Confidence: .98, Routes: []RouteRef{{Protocol: "http", Method: "GET", Path: "/orders"}, {Protocol: "http", Method: "POST", Path: "/orders"}}},
			{From: "bff", To: "order", Status: "automatic", Confidence: .90, Routes: []RouteRef{{Protocol: "http", Method: "POST", Path: "/internal/orders"}}},
		},
	}

	paths := FindPaths(graph, Query{StartService: "web", Protocol: "HTTP", Method: "get", Path: "/orders/"})
	if len(paths) != 2 {
		t.Fatalf("filtered paths=%#v, want web>bff and web>bff>order", paths)
	}
	for _, path := range paths {
		if len(path.Services) < 2 || path.Services[1] != "bff" {
			t.Fatalf("non-matching first hop returned: %#v", path)
		}
	}
	if len(paths[1].Edges) != 2 {
		t.Fatalf("second hop was incorrectly filtered by the entry route: %#v", paths)
	}
}

func TestFindPathsFiltersGRPCFirstHopByRPCMethod(t *testing.T) {
	graph := ServiceGraph{
		Services: map[string]ServiceNode{"web": {Service: "web"}, "order": {Service: "order"}, "profile": {Service: "profile"}},
		Edges: []ServiceEdge{
			{From: "web", To: "profile", Status: "automatic", Confidence: .98, Routes: []RouteRef{{Protocol: "grpc", RPCMethod: "profile.Profile/Get"}}},
			{From: "web", To: "order", Status: "automatic", Confidence: .98, Routes: []RouteRef{{Protocol: "grpc", RPCMethod: "shop.Order/Get"}}},
		},
	}

	paths := FindPaths(graph, Query{StartService: "web", Protocol: "grpc", Method: "/shop.Order/Get", MaxDepth: 1})
	if len(paths) != 1 || !reflect.DeepEqual(paths[0].Services, []string{"web", "order"}) {
		t.Fatalf("gRPC first-hop filter returned %#v", paths)
	}
}

func TestFindPathsRanksHumanStatusThenConfidenceLengthAndLexicalSequence(t *testing.T) {
	graph := ServiceGraph{
		Services: map[string]ServiceNode{},
		Edges: []ServiceEdge{
			pathFixtureEdge("root", "manual", "manual", .60),
			pathFixtureEdge("root", "confirmed", "confirmed", .70),
			pathFixtureEdge("root", "auto-high", "automatic", .99),
			pathFixtureEdge("root", "beta", "automatic", .80),
			pathFixtureEdge("root", "alpha", "automatic", .80),
			pathFixtureEdge("alpha", "child", "automatic", .80),
		},
	}

	paths := FindPaths(graph, Query{StartService: "root", MaxDepth: 2})
	want := [][]string{
		{"root", "confirmed"},
		{"root", "manual"},
		{"root", "auto-high"},
		{"root", "alpha"},
		{"root", "beta"},
		{"root", "alpha", "child"},
	}
	if len(paths) != len(want) {
		t.Fatalf("ranked path count=%d want %d: %#v", len(paths), len(want), paths)
	}
	for index := range want {
		if !reflect.DeepEqual(paths[index].Services, want[index]) {
			t.Fatalf("path[%d]=%#v want %#v; all=%#v", index, paths[index].Services, want[index], paths)
		}
	}
	if paths[0].Score != .70 {
		t.Fatalf("confirmed path score=%v want confidence .70", paths[0].Score)
	}
}

func graphOf(specs ...string) ServiceGraph {
	graph := ServiceGraph{Services: map[string]ServiceNode{}}
	for _, spec := range specs {
		parts := strings.Split(spec, ">")
		graph.Edges = append(graph.Edges, ServiceEdge{
			From: parts[0], To: parts[1], Status: "automatic", Confidence: .98,
			Routes: []RouteRef{{Protocol: "http", Method: "GET", Path: "/fixture", EndpointEdge: spec}},
		})
		graph.Services[parts[0]] = ServiceNode{Service: parts[0]}
		graph.Services[parts[1]] = ServiceNode{Service: parts[1]}
	}
	return graph
}

func assertNoRepeatedService(t *testing.T, path Path) {
	t.Helper()
	seen := map[string]bool{}
	for _, service := range path.Services {
		if seen[service] {
			t.Fatalf("cycle in %#v", path)
		}
		seen[service] = true
	}
}

func pathFixtureEdge(from, to, status string, confidence float64) ServiceEdge {
	return ServiceEdge{
		From: from, To: to, Status: status, Confidence: confidence,
		Routes: []RouteRef{{Protocol: "http", Method: "GET", Path: "/fixture", EndpointEdge: from + ">" + to}},
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
