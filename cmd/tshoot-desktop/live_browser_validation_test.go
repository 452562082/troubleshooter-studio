package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type liveBrowserDiagnosticController struct {
	incidentBrowserController
	t *testing.T
}

func (c liveBrowserDiagnosticController) Execute(ctx context.Context, request bughub.BrowserVerificationRequest) (bughub.BrowserVerificationResult, error) {
	result, err := c.incidentBrowserController.Execute(ctx, request)
	c.t.Logf("host verifier: status=%s code=%s failed=%s err=%v", result.Status, result.ErrorCode, result.FailedActionID, err)
	return result, err
}

// TestLiveBrowserValidationReset is an opt-in local acceptance check. It uses
// the same desktop bindings, durable store, Codex executor, policy resolver,
// and browser verifier as the packaged app. It is skipped in normal test runs.
func TestLiveBrowserValidationReset(t *testing.T) {
	caseID := os.Getenv("TSHOOT_LIVE_CASE_ID")
	if caseID == "" {
		t.Skip("set TSHOOT_LIVE_CASE_ID to run the desktop acceptance check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	app := &App{templateRoot: resolveTemplateDir(), workflowRoot: bughub.DefaultRoot()}
	app.setRuntimeContext(ctx)
	app.workflowEmit = func(string, any) {}
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(projectRoot, "dist", "TroubleshooterStudio.app", "Contents", "Resources", "browser-runtime", browserverify.BrowserRuntimeVersion)
	runtimeManager := browserverify.NewRuntimeManagerWithBundle(app.workflowRoot, bundle, nil)
	hostVerifier := browserverify.NewHostVerifier(runtimeManager, nil, net.DefaultResolver)
	hostVerifier.SetSessionStore(browserverify.NewSessionStore(filepath.Join(app.workflowRoot, "browser-sessions"), newIncidentBrowserKeyringStore()))
	app.workflowBrowser = liveBrowserDiagnosticController{incidentBrowserController: hostVerifier, t: t}
	controller := app.incidentBrowserController()
	if err := controller.Prepare(ctx, func(progress bughub.BrowserProgress) {
		t.Logf("browser preparation: %s %s", progress.Code, progress.Message)
	}); err != nil {
		t.Fatalf("prepare browser runtime: %v", err)
	}
	if status := controller.Status(); status.State != browserverify.RuntimeReady {
		t.Fatalf("browser runtime state = %s (%s), want ready", status.State, status.ErrorCode)
	}
	app.workflowBrowserPreparationStarted = true
	app.workflowBrowserPreparationFinished = true
	if err := app.startIncidentWorkflow(ctx); err != nil {
		t.Fatalf("start workflow: %v", err)
	}
	defer func() {
		if err := app.closeIncidentWorkflow(); err != nil {
			t.Errorf("close workflow: %v", err)
		}
	}()

	store, _, err := app.workflowComponents()
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.GetCase(ctx, caseID)
	if err != nil {
		t.Fatalf("load original Case: %v", err)
	}
	now := time.Now().UnixNano()
	newCaseID := fmt.Sprintf("case-live-browser-validation-%d", now)
	outcome, err := app.ResetIncidentCaseWithWarnings(ResetIncidentCaseInput{
		CaseID: caseID, NewCaseID: newCaseID, BotKey: original.SelectedBotKey,
		BotEnvironment: original.Environment, ExpectedVersion: original.Version,
		IdempotencyKey: fmt.Sprintf("live-browser-validation:%d", now), ActorID: "codex-live-verifier",
		InputJSON: map[string]any{"reason": "live acceptance after browser worker protocol fix"},
	})
	if err != nil {
		t.Fatalf("reset Case: %v", err)
	}
	if len(outcome.Warnings) != 0 {
		t.Fatalf("reset warnings: %+v", outcome.Warnings)
	}
	t.Logf("replacement Case: %s", outcome.Case.ID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		attempts, listErr := store.ListAttempts(ctx, bughub.AttemptFilter{CaseID: newCaseID})
		if listErr != nil {
			t.Fatalf("list attempts: %v", listErr)
		}
		for _, attempt := range attempts {
			if attempt.Phase != bughub.PhaseValidation || attempt.Status == bughub.AttemptStatusRunning {
				continue
			}
			t.Logf("validation attempt: id=%s status=%s output=%s", attempt.ID, attempt.Status, attempt.OutputJSON)
			if attempt.Status != bughub.AttemptStatusSucceeded {
				t.Fatalf("validation did not succeed: status=%s output=%s", attempt.Status, attempt.OutputJSON)
			}
			artifacts, artifactErr := store.ListEvidenceArtifacts(ctx, newCaseID)
			if artifactErr != nil {
				t.Fatalf("list evidence: %v", artifactErr)
			}
			kinds := map[string]bool{}
			for _, artifact := range artifacts {
				kinds[artifact.Kind] = true
			}
			if !kinds["screenshot"] || !kinds["network"] || !kinds["browser_actions"] {
				t.Fatalf("validation evidence kinds = %v, want screenshot/network/browser_actions", kinds)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for validation: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}
