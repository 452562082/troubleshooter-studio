//go:build !windows

package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRegisterArtifactRejectsOversizedSourceBeforeReading(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "oversized")
	source := filepath.Join(resolvedTempDir(t), "oversized.bin")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxEvidenceArtifactBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	if _, err := RegisterArtifact(ctx, store, input); !errors.Is(err, ErrEvidenceArtifactTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegisterArtifactRejectsSourceSymlinkAndFIFO(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "source-types")
	realSource := filepath.Join(resolvedTempDir(t), "real.log")
	if err := os.WriteFile(realSource, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(resolvedTempDir(t), "source-link")
	if err := os.Symlink(realSource, symlink); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = symlink
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected source symlink rejection")
	}

	fifo := filepath.Join(resolvedTempDir(t), "source-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		writer, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			_, _ = writer.Write([]byte("safe"))
			_ = writer.Close()
		}
	}()
	input.SourcePath = fifo
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected FIFO rejection")
	}
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO writer blocked")
	}
}

func TestRegisterArtifactRejectsAncestorSymlinkEscape(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "ancestor-link")
	source := filepath.Join(resolvedTempDir(t), "source.log")
	if err := os.WriteFile(source, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := resolvedTempDir(t)
	parent := resolvedTempDir(t)
	linkedAncestor := filepath.Join(parent, "linked")
	if err := os.Symlink(outside, linkedAncestor); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	input.ArtifactsRoot = filepath.Join(linkedAncestor, "artifacts")
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected artifact ancestor symlink rejection")
	}
}

func TestRegisterArtifactRejectsDestinationHardlink(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "hardlink")
	content := []byte("safe immutable bytes")
	source := filepath.Join(resolvedTempDir(t), "source.log")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	caseDir := filepath.Join(input.ArtifactsRoot, input.CaseID)
	if err := os.MkdirAll(caseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	destination := filepath.Join(caseDir, hex.EncodeToString(digest[:]))
	outside := filepath.Join(resolvedTempDir(t), "outside-hardlink")
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected destination hardlink rejection")
	}
}

func TestRegisterArtifactDetectsMutatedCommittedContent(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "mutated")
	source := filepath.Join(resolvedTempDir(t), "source.log")
	if err := os.WriteFile(source, []byte("original immutable bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	artifact, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.PathOrReference, []byte("mutated content bytes!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected committed artifact mutation rejection")
	}
}

func TestRegisterArtifactRejectsCaseDirectorySwapBeforeCommit(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "case-swap")
	source := filepath.Join(resolvedTempDir(t), "source.log")
	if err := os.WriteFile(source, []byte("safe immutable bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	casePath := filepath.Join(input.ArtifactsRoot, input.CaseID)
	movedPath := casePath + "-moved"
	outside := resolvedTempDir(t)
	var hookErr error
	_, err := registerArtifactWithHooks(ctx, store, input, artifactHooks{BeforeCommit: func() {
		if renameErr := os.Rename(casePath, movedPath); renameErr != nil {
			hookErr = renameErr
			return
		}
		hookErr = os.Symlink(outside, casePath)
	}})
	if hookErr != nil {
		t.Fatalf("inject case swap: %v", hookErr)
	}
	if err == nil {
		t.Fatal("expected case directory swap rejection")
	}
	artifacts, listErr := store.ListEvidenceArtifacts(ctx, input.CaseID)
	if listErr != nil || len(artifacts) != 0 {
		t.Fatalf("metadata committed after swap: %+v err=%v", artifacts, listErr)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if len(entry.Name()) == 64 {
			t.Fatalf("artifact escaped to replacement directory: %s", entry.Name())
		}
	}
}

func secureArtifactFixture(t *testing.T, suffix string) (context.Context, *CaseStore, ArtifactInput) {
	t.Helper()
	ctx := context.Background()
	store := openTestCaseStore(t)
	caseID := "case-secure-" + suffix
	attemptID := "attempt-secure-" + suffix
	createTestCase(t, store, caseID)
	if err := store.CreateAttempt(ctx, validRunningAttempt(attemptID, caseID)); err != nil {
		t.Fatal(err)
	}
	return ctx, store, ArtifactInput{
		ArtifactsRoot: resolvedTempDir(t), CaseID: caseID, AttemptID: attemptID,
		Kind: "log", RedactionStatus: RedactionStatusNotRequired,
	}
}
