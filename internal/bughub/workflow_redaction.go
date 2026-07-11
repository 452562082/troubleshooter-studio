package bughub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var (
	pemPrivateKeyPattern    = regexp.MustCompile(`(?is)-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----.*?-----END(?: [A-Z0-9]+)* PRIVATE KEY-----`)
	commonTokenPattern      = regexp.MustCompile(`(?i)\b(?:github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|(?:AKIA|ASIA|A3T[A-Z0-9])[A-Z0-9]{12,})\b`)
	bearerPattern           = regexp.MustCompile(`(?i)\bBearer[ \t]+([A-Za-z0-9._~+/=-]{8,})\b`)
	structuredSecretPattern = regexp.MustCompile(`(?im)(^|[?&;,\s{])(["']?(?:authorization|cookie|set[-_]cookie|password|passwd|token|access[-_]token|api[-_]key|client[-_]secret|secret|access[-_]key|private[-_]key)["']?\s*[:=]\s*)([^\s&,}]+)`)
)

var sensitiveNames = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set_cookie": {}, "password": {}, "passwd": {},
	"token": {}, "access_token": {}, "api_key": {}, "client_secret": {}, "secret": {},
	"access_key": {}, "private_key": {},
}

func containsSensitiveData(data []byte) bool {
	if pemPrivateKeyPattern.Match(data) || commonTokenPattern.Match(data) || containsActualBearer(data) {
		return true
	}
	for _, match := range structuredSecretPattern.FindAllSubmatch(data, -1) {
		if len(match) == 4 && isActualSecretValue(string(match[3])) {
			return true
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err == nil {
		var trailing any
		if err := decoder.Decode(&trailing); err == io.EOF && sensitiveJSONValue(value) {
			return true
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if key, value, ok := splitStructuredSecret(scanner.Text()); ok && isSensitiveName(key) && isActualSecretValue(value) {
			return true
		}
	}
	return false
}

func containsActualBearer(data []byte) bool {
	for _, match := range bearerPattern.FindAllSubmatch(data, -1) {
		if len(match) == 2 && tokenLooksActual(string(match[1])) {
			return true
		}
	}
	return false
}

func tokenLooksActual(value string) bool {
	lower := strings.ToLower(value)
	switch lower {
	case "authentication", "authorization", "credentials", "token", "example", "configured":
		return false
	}
	if len(value) >= 20 {
		return true
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return r >= '0' && r <= '9' || strings.ContainsRune("._~+/-=", r)
	}) >= 0
}

func sensitiveJSONValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isSensitiveName(key) && actualJSONValue(child) {
				return true
			}
			if sensitiveJSONValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if sensitiveJSONValue(child) {
				return true
			}
		}
	case string:
		return containsSensitiveText(typed)
	}
	return false
}

func actualJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil, bool:
		return false
	case string:
		return isActualSecretValue(typed)
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return true
	}
}

func containsSensitiveText(value string) bool {
	data := []byte(value)
	if pemPrivateKeyPattern.Match(data) || commonTokenPattern.Match(data) || containsActualBearer(data) {
		return true
	}
	for _, match := range structuredSecretPattern.FindAllStringSubmatch(value, -1) {
		if len(match) == 4 && isActualSecretValue(match[3]) {
			return true
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		if key, candidate, ok := splitStructuredSecret(scanner.Text()); ok && isSensitiveName(key) && isActualSecretValue(candidate) {
			return true
		}
	}
	return false
}

func splitStructuredSecret(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, separator := range []string{":", "="} {
		if index := strings.Index(trimmed, separator); index > 0 {
			key := strings.Trim(strings.TrimSpace(trimmed[:index]), `"'`)
			if strings.ContainsAny(key, " {}[],") {
				continue
			}
			return key, strings.TrimSpace(trimmed[index+1:]), true
		}
	}
	return "", "", false
}

func isSensitiveName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	_, ok := sensitiveNames[normalized]
	return ok
}

func isActualSecretValue(value string) bool {
	trimmed := strings.Trim(strings.TrimSpace(value), `"',}`)
	return trimmed != "" && trimmed != "[]" && trimmed != "{}" &&
		!strings.EqualFold(trimmed, "null") && !strings.EqualFold(trimmed, "true") &&
		!strings.EqualFold(trimmed, "false") && !strings.EqualFold(trimmed, redactedValue)
}

func redactSensitiveAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isSensitiveName(key) && actualJSONValue(child) {
				typed[key] = redactedValue
			} else {
				typed[key] = redactSensitiveAny(child)
			}
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = redactSensitiveAny(child)
		}
		return typed
	case string:
		return redactSensitiveText(typed)
	default:
		return value
	}
}

func redactSensitiveText(value string) string {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var structured any
	if err := decoder.Decode(&structured); err == nil {
		var trailing any
		if err := decoder.Decode(&trailing); err == io.EOF {
			switch structured.(type) {
			case map[string]any, []any:
				if encoded, err := json.Marshal(redactSensitiveAny(structured)); err == nil {
					return string(encoded)
				}
			}
		}
	}
	value = pemPrivateKeyPattern.ReplaceAllString(value, redactedValue)
	value = commonTokenPattern.ReplaceAllString(value, redactedValue)
	value = bearerPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := strings.Fields(match)
		if len(parts) == 2 && tokenLooksActual(parts[1]) {
			return "Bearer " + redactedValue
		}
		return match
	})
	value = structuredSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := structuredSecretPattern.FindStringSubmatch(match)
		if len(parts) == 4 && isActualSecretValue(parts[3]) {
			return parts[1] + parts[2] + redactedValue
		}
		return match
	})
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		key, candidate, ok := splitStructuredSecret(line)
		if !ok || !isSensitiveName(key) || !isActualSecretValue(candidate) {
			continue
		}
		separatorIndex := strings.IndexAny(line, ":=")
		if separatorIndex >= 0 {
			lines[index] = line[:separatorIndex+1] + " " + redactedValue
		}
	}
	return strings.Join(lines, "\n")
}
