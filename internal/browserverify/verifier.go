package browserverify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const maxBrowserArtifactBytes = 16 << 20
const maxBrowserEvidenceBytes = 1 << 20
const maxBrowserWorkerOutputBytes = 1 << 20
const maxBrowserWorkerStderrBytes = 1 << 20
const maxBrowserWorkerProgressLines = 1000
const maxBrowserWorkerProgressLineBytes = 64 << 10

const browserProgressPrefix = "TSHOOT_BROWSER_PROGRESS "

var ErrBrowserExecutionInterrupted = errors.New("browser execution was interrupted")
var ErrBrowserStagingIdentityChanged = errors.New("browser staging directory identity changed")
var ErrBrowserWorkerOutputTooLarge = errors.New("browser worker output exceeds its limit")

var errPlaintextSessionCleanup = errors.New("temporary browser session cleanup failed")

var verifierCredentialPattern = regexp.MustCompile(`(?i)(?:["']?(?:authorization|proxy-authorization|set-cookie|cookie)["']?\s*:)|\bbearer\s+[A-Za-z0-9._~+/=-]{3,}|(?:^|[?&;,\s{])["']?(?:password|passwd|access[_-]?token|token|api[_-]?key|client[_-]?secret|secret|session|authorization|auth|cookie|code|key)["']?\s*[:=]\s*["']?[^\s&,;}"']+`)
var verifierSensitiveQueryKey = regexp.MustCompile(`(?i)token|password|secret|code|session|auth|cookie|key`)
var verifierRedactionMarker = regexp.MustCompile(`(?i)(?:\[REDACTED\]|%5B(?:REDACTED|redacted)%5D)`)

type WorkerRunner interface {
	Run(context.Context, RuntimePaths, workerRequest, func(bughub.BrowserProgress)) (workerResult, error)
}

type workerRequest struct {
	Mode             string                       `json:"mode"`
	Plan             bughub.BrowserPlan           `json:"plan"`
	Policy           bughub.BrowserSecurityPolicy `json:"policy"`
	StagingDir       string                       `json:"staging_dir"`
	StorageStatePath string                       `json:"storage_state_path,omitempty"`
	Headless         bool                         `json:"headless"`
}

type workerArtifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
}

type workerResult struct {
	Status               string                            `json:"status"`
	ErrorCode            string                            `json:"error_code,omitempty"`
	ErrorMessage         string                            `json:"error_message,omitempty"`
	FailedActionID       string                            `json:"failed_action_id,omitempty"`
	FinalURL             string                            `json:"final_url,omitempty"`
	Title                string                            `json:"title,omitempty"`
	LoginOrigin          string                            `json:"login_origin,omitempty"`
	FinalScreenshotPath  string                            `json:"final_screenshot_path,omitempty"`
	AccessibilitySummary []bughub.BrowserAccessibilityNode `json:"accessibility_summary,omitempty"`
	Artifacts            []workerArtifact                  `json:"artifacts"`
}

type HostVerifier struct {
	runtime            *RuntimeManager
	worker             WorkerRunner
	resolver           IPResolver
	sessions           *SessionStore
	mu                 sync.Mutex
	cleanupInterrupted func(browserDirectoryIdentity, string) error
	removePlaintext    func(string) error
}

type BrowserLoginRequest struct {
	SystemID          string
	Environment       string
	ApplicationOrigin string
	LoginOrigin       string
	// Origin is retained for same-origin internal callers. New recovery callers
	// must provide ApplicationOrigin and LoginOrigin separately.
	Origin  string
	Policy  bughub.BrowserSecurityPolicy
	Timeout time.Duration
	Emit    func(bughub.BrowserProgress)
}

type browserDirectoryIdentity struct {
	path string
	info os.FileInfo
}

type browserReservation struct {
	CaseID      string `json:"case_id"`
	CycleNumber int    `json:"cycle_number"`
	AttemptID   string `json:"attempt_id"`
	PlanSHA256  string `json:"plan_sha256"`
	State       string `json:"state"`
	RerunCount  int    `json:"rerun_count"`
}

type browserResultManifest struct {
	CaseID         string                           `json:"case_id"`
	CycleNumber    int                              `json:"cycle_number"`
	AttemptID      string                           `json:"attempt_id"`
	PlanSHA256     string                           `json:"plan_sha256"`
	ArtifactSHA256 map[string]string                `json:"artifact_sha256"`
	Result         bughub.BrowserVerificationResult `json:"result"`
}

type manifestArtifactValidation struct {
	FinalScreenshot string
	SHA256          map[string]string
	Size            map[string]int64
}

type verifierError struct {
	code  string
	cause error
}

func (e *verifierError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return e.code
	}
	return e.code + ": " + e.cause.Error()
}

func (e *verifierError) Unwrap() error { return e.cause }

func NewHostVerifier(runtime *RuntimeManager, worker WorkerRunner, resolver IPResolver) *HostVerifier {
	if worker == nil {
		worker = nodeWorkerRunner{}
	}
	return &HostVerifier{
		runtime: runtime, worker: worker, resolver: resolver,
		cleanupInterrupted: cleanupInterruptedBrowserOutputs,
		removePlaintext:    os.Remove,
	}
}

func (v *HostVerifier) SetSessionStore(store *SessionStore) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sessions = store
}

func (v *HostVerifier) Repair(ctx context.Context, emit func(bughub.BrowserProgress)) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.runtime == nil {
		return KnownFailedRecoveryEffect(&verifierError{code: "browser_runtime_missing", cause: errors.New("browser runtime manager is required")})
	}
	_, err := v.runtime.Repair(ctx, emit)
	return err
}

