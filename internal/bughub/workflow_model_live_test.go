package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Serve only the temporary fixture over loopback: a file:// push would require
// the sandboxed model to write outside its approved standalone fix workspace.
// The service, not the model, owns the bare remote just as a real Git host does.
func serveLiveWorkflowGit(t *testing.T, f gitFixture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	root := filepath.Dir(f.remote)
	daemon := exec.Command("git", "daemon", "--reuseaddr", "--export-all", "--enable=receive-pack", "--base-path="+root, "--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), root)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })
	address := fmt.Sprintf("127.0.0.1:%d", port)
	ready := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("local Git fixture service did not start")
	}
	runGitTest(t, f.repo, "config", "--unset-all", "url.file://"+f.remote+".insteadOf")
	runGitTest(t, f.repo, "config", "url.git://"+address+"/remote.git.insteadOf", "git@example.test:repo.git")
	runGitTest(t, f.repo, "ls-remote", "origin", "refs/heads/test")
}

type liveWorkflowExecutor struct {
	*CodexInvestigator
	t        *testing.T
	mu       sync.Mutex
	sequence int
}

func (e *liveWorkflowExecutor) record(bot BotRef, result PhaseExecutionResult, prompt string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sequence++
	dir := os.Getenv("TSHOOT_LIVE_WORKFLOW_REPORT_DIR")
	if dir == "" || containsSensitiveData([]byte(result.FinalYAML)) || resetURLUserinfoPattern.MatchString(result.FinalYAML) {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		e.t.Error(err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-model-%d.yaml", bot.Target, e.sequence))
	if err := os.WriteFile(path, []byte(result.FinalYAML), 0600); err != nil {
		e.t.Error(err)
	}
	if staging := codexStagingPathFromPrompt(prompt); staging != "" {
		data, err := os.ReadFile(filepath.Join(staging, fixCheckpointManifestName))
		if err == nil && !containsSensitiveData(data) && !resetURLUserinfoPattern.Match(data) {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-checkpoint-%d.json", bot.Target, e.sequence)), data, 0600); err != nil {
				e.t.Error(err)
			}
		}
	}
}

func (e *liveWorkflowExecutor) ExecutePhase(ctx context.Context, id string, bot BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	result, err := e.CodexInvestigator.ExecutePhase(ctx, id, bot, prompt, emit)
	e.record(bot, result, prompt)
	return result, err
}

func (e *liveWorkflowExecutor) ExecutePhaseWithAttachments(ctx context.Context, id string, bot BotRef, prompt string, attachments []PhaseAttachment, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
	result, err := e.CodexInvestigator.ExecutePhaseWithAttachments(ctx, id, bot, prompt, attachments, emit)
	e.record(bot, result, prompt)
	return result, err
}

// This opt-in test connects the actual model executor to the production phase
// runner, orchestrator, SQLite store and Git service. Only the incident and Git
// remote are fixtures; model responses and phase completion are never fabricated.
func TestWorkflowRealModelLive(t *testing.T) {
	targets := strings.TrimSpace(os.Getenv("TSHOOT_LIVE_WORKFLOW_TARGETS"))
	if targets == "" {
		t.Skip("explicit paid-model opt-in: TSHOOT_LIVE_WORKFLOW_TARGETS")
	}
	for _, target := range strings.Split(targets, ",") {
		t.Run(strings.TrimSpace(target), func(t *testing.T) {
			ctx := context.Background()
			f := newGitFixture(t)
			serveLiveWorkflowGit(t, f)
			files := map[string]string{
				"feed.py":      "def latest_items(rows):\n    return sorted(rows, key=lambda row: row['published_at'])\n",
				"test_feed.py": "import unittest\nfrom feed import latest_items\nclass FeedTests(unittest.TestCase):\n    def test_latest_first(self):\n        rows = [{'id': 1, 'published_at': 10}, {'id': 3, 'published_at': 30}, {'id': 2, 'published_at': 20}]\n        self.assertEqual([r['id'] for r in latest_items(rows)], [3, 2, 1])\n    def test_empty(self):\n        self.assertEqual(latest_items([]), [])\nif __name__ == '__main__': unittest.main()\n",
				".gitignore":   "__pycache__/\n",
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(f.repo, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runGitTest(t, f.repo, "add", "feed.py", "test_feed.py", ".gitignore")
			runGitTest(t, f.repo, "commit", "-m", "Add controlled feed-order regression")
			runGitTest(t, f.repo, "push", "origin", "test")
			baseline := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD"))
			broken := exec.Command("python3", "-B", "-m", "unittest", "-v")
			broken.Dir = f.repo
			if err := broken.Run(); err == nil {
				t.Fatal("fixture has no observable regression")
			}
			botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
			namedBotPath := filepath.Join(t.TempDir(), "studio-smoke")
			if err := os.Rename(botPath, namedBotPath); err != nil {
				t.Fatal(err)
			}
			botPath = namedBotPath
			if target == "claude-code" {
				// Claude resolves --agent from the workspace basename and reads
				// the corresponding local .claude/agents profile.
				name := filepath.Base(botPath)
				dir := filepath.Join(botPath, ".claude", "agents")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				profile := "---\nname: " + name + "\ndescription: Authorized isolated Studio workflow test\n---\nUse only the Studio-provided repository and staging paths. Follow the current phase, approval scope and output schema. Do not access business repositories or MCP services.\n"
				if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(profile), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bot := BotRef{Key: "live|" + target, Target: target, AgentID: "studio-smoke", Path: botPath, Env: "test", SystemID: "studio-fixture"}
			// The fixture's repository map is the only project routing available.
			if target == "opencode" {
				root := t.TempDir()
				bot.Path = filepath.Join(root, "skills", "studio-smoke")
				if err := os.MkdirAll(filepath.Join(root, "agents"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(bot.Path, "routing", "references"), 0700); err != nil {
					t.Fatal(err)
				}
				branchMap, err := os.ReadFile(filepath.Join(botPath, "skills", "routing", "references", "env-branch-map.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bot.Path, "routing", "references", "env-branch-map.yaml"), branchMap, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "agents", "studio-smoke.md"), []byte("---\nmode: all\ndescription: Authorized isolated Studio workflow test\npermission:\n  external_directory: allow\n---\nUse only the Studio-provided repository and staging paths. Do not access business repositories or MCP services. Follow the current phase, approval scope and output schema.\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			dbPath := filepath.Join(t.TempDir(), "cases.db")
			store, err := OpenCaseStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			legacy := NewInvestigationStore(t.TempDir())
			executor := &liveWorkflowExecutor{CodexInvestigator: NewCodexInvestigator(legacy, ""), t: t}
			git := &workflowE2EGit{inner: f.service(t)}
			runner := NewAgentPhaseRunner(store, executor, legacy, phaseArtifactsRoot(t), nil)
			runner.SetFixWorkspaceManager(NewFixWorkspaceManager(f.worktrees, func(context.Context, string, string) (string, error) { return f.repo, nil }))
			runner.SetRepositoryAccessResolver(RepositoryAccessResolverFunc(func(context.Context, IncidentCase) (map[string]string, error) {
				return map[string]string{"api": f.repo}, nil
			}))
			var mu sync.Mutex
			toolEvents := 0
			runner.SetEventSink(func(_ InvestigationRun, e InvestigationEvent) {
				if e.Type == "mcp_tool_call" || e.Type == "command_execution" {
					mu.Lock()
					toolEvents++
					mu.Unlock()
				}
			})
			orchestrator := NewCaseOrchestrator(store, runner, git)
			runner.SetCompletionCallback(func(ctx context.Context, cmd CompleteAttemptCommand) error {
				_, err := orchestrator.CompleteAttempt(ctx, cmd)
				return err
			})
			bug := Bug{ID: "live-feed-order", Source: "manual", SystemID: "studio-fixture", Env: "test", Title: "Feed is not sorted newest first", Description: "Authorized isolated regression test. The api repository contains feed.py and test_feed.py. Input published_at values 10,30,20 return IDs 1,2,3; expected 3,2,1. The checked out test branch is the deployed version for this local fixture. Investigate the actual source and test result; use only Studio's repository manifest and staging. Do not use external MCP services, search home directories, access real business systems or change the original checkout. Test with python3 -B -m unittest -v. Only after Studio fix approval may the locked copy be edited and its repair branch pushed to the provided local fixture remote. Do not modify tests to make them pass.", Expected: "Newest published_at first; empty input stays empty"}
			start := CreateAndStartCaseCommand{CaseID: "live-" + target, IdempotencyKey: "live:start", ActorID: "integration-test", Bug: bug, Bot: bot}
			incident, err := orchestrator.CreateAndStartCase(ctx, start)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				latest, _ := store.GetCase(ctx, incident.ID)
				if latest.CurrentAttemptID != "" {
					_ = runner.Cancel(context.Background(), latest.CurrentAttemptID)
				}
			}()
			wait := func(want CaseStatus) IncidentCase {
				t.Helper()
				deadline := time.Now().Add(8 * time.Minute)
				for time.Now().Before(deadline) {
					item, err := store.GetCase(ctx, incident.ID)
					if err != nil {
						t.Fatal(err)
					}
					if item.Status == want {
						return item
					}
					if item.Status != CaseInvestigating && item.Status != CaseFixing {
						attempt, _ := store.GetAttempt(ctx, item.CurrentAttemptID)
						fix, _ := ParseFixResult(attempt.OutputJSON)
						t.Fatalf("unexpected state=%s error=%s blocked=%s result=%s", item.Status, redactLiveProbeError(attempt.ErrorMessage), redactLiveProbeError(fix.BlockedReason), redactLiveProbeError(string(attempt.OutputJSON)))
					}
					time.Sleep(200 * time.Millisecond)
				}
				t.Fatalf("timeout awaiting %s", want)
				return IncidentCase{}
			}
			incident = wait(CaseWaitingFixApproval)
			waitForAgentPhaseRunnerInactive(t, runner, incident.CurrentAttemptID)
			rootAttempt, err := store.GetAttempt(ctx, incident.CurrentAttemptID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseInvestigationResult(rootAttempt.OutputJSON)
			if err != nil || !result.UsesCodeFixWorkflow() || !strings.Contains(strings.ToLower(result.RootCause), "sort") && !strings.Contains(result.RootCause, "排序") {
				t.Fatalf("root cause=%+v err=%v", result, err)
			}
			if strings.TrimSpace(runGitTest(t, f.repo, "status", "--porcelain")) != "" || strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD")) != baseline {
				t.Fatal("investigation modified original checkout")
			}
			t.Log("actual model investigation passed; waiting for explicit fix approval")
			approval := ApproveFixCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: StartFixApprovalKey(incident.ID, rootAttempt.ID, incident.Version), ActorID: "integration-test", RootCauseAttemptID: rootAttempt.ID, Bug: bug, Bot: bot, InputJSON: []byte(`{"source_baselines":{"api":"test"}}`)}
			incident, err = orchestrator.ApproveFix(ctx, approval)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := orchestrator.ApproveFix(ctx, approval); err != nil {
				t.Fatalf("idempotent approval: %v", err)
			}
			incident = wait(CaseWaitingMergeApproval)
			waitForAgentPhaseRunnerInactive(t, runner, incident.CurrentAttemptID)
			changes, err := store.ListCodeChanges(ctx, incident.ID)
			if err != nil || len(changes) != 1 {
				t.Fatalf("changes=%+v err=%v", changes, err)
			}
			change := changes[0]
			if change.FixCommit == baseline || change.PushStatus != "pushed" {
				t.Fatal("no actual repair push")
			}
			inspection, err := git.Inspect(ctx, MergeRequest{CaseID: incident.ID, FixCommits: map[string]string{"api": change.FixCommit}, TargetBranches: map[string]string{"api": "test"}, Changes: changes})
			if err != nil {
				t.Fatal(err)
			}
			merge := ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "live:merge", ActorID: "integration-test", TargetHeads: map[string]string{"api": inspection.Repositories["api"].TargetHead}}
			incident, err = orchestrator.ApproveMerge(ctx, merge)
			if err != nil || incident.Status != CaseSubmitted {
				t.Fatalf("submission=%s err=%v", incident.Status, err)
			}
			if _, err := orchestrator.ApproveMerge(ctx, merge); err != nil {
				t.Fatal(err)
			}
			if git.pushCount() != 1 {
				t.Fatal("duplicate merge push")
			}
			checkout := filepath.Join(t.TempDir(), "acceptance")
			runGitTest(t, filepath.Dir(checkout), "clone", "--branch", "test", f.remote, checkout)
			acceptance := exec.Command("python3", "-B", "-m", "unittest", "-v")
			acceptance.Dir = checkout
			if out, err := acceptance.CombinedOutput(); err != nil {
				t.Fatalf("submitted code fails acceptance: %v: %s", err, out)
			}
			originalTests, _ := os.ReadFile(filepath.Join(f.repo, "test_feed.py"))
			submittedTests, _ := os.ReadFile(filepath.Join(checkout, "test_feed.py"))
			if string(originalTests) != string(submittedTests) {
				t.Fatal("agent modified acceptance tests")
			}
			if strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD")) != baseline || strings.TrimSpace(runGitTest(t, f.repo, "status", "--porcelain")) != "" {
				t.Fatal("original checkout changed")
			}
			attempts, _ := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
			if len(attempts) != 2 {
				t.Fatalf("unexpected attempts=%d", len(attempts))
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenCaseStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := store.GetCase(ctx, incident.ID)
			if err != nil || reopened.Status != CaseSubmitted {
				t.Fatal("submission not durable")
			}
			approvals, _ := store.ListApprovals(ctx, incident.ID)
			if len(approvals) != 2 {
				t.Fatalf("approvals=%d", len(approvals))
			}
			mu.Lock()
			count := toolEvents
			mu.Unlock()
			if count == 0 {
				t.Fatal("no real tool execution")
			}
			report := map[string]any{"target": target, "status": reopened.Status, "attempts": len(attempts), "approvals": len(approvals), "tool_events": count, "fix_commit": change.FixCommit, "acceptance": "passed", "original_checkout": "unchanged", "sqlite_reopen": "passed", "root_cause": result.RootCause}
			encoded, _ := json.MarshalIndent(report, "", "  ")
			t.Logf("real model full workflow PASS: %s", encoded)
			if dir := os.Getenv("TSHOOT_LIVE_WORKFLOW_REPORT_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.json", target)), encoded, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
