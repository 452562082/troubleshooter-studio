package config

import "testing"

func TestHealthCheck_K8sRuntimeOne2AllDoesNotRequireKuboardURL(t *testing.T) {
	cfg := &SystemConfig{
		System:       System{ID: "shop", Name: "Shop"},
		Agent:        Agent{Name: "Shop Bot"},
		Environments: []Environment{{ID: "dev"}},
		Infrastructure: Infrastructure{
			Observability: Observability{
				K8sRuntime: K8sRuntime{
					Enabled:  true,
					Provider: "one2all",
					ServiceMap: []K8sRuntimeServiceMapEntry{{
						Env:       "dev",
						Service:   "order",
						ClusterID: "1",
						Namespace: "default",
						Workload:  "order",
					}},
				},
			},
		},
	}

	for _, issue := range HealthCheck(cfg) {
		if issue.Field == "infrastructure.observability.k8s_runtime.url_by_env" {
			t.Fatalf("provider=one2all should not require Kuboard url_by_env; issue=%+v", issue)
		}
	}
}
