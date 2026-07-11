package bughub

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ArtifactInput struct {
	ArtifactsRoot   string
	SourcePath      string
	CaseID          string
	AttemptID       string
	Kind            string
	CapturedAt      time.Time
	Environment     string
	Version         string
	RequestID       string
	TraceID         string
	RedactionStatus RedactionStatus
	// RejectSHA256 prevents a newly reported source from being re-registered
	// when its bytes are already known to belong to an earlier attempt.
	RejectSHA256 []string
}

func RegisterArtifact(ctx context.Context, store *CaseStore, input ArtifactInput) (EvidenceArtifact, error) {
	return registerArtifactWithHooks(ctx, store, input, artifactHooks{})
}

func registerArtifactWithHooks(ctx context.Context, store *CaseStore, input ArtifactInput, hooks artifactHooks) (EvidenceArtifact, error) {
	if store == nil {
		return EvidenceArtifact{}, fmt.Errorf("case store is required")
	}
	if err := validateArtifactComponent("case ID", input.CaseID); err != nil {
		return EvidenceArtifact{}, err
	}
	if strings.TrimSpace(input.ArtifactsRoot) == "" || strings.TrimSpace(input.SourcePath) == "" {
		return EvidenceArtifact{}, fmt.Errorf("artifact root and source path are required")
	}
	absRoot, err := filepath.Abs(input.ArtifactsRoot)
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("resolve artifact root: %w", err)
	}
	volumeRoot := filepath.VolumeName(absRoot) + string(filepath.Separator)
	if filepath.Clean(absRoot) == filepath.Clean(volumeRoot) {
		return EvidenceArtifact{}, fmt.Errorf("artifact root must be a dedicated subdirectory")
	}
	if strings.TrimSpace(input.AttemptID) == "" || strings.TrimSpace(input.Kind) == "" {
		return EvidenceArtifact{}, fmt.Errorf("artifact attempt ID and kind are required")
	}
	switch input.RedactionStatus {
	case RedactionStatusPending, RedactionStatusRedacted, RedactionStatusNotRequired:
	default:
		return EvidenceArtifact{}, fmt.Errorf("unsupported evidence redaction status %q", input.RedactionStatus)
	}
	attempt, err := store.GetAttempt(ctx, input.AttemptID)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	if attempt.CaseID != input.CaseID {
		return EvidenceArtifact{}, fmt.Errorf("artifact attempt does not belong to case")
	}
	captured, err := captureArtifactSource(input.SourcePath)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	for _, rejected := range input.RejectSHA256 {
		if captured.SHA256 == rejected {
			return EvidenceArtifact{}, ErrEvidenceArtifactReused
		}
	}
	metadata := strings.Join([]string{input.CaseID, input.AttemptID, input.Kind, input.Environment, input.Version, input.RequestID, input.TraceID}, "\n")
	if input.RedactionStatus != RedactionStatusRedacted && (containsSensitiveData(captured.Content) || containsSensitiveData([]byte(metadata))) {
		return EvidenceArtifact{}, fmt.Errorf("artifact may contain credentials and must be explicitly redacted")
	}
	unlockPublication := lockArtifactPublication(input.ArtifactsRoot, input.CaseID, captured.SHA256)
	defer unlockPublication()
	if existing, found, err := store.GetEvidenceArtifact(ctx, input.AttemptID, captured.SHA256, input.Kind); err != nil {
		return EvidenceArtifact{}, err
	} else if found {
		if err := verifyRegisteredArtifact(existing.PathOrReference, captured.SHA256); err != nil {
			return EvidenceArtifact{}, err
		}
		return existing, nil
	}
	publication, err := publishArtifact(input.ArtifactsRoot, input.CaseID, captured.SHA256, captured.Content)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	defer publication.Close()
	artifact := EvidenceArtifact{
		ID:     deterministicWorkflowID("artifact:" + input.CaseID + ":" + input.AttemptID + ":" + input.Kind + ":" + captured.SHA256),
		CaseID: input.CaseID, AttemptID: input.AttemptID, Kind: input.Kind,
		PathOrReference: publication.Path(), SHA256: captured.SHA256, CapturedAt: input.CapturedAt,
		Environment: input.Environment, Version: input.Version, RequestID: input.RequestID,
		TraceID: input.TraceID, RedactionStatus: input.RedactionStatus,
	}
	if artifact.CapturedAt.IsZero() {
		artifact.CapturedAt = captured.CapturedAt
	}
	if err := artifact.Validate(); err != nil {
		return EvidenceArtifact{}, err
	}
	stored, inserted, err := store.recordEvidenceArtifact(ctx, artifact, func() error {
		if hooks.BeforeCommit != nil {
			hooks.BeforeCommit()
		}
		return publication.Verify()
	})
	if err != nil {
		if publication.Created() {
			_ = publication.Cleanup()
		}
		return EvidenceArtifact{}, err
	}
	if !inserted && stored.PathOrReference != publication.Path() {
		if publication.Created() {
			if err := publication.Cleanup(); err != nil {
				return EvidenceArtifact{}, err
			}
		}
		if err := verifyRegisteredArtifact(stored.PathOrReference, captured.SHA256); err != nil {
			return EvidenceArtifact{}, err
		}
	}
	return stored, nil
}

func validateArtifactComponent(name, value string) error {
	if strings.TrimSpace(value) == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("artifact %s is not a safe path component", name)
	}
	return nil
}
