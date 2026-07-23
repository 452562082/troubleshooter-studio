package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const onePixelEvidencePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestUploadIncidentEvidenceImagesRegistersCurrentValidationArtifact(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "evidence-upload.db"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.workflowRoot = root
	incident := bughub.IncidentCase{
		ID: "case-upload", BugID: "bug-upload", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseNotReproduced, CycleNumber: 1, CurrentAttemptID: "attempt-upload", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(context.Background(), bughub.PhaseAttempt{
		ID: "attempt-upload", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseValidation,
		Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusFailed,
		AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := app.UploadIncidentEvidenceImages(UploadIncidentEvidenceImagesInput{
		CaseID: incident.ID, AttemptID: incident.CurrentAttemptID, ExpectedVersion: current.Version,
		Images: []IncidentEvidenceImageInput{{Name: "页面结果.png", MIMEType: "image/png", Base64Data: onePixelEvidencePNG}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ArtifactID == "" || got[0].MIMEType != "image/png" || got[0].Size == 0 {
		t.Fatalf("uploaded = %+v", got)
	}
	registered, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || len(registered) != 1 || registered[0].AttemptID != incident.CurrentAttemptID || registered[0].Kind != "user_screenshot" {
		t.Fatalf("registered = %+v err=%v", registered, err)
	}
}

func TestUploadIncidentEvidenceImagesRegistersRootCauseDisputeArtifact(t *testing.T) {
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "root-dispute-evidence.db"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.workflowRoot = root
	now := time.Now().UTC()
	incident := bughub.IncidentCase{
		ID: "case-root-evidence", BugID: "bug-root-evidence", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseWaitingFixApproval, CycleNumber: 1, CurrentAttemptID: "root-evidence", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(context.Background(), bughub.PhaseAttempt{
		ID: "root-evidence", CaseID: incident.ID, CycleNumber: 1, Phase: bughub.PhaseInvestigation,
		Status: bughub.AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "base|codex",
		InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now.Add(-time.Minute), FinishedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.UploadIncidentEvidenceImages(UploadIncidentEvidenceImagesInput{
		CaseID: incident.ID, AttemptID: incident.CurrentAttemptID, ExpectedVersion: current.Version,
		Images: []IncidentEvidenceImageInput{{Name: "反证.png", MIMEType: "image/png", Base64Data: onePixelEvidencePNG}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := store.ListEvidenceArtifacts(context.Background(), incident.ID)
	if err != nil || len(got) != 1 || len(registered) != 1 || registered[0].AttemptID != incident.CurrentAttemptID {
		t.Fatalf("uploaded=%+v registered=%+v err=%v", got, registered, err)
	}
}

func TestDecodeIncidentEvidenceImageRejectsDeclaredTypeMismatch(t *testing.T) {
	if _, err := decodeIncidentEvidenceImage(IncidentEvidenceImageInput{
		MIMEType: "image/jpeg", Base64Data: onePixelEvidencePNG,
	}); err == nil {
		t.Fatal("PNG bytes declared as JPEG were accepted")
	}
	bytes, err := base64.StdEncoding.DecodeString(onePixelEvidencePNG)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 {
		t.Fatal("test PNG is empty")
	}
}
