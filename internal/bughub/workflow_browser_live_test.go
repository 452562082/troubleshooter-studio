//go:build live_codex

package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test exercises the real Codex CLI with the same planner,
// attachment transport, permission profile, and staging path used by Studio.
// It is intentionally excluded from normal test runs because it consumes a
// live model session.
func TestLiveCodexBrowserPlanner(t *testing.T) {
	bug, bot := liveBrowserFixture(t)
	staging := t.TempDir()
	attempt := PhaseAttempt{ID: "live-browser-planner", CaseID: "live-browser-case", CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce}
	basePrompt := BuildCodexValidationPrompt(bug, bot) + evidenceStagingPrompt(staging)
	request := BrowserCoordinatorRequest{
		Attempt:    attempt,
		Bug:        bug,
		Bot:        bot,
		BasePrompt: basePrompt,
		Policy: BrowserSecurityPolicy{
			StartOrigins:       []string{"https://funhub-web-test.guadd.fun"},
			ApplicationOrigins: []string{"https://funhub-web-test.guadd.fun"},
		},
		StagingDir: staging,
	}
	executor := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "codex")
	coordinator := BrowserCoordinator{Executor: executor}
	for round := 1; round <= 3; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		result, runErr := coordinator.executeBrowserPlanner(ctx, request, browserPlannerPrompt(request, nil))
		cancel()
		if runErr != nil {
			t.Fatalf("round %d: %v", round, runErr)
		}
		plan, parseErr := ParseBrowserPlan([]byte(result.FinalYAML))
		if parseErr != nil {
			t.Fatalf("round %d parse: %v\n%s", round, parseErr, result.FinalYAML)
		}
		if validateDurableBrowserPlan(plan) != nil {
			t.Fatalf("round %d returned an invalid plan: %+v", round, plan)
		}
	}
}

func TestLiveCodexBrowserCoordinator(t *testing.T) {
	bug, bot := liveBrowserFixture(t)
	executor := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "codex")
	for round := 1; round <= 3; round++ {
		request := browserCoordinatorRequest(t)
		request.Attempt.ID = fmt.Sprintf("live-browser-coordinator-%d", round)
		request.Attempt.CaseID = fmt.Sprintf("live-browser-case-%d", round)
		request.Bug = bug
		request.Bot = bot
		request.BasePrompt = BuildCodexValidationPrompt(bug, bot) + evidenceStagingPrompt(request.StagingDir)
		request.Policy = BrowserSecurityPolicy{
			AllowedOrigins:     []string{bug.FrontendURL},
			StartOrigins:       []string{bug.FrontendURL},
			ApplicationOrigins: []string{bug.FrontendURL},
		}
		finalScreenshot, err := os.ReadFile(bug.Attachments[0].LocalPath)
		if err != nil {
			t.Fatal(err)
		}
		browserResult, frozen := liveFrozenBrowserResult(t, finalScreenshot)
		request.FreezeArtifacts = frozen
		verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{browserResult}}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		result, runErr := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(ctx, request)
		cancel()
		if runErr != nil || result.ErrorCode != "" {
			t.Fatalf("round %d: result=%+v err=%v", round, result, runErr)
		}
		parsed, parseErr := ParsePhaseResult(request.Attempt, []byte(result.FinalYAML))
		if parseErr != nil {
			t.Fatalf("round %d parse: %v\n%s", round, parseErr, result.FinalYAML)
		}
		if parsed.Outcome != PhaseOutcomeReproduced && parsed.Outcome != PhaseOutcomeNotReproduced && parsed.Outcome != PhaseOutcomeNeedsEvidence {
			t.Fatalf("round %d returned unexpected outcome %s", round, parsed.Outcome)
		}
	}
}

