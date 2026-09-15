package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

var pngFileSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type IncidentArtifactPreview struct {
	ArtifactID string `json:"artifact_id"`
	MIMEType   string `json:"mime_type"`
	Base64Data string `json:"base64_data"`
	Size       int    `json:"size"`
}

func (a *App) GetIncidentArtifactPreview(caseID, artifactID string) (IncidentArtifactPreview, error) {
	caseID, artifactID = strings.TrimSpace(caseID), strings.TrimSpace(artifactID)
	if caseID == "" || artifactID == "" {
		return IncidentArtifactPreview{}, errors.New("case_id and artifact_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return IncidentArtifactPreview{}, err
	}
	content, err := bughub.ReadEvidenceArtifactFromRoot(a.workflowCommandContext(), store, filepath.Join(a.workflowRoot, "artifacts"), caseID, artifactID)
	if err != nil {
		return IncidentArtifactPreview{}, err
	}
	if content.Artifact.Kind != "screenshot" || !bytes.HasPrefix(content.Content, pngFileSignature) {
		return IncidentArtifactPreview{}, errors.New("artifact is not a registered PNG screenshot")
	}
	return IncidentArtifactPreview{ArtifactID: content.Artifact.ID, MIMEType: "image/png", Base64Data: base64.StdEncoding.EncodeToString(content.Content), Size: len(content.Content)}, nil
}

func (a *App) SaveIncidentArtifact(caseID, artifactID string) (bool, error) {
	caseID, artifactID = strings.TrimSpace(caseID), strings.TrimSpace(artifactID)
	if caseID == "" || artifactID == "" {
		return false, errors.New("case_id and artifact_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return false, err
	}
	content, err := bughub.ReadEvidenceArtifactFromRoot(a.workflowCommandContext(), store, filepath.Join(a.workflowRoot, "artifacts"), caseID, artifactID)
	if err != nil {
		return false, err
	}
	save := a.workflowSaveArtifact
	if save == nil {
		save = saveFileNative
	}
	destination, err := save("保存故障证据", incidentArtifactDefaultFilename(content.Artifact.Kind), a.getRuntimeContext())
	if err != nil {
		return false, errors.New("save artifact dialog failed")
	}
	if destination == "" {
		return false, nil
	}
	if err := os.WriteFile(destination, content.Content, 0o600); err != nil {
		return false, errors.New("write saved artifact failed")
	}
	return true, nil
}

func incidentArtifactDefaultFilename(kind string) string {
	switch kind {
	case "screenshot":
		return "incident-screenshot.png"
	case "network":
		return "incident-network.json"
	case "console":
		return "incident-console.txt"
	case "browser_actions":
		return "incident-browser-actions.json"
	case "request_facts":
		return "incident-request-facts.json"
	case "response_assertions":
		return "incident-response-assertions.json"
	case "response_facts":
		return "incident-response-facts.json"
	default:
		return "incident-evidence.bin"
	}
}
