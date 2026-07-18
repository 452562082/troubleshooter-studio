// migrate.go —— 老 yaml schema → 新 schema 的迁移函数。
// LoadFromBytes 在 Validate 之前先跑这两个 migrate*,这样老 yaml 单源 + 平铺 obs URL
// 走完迁移就符合新 schema,validate 不用再为兼容老结构留特例。
package config

import "fmt"

func migrateObservabilityEndpoints(cfg *SystemConfig) {
	obs := &cfg.Infrastructure.Observability

	fillURL := func(m *map[string]string, eps []ObsEndpoint) {
		for _, ep := range eps {
			if ep.Env == "" || ep.URL == "" {
				continue
			}
			if *m == nil {
				*m = map[string]string{}
			}
			if _, exists := (*m)[ep.Env]; !exists {
				(*m)[ep.Env] = ep.URL
			}
		}
	}

	fillURL(&obs.Grafana.URLByEnv, obs.Grafana.Endpoints)
	fillURL(&obs.Jaeger.URLByEnv, obs.Jaeger.Endpoints)
	fillURL(&obs.SkyWalking.URLByEnv, obs.SkyWalking.Endpoints)
	fillURL(&obs.Tempo.URLByEnv, obs.Tempo.Endpoints)
	fillURL(&obs.K8sRuntime.URLByEnv, obs.K8sRuntime.Endpoints)
	// Loki / Prometheus 没 URLByEnv map 字段(模板里它俩走 Grafana 代理只需要 datasource_uid_by_env);
	// 即便用户填了 endpoints,目前没消费点,所以这里不动。

	// ELK 的 url 在 endpoints 里以 kibana_url / es_url 区分(GUI wizard `web/src/pages/InitPage.vue:2068-2069`)
	for _, ep := range obs.ELK.Endpoints {
		if ep.Env == "" {
			continue
		}
		if ep.KibanaURL != "" {
			if obs.ELK.KibanaByEnv == nil {
				obs.ELK.KibanaByEnv = map[string]string{}
			}
			if _, ok := obs.ELK.KibanaByEnv[ep.Env]; !ok {
				obs.ELK.KibanaByEnv[ep.Env] = ep.KibanaURL
			}
		}
		if ep.ESURL != "" {
			if obs.ELK.ESByEnv == nil {
				obs.ELK.ESByEnv = map[string]string{}
			}
			if _, ok := obs.ELK.ESByEnv[ep.Env]; !ok {
				obs.ELK.ESByEnv[ep.Env] = ep.ESURL
			}
		}
	}

	// loki.label_mapping_by_env.<env>.grafana_ds_uid → Loki.DatasourceUIDByEnv
	for env, lm := range obs.Loki.LabelMappingByEnv {
		if env == "" || lm.GrafanaDSUID == "" {
			continue
		}
		if obs.Loki.DatasourceUIDByEnv == nil {
			obs.Loki.DatasourceUIDByEnv = map[string]string{}
		}
		if _, ok := obs.Loki.DatasourceUIDByEnv[env]; !ok {
			obs.Loki.DatasourceUIDByEnv[env] = lm.GrafanaDSUID
		}
	}
}

// migrateLegacyConfigCenter 把老 schema 的 infrastructure.config_center(单数)
// 迁到新 schema 的 infrastructure.config_centers(数组)。
//
// 规则:
//  1. 已有 ConfigCenters[]:以新字段为准,把 ConfigCenter 同步成 ConfigCenters[0] 副本
//     (供老 template / 老代码做"主源"读)。
//  2. 只有 ConfigCenter.Type:把它包成单元素 ConfigCenters,id 默认 "default";
//     ConfigCenter 字段保留作为同份数据的副本。
//  3. 都为空:跳过。
//
// 注意:刻意 NOT 清掉 ConfigCenter —— 大量 *.tmpl 模板和老代码读 .Infrastructure.ConfigCenter,
// 维持成"主源(ConfigCenters[0])镜像"让它们不破。多源感知逻辑(prompts/MCP/install)
// 必须显式遍历 ConfigCenters,这层在编码层面已分离开,不会回退。
//
// 同时给每个 repo 兜底 ConfigSource:
//   - 已写就保留(新 yaml 直接用)
//   - 空时绑到 ConfigCenters[0].id(老 yaml 隐式只用一个源)
//   - ConfigCenters 也空时 ConfigSource 保持空(无配置中心场景)
func migrateLegacyConfigCenter(cfg *SystemConfig) {
	if len(cfg.Infrastructure.ConfigCenters) == 0 && cfg.Infrastructure.ConfigCenter.Type != "" {
		legacy := cfg.Infrastructure.ConfigCenter
		if legacy.ID == "" {
			legacy.ID = "default"
		}
		cfg.Infrastructure.ConfigCenters = []ConfigCenter{legacy}
	}
	// 保持 ConfigCenter 作为 ConfigCenters[0] 的镜像(老 template/代码兼容点)
	if len(cfg.Infrastructure.ConfigCenters) > 0 {
		cfg.Infrastructure.ConfigCenter = cfg.Infrastructure.ConfigCenters[0]
	} else {
		cfg.Infrastructure.ConfigCenter = ConfigCenter{}
	}

	if len(cfg.Infrastructure.ConfigCenters) == 0 {
		return
	}
	defaultSource := cfg.Infrastructure.ConfigCenters[0].ID
	for i := range cfg.Repos {
		if cfg.Repos[i].ConfigSource == "" {
			cfg.Repos[i].ConfigSource = defaultSource
		}
	}
}

