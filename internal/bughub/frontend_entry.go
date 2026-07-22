package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

const (
	FrontendResolutionSelected    = "selected"
	FrontendResolutionAmbiguous   = "ambiguous"
	FrontendResolutionUnavailable = "unavailable"
)

// FrontendEntryBinding is the immutable frontend selection stored with a
// Case. URL is the actual start URL (and may therefore be deeper than the
// configured entry URL); ConfigURL identifies the configured application.
type FrontendEntryBinding struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	URL              string `json:"url"`
	ConfigURL        string `json:"config_url,omitempty"`
	Repo             string `json:"repo,omitempty"`
	DeviceProfile    string `json:"device_profile,omitempty"`
	ResolutionSource string `json:"resolution_source"`
	Score            int    `json:"score,omitempty"`
	Reason           string `json:"reason,omitempty"`
	ConfigSHA256     string `json:"config_sha256,omitempty"`
}

func (b FrontendEntryBinding) Clone() FrontendEntryBinding {
	return b
}

func (b FrontendEntryBinding) IsZero() bool {
	return strings.TrimSpace(b.ID) == "" && strings.TrimSpace(b.URL) == ""
}

type FrontendEntryCandidate struct {
	Binding FrontendEntryBinding `json:"binding"`
	Score   int                  `json:"score"`
	Reasons []string             `json:"reasons"`
}

type FrontendEntryResolution struct {
	Status     string                   `json:"status"`
	Selected   *FrontendEntryBinding    `json:"selected,omitempty"`
	Candidates []FrontendEntryCandidate `json:"candidates,omitempty"`
	Message    string                   `json:"message,omitempty"`
}

// ResolveFrontendEntry uses only durable ticket/configuration evidence. It is
// intentionally deterministic: an Agent may inspect the selected page later,
// but it cannot silently choose a different frontend application.
func ResolveFrontendEntry(entries []config.FrontendEntry, bug Bug, selectedID string) (FrontendEntryResolution, error) {
	prepared := make([]FrontendEntryCandidate, 0, len(entries))
	for _, entry := range entries {
		binding, err := frontendBinding(entry)
		if err != nil {
			return FrontendEntryResolution{}, err
		}
		prepared = append(prepared, scoreFrontendEntry(binding, entry, bug))
	}
	if len(prepared) == 0 {
		if explicit := strings.TrimSpace(bug.FrontendURL); explicit != "" {
			binding, err := explicitTicketFrontendBinding(explicit)
			if err != nil {
				return FrontendEntryResolution{}, err
			}
			return selectedFrontendResolution(binding, nil), nil
		}
		return FrontendEntryResolution{Status: FrontendResolutionUnavailable, Message: "当前环境未配置前端入口，工单也没有可用页面 URL"}, nil
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if prepared[i].Score != prepared[j].Score {
			return prepared[i].Score > prepared[j].Score
		}
		return prepared[i].Binding.ID < prepared[j].Binding.ID
	})
	selectedID = strings.TrimSpace(selectedID)
	if selectedID != "" {
		for _, candidate := range prepared {
			if candidate.Binding.ID == selectedID {
				binding := candidate.Binding.Clone()
				binding.ResolutionSource = "user"
				binding.Score = candidate.Score
				binding.Reason = strings.Join(append([]string{"用户明确选择"}, candidate.Reasons...), "；")
				return selectedFrontendResolution(binding, prepared), nil
			}
		}
		return FrontendEntryResolution{}, errors.New("selected frontend entry is not available in the current environment")
	}
	if explicit := strings.TrimSpace(bug.FrontendURL); explicit != "" {
		canonicalExplicit, err := canonicalFrontendTicketURL(explicit)
		if err != nil {
			return FrontendEntryResolution{}, err
		}
		explicit = canonicalExplicit
		matching := make([]FrontendEntryCandidate, 0, len(prepared))
		for _, candidate := range prepared {
			if frontendURLBelongsToEntry(explicit, candidate.Binding.ConfigURL) {
				matching = append(matching, candidate)
			}
		}
		if len(matching) == 1 {
			return ticketURLFrontendResolution(matching[0], explicit, prepared), nil
		}
		if len(matching) == 0 {
			return FrontendEntryResolution{
				Status:     FrontendResolutionAmbiguous,
				Candidates: prepared,
				Message:    "工单页面 URL 未命中当前环境配置的前端入口，请确认本次验证对应的应用",
			}, nil
		}
		// Multiple applications may share an origin while being mounted at
		// different stable paths. Prefer the longest configured path only when
		// it is unique; otherwise keep evaluating the declared path prefixes and
		// ticket metadata below instead of guessing.
		if specific, ok := mostSpecificFrontendURLMatch(matching); ok {
			return ticketURLFrontendResolution(specific, explicit, prepared), nil
		}
	}
	if len(prepared) == 1 {
		binding := prepared[0].Binding.Clone()
		binding.ResolutionSource = "only_candidate"
		binding.Score = prepared[0].Score
		binding.Reason = strings.Join(prepared[0].Reasons, "；")
		return selectedFrontendResolution(binding, prepared), nil
	}
	top := prepared[0]
	runnerUp := prepared[1]
	if top.Score >= 30 && top.Score-runnerUp.Score >= 15 {
		binding := top.Binding.Clone()
		binding.ResolutionSource = "ticket_signals"
		binding.Score = top.Score
		binding.Reason = strings.Join(top.Reasons, "；")
		return selectedFrontendResolution(binding, prepared), nil
	}
	return FrontendEntryResolution{
		Status: FrontendResolutionAmbiguous, Candidates: prepared,
		Message: "工单证据无法唯一确定前端入口，请选择本次验证对应的应用",
	}, nil
}