func (v *HostVerifier) Status() RuntimeStatus {
	if v.runtime == nil {
		return RuntimeStatus{State: RuntimeBroken, Version: browserRuntimeVersion, ErrorCode: "browser_runtime_missing", Message: "browser runtime manager is required"}
	}
	return v.runtime.Status()
}

func (v *HostVerifier) ClearSession(_ context.Context, key SessionKey) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sessions == nil {
		return nil
	}
	return v.sessions.Clear(key)
}

func (v *HostVerifier) Execute(ctx context.Context, request bughub.BrowserVerificationRequest) (bughub.BrowserVerificationResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := validateVerificationRequest(ctx, v.resolver, request); err != nil {
		return bughub.BrowserVerificationResult{}, err
	}
	planSHA, err := browserPlanSHA256(request.Plan)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_plan_invalid", cause: err}
	}
	browserDir, err := ensureBrowserStagingDirectory(request.StagingDir)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
	}
	browserIdentity, err := pinBrowserDirectory(browserDir)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
	}
	reservationIdentity := browserReservation{
		CaseID: request.CaseID, CycleNumber: request.CycleNumber, AttemptID: request.AttemptID,
		PlanSHA256: planSHA, State: "running",
	}
	resultPath := filepath.Join(browserDir, "result.json")
	if err := browserIdentity.Verify(); err != nil {
		return bughub.BrowserVerificationResult{}, unsafeBrowserJournalError(err)
	}
	if manifest, found, err := readBrowserResultManifest(resultPath); err != nil {
		return bughub.BrowserVerificationResult{}, interruptedError("completed browser result manifest is invalid")
	} else if found {
		if !manifestMatches(manifest.CaseID, manifest.CycleNumber, manifest.AttemptID, manifest.PlanSHA256, reservationIdentity) {
			return bughub.BrowserVerificationResult{}, interruptedError("completed browser result belongs to a different attempt or plan")
		}
		validation, err := validateManifestArtifacts(request.StagingDir, browserIdentity, manifest.Result.Artifacts, manifest.Result.Status, manifest.Result.FinalScreenshotPath)
		if err != nil {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
		}
		if !artifactDigestsEqual(validation.SHA256, manifest.ArtifactSHA256) {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: errors.New("browser artifact digest changed after completion")}
		}
		return bindVerifiedBrowserArtifacts(manifest.Result, validation), nil
	}

	reservationPath := filepath.Join(browserDir, "reservation.json")
	if err := browserIdentity.Verify(); err != nil {
		return bughub.BrowserVerificationResult{}, unsafeBrowserJournalError(err)
	}
	reservation, hasReservation, err := readBrowserReservation(reservationPath)
	if err != nil {
		return bughub.BrowserVerificationResult{}, interruptedError("browser reservation is invalid")
	}
	rerunCount := 0
	if hasReservation {
		if !reservationMatches(reservation, reservationIdentity) || reservation.State != "running" {
			return bughub.BrowserVerificationResult{}, interruptedError("browser reservation belongs to a different attempt or plan")
		}
		if !browserPlanCanReplay(request.Plan) || reservation.RerunCount >= 1 {
			return bughub.BrowserVerificationResult{}, interruptedError("browser plan cannot be replayed automatically")
		}
		rerunCount = reservation.RerunCount + 1
	}

	if v.runtime == nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_runtime_missing", cause: errors.New("browser runtime manager is required")}
	}
	runtimePaths, err := v.runtime.Ensure(ctx, request.Emit)
	if err != nil {
		return bughub.BrowserVerificationResult{}, err
	}
	var sessionState []byte
	hasSession := false
	sessionKey := SessionKey{SystemID: request.SystemID, Environment: request.Environment, Origin: request.Plan.StartURL}
	if v.sessions != nil {
		sessionState, hasSession, err = v.sessions.Load(sessionKey)
		if err != nil {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_session_unavailable", cause: errors.New("load encrypted browser session")}
		}
	}
	reservationIdentity.RerunCount = rerunCount
	if hasReservation {
		if err := writeAtomicBrowserJSON(browserIdentity, reservationPath, reservationIdentity); err != nil {
			return bughub.BrowserVerificationResult{}, browserJournalWriteError("browser_reservation_write_failed", err)
		}
		if err := v.cleanupInterrupted(browserIdentity, reservationPath); err != nil {
			if errors.Is(err, ErrBrowserStagingIdentityChanged) {
				return bughub.BrowserVerificationResult{}, unsafeBrowserJournalError(err)
			}
			return bughub.BrowserVerificationResult{}, interruptedError("clean interrupted browser evidence")
		}
	} else {
		if err := writeAtomicBrowserJSON(browserIdentity, reservationPath, reservationIdentity); err != nil {
			return bughub.BrowserVerificationResult{}, browserJournalWriteError("browser_reservation_write_failed", err)
		}
	}

	workerOutput, err := v.runWorkerWithSession(ctx, runtimePaths, workerRequest{
		Mode:       "execute",
		Plan:       request.Plan,
		Policy:     request.Policy,
		StagingDir: browserDir,
		Headless:   true,
	}, request.Emit, sessionKey, sessionState, hasSession)
	if err != nil {
		if errors.Is(err, errPlaintextSessionCleanup) {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_session_cleanup_failed", cause: errPlaintextSessionCleanup}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_interrupted", cause: ctxErr}
		}
		if errors.Is(err, ErrBrowserWorkerOutputTooLarge) {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_output_too_large", cause: ErrBrowserWorkerOutputTooLarge}
		}
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_failed", cause: errors.New("browser worker exited before producing a result")}
	}
	if err := browserIdentity.Verify(); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
	}
	if err := validateWorkerResultBounds(workerOutput); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_protocol_invalid", cause: err}
	}
	workerOutput = sanitizeWorkerResult(workerOutput)
	if err := validateWorkerResultURLs(ctx, v.resolver, request.Policy, workerOutput); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_protocol_invalid", cause: err}
	}
	result := browserVerificationResult(request, workerOutput)
	validation, err := validateManifestArtifacts(request.StagingDir, browserIdentity, result.Artifacts, result.Status, result.FinalScreenshotPath)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
	}
	if result.FinalScreenshotPath == "" {
		result.FinalScreenshotPath = validation.FinalScreenshot
	}
	result = bindVerifiedBrowserArtifacts(result, validation)
	manifest := browserResultManifest{
		CaseID: request.CaseID, CycleNumber: request.CycleNumber, AttemptID: request.AttemptID,
		PlanSHA256: planSHA, ArtifactSHA256: validation.SHA256, Result: result,
	}
	if err := writeAtomicBrowserJSON(browserIdentity, resultPath, manifest); err != nil {
		return bughub.BrowserVerificationResult{}, browserJournalWriteError("browser_result_write_failed", err)
	}
	return result, nil
}

