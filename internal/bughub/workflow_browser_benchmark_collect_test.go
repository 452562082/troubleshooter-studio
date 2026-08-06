package bughub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectBrowserDecisionBenchmarkFromRootUsesOnlyHostProvedFacts(t *testing.T) {
	root := t.TempDir()
	store, err := OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ordinaryFinished := now.Add(500 * time.Millisecond)
	locatorFinished := now.Add(1500 * time.Millisecond)
	gapFinished := now.Add(2500 * time.Millisecond)
	createTestCase(t, store, "case-ordinary-raw-id")
	ordinary := PhaseAttempt{
		ID: "attempt-ordinary-raw-id", CaseID: "case-ordinary-raw-id", CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded,
		InputJSON: []byte(`{}`), OutputJSON: []byte(`{"verification_status":"reproduced","evidence":[{"kind":"screenshot"}]}`),
		StartedAt: now, FinishedAt: &ordinaryFinished,
	}
	if err := store.CreateAttempt(context.Background(), ordinary); err != nil {
		t.Fatal(err)
	}
	registerBenchmarkCollectionArtifact(t, store, ordinary, "screenshot", strings.Repeat("a", 64))

	createTestCase(t, store, "case-locator-raw-id")
	locator := PhaseAttempt{
		ID: "attempt-legacy-locator", CaseID: "case-locator-raw-id", CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed,
		InputJSON: []byte(`{}`), OutputJSON: []byte(`{"error_code":"browser_locator_failed"}`),
		StartedAt: now.Add(time.Second), FinishedAt: &locatorFinished, ErrorCode: "browser_locator_failed",
	}
	if err := store.CreateAttempt(context.Background(), locator); err != nil {
		t.Fatal(err)
	}
	proof := BrowserManualReproductionGateProof{
		Version: BrowserManualReproductionGateProofVersion, Code: BrowserManualReproductionAvailableCode,
		AttemptID: "attempt-capability-gap", SceneID: "scene-gap", ScenarioContractSHA256: strings.Repeat("c", 64),
		FrontendEntryID: "admin", DecisionSHA256: strings.Repeat("d", 64),
		ExhaustedChannels: []string{"safe_exploration", "semantic_grounding", "structured_grounding"},
	}
	gapOutput, err := json.Marshal(map[string]any{"error_code": "browser_capability_gap", "manual_reproduction_gate": proof})
	if err != nil {
		t.Fatal(err)
	}
	gap := PhaseAttempt{
		ID: proof.AttemptID, CaseID: locator.CaseID, CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed,
		InputJSON: []byte(`{}`), OutputJSON: gapOutput, StartedAt: now.Add(2 * time.Second), FinishedAt: &gapFinished,
		ErrorCode: "browser_capability_gap",
	}
	if err := store.CreateAttempt(context.Background(), gap); err != nil {
		t.Fatal(err)
	}
	registerBenchmarkCollectionArtifact(t, store, gap, ManualReproductionArtifactKind, strings.Repeat("b", 64))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	projected := NewInvestigationStore(root)
	ordinaryEvent := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{
		Status: BrowserDecisionLoopConcluded, ScenarioContractSHA256: strings.Repeat("e", 64),
		Metrics: BrowserDecisionLoopMetrics{DecisionCalls: 2, ScenarioSteps: 1, ConfirmedSteps: 1, StateActions: 1},
	})
	bindBenchmarkMetricEvent(&ordinaryEvent, ordinary)
	if err := projected.Upsert(InvestigationRun{ID: ordinary.ID, BugID: "bug-ordinary", Status: InvestigationSucceeded, StartedAt: ordinary.StartedAt, Events: []InvestigationEvent{ordinaryEvent}}); err != nil {
		t.Fatal(err)
	}
	gapEvent := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{
		Status: BrowserDecisionLoopCapabilityGap, ScenarioContractSHA256: proof.ScenarioContractSHA256,
		Metrics: BrowserDecisionLoopMetrics{DecisionCalls: 1},
	})
	bindBenchmarkMetricEvent(&gapEvent, gap)
	if err := projected.Upsert(InvestigationRun{ID: gap.ID, BugID: "bug-gap", Status: InvestigationFailed, StartedAt: gap.StartedAt, Events: []InvestigationEvent{gapEvent}}); err != nil {
		t.Fatal(err)
	}

	collection, err := CollectBrowserDecisionBenchmarkFromRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if collection.CollectedSamples != 2 || collection.AutonomousAttempts != 2 || len(collection.Corpus.Samples) != 2 {
		t.Fatalf("collection=%+v", collection)
	}
	var ordinarySample, locatorSample BrowserDecisionBenchmarkSample
	for _, sample := range collection.Corpus.Samples {
		switch sample.Suite {
		case BrowserDecisionBenchmarkOrdinary:
			ordinarySample = sample
		case BrowserDecisionBenchmarkLocatorCorpus:
			locatorSample = sample
		}
	}
	if !ordinarySample.AutonomousCompleted || ordinarySample.Conclusion != "reproduced" || !ordinarySample.CurrentEvidence || ordinarySample.StateActions != 1 {
		t.Fatalf("ordinary=%+v", ordinarySample)
	}
	if locatorSample.AutonomousCompleted || !locatorSample.ManualReproductionSelected || !locatorSample.CapabilityGapRecorded || locatorSample.LocatorTriggeredManual {
		t.Fatalf("locator=%+v", locatorSample)
	}
	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{ordinary.ID, ordinary.CaseID, gap.ID, gap.CaseID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("raw workflow identity leaked: %s", secret)
		}
	}
}

