package bughub

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRegisterArtifactCopiesHashesSecuresAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-artifact")
	attempt := validRunningAttempt("attempt-artifact", "case-artifact")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "network.har")
	content := []byte(`{"log":{"entries":[]}}`)
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "private-artifacts")
	input := ArtifactInput{ArtifactsRoot: root, SourcePath: source, CaseID: "case-artifact", AttemptID: attempt.ID, Kind: "har", CapturedAt: time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC), Environment: "staging", RedactionStatus: RedactionStatusNotRequired}

	artifact, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != "59844341b77e83736d4873a5c6f1d2973277cc6d0a16ba6e317b01b0d9104e1d" {
		t.Fatalf("sha=%s", artifact.SHA256)
	}
	if filepath.Dir(filepath.Dir(artifact.PathOrReference)) != root {
		t.Fatalf("path escaped root: %s", artifact.PathOrReference)
	}
	got, err := os.ReadFile(artifact.PathOrReference)
	if err != nil || string(got) != string(content) {
		t.Fatalf("content=%q err=%v", got, err)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Dir(artifact.PathOrReference), 0o700)
	assertMode(t, artifact.PathOrReference, 0o600)

	second, err := RegisterArtifact(ctx, store, input)
	if err != nil || second != artifact {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	artifacts, err := store.ListEvidenceArtifacts(ctx, "case-artifact")
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
	artifacts[0].PathOrReference = "mutated"
	again, err := store.ListEvidenceArtifacts(ctx, "case-artifact")
	if err != nil || again[0].PathOrReference != artifact.PathOrReference {
		t.Fatalf("clone safety=%+v err=%v", again, err)
	}
}

func TestRegisterArtifactRejectsSecretsTraversalAndSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-safe")
	attempt := validRunningAttempt("attempt-safe", "case-safe")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "artifacts")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("Authorization: Bearer top.secret.token"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := ArtifactInput{ArtifactsRoot: root, SourcePath: secret, CaseID: "case-safe", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, base); err == nil {
		t.Fatal("expected content secret rejection")
	}
	base.RedactionStatus = RedactionStatusRedacted
	if _, err := RegisterArtifact(ctx, store, base); err != nil {
		t.Fatalf("explicitly redacted artifact: %v", err)
	}

	clean := filepath.Join(t.TempDir(), "clean.txt")
	if err := os.WriteFile(clean, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	base = ArtifactInput{ArtifactsRoot: root, SourcePath: clean, CaseID: "../escape", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, base); err == nil {
		t.Fatal("expected traversal rejection")
	}

	outside := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "linked-artifacts")
	if err := os.Symlink(outside, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	base = ArtifactInput{ArtifactsRoot: symlinkRoot, SourcePath: clean, CaseID: "case-safe", AttemptID: attempt.ID, Kind: "text", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, base); err == nil {
		t.Fatal("expected symlink root rejection")
	}
}

func TestRegisterArtifactSecretGateCoversHeadersPasswordsAndBearerTokens(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-secrets")
	attempt := validRunningAttempt("attempt-secrets", "case-secrets")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"Authorization: Basic abc", "Cookie: session=abc", "Set-Cookie: session=abc",
		"password=hunter2", "prefix Bearer abc.def.ghi suffix",
	} {
		content := content
		t.Run(content, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "evidence.txt")
			if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			input := ArtifactInput{ArtifactsRoot: filepath.Join(t.TempDir(), "artifacts"), SourcePath: source, CaseID: "case-secrets", AttemptID: attempt.ID, Kind: "text", RedactionStatus: RedactionStatusNotRequired}
			if _, err := RegisterArtifact(ctx, store, input); err == nil {
				t.Fatalf("secret was accepted: %q", content)
			}
		})
	}

	clean := filepath.Join(t.TempDir(), "clean.txt")
	if err := os.WriteFile(clean, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{ArtifactsRoot: filepath.Join(t.TempDir(), "artifacts"), SourcePath: clean, CaseID: "case-secrets", AttemptID: attempt.ID, Kind: "text", RequestID: "Authorization: secret", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("metadata secret was accepted")
	}
}

