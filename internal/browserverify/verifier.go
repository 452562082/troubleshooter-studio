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

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const maxBrowserArtifactBytes = 16 << 20

const browserProgressPrefix = "TSHOOT_BROWSER_PROGRESS "

var ErrBrowserExecutionInterrupted = errors.New("browser execution was interrupted")

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
	runtime  *RuntimeManager
	worker   WorkerRunner
	resolver IPResolver
	mu       sync.Mutex
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
	return &HostVerifier{runtime: runtime, worker: worker, resolver: resolver}
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
	identity := browserReservation{
		CaseID: request.CaseID, CycleNumber: request.CycleNumber, AttemptID: request.AttemptID,
		PlanSHA256: planSHA, State: "running",
	}
	resultPath := filepath.Join(browserDir, "result.json")
	if manifest, found, err := readBrowserResultManifest(resultPath); err != nil {
		return bughub.BrowserVerificationResult{}, interruptedError("completed browser result manifest is invalid")
	} else if found {
		if !manifestMatches(manifest.CaseID, manifest.CycleNumber, manifest.AttemptID, manifest.PlanSHA256, identity) {
			return bughub.BrowserVerificationResult{}, interruptedError("completed browser result belongs to a different attempt or plan")
		}
		validation, err := validateManifestArtifacts(request.StagingDir, browserDir, manifest.Result.Artifacts, manifest.Result.Status, manifest.Result.FinalScreenshotPath)
		if err != nil {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
		}
		if !artifactDigestsEqual(validation.SHA256, manifest.ArtifactSHA256) {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: errors.New("browser artifact digest changed after completion")}
		}
		return manifest.Result, nil
	}

	reservationPath := filepath.Join(browserDir, "reservation.json")
	reservation, hasReservation, err := readBrowserReservation(reservationPath)
	if err != nil {
		return bughub.BrowserVerificationResult{}, interruptedError("browser reservation is invalid")
	}
	rerunCount := 0
	if hasReservation {
		if !reservationMatches(reservation, identity) || reservation.State != "running" {
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
	if hasReservation {
		if err := resetInterruptedBrowserDirectory(request.StagingDir, browserDir); err != nil {
			return bughub.BrowserVerificationResult{}, interruptedError("clean interrupted browser evidence")
		}
		browserDir = filepath.Join(request.StagingDir, "browser")
		reservationPath = filepath.Join(browserDir, "reservation.json")
		resultPath = filepath.Join(browserDir, "result.json")
	}
	identity.RerunCount = rerunCount
	if err := writeAtomicBrowserJSON(reservationPath, identity); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_reservation_write_failed", cause: err}
	}

	workerOutput, err := v.worker.Run(ctx, runtimePaths, workerRequest{
		Mode:       "execute",
		Plan:       request.Plan,
		Policy:     request.Policy,
		StagingDir: browserDir,
		Headless:   true,
	}, request.Emit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_interrupted", cause: ctxErr}
		}
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_failed", cause: errors.New("browser worker exited before producing a result")}
	}
	workerOutput = sanitizeWorkerResult(workerOutput)
	if err := validateWorkerResultURLs(ctx, v.resolver, request.Policy, workerOutput); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_worker_protocol_invalid", cause: err}
	}
	result := browserVerificationResult(request, workerOutput)
	validation, err := validateManifestArtifacts(request.StagingDir, browserDir, result.Artifacts, result.Status, result.FinalScreenshotPath)
	if err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_artifact_invalid", cause: err}
	}
	if result.FinalScreenshotPath == "" {
		result.FinalScreenshotPath = validation.FinalScreenshot
	}
	manifest := browserResultManifest{
		CaseID: request.CaseID, CycleNumber: request.CycleNumber, AttemptID: request.AttemptID,
		PlanSHA256: planSHA, ArtifactSHA256: validation.SHA256, Result: result,
	}
	if err := writeAtomicBrowserJSON(resultPath, manifest); err != nil {
		return bughub.BrowserVerificationResult{}, &verifierError{code: "browser_result_write_failed", cause: err}
	}
	return result, nil
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

func resetInterruptedBrowserDirectory(stagingRoot, browserDir string) error {
	want := filepath.Join(stagingRoot, "browser")
	if browserDir != want {
		return errors.New("browser directory is not bound to staging root")
	}
	info, err := os.Lstat(browserDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("interrupted browser directory is unsafe")
	}
	if err := os.RemoveAll(browserDir); err != nil {
		return err
	}
	if err := os.Mkdir(browserDir, 0o700); err != nil {
		return err
	}
	return syncRuntimeDirectory(stagingRoot)
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

func writeAtomicBrowserJSON(path string, value any) error {
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

func validateManifestArtifacts(stagingRoot, browserDir string, artifacts []bughub.BrowserArtifactReference, status, declaredFinal string) (manifestArtifactValidation, error) {
	declared := make(map[string]bughub.BrowserArtifactReference, len(artifacts))
	digests := make(map[string]string, len(artifacts))
	var screenshots []string
	for _, artifact := range artifacts {
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
		digest := sha256.Sum256(content)
		digests[path] = hex.EncodeToString(digest[:])
		if artifact.Kind == "screenshot" {
			if len(content) <= 8 || !bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")) {
				return manifestArtifactValidation{}, fmt.Errorf("browser screenshot %q is not a non-empty PNG", path)
			}
			screenshots = append(screenshots, path)
		} else if containsForbiddenBrowserEvidence(content) {
			return manifestArtifactValidation{}, fmt.Errorf("browser artifact %q contains forbidden credential material", path)
		}
	}
	if err := rejectUndeclaredBrowserOutputs(stagingRoot, browserDir, declared); err != nil {
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
	return manifestArtifactValidation{FinalScreenshot: final, SHA256: digests}, nil
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

func (nodeWorkerRunner) Run(ctx context.Context, paths RuntimePaths, request workerRequest, emit func(bughub.BrowserProgress)) (workerResult, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return workerResult{}, err
	}
	command := exec.CommandContext(ctx, "node", paths.WorkerPath, "--mode", request.Mode)
	command.Dir = paths.Root
	command.Env = mergeCommandEnvironment(os.Environ(), []string{"PLAYWRIGHT_BROWSERS_PATH=" + paths.BrowsersPath})
	command.Stdin = bytes.NewReader(append(encoded, '\n'))
	var stdout bytes.Buffer
	command.Stdout = &stdout
	stderr, err := command.StderrPipe()
	if err != nil {
		return workerResult{}, err
	}
	if err := command.Start(); err != nil {
		return workerResult{}, err
	}
	progressDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
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
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, browserProgressPrefix)), &progress); err == nil && emit != nil {
				emit(bughub.BrowserProgress{Code: progress.Code, Message: progress.Message, ActionID: progress.ActionID, Current: progress.Current, Total: progress.Total})
			}
		}
		progressDone <- scanner.Err()
	}()
	waitErr := command.Wait()
	scanErr := <-progressDone
	if waitErr != nil || scanErr != nil {
		return workerResult{}, errors.Join(waitErr, scanErr)
	}
	var result workerResult
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return workerResult{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return workerResult{}, err
	}
	return result, nil
}
