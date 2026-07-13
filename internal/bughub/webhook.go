package bughub

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type WebhookResult struct {
	Accepted bool   `json:"accepted"`
	StoredID string `json:"stored_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type webhookBugPayload struct {
	ID          any    `json:"id"`
	Title       string `json:"title"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	AssignedTo  string `json:"assignedTo"`
	Assignee    string `json:"assignee"`
	OpenedBy    string `json:"openedBy"`
	Reporter    string `json:"reporter"`
	Severity    any    `json:"severity"`
	Pri         any    `json:"pri"`
	Priority    any    `json:"priority"`
	Product     string `json:"product"`
	Module      string `json:"module"`
	Type        string `json:"type"`
	OS          string `json:"os"`
	Browser     string `json:"browser"`
	Steps       string `json:"steps"`
	Description string `json:"description"`
	Keywords    string `json:"keywords"`
	URL         string `json:"url"`
	OpenedDate  string `json:"openedDate"`
	EditedDate  string `json:"editedDate"`
}

func BugFromWebhook(platform PlatformConfig, payload []byte) (Bug, WebhookResult, error) {
	if !platform.Enabled {
		return Bug{}, WebhookResult{Accepted: false, Reason: "platform disabled"}, nil
	}
	raw, err := decodeWebhookBug(payload)
	if err != nil {
		return Bug{}, WebhookResult{}, err
	}
	assignee := firstNonEmpty(raw.AssignedTo, raw.Assignee)
	if platform.Account != "" && assignee != "" && !strings.EqualFold(strings.TrimSpace(assignee), strings.TrimSpace(platform.Account)) {
		return Bug{}, WebhookResult{Accepted: false, Reason: "assignee mismatch"}, nil
	}
	sourceID := stringify(raw.ID)
	if sourceID == "" {
		sourceID = randomHex(6)
	}
	title := firstNonEmpty(raw.Title, raw.Name)
	if title == "" {
		return Bug{}, WebhookResult{}, errors.New("bug title is required")
	}
	env, frontend, hints := parseZentaoKeywords(raw.Keywords)
	createdAt := parseZentaoTime(raw.OpenedDate)
	updatedAt := firstTime(parseZentaoTime(raw.EditedDate), time.Now().UTC())
	bug := Bug{
		ID:           platform.ID + "-" + sourceID,
		Source:       platform.Type,
		SourceID:     sourceID,
		Title:        title,
		Status:       raw.Status,
		Severity:     stringify(raw.Severity),
		Priority:     firstNonEmpty(stringify(raw.Pri), stringify(raw.Priority)),
		Product:      raw.Product,
		Module:       raw.Module,
		BugType:      raw.Type,
		OS:           raw.OS,
		Browser:      raw.Browser,
		Keywords:     raw.Keywords,
		Assignee:     assignee,
		Reporter:     firstNonEmpty(raw.OpenedBy, raw.Reporter),
		Steps:        raw.Steps,
		Description:  raw.Description,
		Env:          env,
		FrontendRepo: frontend,
		FrontendURL:  raw.URL,
		ServiceHints: hints,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		RawPreview:   string(payload),
	}
	applyPlatformEnvConfig(platform, &bug)
	return bug, WebhookResult{Accepted: true, StoredID: bug.ID}, nil
}

func decodeWebhookBug(payload []byte) (webhookBugPayload, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return webhookBugPayload{}, err
	}
	for _, key := range []string{"bug", "data", "object"} {
		if raw := root[key]; len(raw) > 0 {
			var bug webhookBugPayload
			if err := json.Unmarshal(raw, &bug); err == nil && (bug.Title != "" || bug.Name != "" || bug.ID != nil) {
				return bug, nil
			}
		}
	}
	var bug webhookBugPayload
	if err := json.Unmarshal(payload, &bug); err != nil {
		return webhookBugPayload{}, err
	}
	return bug, nil
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		data, _ := json.Marshal(x)
		return strings.Trim(strings.TrimSpace(string(data)), `"`)
	}
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return strings.TrimSpace(item)
		}
	}
	return ""
}
