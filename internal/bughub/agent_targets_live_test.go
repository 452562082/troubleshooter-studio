package bughub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in only: ordinary tests never call model providers. All file
// reads and edits are restricted by the probe prompt to a temporary workspace.
func TestAgentTargetsLive(t *testing.T) {
	targets := strings.TrimSpace(os.Getenv("TSHOOT_LIVE_AGENT_TARGETS"))
	if targets == "" {
		t.Skip("set TSHOOT_LIVE_AGENT_TARGETS to explicitly run provider probes")
	}
	for _, target := range strings.Split(targets, ",") {
		target := strings.TrimSpace(target)
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			workspace := filepath.Join(t.TempDir(), "studio-smoke")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			token := "TSHOOT_" + strings.ReplaceAll(target, "-", "_") + "_PROBE_915"
			if err := os.WriteFile(filepath.Join(workspace, "probe.txt"), []byte(token+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if target == "claude-code" {
				directory := filepath.Join(workspace, ".claude", "agents")
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "studio-smoke.md"), []byte("---\nname: studio-smoke\ndescription: Temporary Studio integration test\n---\nFollow only the user's bounded temporary-file smoke test. Do not access other repositories or MCP servers.\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bot := BotRef{Target: target, Path: workspace, AgentID: "studio-smoke", Env: "test"}
			inv := NewCodexInvestigator(nil, "")
			phases := []string{"investigation", "fix"}
			if os.Getenv("TSHOOT_LIVE_PHASES") == "investigation" {
				phases = phases[:1]
			}
			for _, phase := range phases {
				t.Run(phase, func(t *testing.T) {
					prompt := "This is an authorized temporary-file integration probe, not a real incident. Stay in the current workspace. Do not use MCP servers, read other files/repositories, commit, push, deploy, or send messages. Read probe.txt with a file tool. "
					if phase == "fix" {
						prompt += "Write its exact token followed by a newline to solution.txt and run a local command that compares these two files; do not modify probe.txt. "
					}
					prompt += "Your final response MUST contain only YAML without prose or markdown fences: phase: " + phase + "\ntoken: <exact token read from probe.txt>\n"
					cmd, parser, err := inv.buildCommand(target, bot, prompt)
					if err != nil {
						t.Fatal(err)
					}
					if traceRoot := os.Getenv("TSHOOT_LIVE_TRACE_DIR"); traceRoot != "" {
						if err := os.MkdirAll(traceRoot, 0700); err != nil {
							t.Fatal(err)
						}
						trace, err := os.OpenFile(filepath.Join(traceRoot, target+"-"+phase+".jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						defer trace.Close()
						original := parser
						parser = func(line []byte) (InvestigationEvent, string, string) {
							_, _ = trace.Write(append(append([]byte{}, line...), '\n'))
							return original(line)
						}
					}
					ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
					defer cancel()
					var eventTypes []string
					result, err := inv.executePreparedPhase(ctx, target+"-"+phase, cmd, parser, func(e InvestigationEvent) { eventTypes = append(eventTypes, e.Type) })
					if err != nil {
						t.Fatalf("%s/%s runtime failed: %s", target, phase, redactLiveProbeError(err.Error()))
					}
					var report struct {
						Phase string `yaml:"phase"`
						Token string `yaml:"token"`
					}
					if err := decodeStrictYAML([]byte(result.FinalYAML), &report); err != nil {
						t.Fatalf("report not parseable (%s): %v", target, err)
					}
					if report.Phase != phase || report.Token != token {
						t.Fatalf("incorrect probe report: %+v", report)
					}
					if phase == "fix" {
						data, err := os.ReadFile(filepath.Join(workspace, "solution.txt"))
						if err != nil || string(data) != token+"\n" {
							t.Fatalf("temporary edit missing/wrong: %v", err)
						}
					}
					t.Logf("%s/%s real runtime PASS; events=%v", target, phase, eventTypes)
				})
			}
		})
	}
}

func redactLiveProbeError(message string) string {
	// Only concise diagnostics are written to test output; shared workflow tests
	// exercise full secret redaction at the persistence boundary.
	if containsSensitiveData([]byte(message)) || resetURLUserinfoPattern.MatchString(message) {
		return "runtime error contains sensitive data (suppressed)"
	}
	if len(message) > 1200 {
		return message[:1200]
	}
	return message
}