// migrateResourceCatalog derives the normalized catalog for legacy YAML. New
// YAML that already declares catalog entries remains authoritative. The legacy
// fields stay populated because older templates and external integrations may
// still read them during the compatibility window.
func migrateResourceCatalog(cfg *SystemConfig) {
	// Give every data-store instance a stable ID first, including explicit
	// catalogs that may reference a legacy data_stores block without IDs.
	typeCount := map[string]int{}
	usedIDs := map[string]bool{}
	for i := range cfg.Infrastructure.DataStores {
		ds := &cfg.Infrastructure.DataStores[i]
		if ds.ID != "" {
			usedIDs[ds.ID] = true
			continue
		}
		typeCount[ds.Type]++
		candidate := ds.Type
		if typeCount[ds.Type] > 1 || usedIDs[candidate] {
			candidate = fmt.Sprintf("%s-%d", ds.Type, typeCount[ds.Type])
		}
		for usedIDs[candidate] {
			typeCount[ds.Type]++
			candidate = fmt.Sprintf("%s-%d", ds.Type, typeCount[ds.Type])
		}
		ds.ID = candidate
		usedIDs[candidate] = true
	}

	if len(cfg.ResourceCatalog.Services) == 0 {
		byID := map[string]*ServiceResource{}
		for _, repo := range cfg.Repos {
			for _, service := range repo.ServiceNames {
				if service == "" {
					continue
				}
				if _, exists := byID[service]; exists {
					continue
				}
				item := ServiceResource{
					ID: service, Repository: repo.Name,
					ConfigSources: map[string]string{}, DataStores: map[string][]string{}, Workloads: map[string]string{},
				}
				cfg.ResourceCatalog.Services = append(cfg.ResourceCatalog.Services, item)
				byID[service] = &cfg.ResourceCatalog.Services[len(cfg.ResourceCatalog.Services)-1]
			}
		}
		// Slice growth can move entries, so rebuild pointers before enriching.
		byID = map[string]*ServiceResource{}
		for i := range cfg.ResourceCatalog.Services {
			byID[cfg.ResourceCatalog.Services[i].ID] = &cfg.ResourceCatalog.Services[i]
		}
		for _, ds := range cfg.Infrastructure.DataStores {
			for _, ep := range ds.Endpoints {
				item := byID[ep.Service]
				if item == nil || ep.Env == "" {
					continue
				}
				item.DataStores[ep.Env] = appendUnique(item.DataStores[ep.Env], ds.ID)
			}
		}
	}

	if len(cfg.ResourceCatalog.Workloads) == 0 {
		workloads := map[string]int{}
		serviceByID := map[string]*ServiceResource{}
		for i := range cfg.ResourceCatalog.Services {
			serviceByID[cfg.ResourceCatalog.Services[i].ID] = &cfg.ResourceCatalog.Services[i]
		}
		for _, entry := range cfg.Infrastructure.Observability.K8sRuntime.ServiceMap {
			id := entry.Service
			if id == "" {
				continue
			}
			idx, exists := workloads[id]
			if !exists {
				repository := ""
				if service := serviceByID[entry.Service]; service != nil {
					repository = service.Repository
				} else {
					for _, repo := range cfg.Repos {
						if repo.Name == entry.Service {
							repository = repo.Name
							break
						}
					}
				}
				if repository == "" {
					continue
				}
				cfg.ResourceCatalog.Workloads = append(cfg.ResourceCatalog.Workloads, WorkloadResource{
					ID: id, Repository: repository, Service: entry.Service, Names: map[string]string{},
				})
				idx = len(cfg.ResourceCatalog.Workloads) - 1
				workloads[id] = idx
			}
			item := &cfg.ResourceCatalog.Workloads[idx]
			name := entry.Workload
			if name == "" {
				name = entry.Service
			}
			item.Names[entry.Env] = name
			if service := serviceByID[entry.Service]; service != nil {
				service.Workloads[entry.Env] = id
			}
		}
		// Frontend/mobile repositories are runtime identities even when they have
		// no configuration-center service name or current K8s mapping.
		for _, repo := range cfg.Repos {
			if repo.EffectiveRole() != RoleFrontend && repo.EffectiveRole() != RoleMobile {
				continue
			}
			if _, exists := workloads[repo.Name]; exists || repo.Name == "" {
				continue
			}
			names := map[string]string{}
			for _, env := range cfg.Environments {
				names[env.ID] = repo.Name
			}
			cfg.ResourceCatalog.Workloads = append(cfg.ResourceCatalog.Workloads, WorkloadResource{
				ID: repo.Name, Repository: repo.Name, Names: names,
			})
		}
	}
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