func ticketURLFrontendResolution(candidate FrontendEntryCandidate, explicit string, all []FrontendEntryCandidate) FrontendEntryResolution {
	binding := candidate.Binding.Clone()
	binding.URL = explicit
	binding.ResolutionSource = "ticket_url"
	binding.Score = candidate.Score
	binding.Reason = strings.Join(append([]string{"工单页面 URL 命中该入口"}, candidate.Reasons...), "；")
	return selectedFrontendResolution(binding, all)
}

func mostSpecificFrontendURLMatch(candidates []FrontendEntryCandidate) (FrontendEntryCandidate, bool) {
	bestLength := -1
	bestIndex := -1
	tied := false
	for i, candidate := range candidates {
		parsed, err := url.Parse(candidate.Binding.ConfigURL)
		if err != nil {
			continue
		}
		length := len(strings.TrimSuffix(parsed.EscapedPath(), "/"))
		switch {
		case length > bestLength:
			bestLength, bestIndex, tied = length, i, false
		case length == bestLength:
			tied = true
		}
	}
	if bestIndex < 0 || tied {
		return FrontendEntryCandidate{}, false
	}
	return candidates[bestIndex], true
}

func selectedFrontendResolution(binding FrontendEntryBinding, candidates []FrontendEntryCandidate) FrontendEntryResolution {
	cloned := binding.Clone()
	return FrontendEntryResolution{Status: FrontendResolutionSelected, Selected: &cloned, Candidates: candidates}
}