func TestCollectBrowserDecisionBenchmarkSkipsLegacyIncompleteMetrics(t *testing.T) {
	root := t.TempDir()
	store, err := OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	createTestCase(t, store, "case-legacy-metric")
	attempt := PhaseAttempt{
		ID: "attempt-legacy-metric", CaseID: "case-legacy-metric", CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed,
		InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC(), ErrorCode: "browser_validator_failed",
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	event := BrowserDecisionLoopMetricEvent(BrowserDecisionLoopResult{Status: BrowserDecisionLoopRecovery, ScenarioContractSHA256: strings.Repeat("a", 64)})
	delete(event.Meta, "wrong_element_actions")
	bindBenchmarkMetricEvent(&event, attempt)
	if err := NewInvestigationStore(root).Upsert(InvestigationRun{ID: attempt.ID, BugID: "bug", Status: InvestigationFailed, Events: []InvestigationEvent{event}}); err != nil {
		t.Fatal(err)
	}
	collection, err := CollectBrowserDecisionBenchmarkFromRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if collection.CollectedSamples != 0 || collection.Skipped[BrowserBenchmarkSkipMalformedMetric] != 1 {
		t.Fatalf("collection=%+v", collection)
	}
}

func bindBenchmarkMetricEvent(event *InvestigationEvent, attempt PhaseAttempt) {
	if event.Meta == nil {
		event.Meta = make(map[string]any)
	}
	event.Meta["attempt_id"] = attempt.ID
	event.Meta["case_id"] = attempt.CaseID
}

func registerBenchmarkCollectionArtifact(t *testing.T, store *CaseStore, attempt PhaseAttempt, kind, digest string) {
	t.Helper()
	_, _, err := store.recordEvidenceArtifact(context.Background(), EvidenceArtifact{
		ID: "artifact-" + kind + "-" + attempt.ID, CaseID: attempt.CaseID, AttemptID: attempt.ID,
		Kind: kind, PathOrReference: "artifacts/redacted.bin", SHA256: digest, CapturedAt: time.Now().UTC(),
		Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenCaseStoreReadOnlyDoesNotMutateOrMigrate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	createTestCase(t, store, "case-read-only")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenCaseStoreReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if cases, err := readOnly.ListCases(context.Background()); err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	if err := readOnly.CreateCase(context.Background(), IncidentCase{ID: "forbidden", BugID: "bug", Status: CasePendingValidation, CycleNumber: 1, Version: 1}); err == nil {
		t.Fatal("read-only store accepted a write")
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("read-only open mutated database metadata: before=%+v after=%+v", before, after)
	}
}

func TestOpenCaseStoreReadOnlySupportsCompatibleV11WithoutMigration(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflows.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	createTestCase(t, store, "case-compatible-v11")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db := openRawWorkflowDB(t, path)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE browser_decision_steps`,
		`ALTER TABLE validation_recipes DROP COLUMN autonomous_recipe_sha256`,
		`ALTER TABLE validation_recipes DROP COLUMN autonomous_recipe_json`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	detail, err := json.Marshal(workflowSchemaMigrationDetail{Version: 11, Fingerprint: fingerprint})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE schema_migrations SET detail_json=? WHERE key=?`, string(detail), workflowStoreSchemaV1Key); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`PRAGMA user_version=11`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenCaseStoreReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if cases, err := readOnly.ListCases(context.Background()); err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}

	check := openRawWorkflowDB(t, path)
	defer check.Close()
	var version int
	if err := check.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 11 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var decisionTables int
	if err := check.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='browser_decision_steps'`).Scan(&decisionTables); err != nil || decisionTables != 0 {
		t.Fatalf("browser decision tables=%d err=%v", decisionTables, err)
	}
}
