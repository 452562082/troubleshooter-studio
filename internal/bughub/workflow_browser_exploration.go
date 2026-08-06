package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const (
	BrowserExplorationMaxStateActions        = 24
	BrowserExplorationMaxActions             = 10
	BrowserExplorationMaxStates              = 16
	BrowserExplorationMaxDecisionCorrections = 2
	BrowserExplorationMaxCandidates          = 16
)

const (
	BrowserExplorationNavigateSameOrigin = "navigate_same_origin"
	BrowserExplorationOpenTab            = "open_tab"
	BrowserExplorationPassiveWait        = "passive_wait"
	BrowserExplorationDismissObstruction = "dismiss_obstruction"
)

const (
	BrowserExplorationProofObservedSameOriginHref = "observed_same_origin_href"
	BrowserExplorationProofARIATab                = "aria_tab"
	BrowserExplorationProofPassiveWait            = "passive_wait"
	BrowserExplorationProofConfirmedObstruction   = "confirmed_non_business_obstruction"
)

const (
	BrowserExplorationDisabledCode      = "browser_exploration_disabled"
	BrowserExplorationNotReadyCode      = "browser_exploration_not_ready"
	BrowserExplorationNotNeededCode     = "browser_exploration_not_needed"
	BrowserExplorationUnsafeCode        = "browser_exploration_unsafe"
	BrowserExplorationLoopCode          = "browser_exploration_loop"
	BrowserExplorationExhaustedCode     = "browser_exploration_exhausted"
	BrowserValidatorDecisionInvalidCode = "browser_validator_decision_invalid"
)

type BrowserExplorationBudget struct {
	MaxStateActions        int
	MaxExplorationActions  int
	MaxStates              int
	MaxDecisionCorrections int
}

func DefaultBrowserExplorationBudget() BrowserExplorationBudget {
	return BrowserExplorationBudget{
		MaxStateActions:        BrowserExplorationMaxStateActions,
		MaxExplorationActions:  BrowserExplorationMaxActions,
		MaxStates:              BrowserExplorationMaxStates,
		MaxDecisionCorrections: BrowserExplorationMaxDecisionCorrections,
	}
}

type BrowserExplorationContext struct {
	AttemptID              string
	ScenarioContractSHA256 string
	FrontendEntryID        string
	Scene                  BrowserScene
	IsProduction           bool
	ScenarioReady          bool
	LoginReady             bool
	TestInputsReady        bool
	AuthorizationReady     bool
	DirectTargetAvailable  bool
}

// BrowserExplorationCandidate is Host-created. The Validator can nominate a
// current element ref, but it cannot mint RiskProofCode or executable values.
type BrowserExplorationCandidate struct {
	Intent        string
	RiskProofCode string
	ElementRef    string
	Action        BrowserAction
}

type BrowserExplorationAdmission struct {
	StateFingerprint  string
	ActionFingerprint string
}

// BrowserRecoverySignals are Host-derived facts from the last execution and
// effect evaluation. Page text and model output must never set these fields.
type BrowserRecoverySignals struct {
	WaitElementRefs                []string
	ConfirmedObstructionSurfaceRef string
}

type BrowserExplorationSnapshot struct {
	StateActions        int
	ExplorationActions  int
	UniqueStates        int
	FailedEdges         int
	DecisionCorrections int
}

type browserExplorationError struct {
	code string
}

func (err *browserExplorationError) Error() string { return err.code }

func BrowserExplorationErrorCode(err error) string {
	var explorationErr *browserExplorationError
	if errors.As(err, &explorationErr) {
		return explorationErr.code
	}
	return ""
}

type BrowserExplorationGuard struct {
	mu                  sync.Mutex
	budget              BrowserExplorationBudget
	stateActions        int
	explorationActions  int
	states              map[string]struct{}
	failedEdges         map[string]struct{}
	reservedEdges       map[string]string
	decisionCorrections map[string]int
}

