package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/zalando/go-keyring"
)

type fakeIncidentBrowserController struct {
	mu            sync.Mutex
	loginRequests []browserverify.BrowserLoginRequest
	clearKeys     []browserverify.SessionKey
	repairs       int
	loginErr      error
	repairErr     error
	clearErr      error
	progress      bughub.BrowserProgress
	afterLogin    func()
}

func (*fakeIncidentBrowserController) Execute(context.Context, bughub.BrowserVerificationRequest) (bughub.BrowserVerificationResult, error) {
	return bughub.BrowserVerificationResult{}, nil
}

func (f *fakeIncidentBrowserController) Login(_ context.Context, request browserverify.BrowserLoginRequest) error {
	f.mu.Lock()
	f.loginRequests = append(f.loginRequests, request)
	err := f.loginErr
	progress := f.progress
	afterLogin := f.afterLogin
	f.mu.Unlock()
	if request.Emit != nil && progress.Code != "" {
		request.Emit(progress)
	}
	if afterLogin != nil {
		afterLogin()
	}
	return err
}

func (f *fakeIncidentBrowserController) ClearSession(_ context.Context, key browserverify.SessionKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearKeys = append(f.clearKeys, key)
	return f.clearErr
}

func (f *fakeIncidentBrowserController) Repair(_ context.Context, emit func(bughub.BrowserProgress)) error {
	f.mu.Lock()
	f.repairs++
	err := f.repairErr
	progress := f.progress
	f.mu.Unlock()
	if emit != nil && progress.Code != "" {
		emit(progress)
	}
	return err
}

func (*fakeIncidentBrowserController) Status() browserverify.RuntimeStatus {
	return browserverify.RuntimeStatus{State: browserverify.RuntimeReady, Version: "1.61.1"}
}

func (f *fakeIncidentBrowserController) snapshot() (logins []browserverify.BrowserLoginRequest, clears []browserverify.SessionKey, repairs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]browserverify.BrowserLoginRequest(nil), f.loginRequests...), append([]browserverify.SessionKey(nil), f.clearKeys...), f.repairs
}

