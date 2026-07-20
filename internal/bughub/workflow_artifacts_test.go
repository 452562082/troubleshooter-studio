package bughub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	root := filepath.Join(resolvedTempDir(t), "private-artifacts")
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

func TestRegisterArtifactBytesCopiesAndPublishesHostEvidence(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-host-evidence")
	attempt := validRunningAttempt("attempt-host-evidence", "case-host-evidence")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	content := []byte("safe uploaded evidence")
	root := filepath.Join(resolvedTempDir(t), "host-evidence")
	input := ArtifactInput{
		ArtifactsRoot: root, CaseID: attempt.CaseID, AttemptID: attempt.ID,
		Kind: "user_screenshot", Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}

	artifact, err := RegisterArtifactBytes(ctx, store, input, content)
	if err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	stored, err := ReadEvidenceArtifactFromRoot(ctx, store, root, attempt.CaseID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Content) != "safe uploaded evidence" || artifact.Kind != "user_screenshot" {
		t.Fatalf("stored = %+v content=%q", artifact, stored.Content)
	}
	second, err := RegisterArtifactBytes(ctx, store, input, []byte("safe uploaded evidence"))
	if err != nil || second.ID != artifact.ID {
		t.Fatalf("idempotent artifact = %+v err=%v", second, err)
	}
}

