package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	nodeFetchRE        = regexp.MustCompile(`(?i)\bfetch\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeFetchOptionsRE = regexp.MustCompile(`(?is)^\s*,\s*\{([^{}]{0,1000})\}`)
	nodeAxiosVerbRE    = regexp.MustCompile(`(?i)\baxios\.(get|post|put|patch|delete)\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeAxiosConfigRE  = regexp.MustCompile(`(?is)\baxios(?:\.request)?\s*\(\s*\{([^{}]{0,1000})\}\s*\)`)
	nodeExpressRE      = regexp.MustCompile(`(?i)\b(?:app|router)\.(get|post|put|patch|delete|all)\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeControllerRE   = regexp.MustCompile(`(?i)@Controller\s*\(\s*["'\x60]([^"'\x60]*)["'\x60]\s*\)`)
	nodeNestRouteRE    = regexp.MustCompile(`(?i)@(Get|Post|Put|Patch|Delete|All)\s*\(\s*(?:["'\x60]([^"'\x60]*)["'\x60])?\s*\)`)
	nodeObjectRE       = regexp.MustCompile(`(?s)\{([^{}]{0,1000})\}`)
)

func scanNodeEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue":
			return true
		default:
			return false
		}
	})
	if err != nil {
		return nil, err
	}

	var endpoints []topology.Endpoint
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extractNodeCalls(source)...)
		endpoints = append(endpoints, extractNodeRoutes(source)...)
		if strings.HasPrefix(strings.ToLower(filepath.Base(source.rel)), "next.config.") || strings.Contains(strings.ToLower(opts.Framework), "next") {
			endpoints = append(endpoints, extractNextRewrites(source)...)
		}
	}
	return endpoints, nil
}

func extractNodeCalls(source endpointSource) []topology.Endpoint {
	var endpoints []topology.Endpoint
	for _, loc := range nodeFetchRE.FindAllStringSubmatchIndex(source.text, -1) {
		raw := source.text[loc[2]:loc[3]]
		path, hint := splitHTTPURL(raw)
		if path == "" {
			continue
		}
		method := "GET"
		if options := nodeFetchOptionsRE.FindStringSubmatch(source.text[loc[1]:]); len(options) == 2 {
			if configured := jsStringProperty(options[1], "method"); configured != "" {
				method = configured
			}
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "fetch"))
	}
	for _, loc := range nodeAxiosVerbRE.FindAllStringSubmatchIndex(source.text, -1) {
		method := source.text[loc[2]:loc[3]]
		raw := source.text[loc[4]:loc[5]]
		path, hint := splitHTTPURL(raw)
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "axios"))
	}
	for _, loc := range nodeAxiosConfigRE.FindAllStringSubmatchIndex(source.text, -1) {
		body := source.text[loc[2]:loc[3]]
		method := jsStringProperty(body, "method")
		raw := jsStringProperty(body, "url")
		path, hint := splitHTTPURL(raw)
		if method == "" || path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "axios"))
	}
	return endpoints
}

func extractNodeRoutes(source endpointSource) []topology.Endpoint {
	var endpoints []topology.Endpoint
	for _, loc := range nodeExpressRE.FindAllStringSubmatchIndex(source.text, -1) {
		method := source.text[loc[2]:loc[3]]
		if strings.EqualFold(method, "all") {
			method = "ANY"
		}
		path := source.text[loc[4]:loc[5]]
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "express-route"))
	}

	prefix := ""
	if match := nodeControllerRE.FindStringSubmatch(source.text); len(match) == 2 {
		prefix = match[1]
	}
	for _, loc := range nodeNestRouteRE.FindAllStringSubmatchIndex(source.text, -1) {
		method := source.text[loc[2]:loc[3]]
		if strings.EqualFold(method, "all") {
			method = "ANY"
		}
		path := ""
		if loc[4] >= 0 && loc[5] >= 0 {
			path = source.text[loc[4]:loc[5]]
		}
		path = joinEndpointPaths(prefix, path)
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "nest-route"))
	}
	return endpoints
}

func extractNextRewrites(source endpointSource) []topology.Endpoint {
	var endpoints []topology.Endpoint
	for _, loc := range nodeObjectRE.FindAllStringSubmatchIndex(source.text, -1) {
		body := source.text[loc[2]:loc[3]]
		from := jsStringProperty(body, "source")
		destination := jsStringProperty(body, "destination")
		to, hint := splitHTTPURL(destination)
		if from == "" || to == "" {
			continue
		}
		endpoint := httpEndpoint(topology.DirectionInbound, "ANY", from, hint, endpointLocation(source, loc[0]), "next-rewrite")
		endpoint.Transforms = []topology.Transform{{Kind: "rewrite", From: from, To: to}}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}

func jsStringProperty(body, property string) string {
	re := regexp.MustCompile(`(?i)(?:^|[,\s])` + regexp.QuoteMeta(property) + `\s*:\s*["'\x60]([^"'\x60]+)["'\x60]`)
	match := re.FindStringSubmatch(body)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func httpEndpoint(direction topology.Direction, method, path, hint, location, source string) topology.Endpoint {
	return topology.Endpoint{
		Direction:  direction,
		Protocol:   "http",
		Method:     method,
		Path:       path,
		TargetHint: hint,
		Location:   location,
		Source:     source,
	}
}

func joinEndpointPaths(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
}
