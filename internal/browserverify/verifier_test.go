package browserverify

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type fakeWorker struct {
	Result        workerResult
	Errors        []error
	Calls         int
	ArtifactBytes map[string][]byte
	ExtraFiles    map[string][]byte
}

func (w *fakeWorker) Run(_ context.Context, _ RuntimePaths, request workerRequest, _ func(bughub.BrowserProgress)) (workerResult, error) {
	w.Calls++
	if w.Calls <= len(w.Errors) && w.Errors[w.Calls-1] != nil {
		return workerResult{}, w.Errors[w.Calls-1]
	}
	for _, artifact := range w.Result.Artifacts {
		if filepath.IsAbs(artifact.Path) || !strings.HasPrefix(artifact.Path, "browser/") {
			continue
		}
		relative := strings.TrimPrefix(artifact.Path, "browser/")
		content := w.ArtifactBytes[artifact.Path]
		if content == nil {
			content = workerFixtureBytes(artifact.Kind)
		}
		path := filepath.Join(request.StagingDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return workerResult{}, err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return workerResult{}, err
		}
	}
	for relative, content := range w.ExtraFiles {
		path := filepath.Join(request.StagingDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return workerResult{}, err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return workerResult{}, err
		}
	}
	return w.Result, nil
}

func workerFixtureBytes(kind string) []byte {
	switch kind {
	case "screenshot":
		return []byte("\x89PNG\r\n\x1a\nfixture")
	case "network":
		return []byte("[]\n")
	case "console":
		return []byte("{\"type\":\"log\",\"text\":\"safe\"}\n")
	case "browser_actions":
		return []byte("[]\n")
	default:
		return []byte("fixture\n")
	}
}

func newTestHostVerifier(t *testing.T, worker WorkerRunner) *HostVerifier {
	t.Helper()
	runtime := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	if _, err := runtime.Ensure(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return NewHostVerifier(runtime, worker, publicResolver("app.test", "login.test"))
}

func validBrowserRequest(t *testing.T) bughub.BrowserVerificationRequest {
	t.Helper()
	return bughub.BrowserVerificationRequest{
		CaseID:      "case-1",
		CycleNumber: 1,
		AttemptID:   "attempt-1",
		SystemID:    "shop",
		Environment: "test",
		Version:     "v1",
		Policy: bughub.BrowserSecurityPolicy{
			AllowedOrigins: []string{"https://app.test"},
			AuthOrigins:    []string{"https://login.test"},
		},
		Plan: bughub.BrowserPlan{
			Version:    1,
			StartURL:   "https://app.test/users",
			Actions:    []bughub.BrowserAction{{ID: "final", Action: "screenshot"}},
			Assertions: []bughub.BrowserAssertion{{Kind: "visible_text", Value: "Users"}},
		},
		StagingDir: t.TempDir(),
	}
}

func completedWorkerResult() workerResult {
	return workerResult{
		Status:              "completed",
		FinalURL:            "https://app.test/users",
		Title:               "Users",
		FinalScreenshotPath: "browser/final.png",
		Artifacts: []workerArtifact{
			{Kind: "screenshot", Path: "browser/final.png"},
			{Kind: "network", Path: "browser/network.json", RequestID: "req-1", TraceID: "trace-1"},
		},
	}
}

func TestHostVerifierReturnsOnlyManifestArtifacts(t *testing.T) {
	worker := &fakeWorker{Result: workerResult{
		Status: "completed",
		Artifacts: []workerArtifact{
			{Kind: "screenshot", Path: "browser/final.png"},
			{Kind: "network", Path: "browser/network.json"},
		},
	}}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	result, err := verifier.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || len(result.Artifacts) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.FinalScreenshotPath != "browser/final.png" {
		t.Fatalf("final screenshot = %q", result.FinalScreenshotPath)
	}
	for _, artifact := range result.Artifacts {
		if filepath.IsAbs(artifact.Path) || strings.Contains(artifact.Path, "..") {
			t.Fatalf("unsafe artifact: %+v", artifact)
		}
	}
	if _, err := os.Stat(filepath.Join(request.StagingDir, "browser", "result.json")); err != nil {
		t.Fatalf("result manifest is missing: %v", err)
	}
}

func TestHostVerifierReplaysCompletedManifestWithoutRerunningWorker(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult()}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	first, err := verifier.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := verifier.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Calls != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("calls=%d first=%+v second=%+v", worker.Calls, first, second)
	}
}

func TestHostVerifierRejectsChangedArtifactDuringCompletedReplay(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult()}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	if _, err := verifier.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.StagingDir, "browser", "network.json"), []byte("[{}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected changed artifact replay rejection")
	}
	if worker.Calls != 1 {
		t.Fatalf("worker calls = %d, changed evidence must not rerun", worker.Calls)
	}
}

