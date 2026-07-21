package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const investigationEvidenceManifestName = "validation-evidence-manifest.json"

type InvestigationEvidenceReference struct {
	ArtifactID  string `json:"artifact_id"`
	Kind        string `json:"kind"`
	SHA256      string `json:"sha256"`
	Environment string `json:"environment"`
	Version     string `json:"version,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
}

// InitialInvestigationInput is the durable handoff from a successful
// reproduction to root-cause analysis. It contains immutable artifact
// identities rather than host paths; the runner verifies and materializes the
// bytes into the investigation staging directory immediately before execution.
type InitialInvestigationInput struct {
	ValidationAttemptID string                           `json:"validation_attempt_id"`
	ScenarioHash        string                           `json:"scenario_hash"`
	ObservedBehavior    string                           `json:"observed_behavior"`
	ExpectedBehavior    string                           `json:"expected_behavior"`
	Evidence            []InvestigationEvidenceReference `json:"validation_evidence"`
}

type materializedInvestigationEvidence struct {
	SourcePhase               Phase                                   `json:"source_phase"`
	SourceAttemptID           string                                  `json:"source_attempt_id"`
	ScenarioHash              string                                  `json:"scenario_hash"`
	ObservedBehavior          string                                  `json:"observed_behavior,omitempty"`
	ExpectedBehavior          string                                  `json:"expected_behavior,omitempty"`
	PreviousCycle             int                                     `json:"previous_cycle,omitempty"`
	ObservedDeploymentVersion string                                  `json:"observed_deployment_version,omitempty"`
	Delta                     string                                  `json:"delta,omitempty"`
	Evidence                  []InvestigationEvidenceReference        `json:"evidence"`
	Files                     []materializedInvestigationEvidenceFile `json:"files"`
}

type materializedInvestigationEvidenceFile struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Path       string `json:"path"`
}

func (o *CaseOrchestrator) buildInitialInvestigationInput(ctx context.Context, attempt PhaseAttempt, output json.RawMessage) (json.RawMessage, error) {
	if o == nil || o.store == nil {
		return nil, errors.New("case orchestrator store is required")
	}
	result, err := ParseValidationResult(output)
	if err != nil || result.VerificationStatus != "reproduced" {
		return nil, errors.Join(errors.New("initial investigation requires reproduced validation output"), err)
	}
	artifacts, err := o.store.ListEvidenceArtifacts(ctx, attempt.CaseID)
	if err != nil {
		return nil, err
	}
	references := make([]InvestigationEvidenceReference, 0)
	for _, artifact := range artifacts {
		if artifact.AttemptID != attempt.ID {
			continue
		}
		references = append(references, InvestigationEvidenceReference{
			ArtifactID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256,
			Environment: artifact.Environment, Version: artifact.Version,
			RequestID: artifact.RequestID, TraceID: artifact.TraceID,
		})
	}
	if len(references) == 0 {
		return nil, errors.New("initial investigation requires registered validation evidence")
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].Kind != references[j].Kind {
			return references[i].Kind < references[j].Kind
		}
		return references[i].ArtifactID < references[j].ArtifactID
	})
	return json.Marshal(InitialInvestigationInput{
		ValidationAttemptID: attempt.ID,
		ScenarioHash:        result.ScenarioHash,
		ObservedBehavior:    result.ObservedBehavior,
		ExpectedBehavior:    result.ExpectedBehavior,
		Evidence:            references,
	})
}

func (o *CaseOrchestrator) buildValidationEvidenceRefreshAttempt(ctx context.Context, incident IncidentCase, investigation PhaseAttempt, gaps []string, key string) (PhaseAttempt, error) {
	var handoff InitialInvestigationInput
	if err := json.Unmarshal(investigation.InputJSON, &handoff); err != nil || strings.TrimSpace(handoff.ValidationAttemptID) == "" {
		return PhaseAttempt{}, errors.Join(errors.New("validation evidence refresh requires an initial validation handoff"), err)
	}
	validation, err := o.store.GetAttempt(ctx, handoff.ValidationAttemptID)
	if err != nil {
		return PhaseAttempt{}, err
	}
	if validation.CaseID != incident.ID || validation.CycleNumber != incident.CycleNumber || validation.Phase != PhaseValidation || validation.Mode != AttemptReproduce || validation.BotKey != investigation.BotKey || validation.AgentTarget != investigation.AgentTarget {
		return PhaseAttempt{}, errors.New("validation evidence refresh source does not match the investigation Case")
	}
	var input map[string]any
	if err := json.Unmarshal(validation.InputJSON, &input); err != nil || input == nil {
		return PhaseAttempt{}, errors.Join(errors.New("validation evidence refresh source input must be an object"), err)
	}
	input["source_investigation_attempt_id"] = investigation.ID
	input["evidence_refresh_gaps"] = append([]string(nil), gaps...)
	encoded, err := json.Marshal(input)
	if err != nil {
		return PhaseAttempt{}, err
	}
	return newAttempt(incident, PhaseValidation, AttemptReproduce, key, BotRef{Key: investigation.BotKey, Target: investigation.AgentTarget}, encoded, validation.ID), nil
}

func (r *AgentPhaseRunner) materializeInvestigationEvidence(ctx context.Context, attempt PhaseAttempt, staging attemptEvidenceStaging) (string, error) {
	if attempt.Phase != PhaseInvestigation || len(attempt.InputJSON) == 0 || string(attempt.InputJSON) == "{}" {
		return "", nil
	}
	var initial InitialInvestigationInput
	if err := json.Unmarshal(attempt.InputJSON, &initial); err != nil {
		return "", nil
	}
	manifest := materializedInvestigationEvidence{}
	if strings.TrimSpace(initial.ValidationAttemptID) != "" {
		manifest.SourcePhase = PhaseValidation
		manifest.SourceAttemptID = initial.ValidationAttemptID
		manifest.ScenarioHash = initial.ScenarioHash
		manifest.ObservedBehavior = initial.ObservedBehavior
		manifest.ExpectedBehavior = initial.ExpectedBehavior
		manifest.Evidence = initial.Evidence
	} else {
		var next NextCycleInvestigationInput
		if err := json.Unmarshal(attempt.InputJSON, &next); err != nil || strings.TrimSpace(next.RegressionAttemptID) == "" {
			return "", nil
		}
		manifest.SourcePhase = PhaseRegression
		manifest.SourceAttemptID = next.RegressionAttemptID
		manifest.ScenarioHash = next.ScenarioHash
		manifest.PreviousCycle = next.PreviousCycle
		manifest.ObservedDeploymentVersion = next.ObservedDeploymentVersion
		manifest.Delta = next.Delta
		manifest.Evidence = next.RegressionEvidenceReferences
	}
	if strings.TrimSpace(manifest.SourceAttemptID) == "" {
		return "", nil
	}
	if len(manifest.Evidence) == 0 {
		return "", errors.New("investigation reproduction handoff contains no evidence")
	}
	directory := filepath.Join(staging.Path(), "validation-input")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create investigation validation input: %w", err)
	}
	for index, reference := range manifest.Evidence {
		verified, err := ReadEvidenceArtifactFromRoot(ctx, r.store, r.artifactsRoot, attempt.CaseID, reference.ArtifactID)
		if err != nil {
			return "", fmt.Errorf("read validation evidence %s: %w", reference.ArtifactID, err)
		}
		artifact := verified.Artifact
		if artifact.AttemptID != manifest.SourceAttemptID || artifact.Kind != reference.Kind || artifact.SHA256 != reference.SHA256 || artifact.Environment != reference.Environment || artifact.Version != reference.Version || artifact.RequestID != reference.RequestID || artifact.TraceID != reference.TraceID {
			return "", errors.New("reproduction evidence no longer matches its durable investigation binding")
		}
		extension := ".json"
		if artifact.Kind == "console" {
			extension = ".jsonl"
		} else if artifact.Kind == "screenshot" {
			extension = ".png"
		}
		name := fmt.Sprintf("%02d-%s-%s%s", index+1, safeEvidenceFilenamePart(artifact.Kind), artifact.SHA256[:12], extension)
		relative := filepath.ToSlash(filepath.Join("validation-input", name))
		path := filepath.Join(staging.Path(), filepath.FromSlash(relative))
		if err := writeImmutableInvestigationInput(path, verified.Content); err != nil {
			return "", err
		}
		manifest.Files = append(manifest.Files, materializedInvestigationEvidenceFile{ArtifactID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256, Path: relative})
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(staging.Path(), investigationEvidenceManifestName)
	if err := writeImmutableInvestigationInput(manifestPath, append(encoded, '\n')); err != nil {
		return "", err
	}
	return "\n## Frozen validation evidence (mandatory input)\n\nValidation or regression reproduction is already complete. Read `STUDIO_EVIDENCE_STAGING_DIR/" + investigationEvidenceManifestName + "` before querying runtime systems or source code. The files listed by its `files[].path` are relative to STUDIO_EVIDENCE_STAGING_DIR and are immutable evidence from the completed validation. Reuse their action/network/console/request/trace facts. Do not invoke validator-only skills (`bug-verifier`, `api-verifier`, `attachment-evidence-verifier`) and do not rerun the browser. If an immutable validation file is missing or insufficient, put the exact collection gap in validation_gaps; Studio, not the investigation Agent, schedules the validation refresh. Distinguish runtime facts from static inference.\n", nil
}

func safeEvidenceFilenamePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			result.WriteRune(char)
		}
	}
	if result.Len() == 0 {
		return "evidence"
	}
	return result.String()
}

func writeImmutableInvestigationInput(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("create investigation evidence input: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write investigation evidence input: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync investigation evidence input: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close investigation evidence input: %w", err)
	}
	return nil
}
