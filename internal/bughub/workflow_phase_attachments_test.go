package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPhaseScreenshotAttachment(t *testing.T) (PhaseAttachment, func() error) {
	t.Helper()
	content := append([]byte(nil), browserPNGSignature...)
	content = append(content, []byte("rendered-browser-evidence")...)
	path, cleanup, err := createBrowserEvaluatorScreenshotView(content)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return PhaseAttachment{
		Kind: "screenshot", MIMEType: "image/png", Path: path,
		SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)),
	}, cleanup
}

func TestTargetCommandsTransportBrowserScreenshot(t *testing.T) {
	attachment, cleanup := testPhaseScreenshotAttachment(t)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	}()

	workspace := t.TempDir()
	codex, err := buildCodexExecCommand("codex", workspace, "evaluate", []string{attachment.Path})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, "\n")
	if !strings.Contains(joined, "--image\n"+attachment.Path) || strings.Contains(codex.Args[len(codex.Args)-1], attachment.Path) {
		t.Fatalf("Codex screenshot transport args = %#v", codex.Args)
	}
	if got := codex.Args[len(codex.Args)-2]; got != "--" {
		t.Fatalf("Codex prompt delimiter = %q, want --; args = %#v", got, codex.Args)
	}

	agentPath := filepath.Join(workspace, "base-validator.md")
	if err := os.WriteFile(agentPath, []byte("# validator"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudePrompt := "evaluate" + phaseAttachmentPrompt([]string{attachment.Path})
	claude, err := buildClaudeInvestigationCommand("claude", workspace, agentPath, claudePrompt, []string{filepath.Dir(attachment.Path)})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(claude.Args, "\n")
	if !strings.Contains(joined, "--add-dir\n"+filepath.Dir(attachment.Path)) || !strings.Contains(claude.Args[len(claude.Args)-1], attachment.Path) {
		t.Fatalf("Claude screenshot transport args = %#v", claude.Args)
	}
	if got := claude.Args[len(claude.Args)-2]; got != "--" {
		t.Fatalf("Claude prompt delimiter = %q, want --; args = %#v", got, claude.Args)
	}
}

func TestClaudeAttachmentPhaseExecutesPromptAfterVariadicDirectoryDelimiter(t *testing.T) {
	attachment, cleanup := testPhaseScreenshotAttachment(t)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	}()

	workspace := t.TempDir()
	agentPath := filepath.Join(workspace, "base-validator.md")
	if err := os.WriteFile(agentPath, []byte("# validator"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
previous=""
attachment_dir=""
delimiter=""
last=""
for argument in "$@"; do
  if [ "$previous" = "--add-dir" ]; then attachment_dir="$argument"; fi
  if [ "$argument" = "--" ]; then delimiter="yes"; fi
  previous="$argument"
  last="$argument"
done
[ "$attachment_dir" = "` + filepath.Dir(attachment.Path) + `" ] || exit 21
[ "$delimiter" = "yes" ] || exit 22
case "$last" in
  *Host\ evidence\ attachment\ instructions:*tshoot-browser-evaluator-*) ;;
  *) exit 23 ;;
esac
printf '%s\n' '{"type":"result","subtype":"success","result":"verification_status: not_reproduced\nenvironment: test\nobserved_behavior: page rendered\nexpected_behavior: page rendered\nevidence: []\ngaps: []"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	investigator := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "codex")
	investigator.SetBinaryForTarget("claude-code", bin)
	result, err := investigator.ExecutePhaseWithAttachments(
		context.Background(),
		"claude-attachment",
		BotRef{Target: "claude-code", Path: agentPath, AgentID: "base-validator"},
		"evaluate",
		[]PhaseAttachment{attachment},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalYAML, "verification_status: not_reproduced") {
		t.Fatalf("result = %+v", result)
	}
}

func TestOpenClawAttachmentUsesAndCleansWorkspaceView(t *testing.T) {
	attachment, cleanup := testPhaseScreenshotAttachment(t)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	}()
	workspace := t.TempDir()
	bin := filepath.Join(t.TempDir(), "openclaw")
	script := `#!/bin/sh
case "$*" in
  *Host\ evidence\ attachment\ instructions:*tshoot-browser-evaluator-*) ;;
  *) exit 17 ;;
esac
printf '%s\n' '{"ok":true,"reply":"verification_status: not_reproduced\nenvironment: test\nobserved_behavior: page rendered\nexpected_behavior: page rendered\nevidence: []\ngaps: []"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	investigator := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "codex")
	investigator.SetBinaryForTarget("openclaw", bin)
	result, err := investigator.ExecutePhaseWithAttachments(context.Background(), "openclaw-attachment", BotRef{Target: "openclaw", Path: workspace, AgentID: "base"}, "evaluate", []PhaseAttachment{attachment}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalYAML, "verification_status: not_reproduced") {
		t.Fatalf("result = %+v", result)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, ".tshoot-browser-evaluator-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("OpenClaw attachment view leaked: matches=%v err=%v", matches, err)
	}
}

type pathEchoExecutor struct{}

func (*pathEchoExecutor) ExecutePhase(context.Context, string, BotRef, string, func(InvestigationEvent)) (PhaseExecutionResult, error) {
	return PhaseExecutionResult{FinalYAML: validBrowserPlanYAML()}, nil
}

func (*pathEchoExecutor) ExecutePhaseWithAttachments(_ context.Context, _ string, _ BotRef, _ string, attachments []PhaseAttachment, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
	return PhaseExecutionResult{FinalYAML: "verification_status: not_reproduced\nenvironment: test\nobserved_behavior: inspected " + attachments[0].Path + "\nexpected_behavior: safe\nevidence: []\ngaps: []\n"}, nil
}

func (*pathEchoExecutor) CancelPhase(context.Context, string) error { return nil }

func TestBrowserCoordinatorRejectsEphemeralScreenshotPathInEvaluatorOutput(t *testing.T) {
	request := browserCoordinatorRequest(t)
	coordinator := BrowserCoordinator{
		Executor: &pathEchoExecutor{},
		Verifier: &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/final.png")}},
	}
	result, err := coordinator.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_evaluator_result_invalid" || result.FinalYAML != "" {
		t.Fatalf("result = %+v", result)
	}
}
