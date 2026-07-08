package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

type BugInvestigationInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

type BugInvestigationCancelInput struct {
	RunID string `json:"run_id"`
}

func (a *App) StartBugInvestigation(input BugInvestigationInput) (bughub.InvestigationRun, error) {
	bug, ok, err := bugStore().Get(input.BugID)
	if err != nil {
		return bughub.InvestigationRun{}, err
	}
	if !ok {
		return bughub.InvestigationRun{}, os.ErrNotExist
	}
	bug = materializeBugAttachmentsForAgent(bug)
	target := strings.TrimSpace(input.Bot.Target)
	switch target {
	case "codex":
		if _, err := exec.LookPath("codex"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 codex CLI")
		}
	case "claude-code":
		if _, err := exec.LookPath("claude"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 claude CLI")
		}
	case "openclaw":
		if _, err := exec.LookPath("openclaw"); err != nil {
			return bughub.InvestigationRun{}, errors.New("未检测到 openclaw CLI")
		}
	case "cursor":
		return bughub.InvestigationRun{}, errors.New("暂不支持 Cursor 后台直启，请复制上下文后在 Cursor Custom Agent 中发起")
	default:
		return bughub.InvestigationRun{}, errors.New("暂不支持该机器人后台直启")
	}
	ctx := a.getRuntimeContext()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.codexInvestigator().Start(ctx, bug, input.Bot)
}

func materializeBugAttachmentsForAgent(bug bughub.Bug) bughub.Bug {
	if len(bug.Attachments) == 0 {
		return bug
	}
	platform, ok := platformForBugAttachments(bug)
	if !ok {
		return bug
	}
	out := bug
	out.Attachments = append([]bughub.Attachment(nil), bug.Attachments...)
	dir := filepath.Join(bughub.DefaultRoot(), "attachments", safePathSegment(bug.ID))
	for idx, att := range out.Attachments {
		if strings.TrimSpace(att.LocalPath) != "" {
			continue
		}
		data, contentType, err := (bughub.ZentaoClient{
			BaseURL:       platform.BaseURL,
			Account:       platform.Account,
			AuthMode:      platform.AuthMode,
			SessionHeader: platform.SessionHeader,
			Password:      platform.Password,
			Token:         platform.Token,
		}).FetchAttachment(att)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		name := materializedAttachmentName(att, contentType, idx)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			continue
		}
		out.Attachments[idx].LocalPath = path
		if out.Attachments[idx].Type == "" {
			out.Attachments[idx].Type = contentType
		}
	}
	return out
}

func platformForBugAttachments(bug bughub.Bug) (bughub.PlatformConfig, bool) {
	platforms, err := bugPlatformStore().List()
	if err != nil {
		return bughub.PlatformConfig{}, false
	}
	source := strings.TrimSpace(strings.ToLower(bug.Source))
	for _, platform := range platforms {
		if !platform.Enabled {
			continue
		}
		if source != "" && strings.EqualFold(platform.Type, source) {
			return platform, true
		}
	}
	for _, platform := range platforms {
		if platform.Enabled && strings.EqualFold(platform.Type, "zentao") {
			return platform, true
		}
	}
	return bughub.PlatformConfig{}, false
}

func materializedAttachmentName(att bughub.Attachment, contentType string, idx int) string {
	prefix := strings.TrimSpace(att.ID)
	if prefix == "" {
		prefix = "attachment-" + strconv.Itoa(idx+1)
	}
	name := safePathSegment(att.Name)
	if name == "" {
		name = prefix
	}
	if !strings.HasPrefix(name, prefix+"-") && name != prefix {
		name = prefix + "-" + name
	}
	if filepath.Ext(name) == "" {
		switch strings.ToLower(strings.TrimSpace(contentType)) {
		case "image/png":
			name += ".png"
		case "image/jpeg":
			name += ".jpg"
		case "image/gif":
			name += ".gif"
		case "image/webp":
			name += ".webp"
		}
	}
	return name
}

var unsafePathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safePathSegment(input string) string {
	input = strings.TrimSpace(input)
	input = strings.ReplaceAll(input, string(filepath.Separator), "-")
	input = unsafePathChars.ReplaceAllString(input, "-")
	input = strings.Trim(input, ".-")
	if len(input) > 120 {
		input = input[:120]
	}
	return input
}

func (a *App) CancelBugInvestigation(input BugInvestigationCancelInput) error {
	if strings.TrimSpace(input.RunID) == "" {
		return errors.New("run id is required")
	}
	return a.codexInvestigator().Cancel(input.RunID)
}

func (a *App) ListBugInvestigationRuns(bugID string) ([]bughub.InvestigationRun, error) {
	return bugInvestigationStore().ListByBug(bugID)
}

func (a *App) codexInvestigator() *bughub.CodexInvestigator {
	a.bugInvestigationMu.Lock()
	defer a.bugInvestigationMu.Unlock()
	if a.bugInvestigator == nil {
		a.bugInvestigator = bughub.NewCodexInvestigator(bugInvestigationStore(), "codex")
		a.bugInvestigator.SetEventSink(func(run bughub.InvestigationRun, event bughub.InvestigationEvent) {
			if ctx := a.getRuntimeContext(); ctx != nil {
				wailsruntime.EventsEmit(ctx, "bug-investigation:event", map[string]any{
					"run":   run,
					"event": event,
				})
			}
		})
	}
	return a.bugInvestigator
}

func bugInvestigationStore() *bughub.InvestigationStore {
	return bughub.NewInvestigationStore(bughub.DefaultRoot())
}
