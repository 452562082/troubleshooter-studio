package bughub

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
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
	AllowedOrigins     []string `json:"allowed_origins"`
	ApplicationOrigins []string `json:"application_origins"`
	StartOrigins       []string `json:"start_origins"`
	PrivateOrigins     []string `json:"private_origins"`
	AuthOrigins        []string `json:"auth_origins"`
	IsProd             bool     `json:"is_prod"`
}

type BrowserProgress struct {
	Code     string
	Message  string
	ActionID string
	Current  int
	Total    int
}

type BrowserArtifactReference struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	Environment string `json:"environment"`
	Version     string `json:"version,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

type BrowserVerificationResult struct {
	Status               string
	ErrorCode            string
	ErrorMessage         string
	FailedActionID       string
	FinalURL             string
	Title                string
	ApplicationURL       string
	ApplicationOrigin    string
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
	ID              string    `yaml:"id"`
	Action          string    `yaml:"action"`
	Locator         yaml.Node `yaml:"locator,omitempty"`
	URL             yaml.Node `yaml:"url,omitempty"`
	Value           yaml.Node `yaml:"value,omitempty"`
	Key             yaml.Node `yaml:"key,omitempty"`
	ScreenshotAfter yaml.Node `yaml:"screenshot_after,omitempty"`
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
	locatorPresent := browserYAMLFieldPresent(raw.Locator)
	urlPresent := browserYAMLFieldPresent(raw.URL)
	valuePresent := browserYAMLFieldPresent(raw.Value)
	keyPresent := browserYAMLFieldPresent(raw.Key)
	screenshotAfterPresent := browserYAMLFieldPresent(raw.ScreenshotAfter)

	require := func(field string, present bool) error {
		if !present {
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
		if err := require("url", urlPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"locator", locatorPresent},
			browserActionFieldPresence{"value", valuePresent},
			browserActionFieldPresence{"key", keyPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "click", "wait_for":
		if err := require("locator", locatorPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"value", valuePresent},
			browserActionFieldPresence{"key", keyPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "fill", "select":
		if err := require("locator", locatorPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := require("value", valuePresent); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"key", keyPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "press":
		if err := require("locator", locatorPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := require("key", keyPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"value", valuePresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "screenshot":
		if err := forbidFields(
			browserActionFieldPresence{"locator", locatorPresent},
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"value", valuePresent},
			browserActionFieldPresence{"key", keyPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	default:
		return BrowserAction{}, fmt.Errorf("browser plan %s.action %q is not supported", prefix, raw.Action)
	}

	locator, err := decodeBrowserLocatorYAML(prefix+".locator", raw.Locator)
	if err != nil {
		return BrowserAction{}, err
	}
	urlValue, err := decodeBrowserPlanYAMLString(prefix+".url", raw.URL, raw.Action == "goto")
	if err != nil {
		return BrowserAction{}, err
	}
	value, err := decodeBrowserPlanYAMLString(prefix+".value", raw.Value, raw.Action == "fill" || raw.Action == "select")
	if err != nil {
		return BrowserAction{}, err
	}
	key, err := decodeBrowserPlanYAMLString(prefix+".key", raw.Key, raw.Action == "press")
	if err != nil {
		return BrowserAction{}, err
	}
	screenshotAfter, err := decodeBrowserPlanYAMLBool(prefix+".screenshot_after", raw.ScreenshotAfter)
	if err != nil {
		return BrowserAction{}, err
	}
	if raw.Action == "screenshot" && screenshotAfterPresent && screenshotAfter {
		return BrowserAction{}, fmt.Errorf("browser plan %s.screenshot_after=true is forbidden for screenshot", prefix)
	}

	return BrowserAction{
		ID:              raw.ID,
		Action:          raw.Action,
		Locator:         locator,
		URL:             urlValue,
		Value:           value,
		Key:             key,
		ScreenshotAfter: screenshotAfter,
	}, nil
}

func decodeBrowserLocatorYAML(field string, node yaml.Node) (*BrowserLocator, error) {
	if !browserYAMLFieldPresent(node) {
		return nil, nil
	}
	node = resolveBrowserYAMLAlias(node)
	if node.Tag == "!!null" || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("browser plan %s must be a locator mapping", field)
	}
	fields := make(map[string]yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("browser plan %s has a non-scalar field name", field)
		}
		switch key.Value {
		case "kind", "value", "name":
		default:
			return nil, fmt.Errorf("browser plan %s has unknown field %q", field, key.Value)
		}
		if _, exists := fields[key.Value]; exists {
			return nil, fmt.Errorf("browser plan %s field %q is duplicated", field, key.Value)
		}
		fields[key.Value] = *node.Content[i+1]
	}
	kind, err := decodeBrowserPlanYAMLString(field+".kind", fields["kind"], true)
	if err != nil {
		return nil, err
	}
	value, err := decodeBrowserPlanYAMLString(field+".value", fields["value"], true)
	if err != nil {
		return nil, err
	}
	nameNode, namePresent := fields["name"]
	if kind != "role" && namePresent {
		return nil, fmt.Errorf("browser plan %s.name is only allowed for role locators", field)
	}
	name, err := decodeBrowserPlanYAMLString(field+".name", nameNode, false)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "role", "label", "text", "placeholder", "test_id", "css":
	default:
		return nil, fmt.Errorf("browser plan %s.kind %q is not supported", field, kind)
	}
	return &BrowserLocator{Kind: kind, Value: value, Name: name}, nil
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

func decodeBrowserPlanYAMLString(field string, node yaml.Node, required bool) (string, error) {
	if !browserYAMLFieldPresent(node) {
		if required {
			return "", fmt.Errorf("browser plan %s is required", field)
		}
		return "", nil
	}
	node = resolveBrowserYAMLAlias(node)
	if node.Tag == "!!null" {
		return "", fmt.Errorf("browser plan %s must be a string", field)
	}
	var value string
	if err := node.Decode(&value); err != nil {
		return "", fmt.Errorf("browser plan %s must be a string: %w", field, err)
	}
	if err := validateBrowserPlanString(field, value, required); err != nil {
		return "", err
	}
	return value, nil
}

func decodeBrowserPlanYAMLBool(field string, node yaml.Node) (bool, error) {
	if !browserYAMLFieldPresent(node) {
		return false, nil
	}
	node = resolveBrowserYAMLAlias(node)
	if node.Tag == "!!null" {
		return false, fmt.Errorf("browser plan %s must be a boolean", field)
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false, fmt.Errorf("browser plan %s must be a boolean: %w", field, err)
	}
	return value, nil
}

func browserYAMLFieldPresent(node yaml.Node) bool {
	return node.Kind != 0
}

func resolveBrowserYAMLAlias(node yaml.Node) yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = *node.Alias
	}
	return node
}
