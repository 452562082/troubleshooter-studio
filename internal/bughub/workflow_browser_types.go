package bughub

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxBrowserPlanStringBytes = 4096

const (
	BrowserPlanLegacyVersion = 1
	BrowserPlanVersion       = 2
)

func isSupportedBrowserAction(action string) bool {
	switch action {
	case "goto", "click", "fill", "press", "select", "upload_file", "wait_for", "screenshot":
		return true
	default:
		return false
	}
}

type BrowserPlan struct {
	Version            int                        `yaml:"version" json:"version"`
	DeviceProfile      string                     `yaml:"device_profile,omitempty" json:"device_profile,omitempty"`
	StartURL           string                     `yaml:"start_url" json:"start_url"`
	Actions            []BrowserAction            `yaml:"actions" json:"actions"`
	Assertions         []BrowserAssertion         `yaml:"assertions" json:"assertions"`
	RequestCaptures    []BrowserRequestCapture    `yaml:"request_captures,omitempty" json:"request_captures,omitempty"`
	ResponseAssertions []BrowserResponseAssertion `yaml:"response_assertions,omitempty" json:"response_assertions,omitempty"`
}

type BrowserLocator struct {
	Kind  string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Exact *bool  `yaml:"exact,omitempty" json:"exact,omitempty"`
}

type BrowserAction struct {
	ID              string          `yaml:"id" json:"id"`
	Action          string          `yaml:"action" json:"action"`
	Locator         *BrowserLocator `yaml:"locator,omitempty" json:"locator,omitempty"`
	URL             string          `yaml:"url,omitempty" json:"url,omitempty"`
	Value           string          `yaml:"value,omitempty" json:"value,omitempty"`
	Key             string          `yaml:"key,omitempty" json:"key,omitempty"`
	FileRef         string          `yaml:"file_ref,omitempty" json:"file_ref,omitempty"`
	ScreenshotAfter bool            `yaml:"screenshot_after,omitempty" json:"screenshot_after,omitempty"`
}

type BrowserAssertion struct {
	Kind  string `yaml:"kind" json:"kind"`
	Value string `yaml:"value" json:"value"`
}

// BrowserResponseAssertion evaluates a narrow relationship between two JSON
// fields in an XHR/fetch response caused by one browser action. The worker
// never persists the response body or either field value; only comparison
// counts are emitted as evidence.
type BrowserResponseAssertion struct {
	ID          string `yaml:"id" json:"id"`
	ActionID    string `yaml:"action_id" json:"action_id"`
	URLContains string `yaml:"url_contains,omitempty" json:"url_contains,omitempty"`
	Kind        string `yaml:"kind" json:"kind"`
	LeftField   string `yaml:"left_field" json:"left_field"`
	RightField  string `yaml:"right_field" json:"right_field"`
}

// BrowserRequestCapture declares the minimum request parameters that the
// worker may persist as structured facts. Unlisted body fields never leave the
// worker, and credential-like field paths are rejected before execution.
type BrowserRequestCapture struct {
	ID          string   `yaml:"id" json:"id"`
	ActionID    string   `yaml:"action_id" json:"action_id"`
	URLContains string   `yaml:"url_contains,omitempty" json:"url_contains,omitempty"`
	Method      string   `yaml:"method,omitempty" json:"method,omitempty"`
	Source      string   `yaml:"source" json:"source"`
	Fields      []string `yaml:"fields" json:"fields"`
}

type BrowserAccessibilityNode struct {
	Role        string `json:"role"`
	Name        string `json:"name"`
	LocatorKind string `json:"locator_kind,omitempty"`
	Href        string `json:"href,omitempty"`
	Visible     bool   `json:"visible"`
	Disabled    bool   `json:"disabled"`
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
	UploadFiles []BrowserUploadFile
	StagingDir  string
	Emit        func(BrowserProgress)
}

