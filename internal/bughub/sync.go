package bughub

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var bugIDPattern = regexp.MustCompile(`(?i)#\s*([0-9]+)|bug\s*#?\s*([0-9]+)|^\s*([0-9]+)\s*$`)

type SyncResult struct {
	PlatformID    string   `json:"platform_id"`
	Fetched       int      `json:"fetched"`
	Stored        int      `json:"stored"`
	SelectedBugID string   `json:"selected_bug_id,omitempty"`
	Account       string   `json:"account,omitempty"`
	RawFetched    int      `json:"raw_fetched,omitempty"`
	Filtered      int      `json:"filtered,omitempty"`
	Pruned        int      `json:"pruned,omitempty"`
	PrunedIDs     []string `json:"-"`
	ProductCount  int      `json:"product_count,omitempty"`
}

func SyncZentaoAssigned(platform PlatformConfig, store *Store, client *http.Client) (SyncResult, error) {
	result := SyncResult{PlatformID: platform.ID}
	if store == nil {
		return result, fmt.Errorf("bug store is required")
	}
	if err := validateZentaoPlatform(platform); err != nil {
		return result, err
	}
	zentao := ZentaoClient{
		BaseURL:       platform.BaseURL,
		Account:       platform.Account,
		AuthMode:      platform.AuthMode,
		SessionHeader: platform.SessionHeader,
		Password:      platform.Password,
		Token:         platform.Token,
		HTTPClient:    client,
	}
	account := strings.TrimSpace(platform.Account)
	if account == "" {
		var err error
		account, err = zentao.CurrentUserAccount()
		if err != nil {
			account = ""
		}
	}
	result.Account = account
	sync, err := zentao.FetchAssignedWithStats(account)
	if err != nil {
		return result, err
	}
	bugs := sync.Bugs
	result.RawFetched = sync.RawFetched
	result.Filtered = sync.Filtered
	result.ProductCount = sync.ProductCount
	bugs = zentao.HydrateBugDetails(bugs)
	result.Fetched = len(bugs)
	keepIDs := make([]string, 0, len(bugs))
	for i := range bugs {
		bugs[i].PlatformID = platform.ID
		applyPlatformEnvConfig(platform, &bugs[i])
		keepIDs = append(keepIDs, bugs[i].ID)
		if err := store.Upsert(bugs[i]); err != nil {
			return result, err
		}
		result.Stored++
		if result.SelectedBugID == "" {
			result.SelectedBugID = bugs[i].ID
		}
	}
	// 清理本地存储中不在本次同步结果里的同平台旧 bug（已修复/关闭/重新指派）
	if prunedIDs, err := store.PruneStaleIDs("zentao", platform.ID, keepIDs); err == nil && len(prunedIDs) > 0 {
		result.PrunedIDs = prunedIDs
		result.Pruned = len(prunedIDs)
	}
	return result, nil
}

func SyncZentaoBug(platform PlatformConfig, store *Store, bugID string, client *http.Client) (SyncResult, error) {
	result := SyncResult{PlatformID: platform.ID}
	if store == nil {
		return result, fmt.Errorf("bug store is required")
	}
	if err := validateZentaoPlatform(platform); err != nil {
		return result, err
	}
	bugID = extractBugID(bugID)
	if bugID == "" {
		return result, fmt.Errorf("bug id is required")
	}
	bug, err := (ZentaoClient{
		BaseURL:       platform.BaseURL,
		Account:       platform.Account,
		AuthMode:      platform.AuthMode,
		SessionHeader: platform.SessionHeader,
		Password:      platform.Password,
		Token:         platform.Token,
		HTTPClient:    client,
	}).FetchByID(bugID)
	if err != nil {
		return result, err
	}
	bug.PlatformID = platform.ID
	applyPlatformEnvConfig(platform, &bug)
	result.Fetched = 1
	if err := store.Upsert(bug); err != nil {
		return result, err
	}
	result.Stored = 1
	result.SelectedBugID = bug.ID
	return result, nil
}

func extractBugID(input string) string {
	matches := bugIDPattern.FindStringSubmatch(strings.TrimSpace(input))
	for _, match := range matches[1:] {
		if match != "" {
			return match
		}
	}
	return ""
}

func validateZentaoPlatform(platform PlatformConfig) error {
	if strings.TrimSpace(platform.Type) != "" && strings.TrimSpace(strings.ToLower(platform.Type)) != "zentao" {
		return fmt.Errorf("platform %s is not zentao", platform.ID)
	}
	if strings.TrimSpace(platform.BaseURL) == "" {
		return fmt.Errorf("zentao base url is required")
	}
	return nil
}

func applyPlatformEnvConfig(platform PlatformConfig, bug *Bug) {
	if bug == nil {
		return
	}
	if env := strings.TrimSpace(platform.Env); env != "" {
		bug.Env = env
	}
	if env := strings.TrimSpace(platform.BotEnv); env != "" {
		bug.BotEnv = env
	}
}
