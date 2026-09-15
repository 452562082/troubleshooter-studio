package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectIncidentEvidenceNativeFiles(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "截图 1.PNG"), filepath.Join(root, "request.json")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("selected bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	app := &App{workflowPickEvidence: func(context.Context) ([]string, error) { calls++; return paths, nil }}
	result, err := app.SelectIncidentEvidence()
	if err != nil || calls != 1 || len(result.Images) != 1 || len(result.Files) != 1 {
		t.Fatalf("selection=%+v calls=%d err=%v", result, calls, err)
	}
	if result.Images[0].Name != "截图 1.PNG" || result.Images[0].MIMEType != "image/png" || result.Files[0].Name != "request.json" {
		t.Fatalf("unexpected names or type: %+v", result)
	}
	content, err := base64.StdEncoding.DecodeString(result.Files[0].Base64Data)
	if err != nil || string(content) != "selected bytes" {
		t.Fatal("selected bytes were not returned")
	}
}

func TestSelectIncidentEvidenceCancelAndPickerFailure(t *testing.T) {
	app := &App{workflowPickEvidence: func(context.Context) ([]string, error) { return nil, nil }}
	result, err := app.SelectIncidentEvidence()
	if err != nil || len(result.Images)+len(result.Files) != 0 {
		t.Fatalf("cancel should be an empty selection: %+v %v", result, err)
	}
	app.workflowPickEvidence = func(context.Context) ([]string, error) {
		return nil, errors.New("/private/file token=secret Cookie=secret Authorization=secret password=secret https://user:secret@example.test")
	}
	_, err = app.SelectIncidentEvidence()
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private") {
		t.Fatalf("picker error should be generic: %v", err)
	}
}

func TestSelectedEvidenceBoundsAndAtomicity(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good.txt")
	if err := os.WriteFile(good, []byte("valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large.txt")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxIncidentEvidenceFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	for name, paths := range map[string][]string{
		"empty": {empty}, "large": {large}, "directory": {root},
		"unsupported":       {filepath.Join(root, "script.sh")},
		"no partial result": {good, empty},
		"too many files":    {good, good, good, good, good},
	} {
		t.Run(name, func(t *testing.T) {
			selection, err := readSelectedIncidentEvidence(paths)
			if err == nil || len(selection.Images)+len(selection.Files) != 0 {
				t.Fatalf("invalid selection must return no partial files: %+v %v", selection, err)
			}
			if strings.Contains(err.Error(), root) {
				t.Fatal("error leaked absolute path")
			}
		})
	}
	if _, err := readSelectedIncidentEvidence([]string{good, good, good, good}); err != nil {
		t.Fatal(err)
	}
}
