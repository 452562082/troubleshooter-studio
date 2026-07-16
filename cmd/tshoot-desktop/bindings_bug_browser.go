package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/browserverify"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/zalando/go-keyring"
)

var pngFileSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type IncidentArtifactPreview struct {
	ArtifactID string `json:"artifact_id"`
	MIMEType   string `json:"mime_type"`
	Base64Data string `json:"base64_data"`
	Size       int    `json:"size"`
}

type IncidentBrowserCommandInput struct {
	CaseID          string `json:"case_id"`
	AttemptID       string `json:"attempt_id"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
	ActorID         string `json:"actor_id"`
}

type incidentBrowserController interface {
	bughub.BrowserVerifier
	Login(context.Context, browserverify.BrowserLoginRequest) error
	ClearSession(context.Context, browserverify.SessionKey) error
	Repair(context.Context, func(bughub.BrowserProgress)) error
	Status() browserverify.RuntimeStatus
}

const incidentBrowserKeyringService = "tshoot-studio-browser-session"

type incidentBrowserKeyringStore struct {
	get    func(string, string) (string, error)
	set    func(string, string, string) error
	delete func(string, string) error
}

func newIncidentBrowserKeyringStore() incidentBrowserKeyringStore {
	return incidentBrowserKeyringStore{get: keyring.Get, set: keyring.Set, delete: keyring.Delete}
}

func (s incidentBrowserKeyringStore) Get(identifier string) (string, error) {
	value, err := s.get(incidentBrowserKeyringService, identifier)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", browserverify.ErrSecretNotFound
	}
	return value, err
}

func (s incidentBrowserKeyringStore) Set(identifier, value string) error {
	return s.set(incidentBrowserKeyringService, identifier, value)
}

func (s incidentBrowserKeyringStore) Delete(identifier string) error {
	err := s.delete(incidentBrowserKeyringService, identifier)
	if errors.Is(err, keyring.ErrNotFound) {
		return browserverify.ErrSecretNotFound
	}
	return err
}

func (a *App) initializeIncidentBrowser(root string) {
	if a.workflowBrowser != nil {
		return
	}
	runtimeManager := browserverify.NewRuntimeManager(root, nil)
	controller := browserverify.NewHostVerifier(runtimeManager, nil, net.DefaultResolver)
	controller.SetSessionStore(browserverify.NewSessionStore(filepath.Join(root, "browser-sessions"), newIncidentBrowserKeyringStore()))
	a.workflowBrowser = controller
}

type caseBrowserPolicyResolver struct{ app *App }

func (r caseBrowserPolicyResolver) ResolveBrowserPolicy(ctx context.Context, incident bughub.IncidentCase, _ bughub.Bug) (bughub.BrowserSecurityPolicy, error) {
	if r.app == nil {
		return bughub.BrowserSecurityPolicy{}, errors.New("browser policy is unavailable")
	}
	loader := r.app.workflowLoadDeploymentConfig
	if loader == nil {
		loader = r.app.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.System.ID) != strings.TrimSpace(incident.SystemID) {
		return bughub.BrowserSecurityPolicy{}, errors.New("incident browser configuration is unavailable")
	}
	var environment *config.Environment
	for index := range cfg.Environments {
		if strings.TrimSpace(cfg.Environments[index].ID) == strings.TrimSpace(incident.Environment) {
			environment = &cfg.Environments[index]
			break
		}
	}
	if environment == nil {
		return bughub.BrowserSecurityPolicy{}, errors.New("incident browser environment is unavailable")
	}
	allowed, err := canonicalIncidentBrowserOrigins(append([]string{environment.WebDomain, environment.APIDomain}, environment.BrowserAuthOrigins...))
	if err != nil || len(allowed) == 0 {
		return bughub.BrowserSecurityPolicy{}, errors.New("incident browser origins are invalid")
	}
	auth, err := canonicalIncidentBrowserOrigins(environment.BrowserAuthOrigins)
	if err != nil {
		return bughub.BrowserSecurityPolicy{}, errors.New("incident browser authentication origins are invalid")
	}
	return bughub.BrowserSecurityPolicy{
		AllowedOrigins: allowed,
		PrivateOrigins: append([]string(nil), allowed...),
		AuthOrigins:    auth,
		IsProd:         environment.IsProd,
	}, nil
}

func canonicalIncidentBrowserOrigins(values []string) ([]string, error) {
	origins := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		origin, err := canonicalIncidentBrowserOrigin(value)
		if err != nil {
			return nil, err
		}
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	deduplicated := origins[:0]
	for _, origin := range origins {
		if len(deduplicated) == 0 || deduplicated[len(deduplicated)-1] != origin {
			deduplicated = append(deduplicated, origin)
		}
	}
	return deduplicated, nil
}

func canonicalIncidentBrowserOrigin(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid browser origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("invalid browser origin")
	}
	hostname := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("invalid browser origin")
	}
	if port != "" {
		numeric, err := strconv.ParseUint(port, 10, 16)
		if err != nil || numeric == 0 {
			return "", errors.New("invalid browser origin")
		}
		port = strconv.FormatUint(numeric, 10)
	}
	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return scheme + "://" + host, nil
}

func (a *App) GetIncidentArtifactPreview(caseID, artifactID string) (IncidentArtifactPreview, error) {
	caseID = strings.TrimSpace(caseID)
	artifactID = strings.TrimSpace(artifactID)
	if caseID == "" || artifactID == "" {
		return IncidentArtifactPreview{}, errors.New("case_id and artifact_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return IncidentArtifactPreview{}, err
	}
	content, err := bughub.ReadEvidenceArtifactFromRoot(a.workflowCommandContext(), store, filepath.Join(a.workflowRoot, "artifacts"), caseID, artifactID)
	if err != nil {
		return IncidentArtifactPreview{}, err
	}
	if content.Artifact.Kind != "screenshot" || !bytes.HasPrefix(content.Content, pngFileSignature) {
		return IncidentArtifactPreview{}, errors.New("artifact is not a registered PNG screenshot")
	}
	return IncidentArtifactPreview{
		ArtifactID: content.Artifact.ID,
		MIMEType:   "image/png",
		Base64Data: base64.StdEncoding.EncodeToString(content.Content),
		Size:       len(content.Content),
	}, nil
}

func (a *App) SaveIncidentArtifact(caseID, artifactID string) (string, error) {
	caseID = strings.TrimSpace(caseID)
	artifactID = strings.TrimSpace(artifactID)
	if caseID == "" || artifactID == "" {
		return "", errors.New("case_id and artifact_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return "", err
	}
	content, err := bughub.ReadEvidenceArtifactFromRoot(a.workflowCommandContext(), store, filepath.Join(a.workflowRoot, "artifacts"), caseID, artifactID)
	if err != nil {
		return "", err
	}
	save := a.workflowSaveArtifact
	if save == nil {
		save = saveFileNative
	}
	destination, err := save("保存故障证据", incidentArtifactDefaultFilename(content.Artifact.Kind), a.getRuntimeContext())
	if err != nil {
		return "", errors.New("save artifact dialog failed")
	}
	if destination == "" {
		return "", nil
	}
	if err := os.WriteFile(destination, content.Content, 0o600); err != nil {
		return "", errors.New("write saved artifact failed")
	}
	return destination, nil
}

func incidentArtifactDefaultFilename(kind string) string {
	switch kind {
	case "screenshot":
		return "incident-screenshot.png"
	case "network":
		return "incident-network.json"
	case "console":
		return "incident-console.txt"
	case "browser_actions":
		return "incident-browser-actions.json"
	default:
		return "incident-evidence.bin"
	}
}

func (a *App) OpenIncidentBrowserLogin(input IncidentBrowserCommandInput) (bughub.IncidentCase, error) {
	a.workflowBrowserMu.Lock()
	defer a.workflowBrowserMu.Unlock()

	incident, attempt, request, operation, err := a.incidentBrowserBlockedAttempt(input, bughub.BrowserRecoveryLogin, "browser_login_required")
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if operation != nil {
		return a.resumeIncidentBrowserRecovery(input, attempt, request, *operation)
	}
	controller := a.workflowBrowser
	if controller == nil {
		return bughub.IncidentCase{}, errors.New("incident browser is unavailable")
	}
	bug, _, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser Case context is unavailable")
	}
	policy, err := (caseBrowserPolicyResolver{app: a}).ResolveBrowserPolicy(a.workflowCommandContext(), incident, bug)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	applicationOrigin, loginOrigin, err := incidentBrowserLoginOrigins(attempt)
	if err != nil || !incidentBrowserOriginAllowed(applicationOrigin, policy.AllowedOrigins) || !incidentBrowserOriginAllowed(loginOrigin, policy.AllowedOrigins) {
		return bughub.IncidentCase{}, errors.New("browser login origin is unavailable")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	claimToken, err := newIncidentBrowserClaimToken()
	if err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser recovery journal is unavailable")
	}
	claimed, acquired, err := store.ClaimBrowserRecoveryOperation(a.workflowCommandContext(), request, claimToken)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if !acquired {
		return a.resumeIncidentBrowserRecovery(input, attempt, request, claimed)
	}
	if err := controller.Login(a.workflowCommandContext(), browserverify.BrowserLoginRequest{
		SystemID: incident.SystemID, Environment: incident.Environment, ApplicationOrigin: applicationOrigin, LoginOrigin: loginOrigin, Policy: policy,
		Emit: func(progress bughub.BrowserProgress) { a.emitIncidentBrowserProgress(input.CaseID, progress) },
	}); err != nil {
		a.markIncidentBrowserRecoveryUncertain(store, request, claimToken)
		return bughub.IncidentCase{}, errors.New("incident browser login failed")
	}
	if a.workflowBrowserRecoveryBeforeOutcome != nil {
		if err := a.workflowBrowserRecoveryBeforeOutcome(); err != nil {
			return bughub.IncidentCase{}, errors.New("incident browser recovery journal is unavailable")
		}
	}
	claimed, err = a.recordIncidentBrowserRecoverySucceeded(store, request, claimToken)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	return a.continueIncidentBrowserRecovery(input, attempt, request, claimed)
}

func (a *App) RepairIncidentBrowserRuntime(input IncidentBrowserCommandInput) (bughub.IncidentCase, error) {
	a.workflowBrowserMu.Lock()
	defer a.workflowBrowserMu.Unlock()

	_, attempt, request, operation, err := a.incidentBrowserBlockedAttempt(input, bughub.BrowserRecoveryRepair, "browser_runtime_broken")
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if operation != nil {
		return a.resumeIncidentBrowserRecovery(input, attempt, request, *operation)
	}
	controller := a.workflowBrowser
	if controller == nil {
		return bughub.IncidentCase{}, errors.New("incident browser is unavailable")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	claimToken, err := newIncidentBrowserClaimToken()
	if err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser recovery journal is unavailable")
	}
	claimed, acquired, err := store.ClaimBrowserRecoveryOperation(a.workflowCommandContext(), request, claimToken)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if !acquired {
		return a.resumeIncidentBrowserRecovery(input, attempt, request, claimed)
	}
	if err := controller.Repair(a.workflowCommandContext(), func(progress bughub.BrowserProgress) {
		a.emitIncidentBrowserProgress(input.CaseID, progress)
	}); err != nil {
		a.markIncidentBrowserRecoveryUncertain(store, request, claimToken)
		return bughub.IncidentCase{}, errors.New("incident browser runtime repair failed")
	}
	if status := controller.Status(); status.State != browserverify.RuntimeReady {
		a.markIncidentBrowserRecoveryUncertain(store, request, claimToken)
		return bughub.IncidentCase{}, errors.New("incident browser runtime is not ready")
	}
	if a.workflowBrowserRecoveryBeforeOutcome != nil {
		if err := a.workflowBrowserRecoveryBeforeOutcome(); err != nil {
			return bughub.IncidentCase{}, errors.New("incident browser recovery journal is unavailable")
		}
	}
	claimed, err = a.recordIncidentBrowserRecoverySucceeded(store, request, claimToken)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	return a.continueIncidentBrowserRecovery(input, attempt, request, claimed)
}

func incidentBrowserContinuationError(err error) error {
	switch {
	case errors.Is(err, bughub.ErrCaseVersionConflict):
		return fmt.Errorf("incident browser continuation version conflict: %w", bughub.ErrCaseVersionConflict)
	case errors.Is(err, bughub.ErrIdempotencyConflict):
		return fmt.Errorf("incident browser continuation idempotency conflict: %w", bughub.ErrIdempotencyConflict)
	case errors.Is(err, bughub.ErrApprovalNotReady):
		return fmt.Errorf("incident browser continuation is no longer ready: %w", bughub.ErrApprovalNotReady)
	case errors.Is(err, bughub.ErrRegressionBinding):
		return fmt.Errorf("incident browser regression binding changed: %w", bughub.ErrRegressionBinding)
	default:
		return errors.New("incident browser continuation failed")
	}
}

func (a *App) ClearIncidentBrowserSession(input IncidentBrowserCommandInput) error {
	a.workflowBrowserMu.Lock()
	defer a.workflowBrowserMu.Unlock()

	incident, attempt, err := a.incidentBrowserClearAttempt(input)
	if err != nil {
		return err
	}
	controller := a.workflowBrowser
	if controller == nil {
		return errors.New("incident browser is unavailable")
	}
	applicationOrigin, loginOrigin, err := incidentBrowserLoginOrigins(attempt)
	if err != nil {
		return errors.New("browser login origin is unavailable")
	}
	bug, _, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return errors.New("incident browser Case context is unavailable")
	}
	policy, err := (caseBrowserPolicyResolver{app: a}).ResolveBrowserPolicy(a.workflowCommandContext(), incident, bug)
	if err != nil || !incidentBrowserOriginAllowed(applicationOrigin, policy.AllowedOrigins) || !incidentBrowserOriginAllowed(loginOrigin, policy.AllowedOrigins) {
		return errors.New("browser login origin is unavailable")
	}
	if err := controller.ClearSession(a.workflowCommandContext(), browserverify.SessionKey{
		SystemID: incident.SystemID, Environment: incident.Environment, Origin: applicationOrigin,
	}); err != nil {
		return errors.New("clear incident browser session failed")
	}
	return nil
}

func (a *App) incidentBrowserClearAttempt(input IncidentBrowserCommandInput) (bughub.IncidentCase, bughub.PhaseAttempt, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	if input.AttemptID == "" {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, errors.New("attempt_id is required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, err
	}
	attempt, err := store.GetAttempt(a.workflowCommandContext(), input.AttemptID)
	if err != nil || attempt.CaseID != input.CaseID || attempt.Status != bughub.AttemptStatusFailed || attempt.ErrorCode != "browser_login_required" || attempt.Phase != bughub.PhaseValidation && attempt.Phase != bughub.PhaseRegression {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, errors.New("incident browser attempt is not eligible for this command")
	}
	incident, err := store.GetCase(a.workflowCommandContext(), input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, err
	}
	if incident.Version != input.ExpectedVersion || incident.Status != bughub.CaseWaitingEvidence || incident.CurrentAttemptID != attempt.ID || incident.CycleNumber != attempt.CycleNumber {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, errors.New("incident browser command requires the current blocked attempt")
	}
	return incident, attempt, nil
}

func (a *App) incidentBrowserBlockedAttempt(input IncidentBrowserCommandInput, operationKind bughub.BrowserRecoveryOperationKind, requiredCode string) (bughub.IncidentCase, bughub.PhaseAttempt, bughub.BrowserRecoveryOperationRequest, *bughub.BrowserRecoveryOperation, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, bughub.BrowserRecoveryOperationRequest{}, nil, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ActorID = strings.TrimSpace(input.ActorID)
	if input.AttemptID == "" {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, bughub.BrowserRecoveryOperationRequest{}, nil, errors.New("attempt_id is required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, bughub.BrowserRecoveryOperationRequest{}, nil, err
	}
	ctx := a.workflowCommandContext()
	attempt, err := store.GetAttempt(ctx, input.AttemptID)
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, bughub.BrowserRecoveryOperationRequest{}, nil, errors.New("incident browser attempt is unavailable")
	}
	if attempt.CaseID != input.CaseID || attempt.Status != bughub.AttemptStatusFailed || attempt.ErrorCode != requiredCode || attempt.Phase != bughub.PhaseValidation && attempt.Phase != bughub.PhaseRegression {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, bughub.BrowserRecoveryOperationRequest{}, nil, errors.New("incident browser attempt is not eligible for this command")
	}
	request := bughub.BrowserRecoveryOperationRequest{
		Operation: operationKind, CaseID: input.CaseID, AttemptID: input.AttemptID,
		ExpectedErrorCode: requiredCode, CycleNumber: attempt.CycleNumber, ExpectedVersion: input.ExpectedVersion,
		ActorID: input.ActorID, IdempotencyKey: input.IdempotencyKey,
	}
	if existing, found, err := store.GetBrowserRecoveryOperation(ctx, request); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, request, nil, err
	} else if found {
		return bughub.IncidentCase{}, attempt, request, &existing, nil
	}
	if _, found, err := store.GetCommittedCaseMutation(ctx, input.IdempotencyKey); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, request, nil, err
	} else if found {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, request, nil, bughub.ErrIdempotencyConflict
	}
	incident, err := store.GetCase(ctx, input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, request, nil, err
	}
	if incident.Version != input.ExpectedVersion || incident.Status != bughub.CaseWaitingEvidence || incident.CurrentAttemptID != attempt.ID || incident.CycleNumber != attempt.CycleNumber {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, request, nil, errors.New("incident browser command requires the current blocked attempt")
	}
	return incident, attempt, request, nil, nil
}

type incidentBrowserRecoveryMarker struct {
	Operation          bughub.BrowserRecoveryOperationKind `json:"operation"`
	BlockedAttemptID   string                              `json:"blocked_attempt_id"`
	ExpectedErrorCode  string                              `json:"expected_browser_error_code"`
	RequestFingerprint string                              `json:"request_fingerprint"`
}

func newIncidentBrowserClaimToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func (a *App) resumeIncidentBrowserRecovery(input IncidentBrowserCommandInput, attempt bughub.PhaseAttempt, request bughub.BrowserRecoveryOperationRequest, operation bughub.BrowserRecoveryOperation) (bughub.IncidentCase, error) {
	switch operation.Status {
	case bughub.BrowserRecoveryClaimed, bughub.BrowserRecoveryOutcomeUncertain:
		return bughub.IncidentCase{}, bughub.ErrBrowserRecoveryOutcomeUncertain
	case bughub.BrowserRecoveryEffectSucceeded:
		return a.continueIncidentBrowserRecovery(input, attempt, request, operation)
	case bughub.BrowserRecoveryContinued:
		return operation.ResultCase.Clone(), nil
	default:
		return bughub.IncidentCase{}, bughub.ErrIdempotencyConflict
	}
}

func (a *App) markIncidentBrowserRecoveryUncertain(store *bughub.CaseStore, request bughub.BrowserRecoveryOperationRequest, claimToken string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = store.RecordBrowserRecoveryOutcome(ctx, request, claimToken, bughub.BrowserRecoveryOutcomeUncertain)
}

func (a *App) recordIncidentBrowserRecoverySucceeded(store *bughub.CaseStore, request bughub.BrowserRecoveryOperationRequest, claimToken string) (bughub.BrowserRecoveryOperation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	operation, err := store.RecordBrowserRecoveryOutcome(ctx, request, claimToken, bughub.BrowserRecoveryEffectSucceeded)
	if err != nil {
		return bughub.BrowserRecoveryOperation{}, errors.New("incident browser recovery journal is unavailable")
	}
	return operation, nil
}

func (a *App) continueIncidentBrowserRecovery(input IncidentBrowserCommandInput, attempt bughub.PhaseAttempt, request bughub.BrowserRecoveryOperationRequest, operation bughub.BrowserRecoveryOperation) (bughub.IncidentCase, error) {
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(request.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser Case context is unavailable")
	}
	marker := incidentBrowserRecoveryMarker{
		Operation: request.Operation, BlockedAttemptID: request.AttemptID,
		ExpectedErrorCode: request.ExpectedErrorCode, RequestFingerprint: operation.RequestFingerprint,
	}
	inputJSON, err := json.Marshal(map[string]any{"browser_recovery": marker})
	if err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser continuation is unavailable")
	}
	if a.workflowBrowserRecoveryBeforeContinuation != nil {
		if err := a.workflowBrowserRecoveryBeforeContinuation(); err != nil {
			return bughub.IncidentCase{}, errors.New("incident browser continuation failed")
		}
	}
	continued, continueErr := orchestrator.ContinueBrowserRecoveryWithEvidence(a.workflowCommandContext(), bughub.ContinueWithEvidenceCommand{
		CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID),
		Phase: attempt.Phase, Bug: bug, Bot: bot, InputJSON: inputJSON,
	}, operation)
	a.emitIncidentResult(continued, continueErr)
	if continueErr != nil {
		return continued, incidentBrowserContinuationError(continueErr)
	}
	return continued, nil
}

func incidentBrowserLoginOrigins(attempt bughub.PhaseAttempt) (string, string, error) {
	var output struct {
		ErrorCode         string `json:"error_code"`
		ApplicationOrigin string `json:"application_origin"`
		LoginOrigin       string `json:"login_origin"`
	}
	if json.Unmarshal(attempt.OutputJSON, &output) != nil || output.ErrorCode != "browser_login_required" || strings.TrimSpace(output.ApplicationOrigin) == "" || strings.TrimSpace(output.LoginOrigin) == "" {
		return "", "", errors.New("browser login origin is unavailable")
	}
	applicationOrigin, err := canonicalIncidentBrowserOrigin(strings.TrimSpace(output.ApplicationOrigin))
	if err != nil {
		return "", "", err
	}
	loginOrigin, err := canonicalIncidentBrowserOrigin(strings.TrimSpace(output.LoginOrigin))
	if err != nil {
		return "", "", err
	}
	return applicationOrigin, loginOrigin, nil
}

func incidentBrowserOriginAllowed(origin string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == origin {
			return true
		}
	}
	return false
}

func (a *App) emitIncidentBrowserProgress(caseID string, progress bughub.BrowserProgress) {
	code := strings.TrimSpace(progress.Code)
	if !strings.HasPrefix(code, "browser_") || !incidentBrowserProgressCodeSafe(code) {
		code = "browser_progress"
	}
	message := "Browser operation in progress"
	switch {
	case strings.Contains(code, "login"):
		message = "Waiting for browser login"
	case strings.Contains(code, "install") || strings.Contains(code, "repair"):
		message = "Preparing browser runtime"
	case strings.HasSuffix(code, "_ready"):
		message = "Browser runtime is ready"
	}
	a.emitIncidentPhaseEvent(caseID, bughub.InvestigationEvent{
		Type: "browser_progress", Message: message,
		Meta: map[string]any{"browser_code": code, "current": progress.Current, "total": progress.Total},
	})
}

func incidentBrowserProgressCodeSafe(code string) bool {
	switch code {
	case "browser_starting", "browser_action_started", "browser_action_completed",
		"browser_login_opened", "browser_login_completed",
		"browser_runtime_installing", "browser_runtime_ready":
		return true
	default:
		return false
	}
}
