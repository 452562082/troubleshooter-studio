package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const (
	browserRouteJournalName          = "browser-route.json"
	browserRouteJournalKind          = "studio_browser_route"
	browserRouteJournalVersion       = 1
	maxBrowserRouteJournalSize int64 = 256 << 10
)

type browserRouteJournal struct {
	Kind                 string                `json:"kind"`
	Version              int                   `json:"version"`
	CaseID               string                `json:"case_id"`
	CycleNumber          int                   `json:"cycle_number"`
	AttemptID            string                `json:"attempt_id"`
	Assisted             bool                  `json:"assisted"`
	FrontendURL          string                `json:"frontend_url"`
	FrontendEntryID      string                `json:"frontend_entry_id,omitempty"`
	FrontendConfigSHA256 string                `json:"frontend_config_sha256,omitempty"`
	SystemID             string                `json:"system_id"`
	Environment          string                `json:"environment"`
	PolicyResolved       bool                  `json:"policy_resolved"`
	PolicySHA256         string                `json:"policy_sha256"`
	Policy               BrowserSecurityPolicy `json:"policy"`
}

type BrowserRouteRecoveryReader interface {
	BrowserRouteForRecovery(context.Context, PhaseAttempt) (bool, error)
}

type AttemptStagingCleaner interface {
	CleanupAttemptStaging(context.Context, PhaseAttempt) error
}

func (r *AgentPhaseRunner) CleanupAttemptStaging(_ context.Context, attempt PhaseAttempt) error {
	staging, found, err := openExistingBrowserAttemptStaging(r.artifactsRoot, attempt.ID)
	if err != nil || !found {
		return err
	}
	cleanupErr := staging.Cleanup()
	closeErr := staging.Close()
	return errors.Join(cleanupErr, closeErr)
}

func (r *AgentPhaseRunner) BrowserRouteForRecovery(ctx context.Context, attempt PhaseAttempt) (bool, error) {
	if r == nil || r.store == nil {
		return false, errors.New("browser route recovery reader is unavailable")
	}
	incident, err := r.store.GetCase(ctx, attempt.CaseID)
	if err != nil {
		return false, err
	}
	staging, found, err := openExistingBrowserAttemptStaging(r.artifactsRoot, attempt.ID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("browser route recovery staging is missing")
		}
		return false, err
	}
	defer staging.Close()
	route, found, err := loadBrowserRouteJournal(staging.Path(), incident, attempt)
	if err != nil || !found {
		if err == nil {
			err = errors.New("browser route recovery marker is missing")
		}
		return false, err
	}
	return route.Assisted, nil
}

func (r *AgentPhaseRunner) resolveBrowserRoute(ctx context.Context, attempt PhaseAttempt, incident IncidentCase, bug Bug, resolver BrowserPolicyResolver, staging attemptEvidenceStaging) (browserRouteJournal, string, error) {
	route, found, err := loadBrowserRouteJournal(staging.Path(), incident, attempt)
	if err != nil {
		return browserRouteJournal{}, "browser_execution_interrupted", err
	}
	if found {
		code, err := validateCurrentBrowserRoutePolicy(ctx, route, incident, bug, resolver)
		return route, code, err
	}

	route = browserRouteJournal{
		Kind: browserRouteJournalKind, Version: browserRouteJournalVersion,
		CaseID: attempt.CaseID, CycleNumber: attempt.CycleNumber, AttemptID: attempt.ID,
		Assisted: browserAssistedAttempt(bug, attempt), SystemID: incident.SystemID, Environment: incident.Environment,
		FrontendEntryID: incident.FrontendEntry.ID, FrontendConfigSHA256: incident.FrontendEntry.ConfigSHA256,
	}
	if rawURL := strings.TrimSpace(bug.FrontendURL); rawURL != "" {
		canonical, _, canonicalErr := canonicalBrowserURL(rawURL)
		if canonicalErr != nil {
			return browserRouteJournal{}, "browser_execution_interrupted", canonicalErr
		}
		route.FrontendURL = canonical
	}
	code := ""
	if route.Assisted && route.FrontendURL != "" {
		if resolver == nil {
			code = "browser_policy_unavailable"
		} else {
			policy, resolveErr := resolver.ResolveBrowserPolicy(ctx, incident, routeBrowserBug(bug, route))
			if resolveErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return browserRouteJournal{}, "", ctxErr
				}
				code = "browser_policy_unavailable"
			} else {
				route.Policy = canonicalBrowserSecurityPolicy(policy)
				route.PolicyResolved = true
			}
		}
	}
	route.Policy = canonicalBrowserSecurityPolicy(route.Policy)
	route.PolicySHA256, err = browserPolicySHA256(route.Policy)
	if err != nil {
		return browserRouteJournal{}, "browser_execution_interrupted", err
	}
	if err := persistBrowserRouteJournal(staging.Path(), route); err != nil {
		return browserRouteJournal{}, "browser_execution_interrupted", err
	}
	return route, code, nil
}

func routeBrowserBug(bug Bug, route browserRouteJournal) Bug {
	bound := bug
	bound.FrontendURL = route.FrontendURL
	bound.SystemID = route.SystemID
	bound.Env = route.Environment
	bound.BotEnv = route.Environment
	return bound
}

