package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

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
