package config

import (
	"strings"
	"testing"
)

func TestServiceTopologyOverridesValidateAndNormalize(t *testing.T) {
	cfg := minimalValid()
	cfg.Repos = []Repo{
		{Name: "web", URL: "https://example.test/web.git", Stack: "node", ServiceNames: []string{"mall-web"}},
		{Name: "bff", URL: "https://example.test/bff.git", Stack: "php", ServiceNames: []string{"mall-bff"}},
		{Name: "orders", URL: "https://example.test/orders.git", Stack: "go"},
	}
	cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{
		{
			Action: "confirm", FromService: "mall-web", ToService: "mall-bff",
			Protocol: "HTTP", Method: "get", Path: "/api/orders/:id",
		},
		{
			Action: "reject", FromService: "mall-bff", ToService: "orders",
			Protocol: "gRPC", RPCMethod: "orders.v1.OrderService/GetOrder",
		},
		{
			Action: "add", FromService: "mall-web", ToService: "orders",
			Protocol: "http", Method: "post", Path: "/api/orders/[orderId]",
		},
	}

	if err := Validate(&cfg); err != nil {
		t.Fatal(err)
	}

	got := cfg.ServiceTopology.Overrides
	if got[0].Protocol != "http" || got[0].Method != "GET" || got[0].Path != "/api/orders/{param}" {
		t.Fatalf("normalized confirm override = %#v", got[0])
	}
	if got[1].Protocol != "grpc" || got[1].RPCMethod != "orders.v1.OrderService/GetOrder" {
		t.Fatalf("normalized reject override = %#v", got[1])
	}
	if got[2].Method != "POST" || got[2].Path != "/api/orders/{param}" {
		t.Fatalf("normalized add override = %#v", got[2])
	}
}

func TestServiceTopologyOverridesRejectInvalidContract(t *testing.T) {
	cases := []struct {
		name     string
		override ServiceTopologyOverride
	}{
		{
			name: "unknown action",
			override: ServiceTopologyOverride{
				Action: "guess", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "http", Method: "GET", Path: "/x",
			},
		},
		{
			name: "unknown service",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "missing", ToService: "mall-bff",
				Protocol: "http", Method: "GET", Path: "/x",
			},
		},
		{
			name: "relative HTTP path",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "http", Method: "GET", Path: "x",
			},
		},
		{
			name: "HTTP with rpc_method",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "http", Method: "GET", Path: "/x", RPCMethod: "Service/Get",
			},
		},
		{
			name: "gRPC without rpc_method",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "grpc",
			},
		},
		{
			name: "gRPC with HTTP fields",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "grpc", Method: "GET", Path: "/x", RPCMethod: "Service/Get",
			},
		},
		{
			name: "unknown protocol",
			override: ServiceTopologyOverride{
				Action: "add", FromService: "mall-web", ToService: "mall-bff",
				Protocol: "amqp", RPCMethod: "queue.consume",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalValid()
			cfg.Repos = []Repo{
				{Name: "web", URL: "https://example.test/web.git", Stack: "node", ServiceNames: []string{"mall-web"}},
				{Name: "bff", URL: "https://example.test/bff.git", Stack: "php", ServiceNames: []string{"mall-bff"}},
			}
			cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{tc.override}
			if err := Validate(&cfg); err == nil {
				t.Fatalf("expected rejection for %#v", tc.override)
			}
		})
	}
}

func TestServiceTopologyOverridesRejectDuplicateSemanticKey(t *testing.T) {
	cfg := minimalValid()
	cfg.Repos = []Repo{
		{Name: "web", URL: "https://example.test/web.git", Stack: "node", ServiceNames: []string{"mall-web"}},
		{Name: "bff", URL: "https://example.test/bff.git", Stack: "php", ServiceNames: []string{"mall-bff"}},
	}
	cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{
		{Action: "confirm", FromService: "mall-web", ToService: "mall-bff", Protocol: "HTTP", Method: "get", Path: "/orders/:id"},
		{Action: "reject", FromService: "mall-web", ToService: "mall-bff", Protocol: "http", Method: "GET", Path: "/orders/{id}"},
	}

	err := Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate semantic key") {
		t.Fatalf("expected duplicate semantic key rejection, got: %v", err)
	}
}

func TestServiceTopologyOverridesExcludeNonServiceRepoNames(t *testing.T) {
	cfg := minimalValid()
	cfg.Repos = []Repo{
		{Name: "web", URL: "https://example.test/web.git", Stack: "node"},
		{Name: "docs", URL: "https://example.test/docs.git", Stack: "markdown", Role: RoleDocs},
	}
	cfg.ServiceTopology.Overrides = []ServiceTopologyOverride{{
		Action: "add", FromService: "web", ToService: "docs",
		Protocol: "http", Method: "GET", Path: "/x",
	}}

	err := Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "to_service") {
		t.Fatalf("expected non-service repo name rejection, got: %v", err)
	}
}
