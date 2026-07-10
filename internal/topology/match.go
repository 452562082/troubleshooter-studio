package topology

import (
	"net"
	"net/url"
	"sort"
	"strings"
)

type ServiceDescriptor struct {
	Repo    string
	Service string
	Role    string
	Aliases []string
	Hosts   []string
}

type MatchInput struct {
	Endpoints []Endpoint
	Services  []ServiceDescriptor
}

type MatchResult struct {
	Edges []CandidateEdge
	Hints []CandidateEdge
}

func Match(input MatchInput) MatchResult {
	descriptors := descriptorsByService(input.Services)
	inbound := inboundEndpoints(input.Endpoints)
	var result MatchResult

	for _, outbound := range input.Endpoints {
		if outbound.Direction != DirectionOutbound {
			continue
		}

		candidates := routeCandidates(outbound, inbound)
		matchedTargets := resolvedTargetServices(outbound.TargetHint, candidates, descriptors)
		if len(matchedTargets) > 0 {
			candidates = candidatesForServices(candidates, matchedTargets)
		}
		duplicateRoute := distinctServiceCount(candidates) > 1

		for _, candidate := range candidates {
			edge := matchedRouteEdge(outbound, candidate, descriptors, duplicateRoute)
			if edge.Confidence >= 0.60 {
				result.Edges = append(result.Edges, edge)
			} else {
				result.Hints = append(result.Hints, edge)
			}
		}
	}

	sortCandidateEdges(result.Edges)
	sortCandidateEdges(result.Hints)
	return result
}

type serviceKey struct {
	repo    string
	service string
}

func descriptorsByService(services []ServiceDescriptor) map[serviceKey]ServiceDescriptor {
	result := make(map[serviceKey]ServiceDescriptor, len(services))
	for _, service := range services {
		key := serviceKey{repo: service.Repo, service: service.Service}
		descriptor := result[key]
		descriptor.Repo = service.Repo
		descriptor.Service = service.Service
		if descriptor.Role == "" || service.Role != "" && service.Role < descriptor.Role {
			descriptor.Role = service.Role
		}
		descriptor.Aliases = append(descriptor.Aliases, service.Aliases...)
		descriptor.Hosts = append(descriptor.Hosts, service.Hosts...)
		result[key] = descriptor
	}
	for key, descriptor := range result {
		descriptor.Aliases = sortedUniqueStrings(descriptor.Aliases)
		descriptor.Hosts = sortedUniqueStrings(descriptor.Hosts)
		result[key] = descriptor
	}
	return result
}

func sortedUniqueStrings(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func inboundEndpoints(endpoints []Endpoint) []Endpoint {
	result := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Direction == DirectionInbound {
			result = append(result, endpoint)
		}
	}
	return result
}

type routeMatchKind uint8

const (
	routeExact routeMatchKind = iota
	routeTransformed
	routeSimilar
)

type routeCandidate struct {
	endpoint Endpoint
	kind     routeMatchKind
}

func routeCandidates(outbound Endpoint, inbound []Endpoint) []routeCandidate {
	primary := make([]routeCandidate, 0, len(inbound))
	similar := make([]routeCandidate, 0, len(inbound))
	for _, candidate := range inbound {
		if outbound.Repo == candidate.Repo || !protocolsCompatible(outbound.Protocol, candidate.Protocol) {
			continue
		}
		switch normalizedProtocol(outbound.Protocol) {
		case "http":
			if !methodsCompatible(outbound.Method, candidate.Method) {
				continue
			}
			switch {
			case NormalizePath(outbound.Path) == NormalizePath(candidate.Path):
				primary = append(primary, routeCandidate{endpoint: candidate, kind: routeExact})
			case pathsMatchThroughTransforms(outbound, candidate):
				primary = append(primary, routeCandidate{endpoint: candidate, kind: routeTransformed})
			case pathsHaveSuffixSimilarity(outbound.Path, candidate.Path):
				similar = append(similar, routeCandidate{endpoint: candidate, kind: routeSimilar})
			}
		case "grpc":
			if normalizeRPCMethod(outbound.RPCMethod) == normalizeRPCMethod(candidate.RPCMethod) {
				primary = append(primary, routeCandidate{endpoint: candidate, kind: routeExact})
			}
		}
	}
	if len(primary) > 0 {
		return primary
	}
	return similar
}