func liveBrowserFixture(t *testing.T) (Bug, BotRef) {
	t.Helper()
	bugStorePath := os.Getenv("TSHOOT_LIVE_BUG_STORE")
	bugID := os.Getenv("TSHOOT_LIVE_BUG_ID")
	selectedPath := os.Getenv("TSHOOT_LIVE_BOT_PATH")
	frontendURL := strings.TrimRight(os.Getenv("TSHOOT_LIVE_FRONTEND_URL"), "/")
	if bugStorePath == "" || bugID == "" || selectedPath == "" || frontendURL == "" {
		t.Skip("TSHOOT_LIVE_BUG_STORE, TSHOOT_LIVE_BUG_ID, TSHOOT_LIVE_BOT_PATH, and TSHOOT_LIVE_FRONTEND_URL are required")
	}
	data, err := os.ReadFile(bugStorePath)
	if err != nil {
		t.Fatal(err)
	}
	var bugs []Bug
	if err := json.Unmarshal(data, &bugs); err != nil {
		t.Fatal(err)
	}
	var bug Bug
	for _, candidate := range bugs {
		if candidate.ID == bugID {
			bug = candidate
			break
		}
	}
	if bug.ID == "" || len(bug.Attachments) == 0 {
		t.Fatal("live Bug or its attachments were not found")
	}
	systemID := strings.TrimSpace(os.Getenv("TSHOOT_LIVE_SYSTEM_ID"))
	if systemID == "" {
		systemID = "base"
	}
	bug.Env, bug.BotEnv, bug.SystemID, bug.FrontendURL = "test", "test", systemID, frontendURL
	selected := BotRef{
		Key:      selectedPath + "|codex",
		SystemID: systemID,
		Target:   "codex",
		Path:     selectedPath,
		Env:      "test",
		InternalAgents: []BotInternalAgent{
			{ID: systemID + "-troubleshooter", Role: "troubleshooter"},
			{ID: systemID + "-validator", Role: "validator"},
		},
	}
	bot, err := ExecutionBotForPhase(PhaseValidation, selected)
	if err != nil {
		t.Fatal(err)
	}
	return bug, bot
}

func liveFrozenBrowserResult(t *testing.T, finalScreenshot []byte) (BrowserVerificationResult, func(context.Context, []BrowserArtifactReference) ([]browserFrozenArtifact, error)) {
	t.Helper()
	contents := map[string][]byte{
		"screenshot":      finalScreenshot,
		"network":         []byte(`[{"method":"GET","url":"https://funhub-web-test.guadd.fun/","status":200,"duration_ms":10,"content_type":"text/html","content_length":100,"request_id":"","trace_id":""}]`),
		"console":         []byte(`{"type":"log","text":"live coordinator fixture","timestamp":"2026-07-19T05:00:00Z"}` + "\n"),
		"browser_actions": []byte(`[{"id":"capture-final","action":"screenshot","locator_kind":"","started_at":"2026-07-19T05:00:00Z","duration_ms":10,"result":"completed","error_code":""}]`),
	}
	paths := map[string]string{"screenshot": "browser/final.png", "network": "browser/network.json", "console": "browser/console.jsonl", "browser_actions": "browser/actions.json"}
	result := BrowserVerificationResult{Status: "completed", FinalURL: "https://funhub-web-test.guadd.fun/", Title: "Live fixture", FinalScreenshotPath: paths["screenshot"]}
	for _, kind := range []string{"screenshot", "network", "console", "browser_actions"} {
		content := contents[kind]
		result.Artifacts = append(result.Artifacts, BrowserArtifactReference{Kind: kind, Path: paths[kind], SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), Size: int64(len(content)), Environment: "test"})
	}
	frozenRoot := t.TempDir()
	freeze := func(_ context.Context, references []BrowserArtifactReference) ([]browserFrozenArtifact, error) {
		frozen := make([]browserFrozenArtifact, 0, len(references))
		for _, reference := range references {
			content, ok := contents[reference.Kind]
			if !ok {
				return nil, fmt.Errorf("unsupported live artifact kind %s", reference.Kind)
			}
			path := filepath.Join(frozenRoot, reference.SHA256)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return nil, err
			}
			frozen = append(frozen, browserFrozenArtifact{ReferencePath: reference.Path, Kind: reference.Kind, SHA256: reference.SHA256, Size: reference.Size, PathOrReference: path, Content: append([]byte(nil), content...)})
		}
		return frozen, nil
	}
	return result, freeze
}
