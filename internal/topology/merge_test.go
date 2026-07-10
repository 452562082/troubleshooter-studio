package topology

import "testing"

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
