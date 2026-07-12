package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type gitFixture struct {
	remote, repo, worktrees string
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGitTest(t, root, "init", "--bare", remote)
	repo := filepath.Join(root, "repo")
	runGitTest(t, root, "clone", remote, repo)
	runGitTest(t, repo, "config", "user.name", "Studio Test")
	runGitTest(t, repo, "config", "user.email", "studio@example.test")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "app.txt")
	runGitTest(t, repo, "commit", "-m", "base")
	runGitTest(t, repo, "branch", "-M", "test")
	runGitTest(t, repo, "push", "-u", "origin", "test")
	// Keep the integration test offline while presenting an SSH remote to the
	// service: Git rewrites the test-only SSH prefix back to the local bare repo.
	runGitTest(t, repo, "remote", "set-url", "origin", "git@example.test:repo.git")
	runGitTest(t, repo, "config", "url.file://"+remote+".insteadOf", "git@example.test:repo.git")
	return gitFixture{remote: remote, repo: repo, worktrees: filepath.Join(root, "worktrees")}
}

func (f gitFixture) makeFix(t *testing.T, content string) string {
	t.Helper()
	runGitTest(t, f.repo, "switch", "-c", "fix/bug")
	if err := os.WriteFile(filepath.Join(f.repo, "fix.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "add", "fix.txt")
	runGitTest(t, f.repo, "commit", "-m", "fix")
	commit := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD"))
	runGitTest(t, f.repo, "push", "-u", "origin", "fix/bug")
	return commit
}

func (f gitFixture) service(t *testing.T) *GitIntegrationService {
	t.Helper()
	return NewGitIntegrationService(f.worktrees, func(_ context.Context, _, repo string) (string, error) {
		if repo != "api" {
			return "", errors.New("unknown repo")
		}
		return f.repo, nil
	})
}

func (f gitFixture) request(commit string) MergeRequest {
	change := CodeChange{Repo: "api", FixBranch: "fix/bug", FixCommit: commit, TargetEnvironmentBranch: "test", PushRemote: "origin"}
	return MergeRequest{CaseID: "case-1", FixCommits: map[string]string{"api": commit}, TargetBranches: map[string]string{"api": "test"}, Changes: []CodeChange{change}}
}

func TestGitIntegrationFastForwardAndIdempotentReplay(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil || inspection.Conflict || inspection.Repositories["api"].TargetHead == "" {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	result, err := service.MergeAndPush(context.Background(), req)
	if err != nil || !result.Repositories["api"].Pushed || result.Repositories["api"].MergeCommit != commit {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runGitTest(t, f.repo, "push", "origin", "--delete", "fix/bug")
	replayed, err := service.MergeAndPush(context.Background(), req)
	if err != nil || !replayed.Repositories["api"].Pushed || replayed.Repositories["api"].MergeCommit != commit {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestGitIntegrationCreatesMergeCommit(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	runGitTest(t, f.repo, "switch", "test")
	if err := os.WriteFile(filepath.Join(f.repo, "target.txt"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "add", "target.txt")
	runGitTest(t, f.repo, "commit", "-m", "target")
	runGitTest(t, f.repo, "push", "origin", "test")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	result, err := service.MergeAndPush(context.Background(), req)
	if err != nil || !result.Repositories["api"].Pushed || result.Repositories["api"].MergeCommit == commit {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitIntegrationRejectsTargetHeadChangedAfterApproval(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	runGitTest(t, f.repo, "switch", "test")
	if err := os.WriteFile(filepath.Join(f.repo, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "add", "later.txt")
	runGitTest(t, f.repo, "commit", "-m", "later")
	runGitTest(t, f.repo, "push", "origin", "test")
	result, err := service.MergeAndPush(context.Background(), req)
	if !errors.Is(err, ErrMergeApprovalStale) || result.Repositories["api"].TargetHead == req.TargetHeads["api"] {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitIntegrationRejectsDirtyDetachedAndConflict(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		f := newGitFixture(t)
		commit := f.makeFix(t, "fix\n")
		if err := os.WriteFile(filepath.Join(f.repo, "dirty"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := f.service(t).Inspect(context.Background(), f.request(commit))
		if !errors.Is(err, ErrGitWorktreeDirty) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("detached", func(t *testing.T) {
		f := newGitFixture(t)
		commit := f.makeFix(t, "fix\n")
		runGitTest(t, f.repo, "checkout", "--detach")
		_, err := f.service(t).Inspect(context.Background(), f.request(commit))
		if !errors.Is(err, ErrGitDetachedHEAD) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		f := newGitFixture(t)
		runGitTest(t, f.repo, "switch", "-c", "fix/bug")
		if err := os.WriteFile(filepath.Join(f.repo, "app.txt"), []byte("fix\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, f.repo, "commit", "-am", "fix")
		commit := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD"))
		runGitTest(t, f.repo, "push", "-u", "origin", "fix/bug")
		runGitTest(t, f.repo, "switch", "test")
		if err := os.WriteFile(filepath.Join(f.repo, "app.txt"), []byte("target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, f.repo, "commit", "-am", "target")
		runGitTest(t, f.repo, "push", "origin", "test")
		inspection, err := f.service(t).Inspect(context.Background(), f.request(commit))
		if err != nil || !inspection.Conflict {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
}

func TestGitIntegrationRequiresSSHRemote(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	runGitTest(t, f.repo, "remote", "set-url", "origin", "https://example.invalid/repo.git")
	_, err := f.service(t).Inspect(context.Background(), f.request(commit))
	if !errors.Is(err, ErrGitRemoteNotSSH) {
		t.Fatalf("err=%v", err)
	}
}

func TestGitIntegrationSSHPushFailurePreservesLocalMerge(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	rejectAllPushes(t, f.remote)
	result, err := service.MergeAndPush(context.Background(), req)
	if err == nil || result.Repositories["api"].MergeCommit == "" || result.Repositories["api"].Pushed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitIntegrationResumePushRequiresFreshTargetApproval(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	rejectAllPushes(t, f.remote)
	failed, err := service.MergeAndPush(context.Background(), req)
	if err == nil || failed.Repositories["api"].MergeCommit == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if err := os.Remove(filepath.Join(f.remote, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "switch", "test")
	if err := os.WriteFile(filepath.Join(f.repo, "advanced.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "add", "advanced.txt")
	runGitTest(t, f.repo, "commit", "-m", "advance target")
	runGitTest(t, f.repo, "push", "origin", "test")
	req.Changes[0].MergeCommit = failed.Repositories["api"].MergeCommit
	resumed, err := service.ResumePush(context.Background(), req)
	if !errors.Is(err, ErrMergeApprovalStale) || resumed.Repositories["api"].TargetHead == req.TargetHeads["api"] {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
}

func TestGitIntegrationInspectRecoversUnrecordedLocalMergeAfterCrash(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	rejectAllPushes(t, f.remote)
	failed, err := service.MergeAndPush(context.Background(), req)
	if err == nil || failed.Repositories["api"].MergeCommit == "" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	restarted := f.service(t)
	recovered, err := restarted.Inspect(context.Background(), req)
	if err != nil || recovered.Repositories["api"].MergeCommit != failed.Repositories["api"].MergeCommit || recovered.Repositories["api"].Pushed {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if err := os.Remove(filepath.Join(f.remote, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	req.Changes[0].MergeCommit = recovered.Repositories["api"].MergeCommit
	resumed, err := restarted.ResumePush(context.Background(), req)
	if err != nil || !resumed.Repositories["api"].Pushed {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	worktree := restarted.worktreePath(req.CaseID, "api", req.TargetHeads["api"], commit)
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resumed worktree retained: %v", err)
	}
}

func TestGitIntegrationDedicatedWorktreePermissionTamperAndCleanup(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	rejectAllPushes(t, f.remote)
	_, err = service.MergeAndPush(context.Background(), req)
	if err == nil {
		t.Fatal("push unexpectedly succeeded")
	}
	worktree := service.worktreePath(req.CaseID, "api", req.TargetHeads["api"], commit)
	info, statErr := os.Stat(worktree)
	if statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	if err := os.WriteFile(filepath.Join(worktree, "tampered.txt"), []byte("tamper"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Inspect(context.Background(), req); err == nil {
		t.Fatal("dirty dedicated worktree accepted")
	}
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered worktree retained: %v", err)
	}
}

func TestGitIntegrationSuccessfulPushCleansDedicatedWorktree(t *testing.T) {
	f := newGitFixture(t)
	commit := f.makeFix(t, "fix\n")
	service := f.service(t)
	req := f.request(commit)
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
	result, err := service.MergeAndPush(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	worktree := service.worktreePath(req.CaseID, "api", req.TargetHeads["api"], commit)
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful worktree retained: result=%+v err=%v", result, err)
	}
}

func TestGitIntegrationRejectsTamperedHeadAndCleansStaleScope(t *testing.T) {
	t.Run("attached branch", func(t *testing.T) {
		f := newGitFixture(t)
		commit := f.makeFix(t, "fix\n")
		service := f.service(t)
		req := f.request(commit)
		inspection, err := service.Inspect(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
		rejectAllPushes(t, f.remote)
		if _, err := service.MergeAndPush(context.Background(), req); err == nil {
			t.Fatal("push unexpectedly succeeded")
		}
		worktree := service.worktreePath(req.CaseID, "api", req.TargetHeads["api"], commit)
		runGitTest(t, worktree, "switch", "-c", "hijack")
		if _, err := service.Inspect(context.Background(), req); !errors.Is(err, ErrStudioWorktree) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("tampered HEAD", func(t *testing.T) {
		f := newGitFixture(t)
		commit := f.makeFix(t, "fix\n")
		service := f.service(t)
		req := f.request(commit)
		inspection, err := service.Inspect(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		req.TargetHeads = map[string]string{"api": inspection.Repositories["api"].TargetHead}
		rejectAllPushes(t, f.remote)
		if _, err := service.MergeAndPush(context.Background(), req); err == nil {
			t.Fatal("push unexpectedly succeeded")
		}
		worktree := service.worktreePath(req.CaseID, "api", req.TargetHeads["api"], commit)
		runGitTest(t, worktree, "commit", "--allow-empty", "-m", "tamper")
		if _, err := service.Inspect(context.Background(), req); !errors.Is(err, ErrStudioWorktree) {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tampered worktree retained: %v", err)
		}
	})
	t.Run("stale target", func(t *testing.T) {
		f := newGitFixture(t)
		commit := f.makeFix(t, "fix\n")
		service := f.service(t)
		req := f.request(commit)
		inspection, err := service.Inspect(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		old := inspection.Repositories["api"].TargetHead
		req.TargetHeads = map[string]string{"api": old}
		rejectAllPushes(t, f.remote)
		if _, err := service.MergeAndPush(context.Background(), req); err == nil {
			t.Fatal("push unexpectedly succeeded")
		}
		worktree := service.worktreePath(req.CaseID, "api", old, commit)
		if err := os.Remove(filepath.Join(f.remote, "hooks", "pre-receive")); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, f.repo, "switch", "test")
		if err := os.WriteFile(filepath.Join(f.repo, "advance"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, f.repo, "add", "advance")
		runGitTest(t, f.repo, "commit", "-m", "advance")
		runGitTest(t, f.repo, "push", "origin", "test")
		fresh, err := service.Inspect(context.Background(), req)
		if err != nil || fresh.Repositories["api"].TargetHead == old {
			t.Fatalf("fresh=%+v err=%v", fresh, err)
		}
		if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale worktree retained: %v", err)
		}
	})
}

func TestGitIntegrationOrchestratorRejectsMissingInspectionScope(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-missing-scope", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, PhaseAttempt{ID: "fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCodeChange(ctx, CodeChange{ID: "change", CaseID: incident.ID, AttemptID: "fix", Repo: "api", BaseBranch: "test", FixBranch: "fix", FixCommit: "abc", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}); err != nil {
		t.Fatal(err)
	}
	git := &recordingGitIntegration{inspection: MergeInspection{Repositories: map[string]MergeRepositoryResult{"api": {}}}}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	if _, err := orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "missing", ActorID: "alice"}); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("err=%v", err)
	}
	if approvals, _ := store.ListApprovals(ctx, incident.ID); len(approvals) != 0 {
		t.Fatalf("approvals=%+v", approvals)
	}
}

func TestGitIntegrationTwoRepoConflictPersistsEveryRepositoryResult(t *testing.T) {
	ctx := context.Background()
	a := newGitFixture(t)
	ac := a.makeFix(t, "a\n")
	b := newGitFixture(t)
	runGitTest(t, b.repo, "switch", "-c", "fix/bug")
	if err := os.WriteFile(filepath.Join(b.repo, "app.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, b.repo, "commit", "-am", "fix")
	bc := strings.TrimSpace(runGitTest(t, b.repo, "rev-parse", "HEAD"))
	runGitTest(t, b.repo, "push", "-u", "origin", "fix/bug")
	runGitTest(t, b.repo, "switch", "test")
	if err := os.WriteFile(filepath.Join(b.repo, "app.txt"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, b.repo, "commit", "-am", "target")
	runGitTest(t, b.repo, "push", "origin", "test")
	service := NewGitIntegrationService(filepath.Join(t.TempDir(), "worktrees"), func(_ context.Context, _, repo string) (string, error) {
		if repo == "a" {
			return a.repo, nil
		}
		return b.repo, nil
	})
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-two-conflict", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAttempt(ctx, PhaseAttempt{ID: "fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	changes := []CodeChange{{ID: "ca", CaseID: incident.ID, AttemptID: "fix", Repo: "a", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: ac, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}, {ID: "cb", CaseID: incident.ID, AttemptID: "fix", Repo: "b", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: bc, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}}
	for _, change := range changes {
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	req := MergeRequest{CaseID: incident.ID, FixCommits: map[string]string{"a": ac, "b": bc}, TargetBranches: map[string]string{"a": "test", "b": "test"}, Changes: changes}
	inspection, err := service.Inspect(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	heads := map[string]string{"a": inspection.Repositories["a"].TargetHead, "b": inspection.Repositories["b"].TargetHead}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, service, &recordingDeploymentVerifier{})
	got, err := orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "conflict-two", ActorID: "alice", TargetHeads: heads})
	if err == nil || got.Status != CaseMergeConflict {
		t.Fatalf("case=%+v err=%v", got, err)
	}
	persisted, _ := store.ListCodeChanges(ctx, incident.ID)
	statuses := map[string]string{}
	for _, change := range persisted {
		statuses[change.Repo] = change.PushStatus
	}
	if statuses["a"] != "pushed" || statuses["b"] != "conflict" {
		t.Fatalf("statuses=%v", statuses)
	}
	events, _ := store.ListEvents(ctx, incident.ID)
	last := events[len(events)-1]
	if !strings.Contains(string(last.PayloadJSON), `"a"`) || !strings.Contains(string(last.PayloadJSON), `"b"`) {
		t.Fatalf("payload=%s", last.PayloadJSON)
	}
}

func TestGitIntegrationTwoReposStopsAtSecondFailure(t *testing.T) {
	a := newGitFixture(t)
	ac := a.makeFix(t, "a\n")
	b := newGitFixture(t)
	bc := b.makeFix(t, "b\n")
	service := NewGitIntegrationService(filepath.Join(t.TempDir(), "worktrees"), func(_ context.Context, _, repo string) (string, error) {
		if repo == "a" {
			return a.repo, nil
		}
		return b.repo, nil
	})
	req := MergeRequest{CaseID: "case", FixCommits: map[string]string{"a": ac, "b": bc}, TargetBranches: map[string]string{"a": "test", "b": "test"}, Changes: []CodeChange{{Repo: "a", FixCommit: ac, FixBranch: "fix/bug", TargetEnvironmentBranch: "test", PushRemote: "origin"}, {Repo: "b", FixCommit: bc, FixBranch: "fix/bug", TargetEnvironmentBranch: "test", PushRemote: "origin"}}}
	inspection, err := service.Inspect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.TargetHeads = map[string]string{"a": inspection.Repositories["a"].TargetHead, "b": inspection.Repositories["b"].TargetHead}
	rejectAllPushes(t, b.remote)
	result, err := service.MergeAndPush(context.Background(), req)
	if err == nil || !result.Repositories["a"].Pushed || result.Repositories["b"].Pushed || result.Repositories["b"].MergeCommit == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitIntegrationOrchestratorRefreshesStaleApprovalScope(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	fixCommit := f.makeFix(t, "fix\n")
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-git-scope", BugID: "bug", SystemID: "system", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "fix-attempt", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	attempt := PhaseAttempt{ID: incident.CurrentAttemptID, CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	change := CodeChange{ID: "change-api", CaseID: incident.ID, AttemptID: attempt.ID, Repo: "api", BaseBranch: "test", FixBranch: "fix/bug", FixCommit: fixCommit, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: "pushed"}
	if err := store.RecordCodeChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, f.service(t), &recordingDeploymentVerifier{})

	stale, err := orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "approve-stale", ActorID: "alice", TargetHeads: map[string]string{"api": "old"}})
	if !errors.Is(err, ErrMergeApprovalStale) || stale.Status != CaseWaitingMergeApproval || stale.Version <= incident.Version {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	changes, err := store.ListCodeChanges(ctx, incident.ID)
	if err != nil || len(changes) != 1 || changes[0].MergeBaseHead == "" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if approvals, _ := store.ListApprovals(ctx, incident.ID); len(approvals) != 0 {
		t.Fatalf("stale approval persisted: %+v", approvals)
	}

	merged, err := orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: stale.Version, IdempotencyKey: "approve-current", ActorID: "alice", TargetHeads: map[string]string{"api": changes[0].MergeBaseHead}})
	if err != nil || merged.Status != CaseWaitingDeployment {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
	approvals, err := store.ListApprovals(ctx, incident.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	var scope MergeApprovalScope
	if err := json.Unmarshal(approvals[0].ScopeJSON, &scope); err != nil || len(scope.CodeChanges) != 1 || scope.CodeChanges[0].ApprovalKey != MergeApprovalKey(incident.ID, "api", fixCommit, "test", changes[0].MergeBaseHead) {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
}

func TestGitIntegrationFreshApprovalRetainsPreviouslyBlockedRepository(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := IncidentCase{ID: "case-partial-fresh", BugID: "bug", Status: CaseWaitingMergeApproval, CycleNumber: 1, CurrentAttemptID: "fix", Version: 1}
	if err := store.CreateCase(ctx, incident); err != nil {
		t.Fatal(err)
	}
	attempt := PhaseAttempt{ID: "fix", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ repo, status string }{{"a", "pushed"}, {"b", "push_unknown"}} {
		change := CodeChange{ID: "change-" + item.repo, CaseID: incident.ID, AttemptID: attempt.ID, Repo: item.repo, BaseBranch: "test", FixBranch: "fix/" + item.repo, FixCommit: "fix-" + item.repo, TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushRemote: "origin", PushStatus: item.status}
		if err := store.RecordCodeChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	git := &recordingGitIntegration{result: MergeResult{Repositories: map[string]MergeRepositoryResult{"a": {MergeCommit: "merge-a", Pushed: true}, "b": {MergeCommit: "merge-b", Pushed: true}}}}
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, git, &recordingDeploymentVerifier{})
	merged, err := orchestrator.ApproveMerge(ctx, ApproveMergeCommand{CaseID: incident.ID, ExpectedVersion: 1, IdempotencyKey: "fresh-partial", ActorID: "alice", TargetHeads: map[string]string{"a": "head-a", "b": "head-b"}})
	if err != nil || merged.Status != CaseWaitingDeployment || len(git.merges) != 1 || len(git.merges[0].FixCommits) != 2 {
		t.Fatalf("merged=%+v request=%+v err=%v", merged, git.merges, err)
	}
}

func rejectAllPushes(t *testing.T, bareRepo string) {
	t.Helper()
	hook := filepath.Join(bareRepo, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
