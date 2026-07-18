package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

type BugInvestigationInput struct {
	BugID string        `json:"bug_id"`
	Bot   bughub.BotRef `json:"bot"`
}

type BugInvestigationCancelInput struct {
	RunID string `json:"run_id"`
}

// checkoutEnvBranches 在启动排障 Agent 前，根据 bot 的环境 → 仓库分支映射，
// 把 workspace 涉及的各仓库切到对应分支上。bot.Env 为空时跳过。
func checkoutEnvBranches(bot bughub.BotRef, bug bughub.Bug) error {
	env := strings.TrimSpace(bot.Env)
	if env == "" {
		env = strings.TrimSpace(bug.BotEnv)
	}
	if env == "" {
		return nil
	}

	botDir := strings.TrimSpace(bot.Path)
	if info, err := os.Stat(botDir); err == nil && !info.IsDir() {
		botDir = filepath.Dir(botDir)
	}
	metaPath := filepath.Join(botDir, discover.MetaFilename)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("读取机器人元数据失败，无法确认环境 %s 的代码分支: %w", env, err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("解析机器人元数据失败，无法确认环境 %s 的代码分支: %w", env, err)
	}
	cfg, err := config.LoadFromBytes([]byte(meta.TroubleshooterYAML))
	if err != nil {
		return fmt.Errorf("读取机器人 yaml 失败，无法确认环境 %s 的代码分支: %w", env, err)
	}

	systemID := strings.TrimSpace(bot.SystemID)
	if systemID == "" {
		systemID = strings.TrimSpace(meta.SystemID)
	}
	if systemID == "" {
		systemID = strings.TrimSpace(cfg.System.ID)
	}
	repoPaths := userconfig.GetRepoPathsForSystem(systemID)
	if repoPaths == nil {
		repoPaths = map[string]string{}
	}

	for _, repo := range cfg.Repos {
		branch := strings.TrimSpace(repo.EnvBranches[env])
		if branch == "" {
			continue
		}
		repoPath := strings.TrimSpace(repoPaths[repo.Name])
		if repoPath == "" {
			return fmt.Errorf("环境 %s 需要仓库 %s 切到分支 %s，但未配置本地仓库路径", env, repo.Name, branch)
		}
		if repo.SubPath != "" {
			repoPath = filepath.Join(repoPath, repo.SubPath)
		}
		if err := ensureRepoBranch(repoPath, branch); err != nil {
			return fmt.Errorf("环境 %s 仓库 %s 分支切换失败: %w", env, repo.Name, err)
		}
	}
	return nil
}

func checkoutBugAgentEnvBranches(bot bughub.BotRef, bug bughub.Bug) error {
	return checkoutEnvBranches(bot, bug)
}

