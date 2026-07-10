package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	javaFeignRE           = regexp.MustCompile(`@FeignClient\s*\(([^)]*)\)`)
	javaFeignTargetRE     = regexp.MustCompile(`\b(name|value|url)\s*=\s*["']([^"']+)["']`)
	javaInterfaceRE       = regexp.MustCompile(`(?s)\binterface\s+[A-Za-z_][A-Za-z0-9_]*[^\{]*\{`)
	javaGatewayRouteRE    = regexp.MustCompile(`\.route\s*\(`)
	javaGatewayPathRE     = regexp.MustCompile(`\.path\s*\(\s*["']([^"']+)["']`)
	javaGatewayURI_RE     = regexp.MustCompile(`\.uri\s*\(\s*["']([^"']+)["']`)
	javaGatewayStripRE    = regexp.MustCompile(`\.stripPrefix\s*\(\s*(\d+)\s*\)`)
	javaGatewayRewriteRE  = regexp.MustCompile(`\.rewritePath\s*\(\s*["']([^"']+)["']\s*,\s*["']([^"']+)["']`)
	gatewayYAMLRouteRE    = regexp.MustCompile(`(?i)^\s*-\s*id\s*:`)
	gatewayYAMLURI_RE     = regexp.MustCompile(`(?i)^uri\s*:\s*["']?([^"'\s]+)`)
	gatewayYAMLPathRE     = regexp.MustCompile(`(?i)^-\s*Path\s*[=:]\s*["']?([^"']+)`)
	gatewayYAMLStripRE    = regexp.MustCompile(`(?i)^-\s*StripPrefix\s*[=:]\s*(\d+)`)
	gatewayYAMLRewriteRE  = regexp.MustCompile(`(?i)^-\s*RewritePath\s*[=:]\s*(.+)$`)
	gatewayNamedCaptureRE = regexp.MustCompile(`\(\?<[^>]+>\.\*\)|\(\.\*\)|\(\.\+\)`)
	gatewayVariableRE     = regexp.MustCompile(`\$\{[^}]+\}`)
	gatewayWildcardRE     = regexp.MustCompile(`/\*\*?`)
)

type javaSourceSpan struct {
	start int
	end   int
}

type javaPrefixSpan struct {
	javaSourceSpan
	path string
}

func scanJavaEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".java", ".kt", ".proto", ".yaml", ".yml":
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
		switch strings.ToLower(filepath.Ext(source.rel)) {
		case ".proto":
			extracted, err = extractProtoEndpointsContext(ctx, source)
		case ".yaml", ".yml":
			extracted, err = extractSpringGatewayYAMLContext(ctx, source)
		default:
			source.text = maskCStyleComments(source.text)
			extracted, err = extractJavaEndpointsContext(ctx, source)
		}
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extracted...)
	}
	return endpoints, ctx.Err()
}

func extractJavaEndpointsContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	feignEndpoints, feignSpans, err := extractFeignEndpointsContext(ctx, source)
	if err != nil {
		return nil, err
	}
	endpoints := append([]topology.Endpoint(nil), feignEndpoints...)

	mappings := springMappingRE.FindAllStringSubmatchIndex(source.text, -1)
	var classPrefixes []javaPrefixSpan
	classMappings := make(map[int]struct{})
	for _, loc := range mappings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if insideJavaSpan(loc[0], feignSpans) || source.text[loc[2]:loc[3]] != "RequestMapping" || !springMappingTargetsClass(source.text, loc[1]) {
			continue
		}
		args := javaMappingArgs(source.text, loc)
		path, ok := springMappingPath(args)
		if !ok {
			continue
		}
		openRelative := strings.IndexByte(source.text[loc[1]:], '{')
		if openRelative < 0 {
			continue
		}
		openOffset := loc[1] + openRelative
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '{', '}', true, false)
		if !ok {
			continue
		}
		classMappings[loc[0]] = struct{}{}
		classPrefixes = append(classPrefixes, javaPrefixSpan{javaSourceSpan: javaSourceSpan{start: openOffset + 1, end: closeOffset}, path: path})
	}

	for _, loc := range mappings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if insideJavaSpan(loc[0], feignSpans) {
			continue
		}
		if _, classMapping := classMappings[loc[0]]; classMapping {
			continue
		}
		args := javaMappingArgs(source.text, loc)
		path, ok := springMappingPath(args)
		if !ok {
			continue
		}
		for _, prefix := range classPrefixes {
			if loc[0] >= prefix.start && loc[0] < prefix.end {
				path = joinEndpointPaths(prefix.path, path)
				break
			}
		}
		annotation := source.text[loc[2]:loc[3]]
		method := methodFromSpringMapping(annotation, source.text[loc[0]:loc[1]])
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "spring-route"))
	}

	gatewayEndpoints, err := extractSpringGatewayJavaContext(ctx, source)
	if err != nil {
		return nil, err
	}
	endpoints = append(endpoints, gatewayEndpoints...)
	return endpoints, ctx.Err()
}

func extractFeignEndpointsContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, []javaSourceSpan, error) {
	var endpoints []topology.Endpoint
	var spans []javaSourceSpan
	for _, feign := range javaFeignRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		header := source.text[feign[1]:]
		interfaceLoc := javaInterfaceRE.FindStringIndex(header)
		if interfaceLoc == nil || interfaceLoc[0] > 2000 {
			continue
		}
		openOffset := feign[1] + interfaceLoc[1] - 1
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '{', '}', true, false)
		if !ok {
			continue
		}
		headerStart := javaDeclarationHeaderStart(source.text, feign[0])
		span := javaSourceSpan{start: headerStart, end: closeOffset + 1}
		spans = append(spans, span)
		target := javaFeignTarget(source.text[feign[2]:feign[3]])
		prefix := ""
		declarationHeader := source.text[headerStart:openOffset]
		for _, mapping := range springMappingRE.FindAllStringSubmatchIndex(declarationHeader, -1) {
			if declarationHeader[mapping[2]:mapping[3]] != "RequestMapping" {
				continue
			}
			args := javaMappingArgs(declarationHeader, mapping)
			if path, found := springMappingPath(args); found {
				prefix = path
			}
		}

		bodyStart := openOffset + 1
		body := source.text[bodyStart:closeOffset]
		for _, mapping := range springMappingRE.FindAllStringSubmatchIndex(body, -1) {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			args := javaMappingArgs(body, mapping)
			path, found := springMappingPath(args)
			if !found {
				continue
			}
			path = joinEndpointPaths(prefix, path)
			annotation := body[mapping[2]:mapping[3]]
			method := methodFromSpringMapping(annotation, body[mapping[0]:mapping[1]])
			endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, target, endpointLocation(source, bodyStart+mapping[0]), "feign"))
		}
	}
	return endpoints, spans, ctx.Err()
}

func javaDeclarationHeaderStart(text string, annotationStart int) int {
	if annotationStart <= 0 {
		return 0
	}
	if boundary := strings.LastIndexAny(text[:annotationStart], ";{}"); boundary >= 0 {
		return boundary + 1
	}
	return 0
}

func javaFeignTarget(args string) string {
	values := make(map[string]string)
	for _, match := range javaFeignTargetRE.FindAllStringSubmatch(args, -1) {
		values[match[1]] = match[2]
	}
	for _, key := range []string{"name", "value", "url"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			if key == "url" {
				_, hint := splitHTTPURL(value)
				if hint != "" {
					return hint
				}
			}
			return value
		}
	}
	return ""
}

func javaMappingArgs(text string, loc []int) string {
	if len(loc) >= 6 && loc[4] >= 0 && loc[5] >= 0 {
		return text[loc[4]:loc[5]]
	}
	return ""
}

func insideJavaSpan(offset int, spans []javaSourceSpan) bool {
	for _, span := range spans {
		if offset >= span.start && offset < span.end {
			return true
		}
	}
	return false
}

func extractSpringGatewayJavaContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, route := range javaGatewayRouteRE.FindAllStringIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		openOffset := route[1] - 1
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '(', ')', true, false)
		if !ok {
			continue
		}
		body := source.text[openOffset+1 : closeOffset]
		uri := ""
		if match := javaGatewayURI_RE.FindStringSubmatch(body); len(match) == 2 {
			uri = match[1]
		}
		_, hint := splitHTTPURL(uri)
		for _, match := range javaGatewayPathRE.FindAllStringSubmatch(body, -1) {
			path := gatewayEndpointPath(match[1])
			endpoint := httpEndpoint(topology.DirectionInbound, "ANY", path, hint, endpointLocation(source, route[0]), "spring-gateway")
			if strip := javaGatewayStripRE.FindStringSubmatch(body); len(strip) == 2 {
				count, _ := strconv.Atoi(strip[1])
				endpoint.Transforms = append(endpoint.Transforms, topology.Transform{Kind: "strip_prefix", From: path, To: stripGatewayPrefix(path, count)})
			}
			if rewrite := javaGatewayRewriteRE.FindStringSubmatch(body); len(rewrite) == 3 {
				endpoint.Transforms = append(endpoint.Transforms, topology.Transform{Kind: "rewrite", From: gatewayEndpointPath(rewrite[1]), To: gatewayEndpointPath(rewrite[2])})
			}
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, ctx.Err()
}

func extractSpringGatewayYAMLContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	type route struct {
		offset      int
		uri         string
		paths       []string
		strip       int
		rewriteFrom string
		rewriteTo   string
	}

	var routes []route
	var current *route
	offset := 0
	for _, line := range strings.SplitAfter(source.text, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if gatewayYAMLRouteRE.MatchString(line) {
			routes = append(routes, route{offset: offset, strip: -1})
			current = &routes[len(routes)-1]
			offset += len(line)
			continue
		}
		if current != nil {
			if match := gatewayYAMLURI_RE.FindStringSubmatch(trimmed); len(match) == 2 {
				current.uri = match[1]
			}
			if match := gatewayYAMLPathRE.FindStringSubmatch(trimmed); len(match) == 2 {
				current.paths = append(current.paths, strings.TrimSpace(match[1]))
			}
			if match := gatewayYAMLStripRE.FindStringSubmatch(trimmed); len(match) == 2 {
				current.strip, _ = strconv.Atoi(match[1])
			}
			if match := gatewayYAMLRewriteRE.FindStringSubmatch(trimmed); len(match) == 2 {
				parts := strings.SplitN(match[1], ",", 2)
				if len(parts) == 2 {
					current.rewriteFrom = strings.TrimSpace(parts[0])
					current.rewriteTo = strings.TrimSpace(parts[1])
				}
			}
		}
		offset += len(line)
	}

	var endpoints []topology.Endpoint
	for _, route := range routes {
		_, hint := splitHTTPURL(route.uri)
		for _, rawPath := range route.paths {
			path := gatewayEndpointPath(rawPath)
			endpoint := httpEndpoint(topology.DirectionInbound, "ANY", path, hint, endpointLocation(source, route.offset), "spring-gateway")
			if route.strip >= 0 {
				endpoint.Transforms = append(endpoint.Transforms, topology.Transform{Kind: "strip_prefix", From: path, To: stripGatewayPrefix(path, route.strip)})
			}
			if route.rewriteFrom != "" && route.rewriteTo != "" {
				endpoint.Transforms = append(endpoint.Transforms, topology.Transform{Kind: "rewrite", From: gatewayEndpointPath(route.rewriteFrom), To: gatewayEndpointPath(route.rewriteTo)})
			}
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, ctx.Err()
}

func gatewayEndpointPath(path string) string {
	path = strings.TrimSpace(strings.Trim(path, `"'`))
	path = strings.TrimPrefix(path, "^")
	path = strings.TrimSuffix(path, "$")
	path = gatewayWildcardRE.ReplaceAllString(path, "/*wildcard")
	path = gatewayNamedCaptureRE.ReplaceAllString(path, "*wildcard")
	path = gatewayVariableRE.ReplaceAllString(path, "*wildcard")
	return ensureLeadingSlash(path)
}

func stripGatewayPrefix(path string, count int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if count >= len(parts) {
		return "/"
	}
	return ensureLeadingSlash(strings.Join(parts[count:], "/"))
}
