package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type scriptedPhaseExecutor struct {
	Results     []PhaseExecutionResult
	Errors      []error
	Prompts     []string
	Attachments [][]PhaseAttachment
	Calls       int
}

func (s *scriptedPhaseExecutor) ExecutePhase(_ context.Context, _ string, _ BotRef, prompt string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
	s.Calls++
	s.Prompts = append(s.Prompts, prompt)
	if len(s.Results) == 0 {
		return PhaseExecutionResult{}, errors.New("scripted phase executor exhausted")
	}
	result := s.Results[0]
	s.Results = s.Results[1:]
	var err error
	if len(s.Errors) != 0 {
		err = s.Errors[0]
		s.Errors = s.Errors[1:]
	}
	return result, err
}

func (s *scriptedPhaseExecutor) CancelPhase(context.Context, string) error { return nil }

func (s *scriptedPhaseExecutor) ExecutePhaseWithAttachments(ctx context.Context, id string, bot BotRef, prompt string, attachments []PhaseAttachment, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	s.Attachments = append(s.Attachments, append([]PhaseAttachment(nil), attachments...))
	return s.ExecutePhase(ctx, id, bot, prompt, emit)
}

type fakeBrowserVerifier struct {
	Results  []BrowserVerificationResult
	Requests []BrowserVerificationRequest
	Errors   []error
	Calls    int
}

func (f *fakeBrowserVerifier) Execute(_ context.Context, request BrowserVerificationRequest) (BrowserVerificationResult, error) {
	f.Calls++
	f.Requests = append(f.Requests, request)
	if len(f.Results) == 0 {
		return BrowserVerificationResult{}, errors.New("fake browser verifier exhausted")
	}
	result := f.Results[0]
	f.Results = f.Results[1:]
	var err error
	if len(f.Errors) != 0 {
		err = f.Errors[0]
		f.Errors = f.Errors[1:]
	}
	return result, err
}

func validBrowserPlanYAML() string {
	return `version: 1
start_url: https://app.example.com/users
actions:
  - id: open-users
    action: click
    locator:
      kind: role
      value: tab
      name: 用户
    screenshot_after: true
  - id: wait-results
    action: wait_for
    locator:
      kind: text
      value: 搜索结果
    screenshot_after: true
assertions:
  - kind: visible_text
    value: 汤圆
`
}

func repairedRemainingPlanYAML() string {
	return `version: 1
start_url: https://app.example.com/users
actions:
  - id: open-users
    action: click
    locator:
      kind: role
      value: tab
      name: 用户管理
    screenshot_after: true
  - id: wait-results
    action: wait_for
    locator:
      kind: text
      value: 搜索结果
    screenshot_after: true
assertions:
  - kind: visible_text
    value: 汤圆
`
}

func reproducedValidationYAML(path string) string {
	return "verification_status: reproduced\n" +
		"environment: test\n" +
		"observed_behavior: 页面显示错误结果\n" +
		"expected_behavior: 页面显示汤圆\n" +
		"evidence:\n" +
		"  - kind: screenshot\n" +
		"    path: " + path + "\n" +
		"    environment: forged\n" +
		"    version: forged\n" +
		"    redaction_status: redacted\n" +
		"gaps: []\n"
}

func completedBrowserResult(path string) BrowserVerificationResult {
	screenshot := append([]byte("\x89PNG\r\n\x1a\n"), []byte("coordinator-screenshot")...)
	network := []byte(`[{"method":"GET","url":"https://app.example.com/users","status":200,"duration_ms":1,"content_type":"application/json","content_length":2,"request_id":"req-1","trace_id":""}]`)
	return BrowserVerificationResult{
		Status:              "completed",
		FinalURL:            "https://app.example.com/users",
		Title:               "用户管理",
		FinalScreenshotPath: path,
		Artifacts: []BrowserArtifactReference{
			verifiedBrowserArtifact("screenshot", path, "test", screenshot),
			func() BrowserArtifactReference {
				artifact := verifiedBrowserArtifact("network", "browser/network.json", "test", network)
				artifact.RequestID = "req-1"
				return artifact
			}(),
		},
	}
}

