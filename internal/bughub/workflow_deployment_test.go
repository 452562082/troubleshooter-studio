package bughub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDeploymentProofSchemaMigratesV1Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(legacyWorkflowStoreSchema); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(workflowStoreSchemaV1Upgrade); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := workflowSchemaFingerprint(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(workflowSchemaMigrationDetail{Version: 1, Fingerprint: fingerprint})
	if _, err = tx.Exec(`INSERT INTO schema_migrations (key, applied_at, detail_json) VALUES (?, ?, ?)`, workflowStoreSchemaV1Key, formatStoreTime(time.Now().UTC()), string(detail)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != workflowStoreSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	columns, err := workflowTableColumns(context.Background(), store.db)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(columns["deployment_observations"], "verified_commit_ancestors_json") {
		t.Fatalf("columns=%v", columns["deployment_observations"])
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestManualVersionVerifierRequiresCompleteCommitProof(t *testing.T) {
	verifier := ManualVersionVerifier{
		Environment: "test",
		IsDescendant: func(_ context.Context, repo, expected, observed string) (bool, error) {
			return repo == "api" && expected == "merge-api" && observed == "release-api", nil
		},
	}
	request := DeploymentVerificationRequest{
		CaseID: "case-1", Environment: "test", Source: "manual",
		ExpectedCommits: map[string]string{"api": "merge-api", "worker": "merge-worker"},
		ObservedVersion: "build-42",
		ObservedCommits: map[string]string{"api": "release-api", "worker": "merge-worker"},
	}
	got, err := verifier.Verify(context.Background(), request)
	if err != nil || got.Result != DeploymentResultMatched || got.VerifiedAt == nil {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
	if got.ObservedCommits["api"] != "release-api" || got.VerifiedCommitAncestors["api"] != "merge-api" {
		t.Fatalf("descendant proof was not retained: %+v", got)
	}

	request.ObservedCommits = map[string]string{"api": "release-api"}
	got, err = verifier.Verify(context.Background(), request)
	if err != nil || got.Result != DeploymentResultMismatched {
		t.Fatalf("missing repository observation=%+v err=%v", got, err)
	}
}

func TestManualVersionVerifierRejectsInvalidMetadata(t *testing.T) {
	verifier := ManualVersionVerifier{Environment: "test"}
	base := DeploymentVerificationRequest{CaseID: "case", Environment: "test", Source: "manual", ExpectedCommits: map[string]string{"api": "abc"}, ObservedVersion: "v1", ObservedCommits: map[string]string{"api": "abc"}}
	for name, mutate := range map[string]func(*DeploymentVerificationRequest){
		"missing version":   func(v *DeploymentVerificationRequest) { v.ObservedVersion = "" },
		"missing source":    func(v *DeploymentVerificationRequest) { v.Source = "" },
		"wrong environment": func(v *DeploymentVerificationRequest) { v.Environment = "prod" },
	} {
		t.Run(name, func(t *testing.T) {
			request := base.Clone()
			mutate(&request)
			got, err := verifier.Verify(context.Background(), request)
			if err != nil || got.Result == DeploymentResultMatched {
				t.Fatalf("observation=%+v err=%v", got, err)
			}
		})
	}
}

type staticDeploymentVerifier struct {
	called int
	result DeploymentResult
}

func (v *staticDeploymentVerifier) Verify(_ context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	v.called++
	return DeploymentObservation{Environment: request.Environment, ExpectedCommits: CloneStringMap(request.ExpectedCommits), ObservedVersion: request.ObservedVersion, ObservedCommits: CloneStringMap(request.ObservedCommits), VerificationSource: request.Source, Result: v.result}, nil
}

func TestCompositeDeploymentVerifierSelectsExactSource(t *testing.T) {
	manual := &staticDeploymentVerifier{result: DeploymentResultMatched}
	http := &staticDeploymentVerifier{result: DeploymentResultMismatched}
	composite := NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": manual, "http": http})
	got, err := composite.Verify(context.Background(), DeploymentVerificationRequest{Source: "http", Environment: "test", ExpectedCommits: map[string]string{"api": "x"}})
	if err != nil || got.Result != DeploymentResultMismatched || http.called != 1 || manual.called != 0 {
		t.Fatalf("observation=%+v manual=%d http=%d err=%v", got, manual.called, http.called, err)
	}
	if _, err := composite.Verify(context.Background(), DeploymentVerificationRequest{}); err != nil || manual.called != 1 {
		t.Fatalf("empty legacy source did not select manual: calls=%d err=%v", manual.called, err)
	}
	if _, err := composite.Verify(context.Background(), DeploymentVerificationRequest{Source: "unknown"}); !errors.Is(err, ErrDeploymentVerifierUnavailable) {
		t.Fatalf("unknown source error=%v", err)
	}
}

func TestNotifyDeployedManualProofPersistsResultAndReplaysExactly(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "manual-deployment", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	runner := &recordingPhaseRunner{}
	verifier := NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": ManualVersionVerifier{Environment: "test"}})
	orchestrator := NewCaseOrchestrator(store, runner, nil, verifier)
	command := NotifyDeployedCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "manual-deployment-notice", ActorID: "alice",
		ObservedVersion: "build-42", ObservedCommits: map[string]string{"repo": "merge-1"}, Source: "manual",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{"scenario":"original"}`),
	}
	first, err := orchestrator.NotifyDeployed(ctx, command)
	if err != nil || first.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("case=%+v starts=%d err=%v", first, runner.startCount(), err)
	}
	reservationEvent, found, err := store.GetEventByIdempotencyKey(ctx, fmt.Sprintf("deployment-reserve:%s:v%d", incident.ID, incident.Version))
	if err != nil || !found || reservationEvent.ActorID != "alice" {
		t.Fatalf("reservation event=%+v found=%v err=%v", reservationEvent, found, err)
	}
	var reservation DeploymentReservation
	if err := json.Unmarshal(reservationEvent.PayloadJSON, &reservation); err != nil || reservation.CallerIdempotencyKey != command.IdempotencyKey || reservation.ActorID != command.ActorID {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	restarted := NewCaseOrchestrator(store, runner, nil, verifier)
	second, err := restarted.NotifyDeployed(ctx, command)
	if err != nil || second.Status != CaseRegressionValidating || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", second, runner.startCount(), err)
	}
	observations, err := store.ListDeploymentObservations(ctx, incident.ID)
	if err != nil || len(observations) != 1 || observations[0].Result != DeploymentResultMatched || observations[0].ObservedVersion != "build-42" {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	regressions := 0
	for _, attempt := range attempts {
		if attempt.Phase == PhaseRegression {
			regressions++
		}
	}
	if err != nil || regressions != 1 {
		t.Fatalf("attempts=%+v regressions=%d err=%v", attempts, regressions, err)
	}
	changedActor := command
	changedActor.ActorID = "bob"
	if _, err := restarted.NotifyDeployed(ctx, changedActor); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different actor replay error=%v", err)
	}
	changedKey := command
	changedKey.IdempotencyKey = "another-notification"
	if _, err := restarted.NotifyDeployed(ctx, changedKey); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different caller key replay error=%v", err)
	}
	changedProof := command
	changedProof.ObservedVersion = "build-43"
	if _, err := restarted.NotifyDeployed(ctx, changedProof); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different deployment proof replay error=%v", err)
	}
}

func TestNotifyDeployedPersistsMismatchedAndUnavailableWithoutRegression(t *testing.T) {
	for name, fixture := range map[string]struct {
		version string
		commit  string
		result  DeploymentResult
	}{
		"mismatched":  {version: "old-build", commit: "old-commit", result: DeploymentResultMismatched},
		"unavailable": {version: "", commit: "merge-1", result: DeploymentResultUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "manual-"+name, CaseWaitingDeployment)
			incident = addPushedWorkflowChange(t, store, incident)
			runner := &recordingPhaseRunner{}
			orchestrator := NewCaseOrchestrator(store, runner, nil, NewCompositeDeploymentVerifier(map[string]DeploymentVerifier{"manual": ManualVersionVerifier{Environment: "test"}}))
			got, err := orchestrator.NotifyDeployed(ctx, NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "notice-" + name, ActorID: "alice", ObservedVersion: fixture.version, ObservedCommits: map[string]string{"repo": fixture.commit}, Source: "manual"})
			if err != nil || got.Status != CaseDeploymentUnverified || runner.startCount() != 0 {
				t.Fatalf("case=%+v starts=%d err=%v", got, runner.startCount(), err)
			}
			observations, listErr := store.ListDeploymentObservations(ctx, incident.ID)
			if listErr != nil || len(observations) != 1 || observations[0].Result != fixture.result {
				t.Fatalf("observations=%+v err=%v", observations, listErr)
			}
		})
	}
}

func TestNotifyDeployedReservationIdentitySurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident := createWorkflowCase(t, store, "restart-deployment", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	verifier := &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}}
	command := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "restart-notice", ActorID: "alice", ObservedVersion: "old"}
	if _, err := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, verifier).NotifyDeployed(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewCaseOrchestrator(reopened, &recordingPhaseRunner{}, nil, verifier)
	if _, err := restarted.NotifyDeployed(ctx, command); err != nil {
		t.Fatalf("exact restart replay error=%v", err)
	}
	if len(verifier.requests) != 1 {
		t.Fatalf("restart replay verifier calls=%d", len(verifier.requests))
	}
	command.ActorID = "bob"
	if _, err := restarted.NotifyDeployed(ctx, command); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("restart actor conflict error=%v", err)
	}
}
