package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
)

const fixCheckpointManifestName = "fix-checkpoint.json"
const fixCheckpointManifestKind = "studio_fix_checkpoint"
const fixCheckpointManifestVersion = 1

type FixCheckpointManifest struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	CaseID    string    `json:"case_id"`
	AttemptID string    `json:"attempt_id"`
	State     string    `json:"state"`
	Result    FixResult `json:"result"`
}

type FixCheckpointLoader interface {
	LoadFixCheckpoint(context.Context, PhaseAttempt) ([]CodeChange, error)
}

type FixCheckpointCleaner interface {
	CleanupFixCheckpoint(context.Context, PhaseAttempt, string) error
}

type FixCheckpointSweeper interface {
	SweepFixCheckpointOrphans(context.Context, []PhaseAttempt) error
}

func parseFixCheckpointManifest(content []byte, attempt PhaseAttempt, allowPrepared bool) ([]CodeChange, error) {
	if containsSensitiveData(content) {
		return nil, errors.New("fix checkpoint contains sensitive data")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest FixCheckpointManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode fix checkpoint: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("fix checkpoint must contain one JSON object")
	}
	if manifest.Kind != fixCheckpointManifestKind || manifest.Version != fixCheckpointManifestVersion || manifest.CaseID != attempt.CaseID || manifest.AttemptID != attempt.ID {
		return nil, errors.New("fix checkpoint identity does not match the persisted attempt")
	}
	if manifest.State != "prepared" && manifest.State != "pushed" {
		return nil, errors.New("fix checkpoint state must be prepared or pushed")
	}
	if manifest.State == "prepared" && !allowPrepared {
		return nil, errors.New("normal fix completion requires a pushed checkpoint")
	}
	encoded, err := json.Marshal(manifest.Result)
	if err != nil {
		return nil, err
	}
	parsed, err := ParsePhaseResult(attempt, encoded)
	if err != nil || parsed.Outcome != PhaseOutcomeFixPushed || len(parsed.CodeChanges) == 0 {
		return nil, fmt.Errorf("validate fix checkpoint result: %w", err)
	}
	return parsed.CodeChanges, nil
}

func (r *AgentPhaseRunner) LoadFixCheckpoint(ctx context.Context, attempt PhaseAttempt) ([]CodeChange, error) {
	if r == nil || r.store == nil || attempt.Phase != PhaseFix {
		return nil, errors.New("fix checkpoint loader requires a fix attempt")
	}
	checkpoint, found, err := r.store.GetFixCheckpoint(ctx, attempt.ID)
	if err != nil || !found {
		return nil, err
	}
	if checkpoint.CaseID != attempt.CaseID || !strings.HasPrefix(checkpoint.StagingLocator, attempt.ID+"-") {
		return nil, errors.New("fix checkpoint locator identity mismatch")
	}
	staging, err := openExistingAttemptEvidenceStaging(r.artifactsRoot, attempt.ID, checkpoint.StagingLocator)
	if err != nil {
		return nil, err
	}
	defer staging.Close()
	captured, err := staging.Capture(fixCheckpointManifestName)
	if err != nil {
		return nil, err
	}
	return parseFixCheckpointManifest(captured.Content, attempt, true)
}

func (r *AgentPhaseRunner) CleanupFixCheckpoint(_ context.Context, attempt PhaseAttempt, locator string) error {
	staging, err := openExistingAttemptEvidenceStaging(r.artifactsRoot, attempt.ID, locator)
	if err != nil {
		return err
	}
	defer staging.Close()
	return staging.Cleanup()
}

func (r *AgentPhaseRunner) SweepFixCheckpointOrphans(_ context.Context, attempts []PhaseAttempt) error {
	terminal := make([]string, 0)
	for _, attempt := range attempts {
		if attempt.Phase != PhaseFix {
			continue
		}
		switch attempt.Status {
		case AttemptStatusSucceeded, AttemptStatusFailed, AttemptStatusCancelled, AttemptStatusInterrupted:
			terminal = append(terminal, attempt.ID)
		}
	}
	return sweepTerminalFixStaging(r.artifactsRoot, terminal)
}

func validateFixCheckpointMatchesResult(checkpoint, result []CodeChange) error {
	if !reflect.DeepEqual(checkpoint, result) {
		return errors.New("fix checkpoint does not match the final structured result")
	}
	return nil
}

func fixCheckpointLocator(staging attemptEvidenceStaging) string {
	if staging == nil {
		return ""
	}
	return filepath.Base(staging.Path())
}