func (v *HostVerifier) Login(ctx context.Context, request BrowserLoginRequest) (returnedErr error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	effectOutcomeUncertain := false
	defer func() {
		if returnedErr != nil && !effectOutcomeUncertain {
			returnedErr = KnownFailedRecoveryEffect(returnedErr)
		}
	}()
	if v.sessions == nil {
		return &verifierError{code: "browser_session_store_missing", cause: errors.New("browser session store is required")}
	}
	if v.runtime == nil {
		return &verifierError{code: "browser_runtime_missing", cause: errors.New("browser runtime manager is required")}
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if timeout > 15*time.Minute {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser login timeout exceeds its limit")}
	}
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	applicationOrigin := strings.TrimSpace(request.ApplicationOrigin)
	loginOrigin := strings.TrimSpace(request.LoginOrigin)
	if applicationOrigin == "" {
		applicationOrigin = strings.TrimSpace(request.Origin)
	}
	if loginOrigin == "" {
		loginOrigin = firstNonEmptyVerifierOrigin(strings.TrimSpace(request.Origin), applicationOrigin)
	}
	canonicalApplicationOrigin, err := canonicalSessionOrigin(applicationOrigin)
	if err != nil || strings.TrimSpace(request.SystemID) == "" || strings.TrimSpace(request.Environment) == "" {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser login identity or origin is invalid")}
	}
	canonicalLoginOrigin, err := canonicalSessionOrigin(loginOrigin)
	if err != nil {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser login origin is invalid")}
	}
	_, allowedApplicationOrigin, _, err := parseBrowserURL(canonicalApplicationOrigin)
	if err != nil {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser application origin is invalid")}
	}
	if _, allowed := normalizedOriginSet(request.Policy.AllowedOrigins)[allowedApplicationOrigin]; !allowed {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser login must start at a configured application origin")}
	}
	if err := AllowedURL(loginCtx, v.resolver, request.Policy, canonicalApplicationOrigin); err != nil {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser application origin is blocked")}
	}
	if err := AllowedURL(loginCtx, v.resolver, request.Policy, canonicalLoginOrigin); err != nil {
		return &verifierError{code: "browser_login_request_invalid", cause: errors.New("browser login origin is blocked")}
	}

	runtimePaths, err := v.runtime.Ensure(loginCtx, request.Emit)
	if err != nil {
		return err
	}
	key := SessionKey{SystemID: request.SystemID, Environment: request.Environment, Origin: canonicalApplicationOrigin}
	existing, found, err := v.sessions.Load(key)
	if err != nil {
		return &verifierError{code: "browser_session_unavailable", cause: errors.New("load encrypted browser session")}
	}
	path, err := createPlaintextSessionTemp(key, existing, found, v.removePlaintext)
	if err != nil {
		if errors.Is(err, errPlaintextSessionCleanup) {
			effectOutcomeUncertain = true
			return &verifierError{code: "browser_session_cleanup_failed", cause: errPlaintextSessionCleanup}
		}
		return &verifierError{code: "browser_session_temp_failed", cause: errors.New("create temporary browser session")}
	}
	cleanupPath := path
	defer func() {
		if cleanupPath != "" {
			if err := v.cleanupPlaintextSession(cleanupPath); err != nil {
				effectOutcomeUncertain = true
				returnedErr = &verifierError{code: "browser_session_cleanup_failed", cause: errPlaintextSessionCleanup}
			}
		}
	}()

	output, err := v.worker.Run(loginCtx, runtimePaths, workerRequest{
		Mode: "login",
		Plan: bughub.BrowserPlan{
			Version:    1,
			StartURL:   canonicalLoginOrigin,
			Actions:    []bughub.BrowserAction{},
			Assertions: []bughub.BrowserAssertion{},
		},
		Policy:           request.Policy,
		StorageStatePath: path,
		Headless:         false,
	}, request.Emit)
	if err != nil {
		if loginCtx.Err() != nil {
			return &verifierError{code: "browser_login_interrupted", cause: errors.New("browser login was interrupted")}
		}
		if errors.Is(err, ErrBrowserWorkerOutputTooLarge) {
			return &verifierError{code: "browser_worker_output_too_large", cause: ErrBrowserWorkerOutputTooLarge}
		}
		return &verifierError{code: "browser_login_failed", cause: errors.New("browser login worker failed")}
	}
	effectOutcomeUncertain = true
	if err := validateLoginWorkerResult(output); err != nil {
		return &verifierError{code: "browser_worker_protocol_invalid", cause: err}
	}
	state, err := readPlaintextSessionState(path)
	if err != nil {
		return &verifierError{code: "browser_session_invalid", cause: err}
	}
	if err := v.cleanupPlaintextSession(path); err != nil {
		return &verifierError{code: "browser_session_cleanup_failed", cause: errPlaintextSessionCleanup}
	}
	cleanupPath = ""
	if err := v.sessions.Save(key, state); err != nil {
		return &verifierError{code: "browser_session_save_failed", cause: errors.New("save encrypted browser session")}
	}
	return nil
}

func firstNonEmptyVerifierOrigin(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (v *HostVerifier) runWorkerWithSession(
	ctx context.Context,
	paths RuntimePaths,
	request workerRequest,
	emit func(bughub.BrowserProgress),
	key SessionKey,
	state []byte,
	found bool,
) (result workerResult, returnedErr error) {
	if found {
		path, err := createPlaintextSessionTemp(key, state, true, v.removePlaintext)
		if err != nil {
			return workerResult{}, err
		}
		request.StorageStatePath = path
		defer func() {
			if err := v.cleanupPlaintextSession(path); err != nil {
				returnedErr = errors.Join(returnedErr, errPlaintextSessionCleanup)
			}
		}()
	}
	return v.worker.Run(ctx, paths, request, emit)
}

func (v *HostVerifier) cleanupPlaintextSession(path string) error {
	return cleanupPlaintextSessionWith(v.removePlaintext, path)
}

func cleanupPlaintextSessionWith(remove func(string) error, path string) error {
	if remove == nil {
		remove = os.Remove
	}
	if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errPlaintextSessionCleanup
	}
	return nil
}

func createPlaintextSessionTemp(key SessionKey, state []byte, writeState bool, removeFile func(string) error) (_ string, returnedErr error) {
	identifier, err := sessionIdentifier(key)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp("", ".tshoot-browser-session-"+identifier+"-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			returnedErr = errors.Join(returnedErr, cleanupPlaintextSessionWith(removeFile, path))
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	if writeState {
		if len(state) > maxBrowserSessionBytes {
			return "", errors.New("browser session plaintext exceeds its limit")
		}
		if err := writeAll(temporary, state); err != nil {
			return "", err
		}
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func validateLoginWorkerResult(result workerResult) error {
	if err := validateWorkerResultBounds(result); err != nil {
		return err
	}
	if result.Status != "completed" || result.ErrorCode != "" || result.ErrorMessage != "" ||
		result.FailedActionID != "" || result.FinalURL != "" || result.Title != "" || result.LoginOrigin != "" ||
		result.FinalScreenshotPath != "" || len(result.AccessibilitySummary) != 0 || len(result.Artifacts) != 0 {
		return errors.New("browser login worker returned forbidden result fields")
	}
	return nil
}

func readPlaintextSessionState(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 2 || info.Size() > maxBrowserSessionBytes {
		return nil, errors.New("browser login state file is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open browser login state")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("browser login state changed while opening")
	}
	state, err := io.ReadAll(io.LimitReader(file, maxBrowserSessionBytes+1))
	if err != nil || len(state) > maxBrowserSessionBytes {
		return nil, errors.New("read browser login state")
	}
	decoder := json.NewDecoder(bytes.NewReader(state))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("browser login state is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, errors.New("browser login state is invalid")
	}
	return state, nil
}

func validateVerificationRequest(ctx context.Context, resolver IPResolver, request bughub.BrowserVerificationRequest) error {
	if strings.TrimSpace(request.CaseID) == "" || strings.TrimSpace(request.AttemptID) == "" || request.CycleNumber < 1 {
		return &verifierError{code: "browser_request_invalid", cause: errors.New("case, cycle, and attempt identity are required")}
	}
	if strings.TrimSpace(request.StagingDir) == "" || !filepath.IsAbs(request.StagingDir) {
		return &verifierError{code: "browser_request_invalid", cause: errors.New("browser staging directory must be absolute")}
	}
	if err := validateWorkerPlanShape(request.Plan); err != nil {
		return &verifierError{code: "browser_plan_invalid", cause: err}
	}
	return ValidatePlan(ctx, resolver, request.Policy, request.Plan)
}

func validateWorkerPlanShape(plan bughub.BrowserPlan) error {
	if plan.Version != 1 || strings.TrimSpace(plan.StartURL) == "" || len(plan.Actions) < 1 || len(plan.Actions) > 40 || len(plan.Assertions) < 1 {
		return errors.New("browser plan shape is invalid")
	}
	actions := map[string]struct{}{"goto": {}, "click": {}, "fill": {}, "press": {}, "select": {}, "wait_for": {}, "screenshot": {}}
	locators := map[string]struct{}{"role": {}, "label": {}, "text": {}, "placeholder": {}, "test_id": {}, "css": {}}
	seen := make(map[string]struct{}, len(plan.Actions))
	for _, action := range plan.Actions {
		if strings.TrimSpace(action.ID) == "" {
			return errors.New("browser action ID is required")
		}
		if _, duplicate := seen[action.ID]; duplicate {
			return errors.New("browser action IDs must be unique")
		}
		seen[action.ID] = struct{}{}
		if _, allowed := actions[action.Action]; !allowed {
			return fmt.Errorf("browser action %q is not supported", action.Action)
		}
		if action.Locator != nil {
			if _, allowed := locators[action.Locator.Kind]; !allowed || strings.TrimSpace(action.Locator.Value) == "" {
				return errors.New("browser locator is invalid")
			}
		}
	}
	for _, assertion := range plan.Assertions {
		if assertion.Kind != "visible_text" || strings.TrimSpace(assertion.Value) == "" {
			return errors.New("browser assertion is invalid")
		}
	}
	return nil
}

func browserPlanSHA256(plan bughub.BrowserPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func browserPlanCanReplay(plan bughub.BrowserPlan) bool {
	for _, action := range plan.Actions {
		switch action.Action {
		case "goto", "wait_for", "screenshot":
		default:
			return false
		}
	}
	return true
}

func ensureBrowserStagingDirectory(stagingRoot string) (string, error) {
	rootInfo, err := os.Lstat(stagingRoot)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", errors.New("browser staging root is not a regular directory")
	}
	browserDir := filepath.Join(stagingRoot, "browser")
	if err := os.Mkdir(browserDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(browserDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("browser staging directory is unsafe")
	}
	if err := os.Chmod(browserDir, 0o700); err != nil {
		return "", err
	}
	return browserDir, nil
}

func pinBrowserDirectory(path string) (browserDirectoryIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return browserDirectoryIdentity{}, fmt.Errorf("%w: browser root is unsafe", ErrBrowserStagingIdentityChanged)
	}
	return browserDirectoryIdentity{path: path, info: info}, nil
}

func (identity browserDirectoryIdentity) Verify() error {
	info, err := os.Lstat(identity.path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || identity.info == nil || !os.SameFile(identity.info, info) {
		return fmt.Errorf("%w: pinned browser root was replaced", ErrBrowserStagingIdentityChanged)
	}
	return nil
}

func cleanupInterruptedBrowserOutputs(identity browserDirectoryIdentity, reservationPath string) error {
	if reservationPath != filepath.Join(identity.path, "reservation.json") {
		return errors.New("browser directory is not bound to staging root")
	}
	if err := identity.Verify(); err != nil {
		return err
	}
	entries, err := os.ReadDir(identity.path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "reservation.json" {
			continue
		}
		if err := identity.Verify(); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(identity.path, entry.Name())); err != nil {
			return err
		}
	}
	if err := identity.Verify(); err != nil {
		return err
	}
	return syncRuntimeDirectory(identity.path)
}

func readBrowserReservation(path string) (browserReservation, bool, error) {
	var reservation browserReservation
	found, err := readStrictBrowserJSON(path, &reservation)
	if err != nil {
		return browserReservation{}, found, err
	}
	if found && (reservation.State == "" || reservation.RerunCount < 0 || reservation.RerunCount > 1) {
		return browserReservation{}, true, errors.New("browser reservation fields are invalid")
	}
	return reservation, found, nil
}

func readBrowserResultManifest(path string) (browserResultManifest, bool, error) {
	var manifest browserResultManifest
	found, err := readStrictBrowserJSON(path, &manifest)
	return manifest, found, err
}

func readStrictBrowserJSON(path string, destination any) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBrowserArtifactBytes {
		return true, errors.New("browser journal is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return true, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return true, errors.New("browser journal changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxBrowserArtifactBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return true, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return true, err
	}
	return true, nil
}

func reservationMatches(got, want browserReservation) bool {
	return manifestMatches(got.CaseID, got.CycleNumber, got.AttemptID, got.PlanSHA256, want)
}

func manifestMatches(caseID string, cycle int, attemptID, planSHA string, want browserReservation) bool {
	return caseID == want.CaseID && cycle == want.CycleNumber && attemptID == want.AttemptID && planSHA == want.PlanSHA256
}

func interruptedError(message string) error {
	return &verifierError{code: "browser_execution_interrupted", cause: fmt.Errorf("%w: %s", ErrBrowserExecutionInterrupted, message)}
}

func unsafeBrowserJournalError(err error) error {
	return &verifierError{code: "browser_journal_unsafe", cause: err}
}

func browserJournalWriteError(defaultCode string, err error) error {
	if errors.Is(err, ErrBrowserStagingIdentityChanged) {
		return unsafeBrowserJournalError(err)
	}
	return &verifierError{code: defaultCode, cause: err}
}

func writeAtomicBrowserJSON(identity browserDirectoryIdentity, path string, value any) error {
	if filepath.Dir(path) != identity.path {
		return errors.New("browser journal path is outside the pinned browser root")
	}
	if err := identity.Verify(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := identity.Verify(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	_, writeErr := temporary.Write(encoded)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := identity.Verify(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncRuntimeDirectory(directory)
}

func sanitizeWorkerResult(result workerResult) workerResult {
	result.ErrorCode = safeVerifierIdentifier(result.ErrorCode, 128)
	result.ErrorMessage = redactVerifierText(result.ErrorMessage, 2048)
	result.FailedActionID = safeVerifierIdentifier(result.FailedActionID, 256)
	result.Title = redactVerifierText(result.Title, 1024)
	if len(result.AccessibilitySummary) > 50 {
		result.AccessibilitySummary = result.AccessibilitySummary[:50]
	}
	for index := range result.AccessibilitySummary {
		result.AccessibilitySummary[index].Role = redactVerifierText(result.AccessibilitySummary[index].Role, 128)
		result.AccessibilitySummary[index].Name = redactVerifierText(result.AccessibilitySummary[index].Name, 512)
	}
	for index := range result.Artifacts {
		result.Artifacts[index].RequestID = safeVerifierIdentifier(result.Artifacts[index].RequestID, 128)
		result.Artifacts[index].TraceID = safeVerifierIdentifier(result.Artifacts[index].TraceID, 128)
	}
	return result
}

func validateWorkerResultBounds(result workerResult) error {
	if len(result.Artifacts) > 128 {
		return errors.New("browser worker returned too many artifacts")
	}
	if len(result.AccessibilitySummary) > 50 {
		return errors.New("browser worker returned too many accessibility nodes")
	}
	for label, value := range map[string]string{
		"status": result.Status, "final URL": result.FinalURL, "login origin": result.LoginOrigin,
		"final screenshot": result.FinalScreenshotPath,
	} {
		if len(value) > 4096 {
			return fmt.Errorf("browser worker %s is too long", label)
		}
	}
	for _, artifact := range result.Artifacts {
		if len(artifact.Kind) > 128 || len(artifact.Path) > 4096 || len(artifact.RequestID) > 4096 || len(artifact.TraceID) > 4096 {
			return errors.New("browser worker artifact field is too long")
		}
	}
	return nil
}

func validateWorkerResultURLs(ctx context.Context, resolver IPResolver, policy bughub.BrowserSecurityPolicy, result workerResult) error {
	switch result.Status {
	case "completed", "locator_failed", "assertion_failed", "login_required":
	default:
		return fmt.Errorf("unsupported worker status %q", result.Status)
	}
	if result.FinalURL != "" {
		if err := AllowedURL(ctx, resolver, policy, result.FinalURL); err != nil {
			return fmt.Errorf("worker final URL: %w", err)
		}
	}
	if result.LoginOrigin != "" {
		if err := AllowedURL(ctx, resolver, policy, result.LoginOrigin); err != nil {
			return fmt.Errorf("worker login origin: %w", err)
		}
	}
	return nil
}

func browserVerificationResult(request bughub.BrowserVerificationRequest, worker workerResult) bughub.BrowserVerificationResult {
	result := bughub.BrowserVerificationResult{
		Status: worker.Status, ErrorCode: worker.ErrorCode, ErrorMessage: worker.ErrorMessage,
		FailedActionID: worker.FailedActionID, FinalURL: sanitizeVerifierURL(worker.FinalURL),
		Title: worker.Title, LoginOrigin: sanitizeVerifierURL(worker.LoginOrigin),
		FinalScreenshotPath: worker.FinalScreenshotPath, AccessibilitySummary: worker.AccessibilitySummary,
		Artifacts: make([]bughub.BrowserArtifactReference, 0, len(worker.Artifacts)),
	}
	if result.Status == "login_required" && result.ErrorCode == "" {
		result.ErrorCode = "browser_login_required"
	}
	for _, artifact := range worker.Artifacts {
		result.Artifacts = append(result.Artifacts, bughub.BrowserArtifactReference{
			Kind: artifact.Kind, Path: artifact.Path, Environment: request.Environment, Version: request.Version,
			RequestID: artifact.RequestID, TraceID: artifact.TraceID,
		})
	}
	return result
}

func validateManifestArtifacts(stagingRoot string, identity browserDirectoryIdentity, artifacts []bughub.BrowserArtifactReference, status, declaredFinal string) (manifestArtifactValidation, error) {
	if err := identity.Verify(); err != nil {
		return manifestArtifactValidation{}, err
	}
	declared := make(map[string]bughub.BrowserArtifactReference, len(artifacts))
	digests := make(map[string]string, len(artifacts))
	sizes := make(map[string]int64, len(artifacts))
	var screenshots []string
	for _, artifact := range artifacts {
		if err := identity.Verify(); err != nil {
			return manifestArtifactValidation{}, err
		}
		if !validBrowserArtifactKind(artifact.Kind) {
			return manifestArtifactValidation{}, fmt.Errorf("unsupported browser artifact kind %q", artifact.Kind)
		}
		path, err := normalizeBrowserArtifactPath(artifact.Path)
		if err != nil {
			return manifestArtifactValidation{}, err
		}
		if _, duplicate := declared[path]; duplicate {
			return manifestArtifactValidation{}, fmt.Errorf("duplicate browser artifact path %q", path)
		}
		declared[path] = artifact
		content, err := readVerifiedBrowserArtifact(stagingRoot, path)
		if err != nil {
			return manifestArtifactValidation{}, err
		}
		if err := identity.Verify(); err != nil {
			return manifestArtifactValidation{}, err
		}
		digest := sha256.Sum256(content)
		digests[path] = hex.EncodeToString(digest[:])
		sizes[path] = int64(len(content))
		if artifact.Kind == "screenshot" {
			if len(content) <= 8 || !bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")) {
				return manifestArtifactValidation{}, fmt.Errorf("browser screenshot %q is not a non-empty PNG", path)
			}
			screenshots = append(screenshots, path)
		} else {
			if len(content) > maxBrowserEvidenceBytes {
				return manifestArtifactValidation{}, fmt.Errorf("browser evidence %q exceeds %d bytes", path, maxBrowserEvidenceBytes)
			}
			if containsForbiddenBrowserEvidence(content) {
				return manifestArtifactValidation{}, fmt.Errorf("browser artifact %q contains forbidden credential material", path)
			}
		}
	}
	if err := identity.Verify(); err != nil {
		return manifestArtifactValidation{}, err
	}
	if err := rejectUndeclaredBrowserOutputs(stagingRoot, identity.path, declared); err != nil {
		return manifestArtifactValidation{}, err
	}
	if err := identity.Verify(); err != nil {
		return manifestArtifactValidation{}, err
	}
	final := declaredFinal
	if final == "" && len(screenshots) == 1 {
		final = screenshots[0]
	}
	if final != "" {
		normalized, err := normalizeBrowserArtifactPath(final)
		if err != nil {
			return manifestArtifactValidation{}, err
		}
		artifact, exists := declared[normalized]
		if !exists || artifact.Kind != "screenshot" {
			return manifestArtifactValidation{}, errors.New("final screenshot is not a declared screenshot artifact")
		}
		final = normalized
	}
	switch status {
	case "completed", "locator_failed", "assertion_failed":
		if final == "" {
			return manifestArtifactValidation{}, errors.New("browser result requires a final or failure screenshot")
		}
	case "login_required":
		if len(screenshots) != 0 || final != "" {
			return manifestArtifactValidation{}, errors.New("login-required result must not contain screenshots")
		}
	default:
		return manifestArtifactValidation{}, fmt.Errorf("unsupported browser result status %q", status)
	}
	return manifestArtifactValidation{FinalScreenshot: final, SHA256: digests, Size: sizes}, nil
}

func bindVerifiedBrowserArtifacts(result bughub.BrowserVerificationResult, validation manifestArtifactValidation) bughub.BrowserVerificationResult {
	bound := result
	bound.Artifacts = append([]bughub.BrowserArtifactReference(nil), result.Artifacts...)
	for index := range bound.Artifacts {
		path, err := normalizeBrowserArtifactPath(bound.Artifacts[index].Path)
		if err != nil {
			continue
		}
		bound.Artifacts[index].SHA256 = validation.SHA256[path]
		bound.Artifacts[index].Size = validation.Size[path]
	}
	return bound
}

func artifactDigestsEqual(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}
	for path, digest := range first {
		if second[path] != digest {
			return false
		}
	}
	return true
}

func validBrowserArtifactKind(kind string) bool {
	switch kind {
	case "screenshot", "network", "console", "browser_actions":
		return true
	default:
		return false
	}
}

func normalizeBrowserArtifactPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.ContainsRune(path, '\x00') {
		return "", errors.New("browser artifact path must be a normalized relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || !strings.HasPrefix(clean, "browser/") || clean == "browser/reservation.json" || clean == "browser/result.json" {
		return "", errors.New("browser artifact path must stay beneath staging/browser")
	}
	for _, component := range strings.Split(clean, "/") {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("browser artifact path contains an unsafe component")
		}
	}
	return clean, nil
}

func readVerifiedBrowserArtifact(stagingRoot, relative string) ([]byte, error) {
	path := filepath.Join(stagingRoot, filepath.FromSlash(relative))
	components := strings.Split(strings.TrimPrefix(relative, "browser/"), "/")
	current := filepath.Join(stagingRoot, "browser")
	for _, component := range components[:len(components)-1] {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("browser artifact parent is unsafe")
		}
	}
	lstat, err := os.Lstat(path)
	if err != nil || lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return nil, fmt.Errorf("browser artifact %q is not a regular file", relative)
	}
	if lstat.Size() > maxBrowserArtifactBytes {
		return nil, fmt.Errorf("browser artifact %q exceeds %d bytes", relative, maxBrowserArtifactBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fstat, err := file.Stat()
	if err != nil || !fstat.Mode().IsRegular() || !os.SameFile(lstat, fstat) {
		return nil, errors.New("browser artifact changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBrowserArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxBrowserArtifactBytes {
		return nil, fmt.Errorf("browser artifact %q exceeds %d bytes", relative, maxBrowserArtifactBytes)
	}
	return content, nil
}

func rejectUndeclaredBrowserOutputs(stagingRoot, browserDir string, declared map[string]bughub.BrowserArtifactReference) error {
	return filepath.WalkDir(browserDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == browserDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("browser output contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("browser output contains a non-regular file")
		}
		relative, err := filepath.Rel(stagingRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "browser/reservation.json" || relative == "browser/result.json" {
			return nil
		}
		if _, ok := declared[relative]; !ok {
			return fmt.Errorf("browser output %q was not declared in the manifest", relative)
		}
		return nil
	})
}

func containsForbiddenBrowserEvidence(content []byte) bool {
	withoutMarkers := verifierRedactionMarker.ReplaceAll(content, nil)
	return verifierCredentialPattern.Match(withoutMarkers)
}

func redactVerifierText(value string, limit int) string {
	value = boundedVerifierString(value, limit)
	if verifierCredentialPattern.MatchString(value) {
		return "[REDACTED]"
	}
	return value
}

func safeVerifierIdentifier(value string, limit int) string {
	value = strings.TrimSpace(boundedVerifierString(value, limit))
	if value == "" {
		return ""
	}
	if verifierCredentialPattern.MatchString(value) || strings.ContainsAny(value, "\r\n") {
		return "[REDACTED]"
	}
	return value
}

func boundedVerifierString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

func sanitizeVerifierURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[INVALID_URL]"
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if verifierSensitiveQueryKey.MatchString(key) {
			query[key] = []string{"[REDACTED]"}
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type nodeWorkerRunner struct{}

type workerStdoutRead struct {
	content []byte
	err     error
}

func readBoundedWorkerStdout(stdout io.Reader, kill func()) workerStdoutRead {
	limited := &io.LimitedReader{R: stdout, N: maxBrowserWorkerOutputBytes + 1}
	content, err := io.ReadAll(limited)
	if len(content) > maxBrowserWorkerOutputBytes || limited.N == 0 {
		kill()
		return workerStdoutRead{err: ErrBrowserWorkerOutputTooLarge}
	}
	if err != nil {
		return workerStdoutRead{err: err}
	}
	return workerStdoutRead{content: content}
}

func consumeBoundedWorkerStderr(stderr io.Reader, emit func(bughub.BrowserProgress), kill func()) error {
	limited := &io.LimitedReader{R: stderr, N: maxBrowserWorkerStderrBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxBrowserWorkerProgressLineBytes)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount > maxBrowserWorkerProgressLines {
			kill()
			return ErrBrowserWorkerOutputTooLarge
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, browserProgressPrefix) {
			continue
		}
		var progress struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			ActionID string `json:"action_id"`
			Current  int    `json:"current"`
			Total    int    `json:"total"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, browserProgressPrefix)), &progress); err != nil || emit == nil {
			continue
		}
		progress.Code = safeVerifierIdentifier(progress.Code, 128)
		progress.Message = redactVerifierText(progress.Message, 1024)
		progress.ActionID = safeVerifierIdentifier(progress.ActionID, 256)
		if progress.Current < 0 || progress.Current > 40 {
			progress.Current = 0
		}
		if progress.Total < 0 || progress.Total > 40 {
			progress.Total = 0
		}
		emit(bughub.BrowserProgress{Code: progress.Code, Message: progress.Message, ActionID: progress.ActionID, Current: progress.Current, Total: progress.Total})
	}
	if scanner.Err() != nil || limited.N == 0 {
		kill()
		return ErrBrowserWorkerOutputTooLarge
	}
	return nil
}

func (nodeWorkerRunner) Run(ctx context.Context, paths RuntimePaths, request workerRequest, emit func(bughub.BrowserProgress)) (workerResult, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return workerResult{}, err
	}
	command := exec.CommandContext(ctx, "node", paths.WorkerPath, "--mode", request.Mode)
	processController, err := configureWorkerProcess(command)
	if err != nil {
		return workerResult{}, err
	}
	outputs, err := attachOwnedCommandOutputs(command)
	if err != nil {
		return workerResult{}, errors.Join(err, processController.finish())
	}
	defer outputs.closeAll()
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return workerResult{}, errors.Join(err, processController.finish())
	}
	defer stdinRead.Close()
	defer stdinWrite.Close()
	command.Dir = paths.Root
	command.Env = mergeCommandEnvironment(os.Environ(), []string{"PLAYWRIGHT_BROWSERS_PATH=" + paths.BrowsersPath})
	command.Stdin = stdinRead
	if err := command.Start(); err != nil {
		return workerResult{}, errors.Join(err, processController.finish())
	}
	if err := errors.Join(outputs.childStarted(), stdinRead.Close()); err != nil {
		_ = processController.kill(command)
		_ = command.Wait()
		return workerResult{}, errors.Join(err, processController.finish())
	}
	stdinDone := make(chan error, 1)
	go func() {
		_, writeErr := io.Copy(stdinWrite, bytes.NewReader(append(encoded, '\n')))
		stdinDone <- errors.Join(writeErr, stdinWrite.Close())
	}()
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			_ = processController.kill(command)
		})
	}
	stdoutDone := make(chan workerStdoutRead, 1)
	go func() {
		stdoutDone <- readBoundedWorkerStdout(outputs.stdoutRead, kill)
	}()
	stderrDone := make(chan error, 1)
	go func() {
		stderrDone <- consumeBoundedWorkerStderr(outputs.stderrRead, emit, kill)
	}()
	if err := processController.afterStart(command); err != nil {
		kill()
		waitErr := command.Wait()
		cleanupErr := processController.finish()
		stdoutResult, stderrErr := waitWorkerOutputDrains(outputs, stdoutDone, stderrDone)
		stdinErr := <-stdinDone
		return workerResult{}, errors.Join(err, waitErr, cleanupErr, stdoutResult.err, stderrErr, stdinErr)
	}
	waitErr := command.Wait()
	processCleanupErr := processController.finish()
	stdoutResult, stderrErr := waitWorkerOutputDrains(outputs, stdoutDone, stderrDone)
	stdinErr := <-stdinDone
	if errors.Is(stdoutResult.err, ErrBrowserWorkerOutputTooLarge) || errors.Is(stderrErr, ErrBrowserWorkerOutputTooLarge) {
		return workerResult{}, ErrBrowserWorkerOutputTooLarge
	}
	if waitErr != nil || stdoutResult.err != nil || stderrErr != nil || stdinErr != nil {
		return workerResult{}, errors.Join(waitErr, stdoutResult.err, stderrErr, stdinErr, processCleanupErr)
	}
	if processCleanupErr != nil {
		return workerResult{}, processCleanupErr
	}
	var result workerResult
	decoder := json.NewDecoder(bytes.NewReader(stdoutResult.content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return workerResult{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return workerResult{}, err
	}
	return result, nil
}

func waitWorkerOutputDrains(outputs *ownedCommandOutputs, stdoutDone <-chan workerStdoutRead, stderrDone <-chan error) (workerStdoutRead, error) {
	timer := time.NewTimer(commandOutputDrainTimeout)
	defer timer.Stop()
	timeout := timer.C
	var stdoutResult workerStdoutRead
	var stderrErr error
	for stdoutDone != nil || stderrDone != nil {
		select {
		case stdoutResult = <-stdoutDone:
			stdoutDone = nil
		case stderrErr = <-stderrDone:
			stderrDone = nil
		case <-timeout:
			_ = outputs.closeReaders()
			timeout = nil
		}
	}
	return stdoutResult, stderrErr
}
