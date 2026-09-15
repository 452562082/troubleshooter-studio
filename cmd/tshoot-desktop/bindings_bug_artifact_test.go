package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func newBrowserBindingTestApp(t *testing.T) (*App, *bughub.CaseStore) {
	t.Helper()
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "browser-bindings.db"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.workflowRoot = root
	incident := bughub.IncidentCase{
		ID: "case-a", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseInvestigating, CycleNumber: 1, CurrentAttemptID: "attempt-a", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	attempt := bughub.PhaseAttempt{
		ID: "attempt-a", CaseID: "case-a", CycleNumber: 1, Phase: bughub.PhaseInvestigation,
		Mode: "", Status: bughub.AttemptStatusRunning,
		AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	return app, store
}

func registerPNGArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID string) bughub.EvidenceArtifact {
	t.Helper()
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	content, err := base64.StdEncoding.DecodeString(onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	return registerBrowserBindingArtifact(t, app, store, caseID, attemptID, "screenshot", content, ".png")
}

func registerTextArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID string) bughub.EvidenceArtifact {
	t.Helper()
	return registerBrowserBindingArtifact(t, app, store, caseID, attemptID, "console", []byte("safe text"), ".txt")
}

func registerBrowserBindingArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID, kind string, content []byte, suffix string) bughub.EvidenceArtifact {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source"+suffix)
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := bughub.RegisterArtifact(context.Background(), store, bughub.ArtifactInput{
		ArtifactsRoot: filepath.Join(app.workflowRoot, "artifacts"), SourcePath: source,
		CaseID: caseID, AttemptID: attemptID, Kind: kind, CapturedAt: time.Now().UTC(),
		Environment: "test", RedactionStatus: bughub.RedactionStatusNotRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestGetIncidentArtifactPreviewChecksCaseOwnershipAndPNGBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerPNGArtifact(t, app, store, "case-a", "attempt-a")

	preview, err := app.GetIncidentArtifactPreview("case-a", artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ArtifactID != artifact.ID || preview.MIMEType != "image/png" || preview.Base64Data == "" || preview.Size == 0 {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := app.GetIncidentArtifactPreview("case-b", artifact.ID); err == nil {
		t.Fatal("cross-case preview succeeded")
	}
}

func TestGetIncidentArtifactPreviewRejectsNonScreenshotAndChangedBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	text := registerTextArtifact(t, app, store, "case-a", "attempt-a")
	if _, err := app.GetIncidentArtifactPreview("case-a", text.ID); err == nil {
		t.Fatal("text preview succeeded")
	}

	screenshot := registerPNGArtifact(t, app, store, "case-a", "attempt-a")
	if err := os.WriteFile(screenshot.PathOrReference, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetIncidentArtifactPreview("case-a", screenshot.ID); err == nil {
		t.Fatal("changed artifact preview succeeded")
	}
}

func TestSaveIncidentArtifactWritesOnlyVerifiedRegisteredBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerTextArtifact(t, app, store, "case-a", "attempt-a")
	destination := filepath.Join(t.TempDir(), "saved-console.txt")
	app.workflowSaveArtifact = func(_, _ string, _ context.Context) (string, error) {
		return destination, nil
	}

	saved, err := app.SaveIncidentArtifact("case-a", artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if savedBool, ok := any(saved).(bool); !ok || !savedBool || string(content) != "safe text" {
		t.Fatalf("saved=%v (%T) content=%q", saved, saved, content)
	}
	app.workflowSaveArtifact = func(_, _ string, _ context.Context) (string, error) { return "", nil }
	cancelled, err := app.SaveIncidentArtifact("case-a", artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelledBool, ok := any(cancelled).(bool); !ok || cancelledBool {
		t.Fatalf("cancelled=%v (%T)", cancelled, cancelled)
	}
	if _, err := app.SaveIncidentArtifact("case-other", artifact.ID); err == nil {
		t.Fatal("cross-case save succeeded")
	}
}
