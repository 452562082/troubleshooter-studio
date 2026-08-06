package browserverify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const browserStepSessionHelperEnvironment = "TSHOOT_BROWSER_STEP_SESSION_HELPER"

func TestNodeBrowserStepSessionMaintainsOneStrictJSONLProcess(t *testing.T) {
	runner := nodeBrowserStepSessionRunner{command: func(ctx context.Context, _ RuntimePaths) *exec.Cmd {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNodeBrowserStepSessionProcessHelper$")
		command.Env = append(os.Environ(), browserStepSessionHelperEnvironment+"=1")
		return command
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	session, initial, err := runner.Open(ctx, RuntimePaths{Root: root, BrowsersPath: root}, workerRequest{
		Mode: "step_session",
		Plan: bughub.BrowserPlan{
			Version: bughub.BrowserPlanVersion, StartURL: "https://app.test/",
			Actions:    []bughub.BrowserAction{{ID: "fill-name", Action: "fill", Value: "host-frozen-value"}},
			Assertions: []bughub.BrowserAssertion{{Kind: "visible_text", Value: "ready"}},
		},
		Policy:     bughub.BrowserSecurityPolicy{},
		StagingDir: root,
		Headless:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Status != "completed" || initial.Title != "helper ready" {
		t.Fatalf("initial = %+v", initial)
	}
	result, err := session.Step(ctx, workerStepSessionCommand{
		Command: "step", Sequence: 1, SceneID: "scene-123", ActionID: "fill-name",
		ActionType: "fill", ElementRef: "e-1", PassiveChecks: []string{"screenshot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Receipt == nil || result.Receipt.TargetElementRef != "e-1" || result.Effect == nil || !result.Effect.SceneObserved {
		t.Fatalf("step result = %+v", result)
	}
	finished, err := session.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" || finished.FinalScreenshotPath != "browser/final.png" || len(finished.Artifacts) != 1 {
		t.Fatalf("finish result = %+v", finished)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestDecodeWorkerStepSessionResultRejectsProtocolExpansion(t *testing.T) {
	_, err := decodeWorkerStepSessionResult(json.RawMessage(`{"status":"completed","artifacts":[],"raw_value":"secret"}`))
	if err == nil {
		t.Fatal("expected unknown result field to fail closed")
	}
	oversized := bytes.Repeat([]byte{'x'}, maxBrowserStepSessionLineBytes+1)
	if _, err := decodeWorkerStepSessionResult(oversized); err == nil {
		t.Fatal("expected oversized result to fail closed")
	}
}

func TestNodeBrowserStepSessionAcceptsStrictInitializationErrorEnvelope(t *testing.T) {
	runner := nodeBrowserStepSessionRunner{command: func(ctx context.Context, _ RuntimePaths) *exec.Cmd {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNodeBrowserStepSessionErrorProcessHelper$")
		command.Env = append(os.Environ(), browserStepSessionHelperEnvironment+"=error")
		return command
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	_, _, err := runner.Open(ctx, RuntimePaths{Root: root, BrowsersPath: root}, workerRequest{
		Mode: "step_session", Plan: bughub.BrowserPlan{Version: 2}, StagingDir: root,
	})
	if err == nil || !strings.Contains(err.Error(), "failed during initialization") || strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("initialization error = %v", err)
	}
}

func TestNodeBrowserStepSessionErrorProcessHelper(t *testing.T) {
	if os.Getenv(browserStepSessionHelperEnvironment) != "error" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadBytes('\n'); err != nil {
		stepSessionHelperExit("read initialization", err)
	}
	stepSessionHelperWrite(map[string]any{
		"type": "error", "sequence": 0,
		"error_code": "browser_worker_failed", "error_message": "browser step session initialization failed",
	})
}

func TestNodeBrowserStepSessionProcessHelper(t *testing.T) {
	if os.Getenv(browserStepSessionHelperEnvironment) != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	readObject := func() map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			stepSessionHelperExit("read command", err)
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			stepSessionHelperExit("decode command", err)
		}
		return value
	}
	initial := readObject()
	if initial["mode"] != "step_session" {
		stepSessionHelperExit("initial mode", nil)
	}
	stepSessionHelperWrite(map[string]any{
		"type": "ready", "sequence": 0,
		"result": map[string]any{"status": "completed", "title": "helper ready", "artifacts": []any{}},
	})
	step := readObject()
	if step["command"] != "step" || step["action_id"] != "fill-name" {
		stepSessionHelperExit("step binding", nil)
	}
	if _, leaked := step["value"]; leaked {
		stepSessionHelperExit("step leaked action value", nil)
	}
	sequence, _ := step["sequence"].(float64)
	stepSessionHelperWrite(map[string]any{
		"type": "step", "sequence": int(sequence),
		"result": map[string]any{
			"status": "completed", "artifacts": []any{},
			"receipt": map[string]any{"action_id": "fill-name", "action_type": "fill", "target_element_ref": "e-1", "input_persisted": true},
			"effect": map[string]any{
				"action_id": "fill-name", "action_type": "fill", "effect_status": "observed",
				"scene_observed": true, "scene_changed": true, "url_changed": false,
				"surface_transition": "unchanged", "input_persisted": true,
			},
		},
	})
	finishCommand := readObject()
	if len(finishCommand) != 1 || finishCommand["command"] != "finish" {
		stepSessionHelperExit("finish command", nil)
	}
	stepSessionHelperWrite(map[string]any{
		"type": "finished",
		"result": map[string]any{
			"status": "completed", "final_screenshot_path": "browser/final.png",
			"artifacts": []any{map[string]any{"kind": "screenshot", "path": "browser/final.png"}},
		},
	})
	closeCommand := readObject()
	if len(closeCommand) != 1 || closeCommand["command"] != "close" {
		stepSessionHelperExit("close command", nil)
	}
	stepSessionHelperWrite(map[string]any{"type": "closed"})
	os.Exit(0)
}

func stepSessionHelperWrite(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		stepSessionHelperExit("write response", err)
	}
}

func stepSessionHelperExit(stage string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "step session helper %s failed: %v\n", stage, err)
	os.Exit(2)
}