func TestHostVerifierValidatesPlanBeforeManifestReplay(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult()}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	if _, err := verifier.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Policy.IsProd = true
	request.Plan.Actions = []bughub.BrowserAction{{ID: "write", Action: "click", Locator: &bughub.BrowserLocator{Kind: "text", Value: "Delete"}}}
	if _, err := verifier.Execute(context.Background(), request); !errors.Is(err, ErrBrowserProdInteractionBlocked) {
		t.Fatalf("error = %v, want production interaction rejection", err)
	}
	if worker.Calls != 1 {
		t.Fatalf("worker calls = %d, want 1", worker.Calls)
	}
}

func TestHostVerifierDoesNotReplayInterruptedInteractivePlan(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{errors.New("worker crashed")}}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	request.Plan.Actions = []bughub.BrowserAction{{ID: "click", Action: "click", Locator: &bughub.BrowserLocator{Kind: "text", Value: "Search"}}}
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected initial worker crash")
	}
	if _, err := verifier.Execute(context.Background(), request); !errors.Is(err, ErrBrowserExecutionInterrupted) {
		t.Fatalf("error = %v, want ErrBrowserExecutionInterrupted", err)
	}
	if worker.Calls != 1 {
		t.Fatalf("worker calls = %d, want no automatic replay", worker.Calls)
	}
}

func TestHostVerifierReplaysInterruptedReadOnlyPlanAtMostOnce(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{errors.New("first crash"), errors.New("second crash")}}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	request.Plan.Actions = []bughub.BrowserAction{
		{ID: "goto", Action: "goto", URL: "https://app.test/users"},
		{ID: "wait", Action: "wait_for", Locator: &bughub.BrowserLocator{Kind: "text", Value: "Users"}},
		{ID: "shot", Action: "screenshot"},
	}
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected first worker crash")
	}
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected second worker crash")
	}
	if _, err := verifier.Execute(context.Background(), request); !errors.Is(err, ErrBrowserExecutionInterrupted) {
		t.Fatalf("error = %v, want second interruption to fail closed", err)
	}
	if worker.Calls != 2 {
		t.Fatalf("worker calls = %d, want exactly one replay", worker.Calls)
	}
}

func TestHostVerifierRejectsUnsafeOrUnmanifestedArtifacts(t *testing.T) {
	cases := []struct {
		name   string
		result workerResult
		worker func(*fakeWorker, bughub.BrowserVerificationRequest)
	}{
		{name: "absolute", result: workerResult{Status: "completed", Artifacts: []workerArtifact{{Kind: "screenshot", Path: filepath.Join(string(filepath.Separator), "tmp", "final.png")}}}},
		{name: "traversal", result: workerResult{Status: "completed", Artifacts: []workerArtifact{{Kind: "screenshot", Path: "browser/../secret.png"}}}},
		{name: "outside browser", result: workerResult{Status: "completed", Artifacts: []workerArtifact{{Kind: "screenshot", Path: "final.png"}}}},
		{name: "undeclared", result: completedWorkerResult(), worker: func(worker *fakeWorker, _ bughub.BrowserVerificationRequest) {
			worker.ExtraFiles = map[string][]byte{"undeclared.txt": []byte("secret")}
		}},
		{name: "oversized", result: completedWorkerResult(), worker: func(worker *fakeWorker, _ bughub.BrowserVerificationRequest) {
			worker.ArtifactBytes = map[string][]byte{"browser/final.png": make([]byte, maxBrowserArtifactBytes+1)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worker := &fakeWorker{Result: tc.result}
			request := validBrowserRequest(t)
			if tc.worker != nil {
				tc.worker(worker, request)
			}
			verifier := newTestHostVerifier(t, worker)
			if _, err := verifier.Execute(context.Background(), request); err == nil {
				t.Fatal("expected unsafe artifact rejection")
			}
			if _, err := os.Stat(filepath.Join(request.StagingDir, "browser", "result.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe result was published: %v", err)
			}
		})
	}
}

func TestHostVerifierRejectsSymlinkArtifact(t *testing.T) {
	request := validBrowserRequest(t)
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, workerFixtureBytes("screenshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := WorkerRunnerFunc(func(_ context.Context, _ RuntimePaths, workerRequest workerRequest, _ func(bughub.BrowserProgress)) (workerResult, error) {
		if err := os.Symlink(outside, filepath.Join(workerRequest.StagingDir, "final.png")); err != nil {
			t.Fatal(err)
		}
		return completedWorkerResult(), nil
	})
	verifier := newTestHostVerifier(t, worker)
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected symlink artifact rejection")
	}
}

