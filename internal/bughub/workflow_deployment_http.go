package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

const HTTPVersionMaxBodyBytes = 1 << 20

const defaultHTTPVersionTimeout = 5 * time.Second
const maxHTTPVersionTimeout = 10 * time.Second

// HTTPVersionVerifier reads a version document. It intentionally accepts no
// request headers or credentials so neither can enter workflow persistence.
type HTTPVersionVerifier struct {
	Environment string
	Config      config.HTTPDeploymentVerification
	Client      *http.Client
	Timeout     time.Duration
}

func (v HTTPVersionVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	observation := newRuntimeDeploymentObservation(request, "http")
	if normalizedDeploymentSource(request.Source) != "http" {
		setDeploymentDiagnostic(&observation, "provider_mismatch", "部署版本验证方式不匹配")
		return observation, ErrDeploymentVerifierUnavailable
	}
	if strings.TrimSpace(request.Environment) != strings.TrimSpace(v.Environment) {
		observation.Result = DeploymentResultMismatched
		setDeploymentDiagnostic(&observation, "environment_mismatch", "版本接口环境与 Case 不一致")
		return observation, nil
	}
	target, err := url.Parse(strings.TrimSpace(v.Config.URL))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil {
		setDeploymentDiagnostic(&observation, "invalid_http_config", "HTTP 版本验证配置无效")
		return observation, nil
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPVersionTimeout
	}
	if timeout > maxHTTPVersionTimeout {
		timeout = maxHTTPVersionTimeout
	}
	client := &http.Client{Timeout: timeout}
	if v.Client != nil {
		*client = *v.Client
		client.Timeout = timeout
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.User != nil {
			return errors.New("deployment version redirect credentials rejected")
		}
		if !strings.EqualFold(req.URL.Host, target.Host) {
			return errors.New("deployment version redirect rejected")
		}
		if target.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("deployment version HTTPS downgrade rejected")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("deployment version redirect scheme rejected")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many deployment version redirects")
		}
		return nil
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		setDeploymentDiagnostic(&observation, "invalid_http_request", "HTTP 版本验证请求无效")
		return observation, nil
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		setDeploymentDiagnostic(&observation, "http_request_failed", "版本接口暂不可用")
		return observation, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		setDeploymentDiagnostic(&observation, "http_status_unavailable", "版本接口未返回成功状态")
		return observation, nil
	}
	limited := io.LimitReader(response.Body, HTTPVersionMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > HTTPVersionMaxBodyBytes {
		setDeploymentDiagnostic(&observation, "http_body_unavailable", "版本接口响应不可读取或过大")
		return observation, nil
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		setDeploymentDiagnostic(&observation, "invalid_json", "版本接口响应不是有效 JSON")
		return observation, nil
	}
	selected, ok := resolveJSONPointer(document, v.Config.JSONPointer)
	if !ok {
		setDeploymentDiagnostic(&observation, "json_pointer_not_found", "版本字段未找到")
		return observation, nil
	}
	commits := commitsFromVersionValue(selected, request.ExpectedCommits)
	if len(commits) == 0 {
		setDeploymentDiagnostic(&observation, "invalid_version_value", "版本字段不包含可验证的提交")
		return observation, nil
	}
	observation.ObservedCommits = commits
	if scalar, ok := selected.(string); ok {
		observation.ObservedVersion = strings.TrimSpace(scalar)
	} else if encoded, err := json.Marshal(selected); err == nil && len(encoded) <= 128 {
		observation.ObservedVersion = string(encoded)
	}
	return finishExactRuntimeObservation(observation), nil
}

func resolveJSONPointer(document any, pointer string) (any, bool) {
	pointer = strings.TrimSpace(pointer)
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token, ok := decodeJSONPointerToken(rawToken)
		if !ok {
			return nil, false
		}
		switch node := current.(type) {
		case map[string]any:
			current, ok = node[token]
			if !ok {
				return nil, false
			}
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false
			}
			parsed, err := strconv.ParseUint(token, 10, 64)
			if err != nil || parsed >= uint64(len(node)) {
				return nil, false
			}
			current = node[int(parsed)]
		default:
			return nil, false
		}
	}
	return current, true
}

func decodeJSONPointerToken(token string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", false
		}
		i++
		switch token[i] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", false
		}
	}
	return out.String(), true
}

func commitsFromVersionValue(value any, expected map[string]string) map[string]string {
	out := make(map[string]string)
	switch typed := value.(type) {
	case string:
		commit := strings.TrimSpace(typed)
		if commit != "" {
			for repo := range expected {
				out[repo] = commit
			}
		}
	case map[string]any:
		for repo := range expected {
			if commit, ok := typed[repo].(string); ok && strings.TrimSpace(commit) != "" {
				out[repo] = strings.TrimSpace(commit)
			}
		}
	}
	return out
}