func browserCoordinatorRequest(t *testing.T) BrowserCoordinatorRequest {
	t.Helper()
	frozenRoot := t.TempDir()
	return BrowserCoordinatorRequest{
		Attempt: PhaseAttempt{
			ID: "attempt-browser", CaseID: "case-browser", CycleNumber: 1,
			Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning,
			AgentTarget: "codex", BotKey: "shop|codex#validator",
			InputJSON: []byte(`{"mode":"reproduce","target_environment":"test"}`),
			StartedAt: time.Now().UTC(),
		},
		Bug:        Bug{ID: "bug-browser", SystemID: "shop", Env: "test", FrontendURL: "https://app.example.com/users", Steps: "打开用户页并查看汤圆"},
		Bot:        BotRef{Key: "shop|codex#validator", SystemID: "shop", Target: "codex", Role: "validator", Env: "test", Path: t.TempDir()},
		BasePrompt: "bounded validation scope",
		Policy: BrowserSecurityPolicy{
			AllowedOrigins: []string{"https://app.example.com"}, ApplicationOrigins: []string{"https://app.example.com"}, StartOrigins: []string{"https://app.example.com"},
		},
		StagingDir: t.TempDir(),
		FreezeArtifacts: func(_ context.Context, references []BrowserArtifactReference) ([]browserFrozenArtifact, error) {
			result := make([]browserFrozenArtifact, 0, len(references))
			for _, reference := range references {
				var content []byte
				switch reference.Kind {
				case "screenshot":
					content = append([]byte("\x89PNG\r\n\x1a\n"), []byte("coordinator-screenshot")...)
				case "network":
					content = []byte(`[{"method":"GET","url":"https://app.example.com/users","status":200,"duration_ms":1,"content_type":"application/json","content_length":2,"request_id":"req-1","trace_id":""}]`)
				case "console":
					content = []byte(`{"type":"log","text":"safe","timestamp":"2026-07-16T10:00:00Z"}` + "\n")
				case "browser_actions":
					content = []byte(`[{"id":"open-users","action":"click","locator_kind":"role","started_at":"2026-07-16T10:00:00Z","duration_ms":1,"result":"completed","error_code":""}]`)
				default:
					return nil, errors.New("unsupported frozen fixture kind")
				}
				digest := fmt.Sprintf("%x", sha256.Sum256(content))
				if reference.SHA256 != digest || reference.Size != int64(len(content)) {
					return nil, errors.New("frozen fixture does not match reference")
				}
				path := filepath.Join(frozenRoot, digest)
				if err := os.WriteFile(path, content, 0o600); err != nil {
					return nil, err
				}
				result = append(result, browserFrozenArtifact{ReferencePath: reference.Path, Kind: reference.Kind, SHA256: reference.SHA256, Size: reference.Size, PathOrReference: path, Content: append([]byte(nil), content...)})
			}
			return result, nil
		},
	}
}

func TestBrowserCoordinatorDirectorySyncFailurePreventsPlannerAndHost(t *testing.T) {
	originalSync := browserDurabilitySync
	browserDurabilitySync = func(string) error { return errors.New("injected browser staging directory fsync failure") }
	defer func() { browserDurabilitySync = originalSync }()
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/final.png")}}
	request := browserCoordinatorRequest(t)
	for retry := 0; retry < 3; retry++ {
		result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.ErrorCode != "browser_execution_interrupted" || executor.Calls != 0 || verifier.Calls != 0 {
			t.Fatalf("retry=%d agent=%d host=%d result=%+v", retry, executor.Calls, verifier.Calls, result)
		}
	}
}

func TestBrowserCoordinatorPlansExecutesAndEvaluatesInOneAttempt(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML(), Usage: AgentUsage{InputTokens: 10, OutputTokens: 5}},
		{FinalYAML: reproducedValidationYAML("browser/final.png"), Usage: AgentUsage{InputTokens: 7, OutputTokens: 3}},
	}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/final.png")}}
	coordinator := BrowserCoordinator{Executor: executor, Verifier: verifier}
	result, err := coordinator.Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if executor.Calls != 2 || verifier.Calls != 1 {
		t.Fatalf("agent=%d browser=%d", executor.Calls, verifier.Calls)
	}
	if result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 8 {
		t.Fatalf("usage=%+v", result.Usage)
	}
	if len(result.BrowserArtifacts) != 2 || result.FinalYAML == "" {
		t.Fatalf("result=%+v", result)
	}
	parsed, err := ParsePhaseResult(browserCoordinatorRequest(t).Attempt, []byte(result.FinalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ArtifactInputs) != 2 || parsed.ArtifactInputs[0].Environment != "test" || parsed.ArtifactInputs[0].Version != "" {
		t.Fatalf("host artifacts were not authoritative: %+v", parsed.ArtifactInputs)
	}
}