func frontendBinding(entry config.FrontendEntry) (FrontendEntryBinding, error) {
	canonical, err := canonicalFrontendEntryURL(entry.URL)
	if err != nil {
		return FrontendEntryBinding{}, err
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return FrontendEntryBinding{}, err
	}
	digest := sha256.Sum256(raw)
	return FrontendEntryBinding{
		ID: strings.TrimSpace(entry.ID), Name: strings.TrimSpace(entry.Name), URL: canonical, ConfigURL: canonical,
		Repo: strings.TrimSpace(entry.Repo), DeviceProfile: strings.TrimSpace(entry.DeviceProfile), ConfigSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func explicitTicketFrontendBinding(raw string) (FrontendEntryBinding, error) {
	canonical, err := canonicalFrontendTicketURL(raw)
	if err != nil {
		return FrontendEntryBinding{}, err
	}
	digest := sha256.Sum256([]byte(canonical))
	return FrontendEntryBinding{ID: "ticket-url", Name: "工单页面入口", URL: canonical, ConfigURL: canonical, ResolutionSource: "ticket_url", Reason: "使用工单提供的页面 URL", ConfigSHA256: hex.EncodeToString(digest[:])}, nil
}

func canonicalFrontendTicketURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	canonical, _, err := canonicalBrowserURL(raw)
	return canonical, err
}

func canonicalFrontendEntryURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("frontend entry URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func frontendURLBelongsToEntry(rawURL, rawEntry string) bool {
	actual, err := url.Parse(rawURL)
	if err != nil || actual.Hostname() == "" {
		return false
	}
	entry, err := url.Parse(rawEntry)
	if err != nil || entry.Hostname() == "" || !strings.EqualFold(actual.Scheme, entry.Scheme) || !strings.EqualFold(actual.Host, entry.Host) {
		return false
	}
	prefix := strings.TrimSuffix(entry.EscapedPath(), "/")
	return prefix == "" || actual.EscapedPath() == prefix || strings.HasPrefix(actual.EscapedPath(), prefix+"/")
}

func scoreFrontendEntry(binding FrontendEntryBinding, entry config.FrontendEntry, bug Bug) FrontendEntryCandidate {
	score := 0
	reasons := make([]string, 0, 4)
	if repo := strings.TrimSpace(bug.FrontendRepo); repo != "" && strings.EqualFold(repo, strings.TrimSpace(entry.Repo)) {
		score += 50
		reasons = append(reasons, "前端仓库匹配")
	}
	if signalMatches(bug.Product, entry.ProductHints) {
		score += 40
		reasons = append(reasons, "产品匹配")
	}
	if signalMatches(bug.Module, entry.ModuleHints) {
		score += 40
		reasons = append(reasons, "模块匹配")
	}
	if frontendPathMatchesHints(bug.FrontendURL, entry.PathPrefixes) {
		score += 60
		reasons = append(reasons, "工单页面路径匹配")
	}
	text := frontendBugSearchText(bug)
	aliasHits := 0
	for _, alias := range append(append([]string(nil), entry.Aliases...), entry.Name) {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias != "" && strings.Contains(text, alias) {
			aliasHits++
		}
	}
	if aliasHits > 0 {
		if aliasHits > 2 {
			aliasHits = 2
		}
		score += aliasHits * 20
		reasons = append(reasons, "工单文本命中入口名称/别名")
	}
	if device := ticketAttachmentDeviceProfile(bug.Attachments); device != "" && device == strings.TrimSpace(entry.DeviceProfile) {
		score += 12
		reasons = append(reasons, "截图尺寸与设备类型匹配")
	}
	return FrontendEntryCandidate{Binding: binding, Score: score, Reasons: reasons}
}

func frontendPathMatchesHints(rawURL string, prefixes []string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	actual := strings.TrimSuffix(parsed.EscapedPath(), "/")
	for _, rawPrefix := range prefixes {
		prefix := strings.TrimSuffix(strings.TrimSpace(rawPrefix), "/")
		if prefix == "" {
			continue
		}
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		if actual == prefix || strings.HasPrefix(actual, prefix+"/") {
			return true
		}
	}
	return false
}

func signalMatches(value string, hints []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, hint := range hints {
		hint = strings.ToLower(strings.TrimSpace(hint))
		if hint != "" && (value == hint || strings.Contains(value, hint) || strings.Contains(hint, value)) {
			return true
		}
	}
	return false
}

func frontendBugSearchText(bug Bug) string {
	parts := []string{bug.Title, bug.Description, bug.Steps, bug.Expected, bug.Actual, bug.Product, bug.Module, bug.Keywords}
	for _, attachment := range bug.Attachments {
		parts = append(parts, attachment.Name, attachment.RemoteURL)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

func ticketAttachmentDeviceProfile(attachments []Attachment) string {
	portrait, landscape := 0, 0
	for _, attachment := range attachments {
		path := strings.TrimSpace(attachment.LocalPath)
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		cfg, _, decodeErr := image.DecodeConfig(file)
		_ = file.Close()
		if decodeErr != nil || cfg.Width == 0 || cfg.Height == 0 {
			continue
		}
		if cfg.Height > cfg.Width*6/5 {
			portrait++
		} else if cfg.Width > cfg.Height*6/5 {
			landscape++
		}
	}
	if portrait > landscape {
		return "mobile"
	}
	if landscape > portrait {
		return "desktop"
	}
	return ""
}
