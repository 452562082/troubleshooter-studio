package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
}

var artifactSecretPattern = regexp.MustCompile(`(?i)(authorization\s*:|set-cookie\s*:|cookie\s*:|password\s*=|\bbearer\s+[a-z0-9._~+/=-]+)`)

func RegisterArtifact(ctx context.Context, store *CaseStore, input ArtifactInput) (EvidenceArtifact, error) {
	if store == nil {
		return EvidenceArtifact{}, fmt.Errorf("case store is required")
	}
	if err := validateArtifactComponent("case ID", input.CaseID); err != nil {
		return EvidenceArtifact{}, err
	}
	if strings.TrimSpace(input.ArtifactsRoot) == "" || strings.TrimSpace(input.SourcePath) == "" {
		return EvidenceArtifact{}, fmt.Errorf("artifact root and source path are required")
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
	content, err := os.ReadFile(input.SourcePath)
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("read artifact source: %w", err)
	}
	sourceInfo, err := os.Stat(input.SourcePath)
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("inspect artifact source: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return EvidenceArtifact{}, fmt.Errorf("artifact source must be a regular file")
	}
	metadata := strings.Join([]string{input.SourcePath, input.CaseID, input.AttemptID, input.Kind, input.Environment, input.Version, input.RequestID, input.TraceID}, "\n")
	if input.RedactionStatus != RedactionStatusRedacted && (artifactSecretPattern.Match(content) || artifactSecretPattern.MatchString(metadata)) {
		return EvidenceArtifact{}, fmt.Errorf("artifact may contain credentials and must be explicitly redacted")
	}
	digest := sha256.Sum256(content)
	digestHex := hex.EncodeToString(digest[:])
	root, err := secureArtifactRoot(input.ArtifactsRoot)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	caseDir := filepath.Join(root, input.CaseID)
	if err := mkdirPrivateNoSymlink(caseDir); err != nil {
		return EvidenceArtifact{}, err
	}
	ext := filepath.Ext(input.SourcePath)
	if len(ext) > 32 || strings.ContainsAny(ext, `/\\`) {
		ext = ""
	}
	destination := filepath.Join(caseDir, digestHex+ext)
	artifact := EvidenceArtifact{
		ID:     deterministicWorkflowID("artifact:" + input.CaseID + ":" + input.AttemptID + ":" + input.Kind + ":" + digestHex),
		CaseID: input.CaseID, AttemptID: input.AttemptID, Kind: input.Kind,
		PathOrReference: destination, SHA256: digestHex, CapturedAt: input.CapturedAt,
		Environment: input.Environment, Version: input.Version, RequestID: input.RequestID,
		TraceID: input.TraceID, RedactionStatus: input.RedactionStatus,
	}
	if artifact.CapturedAt.IsZero() {
		artifact.CapturedAt = sourceInfo.ModTime().UTC()
	}
	if err := artifact.Validate(); err != nil {
		return EvidenceArtifact{}, err
	}
	if err := installImmutableArtifact(destination, content, digestHex); err != nil {
		return EvidenceArtifact{}, err
	}
	stored, err := store.recordEvidenceArtifact(ctx, artifact)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	return stored, nil
}

func secureArtifactRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root: %w", err)
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artifact root must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect artifact root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create artifact root: %w", err)
	}
	if info, err := os.Lstat(abs); err != nil {
		return "", fmt.Errorf("inspect artifact root: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artifact root must be a real directory")
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return "", fmt.Errorf("secure artifact root: %w", err)
	}
	return abs, nil
}

func mkdirPrivateNoSymlink(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact case path is not a private directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create artifact case directory: %w", err)
		}
	} else {
		return fmt.Errorf("inspect artifact case directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure artifact case directory: %w", err)
	}
	return nil
}

func installImmutableArtifact(destination string, content []byte, digestHex string) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact destination is not a regular file")
		}
		return verifyArtifactFile(destination, digestHex)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect artifact destination: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".artifact-*")
	if err != nil {
		return fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure artifact temporary file: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("publish artifact: %w", err)
		}
		return verifyArtifactFile(destination, digestHex)
	}
	return os.Chmod(destination, 0o600)
}

func verifyArtifactFile(path, digestHex string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open existing artifact: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash existing artifact: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != digestHex {
		return fmt.Errorf("existing artifact content conflicts with digest path")
	}
	return os.Chmod(path, 0o600)
}

func validateArtifactComponent(name, value string) error {
	if strings.TrimSpace(value) == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("artifact %s is not a safe path component", name)
	}
	return nil
}
