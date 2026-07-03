package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type routePattern struct {
	re        *regexp.Regexp
	method    func([]string) string
	pathIndex int
}

var apiRoutePatterns = []routePattern{
	{
		re: regexp.MustCompile(`(?i)\b(?:router|app|r)\.(get|post|put|patch|delete)\s*\(\s*["']([^"']+)["']`),
		method: func(m []string) string {
			return strings.ToUpper(m[1])
		},
		pathIndex: 2,
	},
	{
		re: regexp.MustCompile(`(?i)\b(?:mux|http)\.HandleFunc\s*\(\s*["']([^"']+)["']`),
		method: func([]string) string {
			return "ANY"
		},
		pathIndex: 1,
	},
}

var (
	springMappingRE       = regexp.MustCompile(`@(GetMapping|PostMapping|PutMapping|PatchMapping|DeleteMapping|RequestMapping)\s*(?:\(([^)]*)\))?`)
	springNamedPathRE     = regexp.MustCompile(`(?:^|[,{]\s*)(?:value|path)\s*=\s*["']([^"']+)["']`)
	springShorthandPathRE = regexp.MustCompile(`^\s*["']([^"']+)["']`)
)

func ScanAPIRoutes(stack, repoPath string, includePaths []string) []APIRoute {
	files, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		if pathHasIgnoredSegment(rel) || isTestRouteSource(rel) {
			return false
		}
		return routeSourceExtMatches(stack, strings.ToLower(filepath.Ext(rel)))
	})

	seen := map[string]bool{}
	var out []APIRoute
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil || info.Size() > 512*1024 {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repoPath, file)
		for _, route := range extractRoutesFromSource(string(data), filepath.ToSlash(rel)) {
			key := route.Method + " " + route.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, route)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			if out[i].Method == out[j].Method {
				return out[i].Source < out[j].Source
			}
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func extractRoutesFromSource(src, source string) []APIRoute {
	var routes []APIRoute
	routes = append(routes, extractSpringRoutes(src, source)...)
	for _, pat := range apiRoutePatterns {
		for _, loc := range pat.re.FindAllStringSubmatchIndex(src, -1) {
			matches := pat.re.FindStringSubmatch(src[loc[0]:loc[1]])
			if len(matches) <= pat.pathIndex {
				continue
			}
			path := normalizeRoutePath(matches[pat.pathIndex])
			if path == "" {
				continue
			}
			routes = append(routes, APIRoute{
				Path:     path,
				Method:   pat.method(matches),
				Source:   source,
				Line:     lineNumberAt(src, loc[0]),
				Pattern:  routePatternFor(path),
				Strength: "scanned",
			})
		}
	}
	return routes
}

func extractSpringRoutes(src, source string) []APIRoute {
	var routes []APIRoute
	classPrefix := ""
	for _, loc := range springMappingRE.FindAllStringSubmatchIndex(src, -1) {
		if len(loc) < 6 {
			continue
		}
		annotation := src[loc[2]:loc[3]]
		args := ""
		if loc[4] >= 0 && loc[5] >= 0 {
			args = src[loc[4]:loc[5]]
		}
		path, ok := springMappingPath(args)
		if !ok {
			continue
		}
		if annotation == "RequestMapping" && springMappingTargetsClass(src, loc[1]) {
			classPrefix = normalizeRoutePath(path)
			continue
		}
		path = joinRoutePaths(classPrefix, path)
		path = normalizeRoutePath(path)
		if path == "" {
			continue
		}
		routes = append(routes, APIRoute{
			Path:     path,
			Method:   methodFromSpringMapping(annotation, src[loc[0]:loc[1]]),
			Source:   source,
			Line:     lineNumberAt(src, loc[0]),
			Pattern:  routePatternFor(path),
			Strength: "scanned",
		})
	}
	return routes
}

func springMappingPath(args string) (string, bool) {
	if m := springNamedPathRE.FindStringSubmatch(args); len(m) == 2 {
		return m[1], true
	}
	if m := springShorthandPathRE.FindStringSubmatch(args); len(m) == 2 {
		return m[1], true
	}
	return "", false
}