func normalizedProtocol(protocol string) string {
	return strings.ToLower(strings.TrimSpace(protocol))
}

func protocolsCompatible(left, right string) bool {
	left = normalizedProtocol(left)
	return left != "" && left == normalizedProtocol(right)
}

func methodsCompatible(left, right string) bool {
	left = NormalizeHTTPMethod(left)
	right = NormalizeHTTPMethod(right)
	return left == right || left == "ANY" || right == "ANY"
}

func normalizeRPCMethod(method string) string {
	return strings.TrimPrefix(strings.TrimSpace(method), "/")
}

func resolvedTargetServices(hint string, candidates []routeCandidate, descriptors map[serviceKey]ServiceDescriptor) map[serviceKey]struct{} {
	result := make(map[serviceKey]struct{})
	for _, candidate := range candidates {
		key := serviceKey{repo: candidate.endpoint.Repo, service: candidate.endpoint.Service}
		if targetEvidence(hint, candidate.endpoint, descriptors[key]) != "" {
			result[key] = struct{}{}
		}
	}
	return result
}

func candidatesForServices(candidates []routeCandidate, services map[serviceKey]struct{}) []routeCandidate {
	result := make([]routeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := services[serviceKey{repo: candidate.endpoint.Repo, service: candidate.endpoint.Service}]; ok {
			result = append(result, candidate)
		}
	}
	return result
}

func distinctServiceCount(endpoints []routeCandidate) int {
	services := make(map[serviceKey]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		services[serviceKey{repo: endpoint.endpoint.Repo, service: endpoint.endpoint.Service}] = struct{}{}
	}
	return len(services)
}

func matchedRouteEdge(outbound Endpoint, match routeCandidate, descriptors map[serviceKey]ServiceDescriptor, duplicateRoute bool) CandidateEdge {
	inbound := match.endpoint
	edge := candidateEdge(outbound, inbound)
	evidence := targetEvidence(outbound.TargetHint, inbound, descriptors[serviceKey{repo: inbound.Repo, service: inbound.Service}])

	if match.kind == routeTransformed {
		edge.Reasons = append(edge.Reasons, "path_transform_proven")
		scoreTransformedRoute(&edge, evidence, duplicateRoute)
	} else if match.kind == routeSimilar {
		edge.Reasons = append(edge.Reasons, "http_method_compatible", "path_suffix_similar")
		scoreSimilarRoute(&edge, evidence)
	} else {
		edge.Reasons = append(edge.Reasons, exactRouteReason(edge.Protocol))
		scoreExactRoute(&edge, evidence, duplicateRoute)
	}

	edge.Confidence = clampConfidence(edge.Confidence)
	edge.Status = statusForConfidence(edge.Confidence)
	return edge
}

func scoreExactRoute(edge *CandidateEdge, evidence string, duplicateRoute bool) {
	switch {
	case evidence != "":
		edge.Confidence = 0.98
		edge.Reasons = append(edge.Reasons, evidence)
	case duplicateRoute:
		edge.Confidence = 0.55
		edge.Conflicts = append(edge.Conflicts, "target_unresolved", "route_duplicated_across_services")
	default:
		edge.Confidence = 0.76
		edge.Conflicts = append(edge.Conflicts, "target_unresolved")
	}
}

func scoreTransformedRoute(edge *CandidateEdge, evidence string, duplicateRoute bool) {
	switch {
	case evidence != "":
		edge.Confidence = 0.90
		edge.Reasons = append(edge.Reasons, evidence)
	case duplicateRoute:
		edge.Confidence = 0.50
		edge.Conflicts = append(edge.Conflicts, "target_unresolved", "route_duplicated_across_services")
	default:
		edge.Confidence = 0.70
		edge.Conflicts = append(edge.Conflicts, "target_unresolved")
	}
}

func scoreSimilarRoute(edge *CandidateEdge, evidence string) {
	edge.Confidence = 0.35
	if evidence != "" {
		edge.Confidence = 0.45
		edge.Reasons = append(edge.Reasons, evidence)
	} else {
		edge.Conflicts = append(edge.Conflicts, "target_unresolved")
	}
	edge.Conflicts = append(edge.Conflicts, "path_transform_unproven")
}

