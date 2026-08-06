package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	BrowserFrozenSceneVersion = 1
	BrowserFrozenSceneKind    = "studio_browser_scene"
	maxBrowserFrozenSceneSize = 1 << 20
)

// BrowserFrozenSceneEnvelope is the portable representation stored behind a
// Host-owned immutable artifact reference. The duplicated identity fields make
// substitution and cross-attempt recovery failures explicit before a Scene is
// allowed back into the decision loop.
type BrowserFrozenSceneEnvelope struct {
	Kind        string       `json:"kind"`
	Version     int          `json:"version"`
	AttemptID   string       `json:"attempt_id"`
	SceneSHA256 string       `json:"scene_sha256"`
	Scene       BrowserScene `json:"scene"`
}

// BrowserSceneEvidenceLoader resolves an immutable reference from the
// attempt-scoped artifact store. Implementations must not interpret reference
// as an arbitrary filesystem path; the strict decoder remains Host-owned.
type BrowserSceneEvidenceLoader interface {
	LoadFrozenBrowserScene(context.Context, string, string) ([]byte, error)
}

type BrowserSceneEvidenceLoaderFunc func(context.Context, string, string) ([]byte, error)

func (function BrowserSceneEvidenceLoaderFunc) LoadFrozenBrowserScene(ctx context.Context, attemptID, reference string) ([]byte, error) {
	return function(ctx, attemptID, reference)
}

func EncodeFrozenBrowserScene(scene BrowserScene, attemptID string) ([]byte, error) {
	attemptID = strings.TrimSpace(attemptID)
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return nil, fmt.Errorf("encode frozen browser Scene: %w", err)
	}
	envelope := BrowserFrozenSceneEnvelope{
		Kind: BrowserFrozenSceneKind, Version: BrowserFrozenSceneVersion,
		AttemptID: attemptID, SceneSHA256: scene.SceneSHA256, Scene: scene,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) == 0 || len(encoded) > maxBrowserFrozenSceneSize || containsSensitiveData(encoded) {
		return nil, errors.New("frozen browser Scene is unsafe")
	}
	return encoded, nil
}

func DecodeFrozenBrowserScene(content []byte, expectedAttemptID string) (BrowserScene, error) {
	expectedAttemptID = strings.TrimSpace(expectedAttemptID)
	if expectedAttemptID == "" || len(content) == 0 || len(content) > maxBrowserFrozenSceneSize || containsSensitiveData(content) {
		return BrowserScene{}, errors.New("frozen browser Scene is unsafe")
	}
	var envelope BrowserFrozenSceneEnvelope
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return BrowserScene{}, fmt.Errorf("decode frozen browser Scene: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BrowserScene{}, errors.New("frozen browser Scene contains trailing content")
	}
	if envelope.Kind != BrowserFrozenSceneKind || envelope.Version != BrowserFrozenSceneVersion ||
		envelope.AttemptID != expectedAttemptID || envelope.Scene.AttemptID != expectedAttemptID ||
		envelope.SceneSHA256 != envelope.Scene.SceneSHA256 {
		return BrowserScene{}, errors.New("frozen browser Scene envelope identity is invalid")
	}
	if err := validateBoundBrowserDecisionScene(envelope.Scene, expectedAttemptID, envelope.Scene.SceneID); err != nil {
		return BrowserScene{}, fmt.Errorf("validate frozen browser Scene: %w", err)
	}
	return envelope.Scene, nil
}
