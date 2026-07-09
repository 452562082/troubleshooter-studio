package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func TestStartBugInvestigationRunsCodexBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "base-troubleshooter")
	validatorRepo := filepath.Join(root, "base-validator")
	for _, dir := range []string{repo, validatorRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{
			Key:      repo + "|codex",
			Target:   "codex",
			Path:     repo,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if run.Status != bughub.InvestigationRunning {
		t.Fatalf("run = %+v", run)
	}
	waited, err := app.codexInvestigator().Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != bughub.InvestigationSucceeded || waited.FinalMessage != "done" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestStartBugInvestigationChecksOutConfiguredEnvBranch(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "app")
	initGitRepoWithBranch(t, repo, "release/test")
	agentDir := filepath.Join(root, "base-troubleshooter")
	validatorDir := filepath.Join(root, "base-validator")
	for _, dir := range []string{agentDir, validatorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBotMeta(t, agentDir)
	if err := userconfig.SetRepoPathsForSystem("base", map[string]string{"app": repo}); err != nil {
		t.Fatalf("SetRepoPathsForSystem: %v", err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577", BotEnv: "test"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{
			Key:      agentDir + "|codex",
			Target:   "codex",
			Path:     agentDir,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if run.Status != bughub.InvestigationRunning {
		t.Fatalf("run = %+v", run)
	}
	if got := gitBranch(t, repo); got != "release/test" {
		t.Fatalf("branch = %q", got)
	}
	if _, err := app.codexInvestigator().Wait(run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestStartBugInvestigationRejectsMissingEnvBranch(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "app")
	initGitRepoWithBranch(t, repo, "release/test")
	agentDir := filepath.Join(root, "base-troubleshooter")
	validatorDir := filepath.Join(root, "base-validator")
	for _, dir := range []string{agentDir, validatorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeBotMetaWithBranch(t, dir, "missing/test")
	}
	if err := userconfig.SetRepoPathsForSystem("base", map[string]string{"app": repo}); err != nil {
		t.Fatalf("SetRepoPathsForSystem: %v", err)
	}
	bin := filepath.Join(root, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577", BotEnv: "test"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{
			Key:      agentDir + "|codex",
			Target:   "codex",
			Path:     agentDir,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "分支切换失败") {
		t.Fatalf("err = %v", err)
	}
	if got := gitBranch(t, repo); got != "main" {
		t.Fatalf("branch changed after failed start: %q", got)
	}
}

func TestStartBugInvestigationMaterializesZentaoAttachmentsForAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	repo := filepath.Join(root, "base-troubleshooter")
	validatorRepo := filepath.Join(root, "base-validator")
	for _, dir := range []string{repo, validatorRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\nprintf '%s\\n' \"$last\" >> \"$CALLS_PATH\"\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CALLS_PATH", callsPath)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Token") != "secret" {
			t.Fatalf("Token header = %q", r.Header.Get("Token"))
		}
		if r.URL.Path != "/api.php/v1/files/101/download" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nagent-readable"))
	}))
	defer srv.Close()
	if _, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
		ID:      "zentao-main",
		Name:    "禅道",
		Type:    "zentao",
		BaseURL: srv.URL,
		Token:   "secret",
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert platform: %v", err)
	}
	if err := bugStore().Upsert(bughub.Bug{
		ID:     "zentao-718",
		Source: "zentao",
		Title:  "Bug 718",
		Attachments: []bughub.Attachment{{
			ID:        "101",
			Name:      "screen.png",
			Type:      "image/png",
			RemoteURL: "/data/upload/1/202607/screen",
		}},
	}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-718",
		Bot: bughub.BotRef{
			Key:      repo + "|codex",
			Target:   "codex",
			Path:     repo,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	if _, err := app.codexInvestigator().Wait(run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	prompts, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	if !strings.Contains(string(prompts), "id=101") {
		t.Fatalf("prompt missing attachment id:\n%s", prompts)
	}
	localMarker := filepath.Join(".tshoot", "bugs", "attachments", "zentao-718", "101-screen.png")
	if !strings.Contains(string(prompts), localMarker) {
		t.Fatalf("prompt missing materialized attachment path %q:\n%s", localMarker, prompts)
	}
	materialized := filepath.Join(root, localMarker)
	data, err := os.ReadFile(materialized)
	if err != nil {
		t.Fatalf("materialized attachment missing: %v", err)
	}
	if !strings.Contains(string(data), "agent-readable") {
		t.Fatalf("materialized data = %q", data)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initGitRepoWithBranch(t *testing.T, repo string, branch string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	runGit(t, repo, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "commit", "-am", "branch")
	runGit(t, repo, "checkout", "main")
}

func gitBranch(t *testing.T, repo string) string {
	t.Helper()
	return runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
}

func writeBotMeta(t *testing.T, dir string) {
	t.Helper()
	writeBotMetaWithBranch(t, dir, "release/test")
}

func writeBotMetaWithBranch(t *testing.T, dir string, branch string) {
	t.Helper()
	yamlText := `system:
  id: base
  name: Base
agent:
  name: Base
  model: openai-codex/gpt-5.3-codex
environments:
  - id: test
repos:
  - name: app
    url: https://example.com/app.git
    stack: go
    env_branches:
      test: ` + branch + `
generation:
  targets: [codex]
meta:
  schema_version: "0.1"
`
	data, err := json.Marshal(discover.Meta{
		SchemaVersion:      1,
		SystemID:           "base",
		SystemName:         "Base",
		Target:             "codex",
		TroubleshooterYAML: yamlText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, discover.MetaFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartBugInvestigationRunsClaudeBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	agentPath := filepath.Join(root, "skills", "base-troubleshooter")
	validatorPath := filepath.Join(root, "skills", "base-validator")
	for _, dir := range []string{agentPath, validatorPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "claude")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"verification_status: reproduced\"}' ;;\n  *) printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"claude done\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	app := &App{}
	run, err := app.StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot: bughub.BotRef{
			Key:      agentPath + "|claude-code",
			Target:   "claude-code",
			Path:     agentPath,
			SystemID: "base",
			InternalAgents: []bughub.BotInternalAgent{
				{ID: "base-troubleshooter", Role: "troubleshooter"},
				{ID: "base-validator", Role: "validator"},
			},
		},
	})
	if err != nil {
		t.Fatalf("StartBugInvestigation: %v", err)
	}
	waited, err := app.codexInvestigator().Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != bughub.InvestigationSucceeded || waited.FinalMessage != "claude done" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestStartBugInvestigationRejectsCursorBot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot:   bughub.BotRef{Key: "repo|cursor", Target: "cursor", Path: root, SystemID: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "暂不支持 Cursor") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartBugInvestigationRejectsMissingCodexCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", t.TempDir())
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bugStore().Upsert(bughub.Bug{ID: "zentao-577", Source: "zentao", Title: "Bug 577"}); err != nil {
		t.Fatalf("Upsert bug: %v", err)
	}

	_, err := (&App{}).StartBugInvestigation(BugInvestigationInput{
		BugID: "zentao-577",
		Bot:   bughub.BotRef{Key: repo + "|codex", Target: "codex", Path: repo, SystemID: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "未检测到 codex CLI") {
		t.Fatalf("err = %v", err)
	}
}

func TestListBugInvestigationRunsReturnsStoredRunsForBug(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	store := bugInvestigationStore()
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-old",
		BugID:     "zentao-577",
		Status:    bughub.InvestigationSucceeded,
		StartedAt: time.Now().Add(-time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("Upsert old run: %v", err)
	}
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-new",
		BugID:     "zentao-577",
		Status:    bughub.InvestigationRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert new run: %v", err)
	}
	if err := store.Upsert(bughub.InvestigationRun{
		ID:        "run-other",
		BugID:     "zentao-999",
		Status:    bughub.InvestigationRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert other run: %v", err)
	}

	runs, err := (&App{}).ListBugInvestigationRuns("zentao-577")
	if err != nil {
		t.Fatalf("ListBugInvestigationRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run-new" || runs[1].ID != "run-old" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestCancelBugInvestigationRejectsBlankRunID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	err := (&App{}).CancelBugInvestigation(BugInvestigationCancelInput{})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blank run should be validation error, got %v", err)
	}
}
