package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	pythonDecoratorRE    = regexp.MustCompile(`(?i)@([A-Za-z_][A-Za-z0-9_]*)\.(get|post|put|patch|delete|route|api_route)\s*\(\s*["']([^"']+)["']([^)]*)\)`)
	pythonRouteMethodsRE = regexp.MustCompile(`(?is)\bmethods\s*=\s*[\[(]([^\])]+)[\])]`)
	pythonQuotedMethodRE = regexp.MustCompile(`["'](GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)["']`)
	pythonAddURLRuleRE   = regexp.MustCompile(`(?i)\badd_url_rule\s*\(\s*["']([^"']+)["']([^)]*)\)`)
	pythonDjangoRouteRE  = regexp.MustCompile(`(?i)\b(?:path|re_path)\s*\(\s*["']([^"']+)["']`)
	pythonDjangoParamRE  = regexp.MustCompile(`<(?:[A-Za-z_][A-Za-z0-9_]*:)?[A-Za-z_][A-Za-z0-9_]*>`)
	pythonHTTPCallRE     = regexp.MustCompile(`\b(requests|httpx)(?:\.(?:Client|AsyncClient)\s*\([^)]*\))?\.(get|post|put|patch|delete|head|options)\s*\(\s*`)
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
		source.text = maskPythonCommentsAndDocstrings(source.text)
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
	expression = strings.TrimLeft(expression, " \t\r\n")
	if identifier, rest, ok := pythonIdentifierPrefix(expression); ok {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(rest, "+") {
			return "", ""
		}
		literal, tail, ok := pythonQuotedLiteral(strings.TrimLeft(rest[1:], " \t\r\n"))
		if !ok || !pythonHTTPArgumentEnds(tail) {
			return "", ""
		}
		return ensureLeadingSlash(literal), identifier
	}
	literal, tail, ok := pythonQuotedLiteral(expression)
	if !ok || !pythonHTTPArgumentEnds(tail) {
		return "", ""
	}
	return splitHTTPURL(literal)
}

func pythonIdentifierPrefix(expression string) (identifier, rest string, ok bool) {
	if expression == "" || !isPythonIdentifierStart(expression[0]) {
		return "", expression, false
	}
	i := 1
	for i < len(expression) && isPythonIdentifierPart(expression[i]) {
		i++
	}
	return expression[:i], expression[i:], true
}

func isPythonIdentifierStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isPythonIdentifierPart(ch byte) bool {
	return isPythonIdentifierStart(ch) || ch >= '0' && ch <= '9'
}

func pythonQuotedLiteral(expression string) (literal, tail string, ok bool) {
	if len(expression) < 2 || expression[0] != '\'' && expression[0] != '"' {
		return "", expression, false
	}
	quote := expression[0]
	escaped := false
	for i := 1; i < len(expression); i++ {
		if escaped {
			escaped = false
			continue
		}
		if expression[i] == '\\' {
			escaped = true
			continue
		}
		if expression[i] == quote {
			return expression[1:i], expression[i+1:], true
		}
	}
	return "", expression, false
}

func pythonHTTPArgumentEnds(tail string) bool {
	tail = strings.TrimLeft(tail, " \t\r\n")
	return strings.HasPrefix(tail, ",") || strings.HasPrefix(tail, ")")
}
