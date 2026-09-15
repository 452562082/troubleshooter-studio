package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	nodeFetchRE             = regexp.MustCompile(`(?i)\bfetch\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeFetchOptionsRE      = regexp.MustCompile(`(?is)^\s*,\s*\{([^{}]{0,1000})\}`)
	nodeAxiosVerbRE         = regexp.MustCompile(`(?i)\baxios\.(get|post|put|patch|delete)\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeAxiosConfigRE       = regexp.MustCompile(`(?is)\baxios(?:\.request)?\s*\(\s*\{([^{}]{0,1000})\}\s*\)`)
	nodeClientVerbRE        = regexp.MustCompile(`(?i)\b(?:[A-Za-z_$][\w$]*\.)*(?:httpClient|apiClient|client)\.(get|post|put|patch|delete)\s*(?:<[^;\r\n()]*>)?\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeObjectClientCallRE  = regexp.MustCompile(`(?i)\b((?:[A-Za-z_$][\w$]*\.)*(?:request|apiFetch|ugcFetch))\s*(?:<[^;\r\n()]*>)?\s*\(\s*\{`)
	nodePathClientCallRE    = regexp.MustCompile(`(?i)\b((?:[A-Za-z_$][\w$]*\.)*(?:request|apiFetch|ugcFetch))\s*(?:<[^;\r\n()]*>)?\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeExpressRE           = regexp.MustCompile(`(?i)\b(?:app|router)\.(get|post|put|patch|delete|all)\s*\(\s*["'\x60]([^"'\x60]+)["'\x60]`)
	nodeControllerRE        = regexp.MustCompile(`(?is)@Controller\s*\(\s*(?:["'\x60]([^"'\x60]*)["'\x60])?\s*\)\s*(?:@[A-Za-z_$][\w$]*(?:\s*\([^\r\n]*\))?\s*)*(?:export\s+)?class\s+[A-Za-z_$][\w$]*[^\{]*\{`)
	nodeNestRouteRE         = regexp.MustCompile(`(?i)@(Get|Post|Put|Patch|Delete|All)\s*\(\s*(?:["'\x60]([^"'\x60]*)["'\x60])?\s*\)`)
	nodeRewriteMethodRE     = regexp.MustCompile(`(?is)\b(?:async\s+)?rewrites\s*\([^)]*\)\s*\{`)
	nodeRewriteArrowBlockRE = regexp.MustCompile(`(?is)\brewrites\s*:\s*(?:async\s*)?\([^)]*\)\s*=>\s*\{`)
	nodeRewriteArrowArrayRE = regexp.MustCompile(`(?is)\brewrites\s*:\s*(?:async\s*)?\([^)]*\)\s*=>\s*\[`)
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
		extracted, err := extractNodeCallsContext(ctx, source)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extracted...)
		extracted, err = extractNodeRoutesContext(ctx, source)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extracted...)
		if strings.HasPrefix(strings.ToLower(filepath.Base(source.rel)), "next.config.") {
			extracted, err = extractNextRewritesContext(ctx, source)
			if err != nil {
				return nil, err
			}
			endpoints = append(endpoints, extracted...)
		}
	}
	return endpoints, nil
}

func extractNodeCallsContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, loc := range nodeFetchRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		method := source.text[loc[2]:loc[3]]
		raw := source.text[loc[4]:loc[5]]
		path, hint := splitHTTPURL(raw)
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "axios"))
	}
	for _, loc := range nodeAxiosConfigRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body := source.text[loc[2]:loc[3]]
		method := jsStringProperty(body, "method")
		raw := jsStringProperty(body, "url")
		path, hint := splitHTTPURL(raw)
		if method == "" || path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "axios"))
	}
	for _, loc := range nodeClientVerbRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		method := source.text[loc[2]:loc[3]]
		raw := source.text[loc[4]:loc[5]]
		path, hint := splitHTTPURL(raw)
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "http-client"))
	}
	for _, loc := range nodeObjectClientCallRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		openOffset := loc[1] - 1
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '{', '}', true, false)
		if !ok {
			continue
		}
		body := source.text[openOffset+1 : closeOffset]
		raw := jsStringProperty(body, "url")
		if raw == "" {
			raw = jsStringProperty(body, "path")
		}
		path, hint := splitHTTPURL(raw)
		if path == "" {
			continue
		}
		method := jsStringProperty(body, "method")
		if method == "" {
			method = "GET"
		}
		callName := source.text[loc[2]:loc[3]]
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), nodeClientSource(callName)))
	}
	for _, loc := range nodePathClientCallRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw := source.text[loc[4]:loc[5]]
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
		callName := source.text[loc[2]:loc[3]]
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), nodeClientSource(callName)))
	}
	return endpoints, ctx.Err()
}

