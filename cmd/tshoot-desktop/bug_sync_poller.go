package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

var bugPollInterval = time.Minute
var bugLastPollAt = map[string]time.Time{}
var bugLastKeepAliveAt = map[string]time.Time{}
var bugKeepAliveInterval = 5 * time.Minute

func startBugPoller(ctx context.Context, appState *App) {
	if ctx == nil || appState == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(bugPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncEnabledBugPlatforms()
			}
		}
	}()
}

func syncEnabledBugPlatforms() {
	platforms, err := bugPlatformStore().List()
	if err != nil {
		fmt.Printf("[warn] bug platform poll list failed: %v\n", err)
		return
	}
	store := bugStore()
	now := time.Now().UTC()
	for _, platform := range platforms {
		if shouldKeepAliveBugPlatform(platform, now, bugLastKeepAliveAt[platform.ID]) {
			bugLastKeepAliveAt[platform.ID] = now
			if err := keepAliveBugPlatform(platform); err != nil {
				fmt.Printf("[warn] bug platform keepalive %s failed: %v\n", platform.ID, err)
			}
		}
		if !shouldPollBugPlatform(platform, now, bugLastPollAt[platform.ID]) {
			continue
		}
		bugLastPollAt[platform.ID] = now
		result, err := runZentaoSyncWithSessionRecovery(platform, func(platform bughub.PlatformConfig) (bughub.SyncResult, error) {
			return bughub.SyncZentaoAssigned(platform, store, nil)
		})
		if err != nil {
			fmt.Printf("[warn] bug platform poll %s failed: %v\n", platform.ID, err)
			continue
		}
		cleanupPrunedBugAttachmentCaches(result)
		if result.Stored > 0 {
			fmt.Printf("[info] bug platform poll %s stored %d/%d bugs\n", platform.ID, result.Stored, result.Fetched)
		}
	}
}

func shouldPollBugPlatform(platform bughub.PlatformConfig, now time.Time, last time.Time) bool {
	if !platform.Enabled || !platform.PollEnabled || strings.ToLower(strings.TrimSpace(platform.Type)) != "zentao" {
		return false
	}
	interval := time.Duration(platform.PollIntervalMinutes) * time.Minute
	if interval <= 0 {
		return false
	}
	return last.IsZero() || !now.Before(last.Add(interval))
}

func shouldKeepAliveBugPlatform(platform bughub.PlatformConfig, now time.Time, last time.Time) bool {
	if !platform.Enabled || strings.ToLower(strings.TrimSpace(platform.Type)) != "zentao" {
		return false
	}
	if !bugPlatformUsesCapturedSession(platform) {
		return false
	}
	return last.IsZero() || !now.Before(last.Add(bugKeepAliveInterval))
}

func keepAliveBugPlatform(platform bughub.PlatformConfig) error {
	client := bughub.ZentaoClient{
		BaseURL:       platform.BaseURL,
		Account:       platform.Account,
		AuthMode:      platform.AuthMode,
		SessionHeader: platform.SessionHeader,
		Password:      platform.Password,
		Token:         platform.Token,
	}
	_, err := client.CurrentUserAccount()
	if err == nil {
		return nil
	}
	if shouldRecoverZentaoSession(platform, err) {
		if _, ok := refreshZentaoSession(platform); ok {
			return nil
		}
		if clearExpiredZentaoSession(platform, err) {
			return zentaoSessionExpiredError(err)
		}
	}
	return err
}
