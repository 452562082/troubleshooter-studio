package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportLegacyRunsArchivesHistoryAndIsFileIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	started := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	runs := []InvestigationRun{
		{ID: "run-1", BugID: "bug-1", BotKey: "bot-a", Status: InvestigationSucceeded, StartedAt: started, FinishedAt: &finished, PromptPreview: "inspect login", Events: []InvestigationEvent{{At: started, Type: "message", Message: "started"}}, FinalMessage: "root cause"},
		{ID: "run-2", BugID: "bug-1", Status: InvestigationRunning, StartedAt: started.Add(time.Minute), Error: "worker vanished"},
		{ID: "run-3", BugID: "bug-2", Status: InvestigationFailed, StartedAt: started.Add(2 * time.Minute), Error: "timeout"},
		{ID: "run-4", BugID: "bug-2", Status: InvestigationStatus("malformed-phase"), StartedAt: started.Add(3 * time.Minute)},
		{ID: "run-1", BugID: "bug-1", BotKey: "bot-a", Status: InvestigationSucceeded, StartedAt: started, FinishedAt: &finished, PromptPreview: "inspect login", Events: []InvestigationEvent{{At: started, Type: "message", Message: "started"}}, FinalMessage: "root cause"},
	}
	runsPath := writeLegacyRuns(t, runs)

	result, err := ImportLegacyRuns(ctx, store, runsPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cases != 2 || result.Attempts != 4 {
		t.Fatalf("result=%+v", result)
	}
	for _, incident := range mustListCases(t, store) {
		if incident.Status != CaseLegacyArchived || incident.CurrentAttemptID != "" {
			t.Fatalf("case=%+v", incident)
		}
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 4 {
		t.Fatalf("attempts=%d", len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.Phase != PhaseLegacy {
			t.Fatalf("phase=%s", attempt.Phase)
		}
		if len(attempt.InputJSON) == 0 || len(attempt.OutputJSON) == 0 {
			t.Fatalf("empty object payload: %+v", attempt)
		}
	}
	if attempts[0].Status != AttemptStatusSucceeded {
		t.Fatalf("succeeded status=%s", attempts[0].Status)
	}
	if attempts[1].Status != AttemptStatusInterrupted {
		t.Fatalf("active legacy status=%s", attempts[1].Status)
	}
	var input struct {
		PromptPreview string `json:"prompt_preview"`
	}
	var output struct {
		Events       []InvestigationEvent `json:"events"`
		FinalMessage string               `json:"final_message"`
	}
	if err := json.Unmarshal(attempts[0].InputJSON, &input); err != nil || input.PromptPreview != "inspect login" {
		t.Fatalf("input=%s err=%v", attempts[0].InputJSON, err)
	}
	if err := json.Unmarshal(attempts[0].OutputJSON, &output); err != nil || output.FinalMessage != "root cause" || len(output.Events) != 1 {
		t.Fatalf("output=%s err=%v", attempts[0].OutputJSON, err)
	}

	second, err := ImportLegacyRuns(ctx, store, runsPath)
	if err != nil || second.Cases != 0 || second.Attempts != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := os.Stat(runsPath); err != nil {
		t.Fatalf("runs.json was removed: %v", err)
	}
}

func TestImportLegacyRunsCorruptInputDoesNotModifyStore(t *testing.T) {
	store := openTestCaseStore(t)
	createTestCase(t, store, "existing")
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(`[{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyRuns(context.Background(), store, path); err == nil {
		t.Fatal("expected corrupt legacy input error")
	}
	if cases := mustListCases(t, store); len(cases) != 1 || cases[0].ID != "existing" {
		t.Fatalf("cases=%+v", cases)
	}
}

func TestImportLegacyRunsChangedFileAddsOnlyNewHistory(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	started := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	path := writeLegacyRuns(t, []InvestigationRun{{ID: "run-old", BugID: "bug-1", Status: InvestigationSucceeded, StartedAt: started}})
	first, err := ImportLegacyRuns(ctx, store, path)
	if err != nil || first.Cases != 1 || first.Attempts != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	data, err := json.Marshal([]InvestigationRun{
		{ID: "run-old", BugID: "bug-1", Status: InvestigationSucceeded, StartedAt: started},
		{ID: "run-new", BugID: "bug-1", Status: InvestigationFailed, StartedAt: started.Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := ImportLegacyRuns(ctx, store, path)
	if err != nil || second.Cases != 0 || second.Attempts != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestImportLegacyRunsChangedExistingRunKeepsFirstArchivedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	started := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	path := writeLegacyRuns(t, []InvestigationRun{{
		ID: "run-existing", BugID: "bug-1", Status: InvestigationRunning,
		StartedAt: started, Events: []InvestigationEvent{{At: started, Type: "message", Message: "started"}},
	}})
	first, err := ImportLegacyRuns(ctx, store, path)
	if err != nil || first.Cases != 1 || first.Attempts != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	before, err := store.GetAttempt(ctx, deterministicWorkflowID("legacy-attempt:run-existing"))
	if err != nil {
		t.Fatal(err)
	}

	finished := started.Add(time.Minute)
	data, err := json.Marshal([]InvestigationRun{
		{
			ID: "run-existing", BugID: "bug-1", Status: InvestigationSucceeded,
			StartedAt: started, FinishedAt: &finished,
			Events:       []InvestigationEvent{{At: started, Type: "message", Message: "started"}},
			FinalMessage: "completed after the first import",
		},
		{ID: "run-new", BugID: "bug-1", Status: InvestigationFailed, StartedAt: finished.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := ImportLegacyRuns(ctx, store, path)
	if err != nil || second.Cases != 0 || second.Attempts != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	after, err := store.GetAttempt(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameImportedAttempt(before, after) {
		t.Fatalf("existing archived snapshot changed:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestImportLegacyRunsStillRejectsLegacyAttemptIdentityCollision(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	caseID := deterministicWorkflowID("legacy-case:bug-1")
	attemptID := deterministicWorkflowID("legacy-attempt:run-existing")
	_, err := store.importLegacyBatch(ctx, legacyImportBatch{
		MigrationKey: "test-conflicting-legacy-attempt",
		Cases: []IncidentCase{{
			ID: caseID, BugID: "bug-1", Source: "legacy-runs-json",
			Status: CaseLegacyArchived, CycleNumber: 1, Version: 1,
		}},
		Attempts: []PhaseAttempt{{
			ID: attemptID, CaseID: caseID, CycleNumber: 1, Phase: PhaseLegacy,
			Status: AttemptStatusSucceeded, InputJSON: json.RawMessage(`{}`),
			OutputJSON: json.RawMessage(`{"original_run_id":"different-run"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	path := writeLegacyRuns(t, []InvestigationRun{{
		ID: "run-existing", BugID: "bug-1", Status: InvestigationSucceeded,
	}})
	if _, err := ImportLegacyRuns(ctx, store, path); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected identity conflict, got %v", err)
	}
}

func TestImportLegacyRunsStoreConflictRollsBackWholeFile(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	conflictingID := deterministicWorkflowID("legacy-case:bug-z")
	if err := store.CreateCase(ctx, IncidentCase{ID: conflictingID, BugID: "different-bug", Status: CaseLegacyArchived, CycleNumber: 1}); err != nil {
		t.Fatal(err)
	}
	path := writeLegacyRuns(t, []InvestigationRun{
		{ID: "run-a", BugID: "bug-a", Status: InvestigationSucceeded},
		{ID: "run-z", BugID: "bug-z", Status: InvestigationSucceeded},
	})
	if _, err := ImportLegacyRuns(ctx, store, path); err == nil {
		t.Fatal("expected store identity conflict")
	}
	cases := mustListCases(t, store)
	if len(cases) != 1 || cases[0].ID != conflictingID {
		t.Fatalf("partial import persisted: %+v", cases)
	}
}

func TestImportLegacyRunsRedactsSecretsPreservesNumbersAndContinuation(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	raw := `[
		{"id":"parent","bug_id":"bug-1","status":"succeeded","prompt_preview":"Authorization: Bearer abc.def.123456789","events":[{"type":"message","message":"password=hunter2","raw":{"count":9007199254740993,"token":"raw-secret"},"meta":{"client_secret":"meta-secret","safe":"kept"}},{"type":"message","message":"{\"api_key\":\"nested-secret\"}"}],"final_message":"Cookie: session=abc","error":"access_token=error-secret"},
		{"id":"child","bug_id":"bug-1","status":"failed","continuation_of":"parent"},
		{"id":"other","bug_id":"bug-2","status":"failed","continuation_of":"parent"}
	]`
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyRuns(ctx, store, path); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]PhaseAttempt)
	for _, attempt := range attempts {
		var output map[string]any
		decoder := json.NewDecoder(bytes.NewReader(attempt.OutputJSON))
		decoder.UseNumber()
		if err := decoder.Decode(&output); err != nil {
			t.Fatal(err)
		}
		originalID, _ := output["original_run_id"].(string)
		byID[originalID] = attempt
	}
	parent := byID["parent"]
	if parent.ID == "" {
		t.Fatalf("original run IDs not retained: %+v", byID)
	}
	combined := string(parent.InputJSON) + string(parent.OutputJSON) + parent.ErrorMessage
	for _, secret := range []string{"abc.def.123456789", "hunter2", "raw-secret", "meta-secret", "nested-secret", "session=abc", "error-secret"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret %q persisted in %s", secret, combined)
		}
	}
	if !strings.Contains(combined, "[REDACTED]") || !strings.Contains(combined, "9007199254740993") || !strings.Contains(combined, `"safe":"kept"`) {
		t.Fatalf("redaction/number preservation failed: %s", combined)
	}
	child := byID["child"]
	if child.ParentAttemptID != parent.ID {
		t.Fatalf("same-bug continuation parent=%q want=%q", child.ParentAttemptID, parent.ID)
	}
	if other := byID["other"]; other.ParentAttemptID != "" {
		t.Fatalf("cross-bug continuation linked to %q", other.ParentAttemptID)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != raw {
		t.Fatalf("runs.json changed err=%v", err)
	}
}

func TestImportLegacyRunsRejectsConflictingDuplicatesBeforeRedaction(t *testing.T) {
	store := openTestCaseStore(t)
	path := filepath.Join(t.TempDir(), "runs.json")
	raw := `[
		{"id":"same","bug_id":"bug-1","status":"failed","error":"token=first-secret"},
		{"id":"same","bug_id":"bug-1","status":"failed","error":"token=second-secret"}
	]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyRuns(context.Background(), store, path); err == nil {
		t.Fatal("expected pre-redaction duplicate conflict")
	}
	if cases := mustListCases(t, store); len(cases) != 0 {
		t.Fatalf("conflicting duplicates modified store: %+v", cases)
	}
}

func TestImportLegacyRunsRedactsWholeInlineHeadersAndEventTypeButKeepsProse(t *testing.T) {
	store := openTestCaseStore(t)
	raw := `[{"id":"run-inline","bug_id":"bug-inline","status":"failed","prompt_preview":"prefix Authorization: Basic dXNlcjpwYXNz credential-tail\nUse the token: bucket algorithm","events":[{"type":"Proxy-Authorization: Negotiate TlRMTVNTUAABAAA type-tail","message":"The secret: ingredient is salt"}],"final_message":"prefix Cookie: session=abc; other=def cookie-tail\nkept next line","error":"prefix Set-Cookie: auth=xyz; Secure set-cookie-tail"}]`
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyRuns(context.Background(), store, path); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAttempts(context.Background(), AttemptFilter{})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	combined := string(attempts[0].InputJSON) + string(attempts[0].OutputJSON) + attempts[0].ErrorMessage
	for _, credentialTail := range []string{"dXNlcjpwYXNz", "credential-tail", "TlRMTVNTUAABAAA", "type-tail", "session=abc", "cookie-tail", "auth=xyz", "set-cookie-tail"} {
		if strings.Contains(combined, credentialTail) {
			t.Fatalf("credential tail %q remained in %s", credentialTail, combined)
		}
	}
	for _, prose := range []string{"Use the token: bucket algorithm", "The secret: ingredient is salt", "kept next line"} {
		if !strings.Contains(combined, prose) {
			t.Fatalf("benign prose %q was removed from %s", prose, combined)
		}
	}
}

func TestImportLegacyRunsRedactsPrefixedKeysAndCompleteQuotedValues(t *testing.T) {
	store := openTestCaseStore(t)
	run := InvestigationRun{
		ID: "run-prefixed", BugID: "bug-prefixed", Status: InvestigationFailed,
		PromptPreview: "DB_PASSWORD=\"DB-FIRST DB-SECOND \\\"DB-QUOTED\\\" DB-TAIL\"\nAWS_SECRET_ACCESS_KEY='AWS-FIRST AWS-TAIL'\nGITHUB_TOKEN=GH-CONFIG-VALUE;SAFE_SETTING=keep-me",
		Events: []InvestigationEvent{{
			Type:    "service.authorization=\"Digest TYPE-FIRST TYPE-TAIL\"",
			Message: "prefix.cookie='COOKIE-FIRST COOKIE-TAIL'",
			Raw:     map[string]any{"db_password": "JSON-DB-SECRET", "secretary": "office-manager", "password_hint": "use vault"},
			Meta:    map[string]any{"aws.secret-access-key": "META-AWS-SECRET"},
		}},
		FinalMessage: "service-client_secret=\"CLIENT-FIRST CLIENT-TAIL\"",
		Error:        "config.private-key='PRIVATE-FIRST PRIVATE-TAIL'",
	}
	path := writeLegacyRuns(t, []InvestigationRun{run})
	if _, err := ImportLegacyRuns(context.Background(), store, path); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListAttempts(context.Background(), AttemptFilter{})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	combined := string(attempts[0].InputJSON) + string(attempts[0].OutputJSON) + attempts[0].ErrorMessage
	for _, secret := range []string{
		"DB-FIRST", "DB-SECOND", "DB-QUOTED", "DB-TAIL", "AWS-FIRST", "AWS-TAIL",
		"GH-CONFIG-VALUE", "TYPE-FIRST", "TYPE-TAIL", "COOKIE-FIRST", "COOKIE-TAIL",
		"JSON-DB-SECRET", "META-AWS-SECRET", "CLIENT-FIRST", "CLIENT-TAIL",
		"PRIVATE-FIRST", "PRIVATE-TAIL",
	} {
		if strings.Contains(combined, secret) {
			t.Fatalf("credential %q remained in %s", secret, combined)
		}
	}
	for _, safe := range []string{"office-manager", "use vault", "SAFE_SETTING=keep-me"} {
		if !strings.Contains(combined, safe) {
			t.Fatalf("unrelated value %q was removed from %s", safe, combined)
		}
	}
}

func writeLegacyRuns(t *testing.T, runs []InvestigationRun) string {
	t.Helper()
	data, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustListCases(t *testing.T, store *CaseStore) []IncidentCase {
	t.Helper()
	cases, err := store.ListCases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cases
}
