package bughub

import (
	"context"
	"fmt"
	"strings"
)

const maxBrowserPlanStringBytes = 4096

type BrowserPlan struct {
	Version    int                `yaml:"version" json:"version"`
	StartURL   string             `yaml:"start_url" json:"start_url"`
	Actions    []BrowserAction    `yaml:"actions" json:"actions"`
	Assertions []BrowserAssertion `yaml:"assertions" json:"assertions"`
}

type BrowserLocator struct {
	Kind  string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
}

type BrowserAction struct {
	ID              string          `yaml:"id" json:"id"`
	Action          string          `yaml:"action" json:"action"`
	Locator         *BrowserLocator `yaml:"locator,omitempty" json:"locator,omitempty"`
	URL             string          `yaml:"url,omitempty" json:"url,omitempty"`
	Value           string          `yaml:"value,omitempty" json:"value,omitempty"`
	Key             string          `yaml:"key,omitempty" json:"key,omitempty"`
	ScreenshotAfter bool            `yaml:"screenshot_after,omitempty" json:"screenshot_after,omitempty"`
}

type BrowserAssertion struct {
	Kind  string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
}

type BrowserAccessibilityNode struct {
	Role     string `json:"role"`
	Name     string `json:"name"`
	Visible  bool   `json:"visible"`
	Disabled bool   `json:"disabled"`
}

type BrowserVerificationRequest struct {
	CaseID      string
	CycleNumber int
	AttemptID   string
	SystemID    string
	Environment string
	Version     string
	Policy      BrowserSecurityPolicy
	Plan        BrowserPlan
	StagingDir  string
	Emit        func(BrowserProgress)
}

type BrowserSecurityPolicy struct {
	AllowedOrigins []string `json:"allowed_origins"`
	PrivateOrigins []string `json:"private_origins"`
	AuthOrigins    []string `json:"auth_origins"`
	IsProd         bool     `json:"is_prod"`
}

type BrowserProgress struct {
	Code     string
	Message  string
	ActionID string
	Current  int
	Total    int
}

type BrowserArtifactReference struct {
	Kind        string
	Path        string
	Environment string
	Version     string
	RequestID   string
	TraceID     string
}

type BrowserVerificationResult struct {
	Status               string
	ErrorCode            string
	ErrorMessage         string
	FailedActionID       string
	FinalURL             string
	Title                string
	LoginOrigin          string
	FinalScreenshotPath  string
	AccessibilitySummary []BrowserAccessibilityNode
	Artifacts            []BrowserArtifactReference
}

type BrowserVerifier interface {
	Execute(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error)
}

type BrowserPolicyResolver interface {
	ResolveBrowserPolicy(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error)
}

type browserPlanYAML struct {
	Version    int                 `yaml:"version"`
	StartURL   string              `yaml:"start_url"`
	Actions    []browserActionYAML `yaml:"actions"`
	Assertions []BrowserAssertion  `yaml:"assertions"`
}

type browserActionYAML struct {
	ID              string          `yaml:"id"`
	Action          string          `yaml:"action"`
	Locator         *BrowserLocator `yaml:"locator,omitempty"`
	URL             *string         `yaml:"url,omitempty"`
	Value           *string         `yaml:"value,omitempty"`
	Key             *string         `yaml:"key,omitempty"`
	ScreenshotAfter *bool           `yaml:"screenshot_after,omitempty"`
}

type browserActionFieldPresence struct {
	name    string
	present bool
}

func ParseBrowserPlan(data []byte) (BrowserPlan, error) {
	var raw browserPlanYAML
	if err := decodeStrictYAML(data, &raw); err != nil {
		return BrowserPlan{}, fmt.Errorf("parse browser plan: %w", err)
	}
	if raw.Version != 1 {
		return BrowserPlan{}, fmt.Errorf("browser plan version must be 1, got %d", raw.Version)
	}
	if err := validateBrowserPlanString("start_url", raw.StartURL, true); err != nil {
		return BrowserPlan{}, err
	}
	if len(raw.Actions) < 1 || len(raw.Actions) > 40 {
		return BrowserPlan{}, fmt.Errorf("browser plan actions must contain 1 to 40 entries")
	}
	if len(raw.Assertions) == 0 {
		return BrowserPlan{}, fmt.Errorf("browser plan requires at least one assertion")
	}

	plan := BrowserPlan{
		Version:    raw.Version,
		StartURL:   raw.StartURL,
		Actions:    make([]BrowserAction, 0, len(raw.Actions)),
		Assertions: raw.Assertions,
	}
	seenIDs := make(map[string]struct{}, len(raw.Actions))
	for i, rawAction := range raw.Actions {
		action, err := validateBrowserAction(i, rawAction)
		if err != nil {
			return BrowserPlan{}, err
		}
		if _, exists := seenIDs[action.ID]; exists {
			return BrowserPlan{}, fmt.Errorf("browser plan action id %q is duplicated", action.ID)
		}
		seenIDs[action.ID] = struct{}{}
		plan.Actions = append(plan.Actions, action)
	}
	for i, assertion := range plan.Assertions {
		if err := validateBrowserPlanString(fmt.Sprintf("assertions[%d].kind", i), assertion.Kind, true); err != nil {
			return BrowserPlan{}, err
		}
		if assertion.Kind != "visible_text" {
			return BrowserPlan{}, fmt.Errorf("browser plan assertions[%d].kind %q is not supported", i, assertion.Kind)
		}
		if err := validateBrowserPlanString(fmt.Sprintf("assertions[%d].value", i), assertion.Value, true); err != nil {
			return BrowserPlan{}, err
		}
	}
	return plan, nil
}

