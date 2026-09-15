package bughub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAllAgentTargetsReceiveStagingEnvironment(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			for _, attachments := range []bool{false, true} {
				staging := resolvedTempDir(t)
				inv := NewCodexInvestigator(nil, "")
				inv.SetBinaryForTarget(target, targetCLI(t, targetResultStream(t, target, "status: ready\n"), "test -n \"$STUDIO_EVIDENCE_STAGING_DIR\" || exit 31\nprintf 'actual tool output' > \"$STUDIO_EVIDENCE_STAGING_DIR/runtime.txt\"\n"))
				bot := BotRef{Target: target, Path: t.TempDir(), AgentID: "probe"}
				prompt := evidenceStagingPrompt(staging)
				var err error
				if attachments {
					attachment, cleanup := testPhaseScreenshotAttachment(t)
					_, err = inv.ExecutePhaseWithAttachments(context.Background(), "staging", bot, prompt, []PhaseAttachment{attachment}, nil)
					if cleanupErr := cleanup(); cleanupErr != nil {
						t.Error(cleanupErr)
					}
				} else {
					_, err = inv.ExecutePhase(context.Background(), "staging", bot, prompt, nil)
				}
				if err != nil {
					t.Fatalf("attachments=%v: %v", attachments, err)
				}
				data, err := os.ReadFile(filepath.Join(staging, "runtime.txt"))
				if err != nil || string(data) != "actual tool output" {
					t.Fatalf("evidence missing: %q %v", data, err)
				}
			}
		})
	}
}
