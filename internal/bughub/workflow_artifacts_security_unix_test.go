//go:build !windows

package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRegisterArtifactUsesBoundedStorageDirectoryForLongCaseID(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	caseID := "case-reset-" + strings.Repeat("nested-reset-", 28)
	attemptID := "attempt-long-case-artifact"
	createTestCase(t, store, caseID)
	if err := store.CreateAttempt(ctx, validRunningAttempt(attemptID, caseID)); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolvedTempDir(t), "long-case-artifacts")
	content := []byte("safe browser evidence for a legacy long Case ID")
	artifact, err := RegisterArtifactBytes(ctx, store, ArtifactInput{
		ArtifactsRoot: root, CaseID: caseID, AttemptID: attemptID,
		Kind: "screenshot", RedactionStatus: RedactionStatusNotRequired,
	}, content)
	if err != nil {
		t.Fatal(err)
	}
	storageComponent := filepath.Base(filepath.Dir(artifact.PathOrReference))
	if storageComponent == caseID || len([]byte(storageComponent)) > 255 {
		t.Fatalf("storage component=%q bytes=%d", storageComponent, len([]byte(storageComponent)))
	}
	read, err := ReadEvidenceArtifactFromRoot(ctx, store, root, caseID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Content) != string(content) {
		t.Fatalf("content=%q", read.Content)
	}
}

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
	// If the non-blocking reader rejected and closed before the writer goroutine
	// was scheduled, briefly reopen the FIFO so the writer can observe a reader
	// and terminate instead of leaking across the rest of the package tests.
	drain, _ := os.OpenFile(fifo, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if drain != nil {
		defer drain.Close()
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

func TestReadEvidenceArtifactRejectsParentDirectorySymlinkReplacement(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "read-parent-link")
	content := []byte("safe registered browser screenshot")
	source := filepath.Join(resolvedTempDir(t), "read-parent-link-source.png")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	input.Kind = "screenshot"
	artifact, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	caseDir := filepath.Dir(artifact.PathOrReference)
	moved := caseDir + "-moved"
	if err := os.Rename(caseDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, caseDir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidenceArtifact(ctx, store, input.CaseID, artifact.ID); err == nil {
		t.Fatal("registered artifact read followed a replaced parent-directory symlink")
	}
}

func TestReadEvidenceArtifactRejectsHardLinkReplacement(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "read-hardlink")
	content := []byte("safe registered network evidence")
	source := filepath.Join(resolvedTempDir(t), "read-hardlink-source.json")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	artifact, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(resolvedTempDir(t), "read-hardlink-outside.json")
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact.PathOrReference); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, artifact.PathOrReference); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidenceArtifact(ctx, store, input.CaseID, artifact.ID); err == nil {
		t.Fatal("registered artifact read accepted a hard-link replacement")
	}
}

func TestReadEvidenceArtifactFromRootRequiresRegisteredStoreOwnership(t *testing.T) {
	ctx, store, input := secureArtifactFixture(t, "read-root-ownership")
	source := filepath.Join(resolvedTempDir(t), "read-root-source.txt")
	if err := os.WriteFile(source, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.SourcePath = source
	artifact, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidenceArtifactFromRoot(ctx, store, input.ArtifactsRoot, input.CaseID, artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidenceArtifactFromRoot(ctx, store, resolvedTempDir(t), input.CaseID, artifact.ID); err == nil {
		t.Fatal("registered artifact read accepted a different artifact-store root")
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