func TestBrowserCoordinatorRepairsLocatorOnlyOnce(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: repairedRemainingPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/final.png")},
	}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{
		{Status: "locator_failed", ErrorCode: "locator_not_found", FailedActionID: "open-users", FinalURL: "https://app.example.com/users", Title: "用户", AccessibilitySummary: []BrowserAccessibilityNode{{Role: "tab", Name: "用户管理", Visible: true}}},
		completedBrowserResult("browser/final.png"),
	}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if executor.Calls != 3 || verifier.Calls != 2 || result.RepairCount != 1 {
		t.Fatalf("agent=%d browser=%d result=%+v", executor.Calls, verifier.Calls, result)
	}
	if got := filepath.ToSlash(verifier.Requests[0].StagingDir); !strings.HasSuffix(got, "/browser-executions/primary") {
		t.Fatalf("primary staging = %q", got)
	}
	if got := filepath.ToSlash(verifier.Requests[1].StagingDir); !strings.HasSuffix(got, "/browser-executions/repair-1") {
		t.Fatalf("repair staging = %q", got)
	}
	if !strings.HasPrefix(result.BrowserResult.FinalScreenshotPath, "browser-executions/repair-1/browser/") {
		t.Fatalf("final screenshot was not rebased: %+v", result.BrowserResult)
	}
	if !strings.Contains(executor.Prompts[1], "open-users") || strings.Contains(executor.Prompts[1], "storageState") {
		t.Fatalf("unsafe or incomplete repair prompt: %s", executor.Prompts[1])
	}
}

func TestBrowserCoordinatorRecoveryReusesDurablePrimaryAndRepairPlans(t *testing.T) {
	request := browserCoordinatorRequest(t)
	firstExecutor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: repairedRemainingPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/final.png")},
	}}
	firstVerifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{
		{Status: "locator_failed", FailedActionID: "open-users", FinalURL: "https://app.example.com/users"},
		completedBrowserResult("browser/final.png"),
	}}
	if result, err := (BrowserCoordinator{Executor: firstExecutor, Verifier: firstVerifier}).Execute(context.Background(), request); err != nil || result.ErrorCode != "" {
		t.Fatalf("initial execution result=%+v err=%v", result, err)
	}

	recoveryExecutor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: reproducedValidationYAML("browser/final.png")},
	}}
	recoveryVerifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{
		{Status: "locator_failed", FailedActionID: "open-users", FinalURL: "https://app.example.com/users"},
		completedBrowserResult("browser/final.png"),
	}}
	result, err := (BrowserCoordinator{Executor: recoveryExecutor, Verifier: recoveryVerifier}).Execute(context.Background(), request)
	if err != nil || result.ErrorCode != "" {
		t.Fatalf("recovered execution result=%+v err=%v", result, err)
	}
	if recoveryExecutor.Calls != 1 || recoveryVerifier.Calls != 2 {
		t.Fatalf("recovery replanned: agent=%d browser=%d", recoveryExecutor.Calls, recoveryVerifier.Calls)
	}
	wantPrimary, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	wantRepair, err := ParseBrowserPlan([]byte(repairedRemainingPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recoveryVerifier.Requests[0].Plan, wantPrimary) || !reflect.DeepEqual(recoveryVerifier.Requests[1].Plan, wantRepair) {
		t.Fatalf("recovery plans changed: got=%+v want primary=%+v repair=%+v", recoveryVerifier.Requests, wantPrimary, wantRepair)
	}
}