func validateBrowserAction(index int, raw browserActionYAML) (BrowserAction, error) {
	prefix := fmt.Sprintf("actions[%d]", index)
	if err := validateBrowserPlanString(prefix+".id", raw.ID, true); err != nil {
		return BrowserAction{}, err
	}
	if err := validateBrowserPlanString(prefix+".action", raw.Action, true); err != nil {
		return BrowserAction{}, err
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "url", value: raw.URL},
		{name: "value", value: raw.Value},
		{name: "key", value: raw.Key},
	} {
		if field.value != nil {
			if err := validateBrowserPlanString(prefix+"."+field.name, *field.value, false); err != nil {
				return BrowserAction{}, err
			}
		}
	}
	if raw.Locator != nil {
		if err := validateBrowserLocator(prefix+".locator", raw.Locator); err != nil {
			return BrowserAction{}, err
		}
	}

	require := func(field string, present bool, value *string) error {
		if !present || (value != nil && strings.TrimSpace(*value) == "") {
			return fmt.Errorf("browser plan %s.%s is required", prefix, field)
		}
		return nil
	}
	forbid := func(field string, present bool) error {
		if present {
			return fmt.Errorf("browser plan %s.%s is forbidden for action %q", prefix, field, raw.Action)
		}
		return nil
	}
	forbidFields := func(fields ...browserActionFieldPresence) error {
		for _, field := range fields {
			if err := forbid(field.name, field.present); err != nil {
				return err
			}
		}
		return nil
	}

	switch raw.Action {
	case "goto":
		if err := require("url", raw.URL != nil, raw.URL); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"locator", raw.Locator != nil},
			browserActionFieldPresence{"value", raw.Value != nil},
			browserActionFieldPresence{"key", raw.Key != nil},
		); err != nil {
			return BrowserAction{}, err
		}
	case "click", "wait_for":
		if err := require("locator", raw.Locator != nil, nil); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", raw.URL != nil},
			browserActionFieldPresence{"value", raw.Value != nil},
			browserActionFieldPresence{"key", raw.Key != nil},
		); err != nil {
			return BrowserAction{}, err
		}
	case "fill", "select":
		if err := require("locator", raw.Locator != nil, nil); err != nil {
			return BrowserAction{}, err
		}
		if err := require("value", raw.Value != nil, raw.Value); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", raw.URL != nil},
			browserActionFieldPresence{"key", raw.Key != nil},
		); err != nil {
			return BrowserAction{}, err
		}
	case "press":
		if err := require("locator", raw.Locator != nil, nil); err != nil {
			return BrowserAction{}, err
		}
		if err := require("key", raw.Key != nil, raw.Key); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", raw.URL != nil},
			browserActionFieldPresence{"value", raw.Value != nil},
		); err != nil {
			return BrowserAction{}, err
		}
	case "screenshot":
		if err := forbidFields(
			browserActionFieldPresence{"locator", raw.Locator != nil},
			browserActionFieldPresence{"url", raw.URL != nil},
			browserActionFieldPresence{"value", raw.Value != nil},
			browserActionFieldPresence{"key", raw.Key != nil},
		); err != nil {
			return BrowserAction{}, err
		}
		if raw.ScreenshotAfter != nil && *raw.ScreenshotAfter {
			return BrowserAction{}, fmt.Errorf("browser plan %s.screenshot_after=true is forbidden for screenshot", prefix)
		}
	default:
		return BrowserAction{}, fmt.Errorf("browser plan %s.action %q is not supported", prefix, raw.Action)
	}

	return BrowserAction{
		ID:              raw.ID,
		Action:          raw.Action,
		Locator:         raw.Locator,
		URL:             browserStringValue(raw.URL),
		Value:           browserStringValue(raw.Value),
		Key:             browserStringValue(raw.Key),
		ScreenshotAfter: raw.ScreenshotAfter != nil && *raw.ScreenshotAfter,
	}, nil
}

func validateBrowserLocator(field string, locator *BrowserLocator) error {
	if err := validateBrowserPlanString(field+".kind", locator.Kind, true); err != nil {
		return err
	}
	if err := validateBrowserPlanString(field+".value", locator.Value, true); err != nil {
		return err
	}
	if err := validateBrowserPlanString(field+".name", locator.Name, false); err != nil {
		return err
	}
	switch locator.Kind {
	case "role", "label", "text", "placeholder", "test_id", "css":
	default:
		return fmt.Errorf("browser plan %s.kind %q is not supported", field, locator.Kind)
	}
	if locator.Kind != "role" && strings.TrimSpace(locator.Name) != "" {
		return fmt.Errorf("browser plan %s.name is only allowed for role locators", field)
	}
	return nil
}

func validateBrowserPlanString(field, value string, required bool) error {
	if len(value) > maxBrowserPlanStringBytes {
		return fmt.Errorf("browser plan %s exceeds %d bytes", field, maxBrowserPlanStringBytes)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("browser plan %s is required", field)
	}
	return nil
}

func browserStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
