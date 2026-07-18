package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type BugContextInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

type BugSelectedBotInput struct {
	BugID  string `json:"bug_id"`
	BotKey string `json:"bot_key"`
}

type BugPlatformInput struct {
	ID                  string                      `json:"id"`
	Name                string                      `json:"name"`
	Type                string                      `json:"type"`
	BaseURL             string                      `json:"base_url"`
	Account             string                      `json:"account"`
	Env                 string                      `json:"env"`
	AuthMode            string                      `json:"auth_mode"`
	SessionHeader       string                      `json:"session_header"`
	Password            string                      `json:"password"`
	Token               string                      `json:"token"`
	HookSecret          string                      `json:"hook_secret"`
	BotEnv              string                      `json:"bot_env"`
	BotMappings         []bughub.PlatformBotMapping `json:"bot_mappings"`
	Enabled             bool                        `json:"enabled"`
	PollEnabled         bool                        `json:"poll_enabled"`
	PollIntervalMinutes int                         `json:"poll_interval_minutes"`
}

type BugFetchInput struct {
	PlatformID string `json:"platform_id"`
	BugID      string `json:"bug_id"`
}

type BugAttachmentPreviewInput struct {
	PlatformID      string `json:"platform_id"`
	BugID           string `json:"bug_id"`
	AttachmentIndex int    `json:"attachment_index"`
}

type BugAttachmentPreviewResult struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	DataURL     string `json:"data_url"`
}

type BugLoginInput struct {
	PlatformID string `json:"platform_id"`
}

type BugPlatformDeleteInput struct {
	PlatformID string `json:"platform_id"`
}

type BugLoginResult struct {
	PlatformID   string `json:"platform_id"`
	AuthMode     string `json:"auth_mode"`
	SessionSaved bool   `json:"session_saved"`
	CookieCount  int    `json:"cookie_count"`
	Message      string `json:"message,omitempty"`
}

func (a *App) ListBugPlatforms() ([]bughub.PlatformConfig, error) {
	return bugPlatformStore().List()
}

func (a *App) SaveBugPlatform(input BugPlatformInput) (bughub.PlatformConfig, error) {
	return bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID:                  input.ID,
		Name:                strings.TrimSpace(input.Name),
		Type:                strings.TrimSpace(input.Type),
		BaseURL:             strings.TrimSpace(input.BaseURL),
		Account:             strings.TrimSpace(input.Account),
		Env:                 strings.TrimSpace(input.Env),
		AuthMode:            strings.TrimSpace(input.AuthMode),
		SessionHeader:       strings.TrimSpace(input.SessionHeader),
		Password:            strings.TrimSpace(input.Password),
		Token:               strings.TrimSpace(input.Token),
		HookSecret:          strings.TrimSpace(input.HookSecret),
		BotEnv:              strings.TrimSpace(input.BotEnv),
		BotMappings:         input.BotMappings,
		Enabled:             input.Enabled,
		PollEnabled:         input.PollEnabled,
		PollIntervalMinutes: input.PollIntervalMinutes,
	})
}

func (a *App) DeleteBugPlatform(input BugPlatformDeleteInput) error {
	if platform, err := getBugPlatform(input.PlatformID); err == nil {
		_ = removeZentaoBrowserProfile(platform.BaseURL)
	}
	return bugPlatformStore().Delete(input.PlatformID)
}

func (a *App) BugHookBaseURL() (string, error) {
	if a.bugHookBaseURL != "" {
		return a.bugHookBaseURL, nil
	}
	if a.bugHookErr != nil {
		return "", a.bugHookErr
	}
	a.bugHookOnce.Do(func() {
		a.bugHookBaseURL, a.bugHookErr = startBugHookReceiver(a.templateRoot)
	})
	return a.bugHookBaseURL, a.bugHookErr
}

func (a *App) ListBugs() ([]bughub.Bug, error) {
	return bugStore().List()
}

func (a *App) SyncBugPlatform(platformID string) (bughub.SyncResult, error) {
	platform, err := getBugPlatform(platformID)
	if err != nil {
		return bughub.SyncResult{}, err
	}
	result, err := runZentaoSyncWithSessionRecovery(platform, func(platform bughub.PlatformConfig) (bughub.SyncResult, error) {
		return bughub.SyncZentaoAssigned(platform, bugStore(), nil)
	})
	return result, err
}

