package browserverify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type fakeWorker struct {
	Result        workerResult
	Errors        []error
	Calls         int
	ArtifactBytes map[string][]byte
	ExtraFiles    map[string][]byte
	Requests      []workerRequest
	Inspect       func(context.Context, workerRequest) error
}

func (w *fakeWorker) Run(ctx context.Context, _ RuntimePaths, request workerRequest, _ func(bughub.BrowserProgress)) (workerResult, error) {
	w.Calls++
	w.Requests = append(w.Requests, request)
	if w.Inspect != nil {
		if err := w.Inspect(ctx, request); err != nil {
			return workerResult{}, err
		}
	}
	if w.Calls <= len(w.Errors) && w.Errors[w.Calls-1] != nil {
		return workerResult{}, w.Errors[w.Calls-1]
	}
	for _, artifact := range w.Result.Artifacts {
		if request.StagingDir == "" || filepath.IsAbs(artifact.Path) || !strings.HasPrefix(artifact.Path, "browser/") {
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
			AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}, AuthOrigins: []string{"https://login.test"},
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

func TestHostVerifierExecuteUsesEncryptedSessionOnlyForWorkerLifetime(t *testing.T) {
	for _, test := range []struct {
		name       string
		workerErr  error
		cancel     bool
		wantErr    bool
		wantResult bool
	}{
		{name: "success", wantResult: true},
		{name: "worker failure", workerErr: errors.New("password=worker-secret"), wantErr: true},
		{name: "cancellation", cancel: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validBrowserRequest(t)
			state := []byte(`{"cookies":[{"name":"sid","value":"execute-cookie-secret"}]}`)
			store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
			if err := store.Save(SessionKey{SystemID: request.SystemID, Environment: request.Environment, Origin: request.Plan.StartURL}, state); err != nil {
				t.Fatal(err)
			}
			var plaintextPath string
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{test.workerErr}}
			worker.Inspect = func(_ context.Context, got workerRequest) error {
				plaintextPath = got.StorageStatePath
				if got.Mode != "execute" || !got.Headless || plaintextPath == "" || !filepath.IsAbs(plaintextPath) {
					t.Fatalf("worker request = %+v", got)
				}
				if relative, err := filepath.Rel(request.StagingDir, plaintextPath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					t.Fatalf("plaintext session is inside evidence staging: %q", plaintextPath)
				}
				if mode := mustFileMode(t, plaintextPath); mode != 0o600 {
					t.Fatalf("plaintext session mode = %o", mode)
				}
				loaded, err := os.ReadFile(plaintextPath)
				if err != nil || !bytes.Equal(loaded, state) {
					t.Fatalf("worker state=%q err=%v", loaded, err)
				}
				if test.cancel {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			verifier := newTestHostVerifier(t, worker)
			verifier.SetSessionStore(store)
			result, err := verifier.Execute(ctx, request)
			if test.wantErr != (err != nil) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.wantResult && result.Status != "completed" {
				t.Fatalf("result=%+v", result)
			}
			if plaintextPath == "" {
				t.Fatal("worker did not receive a plaintext state path")
			}
			if _, err := os.Stat(plaintextPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plaintext session remains after Execute: %v", err)
			}
			assertTreeExcludesBytes(t, request.StagingDir, state, []byte(plaintextPath))
		})
	}
}

func TestHostVerifierLoginReplacesEncryptedSessionOnlyAfterWorkerSuccess(t *testing.T) {
	root := t.TempDir()
	secrets := newMemorySecretStore()
	store := NewSessionStore(filepath.Join(root, "sessions"), secrets)
	key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
	oldState := []byte(`{"cookies":[{"value":"old-cookie-secret"}]}`)
	newState := []byte(`{"cookies":[{"value":"new-cookie-secret"}]}`)
	if err := store.Save(key, oldState); err != nil {
		t.Fatal(err)
	}
	var plaintextPath string
	worker := &fakeWorker{Result: workerResult{Status: "completed"}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		plaintextPath = request.StorageStatePath
		if request.Mode != "login" || request.Headless || request.StagingDir != "" || request.Plan.StartURL != "https://app.test" {
			t.Fatalf("login request = %+v", request)
		}
		if mode := mustFileMode(t, plaintextPath); mode != 0o600 {
			t.Fatalf("login state mode = %o", mode)
		}
		state, err := os.ReadFile(plaintextPath)
		if err != nil || !bytes.Equal(state, oldState) {
			t.Fatalf("existing state=%q err=%v", state, err)
		}
		return os.WriteFile(plaintextPath, newState, 0o600)
	}
	secrets.beforeGet = func() {
		if plaintextPath == "" {
			return
		}
		if _, err := os.Stat(plaintextPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("plaintext session still exists while encrypted Save begins: %v", err)
		}
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	if err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy: bughub.BrowserSecurityPolicy{
			AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}, AuthOrigins: []string{"https://login.test"},
		},
		Timeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plaintextPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("login plaintext remains: %v", err)
	}
	loaded, ok, err := store.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, newState) {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
	ciphertext, err := os.ReadFile(store.encryptedPath(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{oldState, newState, []byte("old-cookie-secret"), []byte("new-cookie-secret"), []byte(plaintextPath)} {
		if bytes.Contains(ciphertext, secret) {
			t.Fatalf("ciphertext exposes %q", secret)
		}
	}
	assertTreeExcludesBytes(t, root, oldState, newState, []byte(plaintextPath))
}

func TestHostVerifierLoginNavigatesToOriginalApplicationURLAndStoresApplicationOriginSession(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(filepath.Join(root, "sessions"), newMemorySecretStore())
	applicationKey := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
	authKey := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://login.test"}
	state := []byte(`{"cookies":[{"domain":".test","value":"sso-session"}]}`)
	worker := &fakeWorker{Result: workerResult{Status: "completed"}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		if request.Plan.StartURL != "https://app.test/oauth/start?state=opaque" {
			t.Fatalf("login navigation URL = %q", request.Plan.StartURL)
		}
		return os.WriteFile(request.StorageStatePath, state, 0o600)
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	if err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", ApplicationURL: "https://app.test/oauth/start?state=opaque", ApplicationOrigin: "https://app.test", LoginOrigin: "https://login.test",
		Policy: bughub.BrowserSecurityPolicy{
			AllowedOrigins: []string{"https://app.test", "https://login.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}, AuthOrigins: []string{"https://login.test"},
		},
		Timeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load(applicationKey)
	if err != nil || !found || !bytes.Equal(got, state) {
		t.Fatalf("application session=%q found=%v err=%v", got, found, err)
	}
	if _, found, err := store.Load(authKey); err != nil || found {
		t.Fatalf("auth-origin session found=%v err=%v", found, err)
	}
}

func TestHostVerifierLoginRejectsUnsafeOrInconsistentApplicationURLBeforeWorker(t *testing.T) {
	for _, test := range []struct {
		name, applicationURL, applicationOrigin string
	}{
		{name: "userinfo", applicationURL: "https://user:pass@app.test/start", applicationOrigin: "https://app.test"},
		{name: "fragment", applicationURL: "https://app.test/start#secret", applicationOrigin: "https://app.test"},
		{name: "sensitive query key", applicationURL: "https://app.test/start?token=secret", applicationOrigin: "https://app.test"},
		{name: "credential query value", applicationURL: "https://app.test/start?state=Bearer+credential", applicationOrigin: "https://app.test"},
		{name: "origin mismatch", applicationURL: "https://app.test/start", applicationOrigin: "https://other.test"},
		{name: "API origin cannot own session", applicationURL: "https://api.test/start", applicationOrigin: "https://api.test"},
		{name: "identity provider cannot own session", applicationURL: "https://login.test/start", applicationOrigin: "https://login.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
			worker := &fakeWorker{Result: workerResult{Status: "completed"}}
			verifier := newTestHostVerifier(t, worker)
			verifier.SetSessionStore(store)
			err := verifier.Login(context.Background(), BrowserLoginRequest{
				SystemID: "shop", Environment: "test", ApplicationURL: test.applicationURL, ApplicationOrigin: test.applicationOrigin, LoginOrigin: "https://login.test",
				Policy: bughub.BrowserSecurityPolicy{
					AllowedOrigins: []string{"https://app.test", "https://api.test", "https://login.test", "https://other.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}, AuthOrigins: []string{"https://login.test"},
				},
				Timeout: time.Minute,
			})
			if err == nil || worker.Calls != 0 {
				t.Fatalf("err=%v worker calls=%d", err, worker.Calls)
			}
			for _, secret := range []string{"user:pass", "secret", "Bearer", "other.test"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("login error exposed %q: %v", secret, err)
				}
			}
		})
	}
}

func TestHostVerifierLoginSerializesEmptyPlanCollectionsAsArrays(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	worker := &fakeWorker{Result: workerResult{Status: "completed"}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		var protocol struct {
			Plan struct {
				Actions    json.RawMessage `json:"actions"`
				Assertions json.RawMessage `json:"assertions"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(encoded, &protocol); err != nil {
			return err
		}
		if string(protocol.Plan.Actions) != "[]" || string(protocol.Plan.Assertions) != "[]" {
			t.Fatalf("login protocol plan=%s", encoded)
		}
		return os.WriteFile(request.StorageStatePath, []byte(`{"cookies":[],"origins":[]}`), 0o600)
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	if err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy:  bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
		Timeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostVerifierLoginFailurePreservesPreviousEncryptedSession(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
	oldState := []byte(`{"cookies":[{"value":"preserved-cookie-secret"}]}`)
	if err := store.Save(key, oldState); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.encryptedPath(key))
	if err != nil {
		t.Fatal(err)
	}
	var plaintextPath string
	worker := &fakeWorker{
		Result: workerResult{Status: "completed"},
		Errors: []error{errors.New("password=hunter2 URL=https://app.test/?token=secret")},
		Inspect: func(_ context.Context, request workerRequest) error {
			plaintextPath = request.StorageStatePath
			return os.WriteFile(plaintextPath, []byte(`{"cookies":[{"value":"untrusted-new-secret"}]}`), 0o600)
		},
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	err = verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy:  bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("expected login failure")
	}
	for _, secret := range []string{"hunter2", "token=secret", "untrusted-new-secret", plaintextPath} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("login error exposed %q: %v", secret, err)
		}
	}
	after, err := os.ReadFile(store.encryptedPath(key))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("encrypted session changed after failed login: equal=%v err=%v", bytes.Equal(before, after), err)
	}
	loaded, ok, err := store.Load(key)
	if err != nil || !ok || !bytes.Equal(loaded, oldState) {
		t.Fatalf("loaded=%q ok=%v err=%v", loaded, ok, err)
	}
	if _, err := os.Stat(plaintextPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed login plaintext remains: %v", err)
	}
}

func TestHostVerifierLoginClassifiesWorkerFailureBeforeSessionPublication(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	worker := &fakeWorker{Errors: []error{errors.New("login worker failed")}}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy: bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}}, Timeout: time.Minute,
	})
	if err == nil || RecoveryEffectOutcomeOf(err) != RecoveryEffectKnownFailedNoDurableEffect {
		t.Fatalf("login error=%v outcome=%q", err, RecoveryEffectOutcomeOf(err))
	}
	if _, found, loadErr := store.Load(SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}); loadErr != nil || found {
		t.Fatalf("session found=%v err=%v", found, loadErr)
	}
}

func TestHostVerifierLoginLeavesPostPublishSessionSaveFailureUncertain(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
	state := []byte(`{"cookies":[{"value":"published-session"}]}`)
	var publishedPath string
	store.afterPublish = func(path string) error {
		publishedPath = path
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("ciphertext was not published before injected error: %v", err)
		}
		return errors.New("injected post-publication failure")
	}
	worker := &fakeWorker{Result: workerResult{Status: "completed"}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		return os.WriteFile(request.StorageStatePath, state, 0o600)
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: key.SystemID, Environment: key.Environment, Origin: key.Origin,
		Policy: bughub.BrowserSecurityPolicy{AllowedOrigins: []string{key.Origin}, ApplicationOrigins: []string{key.Origin}, StartOrigins: []string{key.Origin}}, Timeout: time.Minute,
	})
	if err == nil || RecoveryEffectOutcomeOf(err) != RecoveryEffectOutcomeUnknown {
		t.Fatalf("login error=%v outcome=%q", err, RecoveryEffectOutcomeOf(err))
	}
	if publishedPath == "" || publishedPath != store.encryptedPath(key) {
		t.Fatalf("published path=%q want=%q", publishedPath, store.encryptedPath(key))
	}
	store.afterPublish = nil
	loaded, found, loadErr := store.Load(key)
	if loadErr != nil || !found || !bytes.Equal(loaded, state) {
		t.Fatalf("published session=%q found=%v err=%v", loaded, found, loadErr)
	}
}

func TestHostVerifierLoginReportsPlaintextCleanupFailureOnEveryExitPath(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     workerResult
		workerErr  error
		writeState []byte
		cancel     bool
	}{
		{name: "worker error", workerErr: errors.New("password=worker-secret")},
		{name: "cancellation", workerErr: context.Canceled, cancel: true},
		{name: "protocol error", result: workerResult{Status: "completed", FinalURL: "https://app.test/?token=protocol-secret"}},
		{name: "state read error", result: workerResult{Status: "completed"}, writeState: []byte(`{"cookies":[`)},
		{name: "successful worker before save", result: workerResult{Status: "completed"}, writeState: []byte(`{"cookies":[{"value":"new-secret"}]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
			key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
			oldState := []byte(`{"cookies":[{"value":"preserved-secret"}]}`)
			if err := store.Save(key, oldState); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.encryptedPath(key))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var plaintextPath string
			worker := &fakeWorker{Result: test.result, Errors: []error{test.workerErr}}
			worker.Inspect = func(_ context.Context, request workerRequest) error {
				plaintextPath = request.StorageStatePath
				if test.writeState != nil {
					if err := os.WriteFile(plaintextPath, test.writeState, 0o600); err != nil {
						return err
					}
				}
				if test.cancel {
					cancel()
				}
				return nil
			}
			verifier := newTestHostVerifier(t, worker)
			verifier.SetSessionStore(store)
			verifier.removePlaintext = func(path string) error {
				if path != plaintextPath {
					t.Fatalf("removed path %q, want %q", path, plaintextPath)
				}
				return errors.New("remove " + path + " password=cleanup-secret")
			}
			err = verifier.Login(ctx, BrowserLoginRequest{
				SystemID: "shop", Environment: "test", Origin: "https://app.test",
				Policy:  bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
				Timeout: time.Minute,
			})
			if err == nil || err.Error() != "browser_session_cleanup_failed: temporary browser session cleanup failed" {
				t.Fatalf("cleanup error = %v", err)
			}
			if outcome := RecoveryEffectOutcomeOf(err); outcome != RecoveryEffectOutcomeUnknown {
				t.Fatalf("cleanup outcome=%q", outcome)
			}
			for _, secret := range []string{plaintextPath, "cleanup-secret", "worker-secret", "protocol-secret"} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("cleanup error exposed %q: %v", secret, err)
				}
			}
			after, readErr := os.ReadFile(store.encryptedPath(key))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("encrypted session changed: equal=%v err=%v", bytes.Equal(before, after), readErr)
			}
			if plaintextPath != "" {
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(plaintextPath)) })
			}
		})
	}
}

