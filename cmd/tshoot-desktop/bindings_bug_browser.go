package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
	content, err := bughub.ReadEvidenceArtifact(a.workflowCommandContext(), store, caseID, artifactID)
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
	content, err := bughub.ReadEvidenceArtifact(a.workflowCommandContext(), store, caseID, artifactID)
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

	incident, attempt, replayed, err := a.incidentBrowserBlockedAttempt(input, "browser_login_required")
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if replayed != nil {
		return replayed.Clone(), nil
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
	loginOrigin, err := incidentBrowserLoginOrigin(attempt)
	if err != nil || !incidentBrowserOriginAllowed(loginOrigin, policy.AllowedOrigins) {
		return bughub.IncidentCase{}, errors.New("browser login origin is unavailable")
	}
	if err := controller.Login(a.workflowCommandContext(), browserverify.BrowserLoginRequest{
		SystemID: incident.SystemID, Environment: incident.Environment, Origin: loginOrigin, Policy: policy,
		Emit: func(progress bughub.BrowserProgress) { a.emitIncidentBrowserProgress(input.CaseID, progress) },
	}); err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser login failed")
	}
	continued, err := a.ContinueIncidentCase(ContinueIncidentCaseInput{
		CaseID: input.CaseID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey,
		ActorID: input.ActorID, Phase: attempt.Phase,
	})
	if err != nil {
		return continued, incidentBrowserContinuationError(err)
	}
	return continued, nil
}

func (a *App) RepairIncidentBrowserRuntime(input IncidentBrowserCommandInput) (bughub.IncidentCase, error) {
	a.workflowBrowserMu.Lock()
	defer a.workflowBrowserMu.Unlock()

	_, attempt, replayed, err := a.incidentBrowserBlockedAttempt(input, "browser_runtime_broken")
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	if replayed != nil {
		return replayed.Clone(), nil
	}
	controller := a.workflowBrowser
	if controller == nil {
		return bughub.IncidentCase{}, errors.New("incident browser is unavailable")
	}
	if err := controller.Repair(a.workflowCommandContext(), func(progress bughub.BrowserProgress) {
		a.emitIncidentBrowserProgress(input.CaseID, progress)
	}); err != nil {
		return bughub.IncidentCase{}, errors.New("incident browser runtime repair failed")
	}
	if status := controller.Status(); status.State != browserverify.RuntimeReady {
		return bughub.IncidentCase{}, errors.New("incident browser runtime is not ready")
	}
	continued, err := a.ContinueIncidentCase(ContinueIncidentCaseInput{
		CaseID: input.CaseID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: input.IdempotencyKey,
		ActorID: input.ActorID, Phase: attempt.Phase,
	})
	if err != nil {
		return continued, incidentBrowserContinuationError(err)
	}
	return continued, nil
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

	incident, attempt, replayed, err := a.incidentBrowserBlockedAttempt(input, "browser_login_required")
	if err != nil {
		return err
	}
	if replayed != nil {
		return errors.New("browser login continuation already completed")
	}
	controller := a.workflowBrowser
	if controller == nil {
		return errors.New("incident browser is unavailable")
	}
	loginOrigin, err := incidentBrowserLoginOrigin(attempt)
	if err != nil {
		return errors.New("browser login origin is unavailable")
	}
	bug, _, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return errors.New("incident browser Case context is unavailable")
	}
	policy, err := (caseBrowserPolicyResolver{app: a}).ResolveBrowserPolicy(a.workflowCommandContext(), incident, bug)
	if err != nil || !incidentBrowserOriginAllowed(loginOrigin, policy.AllowedOrigins) {
		return errors.New("browser login origin is unavailable")
	}
	if err := controller.ClearSession(a.workflowCommandContext(), browserverify.SessionKey{
		SystemID: incident.SystemID, Environment: incident.Environment, Origin: loginOrigin,
	}); err != nil {
		return errors.New("clear incident browser session failed")
	}
	return nil
}

func (a *App) incidentBrowserBlockedAttempt(input IncidentBrowserCommandInput, requiredCode string) (bughub.IncidentCase, bughub.PhaseAttempt, *bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, err
	}
	input.CaseID = strings.TrimSpace(input.CaseID)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ActorID = strings.TrimSpace(input.ActorID)
	if input.AttemptID == "" {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, errors.New("attempt_id is required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, err
	}
	ctx := a.workflowCommandContext()
	attempt, err := store.GetAttempt(ctx, input.AttemptID)
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, errors.New("incident browser attempt is unavailable")
	}
	if attempt.CaseID != input.CaseID || attempt.Status != bughub.AttemptStatusFailed || attempt.ErrorCode != requiredCode || attempt.Phase != bughub.PhaseValidation && attempt.Phase != bughub.PhaseRegression {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, errors.New("incident browser attempt is not eligible for this command")
	}
	if replay, found, err := store.GetCommittedCaseMutation(ctx, input.IdempotencyKey); err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, err
	} else if found {
		if replay.Event.CaseID != input.CaseID || replay.Event.EventType != "evidence_continued" || replay.Event.FromStatus != bughub.CaseWaitingEvidence || replay.Event.ActorID != input.ActorID || replay.ResultCase.Version != input.ExpectedVersion+1 || replay.ResultCase.CycleNumber != attempt.CycleNumber {
			return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, bughub.ErrIdempotencyConflict
		}
		child, childErr := store.GetAttempt(ctx, replay.ResultCase.CurrentAttemptID)
		if childErr != nil || child.ParentAttemptID != attempt.ID || child.CaseID != attempt.CaseID || child.CycleNumber != attempt.CycleNumber || child.Phase != attempt.Phase {
			return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, bughub.ErrIdempotencyConflict
		}
		result := replay.ResultCase.Clone()
		return result, attempt, &result, nil
	}
	incident, err := store.GetCase(ctx, input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, err
	}
	if incident.Version != input.ExpectedVersion || incident.Status != bughub.CaseWaitingEvidence || incident.CurrentAttemptID != attempt.ID || incident.CycleNumber != attempt.CycleNumber {
		return bughub.IncidentCase{}, bughub.PhaseAttempt{}, nil, errors.New("incident browser command requires the current blocked attempt")
	}
	return incident, attempt, nil, nil
}

func incidentBrowserLoginOrigin(attempt bughub.PhaseAttempt) (string, error) {
	var output struct {
		ErrorCode   string `json:"error_code"`
		LoginOrigin string `json:"login_origin"`
	}
	if json.Unmarshal(attempt.OutputJSON, &output) != nil || output.ErrorCode != "browser_login_required" || strings.TrimSpace(output.LoginOrigin) == "" {
		return "", errors.New("browser login origin is unavailable")
	}
	return canonicalIncidentBrowserOrigin(strings.TrimSpace(output.LoginOrigin))
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
