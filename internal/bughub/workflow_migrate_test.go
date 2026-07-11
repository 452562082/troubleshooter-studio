package bughub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