func TestReadEvidenceArtifactChecksCaseOwnershipAndRegisteredDigest(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-read-artifact")
	attempt := validRunningAttempt("attempt-read-artifact", "case-read-artifact")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "screenshot.png")
	content := []byte("registered screenshot bytes")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := RegisterArtifact(ctx, store, ArtifactInput{
		ArtifactsRoot: filepath.Join(resolvedTempDir(t), "read-artifacts"),
		SourcePath:    source, CaseID: attempt.CaseID, AttemptID: attempt.ID,
		Kind: "screenshot", RedactionStatus: RedactionStatusNotRequired,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadEvidenceArtifact(ctx, store, attempt.CaseID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Artifact != artifact || string(got.Content) != string(content) {
		t.Fatalf("content = %+v", got)
	}
	if _, err := ReadEvidenceArtifact(ctx, store, "case-other", artifact.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-case read error = %v", err)
	}
	if err := os.WriteFile(artifact.PathOrReference, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvidenceArtifact(ctx, store, attempt.CaseID, artifact.ID); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("changed artifact error = %v", err)
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
	root := filepath.Join(resolvedTempDir(t), "artifacts")
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
	symlinkRoot := filepath.Join(resolvedTempDir(t), "linked-artifacts")
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
			input := ArtifactInput{ArtifactsRoot: filepath.Join(resolvedTempDir(t), "artifacts"), SourcePath: source, CaseID: "case-secrets", AttemptID: attempt.ID, Kind: "text", RedactionStatus: RedactionStatusNotRequired}
			if _, err := RegisterArtifact(ctx, store, input); err == nil {
				t.Fatalf("secret was accepted: %q", content)
			}
		})
	}

	clean := filepath.Join(t.TempDir(), "clean.txt")
	if err := os.WriteFile(clean, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{ArtifactsRoot: filepath.Join(resolvedTempDir(t), "artifacts"), SourcePath: clean, CaseID: "case-secrets", AttemptID: attempt.ID, Kind: "text", RequestID: "Authorization: secret", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, input); err == nil {
		t.Fatal("metadata secret was accepted")
	}
}

func TestRegisterArtifactSecretGateStructuredPositiveAndNegativeCases(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-structured-secrets")
	attempt := validRunningAttempt("attempt-structured-secrets", "case-structured-secrets")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	positives := []string{
		`{"access_token":"abc123456789"}`,
		`{"nested":{"client_secret":"actual-value"}}`,
		"API_KEY=key-123456789\n",
		"passwd: hunter2\n",
		"access-key = AKIAIOSFODNN7EXAMPLE\n",
		"GET /callback?access_token=query-secret-123 HTTP/1.1\n",
		"github_pat_11AA22BB33CC44DD55EE66FF77GG88HH\n",
		"AKIAIOSFODNN7EXAMPLE\n",
		"-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n",
	}
	for index, content := range positives {
		source := filepath.Join(t.TempDir(), fmt.Sprintf("positive-%d.txt", index))
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-structured-secrets", AttemptID: attempt.ID, Kind: "text", RedactionStatus: RedactionStatusNotRequired}
		if _, err := RegisterArtifact(ctx, store, input); err == nil {
			t.Fatalf("secret was accepted: %q", content)
		}
	}

	negatives := []string{
		"Bearer authentication is configured by the operator.",
		"Use the token: bucket algorithm.",
		"Use the token: bucket2 algorithm.",
		"The secret: ingredient is salt.",
		"The password field is documented here.",
		`{"token":""}`,
		`{"secret":false}`,
		`{"access_key":[]}`,
		`{"private_key":"[REDACTED]"}`,
	}
	for index, content := range negatives {
		source := filepath.Join(t.TempDir(), fmt.Sprintf("negative-%d.txt", index))
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-structured-secrets", AttemptID: attempt.ID, Kind: fmt.Sprintf("negative-%d", index), RedactionStatus: RedactionStatusNotRequired}
		if _, err := RegisterArtifact(ctx, store, input); err != nil {
			t.Fatalf("benign prose rejected %q: %v", content, err)
		}
	}

	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "password=filename.txt")
	if err := os.WriteFile(source, []byte("safe evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-structured-secrets", AttemptID: attempt.ID, Kind: "safe-filename", RedactionStatus: RedactionStatusNotRequired}
	if _, err := RegisterArtifact(ctx, store, input); err != nil {
		t.Fatalf("keyword filename rejected: %v", err)
	}
}

func TestRegisterArtifactSecretGateRejectsInlineCredentialHeadersAndStrongGenericTokens(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-inline-headers")
	attempt := validRunningAttempt("attempt-inline-headers", "case-inline-headers")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	positives := []string{
		"request Authorization: Basic dXNlcjpwYXNz trailing-credential\nnext line",
		"logged \"Authorization\": Digest username=admin,response=abcdef tail\n",
		"proxy Proxy-Authorization: Negotiate TlRMTVNTUAABAAA tail\n",
		"response Set-Cookie: session=abc; HttpOnly\n",
		"request Cookie: session=abc; other=def\n",
		"token: eyJhbGciOiJIUzI1NiJ9.payload.signature\n",
		"secret: github_pat_11AA22BB33CC44DD55EE66FF77GG88HH\n",
	}
	for index, content := range positives {
		source := filepath.Join(t.TempDir(), fmt.Sprintf("inline-%d.txt", index))
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-inline-headers", AttemptID: attempt.ID, Kind: fmt.Sprintf("inline-%d", index), RedactionStatus: RedactionStatusNotRequired}
		if _, err := RegisterArtifact(ctx, store, input); err == nil {
			t.Fatalf("credential was accepted: %q", content)
		}
	}
}

func TestRegisterArtifactSecretGateNormalizesPrefixedKeysAndQuotedValues(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-prefixed-keys")
	attempt := validRunningAttempt("attempt-prefixed-keys", "case-prefixed-keys")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	positives := []string{
		`DB_PASSWORD="db value with spaces and \"escaped quote\" tail"`,
		`AWS_SECRET_ACCESS_KEY='aws value with spaces tail'`,
		`GITHUB_TOKEN=short-config-value`,
		`service.api-key="api key value"`,
		`service_client_secret='client secret value'`,
		`proxy.authorization="Digest username=admin response=abcdef"`,
		`http_cookie='session=abc; other=def'`,
		`{"db_password":"json-secret","nested":{"aws.secret-access-key":"aws-json-secret"}}`,
	}
	for index, content := range positives {
		source := filepath.Join(t.TempDir(), fmt.Sprintf("prefixed-positive-%d.txt", index))
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-prefixed-keys", AttemptID: attempt.ID, Kind: fmt.Sprintf("positive-%d", index), RedactionStatus: RedactionStatusNotRequired}
		if _, err := RegisterArtifact(ctx, store, input); err == nil {
			t.Fatalf("prefixed credential was accepted: %q", content)
		}
	}

	negatives := []string{
		`SECRETARY=office-manager`,
		`PASSWORD_HINT="use the company vault"`,
		`{"secretary":"office-manager","password_hint":"use vault"}`,
	}
	for index, content := range negatives {
		source := filepath.Join(t.TempDir(), fmt.Sprintf("prefixed-negative-%d.txt", index))
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		input := ArtifactInput{ArtifactsRoot: resolvedTempDir(t), SourcePath: source, CaseID: "case-prefixed-keys", AttemptID: attempt.ID, Kind: fmt.Sprintf("negative-%d", index), RedactionStatus: RedactionStatusNotRequired}
		if _, err := RegisterArtifact(ctx, store, input); err != nil {
			t.Fatalf("unrelated key was rejected %q: %v", content, err)
		}
	}
}

func TestRegisterArtifactContentAddressIgnoresExtensionAndConcurrentRoot(t *testing.T) {
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
	createTestCase(t, first, "case-content-address")
	attempt := validRunningAttempt("attempt-content-address", "case-content-address")
	if err := first.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	contents := []byte("same immutable evidence")
	firstSource := filepath.Join(t.TempDir(), "evidence.log")
	secondSource := filepath.Join(t.TempDir(), "evidence.har")
	for _, source := range []string{firstSource, secondSource} {
		if err := os.WriteFile(source, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstRoot := resolvedTempDir(t)
	secondRoot := resolvedTempDir(t)
	inputs := []ArtifactInput{
		{ArtifactsRoot: firstRoot, SourcePath: firstSource, CaseID: "case-content-address", AttemptID: attempt.ID, Kind: "evidence", RedactionStatus: RedactionStatusNotRequired},
		{ArtifactsRoot: secondRoot, SourcePath: secondSource, CaseID: "case-content-address", AttemptID: attempt.ID, Kind: "evidence", RedactionStatus: RedactionStatusNotRequired},
	}
	results := make(chan EvidenceArtifact, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index, store := range []*CaseStore{first, second} {
		wg.Add(1)
		go func(store *CaseStore, input ArtifactInput) {
			defer wg.Done()
			artifact, err := RegisterArtifact(ctx, store, input)
			results <- artifact
			errs <- err
		}(store, inputs[index])
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent different-root registration: %v", err)
		}
	}
	var committed EvidenceArtifact
	for artifact := range results {
		if committed.ID == "" {
			committed = artifact
		} else if artifact != committed {
			t.Fatalf("results differ: %+v %+v", committed, artifact)
		}
	}
	if filepath.Ext(committed.PathOrReference) != "" {
		t.Fatalf("content address contains source extension: %s", committed.PathOrReference)
	}
	for _, root := range []string{firstRoot, secondRoot} {
		matches, err := filepath.Glob(filepath.Join(root, "case-content-address", "*"))
		if err != nil {
			t.Fatal(err)
		}
		if committed.PathOrReference == filepath.Join(root, "case-content-address", filepath.Base(committed.PathOrReference)) {
			if len(matches) != 1 {
				t.Fatalf("committed root files=%v", matches)
			}
		} else if len(matches) != 0 {
			t.Fatalf("losing root left orphan=%v", matches)
		}
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
	input := ArtifactInput{ArtifactsRoot: filepath.Join(resolvedTempDir(t), "artifacts"), SourcePath: source, CaseID: "case-concurrent", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusRedacted}

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
	input := ArtifactInput{ArtifactsRoot: filepath.Join(resolvedTempDir(t), "artifacts"), SourcePath: source, CaseID: "case-handles", AttemptID: attempt.ID, Kind: "log", RedactionStatus: RedactionStatusRedacted}

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
	root := filepath.Join(resolvedTempDir(t), "artifacts")
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
		ArtifactsRoot: filepath.Join(resolvedTempDir(t), "artifacts"), SourcePath: source,
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

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func validRunningAttempt(id, caseID string) PhaseAttempt {
	return PhaseAttempt{
		ID: id, CaseID: caseID, CycleNumber: 1, Phase: PhaseInvestigation,
		Status: AttemptStatusRunning, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}
}
