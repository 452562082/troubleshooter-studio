package browserverify

import (
	"context"
	"errors"
	"sync"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type hostBrowserDecisionSession struct {
	mu              sync.Mutex
	bound           *boundNodeBrowserStepSession
	transport       *nodeBrowserStepSession
	scenes          *frozenBrowserSceneFileStore
	initial         boundBrowserStepSessionInitial
	request         bughub.BrowserVerificationRequest
	browserIdentity browserDirectoryIdentity
	cleanupUploads  func() error
	releaseVerifier func()
	finished        bool
	final           bughub.BrowserVerificationResult
	closed          bool
	closeErr        error
}

func (session *hostBrowserDecisionSession) InitialBrowserDecisionScene() (bughub.BrowserScene, string) {
	if session == nil {
		return bughub.BrowserScene{}, ""
	}
	return session.initial.Scene, session.initial.SceneRef
}

func (session *hostBrowserDecisionSession) ExecuteBoundBrowserStep(ctx context.Context, request bughub.BoundBrowserStepExecutionRequest) (bughub.BoundBrowserStepExecutionResult, error) {
	if session == nil || session.bound == nil {
		return bughub.BoundBrowserStepExecutionResult{}, errors.New("browser decision Host session is unavailable")
	}
	return session.bound.ExecuteBoundBrowserStep(ctx, request)
}

func (session *hostBrowserDecisionSession) LoadFrozenBrowserScene(ctx context.Context, attemptID, reference string) ([]byte, error) {
	if session == nil || session.scenes == nil {
		return nil, errors.New("browser decision Scene store is unavailable")
	}
	return session.scenes.LoadFrozenBrowserScene(ctx, attemptID, reference)
}

func (session *hostBrowserDecisionSession) FinishBrowserDecision(ctx context.Context) (bughub.BrowserVerificationResult, error) {
	if session == nil {
		return bughub.BrowserVerificationResult{}, errors.New("browser decision Host session is unavailable")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.transport == nil {
		return bughub.BrowserVerificationResult{}, errors.New("browser decision Host session is closed")
	}
	if session.finished {
		return session.final, nil
	}
	worker, err := session.transport.Finish(ctx)
	if err != nil {
		return bughub.BrowserVerificationResult{}, err
	}
	worker, err = validateAndSanitizeWorkerStepSessionFinalResult(ctx, session.bound.resolver, session.request.Policy, worker)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_protocol_invalid", cause: err}
	}
	result := browserVerificationResult(session.request, workerResult{
		Status: worker.Status, ErrorCode: worker.ErrorCode, ErrorMessage: worker.ErrorMessage,
		FailedActionID: worker.FailedActionID, FinalURL: worker.FinalURL, Title: worker.Title,
		LoginOrigin: worker.LoginOrigin, FinalScreenshotPath: worker.FinalScreenshotPath,
		AccessibilitySummary: worker.AccessibilitySummary, Scene: worker.Scene, Artifacts: worker.Artifacts,
	})
	validation, err := validateManifestArtifacts(session.request.StagingDir, session.browserIdentity, result.Artifacts, result.Status, result.FinalScreenshotPath)
	if err != nil {
		return bughub.BrowserVerificationResult{}, browserArtifactManifestError(err)
	}
	if result.FinalScreenshotPath == "" {
		result.FinalScreenshotPath = validation.FinalScreenshot
	}
	result = bindVerifiedBrowserArtifacts(result, validation)
	session.finished = true
	session.final = result
	return result, nil
}

func (session *hostBrowserDecisionSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return session.closeErr
	}
	session.closed = true
	var closeErr error
	if session.bound != nil {
		closeErr = session.bound.Close()
	} else if session.transport != nil {
		closeErr = session.transport.Close()
	}
	if session.cleanupUploads != nil {
		closeErr = errors.Join(closeErr, session.cleanupUploads())
	}
	if session.releaseVerifier != nil {
		session.releaseVerifier()
		session.releaseVerifier = nil
	}
	session.closeErr = closeErr
	return closeErr
}

