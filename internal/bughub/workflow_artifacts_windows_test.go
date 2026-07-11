//go:build windows

package bughub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterArtifactFailsClosedWithoutWindowsSecureStore(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-windows-unsupported")
	attempt := validRunningAttempt("attempt-windows-unsupported", "case-windows-unsupported")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "must-not-exist")
	input := ArtifactInput{
		ArtifactsRoot: root,
		SourcePath:    filepath.Join(t.TempDir(), "source-does-not-exist"),
		CaseID:        "case-windows-unsupported", AttemptID: attempt.ID, Kind: "log",
		RedactionStatus: RedactionStatusNotRequired,
	}
	if _, err := RegisterArtifact(ctx, store, input); !errors.Is(err, ErrSecureArtifactStoreUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("artifact root was created: %v", err)
	}
	artifacts, err := store.ListEvidenceArtifacts(ctx, input.CaseID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
}
