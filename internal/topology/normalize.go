package topology

import (
	"net/url"
	"regexp"
	"strings"
)

var duplicateSlashPattern = regexp.MustCompile(`/+`)

func NormalizePath(rawPath string) string {
	path := pathWithoutQueryOrHost(rawPath)
	path = duplicateSlashPattern.ReplaceAllString(path, "/")
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}

	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = normalizePathSegment(segment)
	}
	return strings.Join(segments, "/")
}

func NormalizeHTTPMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func (e Endpoint) SemanticID() string {
	protocol := strings.ToLower(strings.TrimSpace(e.Protocol))
	parts := []string{e.Repo, protocol, strings.ToLower(string(e.Direction))}
	if protocol == "http" {
		parts = append(parts, NormalizeHTTPMethod(e.Method), NormalizePath(e.Path))
	} else {
		parts = append(parts, e.RPCMethod)
	}
	return strings.Join(parts, ":")
}

func pathWithoutQueryOrHost(rawPath string) string {
	parsed, err := url.Parse(rawPath)
	if err == nil {
		return parsed.Path
	}
	if index := strings.IndexAny(rawPath, "?#"); index >= 0 {
		return rawPath[:index]
	}
	return rawPath
}

func normalizePathSegment(segment string) string {
	switch {
	case strings.HasPrefix(segment, "*"):
		return "{wildcard}"
	case strings.HasPrefix(segment, ":"):
		if strings.HasSuffix(segment, "*") {
			return "{wildcard}"
		}
		return "{param}"
	case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"):
		if strings.HasPrefix(strings.TrimPrefix(segment, "{"), "*") {
			return "{wildcard}"
		}
		return "{param}"
	case strings.HasPrefix(segment, "[") && strings.HasSuffix(segment, "]"):
		if strings.Contains(segment, "...") {
			return "{wildcard}"
		}
		return "{param}"
	default:
		return segment
	}
}
