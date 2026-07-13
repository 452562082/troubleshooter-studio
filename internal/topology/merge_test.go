package topology

import (
	"reflect"
	"testing"
)

func TestMergeOverridePriorityAndStale(t *testing.T) {
	auto := []CandidateEdge{edge("web", "bff", "GET", "/api/orders", .98, "automatic")}
	got := Merge(auto, []Override{
		{Action: "reject", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/api/orders"},
		{Action: "add", FromService: "web", ToService: "order", Protocol: "http", Method: "GET", Path: "/api/orders"},
		{Action: "confirm", FromService: "web", ToService: "profile", Protocol: "http", Method: "GET", Path: "/profile"},
	})
	assertStatus(t, got, "web", "bff", "rejected")
	assertStatus(t, got, "web", "order", "manual")
	assertStatus(t, got, "web", "profile", "stale")
}

func TestMergeRejectWinsOverHumanAndAutomaticDecisions(t *testing.T) {
	auto := []CandidateEdge{edge("web", "bff", "GET", "/orders", .98, "automatic")}
	overrides := []Override{
		{Action: "confirm", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders"},
		{Action: "add", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders"},
		{Action: "reject", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders"},
	}

	got := Merge(auto, overrides)
	if len(got) != 1 {
		t.Fatalf("Merge returned %d edges, want one decision: %#v", len(got), got)
	}
	assertStatus(t, got, "web", "bff", "rejected")
}

func TestMergeConfirmAndAddRetainCurrentEndpointEvidence(t *testing.T) {
	confirmed := edge("web", "bff", "GET", "/orders", .76, "candidate")
	confirmed.FromEndpoint = "web:out"
	confirmed.ToEndpoint = "bff:in"
	manual := edge("bff", "order", "POST", "/internal/orders", .98, "automatic")
	manual.FromEndpoint = "bff:out"
	manual.ToEndpoint = "order:in"

	got := Merge([]CandidateEdge{confirmed, manual}, []Override{
		{Action: "confirm", FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders"},
		{Action: "add", FromService: "bff", ToService: "order", Protocol: "http", Method: "POST", Path: "/internal/orders"},
	})

	assertStatus(t, got, "web", "bff", "confirmed")
	assertStatus(t, got, "bff", "order", "manual")
	for _, candidate := range got {
		if candidate.FromService == "web" && (candidate.FromEndpoint != "web:out" || candidate.ToEndpoint != "bff:in") {
			t.Fatalf("confirm discarded endpoint evidence: %#v", candidate)
		}
		if candidate.FromService == "bff" && (candidate.FromEndpoint != "bff:out" || candidate.ToEndpoint != "order:in") {
			t.Fatalf("add discarded endpoint evidence: %#v", candidate)
		}
	}
}

func TestMergeSupportsGRPCOverrideSemanticKey(t *testing.T) {
	auto := CandidateEdge{
		FromService: "bff", ToService: "order", Protocol: "grpc",
		RPCMethod: "shop.OrderService/GetOrder", Confidence: .98,
		Status: "automatic", Reasons: []string{"rpc_method_exact"},
	}

	got := Merge([]CandidateEdge{auto}, []Override{{
		Action: "confirm", FromService: "bff", ToService: "order", Protocol: "grpc",
		RPCMethod: "/shop.OrderService/GetOrder",
	}})
	assertStatus(t, got, "bff", "order", "confirmed")
}

func TestMergeDuplicateOverridePriorityIsIndependentOfInputOrder(t *testing.T) {
	auto := []CandidateEdge{edge("web", "bff", "GET", "/orders", .98, "automatic")}
	assertOrdersProduceSameMerge(t, auto, "rejected", [][]string{
		{"confirm", "add", "reject"},
		{"confirm", "reject", "add"},
		{"add", "confirm", "reject"},
		{"add", "reject", "confirm"},
		{"reject", "confirm", "add"},
		{"reject", "add", "confirm"},
	})
	assertOrdersProduceSameMerge(t, auto, "manual", [][]string{
		{"confirm", "add"},
		{"add", "confirm"},
	})
}

func assertOrdersProduceSameMerge(t *testing.T, auto []CandidateEdge, wantStatus string, actions [][]string) {
	t.Helper()
	var baseline []CandidateEdge
	for _, order := range actions {
		overrides := make([]Override, 0, len(order))
		for _, action := range order {
			overrides = append(overrides, Override{
				Action: action, FromService: "web", ToService: "bff",
				Protocol: "http", Method: "GET", Path: "/orders",
			})
		}
		got := Merge(auto, overrides)
		if len(got) != 1 || got[0].Status != wantStatus {
			t.Fatalf("actions %v produced %#v, want one %s edge", order, got, wantStatus)
		}
		if baseline == nil {
			baseline = got
			continue
		}
		if !reflect.DeepEqual(got, baseline) {
			t.Fatalf("actions %v produced order-dependent result\n got: %#v\nwant: %#v", order, got, baseline)
		}
	}
}

func TestMergeDoesNotMutateOrAliasInputEvidence(t *testing.T) {
	reasons := make([]string, 1, 3)
	reasons[0] = "route_exact"
	reasonsWithCapacity := reasons[:cap(reasons)]
	reasonsWithCapacity[1] = "reserved_reason"
	reasons = reasons[:1]
	conflicts := make([]string, 1, 3)
	conflicts[0] = "target_ambiguous"
	conflictsWithCapacity := conflicts[:cap(conflicts)]
	conflictsWithCapacity[1] = "reserved_conflict"
	conflicts = conflicts[:1]
	edges := []CandidateEdge{{
		FromService: "web", ToService: "bff", Protocol: "http", Method: "GET", Path: "/orders",
		Status: "candidate", Confidence: .76, Reasons: reasons, Conflicts: conflicts,
	}}

	got := Merge(edges, []Override{{
		Action: "confirm", FromService: "web", ToService: "bff",
		Protocol: "http", Method: "GET", Path: "/orders",
	}})
	if len(got) != 1 {
		t.Fatalf("Merge returned %d edges, want one: %#v", len(got), got)
	}
	if edges[0].Status != "candidate" || !reflect.DeepEqual(edges[0].Reasons, []string{"route_exact"}) ||
		!reflect.DeepEqual(edges[0].Conflicts, []string{"target_ambiguous"}) {
		t.Fatalf("Merge mutated input edge: %#v", edges[0])
	}
	if reasonsWithCapacity[1] != "reserved_reason" || conflictsWithCapacity[1] != "reserved_conflict" {
		t.Fatalf("Merge mutated input slice backing arrays: reasons=%#v conflicts=%#v", reasonsWithCapacity, conflictsWithCapacity)
	}

	got[0].Status = "changed"
	got[0].Reasons[0] = "changed"
	got[0].Conflicts[0] = "changed"
	if edges[0].Status != "candidate" || edges[0].Reasons[0] != "route_exact" || edges[0].Conflicts[0] != "target_ambiguous" {
		t.Fatalf("Merge result aliases input edge evidence: input=%#v result=%#v", edges[0], got[0])
	}
}

func TestMergeMatchesSemanticallyEquivalentNormalizedHTTPOverride(t *testing.T) {
	auto := edge("web", "bff", " get ", "https://api.example.com/orders/:id/?debug=true", .98, "automatic")
	auto.FromEndpoint = "web:out:orders"
	auto.ToEndpoint = "bff:in:orders"

	got := Merge([]CandidateEdge{auto}, []Override{{
		Action: "confirm", FromService: "web", ToService: "bff",
		Protocol: " HTTP ", Method: "GET", Path: "/orders/{orderID}",
	}})
	if len(got) != 1 || got[0].Status != "confirmed" {
		t.Fatalf("normalized HTTP override did not match current evidence: %#v", got)
	}
	if got[0].FromEndpoint != auto.FromEndpoint || got[0].ToEndpoint != auto.ToEndpoint {
		t.Fatalf("normalized HTTP match discarded endpoint evidence: %#v", got[0])
	}
}

func edge(from, to, method, path string, confidence float64, status string) CandidateEdge {
	return CandidateEdge{
		FromService: from,
		ToService:   to,
		Protocol:    "http",
		Method:      method,
		Path:        path,
		Confidence:  confidence,
		Status:      status,
		Reasons:     []string{"fixture:" + method + ":" + path},
	}
}

func assertStatus(t *testing.T, edges []CandidateEdge, from, to, status string) {
	t.Helper()
	for _, candidate := range edges {
		if candidate.FromService == from && candidate.ToService == to && candidate.Status == status {
			return
		}
	}
	t.Fatalf("edge %s -> %s status=%s not found in %#v", from, to, status, edges)
}