func TestRegisterArtifactConcurrentDuplicateIsSingleRecord(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-concurrent")
	attempt := validRunningAttempt("attempt-concurrent", "case-concurrent")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "evidence.log")
	if err := os.WriteFile(source, []byte("redacted evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{ArtifactsRoot: filepath.Join(t.TempDir(), "artifacts"), SourcePath: source, CaseID: "case-concurrent", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusRedacted}

	const workers = 8
	results := make(chan EvidenceArtifact, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			artifact, err := RegisterArtifact(ctx, store, input)
			results <- artifact
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent registration: %v", err)
		}
	}
	var first EvidenceArtifact
	for result := range results {
		if first.ID == "" {
			first = result
		} else if result != first {
			t.Fatalf("non-idempotent results: first=%+v result=%+v", first, result)
		}
	}
	artifacts, err := store.ListEvidenceArtifacts(ctx, "case-concurrent")
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
}

func TestRegisterArtifactConcurrentDuplicateAcrossStoreHandles(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "private", "workflows.db")
	first, err := OpenCaseStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenCaseStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	createTestCase(t, first, "case-handles")
	attempt := validRunningAttempt("attempt-handles", "case-handles")
	if err := first.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "evidence.log")
	if err := os.WriteFile(source, []byte("redacted evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{ArtifactsRoot: filepath.Join(t.TempDir(), "artifacts"), SourcePath: source, CaseID: "case-handles", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusRedacted}

	results := make(chan EvidenceArtifact, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*CaseStore{first, second} {
		wg.Add(1)
		go func(store *CaseStore) {
			defer wg.Done()
			artifact, err := RegisterArtifact(ctx, store, input)
			results <- artifact
			errs <- err
		}(store)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent registration: %v", err)
		}
	}
	var expected EvidenceArtifact
	for artifact := range results {
		if expected.ID == "" {
			expected = artifact
		} else if artifact != expected {
			t.Fatalf("first=%+v second=%+v", expected, artifact)
		}
	}
	artifacts, err := first.ListEvidenceArtifacts(ctx, "case-handles")
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
}

func TestRegisterArtifactValidatesRelationshipBeforeWriting(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-owner")
	createTestCase(t, store, "case-other")
	attempt := validRunningAttempt("attempt-owner", "case-owner")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "clean.log")
	if err := os.WriteFile(source, []byte("clean"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "artifacts")
	input := ArtifactInput{ArtifactsRoot: root, SourcePath: source, CaseID: "case-other", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected attempt/case mismatch rejection")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("artifact root created before validation: %v", err)
	}

	input.CaseID = "case-owner"
	input.Kind = ""
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("expected empty kind rejection")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("artifact root created before metadata validation: %v", err)
	}
}

func TestRegisterArtifactNormalizesCapturedInstantForIdempotency(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-time")
	attempt := validRunningAttempt("attempt-time", "case-time")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(source, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{
		ArtifactsRoot: filepath.Join(t.TempDir(), "artifacts"), SourcePath: source,
		CaseID: "case-time", AttemptID: attempt.ID, Kind: "text",
		CapturedAt:      time.Date(2026, 7, 11, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
		RedactionStatus: RedactionStatusNotRequired,
	}
	first, err := RegisterArtifact(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	input.CapturedAt = input.CapturedAt.UTC()
	second, err := RegisterArtifact(ctx, store, input)
	if err != nil || !second.CapturedAt.Equal(first.CapturedAt) || second.ID != first.ID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode=%#o want=%#o", path, got, want)
	}
}

func validRunningAttempt(id, caseID string) PhaseAttempt {
	return PhaseAttempt{
		ID: id, CaseID: caseID, CycleNumber: 1, Phase: PhaseInvestigation,
		Status: AttemptStatusRunning, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}
}
