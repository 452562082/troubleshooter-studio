package bughub

import (
	"fmt"
	"strings"
)

const BrowserDecisionVersion = 1

// BrowserDecision is the bounded output contract for the future single-step
// browser loop. Parsing it does not authorize execution: the Host must still
// bind ActionID to the frozen BrowserPlan and ElementRef to the current Scene.
type BrowserDecision struct {
	Version           int                             `yaml:"version" json:"version"`
	Decision          string                          `yaml:"decision" json:"decision"`
	SceneID           string                          `yaml:"scene_id" json:"scene_id"`
	RationaleCode     string                          `yaml:"rationale_code" json:"rationale_code"`
	Action            *BrowserDecisionAction          `yaml:"action,omitempty" json:"action,omitempty"`
	ExpectedEffect    *BrowserDecisionExpectedEffects `yaml:"expected_effect,omitempty" json:"expected_effect,omitempty"`
	PassiveChecks     []BrowserDecisionPassiveCheck   `yaml:"passive_checks,omitempty" json:"passive_checks,omitempty"`
	ConclusionCode    string                          `yaml:"conclusion_code,omitempty" json:"conclusion_code,omitempty"`
	Questions         []BrowserValidationQuestion     `yaml:"questions,omitempty" json:"questions,omitempty"`
	CapabilityGapCode string                          `yaml:"capability_gap_code,omitempty" json:"capability_gap_code,omitempty"`
	ExhaustedChannels []string                        `yaml:"exhausted_channels,omitempty" json:"exhausted_channels,omitempty"`
}

type BrowserDecisionAction struct {
	ActionID   string `yaml:"action_id" json:"action_id"`
	Type       string `yaml:"type" json:"type"`
	ElementRef string `yaml:"element_ref,omitempty" json:"element_ref,omitempty"`
}

type BrowserDecisionExpectedEffects struct {
	AnyOf []BrowserDecisionEffect `yaml:"any_of" json:"any_of"`
}

type BrowserDecisionEffect struct {
	Kind                   string `yaml:"kind" json:"kind"`
	ElementRef             string `yaml:"element_ref,omitempty" json:"element_ref,omitempty"`
	Role                   string `yaml:"role,omitempty" json:"role,omitempty"`
	NameContains           string `yaml:"name_contains,omitempty" json:"name_contains,omitempty"`
	SameOriginPathContains string `yaml:"same_origin_path_contains,omitempty" json:"same_origin_path_contains,omitempty"`
	Text                   string `yaml:"text,omitempty" json:"text,omitempty"`
	EvidenceRef            string `yaml:"evidence_ref,omitempty" json:"evidence_ref,omitempty"`
}

type BrowserDecisionPassiveCheck struct {
	Kind string `yaml:"kind" json:"kind"`
}

func ParseBrowserDecision(data []byte, expectedSceneID string) (BrowserDecision, error) {
	var decision BrowserDecision
	if len(data) == 0 || len(data) > 32<<10 || containsSensitiveData(data) {
		return BrowserDecision{}, fmt.Errorf("browser decision is unsafe")
	}
	if err := decodeStrictYAML(data, &decision); err != nil {
		return BrowserDecision{}, fmt.Errorf("parse browser decision: %w", err)
	}
	decision.SceneID = strings.TrimSpace(decision.SceneID)
	decision.RationaleCode = strings.TrimSpace(decision.RationaleCode)
	if decision.Version != BrowserDecisionVersion || !validBrowserDecisionIdentifier(decision.SceneID, 128) ||
		decision.SceneID != strings.TrimSpace(expectedSceneID) || !validBrowserDecisionRationale(decision.RationaleCode) {
		return BrowserDecision{}, fmt.Errorf("browser decision identity is invalid")
	}
	if len(decision.PassiveChecks) > 2 {
		return BrowserDecision{}, fmt.Errorf("browser decision has too many passive checks")
	}
	for _, check := range decision.PassiveChecks {
		if check.Kind != "screenshot" {
			return BrowserDecision{}, fmt.Errorf("browser decision passive check is invalid")
		}
	}
	switch decision.Decision {
	case "act":
		if decision.RationaleCode != "scenario_next_step" && decision.RationaleCode != "locator_recovery" && decision.RationaleCode != "effect_check" {
			return BrowserDecision{}, fmt.Errorf("browser act decision rationale is invalid")
		}
		if err := validateBrowserActDecision(decision); err != nil {
			return BrowserDecision{}, err
		}
	case "conclude":
		if decision.RationaleCode != "evidence_sufficient" || decision.Action != nil || decision.ExpectedEffect != nil || len(decision.PassiveChecks) != 0 ||
			decision.ConclusionCode != "current_evidence_sufficient" || len(decision.Questions) != 0 ||
			decision.CapabilityGapCode != "" || len(decision.ExhaustedChannels) != 0 {
			return BrowserDecision{}, fmt.Errorf("browser conclude decision fields are invalid")
		}
	case "assist":
		if decision.RationaleCode != "user_fact_required" || decision.Action != nil || decision.ExpectedEffect != nil || len(decision.PassiveChecks) != 0 || decision.ConclusionCode != "" ||
			decision.CapabilityGapCode != "" || len(decision.ExhaustedChannels) != 0 || validateBrowserDecisionQuestions(decision.Questions) != nil {
			return BrowserDecision{}, fmt.Errorf("browser assist decision fields are invalid")
		}
	case "capability_gap":
		if decision.RationaleCode != "automation_exhausted" || decision.Action != nil || decision.ExpectedEffect != nil || len(decision.PassiveChecks) != 0 || decision.ConclusionCode != "" ||
			len(decision.Questions) != 0 || decision.CapabilityGapCode != "browser_capability_gap" ||
			!validBrowserDecisionExhaustedChannels(decision.ExhaustedChannels) {
			return BrowserDecision{}, fmt.Errorf("browser capability gap decision fields are invalid")
		}
	default:
		return BrowserDecision{}, fmt.Errorf("browser decision kind is invalid")
	}
	return decision, nil
}