func candidateEdge(outbound, inbound Endpoint) CandidateEdge {
	protocol := normalizedProtocol(outbound.Protocol)
	edge := CandidateEdge{
		FromEndpoint: outbound.ID,
		ToEndpoint:   inbound.ID,
		FromService:  outbound.Service,
		ToService:    inbound.Service,
		Protocol:     protocol,
	}
	if protocol == "http" {
		edge.Method = preferredMethod(outbound.Method, inbound.Method)
		edge.Path = NormalizePath(inbound.Path)
	} else if protocol == "grpc" {
		edge.RPCMethod = normalizeRPCMethod(inbound.RPCMethod)
	}
	return edge
}

func preferredMethod(outbound, inbound string) string {
	outbound = NormalizeHTTPMethod(outbound)
	inbound = NormalizeHTTPMethod(inbound)
	if outbound != "ANY" {
		return outbound
	}
	return inbound
}

func exactRouteReason(protocol string) string {
	if protocol == "grpc" {
		return "rpc_method_exact"
	}
	return "method_path_exact"
}

func pathsMatchThroughTransforms(left, right Endpoint) bool {
	leftPath := NormalizePath(left.Path)
	rightPath := NormalizePath(right.Path)
	if transformed, ok := applyTransformChain(leftPath, left.Transforms); ok && transformed == rightPath {
		return true
	}
	if transformed, ok := applyTransformChain(rightPath, right.Transforms); ok && transformed == leftPath {
		return true
	}
	leftTransformed, leftOK := applyTransformChain(leftPath, left.Transforms)
	rightTransformed, rightOK := applyTransformChain(rightPath, right.Transforms)
	return leftOK && rightOK && leftTransformed == rightTransformed
}

func applyTransformChain(path string, transforms []Transform) (string, bool) {
	if len(transforms) == 0 {
		return "", false
	}
	current := NormalizePath(path)
	for _, transform := range transforms {
		from := NormalizePath(transform.From)
		to := NormalizePath(transform.To)
		if strings.TrimSpace(transform.Kind) == "" || from == "" || to == "" || current != from {
			return "", false
		}
		current = to
	}
	return current, true
}

func pathsHaveSuffixSimilarity(left, right string) bool {
	left = NormalizePath(left)
	right = NormalizePath(right)
	if left == "" || right == "" || left == "/" || right == "/" || left == right {
		return false
	}
	return strings.HasSuffix(left, right) && pathSuffixBoundary(left, right) ||
		strings.HasSuffix(right, left) && pathSuffixBoundary(right, left)
}

func pathSuffixBoundary(longer, suffix string) bool {
	start := len(longer) - len(suffix)
	return start == 0 || suffix[0] == '/' || longer[start-1] == '/'
}

func targetEvidence(hint string, endpoint Endpoint, descriptor ServiceDescriptor) string {
	target := normalizeTarget(hint)
	if target == "" {
		return ""
	}
	for _, host := range descriptor.Hosts {
		if target == normalizeTarget(host) {
			return "target_host_exact"
		}
	}
	aliases := append([]string{endpoint.Service, descriptor.Service}, descriptor.Aliases...)
	for _, alias := range aliases {
		if target == normalizeTarget(alias) {
			return "target_alias_exact"
		}
	}
	return ""
}

func normalizeTarget(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host
	} else if strings.HasPrefix(value, "//") {
		if parsed, err := url.Parse("http:" + value); err == nil {
			value = parsed.Host
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(value, ".")
	return value
}

func clampConfidence(confidence float64) float64 {
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func statusForConfidence(confidence float64) string {
	if confidence >= 0.85 {
		return "automatic"
	}
	return "candidate"
}

func sortCandidateEdges(edges []CandidateEdge) {
	sort.Slice(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		leftKey := []string{left.FromService, left.ToService, left.Protocol, left.Method, left.Path, left.RPCMethod, left.FromEndpoint, left.ToEndpoint, left.Status, strings.Join(left.Reasons, "\x00"), strings.Join(left.Conflicts, "\x00")}
		rightKey := []string{right.FromService, right.ToService, right.Protocol, right.Method, right.Path, right.RPCMethod, right.FromEndpoint, right.ToEndpoint, right.Status, strings.Join(right.Reasons, "\x00"), strings.Join(right.Conflicts, "\x00")}
		for index := range leftKey {
			if leftKey[index] != rightKey[index] {
				return leftKey[index] < rightKey[index]
			}
		}
		return left.Confidence > right.Confidence
	})
}
