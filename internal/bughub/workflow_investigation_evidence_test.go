package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaterializeInitialInvestigationEvidenceVerifiesAndStagesFrozenArtifacts(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-investigation-handoff", CaseInvestigating)
	now := time.Now().UTC()
	finished := now.Add(time.Second)
	validation := PhaseAttempt{ID: "validation-handoff", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now, FinishedAt: &finished}
	if err := store.CreateAttempt(ctx, validation); err != nil {
		t.Fatal(err)
	}
	content := []byte(`[{"action_id":"click-search","started_at":"2026-07-18T10:20:30.123Z","method":"GET","url":"https://app.test/api/users","resource_type":"fetch","outcome":"response","status":200,"duration_ms":12,"request_id":"req-1","trace_id":"trace-1","initiator_type":"script","initiator_stack":[]}]`)
	source := filepath.Join(resolvedTempDir(t), "network.json")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactsRoot := filepath.Join(resolvedTempDir(t), "artifacts")
	artifact, err := RegisterArtifact(ctx, store, ArtifactInput{ArtifactsRoot: artifactsRoot, SourcePath: source, CaseID: incident.ID, AttemptID: validation.ID, Kind: "network", CapturedAt: finished, Environment: "test", Version: "build-1", RequestID: "req-1", TraceID: "trace-1", RedactionStatus: RedactionStatusNotRequired, RejectSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	input := InitialInvestigationInput{ValidationAttemptID: validation.ID, ScenarioHash: "scenario-1", ObservedBehavior: "results incomplete", ExpectedBehavior: "two users", Evidence: []InvestigationEvidenceReference{{ArtifactID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256, Environment: artifact.Environment, Version: artifact.Version, RequestID: artifact.RequestID, TraceID: artifact.TraceID}}}
	encoded, _ := json.Marshal(input)
	investigation := PhaseAttempt{ID: "investigation-handoff", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "bot", InputJSON: encoded, OutputJSON: []byte(`{}`), ParentAttemptID: validation.ID, StartedAt: finished}
	if err := store.CreateAttempt(ctx, investigation); err != nil {
		t.Fatal(err)
	}
	staging, err := openAttemptEvidenceStaging(artifactsRoot, investigation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Cleanup()
	defer staging.Close()
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, artifactsRoot, nil)
	prompt, err := runner.materializeInvestigationEvidence(ctx, investigation, staging)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, investigationEvidenceManifestName) || !strings.Contains(prompt, "do not rerun the browser") {
		t.Fatalf("handoff prompt = %q", prompt)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(staging.Path(), investigationEvidenceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest materializedInvestigationEvidence
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].ArtifactID != artifact.ID || manifest.Files[0].SHA256 != artifact.SHA256 {
		t.Fatalf("manifest = %+v", manifest)
	}
	staged, err := os.ReadFile(filepath.Join(staging.Path(), filepath.FromSlash(manifest.Files[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(staged)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.SHA256) || string(staged) != string(content) {
		t.Fatal("materialized validation artifact changed")
	}
}

func TestMaterializeInitialInvestigationEvidenceRejectsDivergentBinding(t *testing.T) {
	input := InitialInvestigationInput{ValidationAttemptID: "validation", Evidence: []InvestigationEvidenceReference{{ArtifactID: "missing", Kind: "network", SHA256: strings.Repeat("a", 64), Environment: "test"}}}
	encoded, _ := json.Marshal(input)
	runner := &AgentPhaseRunner{store: newOrchestratorStore(t), artifactsRoot: resolvedTempDir(t)}
	staging, err := openAttemptEvidenceStaging(runner.artifactsRoot, "attempt-divergent")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Cleanup()
	defer staging.Close()
	_, err = runner.materializeInvestigationEvidence(context.Background(), PhaseAttempt{ID: "attempt-divergent", CaseID: "case-missing", Phase: PhaseInvestigation, InputJSON: encoded}, staging)
	if err == nil {
		t.Fatal("accepted missing durable validation artifact")
	}
}

func TestMaterializeInvestigationEvidenceStagesStillReproducingRegression(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-regression-handoff", CaseInvestigating)
	now := time.Now().UTC()
	finished := now.Add(time.Second)
	regression := PhaseAttempt{ID: "regression-handoff", CaseID: incident.ID, CycleNumber: 2, Phase: PhaseRegression, Mode: AttemptRegression, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "bot", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: now, FinishedAt: &finished}
	if err := store.CreateAttempt(ctx, regression); err != nil {
		t.Fatal(err)
	}
	content := []byte(`[{"action_id":"submit","method":"POST","url":"https://app.test/api/orders","resource_type":"fetch","outcome":"response","status":500,"request_id":"req-regression","initiator_type":"script","initiator_stack":[]}]`)
	source := filepath.Join(resolvedTempDir(t), "regression-network.json")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactsRoot := filepath.Join(resolvedTempDir(t), "artifacts")
	artifact, err := RegisterArtifact(ctx, store, ArtifactInput{ArtifactsRoot: artifactsRoot, SourcePath: source, CaseID: incident.ID, AttemptID: regression.ID, Kind: "network", CapturedAt: finished, Environment: "test", Version: "build-2", RequestID: "req-regression", RedactionStatus: RedactionStatusNotRequired, RejectSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	reference := InvestigationEvidenceReference{ArtifactID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256, Environment: artifact.Environment, Version: artifact.Version, RequestID: artifact.RequestID, TraceID: artifact.TraceID}
	input := NextCycleInvestigationInput{PreviousCycle: 2, RegressionAttemptID: regression.ID, ScenarioHash: "scenario-1", ObservedDeploymentVersion: "build-2", RegressionEvidenceReferences: []InvestigationEvidenceReference{reference}, Delta: "500 still occurs after build-2"}
	encoded, _ := json.Marshal(input)
	investigation := PhaseAttempt{ID: "next-investigation-handoff", CaseID: incident.ID, CycleNumber: 3, Phase: PhaseInvestigation, Status: AttemptStatusQueued, AgentTarget: "codex", BotKey: "bot", InputJSON: encoded, OutputJSON: []byte(`{}`), ParentAttemptID: regression.ID, StartedAt: finished}
	if err := store.CreateAttempt(ctx, investigation); err != nil {
		t.Fatal(err)
	}
	staging, err := openAttemptEvidenceStaging(artifactsRoot, investigation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Cleanup()
	defer staging.Close()
	runner := NewAgentPhaseRunner(store, &phaseExecutorStub{}, nil, artifactsRoot, nil)
	if _, err := runner.materializeInvestigationEvidence(ctx, investigation, staging); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(staging.Path(), investigationEvidenceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest materializedInvestigationEvidence
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SourcePhase != PhaseRegression || manifest.SourceAttemptID != regression.ID || manifest.PreviousCycle != 2 || manifest.ObservedDeploymentVersion != "build-2" || manifest.Delta != input.Delta || len(manifest.Files) != 1 || manifest.Files[0].ArtifactID != artifact.ID {
		t.Fatalf("regression handoff manifest = %+v", manifest)
	}
}
