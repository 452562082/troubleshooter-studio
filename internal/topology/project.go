package topology

import (
	"sort"
	"strings"
)

type ServiceNode struct {
	Service    string   `json:"service" yaml:"service"`
	Repo       string   `json:"repo,omitempty" yaml:"repo,omitempty"`
	Role       string   `json:"role,omitempty" yaml:"role,omitempty"`
	Upstream   []string `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	Downstream []string `json:"downstream,omitempty" yaml:"downstream,omitempty"`
}

type RouteRef struct {
	Protocol     string `json:"protocol" yaml:"protocol"`
	Method       string `json:"method,omitempty" yaml:"method,omitempty"`
	Path         string `json:"path,omitempty" yaml:"path,omitempty"`
	RPCMethod    string `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
	EndpointEdge string `json:"endpoint_edge" yaml:"endpoint_edge"`
}

type ServiceEdge struct {
	From       string     `json:"from" yaml:"from"`
	To         string     `json:"to" yaml:"to"`
	Status     string     `json:"status" yaml:"status"`
	Confidence float64    `json:"confidence" yaml:"confidence"`
	Routes     []RouteRef `json:"routes" yaml:"routes"`
}

type ServiceGraph struct {
	Services map[string]ServiceNode `json:"services" yaml:"services"`
	Edges    []ServiceEdge          `json:"edges" yaml:"edges"`
}

type Query struct {
	StartService string `json:"start_service" yaml:"start_service"`
	Protocol     string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	Method       string `json:"method,omitempty" yaml:"method,omitempty"`
	Path         string `json:"path,omitempty" yaml:"path,omitempty"`
	MaxDepth     int    `json:"max_depth,omitempty" yaml:"max_depth,omitempty"`
}

type Path struct {
	Services []string      `json:"services" yaml:"services"`
	Edges    []ServiceEdge `json:"edges" yaml:"edges"`
	Score    float64       `json:"score" yaml:"score"`
}

// ProjectServiceGraph builds the formal service graph. Candidate, rejected and
// stale endpoint evidence remains in Snapshot but cannot affect this view.
func ProjectServiceGraph(snapshot Snapshot) ServiceGraph {
	graph := ServiceGraph{Services: make(map[string]ServiceNode)}
	for _, endpoint := range snapshot.Endpoints {
		service := strings.TrimSpace(endpoint.Service)
		if service == "" {
			continue
		}
		node := graph.Services[service]
		node.Service = service
		repo := strings.TrimSpace(endpoint.Repo)
		if node.Repo == "" || repo != "" && repo < node.Repo {
			node.Repo = repo
		}
		graph.Services[service] = node
	}

	pairs := make(map[string]*ServiceEdge)
	for _, candidate := range snapshot.Edges {
		if !isFormalStatus(candidate.Status) {
			continue
		}
		from := strings.TrimSpace(candidate.FromService)
		to := strings.TrimSpace(candidate.ToService)
		if from == "" || to == "" {
			continue
		}
		ensureServiceNode(graph.Services, from)
		ensureServiceNode(graph.Services, to)

		key := from + "\x1f" + to
		projected := pairs[key]
		if projected == nil {
			projected = &ServiceEdge{From: from, To: to, Status: candidate.Status, Confidence: candidate.Confidence}
			pairs[key] = projected
		} else {
			if formalStatusPriority(candidate.Status) > formalStatusPriority(projected.Status) {
				projected.Status = candidate.Status
			}
			if candidate.Confidence > projected.Confidence {
				projected.Confidence = candidate.Confidence
			}
		}
		projected.Routes = append(projected.Routes, routeReference(candidate))
	}

	graph.Edges = make([]ServiceEdge, 0, len(pairs))
	for _, projected := range pairs {
		sortRouteReferences(projected.Routes)
		graph.Edges = append(graph.Edges, *projected)
		from := graph.Services[projected.From]
		from.Downstream = appendUniqueString(from.Downstream, projected.To)
		graph.Services[projected.From] = from
		to := graph.Services[projected.To]
		to.Upstream = appendUniqueString(to.Upstream, projected.From)
		graph.Services[projected.To] = to
	}

	sort.Slice(graph.Edges, func(i, j int) bool {
		left, right := graph.Edges[i], graph.Edges[j]
		if left.From != right.From {
			return left.From < right.From
		}
		return left.To < right.To
	})
	for service, node := range graph.Services {
		sort.Strings(node.Upstream)
		sort.Strings(node.Downstream)
		graph.Services[service] = node
	}
	return graph
}

func isFormalStatus(status string) bool {
	switch status {
	case "automatic", "confirmed", "manual":
		return true
	default:
		return false
	}
}

func formalStatusPriority(status string) int {
	switch status {
	case "manual":
		return 3
	case "confirmed":
		return 2
	case "automatic":
		return 1
	default:
		return 0
	}
}

func ensureServiceNode(services map[string]ServiceNode, service string) {
	node := services[service]
	node.Service = service
	services[service] = node
}