// BrowserUploadFile is a host-owned, Case-bound input file. BrowserPlan refers
// to it only by ID; the model never sees or controls a local filesystem path.
type BrowserUploadFile struct {
	ID       string
	Name     string
	MIMEType string
	Content  []byte
	SHA256   string
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

// BrowserObserver is an optional capability implemented by the production
// host verifier. Keeping it separate lets tests and alternate verifiers remain
// compatible while new plans are grounded in a live page observation.
type BrowserObserver interface {
	Observe(context.Context, BrowserVerificationRequest) (BrowserVerificationResult, error)
}

type BrowserPolicyResolver interface {
	ResolveBrowserPolicy(context.Context, IncidentCase, Bug) (BrowserSecurityPolicy, error)
}

type browserPlanYAML struct {
	Version            int                        `yaml:"version"`
	DeviceProfile      string                     `yaml:"device_profile,omitempty"`
	StartURL           string                     `yaml:"start_url"`
	Actions            []browserActionYAML        `yaml:"actions"`
	Assertions         []BrowserAssertion         `yaml:"assertions"`
	RequestCaptures    []BrowserRequestCapture    `yaml:"request_captures,omitempty"`
	ResponseAssertions []BrowserResponseAssertion `yaml:"response_assertions,omitempty"`
}

type browserActionYAML struct {
	ID              string    `yaml:"id"`
	Action          string    `yaml:"action"`
	Locator         yaml.Node `yaml:"locator,omitempty"`
	URL             yaml.Node `yaml:"url,omitempty"`
	Value           yaml.Node `yaml:"value,omitempty"`
	Key             yaml.Node `yaml:"key,omitempty"`
	FileRef         yaml.Node `yaml:"file_ref,omitempty"`
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
	if raw.Version != BrowserPlanLegacyVersion && raw.Version != BrowserPlanVersion {
		return BrowserPlan{}, fmt.Errorf("browser plan version must be %d or %d, got %d", BrowserPlanLegacyVersion, BrowserPlanVersion, raw.Version)
	}
	if err := validateBrowserPlanString("start_url", raw.StartURL, true); err != nil {
		return BrowserPlan{}, err
	}
	if len(raw.Actions) < 1 || len(raw.Actions) > 40 {
		return BrowserPlan{}, fmt.Errorf("browser plan actions must contain 1 to 40 entries")
	}
	if raw.Version == BrowserPlanLegacyVersion && (raw.DeviceProfile != "" || len(raw.RequestCaptures) != 0 || len(raw.ResponseAssertions) != 0) {
		return BrowserPlan{}, fmt.Errorf("browser plan device_profile, request_captures, and response_assertions require version %d", BrowserPlanVersion)
	}
	if raw.DeviceProfile != "" && raw.DeviceProfile != "desktop" && raw.DeviceProfile != "mobile" {
		return BrowserPlan{}, fmt.Errorf("browser plan device_profile %q is not supported", raw.DeviceProfile)
	}
	if len(raw.Assertions) == 0 && len(raw.ResponseAssertions) == 0 {
		return BrowserPlan{}, fmt.Errorf("browser plan requires at least one UI or response assertion")
	}

	plan := BrowserPlan{
		Version:            raw.Version,
		DeviceProfile:      raw.DeviceProfile,
		StartURL:           raw.StartURL,
		Actions:            make([]BrowserAction, 0, len(raw.Actions)),
		Assertions:         raw.Assertions,
		RequestCaptures:    raw.RequestCaptures,
		ResponseAssertions: raw.ResponseAssertions,
	}
	seenIDs := make(map[string]struct{}, len(raw.Actions))
	for i, rawAction := range raw.Actions {
		action, err := validateBrowserAction(raw.Version, i, rawAction)
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
		if assertion.Kind != "visible_text" && assertion.Kind != "not_visible_text" && (raw.Version != BrowserPlanVersion || assertion.Kind != "page_loaded") {
			return BrowserPlan{}, fmt.Errorf("browser plan assertions[%d].kind %q is not supported", i, assertion.Kind)
		}
		if err := validateBrowserPlanString(fmt.Sprintf("assertions[%d].value", i), assertion.Value, true); err != nil {
			return BrowserPlan{}, err
		}
	}
	seenAssertionIDs := make(map[string]struct{}, len(plan.ResponseAssertions))
	seenCaptureIDs := make(map[string]struct{}, len(plan.RequestCaptures))
	for i, capture := range plan.RequestCaptures {
		prefix := fmt.Sprintf("request_captures[%d]", i)
		for field, value := range map[string]string{"id": capture.ID, "action_id": capture.ActionID, "source": capture.Source} {
			if err := validateBrowserPlanString(prefix+"."+field, value, true); err != nil {
				return BrowserPlan{}, err
			}
		}
		if err := validateBrowserPlanString(prefix+".url_contains", capture.URLContains, false); err != nil {
			return BrowserPlan{}, err
		}
		if err := validateBrowserPlanString(prefix+".method", capture.Method, false); err != nil {
			return BrowserPlan{}, err
		}
		if _, duplicate := seenCaptureIDs[capture.ID]; duplicate {
			return BrowserPlan{}, fmt.Errorf("browser plan request capture id %q is duplicated", capture.ID)
		}
		seenCaptureIDs[capture.ID] = struct{}{}
		if _, exists := seenIDs[capture.ActionID]; !exists {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.action_id %q does not reference an action", prefix, capture.ActionID)
		}
		for _, action := range plan.Actions {
			if action.ID == capture.ActionID && (action.Action == "screenshot" || action.Action == "wait_for") {
				return BrowserPlan{}, fmt.Errorf("browser plan %s.action_id %q does not reference a request-capable action", prefix, capture.ActionID)
			}
		}
		if capture.Method != "" && capture.Method != strings.ToUpper(capture.Method) {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.method must be uppercase", prefix)
		}
		switch capture.Source {
		case "query", "json", "form", "graphql_variables":
		default:
			return BrowserPlan{}, fmt.Errorf("browser plan %s.source %q is not supported", prefix, capture.Source)
		}
		if len(capture.Fields) < 1 || len(capture.Fields) > 16 {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.fields must contain 1 to 16 entries", prefix)
		}
		seenFields := make(map[string]struct{}, len(capture.Fields))
		for fieldIndex, fieldPath := range capture.Fields {
			if !validBrowserRequestFieldPath(fieldPath) || browserRequestFieldSensitive(fieldPath) {
				return BrowserPlan{}, fmt.Errorf("browser plan %s.fields[%d] is invalid or sensitive", prefix, fieldIndex)
			}
			if _, duplicate := seenFields[fieldPath]; duplicate {
				return BrowserPlan{}, fmt.Errorf("browser plan %s field %q is duplicated", prefix, fieldPath)
			}
			seenFields[fieldPath] = struct{}{}
		}
	}
	for i, assertion := range plan.ResponseAssertions {
		prefix := fmt.Sprintf("response_assertions[%d]", i)
		for field, value := range map[string]string{
			"id": assertion.ID, "action_id": assertion.ActionID, "kind": assertion.Kind,
			"left_field": assertion.LeftField, "right_field": assertion.RightField,
		} {
			if err := validateBrowserPlanString(prefix+"."+field, value, true); err != nil {
				return BrowserPlan{}, err
			}
		}
		if err := validateBrowserPlanString(prefix+".url_contains", assertion.URLContains, false); err != nil {
			return BrowserPlan{}, err
		}
		if _, duplicate := seenAssertionIDs[assertion.ID]; duplicate {
			return BrowserPlan{}, fmt.Errorf("browser plan response assertion id %q is duplicated", assertion.ID)
		}
		seenAssertionIDs[assertion.ID] = struct{}{}
		if _, exists := seenIDs[assertion.ActionID]; !exists {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.action_id %q does not reference an action", prefix, assertion.ActionID)
		}
		pairedCapture := false
		for _, capture := range plan.RequestCaptures {
			if capture.ActionID == assertion.ActionID {
				pairedCapture = true
				break
			}
		}
		if !pairedCapture {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.action_id %q requires a request capture for the same action", prefix, assertion.ActionID)
		}
		for _, action := range plan.Actions {
			if action.ID == assertion.ActionID && (action.Action == "screenshot" || action.Action == "wait_for") {
				return BrowserPlan{}, fmt.Errorf("browser plan %s.action_id %q does not reference a request-capable action", prefix, assertion.ActionID)
			}
		}
		if assertion.Kind != "json_fields_not_equal" && assertion.Kind != "json_fields_equal" {
			return BrowserPlan{}, fmt.Errorf("browser plan %s.kind %q is not supported", prefix, assertion.Kind)
		}
		if !validBrowserJSONFieldPath(assertion.LeftField) || !validBrowserJSONFieldPath(assertion.RightField) {
			return BrowserPlan{}, fmt.Errorf("browser plan %s contains an invalid JSON field path", prefix)
		}
	}
	return plan, nil
}

func validBrowserRequestFieldPath(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 64 {
			return false
		}
		for index, char := range part {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || (index > 0 && ((char >= '0' && char <= '9') || char == '-')) {
				continue
			}
			return false
		}
	}
	return true
}