func TestHostVerifierExecuteReportsPlaintextCleanupFailureAfterWorkerError(t *testing.T) {
	request := validBrowserRequest(t)
	state := []byte(`{"cookies":[{"value":"execute-secret"}]}`)
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	if err := store.Save(SessionKey{SystemID: request.SystemID, Environment: request.Environment, Origin: request.Plan.StartURL}, state); err != nil {
		t.Fatal(err)
	}
	var plaintextPath string
	worker := &fakeWorker{Errors: []error{errors.New("password=worker-secret")}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		plaintextPath = request.StorageStatePath
		return nil
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	verifier.removePlaintext = func(path string) error {
		return errors.New("remove " + path + " password=cleanup-secret")
	}
	_, err := verifier.Execute(context.Background(), request)
	if err == nil || err.Error() != "browser_session_cleanup_failed: temporary browser session cleanup failed" {
		t.Fatalf("cleanup error = %v", err)
	}
	for _, secret := range []string{plaintextPath, "cleanup-secret", "worker-secret"} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("cleanup error exposed %q: %v", secret, err)
		}
	}
	if plaintextPath != "" {
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(plaintextPath)) })
	}
}

func TestCreatePlaintextSessionTempPropagatesCleanupFailureAfterCreationError(t *testing.T) {
	var plaintextPath string
	_, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		bytes.Repeat([]byte{'x'}, maxBrowserSessionBytes+1),
		true,
		func(path string) error {
			plaintextPath = path
			return errors.New("remove " + path + " password=cleanup-secret")
		},
	)
	if !errors.Is(err, errPlaintextSessionCleanup) {
		t.Fatalf("create cleanup error = %v", err)
	}
	if plaintextPath == "" {
		t.Fatal("cleanup remover was not called")
	}
	for _, secret := range []string{plaintextPath, "cleanup-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("cleanup error exposed %q: %v", secret, err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(plaintextPath)) })
}