func validateBrowserActDecision(decision BrowserDecision) error {
	if decision.Action == nil || decision.ExpectedEffect == nil || decision.ConclusionCode != "" || len(decision.Questions) != 0 ||
		decision.CapabilityGapCode != "" || len(decision.ExhaustedChannels) != 0 {
		return fmt.Errorf("browser act decision fields are invalid")
	}
	action := decision.Action
	action.ActionID = strings.TrimSpace(action.ActionID)
	action.ElementRef = strings.TrimSpace(action.ElementRef)
	if !validBrowserDecisionIdentifier(action.ActionID, 128) {
		return fmt.Errorf("browser decision action id is invalid")
	}
	switch action.Type {
	case "goto":
		if action.ElementRef != "" {
			return fmt.Errorf("browser goto decision must not contain an element ref")
		}
	case "press":
		if action.ElementRef != "" && !validBrowserDecisionIdentifier(action.ElementRef, 128) {
			return fmt.Errorf("browser press decision element ref is invalid")
		}
	case "dismiss_surface":
		if action.ElementRef != "" {
			return fmt.Errorf("browser dismiss surface decision must not contain an element ref")
		}
	case "click", "fill", "select", "upload_file", "wait_for":
		if !validBrowserDecisionIdentifier(action.ElementRef, 128) {
			return fmt.Errorf("browser decision element ref is invalid")
		}
	default:
		return fmt.Errorf("browser decision action type is invalid")
	}
	if len(decision.ExpectedEffect.AnyOf) < 1 || len(decision.ExpectedEffect.AnyOf) > 4 {
		return fmt.Errorf("browser decision expected effect count is invalid")
	}
	provingEffect := false
	for _, effect := range decision.ExpectedEffect.AnyOf {
		if err := validateBrowserDecisionEffect(effect); err != nil {
			return err
		}
		if effect.Kind != "scene_changed" {
			provingEffect = true
		}
	}
	if !provingEffect {
		return fmt.Errorf("browser decision requires a business-proving expected effect")
	}
	return nil
}

