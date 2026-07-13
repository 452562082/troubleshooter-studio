package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	goRouteRE             = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goGroupAssignmentRE   = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*([A-Za-z_][A-Za-z0-9_]*)\.Group\s*\(\s*["\x60]([^"\x60]+)["\x60]\s*\)`)
	goChainedGroupRouteRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.Group\s*\(\s*["\x60]([^"\x60]+)["\x60]\s*\)\s*\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goHandleRE            = regexp.MustCompile(`\b(?:http|[A-Za-z_][A-Za-z0-9_]*)\.(HandleFunc|Handle)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goHTTPCallRE          = regexp.MustCompile(`\bhttp\.(Get|Post|Head)\s*\(\s*["\x60]([^"\x60]+)["\x60]`)
	goNewRequestRE        = regexp.MustCompile(`\bhttp\.NewRequest\s*\(\s*((?:["\x60](?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]|http\.Method(?:Get|Post|Put|Patch|Delete|Head|Options)))\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goNewRequestContextRE = regexp.MustCompile(`\bhttp\.NewRequestWithContext\s*\(\s*[^,\r\n]+,\s*((?:["\x60](?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]|http\.Method(?:Get|Post|Put|Patch|Delete|Head|Options)))\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goKratosInvokeRE      = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.Invoke\s*\(\s*[^,\r\n]+,\s*["\x60](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["\x60]\s*,\s*["\x60]([^"\x60]+)["\x60]`)
	goGRPCInvokeRE        = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.Invoke\s*\(\s*[^,\r\n]+,\s*["\x60](/[^"\x60\s]+/[^"\x60\s]+)["\x60]`)

	protoPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`)
	protoServiceRE = regexp.MustCompile(`\bservice\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	protoRPCRE     = regexp.MustCompile(`\brpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

type goGroupAssignment struct {
	variable string
	root     string
	prefix   string
	offset   int
	scope    []int
}

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
			source.text = maskCStyleComments(source.text)
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
	routes := goRouteRE.FindAllStringSubmatchIndex(source.text, -1)
	groupLocs := goGroupAssignmentRE.FindAllStringSubmatchIndex(source.text, -1)
	offsets := make([]int, 0, len(routes)+len(groupLocs))
	for _, loc := range routes {
		offsets = append(offsets, loc[0])
	}
	for _, loc := range groupLocs {
		offsets = append(offsets, loc[0])
	}
	scopes := goScopesAtOffsets(source.text, offsets)
	groups := goGroupAssignments(source.text, groupLocs, scopes)

	for _, loc := range routes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receiver := source.text[loc[2]:loc[3]]
		method := source.text[loc[4]:loc[5]]
		if strings.EqualFold(method, "any") {
			method = "ANY"
		}
		path := source.text[loc[6]:loc[7]]
		if group, ok := visibleGoGroup(groups, receiver, loc[0], scopes[loc[0]]); ok {
			path = joinEndpointPaths(group.prefix, path)
			receiver = group.root
		}
		sourceName := goRouteSource(opts, receiver)
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), sourceName))
	}

	for _, loc := range goChainedGroupRouteRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receiver := source.text[loc[2]:loc[3]]
		prefix := source.text[loc[4]:loc[5]]
		method := source.text[loc[6]:loc[7]]
		if strings.EqualFold(method, "any") {
			method = "ANY"
		}
		path := joinEndpointPaths(prefix, source.text[loc[8]:loc[9]])
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), goRouteSource(opts, receiver)))
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
			method := staticGoHTTPMethod(source.text[loc[2]:loc[3]])
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

func goRouteSource(opts EndpointScanOptions, receiver string) string {
	if strings.EqualFold(strings.TrimSpace(opts.Framework), "kratos") {
		return "kratos-route"
	}
	if receiver == "e" {
		return "echo-route"
	}
	return "gin-route"
}

func goGroupAssignments(text string, locations [][]int, scopes map[int][]int) []goGroupAssignment {
	groups := make([]goGroupAssignment, 0, len(locations))
	for _, loc := range locations {
		group := goGroupAssignment{
			variable: text[loc[2]:loc[3]],
			root:     text[loc[4]:loc[5]],
			prefix:   text[loc[6]:loc[7]],
			offset:   loc[0],
			scope:    scopes[loc[0]],
		}
		if parent, ok := visibleGoGroup(groups, group.root, group.offset, group.scope); ok {
			group.root = parent.root
			group.prefix = joinEndpointPaths(parent.prefix, group.prefix)
		}
		groups = append(groups, group)
	}
	return groups
}

func visibleGoGroup(groups []goGroupAssignment, variable string, offset int, scope []int) (goGroupAssignment, bool) {
	best := -1
	for i := range groups {
		group := groups[i]
		if group.variable != variable || group.offset >= offset || !goScopeContains(group.scope, scope) {
			continue
		}
		if best < 0 || len(group.scope) > len(groups[best].scope) || len(group.scope) == len(groups[best].scope) && group.offset > groups[best].offset {
			best = i
		}
	}
	if best < 0 {
		return goGroupAssignment{}, false
	}
	return groups[best], true
}

func goScopeContains(declaration, use []int) bool {
	if len(declaration) > len(use) {
		return false
	}
	for i := range declaration {
		if declaration[i] != use[i] {
			return false
		}
	}
	return true
}

func goScopesAtOffsets(text string, offsets []int) map[int][]int {
	sort.Ints(offsets)
	scopes := make(map[int][]int, len(offsets))
	var stack []int
	var quote byte
	escaped := false
	next := 0
	for i := 0; i <= len(text); i++ {
		for next < len(offsets) && offsets[next] == i {
			scopes[i] = append([]int(nil), stack...)
			next++
		}
		if i == len(text) {
			break
		}
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
				continue
			}
			if quote != '`' && text[i] == '\\' {
				escaped = true
				continue
			}
			if text[i] == quote {
				quote = 0
			}
			continue
		}
		if text[i] == '\'' || text[i] == '"' || text[i] == '`' {
			quote = text[i]
			continue
		}
		switch text[i] {
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return scopes
}

func staticGoHTTPMethod(token string) string {
	token = strings.Trim(strings.TrimSpace(token), "\"`")
	token = strings.TrimPrefix(token, "http.Method")
	return strings.ToUpper(token)
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
	source.text = maskCStyleComments(source.text)
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