func springMappingTargetsClass(src string, annotationEnd int) bool {
	rest := strings.TrimLeft(src[annotationEnd:], " \t\r\n")
	return strings.HasPrefix(rest, "class ") ||
		strings.HasPrefix(rest, "interface ") ||
		strings.HasPrefix(rest, "public class ") ||
		strings.HasPrefix(rest, "public interface ") ||
		strings.HasPrefix(rest, "abstract class ") ||
		strings.HasPrefix(rest, "public abstract class ")
}

func joinRoutePaths(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}
	if path != "/graphql" && path != "/api" && !strings.HasPrefix(path, "/api/") {
		return ""
	}
	return path
}

func routePatternFor(path string) string {
	path = normalizeRoutePath(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if isRouteParam(part) {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/")
}

func routeMatchStrength(routePath, endpointPath string) string {
	routePath = normalizeRoutePath(routePath)
	endpointPath = normalizeRoutePath(endpointPath)
	if routePath == "" || endpointPath == "" {
		return ""
	}
	if routePath == endpointPath {
		return "exact"
	}

	routeParts := strings.Split(strings.Trim(routePath, "/"), "/")
	endpointParts := strings.Split(strings.Trim(endpointPath, "/"), "/")
	if len(routeParts) == len(endpointParts) {
		matched := true
		hasParam := false
		for i := range routeParts {
			if isRouteParam(routeParts[i]) {
				hasParam = true
				continue
			}
			if routeParts[i] != endpointParts[i] {
				matched = false
				break
			}
		}
		if matched && hasParam {
			return "pattern"
		}
	}

	if strings.HasPrefix(endpointPath, strings.TrimRight(routePath, "/")+"/") {
		return "prefix"
	}
	return ""
}

func routeSourceExtMatches(stack, ext string) bool {
	switch strings.ToLower(stack) {
	case "go":
		return ext == ".go"
	case "node", "javascript", "typescript":
		return ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx"
	case "java", "jvm":
		return ext == ".java" || ext == ".kt"
	case "python":
		return ext == ".py"
	default:
		return ext == ".go" || ext == ".java" || ext == ".kt" || ext == ".py" ||
			ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx"
	}
}

func methodFromJavaMapping(mapping string) string {
	return methodFromSpringMapping(mapping, "")
}

func methodFromSpringMapping(mapping, annotation string) string {
	switch mapping {
	case "GetMapping":
		return "GET"
	case "PostMapping":
		return "POST"
	case "PutMapping":
		return "PUT"
	case "PatchMapping":
		return "PATCH"
	case "DeleteMapping":
		return "DELETE"
	default:
		if m := regexp.MustCompile(`RequestMethod\.(GET|POST|PUT|PATCH|DELETE)`).FindStringSubmatch(annotation); len(m) == 2 {
			return m[1]
		}
		return "ANY"
	}
}

func isRouteParam(part string) bool {
	return part == "*" ||
		strings.HasPrefix(part, ":") ||
		(strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}")) ||
		(strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">"))
}

func lineNumberAt(src string, offset int) int {
	if offset < 0 {
		return 0
	}
	line := 1
	for i := 0; i < len(src) && i < offset; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

func pathHasIgnoredSegment(path string) bool {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		if part == "node_modules" || part == "vendor" || part == "testdata" ||
			part == "__tests__" || part == "__mocks__" || part == "fixtures" ||
			part == "mock" || part == "mocks" {
			return true
		}
	}
	return false
}

func isTestRouteSource(path string) bool {
	name := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	return strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, "_test.py") ||
		strings.HasSuffix(name, ".test.js") ||
		strings.HasSuffix(name, ".test.jsx") ||
		strings.HasSuffix(name, ".test.ts") ||
		strings.HasSuffix(name, ".test.tsx") ||
		strings.HasSuffix(name, ".spec.js") ||
		strings.HasSuffix(name, ".spec.jsx") ||
		strings.HasSuffix(name, ".spec.ts") ||
		strings.HasSuffix(name, ".spec.tsx")
}
