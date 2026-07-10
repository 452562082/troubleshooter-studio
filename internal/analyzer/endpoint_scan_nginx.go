package analyzer

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var (
	nginxLocationRE = regexp.MustCompile(`(?is)\blocation\s+(?:\^~\s+|=\s+)?([^\s{]+)\s*\{([^{}]*)\}`)
	nginxProxyRE    = regexp.MustCompile(`(?i)\bproxy_pass\s+([^;\s]+)`)
	nginxRewriteRE  = regexp.MustCompile(`(?i)\brewrite\s+([^\s;]+)\s+([^\s;]+)`)
	nginxCaptureRE  = regexp.MustCompile(`\((?:\.\*|\.\+)\)|\[[^]]+\]\+?`)
	nginxBackrefRE  = regexp.MustCompile(`\$\d+`)
)

func scanNginxEndpoints(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	sources, err := endpointSources(ctx, opts, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return strings.HasSuffix(base, ".conf") || base == "nginx"
	})
	if err != nil {
		return nil, err
	}

	var endpoints []topology.Endpoint
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, loc := range nginxLocationRE.FindAllStringSubmatchIndex(source.text, -1) {
			path := source.text[loc[2]:loc[3]]
			body := source.text[loc[4]:loc[5]]
			hint := ""
			if match := nginxProxyRE.FindStringSubmatch(body); len(match) == 2 {
				hint = targetHintFromURL(match[1])
			}
			endpoint := httpEndpoint(topology.DirectionInbound, "ANY", path, hint, endpointLocation(source, loc[0]), "nginx-location")
			for _, rewrite := range nginxRewriteRE.FindAllStringSubmatch(body, -1) {
				endpoint.Transforms = append(endpoint.Transforms, topology.Transform{
					Kind: "rewrite",
					From: normalizeNginxRewritePath(rewrite[1]),
					To:   normalizeNginxRewritePath(rewrite[2]),
				})
			}
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, nil
}

func normalizeNginxRewritePath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "^")
	path = strings.TrimSuffix(path, "$")
	path = strings.ReplaceAll(path, `\/`, "/")
	path = nginxCaptureRE.ReplaceAllString(path, "*wildcard")
	path = nginxBackrefRE.ReplaceAllString(path, "*wildcard")
	return ensureLeadingSlash(path)
}
