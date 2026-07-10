package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	goRouteRE             = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goHandleRE            = regexp.MustCompile(`\b(?:http|[A-Za-z_][A-Za-z0-9_]*)\.(HandleFunc|Handle)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goHTTPCallRE          = regexp.MustCompile(`\bhttp\.(Get|Post|Head)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goNewRequestRE        = regexp.MustCompile(`\bhttp\.NewRequest\s*\(\s*["\x60](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goNewRequestContextRE = regexp.MustCompile(`\bhttp\.NewRequestWithContext\s*\(\s*[^,\r\n]+,\s*["\x60](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goKratosInvokeRE      = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.Invoke\s*\(\s*[^,\r\n]+,\s*["\x60](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goGRPCInvokeRE        = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.Invoke\s*\(\s*[^,\r\n]+,\s*["\x60](/[^"\x60\s]+/[^"\x60\s]+)["\x60]`)

	protoPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`)
	protoServiceRE = regexp.MustCompile(`\bservice\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	protoRPCRE     = regexp.MustCompile(`\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

func scanGoEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".go", ".proto":
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
		var extracted []topology.Endpoint
		if strings.EqualFold(filepath.Ext(source.rel), ".proto") {
			extracted, err = extractProtoEndpointsContext(ctx, source)
		} else {
			extracted, err = extractGoEndpointsContext(ctx, opts, source)
		}
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extracted...)
	}
	return endpoints, ctx.Err()
}

func extractGoEndpointsContext(ctx context.Context, opts EndpointScanOptions, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, loc := range goRouteRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receiver := source.text[loc[2]:loc[3]]
		method := source.text[loc[4]:loc[5]]
		if strings.EqualFold(method, "any") {
			method = "ANY"
		}
		path := source.text[loc[6]:loc[7]]
		sourceName := "gin-route"
		switch {
		case strings.EqualFold(strings.TrimSpace(opts.Framework), "kratos"):
			sourceName = "kratos-route"
		case receiver == "e":
			sourceName = "echo-route"
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), sourceName))
	}

	for _, loc := range goHandleRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		method, path := splitGoServeMuxPattern(source.text[loc[4]:loc[5]])
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "go-net-http"))
	}

	for _, pattern := range []struct {
		re     *regexp.Regexp
		source string
	}{
		{re: goHTTPCallRE, source: "go-http"},
		{re: goNewRequestRE, source: "go-http"},
		{re: goNewRequestContextRE, source: "go-http"},
		{re: goKratosInvokeRE, source: "kratos-http"},
	} {
		for _, loc := range pattern.re.FindAllStringSubmatchIndex(source.text, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			method := source.text[loc[2]:loc[3]]
			rawURL := source.text[loc[4]:loc[5]]
			path, hint := splitHTTPURL(rawURL)
			if path == "" {
				continue
			}
			endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), pattern.source))
		}
	}

	for _, loc := range goGRPCInvokeRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rpcMethod := strings.TrimPrefix(source.text[loc[2]:loc[3]], "/")
		hint := rpcMethod
		if slash := strings.LastIndex(rpcMethod, "/"); slash >= 0 {
			hint = rpcMethod[:slash]
		}
		endpoints = append(endpoints, grpcEndpoint(topology.DirectionOutbound, rpcMethod, hint, endpointLocation(source, loc[0]), "grpc-client"))
	}
	return endpoints, ctx.Err()
}

func splitGoServeMuxPattern(pattern string) (method, path string) {
	fields := strings.Fields(strings.TrimSpace(pattern))
	if len(fields) == 2 && isHTTPMethod(fields[0]) {
		return strings.ToUpper(fields[0]), fields[1]
	}
	return "ANY", pattern
}

func isHTTPMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "ANY":
		return true
	default:
		return false
	}
}

func extractProtoEndpointsContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	packageName := ""
	if match := protoPackageRE.FindStringSubmatch(source.text); len(match) == 2 {
		packageName = match[1]
	}

	var endpoints []topology.Endpoint
	for _, service := range protoServiceRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		serviceName := source.text[service[2]:service[3]]
		qualifiedService := serviceName
		if packageName != "" {
			qualifiedService = packageName + "." + serviceName
		}
		openOffset := service[1] - 1
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '{', '}', true, false)
		if !ok {
			continue
		}
		bodyStart := openOffset + 1
		body := source.text[bodyStart:closeOffset]
		for _, rpc := range protoRPCRE.FindAllStringSubmatchIndex(body, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			method := body[rpc[2]:rpc[3]]
			endpoints = append(endpoints, grpcEndpoint(topology.DirectionInbound, qualifiedService+"/"+method, "", endpointLocation(source, bodyStart+rpc[0]), "proto"))
		}
	}
	return endpoints, ctx.Err()
}
