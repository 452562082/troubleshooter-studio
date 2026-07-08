package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
