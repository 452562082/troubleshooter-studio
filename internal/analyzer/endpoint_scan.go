package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

const maxEndpointScanFileBytes = 512 * 1024

type EndpointScanOptions struct {
	Repo         string
	Stack        string
	Framework    string
	RepoPath     string
	Services     []string
	IncludePaths []string
}

type endpointSource struct {
	rel  string
	text string
}

func ScanEndpointsContext(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var endpoints []topology.Endpoint
	var err error
	switch strings.ToLower(strings.TrimSpace(opts.Stack)) {
	case "node", "javascript", "typescript":
		endpoints, err = scanNodeEndpoints(ctx, opts)
	case "php":
		endpoints, err = scanPHPEndpoints(ctx, opts)
	}
	if err != nil {
		return nil, err
	}

	nginxEndpoints, err := scanNginxEndpoints(ctx, opts)
	if err != nil {
		return nil, err
	}
	endpoints = append(endpoints, nginxEndpoints...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return normalizeEndpoints(opts, endpoints), nil
}

func endpointSources(ctx context.Context, opts EndpointScanOptions, match func(string) bool) ([]endpointSource, error) {
	files, err := walkFilesContext(ctx, opts.RepoPath, opts.IncludePaths, func(rel string) bool {
		return !pathHasIgnoredSegment(rel) && !isTestRouteSource(rel) && match(filepath.ToSlash(rel))
	})
	if err != nil {
		return nil, err
	}

	out := make([]endpointSource, 0, len(files))
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxEndpointScanFileBytes {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(opts.RepoPath, path)
		if err != nil {
			rel = path
		}
		out = append(out, endpointSource{rel: filepath.ToSlash(rel), text: string(data)})
	}
	return out, nil
}

func normalizeEndpoints(opts EndpointScanOptions, endpoints []topology.Endpoint) []topology.Endpoint {
	service := singleEffectiveService(opts.Services)
	seen := make(map[string]struct{}, len(endpoints))
	normalized := make([]topology.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Repo == "" {
			endpoint.Repo = opts.Repo
		}
		if endpoint.Service == "" {
			endpoint.Service = service
		}
		endpoint.Protocol = strings.ToLower(strings.TrimSpace(endpoint.Protocol))
		if endpoint.Protocol == "" {
			endpoint.Protocol = "http"
		}
		endpoint.Method = topology.NormalizeHTTPMethod(endpoint.Method)
		if endpoint.Path != "" {
			endpoint.Path = topology.NormalizePath(ensureLeadingSlash(endpoint.Path))
		}
		for i := range endpoint.Transforms {
			endpoint.Transforms[i].Kind = strings.ToLower(strings.TrimSpace(endpoint.Transforms[i].Kind))
			endpoint.Transforms[i].From = topology.NormalizePath(ensureLeadingSlash(endpoint.Transforms[i].From))
			endpoint.Transforms[i].To = topology.NormalizePath(ensureLeadingSlash(endpoint.Transforms[i].To))
		}
		endpoint.ID = endpoint.SemanticID()

		encoded, _ := json.Marshal(endpoint)
		key := string(encoded)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, endpoint)
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].ID != normalized[j].ID {
			return normalized[i].ID < normalized[j].ID
		}
		left, _ := json.Marshal(normalized[i])
		right, _ := json.Marshal(normalized[j])
		return string(left) < string(right)
	})
	return normalized
}

func singleEffectiveService(services []string) string {
	unique := map[string]struct{}{}
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service != "" {
			unique[service] = struct{}{}
		}
	}
	if len(unique) != 1 {
		return ""
	}
	for service := range unique {
		return service
	}
	return ""
}

func endpointLocation(source endpointSource, offset int) string {
	return fmt.Sprintf("%s:%d", source.rel, lineNumberAt(source.text, offset))
}

func splitHTTPURL(raw string) (path, targetHint string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "${") {
		if end := strings.Index(raw, "}"); end >= 0 {
			targetHint = raw[:end+1]
			path = raw[end+1:]
			return ensureLeadingSlash(path), targetHint
		}
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		return ensureLeadingSlash(parsed.Path), parsed.Hostname()
	}
	if strings.HasPrefix(raw, "//") {
		if parsed, err := url.Parse("http:" + raw); err == nil {
			return ensureLeadingSlash(parsed.Path), parsed.Hostname()
		}
	}
	return ensureLeadingSlash(raw), ""
}

func targetHintFromURL(raw string) string {
	_, hint := splitHTTPURL(raw)
	if hint != "" {
		return hint
	}
	raw = strings.TrimSpace(raw)
	for _, prefix := range []string{"http://", "https://", "grpc://"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	raw = strings.SplitN(raw, "/", 2)[0]
	raw = strings.SplitN(raw, ":", 2)[0]
	return raw
}

func ensureLeadingSlash(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}
