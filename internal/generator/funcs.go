package generator

import (
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func toFindingView(f analyzer.Finding) *findingView {
	return &findingView{
		ConfigCenter:   f.ConfigCenter,
		SourceFile:     f.SourceFile,
		ServerAddr:     f.ServerAddr,
		DataID:         f.DataID,
		Group:          f.Group,
		NamespaceID:    f.NamespaceID,
		AppID:          f.AppID,
		Namespaces:     f.Namespaces,
		Cluster:        f.Cluster,
		KVPrefix:       f.KVPrefix,
		DefaultContext: f.DefaultContext,
	}
}

var dataStoreSkillAlias = map[string]string{
	"elasticsearch": "es",
}

func dataStoreSkillName(typ string) string {
	if alias, ok := dataStoreSkillAlias[typ]; ok {
		return alias + "-runtime-query"
	}
	return typ + "-runtime-query"
}

func skillEnabled(ctx *Context, name string) bool {
	whitelist := ctx.Generation.SkillsWhitelist
	if len(whitelist) > 0 && !slices.Contains(whitelist, name) {
		return false
	}
	if name == "code-intelligence-query" {
		return ctx.CodeIntelligence.UsesCodeGraph() &&
			(len(whitelist) == 0 || slices.Contains(whitelist, "routing"))
	}
	return true
}

func frontendEndpointsForRepo(ctx *Context, repoName string) []string {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, endpoint := range ctx.FrontendEndpointsByRepo[repoName] {
		path := normalizeRoutePathForTemplate(endpoint)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func frontendCandidateServicesForRepo(ctx *Context, repoName string) []string {
	if ctx == nil {
		return nil
	}
	var services []string
	for _, repo := range ctx.Repos {
		if !repo.RequiresServiceNames() || repo.Name == repoName {
			continue
		}
		services = append(services, repo.ServiceNames...)
	}
	return services
}

type frontendRouteCandidate struct {
	Service string
	Match   string
	Route   string
	Method  string
	Source  string
}

func frontendRouteCandidatesForRepoEndpoint(ctx *Context, frontendRepo, endpoint string) []frontendRouteCandidate {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []frontendRouteCandidate
	for _, repo := range ctx.Repos {
		if repo.Name == frontendRepo || !repo.RequiresServiceNames() {
			continue
		}
		names := repo.ServiceNames
		if len(names) == 0 {
			names = []string{repo.Name}
		}
		for _, route := range ctx.APIRoutesByRepo[repo.Name] {
			routePath := normalizeRoutePathForTemplate(route.Path)
			match := routeMatchStrengthForTemplate(routePath, endpoint)
			if match == "" {
				continue
			}
			for _, svc := range names {
				key := svc + "|" + match + "|" + routePath + "|" + route.Method + "|" + route.Source
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, frontendRouteCandidate{
					Service: svc,
					Match:   match,
					Route:   routePath,
					Method:  route.Method,
					Source:  route.Source,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if routeMatchRank(out[i].Match) != routeMatchRank(out[j].Match) {
			return routeMatchRank(out[i].Match) < routeMatchRank(out[j].Match)
		}
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		if out[i].Route != out[j].Route {
			return out[i].Route < out[j].Route
		}
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func routeMatchStrengthForTemplate(routePath, endpointPath string) string {
	routePath = normalizeRoutePathForTemplate(routePath)
	endpointPath = normalizeRoutePathForTemplate(endpointPath)
	if routePath == "" || endpointPath == "" {
		return ""
	}
	if routePath == endpointPath {
		return "exact"
	}

	routeParts := strings.Split(strings.Trim(routePath, "/"), "/")
	endpointParts := strings.Split(strings.Trim(endpointPath, "/"), "/")
	if len(routeParts) == len(endpointParts) {
		matched := true
		hasParam := false
		for i := range routeParts {
			if isRouteParamForTemplate(routeParts[i]) {
				hasParam = true
				continue
			}
			if routeParts[i] != endpointParts[i] {
				matched = false
				break
			}
		}
		if matched && hasParam {
			return "pattern"
		}
	}

	if strings.HasPrefix(endpointPath, strings.TrimRight(routePath, "/")+"/") {
		return "prefix"
	}
	return ""
}

func normalizeRoutePathForTemplate(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if u, err := url.Parse(path); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		path = u.Path
	}
	if before, _, ok := strings.Cut(path, "#"); ok {
		path = before
	}
	if before, _, ok := strings.Cut(path, "?"); ok {
		path = before
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}
	if path != "/graphql" && path != "/api" && !strings.HasPrefix(path, "/api/") {
		return ""
	}
	return path
}

func isRouteParamForTemplate(part string) bool {
	return part == "*" ||
		strings.HasPrefix(part, ":") ||
		(strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}")) ||
		(strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">"))
}

func routeMatchRank(match string) int {
	switch match {
	case "exact":
		return 0
	case "pattern":
		return 1
	case "prefix":
		return 2
	default:
		return 3
	}
}

func topologyServiceNodes(ctx *Context) []topology.ServiceNode {
	if ctx == nil {
		return nil
	}
	names := make([]string, 0, len(ctx.ServiceGraph.Services))
	for name := range ctx.ServiceGraph.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]topology.ServiceNode, 0, len(names))
	for _, name := range names {
		result = append(result, ctx.ServiceGraph.Services[name])
	}
	return result
}

func canonicalRepoServices(repo config.Repo) []string {
	seen := make(map[string]bool, len(repo.ServiceNames)+1)
	services := make([]string, 0, len(repo.ServiceNames)+1)
	for _, raw := range repo.ServiceNames {
		service := strings.TrimSpace(raw)
		if service == "" || seen[service] {
			continue
		}
		seen[service] = true
		services = append(services, service)
	}
	if len(services) == 0 {
		if name := strings.TrimSpace(repo.Name); name != "" {
			services = append(services, name)
		}
	}
	sort.Strings(services)
	return services
}

// dependencyServicesForRepo returns the canonical service keys emitted by the
// compatibility dependency map. Once topology analysis has been attempted,
// the formal graph/catalog owns those keys; raw YAML service_names must not
// reintroduce whitespace variants, duplicates, or services absent from the
// analyzed catalog. Legacy reports retain the old repo-derived behavior after
// applying the same trim/deduplicate normalization as the topology pipeline.
func dependencyServicesForRepo(ctx *Context, repo config.Repo) []string {
	if ctx == nil || !repo.IsServiceNode() {
		return nil
	}
	if ctx.Topology.SchemaVersion == "" {
		ownerByService := make(map[string]string)
		for _, candidateRepo := range ctx.Repos {
			if !candidateRepo.IsServiceNode() {
				continue
			}
			owner := strings.TrimSpace(candidateRepo.Name)
			for _, service := range canonicalRepoServices(candidateRepo) {
				if _, exists := ownerByService[service]; !exists {
					ownerByService[service] = owner
				}
			}
		}
		owner := strings.TrimSpace(repo.Name)
		services := canonicalRepoServices(repo)
		return slices.DeleteFunc(services, func(service string) bool {
			return ownerByService[service] != owner
		})
	}

	repoName := strings.TrimSpace(repo.Name)
	services := make([]string, 0, len(ctx.ServiceGraph.Services))
	for name, node := range ctx.ServiceGraph.Services {
		service := strings.TrimSpace(name)
		if service == "" || strings.TrimSpace(node.Repo) != repoName {
			continue
		}
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

func scannedDownstreamsForService(ctx *Context, service string) []string {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	var result []string
	for _, repo := range ctx.Repos {
		matches := false
		for _, name := range canonicalRepoServices(repo) {
			if name == service {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		for _, call := range ctx.DownstreamCallsByRepo[repo.Name] {
			if call.Target == "" || seen[call.Target] {
				continue
			}
			seen[call.Target] = true
			result = append(result, call.Target)
		}
	}
	sort.Strings(result)
	return result
}

func legacyUpstreamsForService(ctx *Context, service string) []string {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	var result []string
	for _, repo := range ctx.Repos {
		matched := false
		for _, call := range ctx.DownstreamCallsByRepo[repo.Name] {
			if call.Target == service {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, name := range canonicalRepoServices(repo) {
			if name == service || seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func topologyDownstreamsForService(ctx *Context, service string) []string {
	if ctx == nil {
		return nil
	}
	if ctx.Topology.SchemaVersion == "" {
		return scannedDownstreamsForService(ctx, service)
	}
	node, ok := ctx.ServiceGraph.Services[service]
	if !ok {
		return nil
	}
	return append([]string(nil), node.Downstream...)
}

func topologyUpstreamsForService(ctx *Context, service string) []string {
	if ctx == nil {
		return nil
	}
	if ctx.Topology.SchemaVersion == "" {
		return legacyUpstreamsForService(ctx, service)
	}
	node, ok := ctx.ServiceGraph.Services[service]
	if !ok {
		return nil
	}
	return append([]string(nil), node.Upstream...)
}

func topologyEvidenceID(edge topology.CandidateEdge) string {
	if edge.FromEndpoint != "" || edge.ToEndpoint != "" {
		return edge.FromEndpoint + ">" + edge.ToEndpoint
	}
	formal := edge
	formal.Status = "automatic"
	graph := topology.ProjectServiceGraph(topology.Snapshot{Edges: []topology.CandidateEdge{formal}})
	if len(graph.Edges) == 1 && len(graph.Edges[0].Routes) == 1 {
		return graph.Edges[0].Routes[0].EndpointEdge
	}
	return strings.Join([]string{edge.FromService, edge.ToService, edge.Protocol, edge.Method, edge.Path, edge.RPCMethod}, "|")
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"upper":                                  strings.ToUpper,
		"lower":                                  strings.ToLower,
		"dataStoreSkill":                         dataStoreSkillName,
		"frontendEndpointsForRepo":               frontendEndpointsForRepo,
		"frontendCandidateServicesForRepo":       frontendCandidateServicesForRepo,
		"frontendRouteCandidatesForRepoEndpoint": frontendRouteCandidatesForRepoEndpoint,
		"dependencyServicesForRepo":              dependencyServicesForRepo,
		"topologyServiceNodes":                   topologyServiceNodes,
		"topologyEvidenceID":                     topologyEvidenceID,
		"topologyDownstreamsForService":          topologyDownstreamsForService,
		"topologyUpstreamsForService":            topologyUpstreamsForService,
		"yamlQuote":                              yamlQuote,
		"list":                                   func(items ...string) []string { return items },
		"hasSkill": func(ctx *Context, name string) bool {
			return skillEnabled(ctx, name)
		},
		// findConfig 返回给定 service+env 的 analyzer finding 视图；命中优先级：精确 env > 无 env 默认 > nil
		"findConfig": func(ctx *Context, service, env string) *findingView {
			byEnv, ok := ctx.Findings[service]
			if !ok {
				return nil
			}
			if f, ok := byEnv[env]; ok {
				return toFindingView(f)
			}
			if f, ok := byEnv[""]; ok {
				return toFindingView(f)
			}
			return nil
		},
		// findPrior 返回来自上次生成产物中的人工 override；命中优先级同上
		"findPrior": func(ctx *Context, service, env string) *findingView {
			byEnv, ok := ctx.PriorOverrides[service]
			if !ok {
				return nil
			}
			if f, ok := byEnv[env]; ok {
				return toFindingView(f)
			}
			if f, ok := byEnv[""]; ok {
				return toFindingView(f)
			}
			return nil
		},
		// repoConfigSource 找到拥有 service 的 repo,返回它的 config_source。
		// 没找到 / 没绑(单源场景):返回 "default"。
		"repoConfigSource": func(ctx *Context, service string) string {
			for _, r := range ctx.Repos {
				names := r.ServiceNames
				if len(names) == 0 {
					names = []string{r.Name}
				}
				for _, sn := range names {
					if sn == service {
						if r.ConfigSource != "" {
							return r.ConfigSource
						}
						return "default"
					}
				}
			}
			return "default"
		},
		// configSourceByID 按 id 找 ConfigCenters[] 里的源;找不到返回主源(兜底,
		// 避免 yaml 引用不存在 id 时模板崩),配 doctor 检查给用户预警。
		"configSourceByID": func(ctx *Context, id string) ConfigCenterView {
			for _, cc := range ctx.Infrastructure.ConfigCenters {
				if cc.ID == id {
					return toConfigCenterView(cc)
				}
			}
			return toConfigCenterView(ctx.Infrastructure.PrimaryConfigCenter())
		},
		// configServiceMapEntry 按 source/env/service 读取配置中心 service_map。
		// 注意:这跟 observability.k8s_runtime.service_map 是两张不同的表:
		//   - config center service_map:cluster_id/namespace/configmap,用于读配置
		//   - k8s runtime service_map:workload/label_selector,用于查 pod/log
		"configServiceMapEntry": func(ctx *Context, sourceID, env, service string) *serviceMapEntryView {
			for _, cc := range ctx.Infrastructure.ConfigCenters {
				if sourceID != "default" && cc.ID != sourceID {
					continue
				}
				if sourceID == "default" && cc.ID != "" && len(ctx.Infrastructure.ConfigCenters) > 1 {
					continue
				}
				if bySvc, ok := cc.ServiceMap[env]; ok {
					if entry, ok := bySvc[service]; ok {
						return toServiceMapEntryView(entry)
					}
				}
				if sourceID != "default" || len(ctx.Infrastructure.ConfigCenters) <= 1 {
					break
				}
			}
			if sourceID == "default" {
				cc := ctx.Infrastructure.PrimaryConfigCenter()
				if bySvc, ok := cc.ServiceMap[env]; ok {
					if entry, ok := bySvc[service]; ok {
						return toServiceMapEntryView(entry)
					}
				}
			}
			return nil
		},
		// yamlDataStoresForService 从 cfg.Infrastructure.DataStores[].Endpoints[].Service 抽
		// 当前服务用了哪些数据层 type,返回 ["<type>:<type>"] 列表(逻辑名缺省用 type 自身,
		// 用户可后续自己改,如 "mysql:mysql" → "mysql:order_db")。比 dependency_scan 走代码扫描
		// 更准:wizard 已经收过 (env, service, endpoint) 三元组,直接拿来用。
		"yamlDataStoresForService": func(ctx *Context, svc string) []string {
			seen := map[string]bool{}
			var out []string
			for _, ds := range ctx.Infrastructure.DataStores {
				if !ds.Enabled {
					continue
				}
				for _, ep := range ds.Endpoints {
					if ep.Service == svc {
						key := ds.Type + ":" + ds.Type
						if !seen[key] {
							seen[key] = true
							out = append(out, key)
						}
						break // 同 type 同 service 多 env 只算一次
					}
				}
			}
			return out
		},
		// lokiAppForService 从 loki.label_mapping_by_env[env].service_map[service].app
		// 抽出"用户在 wizard 里手挑过的 Loki app 名"。命中即返回真实 app 名,没填回空串。
		// log-app-map.yaml 用这个填 verifiedApp,免得每条都让用户再手填一遍 ——
		// wizard Step 7 已经收过同一份数据。
		"lokiAppForService": func(ctx *Context, env, service string) string {
			lm, ok := ctx.Infrastructure.Observability.Loki.LabelMappingByEnv[env]
			if !ok {
				return ""
			}
			svc, ok := lm.ServiceMap[service]
			if !ok {
				return ""
			}
			return svc["app"]
		},
		// mcpKeyForSource 跟 internal/agent/install_naming.go 的 mcpKey 保持镜像:
		//   sourceID=="default" 或空 → "<prefix>-<env>"(老命名,向后兼容)
		//   显式多源 → "<prefix>-<sourceID>-<env>"
		"mcpKeyForSource": func(prefix, sourceID, envID string) string {
			if sourceID == "" || sourceID == "default" {
				return prefix + "-" + envID
			}
			return prefix + "-" + sourceID + "-" + envID
		},
		// scannedDataStoresForService 给 service-dependency-map.yaml.tmpl 用:类似上面,提数据层使用。
		// 输出格式 "<type>:scanned"(没 logical 名字时占位 "scanned",用户填具体逻辑名)。
		"scannedDataStoresForService": func(ctx *Context, svc string) []string {
			seen := map[string]bool{}
			var out []string
			for _, repo := range ctx.Repos {
				match := false
				if repo.Name == svc {
					match = true
				}
				for _, sn := range repo.ServiceNames {
					if sn == svc {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				usages := ctx.DataStoreUsagesByRepo[repo.Name]
				for _, u := range usages {
					if u.Type == "" {
						continue
					}
					logical := u.Logical
					if logical == "" {
						logical = "scanned"
					}
					key := u.Type + ":" + logical
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, key)
				}
			}
			return out
		},
		// scannedSchemaTablesForService 给 data-schema-map.yaml.tmpl 用:从 ctx 找指定服务名
		// 对应 repo 的 SchemaTables。同 repo 多服务时复用 repo 的 schema 列表。
		"scannedSchemaTablesForService": func(ctx *Context, svc string) []analyzer.SchemaTable {
			seen := map[string]bool{}
			var out []analyzer.SchemaTable
			for _, repo := range ctx.Repos {
				match := false
				if repo.Name == svc {
					match = true
				}
				for _, sn := range repo.ServiceNames {
					if sn == svc {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				for _, t := range ctx.SchemaTablesByRepo[repo.Name] {
					key := t.Name + "|" + t.Kind
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, t)
				}
			}
			return out
		},
		// mcpKeyForAgentSource 跟 mcpKeyForAgent 镜像:加 agent-id 前缀,Claude Code/Cursor
		// 共享 settings.json 池场景必备,避免多 system 同名 mcp 互相覆盖。OpenClaw 也走
		// 这条路保持三平台命名统一(install_native_openclaw 同步用 mcpKeyForAgent)。
		// sourceID == prefix 时(用户没改 source.id 直接用 type)做去重,避免出现
		// "truss-nacos-nacos-dev" 这种叠词。
		"mcpKeyForAgentSource": func(agentID, prefix, sourceID, envID string) string {
			base := prefix + "-" + envID
			if sourceID != "" && sourceID != "default" && sourceID != prefix {
				base = prefix + "-" + sourceID + "-" + envID
			}
			if agentID == "" {
				return base
			}
			return agentID + "-" + base
		},
	}
}

// ConfigCenterView 模板侧可访问的 config_centers[] 单元素视图。
// 字段子集来自 config.ConfigCenter,显式列出避免把 config 包整个泄漏到模板。
type ConfigCenterView struct {
	ID             string
	Type           string
	DataIDPatterns []string
	Endpoints      []endpointView
}

type endpointView struct {
	Env           string
	Addr          string
	NamespaceHint string
}

func toConfigCenterView(cc config.ConfigCenter) ConfigCenterView {
	out := ConfigCenterView{
		ID:             cc.ID,
		Type:           cc.Type,
		DataIDPatterns: cc.DataIDPatterns,
	}
	for _, ep := range cc.Endpoints {
		out.Endpoints = append(out.Endpoints, endpointView{
			Env: ep.Env, Addr: ep.Addr, NamespaceHint: ep.NamespaceHint,
		})
	}
	return out
}

type serviceMapEntryView struct {
	Namespace string
	Group     string
	DataID    string
	AppID     string
	Cluster   string
	ClusterID string
	ConfigMap string
}

func toServiceMapEntryView(entry config.ServiceMapEntry) *serviceMapEntryView {
	return &serviceMapEntryView{
		Namespace: entry.Namespace,
		Group:     entry.Group,
		DataID:    entry.DataID,
		AppID:     entry.AppID,
		Cluster:   entry.Cluster,
		ClusterID: entry.ClusterID,
		ConfigMap: entry.ConfigMap,
	}
}

// findingView 是模板侧可访问的结构（复制自 analyzer.Finding，避免直接把 analyzer 包泄漏到模板）
type findingView struct {
	ConfigCenter   string
	SourceFile     string
	ServerAddr     string
	DataID         string
	Group          string
	NamespaceID    string
	AppID          string
	Namespaces     []string
	Cluster        string
	KVPrefix       string
	DefaultContext string
}