func browserRequestFieldSensitive(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), ".", "_"))
	for _, token := range []string{"password", "passwd", "secret", "token", "authorization", "auth", "cookie", "session", "api_key", "apikey", "private_key", "access_key", "captcha", "otp"} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func validBrowserJSONFieldPath(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 64 {
			return false
		}
		for index, current := range part {
			if (current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') || current == '_' || (index > 0 && current >= '0' && current <= '9') {
				continue
			}
			return false
		}
	}
	return true
}

func validateBrowserAction(version, index int, raw browserActionYAML) (BrowserAction, error) {
	prefix := fmt.Sprintf("actions[%d]", index)
	if err := validateBrowserPlanString(prefix+".id", raw.ID, true); err != nil {
		return BrowserAction{}, err
	}
	if err := validateBrowserPlanString(prefix+".action", raw.Action, true); err != nil {
		return BrowserAction{}, err
	}
	if !isSupportedBrowserAction(raw.Action) {
		return BrowserAction{}, fmt.Errorf("browser plan %s.action %q is not supported", prefix, raw.Action)
	}
	locatorPresent := browserYAMLFieldPresent(raw.Locator)
	urlPresent := browserYAMLFieldPresent(raw.URL)
	valuePresent := browserYAMLFieldPresent(raw.Value)
	keyPresent := browserYAMLFieldPresent(raw.Key)
	fileRefPresent := browserYAMLFieldPresent(raw.FileRef)
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
			browserActionFieldPresence{"file_ref", fileRefPresent},
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
			browserActionFieldPresence{"file_ref", fileRefPresent},
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
			browserActionFieldPresence{"file_ref", fileRefPresent},
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
			browserActionFieldPresence{"file_ref", fileRefPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "upload_file":
		if err := require("locator", locatorPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := require("file_ref", fileRefPresent); err != nil {
			return BrowserAction{}, err
		}
		if err := forbidFields(
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"value", valuePresent},
			browserActionFieldPresence{"key", keyPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	case "screenshot":
		if err := forbidFields(
			browserActionFieldPresence{"locator", locatorPresent},
			browserActionFieldPresence{"url", urlPresent},
			browserActionFieldPresence{"value", valuePresent},
			browserActionFieldPresence{"key", keyPresent},
			browserActionFieldPresence{"file_ref", fileRefPresent},
		); err != nil {
			return BrowserAction{}, err
		}
	default:
		return BrowserAction{}, fmt.Errorf("browser plan %s.action %q is not supported", prefix, raw.Action)
	}

	locator, err := decodeBrowserLocatorYAML(version, prefix+".locator", raw.Locator)
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
	fileRef, err := decodeBrowserPlanYAMLString(prefix+".file_ref", raw.FileRef, raw.Action == "upload_file")
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
		FileRef:         fileRef,
		ScreenshotAfter: screenshotAfter,
	}, nil
}

func decodeBrowserLocatorYAML(version int, field string, node yaml.Node) (*BrowserLocator, error) {
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
		case "kind", "value", "name", "exact":
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
	exactNode, exactPresent := fields["exact"]
	if exactPresent && (kind == "test_id" || kind == "css" || (kind == "role" && name == "")) {
		return nil, fmt.Errorf("browser plan %s.exact is not meaningful for %s locator", field, kind)
	}
	var exact *bool
	if exactPresent {
		value, err := decodeBrowserPlanYAMLBool(field+".exact", exactNode)
		if err != nil {
			return nil, err
		}
		exact = &value
	}
	return &BrowserLocator{Kind: kind, Value: value, Name: name, Exact: exact}, nil
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