func nodeClientSource(callName string) string {
	name := strings.ToLower(strings.TrimSpace(callName))
	switch {
	case strings.Contains(name, "apifetch"):
		return "api-fetch"
	case strings.Contains(name, "ugcfetch"):
		return "ugc-fetch"
	case strings.Contains(name, "httpclient"), strings.Contains(name, "apiclient"):
		return "http-client"
	default:
		return "request-client"
	}
}

func extractNodeRoutesContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, loc := range nodeExpressRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		method := source.text[loc[2]:loc[3]]
		if strings.EqualFold(method, "all") {
			method = "ANY"
		}
		path := source.text[loc[4]:loc[5]]
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "express-route"))
	}

	for _, controller := range nodeControllerRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prefix := ""
		if controller[2] >= 0 && controller[3] >= 0 {
			prefix = source.text[controller[2]:controller[3]]
		}
		openOffset := controller[1] - 1
		closeOffset, ok := findMatchingDelimiter(source.text, openOffset, '{', '}', true, false)
		if !ok {
			continue
		}
		bodyStart := openOffset + 1
		body := source.text[bodyStart:closeOffset]
		for _, loc := range nodeNestRouteRE.FindAllStringSubmatchIndex(body, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			method := body[loc[2]:loc[3]]
			if strings.EqualFold(method, "all") {
				method = "ANY"
			}
			path := ""
			if loc[4] >= 0 && loc[5] >= 0 {
				path = body[loc[4]:loc[5]]
			}
			path = joinEndpointPaths(prefix, path)
			if path == "" {
				continue
			}
			endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, bodyStart+loc[0]), "nest-route"))
		}
	}
	return endpoints, ctx.Err()
}

func extractNextRewritesContext(ctx context.Context, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, span := range nextRewriteSpans(source.text) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text := source.text[span.start:span.end]
		for _, object := range jsObjectSpans(text) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			body := text[object.start:object.end]
			from := jsStringProperty(body, "source")
			destination := jsStringProperty(body, "destination")
			to, hint := splitHTTPURL(destination)
			if from == "" || to == "" {
				continue
			}
			endpoint := httpEndpoint(topology.DirectionInbound, "ANY", from, hint, endpointLocation(source, span.start+object.start-1), "next-rewrite")
			endpoint.Transforms = []topology.Transform{{Kind: "rewrite", From: from, To: to}}
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, ctx.Err()
}

func jsObjectSpans(text string) []endpointSourceSpan {
	var spans []endpointSourceSpan
	for offset := 0; offset < len(text); {
		relativeOpen := strings.IndexByte(text[offset:], '{')
		if relativeOpen < 0 {
			break
		}
		openOffset := offset + relativeOpen
		closeOffset, ok := findMatchingDelimiter(text, openOffset, '{', '}', true, false)
		if !ok {
			break
		}
		spans = append(spans, endpointSourceSpan{start: openOffset + 1, end: closeOffset})
		offset = closeOffset + 1
	}
	return spans
}

func nextRewriteSpans(text string) []endpointSourceSpan {
	var spans []endpointSourceSpan
	for _, pattern := range []struct {
		re    *regexp.Regexp
		open  byte
		close byte
	}{
		{re: nodeRewriteMethodRE, open: '{', close: '}'},
		{re: nodeRewriteArrowBlockRE, open: '{', close: '}'},
		{re: nodeRewriteArrowArrayRE, open: '[', close: ']'},
	} {
		for _, loc := range pattern.re.FindAllStringIndex(text, -1) {
			openOffset := loc[1] - 1
			closeOffset, ok := findMatchingDelimiter(text, openOffset, pattern.open, pattern.close, true, false)
			if !ok {
				continue
			}
			spans = append(spans, endpointSourceSpan{start: openOffset + 1, end: closeOffset})
		}
	}
	return spans
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
