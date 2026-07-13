package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	laravelRouteRE = regexp.MustCompile(`(?i)\bRoute::(get|post|put|patch|delete|any)\s*\(\s*["']([^"']+)["']`)
	laravelHTTPRE  = regexp.MustCompile(`(?i)\bHttp::(get|post|put|patch|delete)\s*\(\s*((?:env\s*\([^)]*\)|["'][^"']*["'])(?:\s*\.\s*["'][^"']*["'])*)`)
	phpEnvRE       = regexp.MustCompile(`(?i)env\s*\(\s*["']([^"']+)["'][^)]*\)`)
	phpStringRE    = regexp.MustCompile(`["']([^"']*)["']`)
)

func scanPHPEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		return strings.EqualFold(filepath.Ext(rel), ".php")
	})
	if err != nil {
		return nil, err
	}

	var endpoints []topology.Endpoint
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, loc := range laravelRouteRE.FindAllStringSubmatchIndex(source.text, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			method := source.text[loc[2]:loc[3]]
			if strings.EqualFold(method, "any") {
				method = "ANY"
			}
			path := source.text[loc[4]:loc[5]]
			endpoints = append(endpoints, httpEndpoint(topology.DirectionInbound, method, path, "", endpointLocation(source, loc[0]), "laravel-route"))
		}
		for _, loc := range laravelHTTPRE.FindAllStringSubmatchIndex(source.text, -1) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			method := source.text[loc[2]:loc[3]]
			expression := source.text[loc[4]:loc[5]]
			path, hint := splitPHPHTTPExpression(expression)
			if path == "" {
				continue
			}
			endpoints = append(endpoints, httpEndpoint(topology.DirectionOutbound, method, path, hint, endpointLocation(source, loc[0]), "laravel-http"))
		}
	}
	return endpoints, nil
}

func splitPHPHTTPExpression(expression string) (path, hint string) {
	if env := phpEnvRE.FindStringSubmatchIndex(expression); len(env) >= 4 {
		hint = expression[env[2]:env[3]]
		remainder := expression[:env[0]] + expression[env[1]:]
		var pieces []string
		for _, match := range phpStringRE.FindAllStringSubmatch(remainder, -1) {
			pieces = append(pieces, match[1])
		}
		path = strings.Join(pieces, "")
		if path == "" {
			path = "/"
		}
		return ensureLeadingSlash(path), hint
	}
	if literal := phpStringRE.FindStringSubmatch(expression); len(literal) == 2 {
		return splitHTTPURL(literal[1])
	}
	return "", ""
}
