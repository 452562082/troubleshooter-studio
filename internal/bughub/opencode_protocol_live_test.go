package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Runs the real installed CLI against a deterministic local model endpoint.
// No provider credentials or business repositories are used.
func TestOpenCodeLocalProtocolLive(t *testing.T) {
	if os.Getenv("TSHOOT_LIVE_OPENCODE_PROTOCOL") != "1" {
		t.Skip("explicit real CLI opt-in")
	}
	root := t.TempDir()
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	// macOS /var is a symlink to /private/var. OpenCode checks real paths.
	work, _ = filepath.EvalSymlinks(work)
	token := "LOCAL_OPENCODE_PROTOCOL_TOKEN"
	if err := os.WriteFile(filepath.Join(work, "probe.txt"), []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role      string `json:"role"`
				Content   any    `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(request.Messages)
		// Title/summary calls must not consume the main tool sequence.
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Local smoke"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 12 {
			http.Error(w, "unexpected model loop", http.StatusInternalServerError)
			return
		}
		toolCount := 0
		for _, message := range request.Messages {
			if message.Role == "tool" {
				toolCount++
			}
		}
		fix := strings.Contains(string(raw), "PHASE_FIX")
		name := ""
		arguments := map[string]any{}
		if toolCount == 0 {
			name = "read"
			arguments["filePath"] = filepath.Join(work, "probe.txt")
		} else if fix && toolCount == 1 {
			name = "write"
			arguments["filePath"] = filepath.Join(work, "solution.txt")
			arguments["content"] = token + "\n"
		} else if fix && toolCount == 2 {
			name = "bash"
			arguments["command"] = "cmp probe.txt solution.txt"
			arguments["description"] = "Compare temporary probe files"
		}
		reason := "stop"
		delta := map[string]any{"role": "assistant", "content": "phase: investigation\ntoken: " + token + "\n"}
		if fix {
			delta["content"] = "phase: fix\ntoken: " + token + "\n"
		}
		if name != "" {
			reason = "tool_calls"
			a, _ := json.Marshal(arguments)
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta map[string]any, finish any) {
			data, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("chat-%d", n), "object": "chat.completion.chunk", "created": 1, "model": "smoke", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		emit(delta, nil)
		emit(map[string]any{}, reason)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("OPENCODE_DISABLE_DEFAULT_PLUGINS", "true")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "true")
	configuration := map[string]any{"model": "studio/smoke", "small_model": "studio/smoke", "enabled_providers": []string{"studio"}, "provider": map[string]any{"studio": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Local protocol fixture", "options": map[string]any{"baseURL": server.URL + "/v1", "apiKey": "local-fixture"}, "models": map[string]any{"smoke": map[string]any{"name": "smoke", "limit": map[string]int{"context": 128000, "output": 8192}}}}}}
	data, _ := json.Marshal(configuration)
	t.Setenv("OPENCODE_CONFIG_CONTENT", string(data))
	inv := NewCodexInvestigator(nil, "")
	for _, phase := range []string{"investigation", "fix"} {
		t.Run(phase, func(t *testing.T) {
			marker := "PHASE_INVESTIGATION"
			if phase == "fix" {
				marker = "PHASE_FIX"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var events []string
			result, err := inv.ExecutePhase(ctx, "local-"+phase, BotRef{Target: "opencode", Path: work, AgentID: "studio-smoke"}, marker+": authorized temporary-file protocol test; follow tool requests and return YAML.", func(e InvestigationEvent) { events = append(events, e.Type) })
			if err != nil {
				t.Fatalf("runtime: %v; events=%v", err, events)
			}
			if !strings.Contains(result.FinalYAML, "phase: "+phase) || !strings.Contains(result.FinalYAML, token) {
				t.Fatalf("result=%q", result.FinalYAML)
			}
			if phase == "fix" {
				actual, err := os.ReadFile(filepath.Join(work, "solution.txt"))
				if err != nil || string(actual) != token+"\n" {
					t.Fatalf("real CLI edit failed: %v", err)
				}
			}
			if !strings.Contains(strings.Join(events, ","), "mcp_tool_call") {
				t.Fatal("no actual tool execution")
			}
			t.Logf("real OpenCode %s tools + final + usage=%+v PASS", phase, result.Usage)
		})
	}
}
