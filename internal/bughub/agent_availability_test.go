package bughub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentAvailability(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			// Python only encodes the fake CLI's response; no provider/network is used.
			binary := filepath.Join(t.TempDir(), "fake-agent")
			script := `#!/usr/bin/env python3
import sys, re, json
text = sys.stdin.read() if len(sys.argv) > 1 and sys.argv[1] == 'run' else sys.argv[-1]
token = re.search(r'STUDIO_READY_[a-f0-9]+', text).group(0)
TARGET = '` + target + `'
if TARGET == 'codex':
 print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':token}}))
 print(json.dumps({'type':'turn.completed'}))
elif TARGET == 'opencode':
 print(json.dumps({'type':'text','part':{'text':token}}))
 print(json.dumps({'type':'step_finish','part':{'reason':'stop'}}))
else:
 print(json.dumps({'type':'result','subtype':'success','result':token}))
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			inv := NewCodexInvestigator(nil, "")
			inv.SetBinaryForTarget(target, binary)
			got := probeAgentAvailability(context.Background(), target, inv)
			if got.Status != "ready" {
				t.Fatalf("probe failed: %+v", got)
			}
			inv.SetBinaryForTarget(target, targetCLI(t, "", "printf 'secret-private-token' >&2\nexit 1"))
			got = probeAgentAvailability(context.Background(), target, inv)
			if got.Status != "error" || strings.Contains(got.Message, "secret-private-token") {
				t.Fatalf("bad error: %+v", got)
			}
		})
	}
}
func TestAgentAvailabilityRejectsUnknownAndCancelled(t *testing.T) {
	if ProbeAgentAvailability(context.Background(), "not-a-platform").Status != "error" {
		t.Fatal("accepted unknown platform")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ProbeAgentAvailability(ctx, "codex").Status != "error" {
		t.Fatal("ignored cancellation")
	}
}

func TestAgentAvailabilityLive(t *testing.T) {
	target := os.Getenv("TSHOOT_LIVE_AVAILABILITY_TARGET")
	if target == "" {
		t.Skip("set TSHOOT_LIVE_AVAILABILITY_TARGET to run a real model connection check")
	}
	got := ProbeAgentAvailability(context.Background(), target)
	if got.Status != "ready" {
		t.Fatalf("%s: %s", target, got.Message)
	}
	t.Logf("%s: %s", target, got.Message)
}