func routeReference(candidate CandidateEdge) RouteRef {
	protocol := normalizedProtocol(candidate.Protocol)
	reference := RouteRef{
		Protocol:     protocol,
		EndpointEdge: endpointEdgeReference(candidate),
	}
	if protocol == "http" {
		reference.Method = NormalizeHTTPMethod(candidate.Method)
		reference.Path = NormalizePath(candidate.Path)
	} else if protocol == "grpc" {
		reference.RPCMethod = normalizeRPCMethod(candidate.RPCMethod)
	}
	return reference
}

func endpointEdgeReference(candidate CandidateEdge) string {
	if candidate.FromEndpoint != "" || candidate.ToEndpoint != "" {
		return candidate.FromEndpoint + ">" + candidate.ToEndpoint
	}
	return candidateSemanticKey(candidate)
}

func sortRouteReferences(routes []RouteRef) {
	sort.SliceStable(routes, func(i, j int) bool {
		left, right := routes[i], routes[j]
		leftKey := []string{left.Protocol, left.Method, left.Path, left.RPCMethod, left.EndpointEdge}
		rightKey := []string{right.Protocol, right.Method, right.Path, right.RPCMethod, right.EndpointEdge}
		for index := range leftKey {
			if leftKey[index] != rightKey[index] {
				return leftKey[index] < rightKey[index]
			}
		}
		return false
	})
}

// FindPaths returns every acyclic non-empty path from StartService up to the
// requested depth. Route fields constrain only the entry edge.
func FindPaths(graph ServiceGraph, query Query) []Path {
	start := strings.TrimSpace(query.StartService)
	if start == "" {
		return nil
	}
	maxDepth := query.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	adjacency := make(map[string][]ServiceEdge)
	for _, edge := range graph.Edges {
		if edge.From == "" || edge.To == "" {
			continue
		}
		adjacency[edge.From] = append(adjacency[edge.From], edge)
	}
	for service := range adjacency {
		sortServiceEdges(adjacency[service])
	}

	seen := map[string]bool{start: true}
	var paths []Path
	var visit func(string, []string, []ServiceEdge)
	visit = func(service string, services []string, edges []ServiceEdge) {
		if len(edges) >= maxDepth {
			return
		}
		for _, next := range adjacency[service] {
			if len(edges) == 0 && !firstHopMatches(next, query) {
				continue
			}
			if seen[next.To] {
				continue
			}

			nextServices := append(append([]string(nil), services...), next.To)
			nextEdges := append(append([]ServiceEdge(nil), edges...), next)
			path := Path{Services: nextServices, Edges: nextEdges, Score: pathConfidence(nextEdges)}
			paths = append(paths, path)

			seen[next.To] = true
			visit(next.To, nextServices, nextEdges)
			delete(seen, next.To)
		}
	}
	visit(start, []string{start}, nil)

	sort.SliceStable(paths, func(i, j int) bool {
		left, right := paths[i], paths[j]
		leftStatus := pathStatusPriority(left)
		rightStatus := pathStatusPriority(right)
		if leftStatus != rightStatus {
			return leftStatus > rightStatus
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if len(left.Edges) != len(right.Edges) {
			return len(left.Edges) < len(right.Edges)
		}
		return strings.Join(left.Services, "\x00") < strings.Join(right.Services, "\x00")
	})
	return paths
}

func sortServiceEdges(edges []ServiceEdge) {
	sort.SliceStable(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		if left.To != right.To {
			return left.To < right.To
		}
		if left.Status != right.Status {
			return left.Status < right.Status
		}
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		return routeReferencesKey(left.Routes) < routeReferencesKey(right.Routes)
	})
}

func routeReferencesKey(routes []RouteRef) string {
	parts := make([]string, 0, len(routes))
	for _, route := range routes {
		parts = append(parts, strings.Join([]string{route.Protocol, route.Method, route.Path, route.RPCMethod, route.EndpointEdge}, "\x1f"))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func firstHopMatches(edge ServiceEdge, query Query) bool {
	if strings.TrimSpace(query.Protocol) == "" && strings.TrimSpace(query.Method) == "" && strings.TrimSpace(query.Path) == "" {
		return true
	}
	for _, route := range edge.Routes {
		protocol := normalizedProtocol(route.Protocol)
		if wanted := normalizedProtocol(query.Protocol); wanted != "" && wanted != protocol {
			continue
		}
		if query.Method != "" {
			if protocol == "grpc" {
				if normalizeRPCMethod(query.Method) != normalizeRPCMethod(route.RPCMethod) {
					continue
				}
			} else if !methodsCompatible(query.Method, route.Method) {
				continue
			}
		}
		if query.Path != "" && NormalizePath(query.Path) != NormalizePath(route.Path) {
			continue
		}
		return true
	}
	return false
}

func pathConfidence(edges []ServiceEdge) float64 {
	if len(edges) == 0 {
		return 0
	}
	confidence := edges[0].Confidence
	for _, edge := range edges[1:] {
		if edge.Confidence < confidence {
			confidence = edge.Confidence
		}
	}
	return confidence
}

func pathStatusPriority(path Path) int {
	priority := 0
	for _, edge := range path.Edges {
		if current := formalStatusPriority(edge.Status); current > priority {
			priority = current
		}
	}
	if priority >= formalStatusPriority("confirmed") {
		return 1
	}
	return 0
}
