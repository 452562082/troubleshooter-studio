package topology

import (
	"reflect"
	"testing"
)

func TestMatchExactHostMethodPathIsAutomatic(t *testing.T) {
	result := Match(MatchInput{
		Endpoints: []Endpoint{
			endpoint("web", "web", DirectionOutbound, "GET", "/api/orders/{param}", "mall-bff"),
			endpoint("bff", "mall-bff", DirectionInbound, "GET", "/api/orders/{param}", ""),
		},
		Services: []ServiceDescriptor{{Repo: "bff", Service: "mall-bff", Aliases: []string{"mall-bff", "mall-bff.default.svc"}}},
	})
	edge := onlyEdge(t, result)
	if edge.Status != "automatic" || edge.Confidence != 0.98 {
		t.Fatalf("edge=%#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"method_path_exact", "target_alias_exact"})
	assertStrings(t, edge.Conflicts, nil)
}

func TestMatchUniquePathWithUnknownVariableIsCandidate(t *testing.T) {
	result := Match(MatchInput{Endpoints: []Endpoint{
		endpoint("web", "web", DirectionOutbound, "GET", "/api/orders", "API_BASE_URL"),
		endpoint("bff", "mall-bff", DirectionInbound, "GET", "/api/orders", ""),
	}})
	edge := onlyEdge(t, result)
	if edge.Status != "candidate" || edge.Confidence != 0.76 {
		t.Fatalf("edge=%#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"method_path_exact"})
	assertStrings(t, edge.Conflicts, []string{"target_unresolved"})
}

func TestMatchDuplicateRoutesWithoutHostDoNotPromote(t *testing.T) {
	result := Match(MatchInput{Endpoints: duplicateInboundRoutesWithUnknownOutbound()})
	if len(result.Edges) != 0 || len(result.Hints) != 2 {
		t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	for _, edge := range result.Hints {
		if edge.Status != "candidate" || edge.Confidence != 0.55 {
			t.Fatalf("ambiguous edge promoted: %#v", edge)
		}
		assertStrings(t, edge.Reasons, []string{"method_path_exact"})
		assertStrings(t, edge.Conflicts, []string{"target_unresolved", "route_duplicated_across_services"})
	}
}

func TestMatchAmbiguousTargetEvidenceDoesNotPromote(t *testing.T) {
	tests := []struct {
		name       string
		targetHint string
		services   []ServiceDescriptor
		reason     string
	}{
		{
			name:       "shared alias",
			targetHint: "orders-api",
			services: []ServiceDescriptor{
				{Repo: "a", Service: "service-a", Aliases: []string{"orders-api"}},
				{Repo: "b", Service: "service-b", Aliases: []string{"orders-api"}},
			},
			reason: "target_alias_exact",
		},
		{
			name:       "shared host",
			targetHint: "orders.internal",
			services: []ServiceDescriptor{
				{Repo: "a", Service: "service-a", Hosts: []string{"orders.internal"}},
				{Repo: "b", Service: "service-b", Hosts: []string{"orders.internal"}},
			},
			reason: "target_host_exact",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Match(MatchInput{
				Endpoints: []Endpoint{
					endpoint("web", "web", DirectionOutbound, "GET", "/orders", test.targetHint),
					endpoint("a", "service-a", DirectionInbound, "GET", "/orders", ""),
					endpoint("b", "service-b", DirectionInbound, "GET", "/orders", ""),
				},
				Services: test.services,
			})
			if len(result.Edges) != 0 || len(result.Hints) != 2 {
				t.Fatalf("ambiguous target promoted: edges=%#v hints=%#v", result.Edges, result.Hints)
			}
			for _, edge := range result.Hints {
				if edge.Status != "candidate" || edge.Confidence != 0.55 {
					t.Fatalf("ambiguous target edge=%#v", edge)
				}
				assertStrings(t, edge.Reasons, []string{"method_path_exact", test.reason})
				assertStrings(t, edge.Conflicts, []string{"target_ambiguous_across_services", "route_duplicated_across_services"})
			}
		})
	}
}

func TestMatchExplicitHTTPTransformsAreAutomatic(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		transforms []Transform
	}{
		{name: "Next rewrite", source: "next-rewrite", transforms: []Transform{{Kind: "rewrite", From: "/edge/api/orders", To: "/orders"}}},
		{name: "Spring prefix chain", source: "spring-gateway", transforms: []Transform{
			{Kind: "strip_prefix", From: "/edge/api/orders", To: "/api/orders"},
			{Kind: "rewrite", From: "/api/orders", To: "/orders"},
		}},
		{name: "Nginx rewrite", source: "nginx-location", transforms: []Transform{{Kind: "rewrite", From: "/edge/api/orders", To: "/orders"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound := endpoint("gateway", "gateway", DirectionOutbound, "GET", "/edge/api/orders", "orders.internal")
			outbound.Source = test.source
			outbound.Transforms = test.transforms
			result := Match(MatchInput{
				Endpoints: []Endpoint{
					outbound,
					endpoint("orders", "orders", DirectionInbound, "GET", "/orders", ""),
				},
				Services: []ServiceDescriptor{{Repo: "orders", Service: "orders", Hosts: []string{"orders.internal"}}},
			})
			edge := onlyEdge(t, result)
			if edge.Status != "automatic" || edge.Confidence != 0.90 {
				t.Fatalf("edge=%#v", edge)
			}
			assertStrings(t, edge.Reasons, []string{"path_transform_proven", "target_host_exact"})
			assertStrings(t, edge.Conflicts, nil)
		})
	}
}

func TestMatchFullyQualifiedGRPCMethodAndAliasIsAutomatic(t *testing.T) {
	outbound := grpcEndpointForMatch("web", "web", DirectionOutbound, "orders.v1.OrderService/GetOrder", "orders-grpc")
	inbound := grpcEndpointForMatch("orders", "orders", DirectionInbound, "/orders.v1.OrderService/GetOrder", "")
	result := Match(MatchInput{
		Endpoints: []Endpoint{outbound, inbound},
		Services:  []ServiceDescriptor{{Repo: "orders", Service: "orders", Aliases: []string{"orders-grpc"}}},
	})
	edge := onlyEdge(t, result)
	if edge.Protocol != "grpc" || edge.RPCMethod != "orders.v1.OrderService/GetOrder" || edge.Status != "automatic" || edge.Confidence != 0.98 {
		t.Fatalf("edge=%#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"rpc_method_exact", "target_alias_exact"})
}

func TestMatchUnprovenPrefixSimilarityRemainsHint(t *testing.T) {
	result := Match(MatchInput{
		Endpoints: []Endpoint{
			endpoint("web", "web", DirectionOutbound, "GET", "/edge/api/orders", "orders"),
			endpoint("orders", "orders", DirectionInbound, "GET", "/orders", ""),
		},
		Services: []ServiceDescriptor{{Repo: "orders", Service: "orders", Aliases: []string{"orders"}}},
	})
	if len(result.Edges) != 0 || len(result.Hints) != 1 {
		t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	edge := result.Hints[0]
	if edge.Status != "candidate" || edge.Confidence >= 0.60 {
		t.Fatalf("fuzzy path promoted: %#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"http_method_compatible", "path_suffix_similar", "target_alias_exact"})
	assertStrings(t, edge.Conflicts, []string{"path_transform_unproven"})
}

func TestMatchMethodConflictIsExcluded(t *testing.T) {
	result := Match(MatchInput{Endpoints: []Endpoint{
		endpoint("web", "web", DirectionOutbound, "POST", "/orders", "orders"),
		endpoint("orders", "orders", DirectionInbound, "GET", "/orders", ""),
	}})
	if len(result.Edges) != 0 || len(result.Hints) != 0 {
		t.Fatalf("method conflict must be excluded: edges=%#v hints=%#v", result.Edges, result.Hints)
	}
}

func TestMatchLogicalSelfLoopAcrossRepositoriesIsExcluded(t *testing.T) {
	result := Match(MatchInput{
		Endpoints: []Endpoint{
			endpoint("orders-client", "orders", DirectionOutbound, "GET", "/orders", "orders"),
			endpoint("orders-server", "orders", DirectionInbound, "GET", "/orders", ""),
		},
		Services: []ServiceDescriptor{{Repo: "orders-server", Service: "orders", Aliases: []string{"orders"}}},
	})
	if len(result.Edges) != 0 || len(result.Hints) != 0 {
		t.Fatalf("logical self-loop must be excluded: edges=%#v hints=%#v", result.Edges, result.Hints)
	}
}

func TestMatchIncompleteHTTPDiscriminatorsAreExcluded(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Endpoint, *Endpoint)
	}{
		{name: "outbound protocol missing", mutate: func(outbound, _ *Endpoint) { outbound.Protocol = "" }},
		{name: "inbound protocol missing", mutate: func(_, inbound *Endpoint) { inbound.Protocol = "" }},
		{name: "outbound method missing", mutate: func(outbound, _ *Endpoint) { outbound.Method = "" }},
		{name: "inbound method missing", mutate: func(_, inbound *Endpoint) { inbound.Method = "" }},
		{name: "both methods missing", mutate: func(outbound, inbound *Endpoint) { outbound.Method, inbound.Method = "", "" }},
		{name: "outbound path missing", mutate: func(outbound, _ *Endpoint) { outbound.Path = "" }},
		{name: "inbound path missing", mutate: func(_, inbound *Endpoint) { inbound.Path = "" }},
		{name: "both paths missing", mutate: func(outbound, inbound *Endpoint) { outbound.Path, inbound.Path = "", "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound := endpoint("web", "web", DirectionOutbound, "GET", "/orders", "orders")
			inbound := endpoint("orders", "orders", DirectionInbound, "GET", "/orders", "")
			test.mutate(&outbound, &inbound)
			result := Match(MatchInput{Endpoints: []Endpoint{outbound, inbound}})
			if len(result.Edges) != 0 || len(result.Hints) != 0 {
				t.Fatalf("incomplete HTTP discriminator matched: edges=%#v hints=%#v", result.Edges, result.Hints)
			}
		})
	}
}

func TestMatchIncompleteGRPCDiscriminatorsAreExcluded(t *testing.T) {
	tests := []struct {
		name     string
		outbound string
		inbound  string
	}{
		{name: "empty", outbound: "", inbound: ""},
		{name: "bare method", outbound: "GetOrder", inbound: "GetOrder"},
		{name: "service missing", outbound: "/GetOrder", inbound: "/GetOrder"},
		{name: "method missing", outbound: "orders.v1.OrderService/", inbound: "orders.v1.OrderService/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Match(MatchInput{Endpoints: []Endpoint{
				grpcEndpointForMatch("web", "web", DirectionOutbound, test.outbound, "orders"),
				grpcEndpointForMatch("orders", "orders", DirectionInbound, test.inbound, ""),
			}})
			if len(result.Edges) != 0 || len(result.Hints) != 0 {
				t.Fatalf("incomplete gRPC discriminator matched: edges=%#v hints=%#v", result.Edges, result.Hints)
			}
		})
	}
}

func TestMatchSortsResultsDeterministically(t *testing.T) {
	outboundB := endpoint("web", "web", DirectionOutbound, "GET", "/b", "API_B_URL")
	inboundB := endpoint("b", "service-b", DirectionInbound, "GET", "/b", "")
	outboundA := endpoint("web", "web", DirectionOutbound, "GET", "/a", "API_A_URL")
	inboundA := endpoint("a", "service-a", DirectionInbound, "GET", "/a", "")

	result := Match(MatchInput{Endpoints: []Endpoint{inboundB, outboundB, inboundA, outboundA}})
	if len(result.Edges) != 2 {
		t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	got := []string{result.Edges[0].ToService, result.Edges[1].ToService}
	assertStrings(t, got, []string{"service-a", "service-b"})

	reversed := Match(MatchInput{Endpoints: []Endpoint{outboundA, inboundA, outboundB, inboundB}})
	if !reflect.DeepEqual(result, reversed) {
		t.Fatalf("input order changed result:\nfirst=%#v\nreversed=%#v", result, reversed)
	}
}

func TestMatchServiceDescriptorOrderDoesNotChangeEvidence(t *testing.T) {
	endpoints := []Endpoint{
		endpoint("web", "web", DirectionOutbound, "GET", "/orders", "orders.internal"),
		endpoint("orders", "orders", DirectionInbound, "GET", "/orders", ""),
	}
	hostDescriptor := ServiceDescriptor{Repo: "orders", Service: "orders", Hosts: []string{"orders.internal"}}
	aliasDescriptor := ServiceDescriptor{Repo: "orders", Service: "orders", Aliases: []string{"orders-api"}}

	first := Match(MatchInput{Endpoints: endpoints, Services: []ServiceDescriptor{hostDescriptor, aliasDescriptor}})
	second := Match(MatchInput{Endpoints: endpoints, Services: []ServiceDescriptor{aliasDescriptor, hostDescriptor}})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("descriptor order changed result:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if edge := onlyEdge(t, first); edge.Confidence != 0.98 {
		t.Fatalf("edge=%#v", edge)
	}
}

func TestMatchBrokenTransformChainStaysBelowPromotionThreshold(t *testing.T) {
	outbound := endpoint("gateway", "gateway", DirectionOutbound, "GET", "/edge/api/orders", "orders")
	outbound.Transforms = []Transform{
		{Kind: "strip_prefix", From: "/edge/api/orders", To: "/api/orders"},
		{Kind: "rewrite", From: "/disconnected/orders", To: "/orders"},
	}
	result := Match(MatchInput{
		Endpoints: []Endpoint{outbound, endpoint("orders", "orders", DirectionInbound, "GET", "/orders", "")},
		Services:  []ServiceDescriptor{{Repo: "orders", Service: "orders", Aliases: []string{"orders"}}},
	})
	if len(result.Edges) != 0 || len(result.Hints) != 1 {
		t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	if edge := result.Hints[0]; edge.Confidence >= 0.60 || !reflect.DeepEqual(edge.Conflicts, []string{"path_transform_unproven"}) {
		t.Fatalf("edge=%#v", edge)
	}
}

func TestMatchUnknownTransformKindStaysBelowPromotionThreshold(t *testing.T) {
	outbound := endpoint("gateway", "gateway", DirectionOutbound, "GET", "/edge/orders", "orders")
	outbound.Transforms = []Transform{{Kind: "metadata", From: "/edge/orders", To: "/orders"}}
	result := Match(MatchInput{
		Endpoints: []Endpoint{outbound, endpoint("orders", "orders", DirectionInbound, "GET", "/orders", "")},
		Services:  []ServiceDescriptor{{Repo: "orders", Service: "orders", Aliases: []string{"orders"}}},
	})
	if len(result.Edges) != 0 || len(result.Hints) != 1 {
		t.Fatalf("unknown transform promoted: edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	edge := result.Hints[0]
	if edge.Confidence != 0.45 || edge.Status != "candidate" {
		t.Fatalf("edge=%#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"http_method_compatible", "path_suffix_similar", "target_alias_exact"})
	assertStrings(t, edge.Conflicts, []string{"path_transform_unproven"})
}

func TestMatchNormalizesExactHostEvidence(t *testing.T) {
	result := Match(MatchInput{
		Endpoints: []Endpoint{
			endpoint("web", "web", DirectionOutbound, "GET", "/orders", "https://orders.internal:8443/api"),
			endpoint("orders", "orders", DirectionInbound, "GET", "/orders", ""),
		},
		Services: []ServiceDescriptor{{Repo: "orders", Service: "orders", Hosts: []string{"orders.internal"}}},
	})
	edge := onlyEdge(t, result)
	if edge.Confidence != 0.98 {
		t.Fatalf("edge=%#v", edge)
	}
	assertStrings(t, edge.Reasons, []string{"method_path_exact", "target_host_exact"})
}

func endpoint(repo, service string, direction Direction, method, path, hint string) Endpoint {
	value := Endpoint{Repo: repo, Service: service, Direction: direction, Protocol: "http", Method: method, Path: path, TargetHint: hint}
	value.ID = value.SemanticID()
	return value
}

func onlyEdge(t *testing.T, result MatchResult) CandidateEdge {
	t.Helper()
	if len(result.Edges) != 1 {
		t.Fatalf("edges=%#v hints=%#v", result.Edges, result.Hints)
	}
	return result.Edges[0]
}

func duplicateInboundRoutesWithUnknownOutbound() []Endpoint {
	return []Endpoint{
		endpoint("web", "web", DirectionOutbound, "GET", "/health", "API_URL"),
		endpoint("a", "service-a", DirectionInbound, "GET", "/health", ""),
		endpoint("b", "service-b", DirectionInbound, "GET", "/health", ""),
	}
}

func grpcEndpointForMatch(repo, service string, direction Direction, method, hint string) Endpoint {
	value := Endpoint{Repo: repo, Service: service, Direction: direction, Protocol: "grpc", RPCMethod: method, TargetHint: hint}
	value.ID = value.SemanticID()
	return value
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}
