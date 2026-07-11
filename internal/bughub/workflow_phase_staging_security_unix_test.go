//go:build !windows

package bughub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureAttemptStagedArtifactRejectsTraversalSymlinkAndWrongAttempt(t *testing.T) {
	root := filepath.Join(resolvedTempDir(t), "staging-security")
	first, err := createAttemptEvidenceStaging(root, "attempt-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := createAttemptEvidenceStaging(root, "attempt-second")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(first, "final-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(first, "ancestor-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "wrong.txt"), []byte("wrong attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"traversal":        "../attempt-second/wrong.txt",
		"absolute outside": outside,
		"final symlink":    "final-link",
		"ancestor symlink": "ancestor-link/outside.txt",
		"wrong attempt":    filepath.Join("..", filepath.Base(second), "wrong.txt"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := captureAttemptStagedArtifact(first, path); err == nil {
				t.Fatalf("accepted unsafe staged evidence %q", path)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(first, "current.txt"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if captured, err := captureAttemptStagedArtifact(first, "current.txt"); err != nil || string(captured.Content) != "current" || captured.CapturedAt.IsZero() {
		t.Fatalf("current staged evidence = %+v, err=%v", captured, err)
	}
}

func TestAttemptEvidenceStagingDescriptorSurvivesDirectoryReplacement(t *testing.T) {
	root := filepath.Join(resolvedTempDir(t), "staging-replacement")
	staging, err := openAttemptEvidenceStaging(root, "attempt-owned")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	if err := os.WriteFile(filepath.Join(staging.Path(), "proof.txt"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := staging.Path() + "-moved"
	if err := os.Rename(staging.Path(), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(staging.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging.Path(), "proof.txt"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured, err := staging.Capture("proof.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(captured.Content) != "owned" {
		t.Fatalf("captured replacement directory bytes %q", captured.Content)
	}
}