func TestHostVerifierRequiresFailureScreenshotExceptLogin(t *testing.T) {
	for _, status := range []string{"locator_failed", "assertion_failed"} {
		t.Run(status, func(t *testing.T) {
			worker := &fakeWorker{Result: workerResult{Status: status, ErrorCode: status, Artifacts: []workerArtifact{{Kind: "network", Path: "browser/network.json"}}}}
			if _, err := newTestHostVerifier(t, worker).Execute(context.Background(), validBrowserRequest(t)); err == nil {
				t.Fatal("expected missing failure screenshot rejection")
			}
		})
	}
	worker := &fakeWorker{Result: workerResult{Status: "login_required", ErrorCode: "browser_login_required", LoginOrigin: "https://login.test", Artifacts: []workerArtifact{}}}
	result, err := newTestHostVerifier(t, worker).Execute(context.Background(), validBrowserRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "login_required" || len(result.Artifacts) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

type WorkerRunnerFunc func(context.Context, RuntimePaths, workerRequest, func(bughub.BrowserProgress)) (workerResult, error)

func (fn WorkerRunnerFunc) Run(ctx context.Context, paths RuntimePaths, request workerRequest, emit func(bughub.BrowserProgress)) (workerResult, error) {
	return fn(ctx, paths, request, emit)
}

func TestHostVerifierRejectsMismatchedReservationIdentity(t *testing.T) {
	request := validBrowserRequest(t)
	browserDir := filepath.Join(request.StagingDir, "browser")
	if err := os.Mkdir(browserDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browserDir, "reservation.json"), []byte(`{"case_id":"other","cycle_number":1,"attempt_id":"attempt-1","plan_sha256":"bad","state":"running","rerun_count":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &fakeWorker{Result: completedWorkerResult()}
	_, err := newTestHostVerifier(t, worker).Execute(context.Background(), request)
	if !errors.Is(err, ErrBrowserExecutionInterrupted) {
		t.Fatalf("error = %v, want identity mismatch interruption", err)
	}
	if worker.Calls != 0 {
		t.Fatalf("worker calls = %d", worker.Calls)
	}
}

func TestHostVerifierErrorDoesNotPersistRawWorkerSecret(t *testing.T) {
	worker := &fakeWorker{Result: workerResult{
		Status:              "locator_failed",
		ErrorCode:           "locator_failed",
		ErrorMessage:        "Authorization: Bearer top-secret",
		FinalScreenshotPath: "browser/failure.png",
		Artifacts:           []workerArtifact{{Kind: "screenshot", Path: "browser/failure.png"}},
	}}
	request := validBrowserRequest(t)
	result, err := newTestHostVerifier(t, worker).Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.ErrorMessage, "top-secret") {
		t.Fatalf("secret in result: %q", result.ErrorMessage)
	}
	manifest, err := os.ReadFile(filepath.Join(request.StagingDir, "browser", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "top-secret") {
		t.Fatalf("secret in manifest: %s", manifest)
	}
}

func TestHostVerifierRejectsCredentialBearingTextArtifact(t *testing.T) {
	for name, evidence := range map[string]string{
		"cookie header": `{"Cookie":"sid=raw-cookie-secret"}`,
		"token field":   `{"token":"raw-token-secret"}`,
	} {
		t.Run(name, func(t *testing.T) {
			worker := &fakeWorker{
				Result: completedWorkerResult(),
				ArtifactBytes: map[string][]byte{
					"browser/network.json": []byte(evidence),
				},
			}
			request := validBrowserRequest(t)
			if _, err := newTestHostVerifier(t, worker).Execute(context.Background(), request); err == nil {
				t.Fatal("expected raw credential evidence rejection")
			}
			if _, err := os.Stat(filepath.Join(request.StagingDir, "browser", "result.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("credential-bearing result was published: %v", err)
			}
		})
	}
}

func TestHostVerifierAcceptsExplicitRedactionMarkers(t *testing.T) {
	worker := &fakeWorker{
		Result: completedWorkerResult(),
		ArtifactBytes: map[string][]byte{
			"browser/network.json": []byte(`[{"url":"https://app.test/api?token=%5BREDACTED%5D"}]`),
		},
	}
	if _, err := newTestHostVerifier(t, worker).Execute(context.Background(), validBrowserRequest(t)); err != nil {
		t.Fatalf("explicitly redacted evidence was rejected: %v", err)
	}
}

func TestHostVerifierDoesNotMistakeActionErrorCodeForCredential(t *testing.T) {
	worker := &fakeWorker{
		Result: workerResult{
			Status:              "completed",
			FinalScreenshotPath: "browser/final.png",
			Artifacts: []workerArtifact{
				{Kind: "screenshot", Path: "browser/final.png"},
				{Kind: "browser_actions", Path: "browser/browser-actions.json"},
			},
		},
		ArtifactBytes: map[string][]byte{
			"browser/browser-actions.json": []byte(`[{"id":"wait","locator_kind":"text","error_code":"locator_failed"}]`),
		},
	}
	if _, err := newTestHostVerifier(t, worker).Execute(context.Background(), validBrowserRequest(t)); err != nil {
		t.Fatalf("safe action trace was rejected: %v", err)
	}
}

func TestRealBrowserSmoke(t *testing.T) {
	if os.Getenv("TSHOOT_BROWSER_SMOKE") != "1" {
		t.Skip("set TSHOOT_BROWSER_SMOKE=1 to install the pinned runtime and run Chromium")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api":
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("X-Request-ID", "smoke-request-1")
			_, _ = response.Write([]byte(`{"ok":true}`))
		default:
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.SetCookie(response, &http.Cookie{Name: "session", Value: "cookie-secret", Path: "/"})
			_, _ = response.Write([]byte(`<!doctype html>
<html><head><title>Browser smoke</title></head><body>
<label for="keyword">Keyword</label><input id="keyword" placeholder="Search users">
<button id="search">Search</button><div id="result"></div>
<script>
console.log('password=console-secret');
document.getElementById('search').addEventListener('click', async () => {
  const value = document.getElementById('keyword').value;
  await fetch('/api?token=network-secret', {headers: {Authorization: 'Bearer auth-secret'}});
  document.getElementById('result').textContent = 'Rendered ' + value;
});
</script></body></html>`))
		}
	}))
	defer server.Close()

	request := bughub.BrowserVerificationRequest{
		CaseID: "smoke-case", CycleNumber: 1, AttemptID: "smoke-attempt",
		SystemID: "smoke", Environment: "test", Version: "smoke-v1",
		Policy: bughub.BrowserSecurityPolicy{
			AllowedOrigins: []string{server.URL},
			PrivateOrigins: []string{server.URL},
		},
		Plan: bughub.BrowserPlan{
			Version: 1, StartURL: server.URL,
			Actions: []bughub.BrowserAction{
				{ID: "fill", Action: "fill", Locator: &bughub.BrowserLocator{Kind: "label", Value: "Keyword"}, Value: "汤圆"},
				{ID: "click", Action: "click", Locator: &bughub.BrowserLocator{Kind: "role", Value: "button", Name: "Search"}},
				{ID: "wait", Action: "wait_for", Locator: &bughub.BrowserLocator{Kind: "text", Value: "Rendered 汤圆"}},
				{ID: "shot", Action: "screenshot"},
			},
			Assertions: []bughub.BrowserAssertion{{Kind: "visible_text", Value: "Rendered 汤圆"}},
		},
		StagingDir: t.TempDir(),
	}
	verifier := NewHostVerifier(NewRuntimeManager(t.TempDir(), nil), nil, net.DefaultResolver)
	result, err := verifier.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.FinalScreenshotPath == "" {
		t.Fatalf("result = %+v", result)
	}
	finalPNG, err := os.ReadFile(filepath.Join(request.StagingDir, filepath.FromSlash(result.FinalScreenshotPath)))
	if err != nil || len(finalPNG) <= 8 {
		t.Fatalf("final screenshot is missing or empty: bytes=%d err=%v", len(finalPNG), err)
	}
	for _, artifact := range result.Artifacts {
		content, err := os.ReadFile(filepath.Join(request.StagingDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(content)
		for _, secret := range []string{"network-secret", "auth-secret", "cookie-secret", "console-secret"} {
			if strings.Contains(encoded, secret) {
				t.Fatalf("artifact %s contains %q", artifact.Path, secret)
			}
		}
		switch artifact.Kind {
		case "network":
			if !strings.Contains(encoded, "/api") || !strings.Contains(encoded, "%5BREDACTED%5D") {
				t.Fatalf("network evidence is incomplete: %s", encoded)
			}
		case "console":
			if !strings.Contains(encoded, "[REDACTED]") {
				t.Fatalf("console evidence was not redacted: %s", encoded)
			}
		case "browser_actions":
			for _, actionID := range []string{"fill", "click", "wait", "shot"} {
				if !strings.Contains(encoded, `"id": "`+actionID+`"`) {
					t.Fatalf("action trace is missing %q: %s", actionID, encoded)
				}
			}
		}
	}
}
