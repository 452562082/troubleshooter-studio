package topology

import (
	"sort"
	"strings"
)

// Override is the topology-local adapter contract for a persisted human
// decision. Callers convert their configuration type into this package-owned
// type so topology does not depend on config.
type Override struct {
	Action      string `json:"action" yaml:"action"`
	FromService string `json:"from_service" yaml:"from_service"`
	ToService   string `json:"to_service" yaml:"to_service"`
	Protocol    string `json:"protocol" yaml:"protocol"`
	Method      string `json:"method,omitempty" yaml:"method,omitempty"`
	Path        string `json:"path,omitempty" yaml:"path,omitempty"`
	RPCMethod   string `json:"rpc_method,omitempty" yaml:"rpc_method,omitempty"`
}

// Merge applies persisted human decisions to the current matcher evidence.
// A reject always wins. Adds and confirmations override matcher statuses, and
// a confirmation with no current evidence is retained as stale.
func Merge(edges []CandidateEdge, overrides []Override) []CandidateEdge {
	decisions := mergeDecisions(overrides)
	matched := make(map[string]bool, len(decisions))
	result := make([]CandidateEdge, 0, len(edges)+len(decisions))

	for _, current := range edges {
		candidate := cloneCandidateEdge(current)
		key := candidateSemanticKey(candidate)
		decision, ok := decisions[key]
		if ok {
			matched[key] = true
			switch decision.Action {
			case "reject":
				candidate.Status = "rejected"
				candidate.Reasons = appendUniqueString(candidate.Reasons, "human_override_reject")
			case "add":
				candidate.Status = "manual"
				candidate.Reasons = appendUniqueString(candidate.Reasons, "human_override_add")
			case "confirm":
				candidate.Status = "confirmed"
				candidate.Reasons = appendUniqueString(candidate.Reasons, "human_override_confirm")
			}
		}
		result = append(result, candidate)
	}

	keys := make([]string, 0, len(decisions))
	for key := range decisions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if matched[key] {
			continue
		}
		decision := decisions[key]
		switch decision.Action {
		case "add":
			result = append(result, candidateFromOverride(decision, "manual", 1, "human_override_add"))
		case "confirm":
			result = append(result, candidateFromOverride(decision, "stale", 0, "human_override_confirm_stale"))
		}
	}

	sortCandidateEdges(result)
	return result
}

func mergeDecisions(overrides []Override) map[string]Override {
	result := make(map[string]Override, len(overrides))
	for _, raw := range overrides {
		override := normalizeOverride(raw)
		if overridePriority(override.Action) == 0 {
			continue
		}
		key := overrideSemanticKey(override)
		current, exists := result[key]
		if !exists || overridePriority(override.Action) > overridePriority(current.Action) ||
			overridePriority(override.Action) == overridePriority(current.Action) && override.Action < current.Action {
			result[key] = override
		}
	}
	return result
}

func overridePriority(action string) int {
	switch action {
	case "reject":
		return 3
	case "add", "confirm":
		return 2
	default:
		return 0
	}
}

func normalizeOverride(override Override) Override {
	override.Action = strings.ToLower(strings.TrimSpace(override.Action))
	override.FromService = strings.TrimSpace(override.FromService)
	override.ToService = strings.TrimSpace(override.ToService)
	override.Protocol = normalizedProtocol(override.Protocol)
	if override.Protocol == "http" {
		override.Method = NormalizeHTTPMethod(override.Method)
		override.Path = NormalizePath(override.Path)
		override.RPCMethod = ""
	} else if override.Protocol == "grpc" {
		override.Method = ""
		override.Path = ""
		override.RPCMethod = normalizeRPCMethod(override.RPCMethod)
	}
	return override
}

func overrideSemanticKey(override Override) string {
	return semanticEdgeKey(
		override.FromService,
		override.ToService,
		override.Protocol,
		override.Method,
		override.Path,
		override.RPCMethod,
	)
}

func candidateSemanticKey(candidate CandidateEdge) string {
	return semanticEdgeKey(
		candidate.FromService,
		candidate.ToService,
		candidate.Protocol,
		candidate.Method,
		candidate.Path,
		candidate.RPCMethod,
	)
}

func semanticEdgeKey(from, to, protocol, method, path, rpcMethod string) string {
	protocol = normalizedProtocol(protocol)
	method = NormalizeHTTPMethod(method)
	path = NormalizePath(path)
	rpcMethod = normalizeRPCMethod(rpcMethod)
	if protocol == "http" {
		rpcMethod = ""
	} else if protocol == "grpc" {
		method = ""
		path = ""
	}
	return strings.Join([]string{
		strings.TrimSpace(from), strings.TrimSpace(to), protocol, method, path, rpcMethod,
	}, "\x1f")
}

func candidateFromOverride(override Override, status string, confidence float64, reason string) CandidateEdge {
	return CandidateEdge{
		FromService: override.FromService,
		ToService:   override.ToService,
		Protocol:    override.Protocol,
		Method:      override.Method,
		Path:        override.Path,
		RPCMethod:   override.RPCMethod,
		Confidence:  confidence,
		Status:      status,
		Reasons:     []string{reason},
	}
}

func cloneCandidateEdge(candidate CandidateEdge) CandidateEdge {
	candidate.Reasons = append([]string(nil), candidate.Reasons...)
	candidate.Conflicts = append([]string(nil), candidate.Conflicts...)
	return candidate
}

func appendUniqueString(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