func (a *App) FetchBugByID(input BugFetchInput) (bughub.SyncResult, error) {
	platform, err := getBugPlatform(input.PlatformID)
	if err != nil {
		return bughub.SyncResult{}, err
	}
	return runZentaoSyncWithSessionRecovery(platform, func(platform bughub.PlatformConfig) (bughub.SyncResult, error) {
		return bughub.SyncZentaoBug(platform, bugStore(), input.BugID, nil)
	})
}

func (a *App) PreviewBugAttachment(input BugAttachmentPreviewInput) (BugAttachmentPreviewResult, error) {
	bug, ok, err := bugStore().Get(input.BugID)
	if err != nil {
		return BugAttachmentPreviewResult{}, err
	}
	if !ok {
		return BugAttachmentPreviewResult{}, os.ErrNotExist
	}
	if input.AttachmentIndex < 0 || input.AttachmentIndex >= len(bug.Attachments) {
		return BugAttachmentPreviewResult{}, fmt.Errorf("attachment index %d out of range", input.AttachmentIndex)
	}
	att := bug.Attachments[input.AttachmentIndex]
	data, contentType, cachedAtt, err := readBugAttachmentPreview(input.PlatformID, bug.ID, input.AttachmentIndex, att)
	if err != nil {
		return BugAttachmentPreviewResult{}, err
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if cachedAtt.LocalPath != att.LocalPath || cachedAtt.Type != att.Type {
		bug.Attachments[input.AttachmentIndex] = cachedAtt
		_ = bugStore().Upsert(bug)
	}
	return BugAttachmentPreviewResult{
		Name:        att.Name,
		ContentType: contentType,
		DataURL:     "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

func readBugAttachmentPreview(platformID string, bugID string, attachmentIndex int, att bughub.Attachment) ([]byte, string, bughub.Attachment, error) {
	if strings.TrimSpace(att.LocalPath) != "" {
		data, err := os.ReadFile(att.LocalPath)
		if err != nil {
			return nil, "", att, err
		}
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(att.LocalPath)))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		return data, contentType, att, nil
	}
	platform, err := getBugPlatform(platformID)
	if err != nil {
		return nil, "", att, err
	}
	data, contentType, err := (bughub.ZentaoClient{
		BaseURL:       platform.BaseURL,
		Account:       platform.Account,
		AuthMode:      platform.AuthMode,
		SessionHeader: platform.SessionHeader,
		Password:      platform.Password,
		Token:         platform.Token,
	}).FetchAttachment(att)
	if err != nil && shouldRecoverZentaoSession(platform, err) {
		if refreshed, ok := refreshZentaoSession(platform); ok {
			data, contentType, err = (bughub.ZentaoClient{
				BaseURL:       refreshed.BaseURL,
				Account:       refreshed.Account,
				AuthMode:      refreshed.AuthMode,
				SessionHeader: refreshed.SessionHeader,
				Password:      refreshed.Password,
				Token:         refreshed.Token,
			}).FetchAttachment(att)
		}
	}
	if err != nil && clearExpiredZentaoSession(platform, err) {
		return nil, "", att, zentaoSessionExpiredError(err)
	}
	if err != nil {
		return nil, "", att, err
	}
	cachedAtt, err := cacheBugAttachment(bugID, attachmentIndex, att, data, contentType)
	if err != nil {
		if att.Type == "" {
			att.Type = contentType
		}
		return data, contentType, att, nil
	}
	return data, contentType, cachedAtt, nil
}

func cacheBugAttachment(bugID string, idx int, att bughub.Attachment, data []byte, contentType string) (bughub.Attachment, error) {
	dir := filepath.Join(bughub.DefaultRoot(), "attachments", safePathSegment(bugID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return att, err
	}
	name := materializedAttachmentName(att, contentType, idx)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return att, err
	}
	att.LocalPath = path
	if att.Type == "" {
		att.Type = contentType
	}
	return att, nil
}

func (a *App) LoginBugPlatform(input BugLoginInput) (BugLoginResult, error) {
	platform, err := getBugPlatform(input.PlatformID)
	if err != nil {
		return BugLoginResult{}, err
	}
	if strings.TrimSpace(platform.BaseURL) == "" {
		return BugLoginResult{}, errors.New("zentao base url is required")
	}
	_, _ = bugPlatformStore().ClearSessionHeader(platform.ID)
	sessionHeader, cookieCount, err := captureZentaoLoginSession(platform.BaseURL)
	if err != nil {
		return BugLoginResult{}, err
	}
	if err := verifyZentaoSession(platform.BaseURL, sessionHeader); err != nil {
		return BugLoginResult{}, err
	}
	saved, err := bugPlatformStore().SetSessionHeader(platform.ID, "feishu_sso", sessionHeader)
	if err != nil {
		return BugLoginResult{}, err
	}
	_ = removeZentaoBrowserProfile(platform.BaseURL)
	return BugLoginResult{
		PlatformID:   saved.ID,
		AuthMode:     saved.AuthMode,
		SessionSaved: saved.SessionHeader != "",
		CookieCount:  cookieCount,
		Message:      "登录态已保存",
	}, nil
}

func (a *App) ClearBugPlatformLogin(input BugLoginInput) (BugLoginResult, error) {
	platform, err := getBugPlatform(input.PlatformID)
	if err != nil {
		return BugLoginResult{}, err
	}
	cleared, err := bugPlatformStore().ClearSessionHeader(platform.ID)
	if err != nil {
		return BugLoginResult{}, err
	}
	return BugLoginResult{
		PlatformID:   cleared.ID,
		AuthMode:     cleared.AuthMode,
		SessionSaved: false,
		CookieCount:  0,
		Message:      "登录态已清除",
	}, nil
}

func (a *App) MatchBugBots(bugID string) ([]bughub.BotMatch, error) {
	selected, ok, err := bugStore().Get(bugID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	bots, err := a.resolvedBugBotRefs(selected)
	if err != nil {
		return nil, err
	}
	return bughub.MatchBots(selected, bots), nil
}

func (a *App) resolvedBugBotRefs(bug bughub.Bug) ([]bughub.BotRef, error) {
	bots, err := a.bugBotRefs()
	if err != nil {
		return nil, err
	}
	return a.applyStoredBugBotEnvironments(bug, bots)
}

func (a *App) applyStoredBugBotEnvironments(bug bughub.Bug, bots []bughub.BotRef) ([]bughub.BotRef, error) {
	var platform *bughub.PlatformConfig
	configured, ok, err := bugPlatformStore().Get(bug.PlatformID)
	if err != nil {
		return nil, err
	}
	if ok {
		platform = &configured
	}
	return applyBugBotEnvironments(bug, bots, platform), nil
}

func applyBugBotEnvironments(bug bughub.Bug, bots []bughub.BotRef, platform *bughub.PlatformConfig) []bughub.BotRef {
	mapped := map[string]string{}
	if platform != nil {
		for _, item := range platform.BotMappings {
			mapped[strings.TrimSpace(item.BotKey)] = strings.TrimSpace(item.Env)
		}
	}
	fallback := strings.TrimSpace(bug.BotEnv)
	if fallback == "" {
		fallback = strings.TrimSpace(bug.Env)
	}
	out := make([]bughub.BotRef, len(bots))
	copy(out, bots)
	for i := range out {
		env, found := mapped[out[i].Key]
		if !found || env == "" {
			env = fallback
		}
		out[i].Env = env
	}
	return out
}

func (a *App) SaveBugSelectedBot(input BugSelectedBotInput) (bughub.Bug, error) {
	store := bugStore()
	b, ok, err := store.Get(input.BugID)
	if err != nil {
		return bughub.Bug{}, err
	}
	if !ok {
		return bughub.Bug{}, os.ErrNotExist
	}
	botKey := strings.TrimSpace(input.BotKey)
	if botKey == "" {
		return bughub.Bug{}, errors.New("bot key is required")
	}
	b.SelectedBotKey = botKey
	if err := store.Upsert(b); err != nil {
		return bughub.Bug{}, err
	}
	return b, nil
}

func (a *App) GenerateBugContext(input BugContextInput) (string, error) {
	store := bugStore()
	b, ok, err := store.Get(input.BugID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", os.ErrNotExist
	}
	if strings.TrimSpace(input.Bot.Key) == "" {
		return "", errors.New("bot is required")
	}
	ctx := bughub.GenerateContext(b, input.Bot)
	b.SelectedBotKey = input.Bot.Key
	b.LastContext = ctx
	b.LastContextAt = time.Now().UTC()
	b.UpdatedAt = b.LastContextAt
	if err := store.Upsert(b); err != nil {
		return "", err
	}
	return ctx, nil
}

func (a *App) bugBotRefs() ([]bughub.BotRef, error) {
	agents, err := a.DiscoverBots(nil)
	if err != nil {
		return nil, err
	}
	bots := make([]bughub.BotRef, 0, len(agents))
	for _, ag := range agents {
		if ag.Ghost || !bughub.SupportsIncidentWorkflowTarget(ag.Meta.Target) {
			continue
		}
		key := ag.Path + "|" + ag.Meta.Target
		bots = append(bots, bughub.BotRef{
			Key:            key,
			SystemID:       ag.Meta.SystemID,
			Target:         ag.Meta.Target,
			Path:           ag.Path,
			Name:           ag.Meta.SystemName,
			AgentID:        ag.Meta.AgentID,
			Role:           ag.Meta.Role,
			InternalAgents: bugInternalAgents(ag.Meta.InternalAgents),
			Envs:           ag.Environments,
		})
	}
	return bots, nil
}

func bugInternalAgents(in []discover.InternalAgent) []bughub.BotInternalAgent {
	out := make([]bughub.BotInternalAgent, 0, len(in))
	for _, ag := range in {
		out = append(out, bughub.BotInternalAgent{ID: ag.ID, Role: ag.Role})
	}
	return out
}

func bugStore() *bughub.Store {
	return bughub.NewStore(bughub.DefaultRoot())
}

func bugPlatformStore() *bughub.PlatformStore {
	return bughub.NewPlatformStore(bughub.DefaultRoot())
}

func getBugPlatform(id string) (bughub.PlatformConfig, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return bughub.PlatformConfig{}, errors.New("bug platform is required")
	}
	platform, ok, err := bugPlatformStore().Get(id)
	if err != nil {
		return bughub.PlatformConfig{}, err
	}
	if !ok {
		return bughub.PlatformConfig{}, os.ErrNotExist
	}
	return platform, nil
}

func runZentaoSyncWithSessionRecovery(platform bughub.PlatformConfig, run func(bughub.PlatformConfig) (bughub.SyncResult, error)) (bughub.SyncResult, error) {
	result, err := run(platform)
	if err != nil && shouldRecoverZentaoSession(platform, err) {
		if refreshed, ok := refreshZentaoSession(platform); ok {
			result, err = run(refreshed)
		}
	}
	if err != nil && clearExpiredZentaoSession(platform, err) {
		return result, zentaoSessionExpiredError(err)
	}
	return result, err
}

func shouldRecoverZentaoSession(platform bughub.PlatformConfig, err error) bool {
	return bughub.IsZentaoUnauthorized(err) && bugPlatformUsesCapturedSession(platform)
}

func refreshZentaoSession(platform bughub.PlatformConfig) (bughub.PlatformConfig, bool) {
	sessionHeader, _, err := recaptureZentaoLoginSession(platform.BaseURL)
	if err != nil || strings.TrimSpace(sessionHeader) == "" {
		return platform, false
	}
	refreshed, err := bugPlatformStore().SetSessionHeader(platform.ID, "feishu_sso", sessionHeader)
	if err != nil {
		return platform, false
	}
	return refreshed, true
}

func clearExpiredZentaoSession(platform bughub.PlatformConfig, err error) bool {
	if !bughub.IsZentaoUnauthorized(err) || !bugPlatformUsesCapturedSession(platform) {
		return false
	}
	_, _ = bugPlatformStore().ClearSessionHeader(platform.ID)
	return true
}

func bugPlatformUsesCapturedSession(platform bughub.PlatformConfig) bool {
	if strings.TrimSpace(platform.SessionHeader) == "" {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(platform.AuthMode)) {
	case "", "feishu_sso", "session_header":
		return true
	default:
		return false
	}
}

func zentaoSessionExpiredError(err error) error {
	return fmt.Errorf("禅道登录授权已失效，请重新点击“登录平台”授权: %w", err)
}