func validateBrowserDecisionEffect(effect BrowserDecisionEffect) error {
	bounded := func(value string, limit int) bool {
		value = strings.TrimSpace(value)
		return value != "" && len(value) <= limit && !browserStrongCredentialSemantic(value)
	}
	emptyExcept := func(values ...string) bool {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return false
			}
		}
		return true
	}
	switch effect.Kind {
	case "url_changed", "url_contains":
		if effect.Kind == "url_contains" && strings.TrimSpace(effect.SameOriginPathContains) == "" {
			return fmt.Errorf("browser decision URL contains effect requires a path")
		}
		if effect.SameOriginPathContains != "" && (!bounded(effect.SameOriginPathContains, 1024) || !strings.HasPrefix(strings.TrimSpace(effect.SameOriginPathContains), "/")) {
			return fmt.Errorf("browser decision URL effect path is invalid")
		}
		if !emptyExcept(effect.ElementRef, effect.Role, effect.NameContains, effect.Text, effect.EvidenceRef) {
			return fmt.Errorf("browser decision URL effect fields are invalid")
		}
	case "surface_opened", "surface_closed":
		if effect.Role != "" && effect.Role != "dialog" && effect.Role != "alertdialog" && effect.Role != "drawer" && effect.Role != "popover" {
			return fmt.Errorf("browser decision surface role is invalid")
		}
		if effect.NameContains != "" && !bounded(effect.NameContains, 512) {
			return fmt.Errorf("browser decision surface name is invalid")
		}
		if !emptyExcept(effect.ElementRef, effect.SameOriginPathContains, effect.Text, effect.EvidenceRef) {
			return fmt.Errorf("browser decision surface effect fields are invalid")
		}
	case "element_visible", "element_absent":
		if strings.TrimSpace(effect.ElementRef) != "" || (strings.TrimSpace(effect.Role) == "" && strings.TrimSpace(effect.NameContains) == "") ||
			(effect.Role != "" && !validBrowserDecisionElementRole(effect.Role)) ||
			(effect.NameContains != "" && !bounded(effect.NameContains, 512)) ||
			!emptyExcept(effect.SameOriginPathContains, effect.Text, effect.EvidenceRef) {
			return fmt.Errorf("browser decision semantic element effect fields are invalid")
		}
	case "input_value_persisted", "selection_persisted":
		if !validBrowserDecisionIdentifier(strings.TrimSpace(effect.ElementRef), 128) ||
			!emptyExcept(effect.Role, effect.NameContains, effect.SameOriginPathContains, effect.Text, effect.EvidenceRef) {
			return fmt.Errorf("browser decision element effect fields are invalid")
		}
	case "text_visible", "text_absent":
		if !bounded(effect.Text, 512) || !emptyExcept(effect.ElementRef, effect.Role, effect.NameContains, effect.SameOriginPathContains, effect.EvidenceRef) {
			return fmt.Errorf("browser decision text effect fields are invalid")
		}
	case "network_request_observed", "response_assertion_passed", "download_observed":
		if !validBrowserDecisionIdentifier(strings.TrimSpace(effect.EvidenceRef), 128) ||
			!emptyExcept(effect.ElementRef, effect.Role, effect.NameContains, effect.SameOriginPathContains, effect.Text) {
			return fmt.Errorf("browser decision evidence effect fields are invalid")
		}
	case "scene_changed":
		if !emptyExcept(effect.ElementRef, effect.Role, effect.NameContains, effect.SameOriginPathContains, effect.Text, effect.EvidenceRef) {
			return fmt.Errorf("browser decision scene effect fields are invalid")
		}
	default:
		return fmt.Errorf("browser decision effect kind is invalid")
	}
	return nil
}

func validBrowserDecisionElementRole(role string) bool {
	switch role {
	case "button", "link", "tab", "menuitem", "option", "checkbox", "radio", "switch", "combobox", "textbox", "searchbox", "heading", "status", "alert":
		return true
	default:
		return false
	}
}

func validBrowserDecisionRationale(value string) bool {
	switch value {
	case "scenario_next_step", "locator_recovery", "effect_check", "evidence_sufficient", "user_fact_required", "automation_exhausted":
		return true
	default:
		return false
	}
}

func validateBrowserDecisionQuestions(questions []BrowserValidationQuestion) error {
	if len(questions) < 1 || len(questions) > 3 {
		return fmt.Errorf("browser decision question count is invalid")
	}
	seen := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		questionID := strings.TrimSpace(question.ID)
		if !validBrowserAssistanceQuestionID(questionID) ||
			!validBrowserAssistanceText(strings.TrimSpace(question.Question), 1000) ||
			!validBrowserAssistanceText(strings.TrimSpace(question.AnswerHint), 1000) ||
			browserStrongCredentialSemantic(question.Question) || browserStrongCredentialSemantic(question.AnswerHint) {
			return fmt.Errorf("browser decision question is invalid")
		}
		if _, duplicate := seen[questionID]; duplicate {
			return fmt.Errorf("browser decision question id is duplicated")
		}
		seen[questionID] = struct{}{}
	}
	return nil
}

func validBrowserDecisionExhaustedChannels(channels []string) bool {
	if len(channels) < 3 || len(channels) > 5 {
		return false
	}
	allowed := map[string]struct{}{
		"semantic_grounding": {}, "structured_grounding": {}, "ai_observe": {}, "visual_grounding": {}, "safe_exploration": {},
	}
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if _, ok := allowed[channel]; !ok {
			return false
		}
		if _, duplicate := seen[channel]; duplicate {
			return false
		}
		seen[channel] = struct{}{}
	}
	return true
}

func validBrowserDecisionIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (index > 0 && strings.ContainsRune("._:-", character)) {
			continue
		}
		return false
	}
	return true
}
