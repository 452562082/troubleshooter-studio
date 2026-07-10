package analyzer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

const maxEndpointScanFileBytes = 512 * 1024

var (
	endpointWalkDir  = filepath.WalkDir
	endpointStat     = os.Stat
	endpointReadFile = os.ReadFile
)

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

type endpointSourceSpan struct {
	start int
	end   int
}

func ScanEndpointsContext(ctx context.Context, opts EndpointScanOptions) ([]topology.Endpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEndpointRepoRoot(opts.RepoPath); err != nil {
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

	return normalizeEndpointsContext(ctx, opts, endpoints)
}

func validateEndpointRepoRoot(repoPath string) error {
	if strings.TrimSpace(repoPath) == "" {
		return fmt.Errorf("endpoint scan repository path is empty")
	}
	root, err := os.Open(repoPath)
	if err != nil {
		return fmt.Errorf("open endpoint scan repository root %q: %w", repoPath, err)
	}
	defer root.Close()

	info, err := root.Stat()
	if err != nil {
		return fmt.Errorf("stat endpoint scan repository root %q: %w", repoPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("endpoint scan repository root %q is not a directory", repoPath)
	}
	if _, err := root.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read endpoint scan repository root %q: %w", repoPath, err)
	}
	return nil
}

func endpointSources(ctx context.Context, opts EndpointScanOptions, match func(string) bool) ([]endpointSource, error) {
	files, err := walkEndpointFilesContext(ctx, opts.RepoPath, opts.IncludePaths, func(rel string) bool {
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
		info, err := endpointStat(path)
		if err != nil {
			return nil, fmt.Errorf("stat endpoint source %q: %w", path, err)
		}
		if info.Size() > maxEndpointScanFileBytes {
			continue
		}
		data, err := endpointReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read endpoint source %q: %w", path, err)
		}
		rel, err := filepath.Rel(opts.RepoPath, path)
		if err != nil {
			rel = path
		}
		out = append(out, endpointSource{rel: filepath.ToSlash(rel), text: string(data)})
	}
	return out, nil
}

func walkEndpointFilesContext(ctx context.Context, root string, include []string, match func(string) bool) ([]string, error) {
	var out []string
	skipDirs := map[string]bool{
		"node_modules": true, "vendor": true,
		"target": true, "build": true, "dist": true,
		"testdata": true, "third_party": true,
	}
	err := endpointWalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("walk endpoint source %q: %w", path, walkErr)
		}
		if entry == nil {
			return fmt.Errorf("walk endpoint source %q: missing directory entry", path)
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
				return fs.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve endpoint source %q relative to %q: %w", path, root, err)
		}
		if len(include) > 0 {
			included := false
			for _, prefix := range include {
				if strings.HasPrefix(rel, strings.TrimSuffix(prefix, "/")) {
					included = true
					break
				}
			}
			if !included {
				return nil
			}
		}
		if match(rel) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeEndpoints(opts EndpointScanOptions, endpoints []topology.Endpoint) []topology.Endpoint {
	normalized, _ := normalizeEndpointsContext(context.Background(), opts, endpoints)
	return normalized
}

func normalizeEndpointsContext(ctx context.Context, opts EndpointScanOptions, endpoints []topology.Endpoint) ([]topology.Endpoint, error) {
	service := singleEffectiveService(opts.Services)
	seen := make(map[string]struct{}, len(endpoints))
	normalized := make([]topology.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].ID != normalized[j].ID {
			return normalized[i].ID < normalized[j].ID
		}
		left, _ := json.Marshal(normalized[i])
		right, _ := json.Marshal(normalized[j])
		return string(left) < string(right)
	})
	return normalized, nil
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
			if path == "" {
				path = "/"
			}
			return ensureLeadingSlash(path), targetHint
		}
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		return ensureLeadingSlash(path), parsed.Hostname()
	}
	if strings.HasPrefix(raw, "//") {
		if parsed, err := url.Parse("http:" + raw); err == nil {
			path := parsed.Path
			if path == "" {
				path = "/"
			}
			return ensureLeadingSlash(path), parsed.Hostname()
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

func findMatchingDelimiter(text string, openOffset int, open, close byte, slashComments, hashComments bool) (int, bool) {
	if openOffset < 0 || openOffset >= len(text) || text[openOffset] != open {
		return 0, false
	}
	depth := 0
	var quote byte
	escaped := false
	lineComment := false
	for i := openOffset; i < len(text); i++ {
		ch := text[i]
		if lineComment {
			if ch == '\n' {
				lineComment = false
			}
			continue
		}
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			continue
		}
		if slashComments && ch == '/' && i+1 < len(text) && text[i+1] == '/' {
			lineComment = true
			i++
			continue
		}
		if hashComments && ch == '#' {
			lineComment = true
			continue
		}
		switch ch {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}
