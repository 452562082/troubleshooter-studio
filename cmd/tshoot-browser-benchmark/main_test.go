package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestRunEmitsReportAndUsesGateFailureExitCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.json")
	content := `{"version":1,"samples":[{"id":"ordinary-1","suite":"ordinary","autonomous_completed":true,"baseline_autonomous_completed":false,"conclusion":"reproduced","current_evidence":true,"manual_reproduction_selected":false,"capability_gap_recorded":false,"locator_triggered_manual":false,"state_actions":1,"wrong_element_actions":0,"high_risk_wrong_actions":0,"unauthorized_production_actions":0}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-corpus", path}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"passed": false`)) || stderr.Len() != 0 {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestRunRejectsMalformedCorpus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"samples":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-corpus", path}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunCollectsReadOnlyCorpusAndEmptyCorpusFailsGate(t *testing.T) {
	root := t.TempDir()
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCase(context.Background(), bughub.IncidentCase{
		ID: "case-not-exported", BugID: "bug-not-exported", Status: bughub.CasePendingValidation, CycleNumber: 1, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(root, "redacted-corpus.json")
	var collectOut, collectErr bytes.Buffer
	if code := run([]string{"-collect-root", root, "-output", outputPath}, &collectOut, &collectErr); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, collectOut.String(), collectErr.String())
	}
	var summary struct {
		ScannedAttempts  int64 `json:"scanned_attempts"`
		CollectedSamples int64 `json:"collected_samples"`
	}
	if err := json.Unmarshal(collectOut.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ScannedAttempts != 0 || summary.CollectedSamples != 0 || collectErr.Len() != 0 {
		t.Fatalf("summary=%+v stderr=%s", summary, collectErr.String())
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("case-not-exported")) || bytes.Contains(content, []byte("bug-not-exported")) {
		t.Fatalf("collected corpus leaked workflow identity: %s", content)
	}
	var gateOut, gateErr bytes.Buffer
	if code := run([]string{"-corpus", outputPath}, &gateOut, &gateErr); code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, gateOut.String(), gateErr.String())
	}
	if !bytes.Contains(gateOut.Bytes(), []byte(`"ordinary_corpus_missing"`)) || gateErr.Len() != 0 {
		t.Fatalf("stdout=%s stderr=%s", gateOut.String(), gateErr.String())
	}
}

func TestRunRejectsInvalidCollectionFlagCombinations(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-collect-root", t.TempDir()},
		{"-corpus", "input.json", "-output", "output.json"},
		{"-corpus", "input.json", "-collect-root", t.TempDir(), "-output", "output.json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}
