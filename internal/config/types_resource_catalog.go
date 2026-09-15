package config

// ResourceCatalog is the normalized identity and binding layer shared by code,
// configuration, data and runtime resources. Infrastructure blocks continue to
// own connection details; catalog entries only reference their stable IDs.
//
// Keeping identity separate from credentials lets one repository contain
// multiple services, lets a service use different source instances per
// environment, and gives frontend/static workloads a runtime identity without
// pretending they use a configuration center.
type ResourceCatalog struct {
	Services  []ServiceResource  `yaml:"services,omitempty"`
	Workloads []WorkloadResource `yaml:"workloads,omitempty"`
}

type ServiceResource struct {
	ID         string `yaml:"id"`
	Repository string `yaml:"repository"`
	// ConfigSources maps environment ID -> infrastructure.config_centers[].id.
	ConfigSources map[string]string `yaml:"config_sources,omitempty"`
	// DataStores maps environment ID -> infrastructure.data_stores[].id.
	DataStores map[string][]string `yaml:"data_stores,omitempty"`
	// Workloads maps environment ID -> resource_catalog.workloads[].id.
	Workloads map[string]string `yaml:"workloads,omitempty"`
}

type WorkloadResource struct {
	ID         string `yaml:"id"`
	Repository string `yaml:"repository"`
	// Service is optional: frontend/mobile/static workloads can stand alone.
	Service string `yaml:"service,omitempty"`
	// Names maps environment ID -> concrete runtime workload name. The K8s
	// observability service_map still owns cluster/namespace/selector details.
	Names map[string]string `yaml:"names,omitempty"`
}

// ConfigSourceFor returns the environment-specific source binding. An empty
// result means the catalog has no explicit binding for that service/env.
func (r ResourceCatalog) ConfigSourceFor(env, service string) string {
	for _, item := range r.Services {
		if item.ID != service {
			continue
		}
		if source := item.ConfigSources[env]; source != "" {
			return source
		}
		return item.ConfigSources[""]
	}
	return ""
}

// ConfigSourceFor resolves the formal catalog first and then falls back to the
// legacy repository-wide binding so old YAML remains behaviorally identical.
func (s *SystemConfig) ConfigSourceFor(env, service string) string {
	if source := s.ResourceCatalog.ConfigSourceFor(env, service); source != "" {
		return source
	}
	for _, repo := range s.Repos {
		names := repo.ServiceNames
		if len(names) == 0 {
			names = []string{repo.Name}
		}
		for _, name := range names {
			if name == service {
				if repo.ConfigSource != "" {
					return repo.ConfigSource
				}
				return "default"
			}
		}
	}
	return "default"
}