// OpenBrowserDecisionSession owns the HostVerifier mutex until Close. This
// preserves the same one-browser-at-a-time session and plaintext credential
// boundary as Execute while allowing multiple bounded steps in one process.
func (v *HostVerifier) OpenBrowserDecisionSession(ctx context.Context, request bughub.BrowserVerificationRequest) (_ bughub.BrowserDecisionHostSession, returnedErr error) {
	if v == nil {
		return nil, errors.New("browser Host verifier is unavailable")
	}
	v.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			v.mu.Unlock()
		}
	}
	defer func() {
		if returnedErr != nil {
			release()
		}
	}()
	if err := validateVerificationRequest(ctx, v.resolver, request); err != nil {
		return nil, err
	}
	if request.Policy.IsProd {
		return nil, &verifierError{code: "browser_production_interaction_blocked", cause: errors.New("autonomous browser session is disabled in production")}
	}
	if v.runtime == nil {
		return nil, &verifierError{code: "browser_runtime_missing", cause: errors.New("browser runtime manager is required")}
	}
	runtimePaths, err := v.runtime.RequireReady()
	if err != nil {
		return nil, err
	}
	browserDir, err := ensureBrowserStagingDirectory(request.StagingDir)
	if err != nil {
		return nil, &verifierError{code: "browser_artifact_staging_invalid", cause: err}
	}
	browserIdentity, err := pinBrowserDirectory(browserDir)
	if err != nil {
		return nil, &verifierError{code: "browser_artifact_staging_invalid", cause: err}
	}
	sceneStore, err := newFrozenBrowserSceneFileStore(request.StagingDir)
	if err != nil {
		return nil, &verifierError{code: "browser_artifact_staging_invalid", cause: err}
	}

	var sessionState []byte
	hasSession := false
	sessionKey := SessionKey{SystemID: request.SystemID, Environment: request.Environment, Origin: request.Plan.StartURL}
	if v.sessions != nil {
		sessionState, hasSession, err = v.sessions.Load(sessionKey)
		if err != nil {
			return nil, &verifierError{code: "browser_session_unavailable", cause: errors.New("load encrypted browser session")}
		}
	}
	storageStatePath := ""
	if hasSession {
		storageStatePath, err = createPlaintextSessionTemp(sessionKey, sessionState, true, v.removePlaintext)
		if err != nil {
			return nil, &verifierError{code: "browser_session_unavailable", cause: err}
		}
	}
	cleanupPlaintext := func() error {
		if storageStatePath == "" {
			return nil
		}
		return v.cleanupPlaintextSession(storageStatePath)
	}
	uploadPaths, cleanupUploads, err := prepareBrowserUploadInputs(request.UploadFiles)
	if err != nil {
		_ = cleanupPlaintext()
		return nil, &verifierError{code: "browser_upload_file_invalid", cause: err}
	}
	fail := func(cause error, transport *nodeBrowserStepSession) (bughub.BrowserDecisionHostSession, error) {
		var closeErr error
		if transport != nil {
			closeErr = transport.Close()
		}
		return nil, errors.Join(cause, closeErr, cleanupUploads(), cleanupPlaintext())
	}
	transport, initialWorker, err := (nodeBrowserStepSessionRunner{emit: request.Emit}).Open(ctx, runtimePaths, workerRequest{
		Mode: "step_session", Plan: executableBrowserWorkerPlan(request.Plan), Policy: executableBrowserWorkerPolicy(request.Policy), StagingDir: browserDir,
		UploadFiles: uploadPaths, StorageStatePath: storageStatePath, Headless: true,
	})
	if err != nil {
		return fail(err, nil)
	}
	if err := cleanupPlaintext(); err != nil {
		return fail(&verifierError{code: "browser_session_cleanup_failed", cause: errPlaintextSessionCleanup}, transport)
	}
	bound, initial, err := bindNodeBrowserStepSession(
		ctx, transport, initialWorker, v.resolver, request.Policy, request.AttemptID,
		func(ctx context.Context, stepNo int, _ workerStepSessionResult, scene bughub.BrowserScene) (string, error) {
			return sceneStore.FreezeBrowserScene(ctx, stepNo, scene, request.AttemptID)
		},
	)
	if err != nil {
		// bindNodeBrowserStepSession already closes transport on failure.
		return nil, errors.Join(err, cleanupUploads())
	}
	return &hostBrowserDecisionSession{
		bound: bound, transport: transport, scenes: sceneStore, initial: initial, request: request,
		browserIdentity: browserIdentity, cleanupUploads: cleanupUploads, releaseVerifier: release,
	}, nil
}

var _ bughub.BrowserDecisionSessionOpener = (*HostVerifier)(nil)
var _ bughub.BrowserDecisionHostSession = (*hostBrowserDecisionSession)(nil)
