package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const (
	maxIncidentEvidenceImages      = 4
	maxIncidentEvidenceImageBytes  = 16 << 20
	maxIncidentEvidenceImagePixels = 50_000_000
	maxIncidentEvidenceFiles       = 4
	maxIncidentEvidenceFileBytes   = 16 << 20
)

type IncidentEvidenceImageInput struct {
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	Base64Data string `json:"base64_data"`
}

type UploadIncidentEvidenceImagesInput struct {
	CaseID          string                       `json:"case_id"`
	AttemptID       string                       `json:"attempt_id"`
	ExpectedVersion int64                        `json:"expected_version"`
	Images          []IncidentEvidenceImageInput `json:"images"`
}

type IncidentEvidenceImage struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	Size       int64  `json:"size"`
}

type IncidentEvidenceFileInput struct {
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	Base64Data string `json:"base64_data"`
}

type UploadIncidentEvidenceFilesInput struct {
	CaseID          string                      `json:"case_id"`
	AttemptID       string                      `json:"attempt_id"`
	ExpectedVersion int64                       `json:"expected_version"`
	Files           []IncidentEvidenceFileInput `json:"files"`
}

type IncidentEvidenceFile struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	Size       int64  `json:"size"`
}

// UploadIncidentEvidenceImages freezes user-selected screenshots into the
// current Case. It deliberately does not advance the workflow: the caller can
// retry the following evidence continuation or root-cause dispute if a version
// conflict occurs without losing the evidence it just uploaded.
func (a *App) UploadIncidentEvidenceImages(input UploadIncidentEvidenceImagesInput) ([]IncidentEvidenceImage, error) {
	caseID := strings.TrimSpace(input.CaseID)
	attemptID := strings.TrimSpace(input.AttemptID)
	if caseID == "" || attemptID == "" {
		return nil, errors.New("case_id and attempt_id are required")
	}
	if input.ExpectedVersion < 1 {
		return nil, errors.New("expected_version must be positive")
	}
	if len(input.Images) == 0 || len(input.Images) > maxIncidentEvidenceImages {
		return nil, fmt.Errorf("evidence images must contain between 1 and %d items", maxIncidentEvidenceImages)
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return nil, err
	}
	ctx := a.workflowCommandContext()
	incident, err := store.GetCase(ctx, caseID)
	if err != nil {
		return nil, err
	}
	if incident.Version != input.ExpectedVersion {
		return nil, fmt.Errorf("workflow_conflict:case_version_conflict: incident case version conflict: expected %d, current %d", input.ExpectedVersion, incident.Version)
	}
	if incident.CurrentAttemptID != attemptID {
		return nil, errors.New("evidence attempt is not the current Case attempt")
	}
	attempt, err := store.GetAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.CaseID != incident.ID || attempt.CycleNumber != incident.CycleNumber {
		return nil, errors.New("evidence attempt does not belong to the current Case cycle")
	}
	switch incident.Status {
	case bughub.CaseWaitingEvidence, bughub.CaseNotReproduced:
		if attempt.Phase != bughub.PhaseValidation && attempt.Phase != bughub.PhaseRegression {
			return nil, errors.New("evidence attempt is not a validation attempt for the current Case cycle")
		}
	case bughub.CaseWaitingFixApproval, bughub.CaseWaitingRemediation:
		if attempt.Phase != bughub.PhaseInvestigation || attempt.Status != bughub.AttemptStatusSucceeded {
			return nil, errors.New("evidence attempt is not the confirmed root cause for the current Case cycle")
		}
	default:
		return nil, errors.New("current Case status does not accept user evidence")
	}

	prepared := make([]struct {
		name string
		data []byte
	}, 0, len(input.Images))
	for _, item := range input.Images {
		name := strings.TrimSpace(item.Name)
		if name == "" || len([]rune(name)) > 200 || strings.ContainsAny(name, "\r\n\x00") {
			return nil, errors.New("evidence image name is invalid")
		}
		data, err := decodeIncidentEvidenceImage(item)
		if err != nil {
			return nil, fmt.Errorf("prepare evidence image %q: %w", name, err)
		}
		prepared = append(prepared, struct {
			name string
			data []byte
		}{name: name, data: data})
	}

	result := make([]IncidentEvidenceImage, 0, len(prepared))
	for _, item := range prepared {
		artifact, err := bughub.RegisterArtifactBytes(ctx, store, bughub.ArtifactInput{
			ArtifactsRoot:   filepath.Join(a.workflowRoot, "artifacts"),
			CaseID:          incident.ID,
			AttemptID:       attempt.ID,
			Kind:            "user_screenshot",
			CapturedAt:      time.Now().UTC(),
			Environment:     incident.Environment,
			RedactionStatus: bughub.RedactionStatusNotRequired,
			RejectSensitive: true,
		}, item.data)
		if err != nil {
			return nil, fmt.Errorf("store evidence image %q: %w", item.name, err)
		}
		result = append(result, IncidentEvidenceImage{ArtifactID: artifact.ID, Name: item.name, MIMEType: "image/png", Size: int64(len(item.data))})
	}
	return result, nil
}