func NewBrowserExplorationGuard(budget BrowserExplorationBudget) (*BrowserExplorationGuard, error) {
	if budget.MaxStateActions < 1 || budget.MaxStateActions > 100 ||
		budget.MaxExplorationActions < 1 || budget.MaxExplorationActions > 100 ||
		budget.MaxStates < 1 || budget.MaxStates > 100 ||
		budget.MaxDecisionCorrections < 1 || budget.MaxDecisionCorrections > 10 {
		return nil, errors.New("browser exploration budget is invalid")
	}
	return &BrowserExplorationGuard{
		budget: budget, states: make(map[string]struct{}), failedEdges: make(map[string]struct{}),
		reservedEdges: make(map[string]string), decisionCorrections: make(map[string]int),
	}, nil
}

// BuildBrowserExplorationCandidates is the Host-owned deterministic candidate
// generator. It never produces values, uploads, submissions, generic buttons,
// coordinates or model-authored URLs. Duplicate semantics are omitted rather
// than resolved by position.
func BuildBrowserExplorationCandidates(scene BrowserScene, attemptID string) ([]BrowserExplorationCandidate, error) {
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return nil, errors.New("browser exploration candidate Scene is invalid")
	}
	byID := make(map[string]BrowserExplorationCandidate)
	duplicates := make(map[string]struct{})
	add := func(candidate BrowserExplorationCandidate) {
		candidate.Action.ID = browserExplorationCandidateActionID(candidate)
		if candidate.Action.ID == "" {
			return
		}
		if _, duplicate := byID[candidate.Action.ID]; duplicate {
			duplicates[candidate.Action.ID] = struct{}{}
			return
		}
		if _, err := validateBrowserExplorationCandidate(scene, candidate, strings.Repeat("0", sha256.Size*2)); err != nil {
			return
		}
		byID[candidate.Action.ID] = candidate
	}
	for _, element := range scene.Elements {
		if href := strings.TrimSpace(element.LocatorHints.SameOriginHref); href != "" && sameBrowserExplorationOrigin(scene.URL, href) {
			add(BrowserExplorationCandidate{
				Intent: BrowserExplorationNavigateSameOrigin, RiskProofCode: BrowserExplorationProofObservedSameOriginHref,
				ElementRef: element.Ref, Action: BrowserAction{Action: "goto", URL: href},
			})
		}
		if element.Role == "tab" {
			locator, err := browserDecisionElementLocator(element)
			if err == nil {
				add(BrowserExplorationCandidate{
					Intent: BrowserExplorationOpenTab, RiskProofCode: BrowserExplorationProofARIATab,
					ElementRef: element.Ref, Action: BrowserAction{Action: "click", Locator: locator},
				})
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		if _, duplicate := duplicates[id]; !duplicate {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > BrowserExplorationMaxCandidates {
		ids = ids[:BrowserExplorationMaxCandidates]
	}
	candidates := make([]BrowserExplorationCandidate, 0, len(ids))
	for _, id := range ids {
		candidates = append(candidates, byID[id])
	}
	return candidates, nil
}

func BuildBrowserPassiveWaitCandidate(scene BrowserScene, attemptID, elementRef string) (BrowserExplorationCandidate, error) {
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return BrowserExplorationCandidate{}, errors.New("browser exploration wait Scene is invalid")
	}
	element, err := browserDecisionSceneElement(scene, strings.TrimSpace(elementRef))
	if err != nil {
		return BrowserExplorationCandidate{}, err
	}
	locator, err := browserDecisionElementLocator(*element)
	if err != nil {
		return BrowserExplorationCandidate{}, err
	}
	candidate := BrowserExplorationCandidate{
		Intent: BrowserExplorationPassiveWait, RiskProofCode: BrowserExplorationProofPassiveWait,
		ElementRef: element.Ref, Action: BrowserAction{Action: "wait_for", Locator: locator},
	}
	candidate.Action.ID = browserExplorationCandidateActionID(candidate)
	if candidate.Action.ID == "" {
		return BrowserExplorationCandidate{}, errors.New("browser exploration wait identity is invalid")
	}
	if _, err := validateBrowserExplorationCandidate(scene, candidate, strings.Repeat("0", sha256.Size*2)); err != nil {
		return BrowserExplorationCandidate{}, err
	}
	return candidate, nil
}

func BuildBrowserDismissObstructionCandidate(scene BrowserScene, attemptID, surfaceRef string) (BrowserExplorationCandidate, error) {
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return BrowserExplorationCandidate{}, errors.New("browser obstruction recovery Scene is invalid")
	}
	surfaceRef = strings.TrimSpace(surfaceRef)
	if scene.ActiveSurface == nil || surfaceRef == "" || scene.ActiveSurface.Ref != surfaceRef {
		return BrowserExplorationCandidate{}, errors.New("browser obstruction recovery proof is stale")
	}
	candidate := BrowserExplorationCandidate{
		Intent: BrowserExplorationDismissObstruction, RiskProofCode: BrowserExplorationProofConfirmedObstruction,
		Action: BrowserAction{Action: "dismiss_surface"},
	}
	candidate.Action.ID = browserExplorationCandidateActionID(candidate)
	if candidate.Action.ID == "" {
		return BrowserExplorationCandidate{}, errors.New("browser obstruction recovery identity is invalid")
	}
	if _, err := validateBrowserExplorationCandidate(scene, candidate, strings.Repeat("0", sha256.Size*2)); err != nil {
		return BrowserExplorationCandidate{}, err
	}
	return candidate, nil
}

// BuildBrowserRecoveryCandidates merges the ordinary Scene-proven navigation
// graph with Host-proven passive recovery facts. It never infers that a modal
// is an obstruction from its name and never generates fill/select/upload or a
// generic button click.
func BuildBrowserRecoveryCandidates(scene BrowserScene, attemptID string, signals BrowserRecoverySignals) ([]BrowserExplorationCandidate, error) {
	if len(signals.WaitElementRefs) > 2 {
		return nil, errors.New("browser recovery wait signal count is invalid")
	}
	candidates, err := BuildBrowserExplorationCandidates(scene, attemptID)
	if err != nil {
		return nil, err
	}
	for _, elementRef := range signals.WaitElementRefs {
		wait, waitErr := BuildBrowserPassiveWaitCandidate(scene, attemptID, elementRef)
		if waitErr != nil {
			return nil, waitErr
		}
		candidates = append(candidates, wait)
	}
	if strings.TrimSpace(signals.ConfirmedObstructionSurfaceRef) != "" {
		dismiss, dismissErr := BuildBrowserDismissObstructionCandidate(scene, attemptID, signals.ConfirmedObstructionSurfaceRef)
		if dismissErr != nil {
			return nil, dismissErr
		}
		candidates = append(candidates, dismiss)
	}
	byID := make(map[string]BrowserExplorationCandidate, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := byID[candidate.Action.ID]; duplicate {
			return nil, errors.New("browser recovery candidate identity is duplicated")
		}
		byID[candidate.Action.ID] = candidate
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > BrowserExplorationMaxCandidates {
		ids = ids[:BrowserExplorationMaxCandidates]
	}
	result := make([]BrowserExplorationCandidate, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}

// BuildBrowserDecisionPlanWithExploration returns an ephemeral binding plan.
// The frozen base plan is copied, and only candidates revalidated against the
// exact current Scene are appended. Callers must never persist this as a new
// scenario contract or silently replace the original BrowserPlan.
func BuildBrowserDecisionPlanWithExploration(
	base BrowserPlan,
	scene BrowserScene,
	attemptID string,
	candidates []BrowserExplorationCandidate,
) (BrowserPlan, error) {
	if err := validateDurableBrowserPlan(base); err != nil {
		return BrowserPlan{}, err
	}
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil || len(candidates) > BrowserExplorationMaxCandidates {
		return BrowserPlan{}, errors.New("browser exploration binding plan input is invalid")
	}
	result := base
	result.Actions = append([]BrowserAction(nil), base.Actions...)
	actionIDs := make(map[string]struct{}, len(base.Actions)+len(candidates))
	for _, action := range base.Actions {
		actionIDs[action.ID] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, err := validateBrowserExplorationCandidate(scene, candidate, strings.Repeat("0", sha256.Size*2)); err != nil {
			return BrowserPlan{}, errors.New("browser exploration candidate cannot enter the binding plan")
		}
		if _, duplicate := actionIDs[candidate.Action.ID]; duplicate {
			return BrowserPlan{}, errors.New("browser exploration action id conflicts with the frozen plan")
		}
		actionIDs[candidate.Action.ID] = struct{}{}
		result.Actions = append(result.Actions, candidate.Action)
	}
	if err := validateDurableBrowserPlan(result); err != nil {
		return BrowserPlan{}, err
	}
	return result, nil
}

func browserExplorationCandidateActionID(candidate BrowserExplorationCandidate) string {
	target := struct {
		Intent     string          `json:"intent"`
		Proof      string          `json:"proof"`
		ActionType string          `json:"action_type"`
		Locator    *BrowserLocator `json:"locator,omitempty"`
		URL        string          `json:"url,omitempty"`
	}{
		Intent: candidate.Intent, Proof: candidate.RiskProofCode, ActionType: candidate.Action.Action,
		Locator: candidate.Action.Locator, URL: candidate.Action.URL,
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return "explore-" + hex.EncodeToString(digest[:8])
}

// ReserveScenarioAction accounts for an explicit scenario action. It does not
// authorize execution; it only shares the attempt-wide state-action budget
// with exploration so exploration cannot evade the global cap.
func (guard *BrowserExplorationGuard) ReserveScenarioAction(action BrowserAction) error {
	if !isSupportedBrowserAction(action.Action) {
		return errors.New("browser scenario action type is invalid")
	}
	if !browserActionConsumesStateBudget(action.Action) {
		return nil
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.stateActions >= guard.budget.MaxStateActions {
		return &browserExplorationError{code: BrowserExplorationExhaustedCode}
	}
	guard.stateActions++
	return nil
}

func (guard *BrowserExplorationGuard) ReserveExploration(context BrowserExplorationContext, candidate BrowserExplorationCandidate) (BrowserExplorationAdmission, error) {
	if err := validateBrowserExplorationPrerequisites(context); err != nil {
		return BrowserExplorationAdmission{}, err
	}
	stateFingerprint, err := browserExplorationStateFingerprint(context.Scene, context.AttemptID, context.FrontendEntryID)
	if err != nil {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationUnsafeCode}
	}
	actionFingerprint, err := validateBrowserExplorationCandidate(context.Scene, candidate, stateFingerprint)
	if err != nil {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationUnsafeCode}
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	consumesStateBudget := browserActionConsumesStateBudget(candidate.Action.Action)
	if (consumesStateBudget && guard.stateActions >= guard.budget.MaxStateActions) || guard.explorationActions >= guard.budget.MaxExplorationActions {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationExhaustedCode}
	}
	if _, failed := guard.failedEdges[actionFingerprint]; failed {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationLoopCode}
	}
	if _, reserved := guard.reservedEdges[actionFingerprint]; reserved {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationLoopCode}
	}
	if _, known := guard.states[stateFingerprint]; !known && len(guard.states) >= guard.budget.MaxStates {
		return BrowserExplorationAdmission{}, &browserExplorationError{code: BrowserExplorationExhaustedCode}
	}
	guard.states[stateFingerprint] = struct{}{}
	guard.reservedEdges[actionFingerprint] = stateFingerprint
	if consumesStateBudget {
		guard.stateActions++
	}
	guard.explorationActions++
	return BrowserExplorationAdmission{StateFingerprint: stateFingerprint, ActionFingerprint: actionFingerprint}, nil
}

// AvailableCandidates removes edges that the Host has already reserved or
// proved failed and returns no candidates once the attempt-wide exploration
// budget is exhausted. Unlike ReserveExploration it is read-only, so merely
// showing candidates to the Decision provider never consumes the budget.
func (guard *BrowserExplorationGuard) AvailableCandidates(context BrowserExplorationContext, candidates []BrowserExplorationCandidate) ([]BrowserExplorationCandidate, error) {
	if len(candidates) > BrowserExplorationMaxCandidates {
		return nil, errors.New("browser exploration candidate count is invalid")
	}
	stateFingerprint, err := browserExplorationStateFingerprint(context.Scene, context.AttemptID, context.FrontendEntryID)
	if err != nil {
		return nil, &browserExplorationError{code: BrowserExplorationUnsafeCode}
	}
	type candidateFingerprint struct {
		candidate   BrowserExplorationCandidate
		fingerprint string
	}
	checked := make([]candidateFingerprint, 0, len(candidates))
	for _, candidate := range candidates {
		fingerprint, validateErr := validateBrowserExplorationCandidate(context.Scene, candidate, stateFingerprint)
		if validateErr != nil {
			return nil, &browserExplorationError{code: BrowserExplorationUnsafeCode}
		}
		checked = append(checked, candidateFingerprint{candidate: candidate, fingerprint: fingerprint})
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.explorationActions >= guard.budget.MaxExplorationActions ||
		(len(guard.states) >= guard.budget.MaxStates && !browserExplorationStateKnown(guard.states, stateFingerprint)) {
		return []BrowserExplorationCandidate{}, nil
	}
	available := make([]BrowserExplorationCandidate, 0, len(checked))
	for _, item := range checked {
		if browserActionConsumesStateBudget(item.candidate.Action.Action) && guard.stateActions >= guard.budget.MaxStateActions {
			continue
		}
		if _, failed := guard.failedEdges[item.fingerprint]; failed {
			continue
		}
		if _, reserved := guard.reservedEdges[item.fingerprint]; reserved {
			continue
		}
		available = append(available, item.candidate)
	}
	return available, nil
}

func browserExplorationStateKnown(states map[string]struct{}, fingerprint string) bool {
	_, known := states[fingerprint]
	return known
}

func browserActionConsumesStateBudget(action string) bool {
	return action != "wait_for" && action != "screenshot"
}

func (guard *BrowserExplorationGuard) RecordOutcome(admission BrowserExplorationAdmission, outcome string) error {
	if !validLowerSHA256(admission.StateFingerprint) || !validLowerSHA256(admission.ActionFingerprint) {
		return errors.New("browser exploration admission is invalid")
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if _, known := guard.states[admission.StateFingerprint]; !known {
		return errors.New("browser exploration state was not reserved")
	}
	if stateFingerprint, reserved := guard.reservedEdges[admission.ActionFingerprint]; !reserved || stateFingerprint != admission.StateFingerprint {
		return errors.New("browser exploration action was not reserved")
	}
	switch outcome {
	case BrowserEffectConfirmed:
		return nil
	case BrowserEffectNoEffect, BrowserEffectBlocked, BrowserEffectAmbiguous, BrowserEffectUncertain:
		guard.failedEdges[admission.ActionFingerprint] = struct{}{}
		return nil
	default:
		return errors.New("browser exploration outcome is invalid")
	}
}

func (guard *BrowserExplorationGuard) ReserveDecisionCorrection(scene BrowserScene, attemptID string) error {
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return &browserExplorationError{code: BrowserValidatorDecisionInvalidCode}
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.decisionCorrections[scene.SceneSHA256] >= guard.budget.MaxDecisionCorrections {
		return &browserExplorationError{code: BrowserValidatorDecisionInvalidCode}
	}
	guard.decisionCorrections[scene.SceneSHA256]++
	return nil
}

func (guard *BrowserExplorationGuard) Snapshot() BrowserExplorationSnapshot {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	corrections := 0
	for _, count := range guard.decisionCorrections {
		corrections += count
	}
	return BrowserExplorationSnapshot{
		StateActions: guard.stateActions, ExplorationActions: guard.explorationActions,
		UniqueStates: len(guard.states), FailedEdges: len(guard.failedEdges), DecisionCorrections: corrections,
	}
}

func validateBrowserExplorationPrerequisites(context BrowserExplorationContext) error {
	if context.IsProduction {
		return &browserExplorationError{code: BrowserExplorationDisabledCode}
	}
	if !context.ScenarioReady || !context.LoginReady || !context.TestInputsReady || !context.AuthorizationReady ||
		!validLowerSHA256(context.ScenarioContractSHA256) || !validBrowserDecisionIdentifier(strings.TrimSpace(context.FrontendEntryID), 128) {
		return &browserExplorationError{code: BrowserExplorationNotReadyCode}
	}
	if context.DirectTargetAvailable {
		return &browserExplorationError{code: BrowserExplorationNotNeededCode}
	}
	return nil
}

func browserExplorationStateFingerprint(scene BrowserScene, attemptID, frontendEntryID string) (string, error) {
	if err := validateBoundBrowserDecisionScene(scene, attemptID, scene.SceneID); err != nil {
		return "", err
	}
	parsed, err := url.Parse(scene.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("browser exploration scene URL is invalid")
	}
	normalizedPath := path.Clean(parsed.EscapedPath())
	if normalizedPath == "." || normalizedPath == "" {
		normalizedPath = "/"
	}
	type elementIdentity struct {
		Role       string `json:"role"`
		Name       string `json:"name"`
		SurfaceRef string `json:"surface_ref,omitempty"`
		RowName    string `json:"row_name,omitempty"`
		GroupName  string `json:"group_name,omitempty"`
	}
	elements := make([]elementIdentity, 0, len(scene.Elements))
	for _, element := range scene.Elements {
		if !element.States.Visible || !element.States.InViewport || element.FrameRef != "f-main" {
			continue
		}
		elements = append(elements, elementIdentity{
			Role: element.Role, Name: element.Name, SurfaceRef: element.SurfaceRef,
			RowName: element.Relations.RowName, GroupName: element.Relations.GroupName,
		})
		if elements[len(elements)-1].SurfaceRef != "" {
			elements[len(elements)-1].SurfaceRef = "active"
		}
	}
	sort.Slice(elements, func(left, right int) bool {
		leftJSON, _ := json.Marshal(elements[left])
		rightJSON, _ := json.Marshal(elements[right])
		return string(leftJSON) < string(rightJSON)
	})
	identity := struct {
		Origin        string               `json:"origin"`
		Path          string               `json:"path"`
		DeviceProfile string               `json:"device_profile"`
		ActiveSurface *BrowserSceneSurface `json:"active_surface,omitempty"`
		Elements      []elementIdentity    `json:"elements"`
		FrontendEntry string               `json:"frontend_entry"`
	}{
		Origin: strings.ToLower(parsed.Scheme + "://" + parsed.Host), Path: normalizedPath,
		DeviceProfile: scene.DeviceProfile, ActiveSurface: scene.ActiveSurface,
		Elements: elements, FrontendEntry: strings.TrimSpace(frontendEntryID),
	}
	if identity.ActiveSurface != nil {
		surface := *identity.ActiveSurface
		surface.Ref = ""
		identity.ActiveSurface = &surface
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateBrowserExplorationCandidate(scene BrowserScene, candidate BrowserExplorationCandidate, stateFingerprint string) (string, error) {
	if !validBrowserDecisionIdentifier(strings.TrimSpace(candidate.Action.ID), 128) || candidate.Action.Value != "" || candidate.Action.FileRef != "" || candidate.Action.ScreenshotAfter {
		return "", errors.New("browser exploration action fields are unsafe")
	}
	var element *BrowserSceneElement
	if candidate.ElementRef != "" {
		var err error
		element, err = browserDecisionSceneElement(scene, candidate.ElementRef)
		if err != nil || element.FrameRef != "f-main" || !element.States.Visible || !element.States.InViewport || element.States.Obscured ||
			(scene.ActiveSurface != nil && element.SurfaceRef != scene.ActiveSurface.Ref) || (scene.ActiveSurface == nil && element.SurfaceRef != "") {
			return "", errors.New("browser exploration element is not safely actionable")
		}
	}
	switch candidate.Intent {
	case BrowserExplorationNavigateSameOrigin:
		if candidate.RiskProofCode != BrowserExplorationProofObservedSameOriginHref || element == nil || candidate.Action.Action != "goto" ||
			candidate.Action.Locator != nil || candidate.Action.Key != "" || candidate.Action.URL == "" || candidate.Action.URL != element.LocatorHints.SameOriginHref ||
			!sameBrowserExplorationOrigin(scene.URL, candidate.Action.URL) {
			return "", errors.New("browser exploration navigation proof is invalid")
		}
	case BrowserExplorationOpenTab:
		if candidate.RiskProofCode != BrowserExplorationProofARIATab || element == nil || !element.States.Enabled || element.Role != "tab" || candidate.Action.Action != "click" || candidate.Action.URL != "" || candidate.Action.Key != "" {
			return "", errors.New("browser exploration tab proof is invalid")
		}
		locator, err := browserDecisionElementLocator(*element)
		if err != nil || !reflect.DeepEqual(locator, candidate.Action.Locator) {
			return "", errors.New("browser exploration tab binding is invalid")
		}
	case BrowserExplorationPassiveWait:
		if candidate.RiskProofCode != BrowserExplorationProofPassiveWait || element == nil || candidate.Action.Action != "wait_for" || candidate.Action.URL != "" || candidate.Action.Key != "" {
			return "", errors.New("browser exploration wait proof is invalid")
		}
		locator, err := browserDecisionElementLocator(*element)
		if err != nil || !reflect.DeepEqual(locator, candidate.Action.Locator) {
			return "", errors.New("browser exploration wait binding is invalid")
		}
	case BrowserExplorationDismissObstruction:
		if candidate.RiskProofCode != BrowserExplorationProofConfirmedObstruction || candidate.ElementRef != "" || scene.ActiveSurface == nil ||
			candidate.Action.Action != "dismiss_surface" || candidate.Action.Locator != nil || candidate.Action.URL != "" || candidate.Action.Key != "" {
			return "", errors.New("browser exploration obstruction proof is invalid")
		}
	default:
		return "", errors.New("browser exploration intent is invalid")
	}
	targetIdentity := struct {
		Role        string `json:"role,omitempty"`
		Name        string `json:"name,omitempty"`
		RowName     string `json:"row_name,omitempty"`
		GroupName   string `json:"group_name,omitempty"`
		Destination string `json:"destination,omitempty"`
		SurfaceType string `json:"surface_type,omitempty"`
		SurfaceName string `json:"surface_name,omitempty"`
	}{}
	if element != nil {
		targetIdentity.Role = element.Role
		targetIdentity.Name = element.Name
		targetIdentity.RowName = element.Relations.RowName
		targetIdentity.GroupName = element.Relations.GroupName
	}
	if candidate.Intent == BrowserExplorationNavigateSameOrigin {
		parsed, _ := url.Parse(candidate.Action.URL)
		targetIdentity.Destination = strings.ToLower(parsed.Scheme+"://"+parsed.Host) + path.Clean(parsed.EscapedPath())
	}
	if candidate.Intent == BrowserExplorationDismissObstruction && scene.ActiveSurface != nil {
		targetIdentity.SurfaceType = scene.ActiveSurface.Type
		targetIdentity.SurfaceName = scene.ActiveSurface.Name
	}
	actionIdentity := struct {
		StateFingerprint string `json:"state_fingerprint"`
		Intent           string `json:"intent"`
		RiskProofCode    string `json:"risk_proof_code"`
		ActionType       string `json:"action_type"`
		Target           any    `json:"target"`
	}{
		StateFingerprint: stateFingerprint, Intent: candidate.Intent, RiskProofCode: candidate.RiskProofCode,
		ActionType: candidate.Action.Action, Target: targetIdentity,
	}
	encoded, err := json.Marshal(actionIdentity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sameBrowserExplorationOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil || leftURL.Scheme == "" || rightURL.Scheme == "" || leftURL.Host == "" || rightURL.Host == "" || rightURL.User != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) && strings.EqualFold(leftURL.Host, rightURL.Host)
}
