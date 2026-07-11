package topology

import "testing"

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/orders/:id":       "/orders/{param}",
		"/orders/{orderId}": "/orders/{param}",
		"/orders/[id]":      "/orders/{param}",
		"/files/*path":      "/files/{wildcard}",
		"/files/{wildcard}": "/files/{wildcard}",
		"https://api.example.com//orders/1?full=true": "/orders/1",
		"/orders/": "/orders",
		"/":        "/",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizePathIsIdempotent(t *testing.T) {
	paths := []string{
		"/orders/:id",
		"/orders/{orderId}",
		"/orders/[id]",
		"/files/*path",
		"https://api.example.com//orders/1?full=true",
		"/orders/",
		"/",
	}
	for _, path := range paths {
		once := NormalizePath(path)
		if twice := NormalizePath(once); twice != once {
			t.Errorf("NormalizePath is not idempotent for %q: once=%q twice=%q", path, once, twice)
		}
	}
}

func TestNormalizeHTTPMethod(t *testing.T) {
	if got, want := NormalizeHTTPMethod("get"), "GET"; got != want {
		t.Fatalf("NormalizeHTTPMethod(get)=%q want %q", got, want)
	}
}

func TestEndpointSemanticIDIsStable(t *testing.T) {
	e := Endpoint{Repo: "mall-web", Service: "mall-web", Direction: DirectionOutbound, Protocol: "http", Method: "get", Path: "/orders/:id"}
	if got, want := e.SemanticID(), "mall-web:http:outbound:GET:/orders/{param}"; got != want {
		t.Fatalf("id=%q want %q", got, want)
	}
	e.ID = "ignored:previous:id"
	if got, want := e.SemanticID(), "mall-web:http:outbound:GET:/orders/{param}"; got != want {
		t.Fatalf("recomputed id=%q want %q", got, want)
	}
}

func TestEndpointSemanticIDQualifiesMultiServiceRepositoryOwnership(t *testing.T) {
	orders := Endpoint{Repo: "platform", Service: "orders", Direction: DirectionInbound, Protocol: "http", Method: "GET", Path: "/health"}
	payments := orders
	payments.Service = "payments"
	if orders.SemanticID() == payments.SemanticID() {
		t.Fatalf("multi-service IDs collided: %q", orders.SemanticID())
	}
	if got, want := orders.SemanticID(), "platform/orders:http:inbound:GET:/health"; got != want {
		t.Fatalf("orders id=%q want %q", got, want)
	}
}