func decodeIncidentEvidenceImage(input IncidentEvidenceImageInput) ([]byte, error) {
	mimeType := strings.ToLower(strings.TrimSpace(input.MIMEType))
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		return nil, errors.New("only PNG and JPEG images are supported")
	}
	encoded := strings.TrimSpace(input.Base64Data)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maxIncidentEvidenceImageBytes)+2 {
		return nil, errors.New("image data is empty or too large")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maxIncidentEvidenceImageBytes {
		return nil, errors.New("image data is not valid bounded base64")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxIncidentEvidenceImagePixels {
		return nil, errors.New("image dimensions are invalid or too large")
	}
	if (mimeType == "image/png" && format != "png") || (mimeType == "image/jpeg" && format != "jpeg") {
		return nil, errors.New("declared image type does not match its bytes")
	}
	if format == "png" {
		return decoded, nil
	}
	value, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return nil, errors.New("decode JPEG image")
	}
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, value); err != nil || normalized.Len() > maxIncidentEvidenceImageBytes {
		return nil, errors.New("normalize JPEG image")
	}
	return normalized.Bytes(), nil
}

// UploadIncidentEvidenceFiles freezes user-selected business input files for
// a blocked validation/regression attempt. These files are never exposed to
// the planner as local paths; the browser can reference them only by the
// resulting Case-bound EvidenceArtifact ID.
func (a *App) UploadIncidentEvidenceFiles(input UploadIncidentEvidenceFilesInput) ([]IncidentEvidenceFile, error) {
	caseID := strings.TrimSpace(input.CaseID)
	attemptID := strings.TrimSpace(input.AttemptID)
	if caseID == "" || attemptID == "" {
		return nil, errors.New("case_id and attempt_id are required")
	}
	if input.ExpectedVersion < 1 {
		return nil, errors.New("expected_version must be positive")
	}
	if len(input.Files) == 0 || len(input.Files) > maxIncidentEvidenceFiles {
		return nil, fmt.Errorf("evidence files must contain between 1 and %d items", maxIncidentEvidenceFiles)
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return nil, err
	}
	ctx := a.workflowCommandContext()
	incident, err := store.GetCase(ctx, caseID)
	if err != nil {
		return nil, err
	}
	if incident.Version != input.ExpectedVersion {
		return nil, fmt.Errorf("workflow_conflict:case_version_conflict: incident case version conflict: expected %d, current %d", input.ExpectedVersion, incident.Version)
	}
	if incident.CurrentAttemptID != attemptID || (incident.Status != bughub.CaseWaitingEvidence && incident.Status != bughub.CaseNotReproduced) {
		return nil, errors.New("current Case does not accept validation input files")
	}
	attempt, err := store.GetAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.CaseID != incident.ID || attempt.CycleNumber != incident.CycleNumber || (attempt.Phase != bughub.PhaseValidation && attempt.Phase != bughub.PhaseRegression) {
		return nil, errors.New("evidence attempt is not a validation attempt for the current Case cycle")
	}

	type preparedFile struct {
		name     string
		mimeType string
		data     []byte
	}
	prepared := make([]preparedFile, 0, len(input.Files))
	for _, item := range input.Files {
		name, err := bughub.NormalizeBrowserUploadFileName(item.Name, item.MIMEType)
		if err != nil {
			return nil, fmt.Errorf("prepare evidence file %q: %w", strings.TrimSpace(item.Name), err)
		}
		data, err := decodeIncidentEvidenceFile(item)
		if err != nil {
			return nil, fmt.Errorf("prepare evidence file %q: %w", name, err)
		}
		prepared = append(prepared, preparedFile{name: name, mimeType: strings.ToLower(strings.TrimSpace(item.MIMEType)), data: data})
	}

	result := make([]IncidentEvidenceFile, 0, len(prepared))
	for _, item := range prepared {
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(item.name)), ".")
		artifact, err := bughub.RegisterArtifactBytes(ctx, store, bughub.ArtifactInput{
			ArtifactsRoot:   filepath.Join(a.workflowRoot, "artifacts"),
			CaseID:          incident.ID,
			AttemptID:       attempt.ID,
			Kind:            "user_browser_file_" + extension,
			CapturedAt:      time.Now().UTC(),
			Environment:     incident.Environment,
			RedactionStatus: bughub.RedactionStatusNotRequired,
			RejectSensitive: true,
		}, item.data)
		if err != nil {
			return nil, fmt.Errorf("store evidence file %q: %w", item.name, err)
		}
		result = append(result, IncidentEvidenceFile{
			ArtifactID: artifact.ID, Name: item.name, MIMEType: item.mimeType, Size: int64(len(item.data)),
		})
	}
	return result, nil
}

func decodeIncidentEvidenceFile(input IncidentEvidenceFileInput) ([]byte, error) {
	if _, err := bughub.NormalizeBrowserUploadFileName(input.Name, input.MIMEType); err != nil {
		return nil, err
	}
	encoded := strings.TrimSpace(input.Base64Data)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maxIncidentEvidenceFileBytes)+2 {
		return nil, errors.New("file data is empty or too large")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maxIncidentEvidenceFileBytes {
		return nil, errors.New("file data is not valid bounded base64")
	}
	return decoded, nil
}