func TestBrowserCoordinatorRejectsCredentialBearingPlanBeforeJournal(t *testing.T) {
	tests := []struct {
		name, id, locator, value string
	}{
		{name: "password action id", id: "enter-password", locator: "#p", value: "hunter2"},
		{name: "pin locator", id: "enter-code", locator: "#pin", value: "1234"},
		{name: "otp action", id: "submit_otp", locator: "#code", value: "123456"},
		{name: "mfa locator", id: "enter-code", locator: "[name=mfa-code]", value: "123456"},
		{name: "chinese verification code", id: "enter-code", locator: "验证码", value: "123456"},
		{name: "chinese account", id: "填写账号", locator: "#user", value: "alice"},
		{name: "signin action", id: "signin", locator: "#identity", value: "alice"},
		{name: "sign in locator", id: "enter-identity", locator: "#sign-in", value: "alice"},
		{name: "credential action", id: "enter-credential", locator: "#identity", value: "alice"},
		{name: "user id locator", id: "enter-identity", locator: "#user-id", value: "alice"},
		{name: "email login action", id: "email-login", locator: "#identity", value: "alice"},
		{name: "captcha locator", id: "enter-code", locator: "#captcha", value: "123456"},
		{name: "verification code action", id: "verification-code", locator: "#code", value: "123456"},
		{name: "email value", id: "enter-identity", locator: "#identity", value: "alice@example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := browserCoordinatorRequest(t)
			plan := fmt.Sprintf("version: 1\nstart_url: https://app.example.com/users\nactions:\n  - id: %q\n    action: fill\n    locator: {kind: css, value: %q}\n    value: %q\nassertions:\n  - kind: visible_text\n    value: 用户管理\n", test.id, test.locator, test.value)
			executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: plan}}}
			verifier := &fakeBrowserVerifier{}
			result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.ErrorCode != "browser_validator_plan_invalid" || verifier.Calls != 0 {
				t.Fatalf("browser=%d result=%+v", verifier.Calls, result)
			}
			if _, err := os.Stat(filepath.Join(request.StagingDir, "browser-executions", "primary", "coordinator-plan.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("credential-bearing plan was journaled: %v", err)
			}
		})
	}
}

func TestBrowserCoordinatorRejectsUnsafeDurableApplicationURLBeforeHost(t *testing.T) {
	for _, startURL := range []string{
		"https://user:pass@app.example.com/users",
		"https://app.example.com/users#fragment",
		"https://app.example.com/users?token=secret",
		"https://app.example.com/users?state=Bearer%20credential",
	} {
		t.Run(url.QueryEscape(startURL), func(t *testing.T) {
			request := browserCoordinatorRequest(t)
			plan := strings.Replace(validBrowserPlanYAML(), "https://app.example.com/users", fmt.Sprintf("%q", startURL), 1)
			executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: plan}}}
			verifier := &fakeBrowserVerifier{}
			result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.ErrorCode != "browser_validator_plan_invalid" || verifier.Calls != 0 || strings.Contains(result.ErrorMessage, startURL) {
				t.Fatalf("browser=%d result=%+v", verifier.Calls, result)
			}
		})
	}
}

func TestBrowserCoordinatorRejectsAPIAndAuthenticationStartOriginsBeforeJournalAndHost(t *testing.T) {
	for _, startURL := range []string{"https://api.example.com/v1/users", "https://login.example.com/sso"} {
		t.Run(url.QueryEscape(startURL), func(t *testing.T) {
			request := browserCoordinatorRequest(t)
			request.Policy.AllowedOrigins = []string{"https://app.example.com", "https://api.example.com", "https://login.example.com"}
			request.Policy.AuthOrigins = []string{"https://login.example.com"}
			plan := strings.Replace(validBrowserPlanYAML(), "https://app.example.com/users", startURL, 1)
			executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: plan}}}
			verifier := &fakeBrowserVerifier{}
			result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.ErrorCode != "browser_validator_plan_invalid" || verifier.Calls != 0 {
				t.Fatalf("browser=%d result=%+v", verifier.Calls, result)
			}
			if _, err := os.Stat(filepath.Join(request.StagingDir, "browser-executions", "primary", "coordinator-plan.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid start-origin plan was journaled: %v", err)
			}
		})
	}
}

