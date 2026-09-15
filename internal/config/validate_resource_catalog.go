package config

import "fmt"

func validateResourceCatalog(c *SystemConfig, envIDs, repoNames, sourceIDs map[string]bool) error {
	dataStoreIDs := map[string]bool{}
	for i, ds := range c.Infrastructure.DataStores {
		if ds.ID == "" {
			return fmt.Errorf("infrastructure.data_stores[%d].id required after migration", i)
		}
		if !idPattern.MatchString(ds.ID) {
			return fmt.Errorf("infrastructure.data_stores[%d].id must match [a-z0-9][a-z0-9-]*, got %q", i, ds.ID)
		}
		if dataStoreIDs[ds.ID] {
			return fmt.Errorf("duplicate data_store id: %s", ds.ID)
		}
		dataStoreIDs[ds.ID] = true
	}

	serviceIDs := map[string]bool{}
	for i, service := range c.ResourceCatalog.Services {
		path := fmt.Sprintf("resource_catalog.services[%d]", i)
		if service.ID == "" {
			return fmt.Errorf("%s.id required", path)
		}
		if serviceIDs[service.ID] {
			return fmt.Errorf("duplicate resource_catalog service id: %s", service.ID)
		}
		serviceIDs[service.ID] = true
		if !repoNames[service.Repository] {
			return fmt.Errorf("%s.repository=%q references unknown repos[].name", path, service.Repository)
		}
		for env, source := range service.ConfigSources {
			if env != "" && !envIDs[env] {
				return fmt.Errorf("%s.config_sources references unknown env: %s", path, env)
			}
			if source != "" && !sourceIDs[source] {
				return fmt.Errorf("%s.config_sources[%s]=%q references unknown config_centers[].id", path, env, source)
			}
		}
		for env, refs := range service.DataStores {
			if env != "" && !envIDs[env] {
				return fmt.Errorf("%s.data_stores references unknown env: %s", path, env)
			}
			for _, ref := range refs {
				if !dataStoreIDs[ref] {
					return fmt.Errorf("%s.data_stores[%s] references unknown data_store id: %s", path, env, ref)
				}
			}
		}
	}

	workloadIDs := map[string]bool{}
	for i, workload := range c.ResourceCatalog.Workloads {
		path := fmt.Sprintf("resource_catalog.workloads[%d]", i)
		if workload.ID == "" {
			return fmt.Errorf("%s.id required", path)
		}
		if workloadIDs[workload.ID] {
			return fmt.Errorf("duplicate resource_catalog workload id: %s", workload.ID)
		}
		workloadIDs[workload.ID] = true
		if !repoNames[workload.Repository] {
			return fmt.Errorf("%s.repository=%q references unknown repos[].name", path, workload.Repository)
		}
		if workload.Service != "" && !serviceIDs[workload.Service] {
			return fmt.Errorf("%s.service=%q references unknown resource_catalog service", path, workload.Service)
		}
		for env := range workload.Names {
			if env != "" && !envIDs[env] {
				return fmt.Errorf("%s.names references unknown env: %s", path, env)
			}
		}
	}
	for i, service := range c.ResourceCatalog.Services {
		for env, ref := range service.Workloads {
			if env != "" && !envIDs[env] {
				return fmt.Errorf("resource_catalog.services[%d].workloads references unknown env: %s", i, env)
			}
			if !workloadIDs[ref] {
				return fmt.Errorf("resource_catalog.services[%d].workloads[%s]=%q references unknown workload", i, env, ref)
			}
			for _, workload := range c.ResourceCatalog.Workloads {
				if workload.ID == ref && workload.Service != "" && workload.Service != service.ID {
					return fmt.Errorf("resource_catalog.services[%d].workloads[%s]=%q belongs to service %q", i, env, ref, workload.Service)
				}
			}
		}
	}
	if err := validateRuntimeResourceMappings(c, envIDs, serviceIDs, workloadIDs); err != nil {
		return err
	}
	return nil
}

func validateRuntimeResourceMappings(c *SystemConfig, envIDs, serviceIDs, workloadIDs map[string]bool) error {
	knownRuntimeIDs := map[string]bool{}
	for id := range serviceIDs {
		knownRuntimeIDs[id] = true
	}
	for id := range workloadIDs {
		knownRuntimeIDs[id] = true
	}

	k8sByEnvService := map[string]K8sRuntimeServiceMapEntry{}
	for i, entry := range c.Infrastructure.Observability.K8sRuntime.ServiceMap {
		if !envIDs[entry.Env] {
			return fmt.Errorf("infrastructure.observability.k8s_runtime.service_map[%d].env references unknown env: %s", i, entry.Env)
		}
		if len(knownRuntimeIDs) > 0 && !knownRuntimeIDs[entry.Service] {
			return fmt.Errorf("infrastructure.observability.k8s_runtime.service_map[%d].service=%q references unknown service/workload identity", i, entry.Service)
		}
		key := entry.Env + "\x00" + entry.Service
		if _, exists := k8sByEnvService[key]; exists {
			return fmt.Errorf("duplicate k8s_runtime service_map entry for env=%s service=%s", entry.Env, entry.Service)
		}
		k8sByEnvService[key] = entry
	}

	workloadByID := map[string]WorkloadResource{}
	for _, workload := range c.ResourceCatalog.Workloads {
		workloadByID[workload.ID] = workload
	}
	for _, service := range c.ResourceCatalog.Services {
		for env, workloadID := range service.Workloads {
			workload := workloadByID[workloadID]
			expectedName := workload.Names[env]
			entry, exists := k8sByEnvService[env+"\x00"+service.ID]
			if exists && expectedName != "" && entry.Workload != "" && entry.Workload != expectedName {
				return fmt.Errorf("runtime workload mismatch for env=%s service=%s: resource_catalog=%q, k8s_runtime=%q", env, service.ID, expectedName, entry.Workload)
			}
		}
	}
	for _, workload := range c.ResourceCatalog.Workloads {
		if workload.Service != "" {
			continue
		}
		for env, expectedName := range workload.Names {
			entry, exists := k8sByEnvService[env+"\x00"+workload.ID]
			if exists && expectedName != "" && entry.Workload != "" && entry.Workload != expectedName {
				return fmt.Errorf("runtime workload mismatch for env=%s workload=%s: resource_catalog=%q, k8s_runtime=%q", env, workload.ID, expectedName, entry.Workload)
			}
		}
	}

	for env, mapping := range c.Infrastructure.Observability.Loki.LabelMappingByEnv {
		if !envIDs[env] {
			return fmt.Errorf("infrastructure.observability.loki.label_mapping_by_env references unknown env: %s", env)
		}
		if len(knownRuntimeIDs) == 0 {
			continue
		}
		for resourceID := range mapping.ServiceMap {
			if !knownRuntimeIDs[resourceID] {
				return fmt.Errorf("infrastructure.observability.loki.label_mapping_by_env[%s].service_map[%s] references unknown service/workload identity", env, resourceID)
			}
		}
	}
	return nil
}