func validateCurrentBrowserRoutePolicy(ctx context.Context, route browserRouteJournal, incident IncidentCase, bug Bug, resolver BrowserPolicyResolver) (string, error) {
	if route.SystemID != incident.SystemID || route.Environment != incident.Environment {
		return "browser_execution_interrupted", errors.New("browser route Case binding changed")
	}
	if !route.Assisted || route.FrontendURL == "" {
		return "", nil
	}
	if !route.PolicyResolved || resolver == nil {
		return "browser_policy_unavailable", nil
	}
	current, err := resolver.ResolveBrowserPolicy(ctx, incident, routeBrowserBug(bug, route))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "browser_policy_unavailable", nil
	}
	current = canonicalBrowserSecurityPolicy(current)
	digest, err := browserPolicySHA256(current)
	if err != nil {
		return "browser_execution_interrupted", err
	}
	if digest != route.PolicySHA256 || !reflect.DeepEqual(current, route.Policy) {
		return "browser_policy_changed", nil
	}
	return "", nil
}

func canonicalBrowserSecurityPolicy(policy BrowserSecurityPolicy) BrowserSecurityPolicy {
	canonical := policy
	canonical.AllowedOrigins = canonicalBrowserPolicyOrigins(policy.AllowedOrigins)
	canonical.ApplicationOrigins = canonicalBrowserPolicyOrigins(policy.ApplicationOrigins)
	canonical.StartOrigins = canonicalBrowserPolicyOrigins(policy.StartOrigins)
	canonical.PrivateOrigins = canonicalBrowserPolicyOrigins(policy.PrivateOrigins)
	canonical.AuthOrigins = canonicalBrowserPolicyOrigins(policy.AuthOrigins)
	return canonical
}

func canonicalBrowserPolicyOrigins(origins []string) []string {
	// Keep the canonical empty value as an allocated slice. The browser worker
	// protocol deliberately requires every origin collection to be a JSON
	// array; a nil slice would otherwise be encoded as null and rejected before
	// Chromium can start.
	values := append([]string{}, origins...)
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return []string{}
	}
	return result
}

func browserPolicySHA256(policy BrowserSecurityPolicy) (string, error) {
	encoded, err := json.Marshal(canonicalBrowserSecurityPolicy(policy))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:]), nil
}

func loadBrowserRouteJournal(root string, incident IncidentCase, attempt PhaseAttempt) (browserRouteJournal, bool, error) {
	path := filepath.Join(root, browserRouteJournalName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return browserRouteJournal{}, false, readErr
		}
		if len(entries) != 0 {
			return browserRouteJournal{}, false, errors.New("attempt staging is missing its durable browser route")
		}
		return browserRouteJournal{}, false, nil
	}
	if err != nil {
		return browserRouteJournal{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > maxBrowserRouteJournalSize {
		return browserRouteJournal{}, false, errors.New("browser route journal is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return browserRouteJournal{}, false, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 {
		_ = file.Close()
		return browserRouteJournal{}, false, errors.New("browser route journal identity changed")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxBrowserRouteJournalSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return browserRouteJournal{}, false, err
	}
	if len(content) == 0 || int64(len(content)) > maxBrowserRouteJournalSize || containsSensitiveData(content) {
		return browserRouteJournal{}, false, errors.New("browser route journal content is unsafe")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var route browserRouteJournal
	if err := decoder.Decode(&route); err != nil {
		return browserRouteJournal{}, false, errors.New("browser route journal is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return browserRouteJournal{}, false, errors.New("browser route journal has trailing content")
	}
	if route.Kind != browserRouteJournalKind || route.Version != browserRouteJournalVersion || route.CaseID != attempt.CaseID || route.CycleNumber != attempt.CycleNumber || route.AttemptID != attempt.ID {
		return browserRouteJournal{}, false, errors.New("browser route journal identity mismatch")
	}
	if !incident.FrontendEntry.IsZero() && (route.FrontendEntryID != incident.FrontendEntry.ID || route.FrontendConfigSHA256 != incident.FrontendEntry.ConfigSHA256) {
		return browserRouteJournal{}, false, errors.New("browser route frontend entry binding mismatch")
	}
	if route.SystemID != incident.SystemID || route.Environment != incident.Environment {
		return browserRouteJournal{}, false, errors.New("browser route journal Case binding mismatch")
	}
	if route.FrontendURL != "" {
		canonical, _, err := canonicalBrowserURL(route.FrontendURL)
		if err != nil || canonical != route.FrontendURL {
			return browserRouteJournal{}, false, errors.New("browser route URL is invalid")
		}
	}
	route.Policy = canonicalBrowserSecurityPolicy(route.Policy)
	digest, err := browserPolicySHA256(route.Policy)
	if err != nil || digest != route.PolicySHA256 {
		return browserRouteJournal{}, false, errors.New("browser route policy digest mismatch")
	}
	if !route.Assisted && (route.PolicyResolved || len(route.Policy.AllowedOrigins) != 0 || len(route.Policy.ApplicationOrigins) != 0 || len(route.Policy.StartOrigins) != 0 || len(route.Policy.PrivateOrigins) != 0 || len(route.Policy.AuthOrigins) != 0 || route.Policy.IsProd) {
		return browserRouteJournal{}, false, errors.New("non-browser route contains a browser policy")
	}
	return route, true, nil
}

func persistBrowserRouteJournal(root string, route browserRouteJournal) error {
	encoded, err := json.Marshal(route)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if int64(len(encoded)) > maxBrowserRouteJournalSize || containsSensitiveData(encoded) {
		return errors.New("browser route journal content is unsafe")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("attempt staging is not empty before browser route publication")
	}
	temporary, err := os.CreateTemp(root, ".browser-route-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writeErr := func() error {
		if _, err := temporary.Write(encoded); err != nil {
			return err
		}
		return temporary.Sync()
	}()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	path := filepath.Join(root, browserRouteJournalName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("browser route journal already exists")
		}
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncBrowserCoordinatorDirectory(root)
}