func TestCanonicalBrowserURLNormalizesIPLiteralSpellingsAndDefaultPorts(t *testing.T) {
	expandedURL, expandedOrigin, err := canonicalBrowserURL("https://[2001:0db8:0000:0000:0000:0000:0000:0001]:443/users")
	if err != nil {
		t.Fatal(err)
	}
	compressedURL, compressedOrigin, err := canonicalBrowserURL("https://[2001:db8::1]/users")
	if err != nil {
		t.Fatal(err)
	}
	if expandedURL != "https://[2001:db8::1]/users" || expandedURL != compressedURL || expandedOrigin != "https://[2001:db8::1]" || expandedOrigin != compressedOrigin {
		t.Fatalf("expanded=(%q,%q) compressed=(%q,%q)", expandedURL, expandedOrigin, compressedURL, compressedOrigin)
	}
	for _, raw := range []string{"https://[fe80::1%25en0]/", "https://user:pass@[2001:db8::1]/"} {
		if _, _, err := canonicalBrowserURL(raw); err == nil {
			t.Fatalf("unsafe IP URL accepted: %q", raw)
		}
	}
}

func TestBrowserCoordinatorRejectsUnsafePlanJournalWithoutReplanning(t *testing.T) {
	request := browserCoordinatorRequest(t)
	directory := filepath.Join(request.StagingDir, "browser-executions", "primary")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "coordinator-plan.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &scriptedPhaseExecutor{}
	verifier := &fakeBrowserVerifier{}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_execution_interrupted" || executor.Calls != 0 || verifier.Calls != 0 {
		t.Fatalf("agent=%d browser=%d result=%+v", executor.Calls, verifier.Calls, result)
	}
}

func TestBrowserCoordinatorMapsBrowserStopsWithoutEvaluator(t *testing.T) {
	tests := []struct {
		name       string
		results    []BrowserVerificationResult
		wantCode   string
		wantCalls  int
		wantRepair int
	}{
		{name: "login", results: []BrowserVerificationResult{{Status: "login_required", LoginOrigin: "https://login.example.com"}}, wantCode: "browser_login_required", wantCalls: 1},
		{name: "runtime", results: []BrowserVerificationResult{{Status: "runtime_broken", ErrorMessage: "Authorization: Bearer must-not-persist"}}, wantCode: "browser_runtime_broken", wantCalls: 1},
		{name: "assertion", results: []BrowserVerificationResult{{Status: "assertion_failed", FailedActionID: "wait-results"}}, wantCode: "browser_assertion_failed", wantCalls: 1},
		{name: "second locator", results: []BrowserVerificationResult{{Status: "locator_failed", FailedActionID: "open-users"}, {Status: "locator_failed", FailedActionID: "open-users"}}, wantCode: "browser_locator_failed", wantCalls: 2, wantRepair: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executorResults := []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}
			if test.wantRepair == 1 {
				executorResults = append(executorResults, PhaseExecutionResult{FinalYAML: repairedRemainingPlanYAML()})
			}
			executor := &scriptedPhaseExecutor{Results: executorResults}
			verifier := &fakeBrowserVerifier{Results: test.results}
			result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			if result.ErrorCode != test.wantCode || verifier.Calls != test.wantCalls || result.RepairCount != test.wantRepair {
				t.Fatalf("calls=%d result=%+v", verifier.Calls, result)
			}
			if strings.Contains(result.ErrorMessage, "must-not-persist") {
				t.Fatalf("raw browser error escaped: %+v", result)
			}
		})
	}
}

func TestBrowserCoordinatorExplicitWebWithoutURLNeedsEvidenceWithoutCalls(t *testing.T) {
	request := browserCoordinatorRequest(t)
	request.Bug.FrontendURL = ""
	request.Attempt.InputJSON = []byte(`{"mode":"reproduce","target_environment":"test","user_input":"请用浏览器复现这个页面"}`)
	executor := &scriptedPhaseExecutor{}
	verifier := &fakeBrowserVerifier{}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_url_required" || executor.Calls != 0 || verifier.Calls != 0 {
		t.Fatalf("agent=%d browser=%d result=%+v", executor.Calls, verifier.Calls, result)
	}
}

