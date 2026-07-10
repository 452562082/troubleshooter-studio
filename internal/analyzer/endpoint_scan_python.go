package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	pythonDecoratorRE      = regexp.MustCompile(`(?i)@([A-Za-z_][A-Za-z0-9_]*)\.(get|post|put|patch|delete|route|api_route)\s*\(\s*["']([^"']+)["']([^)]*)\)`)
	pythonRouteMethodsRE   = regexp.MustCompile(`(?is)\bmethods\s*=\s*[\[(]([^\])]+)[\])]`)
	pythonQuotedMethodRE   = regexp.MustCompile(`["'](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["']`)
	pythonAddURLRuleRE     = regexp.MustCompile(`(?i)\badd_url_rule\s*\(\s*["']([^"']+)["']([^)]*)\)`)
	pythonDjangoRouteRE    = regexp.MustCompile(`(?i)\b(?:path|re_path)\s*\(\s*["']([^"']+)["']`)
	pythonDjangoParamRE    = regexp.MustCompile(`<(?:[A-Za-z_][A-Za-z0-9_]*:)?[A-Za-z_][A-Za-z0-9_]*>`)
	pythonHTTPCallRE       = regexp.MustCompile(`\b(requests|httpx)(?:\.(?:Client|AsyncClient)\s*\([^)]*\))?\.(get|post|put|patch|delete|head|options)\s*\(\s*`)
	pythonNamedURLExprRE   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\+\s*["']([^"']+)["']`)
	pythonLiteralURLExprRE = regexp.MustCompile(`^["']([^"']+)["']`)
)

func scanPythonEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		return strings.EqualFold(filepath.Ext(rel), ".py")
	})
	if err != nil {
		return nil, err
	}

	var endpoints []topology.Endpoint
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		extracted, err := extractPythonEndpointsContext(ctx, opts, source)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, extracted...)
	}
	return endpoints, ctx.Err()
}

func extractPythonEndpointsContext(ctx context.Context, opts EndpointScanOptions, source endpointSource) ([]topology.Endpoint, error) {
	var endpoints []topology.Endpoint
	for _, loc := range pythonDecoratorRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		methodName := strings.ToLower(source.text[loc[4]:loc[5]])
		path := normalizePythonRoutePath(source.text[loc[6]:loc[7]])
		args := source.text[loc[8]:loc[9]]
		sourceName := "fastapi-route"
		if methodName == "route" || strings.Contains(strings.ToLower(opts.Framework), "flask") {
			sourceName = "flask-route"
		}
		methods := []string{strings.ToUpper(methodName)}
		if methodName == "route" || methodName == "api_route" {
			methods = pythonRouteMethods(args)
			if len(methods) == 0 {
				methods = []string{"ANY"}
			}
		}
		for _, method := range methods {
			endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), sourceName))
		}
	}

	for _, loc := range pythonAddURLRuleRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := normalizePythonRoutePath(source.text[loc[2]:loc[3]])
		methods := pythonRouteMethods(source.text[loc[4]:loc[5]])
		if len(methods) == 0 {
			methods = []string{"ANY"}
		}
		for _, method := range methods {
			endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "flask-route"))
		}
	}

	for _, loc := range pythonDjangoRouteRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := normalizeDjangoPath(source.text[loc[2]:loc[3]])
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, "ANY", path, "", endpointLocation(source, loc[0]), "django-route"))
	}

	for _, loc := range pythonHTTPCallRE.FindAllStringSubmatchIndex(source.text, -1) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		library := strings.ToLower(source.text[loc[2]:loc[3]])
		method := source.text[loc[4]:loc[5]]
		path, hint := splitPythonHTTPExpression(source.text[loc[1]:])
		if path == "" {
			continue
		}
		endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "python-"+library))
	}
	return endpoints, ctx.Err()
}

func pythonRouteMethods(args string) []string {
	match := pythonRouteMethodsRE.FindStringSubmatch(args)
	if len(match) != 2 {
		return nil
	}
	var methods []string
	for _, method := range pythonQuotedMethodRE.FindAllStringSubmatch(match[1], -1) {
		methods = append(methods, method[1])
	}
	return methods
}

func normalizeDjangoPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "^")
	path = strings.TrimSuffix(path, "$")
	path = pythonDjangoParamRE.ReplaceAllString(path, "{param}")
	return ensureLeadingSlash(path)
}

func normalizePythonRoutePath(path string) string {
	return pythonDjangoParamRE.ReplaceAllString(path, "{param}")
}

func splitPythonHTTPExpression(expression string) (path, hint string) {
	if match := pythonNamedURLExprRE.FindStringSubmatch(expression); len(match) == 3 {
		return ensureLeadingSlash(match[2]), match[1]
	}
	if match := pythonLiteralURLExprRE.FindStringSubmatch(expression); len(match) == 2 {
		return splitHTTPURL(match[1])
	}
	return "", ""
}