func newBrowserBindingTestApp(t *testing.T) (*App, *bughub.CaseStore) {
	t.Helper()
	app, store, _ := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "browser-bindings.db"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.workflowRoot = root
	incident := bughub.IncidentCase{
		ID: "case-a", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: bughub.CaseValidating, CycleNumber: 1, CurrentAttemptID: "attempt-a", SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	attempt := bughub.PhaseAttempt{
		ID: "attempt-a", CaseID: "case-a", CycleNumber: 1, Phase: bughub.PhaseValidation,
		Mode: bughub.AttemptReproduce, Status: bughub.AttemptStatusRunning,
		AgentTarget: "codex", BotKey: "base|codex", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`),
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	return app, store
}

func registerPNGArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID string) bughub.EvidenceArtifact {
	t.Helper()
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	content, err := base64.StdEncoding.DecodeString(onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	return registerBrowserBindingArtifact(t, app, store, caseID, attemptID, "screenshot", content, ".png")
}

func registerTextArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID string) bughub.EvidenceArtifact {
	t.Helper()
	return registerBrowserBindingArtifact(t, app, store, caseID, attemptID, "console", []byte("safe text"), ".txt")
}

func registerBrowserBindingArtifact(t *testing.T, app *App, store *bughub.CaseStore, caseID, attemptID, kind string, content []byte, suffix string) bughub.EvidenceArtifact {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source"+suffix)
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := bughub.RegisterArtifact(context.Background(), store, bughub.ArtifactInput{
		ArtifactsRoot: filepath.Join(app.workflowRoot, "artifacts"), SourcePath: source,
		CaseID: caseID, AttemptID: attemptID, Kind: kind, CapturedAt: time.Now().UTC(),
		Environment: "test", RedactionStatus: bughub.RedactionStatusNotRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestGetIncidentArtifactPreviewChecksCaseOwnershipAndPNGBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerPNGArtifact(t, app, store, "case-a", "attempt-a")

	preview, err := app.GetIncidentArtifactPreview("case-a", artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ArtifactID != artifact.ID || preview.MIMEType != "image/png" || preview.Base64Data == "" || preview.Size == 0 {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := app.GetIncidentArtifactPreview("case-b", artifact.ID); err == nil {
		t.Fatal("cross-case preview succeeded")
	}
}

func TestGetIncidentArtifactPreviewRejectsNonScreenshotAndChangedBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	text := registerTextArtifact(t, app, store, "case-a", "attempt-a")
	if _, err := app.GetIncidentArtifactPreview("case-a", text.ID); err == nil {
		t.Fatal("text preview succeeded")
	}

	screenshot := registerPNGArtifact(t, app, store, "case-a", "attempt-a")
	if err := os.WriteFile(screenshot.PathOrReference, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetIncidentArtifactPreview("case-a", screenshot.ID); err == nil {
		t.Fatal("changed artifact preview succeeded")
	}
}

func TestSaveIncidentArtifactWritesOnlyVerifiedRegisteredBytes(t *testing.T) {
	app, store := newBrowserBindingTestApp(t)
	artifact := registerTextArtifact(t, app, store, "case-a", "attempt-a")
	destination := filepath.Join(t.TempDir(), "saved-console.txt")
	app.workflowSaveArtifact = func(_, _ string, _ context.Context) (string, error) {
		return destination, nil
	}

	saved, err := app.SaveIncidentArtifact("case-a", artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if saved != destination || string(content) != "safe text" {
		t.Fatalf("saved=%q content=%q", saved, content)
	}
	if _, err := app.SaveIncidentArtifact("case-other", artifact.ID); err == nil {
		t.Fatal("cross-case save succeeded")
	}
}

func newBrowserRecoveryBindingApp(t *testing.T, phase bughub.Phase, errorCode, loginOrigin string) (*App, *bughub.CaseStore, *workflowBindingRunner, *fakeIncidentBrowserController, bughub.IncidentCase, bughub.PhaseAttempt) {
	t.Helper()
	app, store, runner := newWorkflowBindingApp(t, filepath.Join(t.TempDir(), "browser-recovery.db"))
	controller := &fakeIncidentBrowserController{}
	app.workflowBrowser = controller
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		return bughub.Bug{ID: id, Source: "zentao", Title: "checkout fails", Env: "test", SystemID: "base", FrontendURL: "https://app.test/users"}, nil
	}
	app.workflowLoadDeploymentConfig = func(context.Context, bughub.IncidentCase) (*config.SystemConfig, error) {
		return &config.SystemConfig{
			System: config.System{ID: "base"},
			Environments: []config.Environment{{
				ID: "test", WebDomain: "HTTPS://App.Test:443", APIDomain: "http://127.0.0.1:3000",
				BrowserAuthOrigins: []string{"https://LOGIN.Test:443"}, IsProd: false,
			}},
		}, nil
	}
	attemptID := "attempt-browser-blocked"
	status := bughub.CaseWaitingEvidence
	mode := bughub.AttemptReproduce
	if phase == bughub.PhaseRegression {
		mode = bughub.AttemptRegression
	}
	incident := bughub.IncidentCase{
		ID: "case-browser-recovery", BugID: "bug-1", Source: "zentao", SystemID: "base", Environment: "test",
		Status: status, CycleNumber: 2, CurrentAttemptID: attemptID, SelectedBotKey: "base|codex",
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	output, err := json.Marshal(map[string]any{"error_code": errorCode, "application_origin": "https://app.test", "login_origin": loginOrigin})
	if err != nil {
		t.Fatal(err)
	}
	attempt := bughub.PhaseAttempt{
		ID: attemptID, CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: phase, Mode: mode,
		Status: bughub.AttemptStatusFailed, AgentTarget: "codex", BotKey: incident.SelectedBotKey,
		InputJSON: []byte(`{}`), OutputJSON: output, FinishedAt: &finished,
		ErrorCode: errorCode, ErrorMessage: "safe browser stop",
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	incident, err = store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	return app, store, runner, controller, incident, attempt
}

func browserCommandInput(incident bughub.IncidentCase, attempt bughub.PhaseAttempt, key string) IncidentBrowserCommandInput {
	return IncidentBrowserCommandInput{
		CaseID: incident.ID, AttemptID: attempt.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: key, ActorID: "desktop-user",
	}
}

func browserRecoveryOperationRequest(input IncidentBrowserCommandInput, attempt bughub.PhaseAttempt, operation bughub.BrowserRecoveryOperationKind, expectedCode string) bughub.BrowserRecoveryOperationRequest {
	return bughub.BrowserRecoveryOperationRequest{
		Operation: operation, CaseID: input.CaseID, AttemptID: input.AttemptID,
		ExpectedErrorCode: expectedCode, CycleNumber: attempt.CycleNumber,
		ExpectedVersion: input.ExpectedVersion, ActorID: input.ActorID, IdempotencyKey: input.IdempotencyKey,
	}
}

func TestCaseBrowserPolicyResolverCanonicalizesConfiguredOrigins(t *testing.T) {
	app, _, _, _, incident, _ := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	policy, err := (caseBrowserPolicyResolver{app: app}).ResolveBrowserPolicy(context.Background(), incident, bughub.Bug{FrontendURL: "http://10.0.0.2:8080/private"})
	if err != nil {
		t.Fatal(err)
	}
	wantAllowed := []string{"http://127.0.0.1:3000", "https://app.test", "https://login.test"}
	if !reflect.DeepEqual(policy.AllowedOrigins, wantAllowed) || !reflect.DeepEqual(policy.PrivateOrigins, wantAllowed) || !reflect.DeepEqual(policy.AuthOrigins, []string{"https://login.test"}) || policy.IsProd {
		t.Fatalf("policy = %+v", policy)
	}
	for _, origin := range policy.AllowedOrigins {
		if origin == "http://10.0.0.2:8080" {
			t.Fatal("Bug input expanded configured private-origin authority")
		}
	}
}

func TestOpenIncidentBrowserLoginContinuesOnceAndReplaysWithoutSecondLogin(t *testing.T) {
	app, store, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	input := browserCommandInput(incident, attempt, "browser-login:case-browser-recovery")

	continued, err := app.OpenIncidentBrowserLogin(input)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.GetAttempt(context.Background(), continued.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	logins, _, _ := controller.snapshot()
	if continued.Status != bughub.CaseValidating || continued.CycleNumber != incident.CycleNumber || child.ParentAttemptID != attempt.ID || child.Phase != attempt.Phase || runner.count() != 1 || len(logins) != 1 {
		t.Fatalf("continued=%+v child=%+v starts=%d logins=%d", continued, child, runner.count(), len(logins))
	}
	if logins[0].ApplicationOrigin != "https://app.test" || logins[0].LoginOrigin != "https://login.test" || logins[0].SystemID != incident.SystemID || logins[0].Environment != incident.Environment {
		t.Fatalf("login request = %+v", logins[0])
	}

	replayed, err := app.OpenIncidentBrowserLogin(input)
	if err != nil {
		t.Fatal(err)
	}
	logins, _, _ = controller.snapshot()
	if replayed.CurrentAttemptID != continued.CurrentAttemptID || runner.count() != 1 || len(logins) != 1 {
		t.Fatalf("replayed=%+v starts=%d logins=%d", replayed, runner.count(), len(logins))
	}
}

func TestIncidentBrowserRecoveryRejectsWrongAttemptAndErrorCode(t *testing.T) {
	app, _, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	wrongAttempt := browserCommandInput(incident, attempt, "browser-login:wrong-attempt")
	wrongAttempt.AttemptID = "attempt-other"
	if _, err := app.OpenIncidentBrowserLogin(wrongAttempt); err == nil {
		t.Fatal("login accepted a non-current attempt")
	}
	if _, err := app.RepairIncidentBrowserRuntime(browserCommandInput(incident, attempt, "browser-repair:wrong-code")); err == nil {
		t.Fatal("repair accepted a login-required attempt")
	}
	logins, clears, repairs := controller.snapshot()
	if len(logins) != 0 || len(clears) != 0 || repairs != 0 || runner.count() != 0 {
		t.Fatalf("logins=%d clears=%d repairs=%d starts=%d", len(logins), len(clears), repairs, runner.count())
	}
}

func TestRepairIncidentBrowserRuntimeContinuesOnceAndReplaysWithoutSecondRepair(t *testing.T) {
	app, _, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_runtime_broken", "")
	input := browserCommandInput(incident, attempt, "browser-repair:case-browser-recovery")
	continued, err := app.RepairIncidentBrowserRuntime(input)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status != bughub.CaseValidating || continued.CycleNumber != incident.CycleNumber {
		t.Fatalf("continued = %+v", continued)
	}
	if _, err := app.RepairIncidentBrowserRuntime(input); err != nil {
		t.Fatal(err)
	}
	_, _, repairs := controller.snapshot()
	if repairs != 1 || runner.count() != 1 {
		t.Fatalf("repairs=%d starts=%d", repairs, runner.count())
	}
}

func TestIncidentBrowserRecoveryFailsClosedWhenSafeOutcomeCannotBeRecorded(t *testing.T) {
	for _, test := range []struct {
		name        string
		errorCode   string
		operation   bughub.BrowserRecoveryOperationKind
		run         func(*App, IncidentBrowserCommandInput) error
		effectCount func(*fakeIncidentBrowserController) int
	}{
		{name: "login", errorCode: "browser_login_required", operation: bughub.BrowserRecoveryLogin, run: func(app *App, input IncidentBrowserCommandInput) error {
			_, err := app.OpenIncidentBrowserLogin(input)
			return err
		}, effectCount: func(controller *fakeIncidentBrowserController) int {
			logins, _, _ := controller.snapshot()
			return len(logins)
		}},
		{name: "repair", errorCode: "browser_runtime_broken", operation: bughub.BrowserRecoveryRepair, run: func(app *App, input IncidentBrowserCommandInput) error {
			_, err := app.RepairIncidentBrowserRuntime(input)
			return err
		}, effectCount: func(controller *fakeIncidentBrowserController) int {
			_, _, repairs := controller.snapshot()
			return repairs
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, store, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, test.errorCode, "https://login.test")
			input := browserCommandInput(incident, attempt, "browser-safe-outcome:"+test.name)
			app.workflowBrowserRecoveryBeforeOutcome = func() error { return errors.New("injected outcome crash") }
			if err := test.run(app, input); err == nil {
				t.Fatal("expected durable outcome failure")
			}
			operation, found, err := store.GetBrowserRecoveryOperation(context.Background(), browserRecoveryOperationRequest(input, attempt, test.operation, test.errorCode))
			if err != nil || !found || operation.Status != bughub.BrowserRecoveryClaimed || test.effectCount(controller) != 1 || runner.count() != 0 {
				t.Fatalf("operation=%+v found=%v err=%v effects=%d starts=%d", operation, found, err, test.effectCount(controller), runner.count())
			}
			if err := test.run(app, input); !errors.Is(err, bughub.ErrBrowserRecoveryOutcomeUncertain) {
				t.Fatalf("retry error=%v", err)
			}
			if test.effectCount(controller) != 1 || runner.count() != 0 {
				t.Fatalf("retry repeated effect=%d starts=%d", test.effectCount(controller), runner.count())
			}
		})
	}
}

func TestIncidentBrowserRecoveryRetriesOnlyContinuationAfterFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		errorCode   string
		operation   bughub.BrowserRecoveryOperationKind
		run         func(*App, IncidentBrowserCommandInput) (bughub.IncidentCase, error)
		effectCount func(*fakeIncidentBrowserController) int
	}{
		{name: "login", errorCode: "browser_login_required", operation: bughub.BrowserRecoveryLogin, run: func(app *App, input IncidentBrowserCommandInput) (bughub.IncidentCase, error) {
			return app.OpenIncidentBrowserLogin(input)
		}, effectCount: func(controller *fakeIncidentBrowserController) int {
			logins, _, _ := controller.snapshot()
			return len(logins)
		}},
		{name: "repair", errorCode: "browser_runtime_broken", operation: bughub.BrowserRecoveryRepair, run: func(app *App, input IncidentBrowserCommandInput) (bughub.IncidentCase, error) {
			return app.RepairIncidentBrowserRuntime(input)
		}, effectCount: func(controller *fakeIncidentBrowserController) int {
			_, _, repairs := controller.snapshot()
			return repairs
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, store, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, test.errorCode, "https://login.test")
			input := browserCommandInput(incident, attempt, "browser-continuation-retry:"+test.name)
			app.workflowBrowserRecoveryBeforeContinuation = func() error { return errors.New("injected continuation failure") }
			if _, err := test.run(app, input); err == nil {
				t.Fatal("expected continuation failure")
			}
			operation, found, err := store.GetBrowserRecoveryOperation(context.Background(), browserRecoveryOperationRequest(input, attempt, test.operation, test.errorCode))
			if err != nil || !found || operation.Status != bughub.BrowserRecoveryEffectSucceeded || test.effectCount(controller) != 1 || runner.count() != 0 {
				t.Fatalf("operation=%+v found=%v err=%v effects=%d starts=%d", operation, found, err, test.effectCount(controller), runner.count())
			}
			app.workflowBrowserRecoveryBeforeContinuation = nil
			continued, err := test.run(app, input)
			if err != nil {
				t.Fatal(err)
			}
			operation, found, err = store.GetBrowserRecoveryOperation(context.Background(), browserRecoveryOperationRequest(input, attempt, test.operation, test.errorCode))
			if err != nil || !found || operation.Status != bughub.BrowserRecoveryContinued || operation.ResultCase.CurrentAttemptID != continued.CurrentAttemptID || test.effectCount(controller) != 1 || runner.count() != 1 {
				t.Fatalf("continued=%+v operation=%+v found=%v err=%v effects=%d starts=%d", continued, operation, found, err, test.effectCount(controller), runner.count())
			}
		})
	}
}

func TestIncidentBrowserRecoveryDoesNotMistakeOrdinaryEvidenceContinuationForReplay(t *testing.T) {
	app, _, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	input := browserCommandInput(incident, attempt, "ordinary-evidence-key")
	if _, err := app.ContinueIncidentCase(ContinueIncidentCaseInput{
		CaseID: input.CaseID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey,
		ActorID: input.ActorID, Phase: attempt.Phase, InputJSON: map[string]any{"ordinary": "evidence"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenIncidentBrowserLogin(input); !errors.Is(err, bughub.ErrIdempotencyConflict) {
		t.Fatalf("browser recovery accepted ordinary continuation: %v", err)
	}
	logins, _, _ := controller.snapshot()
	if len(logins) != 0 || runner.count() != 1 {
		t.Fatalf("logins=%d starts=%d", len(logins), runner.count())
	}
}

func TestIncidentBrowserLoginReservationBlocksConcurrentCaseMutation(t *testing.T) {
	app, store, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	input := browserCommandInput(incident, attempt, "browser-version-conflict")
	var concurrentErr error
	controller.afterLogin = func() {
		controller.mu.Lock()
		controller.afterLogin = nil
		controller.mu.Unlock()
		_, concurrentErr = store.ApplyCaseMutation(context.Background(), bughub.CaseMutation{
			CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "concurrent-browser-audit",
			RequestJSON: []byte(`{"reason":"concurrent update"}`),
			Steps: []bughub.CaseMutationStep{{To: bughub.CaseWaitingEvidence, AuditOnly: true, Event: bughub.TransitionEvent{
				ID: "event-concurrent-browser-audit", EventType: "manual_evidence_noted", ActorType: "user", ActorID: "other-user",
				PayloadJSON: []byte(`{"reason":"concurrent update"}`),
			}}},
		})
	}
	first, err := app.OpenIncidentBrowserLogin(input)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(concurrentErr, bughub.ErrBrowserRecoveryReserved) {
		t.Fatalf("concurrent error=%v", concurrentErr)
	}
	replayed, err := app.OpenIncidentBrowserLogin(input)
	if err != nil {
		t.Fatal(err)
	}
	logins, _, _ := controller.snapshot()
	if len(logins) != 1 || runner.count() != 1 || replayed.CurrentAttemptID != first.CurrentAttemptID {
		t.Fatalf("first=%+v replayed=%+v logins=%d starts=%d", first, replayed, len(logins), runner.count())
	}
}

func TestIncidentBrowserLoginDoesNotRunWhenCaseDriftsBeforeAtomicClaim(t *testing.T) {
	app, store, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	baseLoader := app.workflowLoadDeploymentConfig
	mutated := false
	app.workflowLoadDeploymentConfig = func(ctx context.Context, current bughub.IncidentCase) (*config.SystemConfig, error) {
		if !mutated {
			mutated = true
			if _, err := store.ApplyCaseMutation(ctx, bughub.CaseMutation{
				CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "advance-during-browser-policy",
				RequestJSON: []byte(`{"advance":true}`),
				Steps: []bughub.CaseMutationStep{{To: bughub.CaseWaitingEvidence, AuditOnly: true, Event: bughub.TransitionEvent{
					ID: "advance-during-browser-policy", EventType: "ordinary_evidence_noted", ActorType: "user", ActorID: "other-user", PayloadJSON: []byte(`{}`),
				}}},
			}); err != nil {
				return nil, err
			}
		}
		return baseLoader(ctx, current)
	}
	if _, err := app.OpenIncidentBrowserLogin(browserCommandInput(incident, attempt, "browser-claim-state-drift")); err == nil {
		t.Fatal("login accepted Case state drift before its durable claim")
	}
	logins, _, _ := controller.snapshot()
	if len(logins) != 0 || runner.count() != 0 {
		t.Fatalf("logins=%d starts=%d", len(logins), runner.count())
	}
}

func TestIncidentBrowserRecoveryCollisionDoesNotExposeCallerKey(t *testing.T) {
	app, store, _, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	secretKey := "Cookie=session-secret Authorization=Bearer-secret password=hunter2 storageState=private"
	input := browserCommandInput(incident, attempt, secretKey)
	request := browserRecoveryOperationRequest(input, attempt, bughub.BrowserRecoveryLogin, "browser_login_required")
	request.ActorID = "different-user"
	if _, acquired, err := store.ClaimBrowserRecoveryOperation(context.Background(), request, "claim-secret-collision"); err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	var emitted []any
	app.workflowEmit = func(_ string, payload any) { emitted = append(emitted, payload) }
	_, err := app.OpenIncidentBrowserLogin(input)
	if !errors.Is(err, bughub.ErrIdempotencyConflict) {
		t.Fatalf("collision error=%v", err)
	}
	encoded, marshalErr := json.Marshal(map[string]any{"error": fmt.Sprint(err), "events": emitted})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, secret := range []string{"session-secret", "Bearer-secret", "hunter2", "storageState", "Cookie", "Authorization", "password"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("browser collision exposed %q: %s", secret, encoded)
		}
	}
	logins, _, _ := controller.snapshot()
	if len(logins) != 0 {
		t.Fatalf("login calls=%d", len(logins))
	}
}

func TestIncidentBrowserLoginContextReloadFailureRetriesOnlyContinuation(t *testing.T) {
	app, _, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	input := browserCommandInput(incident, attempt, "browser-context-retry")
	calls := 0
	app.workflowLoadBug = func(id string) (bughub.Bug, error) {
		calls++
		if calls == 2 {
			return bughub.Bug{}, errors.New("temporary context load failure")
		}
		return bughub.Bug{ID: id, Source: "zentao", Title: "checkout fails", Env: "test", SystemID: "base", FrontendURL: "https://app.test/users"}, nil
	}
	if _, err := app.OpenIncidentBrowserLogin(input); err == nil {
		t.Fatal("expected context reload failure")
	}
	continued, err := app.OpenIncidentBrowserLogin(input)
	if err != nil {
		t.Fatal(err)
	}
	logins, _, _ := controller.snapshot()
	if len(logins) != 1 || runner.count() != 1 || continued.Status != bughub.CaseValidating {
		t.Fatalf("continued=%+v logins=%d starts=%d context_calls=%d", continued, len(logins), runner.count(), calls)
	}
}

func TestClearIncidentBrowserSessionIsIdempotentAndDoesNotMutateCase(t *testing.T) {
	app, store, _, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	before, err := json.Marshal(incident)
	if err != nil {
		t.Fatal(err)
	}
	input := browserCommandInput(incident, attempt, "browser-clear:case-browser-recovery")
	if err := app.ClearIncidentBrowserSession(input); err != nil {
		t.Fatal(err)
	}
	if err := app.ClearIncidentBrowserSession(input); err != nil {
		t.Fatal(err)
	}
	afterCase, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(afterCase)
	if err != nil {
		t.Fatal(err)
	}
	_, clears, _ := controller.snapshot()
	if len(clears) != 2 || clears[0].Origin != "https://app.test" || string(before) != string(after) {
		t.Fatalf("clears=%+v before=%s after=%s", clears, before, after)
	}
}

func TestClearIncidentBrowserSessionRejectsOriginOutsideEnvironmentConfig(t *testing.T) {
	app, _, _, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "http://10.0.0.2:8080")
	if err := app.ClearIncidentBrowserSession(browserCommandInput(incident, attempt, "browser-clear:unconfigured")); err == nil {
		t.Fatal("clear accepted an unconfigured origin")
	}
	_, clears, _ := controller.snapshot()
	if len(clears) != 0 {
		t.Fatalf("clear calls = %d", len(clears))
	}
}

func TestIncidentBrowserRecoveryRedactsControllerErrorsAndProgress(t *testing.T) {
	app, _, runner, controller, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	secret := "storageState Cookie: sid=secret Authorization: Bearer abc.def.ghi password=hunter2"
	controller.loginErr = errors.New(secret)
	controller.progress = bughub.BrowserProgress{Code: "browser_password_hunter2", Message: secret, ActionID: secret, Current: 1, Total: 2}
	var emitted []any
	app.workflowEmit = func(_ string, payload any) { emitted = append(emitted, payload) }

	_, err := app.OpenIncidentBrowserLogin(browserCommandInput(incident, attempt, "browser-login:redaction"))
	if err == nil {
		t.Fatal("expected login failure")
	}
	encoded, marshalErr := json.Marshal(emitted)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	combined := err.Error() + string(encoded)
	for _, forbidden := range []string{"storageState", "Cookie", "Authorization", "hunter2", "abc.def.ghi"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("secret %q leaked in %q", forbidden, combined)
		}
	}
	if runner.count() != 0 {
		t.Fatalf("continuation starts = %d", runner.count())
	}
}

func TestIncidentBrowserLoginRedactsIncidentContextFailure(t *testing.T) {
	app, _, runner, _, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	secret := "storageState Cookie: sid=secret Authorization: Bearer abc.def.ghi password=hunter2"
	app.workflowLoadBug = func(string) (bughub.Bug, error) { return bughub.Bug{}, errors.New(secret) }

	_, err := app.OpenIncidentBrowserLogin(browserCommandInput(incident, attempt, "browser-login:context-redaction"))
	if err == nil {
		t.Fatal("expected incident context failure")
	}
	for _, forbidden := range []string{"storageState", "Cookie", "Authorization", "hunter2", "abc.def.ghi"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("secret %q leaked in %q", forbidden, err)
		}
	}
	if runner.count() != 0 {
		t.Fatalf("continuation starts = %d", runner.count())
	}
}

func TestIncidentBrowserLoginRedactsContinuationRunnerFailure(t *testing.T) {
	app, _, runner, _, incident, attempt := newBrowserRecoveryBindingApp(t, bughub.PhaseValidation, "browser_login_required", "https://login.test")
	secret := "storageState Cookie: sid=secret Authorization: Bearer abc.def.ghi password=hunter2"
	runner.startErr = errors.New(secret)
	_, err := app.OpenIncidentBrowserLogin(browserCommandInput(incident, attempt, "browser-login:runner-redaction"))
	if err == nil {
		t.Fatal("expected continuation failure")
	}
	for _, forbidden := range []string{"storageState", "Cookie", "Authorization", "hunter2", "abc.def.ghi"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("secret %q leaked in %q", forbidden, err)
		}
	}
}

func TestIncidentBrowserKeyringStoreUsesDedicatedServiceAndMapsMissingKeys(t *testing.T) {
	var services []string
	store := incidentBrowserKeyringStore{
		get: func(service, user string) (string, error) {
			services = append(services, service+":"+user)
			return "", keyring.ErrNotFound
		},
		set: func(service, user, value string) error {
			services = append(services, service+":"+user+":"+value)
			return nil
		},
		delete: func(service, user string) error {
			services = append(services, service+":"+user)
			return keyring.ErrNotFound
		},
	}
	if _, err := store.Get("session-id"); !errors.Is(err, browserverify.ErrSecretNotFound) {
		t.Fatalf("get error = %v", err)
	}
	if err := store.Set("session-id", "encrypted-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("session-id"); !errors.Is(err, browserverify.ErrSecretNotFound) {
		t.Fatalf("delete error = %v", err)
	}
	want := []string{
		"tshoot-studio-browser-session:session-id",
		"tshoot-studio-browser-session:session-id:encrypted-key",
		"tshoot-studio-browser-session:session-id",
	}
	if !reflect.DeepEqual(services, want) {
		t.Fatalf("services = %v", services)
	}
}