func TestBrowserCoordinatorRejectsSuccessfulEvaluationWithoutHostFinalScreenshot(t *testing.T) {
	network := []byte(`[{"method":"GET","url":"https://app.example.com/users","status":200,"duration_ms":1,"content_type":"application/json","content_length":2,"request_id":"req-1","trace_id":""}]`)
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/claimed.png")},
	}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{{
		Status:    "completed",
		Artifacts: []BrowserArtifactReference{verifiedBrowserArtifact("network", "browser/network.json", "test", network)},
	}}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_screenshot_required" || result.FinalYAML != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBrowserCoordinatorReturnsBoundedValidatorFailure(t *testing.T) {
	executor := &scriptedPhaseExecutor{
		Results: []PhaseExecutionResult{{Usage: AgentUsage{InputTokens: 4}}},
		Errors:  []error{errors.New("Authorization: Bearer planner-secret")},
	}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: &fakeBrowserVerifier{}}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_validator_failed" || result.Usage.InputTokens != 4 || strings.Contains(result.ErrorMessage, "planner-secret") {
		t.Fatalf("result=%+v", result)
	}
}

func TestBrowserCoordinatorPreservesSafeHostSystemErrorCode(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	verifier := &fakeBrowserVerifier{
		Results: []BrowserVerificationResult{{}},
		Errors:  []error{errors.New("browser_artifact_invalid: Authorization: Bearer raw-host-secret")},
	}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_artifact_invalid" || strings.Contains(result.ErrorMessage, "raw-host-secret") {
		t.Fatalf("result=%+v", result)
	}
}

func TestBrowserCoordinatorRejectsRepairThatRepeatsCompletedAction(t *testing.T) {
	failedSecond := `version: 1
start_url: https://app.example.com/users
actions:
  - id: open-users
    action: click
    locator: {kind: role, value: tab, name: 用户管理}
    screenshot_after: true
  - id: wait-results
    action: wait_for
    locator: {kind: text, value: 搜索结果}
    screenshot_after: true
assertions:
  - kind: visible_text
    value: 汤圆
`
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}, {FinalYAML: failedSecond}}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{{Status: "locator_failed", FailedActionID: "wait-results"}}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_validator_plan_invalid" || verifier.Calls != 1 {
		t.Fatalf("browser=%d result=%+v", verifier.Calls, result)
	}
}

func TestBrowserCoordinatorRejectsUnsafeHostPathBeforeEvaluation(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{{
		Status: "completed", FinalScreenshotPath: "../final.png",
		Artifacts: []BrowserArtifactReference{{Kind: "screenshot", Path: "../final.png", Environment: "test"}},
	}}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_verifier_failed" || executor.Calls != 1 {
		t.Fatalf("agent=%d result=%+v", executor.Calls, result)
	}
}

func TestBrowserCoordinatorRejectsHostArtifactEnvironmentOrVersionMismatch(t *testing.T) {
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{
		{FinalYAML: validBrowserPlanYAML()},
		{FinalYAML: reproducedValidationYAML("browser/final.png")},
	}}
	mismatch := completedBrowserResult("browser/final.png")
	mismatch.Artifacts[0].Environment = "prod"
	mismatch.Artifacts[0].Version = "unbound-version"
	result, err := (BrowserCoordinator{Executor: executor, Verifier: &fakeBrowserVerifier{Results: []BrowserVerificationResult{mismatch}}}).Execute(context.Background(), browserCoordinatorRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "browser_verifier_failed" || result.FinalYAML != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBrowserCoordinatorRedactsPromptsAndReturnedBrowserResult(t *testing.T) {
	request := browserCoordinatorRequest(t)
	request.BasePrompt = "open https://alice:hunter2@app.example.com Authorization: Bearer top-secret-token"
	executor := &scriptedPhaseExecutor{Results: []PhaseExecutionResult{{FinalYAML: validBrowserPlanYAML()}}}
	verifier := &fakeBrowserVerifier{Results: []BrowserVerificationResult{{
		Status: "runtime_broken", ErrorMessage: "Cookie: sid=raw-secret", FinalURL: "https://alice:hunter2@app.example.com/users",
	}}}
	result, err := (BrowserCoordinator{Executor: executor, Verifier: verifier}).Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	combined, _ := json.Marshal(result)
	if strings.Contains(executor.Prompts[0], "hunter2") || strings.Contains(executor.Prompts[0], "top-secret-token") || strings.Contains(string(combined), "raw-secret") || strings.Contains(string(combined), "hunter2") {
		t.Fatalf("prompt/result leaked secret: prompt=%s result=%s", executor.Prompts[0], combined)
	}
}
