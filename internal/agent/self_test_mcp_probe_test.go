package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoProbeMCPHTTPServerInitializesSessionAndListsTools(t *testing.T) {
	const token = "Bearer test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != token {
			http.Error(w, "api key required", http.StatusUnauthorized)
			return
		}
		var message map[string]any
		if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		switch message["method"] {
		case "initialize":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{}}}}\n\n"))
		case "notifications/initialized":
			if request.Header.Get("Mcp-Session-Id") != "session-1" {
				http.Error(w, "session required", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if request.Header.Get("Mcp-Session-Id") != "session-1" || request.Header.Get("MCP-Protocol-Version") != "2024-11-05" {
				http.Error(w, "protocol session required", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"list_deployments"}]}}`))
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	result := doProbeMCPHTTPServer(context.Background(), server.URL, map[string]string{"Authorization": token}, 5*time.Second)
	if result.Err != nil || len(result.Tools) != 1 || result.Tools[0] != "list_deployments" {
		t.Fatalf("HTTP MCP probe result=%+v", result)
	}
}

func TestProbeMCPServersFromConfig_InheritsEnvironmentAndSpecOverrides(t *testing.T) {
	t.Setenv("TSHOOT_PARENT_ENV", "parent")
	t.Setenv("TSHOOT_OVERRIDE_ENV", "parent")

	old := probeMCPFunc
	var captured []string
	probeMCPFunc = func(_ context.Context, _ string, _, env []string, _ time.Duration) MCPProbeResult {
		captured = append([]string(nil), env...)
		return MCPProbeResult{Tools: []string{"ok"}}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"kafka-test": map[string]any{
			"command": "kafka-mcp-server",
			"env": map[string]any{
				"KAFKA_BROKERS":       "broker:9092",
				"TSHOOT_OVERRIDE_ENV": "spec",
			},
		},
	}
	var checks []string
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, name+" "+status+" "+detail)
	})

	env := envSliceToMap(captured)
	if env["TSHOOT_PARENT_ENV"] != "parent" {
		t.Fatalf("parent environment was not inherited: %v", env)
	}
	if env["TSHOOT_OVERRIDE_ENV"] != "spec" {
		t.Fatalf("spec env should override parent env, got %q in %v", env["TSHOOT_OVERRIDE_ENV"], env)
	}
	if env["KAFKA_BROKERS"] != "broker:9092" {
		t.Fatalf("spec env missing KAFKA_BROKERS: %v", env)
	}
	if len(checks) != 1 || !strings.Contains(checks[0], "PASS") {
		t.Fatalf("unexpected checks: %v", checks)
	}
}

func TestProbeMCPServersFromConfigProbesStreamableHTTPWithHeaders(t *testing.T) {
	old := probeMCPHTTPFunc
	var capturedURL string
	var capturedHeaders map[string]string
	probeMCPHTTPFunc = func(_ context.Context, rawURL string, headers map[string]string, _ time.Duration) MCPProbeResult {
		capturedURL = rawURL
		capturedHeaders = headers
		return MCPProbeResult{Tools: []string{"list_deployments"}}
	}
	t.Cleanup(func() { probeMCPHTTPFunc = old })

	servers := map[string]any{
		"base-one2all": map[string]any{
			"type": "streamable-http",
			"url":  "https://one2all.example.test/mcp",
			"headers": map[string]any{
				"Authorization": "Bearer secret",
			},
		},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})
	if capturedURL != "https://one2all.example.test/mcp" || capturedHeaders["Authorization"] != "Bearer secret" {
		t.Fatalf("HTTP MCP probe did not receive configured auth: url=%q headers=%v", capturedURL, capturedHeaders)
	}
	if len(checks) != 1 || checks[0].Status != "PASS" || !strings.Contains(checks[0].Detail, "list_deployments") {
		t.Fatalf("unexpected HTTP MCP checks: %#v", checks)
	}
}

func TestProbeMCPServersFromConfigReportsHTTPAuthenticationFailure(t *testing.T) {
	old := probeMCPHTTPFunc
	probeMCPHTTPFunc = func(context.Context, string, map[string]string, time.Duration) MCPProbeResult {
		return MCPProbeResult{Err: errors.New("initialize HTTP 401: api key required")}
	}
	t.Cleanup(func() { probeMCPHTTPFunc = old })

	servers := map[string]any{
		"base-one2all": map[string]any{"type": "streamable-http", "url": "https://one2all.example.test/mcp"},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})
	if len(checks) != 1 || checks[0].Status != "FAIL" || !strings.Contains(checks[0].Detail, "鉴权或初始化失败") {
		t.Fatalf("HTTP MCP auth failure must be explicit: %#v", checks)
	}
}

func TestProbeMCPServersFromConfig_KafkaRuntimeEOFWarns(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{
			Err:        errors.New("read initialize resp: EOF"),
			StderrTail: "unassigning ev",
		}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"base-kafka-test": map[string]any{
			"command": "kafka-mcp-server",
			"env":     map[string]any{"KAFKA_BROKERS": "broker:9092"},
		},
	}
	var checks []string
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, name+" "+status+" "+detail)
	})
	if len(checks) != 1 || !strings.Contains(checks[0], "WARN") {
		t.Fatalf("kafka runtime EOF should warn, got %v", checks)
	}
	if !strings.Contains(checks[0], "Kafka MCP 已注册") {
		t.Fatalf("missing actionable kafka detail: %v", checks)
	}
}

func TestProbeMCPServersFromConfig_NonKafkaEOFFails(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Err: errors.New("read initialize resp: EOF")}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"base-mongodb-test": map[string]any{
			"command": "mongodb-mcp-server",
		},
	}
	var checks []string
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, name+" "+status+" "+detail)
	})
	if len(checks) != 1 || !strings.Contains(checks[0], "FAIL") {
		t.Fatalf("non-kafka EOF should fail, got %v", checks)
	}
}

func TestProbeMCPServersFromConfig_NonKafkaStartupTimeoutWarns(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Err: errors.New("read initialize resp: timeout / canceled before mcp response")}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"base-mongodb-test": map[string]any{
			"command": "mongodb-mcp-server",
		},
	}
	var checks []string
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, name+" "+status+" "+detail)
	})
	if len(checks) != 1 || !strings.Contains(checks[0], "WARN") {
		t.Fatalf("non-kafka startup timeout should warn, got %v", checks)
	}
	if !strings.Contains(checks[0], "启动/初始化阶段超时") {
		t.Fatalf("missing actionable timeout detail: %v", checks)
	}
}

func TestProbeMCPServers_CodeGraphRequiresExploreTool(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Tools: []string{"codegraph_status"}}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"shop-codegraph": map[string]any{
			"command": "/managed/codegraph",
		},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})

	if len(checks) != 1 || checks[0].Status != "FAIL" || !strings.Contains(checks[0].Detail, "codegraph_explore") {
		t.Fatalf("CodeGraph without codegraph_explore should fail, got %#v", checks)
	}
}

func TestProbeMCPServers_CodeGraphExploreToolPasses(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Tools: []string{"codegraph_explore"}}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"shared-code-intelligence": map[string]any{
			"command": "C:\\managed\\codegraph.cmd",
		},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})

	if len(checks) != 1 || checks[0].Status != "PASS" || !strings.Contains(checks[0].Detail, "codegraph_explore") {
		t.Fatalf("CodeGraph with codegraph_explore should pass, got %#v", checks)
	}
}

func TestProbeMCPServers_CodeGraphEmptyToolSurfaceFails(t *testing.T) {
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{}
	}
	t.Cleanup(func() { probeMCPFunc = old })

	servers := map[string]any{
		"shop-codegraph": map[string]any{"command": "/managed/codegraph"},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})

	if len(checks) != 1 || checks[0].Status != "FAIL" || !strings.Contains(checks[0].Detail, "codegraph_explore") {
		t.Fatalf("empty CodeGraph tool surface should fail, got %#v", checks)
	}
}

func TestProbeMCPServers_CodeGraphMalformedSpecFails(t *testing.T) {
	servers := map[string]any{"shop-codegraph": "not-an-object"}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})

	if len(checks) != 1 || checks[0].Status != "FAIL" || !strings.Contains(checks[0].Detail, "spec") {
		t.Fatalf("malformed CodeGraph spec should fail, got %#v", checks)
	}
}

func TestProbeMCPServers_CodeGraphMissingCommandFails(t *testing.T) {
	servers := map[string]any{
		"shop-codegraph": map[string]any{"args": []any{"serve", "--mcp"}},
	}
	var checks []SelfTestCheck
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	})

	if len(checks) != 1 || checks[0].Status != "FAIL" || !strings.Contains(checks[0].Detail, "command") {
		t.Fatalf("CodeGraph spec without command should fail, got %#v", checks)
	}
}

func envSliceToMap(items []string) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}