func TestHostVerifierLoginRejectsWorkerArtifactsWithoutSavingState(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	worker := &fakeWorker{Result: workerResult{
		Status:    "completed",
		Artifacts: []workerArtifact{{Kind: "screenshot", Path: "browser/login.png"}},
	}}
	worker.Inspect = func(_ context.Context, request workerRequest) error {
		return os.WriteFile(request.StorageStatePath, []byte(`{"cookies":[]}`), 0o600)
	}
	verifier := newTestHostVerifier(t, worker)
	verifier.SetSessionStore(store)
	err := verifier.Login(context.Background(), BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy:  bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
		Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("expected login artifact rejection")
	}
	if _, ok, loadErr := store.Load(SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}); loadErr != nil || ok {
		t.Fatalf("login state saved after artifact rejection: ok=%v err=%v", ok, loadErr)
	}
}

type contextBlockingResolver struct{}

func (contextBlockingResolver) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHostVerifierLoginTimeoutBoundsPolicyResolution(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	verifier := NewHostVerifier(NewRuntimeManager(t.TempDir(), &recordingCommandRunner{}), &fakeWorker{}, contextBlockingResolver{})
	verifier.SetSessionStore(store)
	outer, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := verifier.Login(outer, BrowserLoginRequest{
		SystemID: "shop", Environment: "test", Origin: "https://app.test",
		Policy:  bughub.BrowserSecurityPolicy{AllowedOrigins: []string{"https://app.test"}, ApplicationOrigins: []string{"https://app.test"}, StartOrigins: []string{"https://app.test"}},
		Timeout: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected login timeout")
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("login timeout did not bound policy resolution: %s", elapsed)
	}
}

func TestHostVerifierClearSessionRepairAndStatus(t *testing.T) {
	runtime := NewRuntimeManager(t.TempDir(), &recordingCommandRunner{})
	verifier := NewHostVerifier(runtime, &fakeWorker{}, publicResolver("app.test"))
	if status := verifier.Status(); status.State != RuntimeBroken {
		t.Fatalf("initial status = %+v", status)
	}
	if err := verifier.Repair(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if status := verifier.Status(); status.State != RuntimeReady {
		t.Fatalf("repaired status = %+v", status)
	}

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions"), newMemorySecretStore())
	key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
	if err := store.Save(key, []byte(`{"cookies":[]}`)); err != nil {
		t.Fatal(err)
	}
	verifier.SetSessionStore(store)
	if err := verifier.ClearSession(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := verifier.ClearSession(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load(key); err != nil || ok {
		t.Fatalf("cleared state: ok=%v err=%v", ok, err)
	}
}

func TestHostVerifierRepairClassifiesOnlyExplicitPreEffectFailures(t *testing.T) {
	missing := NewHostVerifier(nil, &fakeWorker{}, publicResolver("app.test"))
	if err := missing.Repair(context.Background(), nil); err == nil || RecoveryEffectOutcomeOf(err) != RecoveryEffectKnownFailedNoDurableEffect {
		t.Fatalf("missing runtime error=%v outcome=%q", err, RecoveryEffectOutcomeOf(err))
	}

	root := t.TempDir()
	busyRuntime := NewRuntimeManager(root, &recordingCommandRunner{})
	if err := os.MkdirAll(busyRuntime.runtimeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := busyRuntime.acquireInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	busy := NewHostVerifier(busyRuntime, &fakeWorker{}, publicResolver("app.test"))
	if err := busy.Repair(context.Background(), nil); err == nil || RecoveryEffectOutcomeOf(err) != RecoveryEffectKnownFailedNoDurableEffect {
		t.Fatalf("busy repair error=%v outcome=%q", err, RecoveryEffectOutcomeOf(err))
	}

	unsafeRoot := filepath.Join(t.TempDir(), "management-root-file")
	if err := os.WriteFile(unsafeRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	ambiguous := NewHostVerifier(NewRuntimeManager(unsafeRoot, &recordingCommandRunner{}), &fakeWorker{}, publicResolver("app.test"))
	if err := ambiguous.Repair(context.Background(), nil); err == nil || RecoveryEffectOutcomeOf(err) != RecoveryEffectOutcomeUnknown {
		t.Fatalf("ambiguous repair error=%v outcome=%q", err, RecoveryEffectOutcomeOf(err))
	}
}

func assertTreeExcludesBytes(t *testing.T, root string, forbidden ...[]byte) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if len(value) > 0 && bytes.Contains(content, value) {
				t.Fatalf("%s contains forbidden plaintext %q", path, value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
		if len(artifact.SHA256) != 64 || artifact.Size != int64(len(workerFixtureBytes(artifact.Kind))) {
			t.Fatalf("artifact is not bound to verified bytes: %+v", artifact)
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

func TestHostVerifierPersistsReadOnlyRerunBeforeInterruptedCleanup(t *testing.T) {
	worker := &fakeWorker{Result: completedWorkerResult(), Errors: []error{errors.New("first crash")}}
	verifier := newTestHostVerifier(t, worker)
	request := validBrowserRequest(t)
	request.Plan.Actions = []bughub.BrowserAction{
		{ID: "goto", Action: "goto", URL: "https://app.test/users"},
		{ID: "wait", Action: "wait_for", Locator: &bughub.BrowserLocator{Kind: "text", Value: "Users"}},
		{ID: "shot", Action: "screenshot"},
	}
	if _, err := verifier.Execute(context.Background(), request); err == nil {
		t.Fatal("expected initial worker crash")
	}
	cleanupCalls := 0
	verifier.cleanupInterrupted = func(_ browserDirectoryIdentity, _ string) error {
		cleanupCalls++
		return errors.New("simulated crash during cleanup")
	}
	if _, err := verifier.Execute(context.Background(), request); !errors.Is(err, ErrBrowserExecutionInterrupted) {
		t.Fatalf("cleanup interruption error = %v", err)
	}
	var reservation browserReservation
	found, err := readStrictBrowserJSON(filepath.Join(request.StagingDir, "browser", "reservation.json"), &reservation)
	if err != nil || !found {
		t.Fatalf("read reservation: found=%v err=%v", found, err)
	}
	if reservation.RerunCount != 1 || cleanupCalls != 1 {
		t.Fatalf("reservation=%+v cleanup calls=%d", reservation, cleanupCalls)
	}
	verifier.cleanupInterrupted = cleanupInterruptedBrowserOutputs
	if _, err := verifier.Execute(context.Background(), request); !errors.Is(err, ErrBrowserExecutionInterrupted) {
		t.Fatalf("post-crash retry error = %v", err)
	}
	if worker.Calls != 1 {
		t.Fatalf("worker calls = %d, cleanup crash must consume the only replay", worker.Calls)
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
		{name: "oversized evidence", result: completedWorkerResult(), worker: func(worker *fakeWorker, _ bughub.BrowserVerificationRequest) {
			worker.ArtifactBytes = map[string][]byte{"browser/network.json": make([]byte, (1<<20)+1)}
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

func TestHostVerifierRejectsAttemptArtifactTotalBeforePublishingResult(t *testing.T) {
	request := validBrowserRequest(t)
	result := workerResult{
		Status:              "completed",
		FinalScreenshotPath: "browser/three.png",
		Artifacts: []workerArtifact{
			{Kind: "screenshot", Path: "browser/one.png"},
			{Kind: "screenshot", Path: "browser/two.png"},
			{Kind: "screenshot", Path: "browser/three.png"},
		},
	}
	artifactBytes := make(map[string][]byte, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		content := make([]byte, maxBrowserAttemptArtifactBytes/3+1)
		copy(content, []byte("\x89PNG\r\n\x1a\n"))
		artifactBytes[artifact.Path] = content
	}
	worker := &fakeWorker{Result: result, ArtifactBytes: artifactBytes}
	if _, err := newTestHostVerifier(t, worker).Execute(context.Background(), request); err == nil {
		t.Fatal("expected aggregate browser artifact budget rejection")
	}
	if _, err := os.Stat(filepath.Join(request.StagingDir, "browser", "result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized aggregate result was published: %v", err)
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

func TestHostVerifierRejectsReplacedBrowserRootWithoutWritingOutsideResult(t *testing.T) {
	request := validBrowserRequest(t)
	outside := t.TempDir()
	worker := WorkerRunnerFunc(func(_ context.Context, _ RuntimePaths, workerRequest workerRequest, _ func(bughub.BrowserProgress)) (workerResult, error) {
		moved := workerRequest.StagingDir + "-original"
		if err := os.Rename(workerRequest.StagingDir, moved); err != nil {
			return workerResult{}, err
		}
		if err := os.Symlink(outside, workerRequest.StagingDir); err != nil {
			return workerResult{}, err
		}
		return completedWorkerResult(), nil
	})

	_, err := newTestHostVerifier(t, worker).Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "browser staging directory identity changed") {
		t.Fatalf("error = %v, want changed staging-directory identity rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result.json escaped through replaced browser root: %v", err)
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

func TestHostVerifierReturnsStableCodeForOversizedWorkerOutput(t *testing.T) {
	worker := WorkerRunnerFunc(func(context.Context, RuntimePaths, workerRequest, func(bughub.BrowserProgress)) (workerResult, error) {
		return workerResult{}, ErrBrowserWorkerOutputTooLarge
	})
	_, err := newTestHostVerifier(t, worker).Execute(context.Background(), validBrowserRequest(t))
	if err == nil || !strings.HasPrefix(err.Error(), "browser_worker_output_too_large:") {
		t.Fatalf("error = %v, want stable oversized-output code", err)
	}
}

func TestHostVerifierRejectsOversizedWorkerResultCollections(t *testing.T) {
	result := completedWorkerResult()
	result.Artifacts = make([]workerArtifact, 129)
	for index := range result.Artifacts {
		result.Artifacts[index] = workerArtifact{Kind: "network", Path: "browser/network.json"}
	}
	_, err := newTestHostVerifier(t, &fakeWorker{Result: result}).Execute(context.Background(), validBrowserRequest(t))
	if err == nil || !strings.HasPrefix(err.Error(), "browser_worker_protocol_invalid:") {
		t.Fatalf("error = %v, want bounded worker protocol rejection", err)
	}
}

func TestNodeWorkerRunnerKillsAndReapsChildAfterStdoutLimit(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "oversized-worker.mjs")
	markerPath := filepath.Join(temporary, "child-survived")
	source := `
import { writeFileSync } from 'node:fs';
process.stdout.write('x'.repeat((1 << 20) + (64 << 10)));
setTimeout(() => writeFileSync(process.env.TSHOOT_TEST_CHILD_MARKER, 'alive'), 500);
setInterval(() => {}, 1000);
`
	if err := os.WriteFile(workerPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TSHOOT_TEST_CHILD_MARKER", markerPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err := (nodeWorkerRunner{}).Run(ctx, RuntimePaths{
		Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
	}, workerRequest{Mode: "execute"}, nil)
	if !errors.Is(err, ErrBrowserWorkerOutputTooLarge) {
		t.Fatalf("error = %v, want ErrBrowserWorkerOutputTooLarge", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("oversized child was not terminated promptly: %s", elapsed)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker child survived after runner returned: %v", err)
	}
}

func TestNodeWorkerRunnerInterruptsWorkerForGracefulBrowserCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not implement os.Interrupt for child processes")
	}
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "interruptible-worker.mjs")
	markerPath := filepath.Join(temporary, "browser-closed")
	source := `
import { writeFileSync } from 'node:fs';
process.on('SIGTERM', () => {
  writeFileSync(process.env.TSHOOT_TEST_CLEANUP_MARKER, 'closed');
  process.exit(130);
});
setInterval(() => {}, 1000);
`
	if err := os.WriteFile(workerPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TSHOOT_TEST_CLEANUP_MARKER", markerPath)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := (nodeWorkerRunner{}).Run(ctx, RuntimePaths{
		Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
	}, workerRequest{Mode: "login"}, nil)
	if err == nil {
		t.Fatal("expected canceled worker error")
	}
	content, readErr := os.ReadFile(markerPath)
	if readErr != nil || string(content) != "closed" {
		t.Fatalf("browser cleanup marker=%q err=%v", content, readErr)
	}
}

func TestNodeWorkerRunnerCancellationTerminatesWorkerProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses direct process termination")
	}
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "worker-with-grandchild.mjs")
	readyPath := filepath.Join(temporary, "grandchild-started")
	markerPath := filepath.Join(temporary, "grandchild-survived")
	source := `
import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';
const childSource = ` + "`" + `
  const { writeFileSync } = require('node:fs');
  setTimeout(() => writeFileSync(process.env.TSHOOT_TEST_GRANDCHILD_MARKER, 'alive'), 600);
  setTimeout(() => process.exit(0), 800);
` + "`" + `;
spawn(process.execPath, ['-e', childSource], { env: process.env, stdio: 'ignore' });
writeFileSync(process.env.TSHOOT_TEST_GRANDCHILD_READY, 'ready');
process.on('SIGTERM', () => process.exit(143));
setInterval(() => {}, 1000);
`
	if err := os.WriteFile(workerPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TSHOOT_TEST_GRANDCHILD_READY", readyPath)
	t.Setenv("TSHOOT_TEST_GRANDCHILD_MARKER", markerPath)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (nodeWorkerRunner{}).Run(ctx, RuntimePaths{
			Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
		}, workerRequest{Mode: "login"}, nil)
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not start its grandchild")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("expected canceled worker error")
	}
	time.Sleep(900 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker grandchild survived cancellation: %v", err)
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
			AllowedOrigins: []string{server.URL}, ApplicationOrigins: []string{server.URL}, StartOrigins: []string{server.URL}, PrivateOrigins: []string{server.URL}, AuthOrigins: []string{},
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
		Emit: func(progress bughub.BrowserProgress) {
			t.Logf("browser progress: code=%s action=%s current=%d total=%d message=%s", progress.Code, progress.ActionID, progress.Current, progress.Total, progress.Message)
		},
	}
	runtimeManager := NewRuntimeManager(t.TempDir(), nil)
	if cacheRoot := strings.TrimSpace(os.Getenv("TSHOOT_BROWSER_SMOKE_CACHE_ROOT")); cacheRoot != "" {
		runtimeManager.SetPlaywrightBrowserCache(cacheRoot)
	}
	verifier := NewHostVerifier(runtimeManager, nil, net.DefaultResolver)
	if err := verifier.Prepare(context.Background(), nil); err != nil {
		t.Fatalf("prepare Studio browser runtime: %v", err)
	}
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
			var records []struct {
				ActionID       string `json:"action_id"`
				URL            string `json:"url"`
				RequestID      string `json:"request_id"`
				InitiatorStack []struct {
					URL    string `json:"url"`
					Line   int    `json:"line"`
					Column int    `json:"column"`
				} `json:"initiator_stack"`
			}
			if err := json.Unmarshal(content, &records); err != nil {
				t.Fatalf("network evidence is not valid JSON: %v", err)
			}
			var apiRecord *struct {
				ActionID       string `json:"action_id"`
				URL            string `json:"url"`
				RequestID      string `json:"request_id"`
				InitiatorStack []struct {
					URL    string `json:"url"`
					Line   int    `json:"line"`
					Column int    `json:"column"`
				} `json:"initiator_stack"`
			}
			for index := range records {
				if strings.Contains(records[index].URL, "/api") {
					apiRecord = &records[index]
					break
				}
			}
			if apiRecord == nil || apiRecord.ActionID != "click" || apiRecord.RequestID != "smoke-request-1" || len(apiRecord.InitiatorStack) == 0 || apiRecord.InitiatorStack[0].URL == "" || apiRecord.InitiatorStack[0].Line == 0 || apiRecord.InitiatorStack[0].Column == 0 {
				t.Fatalf("network evidence is missing its action/request/initiator binding: %+v", apiRecord)
			}
		case "console":
			if !strings.Contains(encoded, "[REDACTED]") {
				t.Fatalf("console evidence was not redacted: %s", encoded)
			}
		case "browser_actions":
			var actions []struct {
				ID     string `json:"id"`
				Result string `json:"result"`
			}
			if err := json.Unmarshal(content, &actions); err != nil {
				t.Fatalf("action trace is not valid JSON: %v", err)
			}
			completed := make(map[string]bool, len(actions))
			for _, action := range actions {
				if action.Result == "completed" {
					completed[action.ID] = true
				}
			}
			for _, actionID := range []string{"fill", "click", "wait", "shot"} {
				if !completed[actionID] {
					t.Fatalf("action trace is missing %q: %s", actionID, encoded)
				}
			}
		}
	}
}
