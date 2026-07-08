package main

import (
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestShouldPollBugPlatformRequiresExplicitPollEnabled(t *testing.T) {
	platform := bughub.PlatformConfig{Enabled: true, Type: "zentao", PollIntervalMinutes: 5}

	if shouldPollBugPlatform(platform, time.Now(), time.Time{}) {
		t.Fatal("poll should be disabled unless poll_enabled is true")
	}
}

func TestShouldPollBugPlatformUsesConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 7, 3, 18, 30, 0, 0, time.UTC)
	platform := bughub.PlatformConfig{
		Enabled: true, Type: "zentao", PollEnabled: true, PollIntervalMinutes: 10,
	}

	if shouldPollBugPlatform(platform, now, now.Add(-9*time.Minute)) {
		t.Fatal("poll ran before configured interval elapsed")
	}
	if !shouldPollBugPlatform(platform, now, now.Add(-10*time.Minute)) {
		t.Fatal("poll did not run after configured interval elapsed")
	}
}