func ensureRepoBranch(repoPath string, branch string) error {
	repoPath = strings.TrimSpace(repoPath)
	branch = strings.TrimSpace(branch)
	if repoPath == "" || branch == "" {
		return nil
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		return fmt.Errorf("检查仓库路径 %s: %w", repoPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("仓库路径 %s 不是目录", repoPath)
	}
	current, err := currentGitBranch(repoPath)
	if err != nil {
		return err
	}
	if current == branch {
		return nil
	}
	if out, err := exec.Command("git", "-C", repoPath, "checkout", branch).CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	current, err = currentGitBranch(repoPath)
	if err != nil {
		return err
	}
	if current != branch {
		return fmt.Errorf("checkout 后当前分支为 %s，期望 %s", current, branch)
	}
	return nil
}

func currentGitBranch(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("读取当前 git 分支失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("当前仓库处于 detached HEAD，无法确认环境分支")
	}
	return branch, nil
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
	if err := checkoutBugAgentEnvBranches(input.Bot, bug); err != nil {
		return bughub.InvestigationRun{}, err
	}
	if strings.TrimSpace(input.Bot.Key) != "" {
		bug.SelectedBotKey = input.Bot.Key
		_ = bugStore().Upsert(bug)
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
	if platformID := strings.TrimSpace(bug.PlatformID); platformID != "" {
		for _, platform := range platforms {
			if platform.Enabled && platform.ID == platformID {
				return platform, true
			}
		}
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

type BugInvestigationContinueInput struct {
	BugID         string        `json:"bug_id"`
	Bot           bughub.BotRef `json:"bot"`
	UserInput     string        `json:"user_input"`
	PreviousRunID string        `json:"previous_run_id,omitempty"`
	Phase         string        `json:"phase,omitempty"`
}

type BugFixInput struct {
	BugID         string        `json:"bug_id"`
	Bot           bughub.BotRef `json:"bot"`
	PreviousRunID string        `json:"previous_run_id,omitempty"`
}

func (a *App) ContinueBugInvestigation(input BugInvestigationContinueInput) (bughub.InvestigationRun, error) {
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
	if strings.TrimSpace(input.UserInput) == "" {
		return bughub.InvestigationRun{}, errors.New("补充信息不能为空")
	}
	if err := checkoutBugAgentEnvBranches(input.Bot, bug); err != nil {
		return bughub.InvestigationRun{}, err
	}
	if strings.TrimSpace(input.Bot.Key) != "" {
		bug.SelectedBotKey = input.Bot.Key
		_ = bugStore().Upsert(bug)
	}
	ctx := a.getRuntimeContext()
	if ctx == nil {
		ctx = context.Background()
	}
	previousRunID := strings.TrimSpace(input.PreviousRunID)
	if previousRunID == "" {
		runs, err := bugInvestigationStore().ListByBug(bug.ID)
		if err == nil && len(runs) > 0 {
			previousRunID = runs[0].ID
		}
	}
	return a.codexInvestigator().Continue(ctx, bug, input.Bot, input.UserInput, previousRunID, input.Phase)
}

func (a *App) StartBugFix(input BugFixInput) (bughub.InvestigationRun, error) {
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
	if err := checkoutBugAgentEnvBranches(input.Bot, bug); err != nil {
		return bughub.InvestigationRun{}, err
	}
	if strings.TrimSpace(input.Bot.Key) != "" {
		bug.SelectedBotKey = input.Bot.Key
		_ = bugStore().Upsert(bug)
	}
	previousRunID := strings.TrimSpace(input.PreviousRunID)
	if previousRunID == "" {
		runs, err := bugInvestigationStore().ListByBug(bug.ID)
		if err == nil && len(runs) > 0 {
			previousRunID = runs[0].ID
		}
	}
	if previousRunID == "" {
		return bughub.InvestigationRun{}, errors.New("缺少排障结论，无法启动修复 Agent")
	}
	ctx := a.getRuntimeContext()
	if ctx == nil {
		ctx = context.Background()
	}
	return a.codexInvestigator().StartFix(ctx, bug, input.Bot, previousRunID)
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
	// Prefer the durable workflow's shared executor. initializeIncidentWorkflow
	// is idempotent and also ensures migration/recovery has run when a legacy
	// binding is invoked before Wails startup completes.
	_ = a.startIncidentWorkflow(a.workflowCommandContext())
	a.bugInvestigationMu.Lock()
	defer a.bugInvestigationMu.Unlock()
	if a.bugInvestigator == nil {
		a.bugInvestigator = bughub.NewCodexInvestigator(bugInvestigationStore(), "codex")
	}
	// The durable workflow reuses this executor, so configure the legacy event
	// projection even when the investigator was created during App startup.
	a.bugInvestigator.SetEventSink(func(run bughub.InvestigationRun, event bughub.InvestigationEvent) {
		if ctx := a.getRuntimeContext(); ctx != nil {
			wailsruntime.EventsEmit(ctx, "bug-investigation:event", map[string]any{
				"run":   run,
				"event": event,
			})
		}
	})
	return a.bugInvestigator
}

func bugInvestigationStore() *bughub.InvestigationStore {
	return bughub.NewInvestigationStore(bughub.DefaultRoot())
}
