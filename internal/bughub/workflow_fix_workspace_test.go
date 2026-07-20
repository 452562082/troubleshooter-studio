package bughub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixWorkspaceManagerLocksExplicitSourceBaselineAndKeepsEnvironmentTarget(t *testing.T) {
	fixture := newGitFixture(t)
	runGitTest(t, fixture.repo, "switch", "-c", "feature/wrong-base")
	if err := os.WriteFile(filepath.Join(fixture.repo, "feature.txt"), []byte("unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repo, "add", "feature.txt")
	runGitTest(t, fixture.repo, "commit", "-m", "unrelated feature")
	featureCommit := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "HEAD"))
	runGitTest(t, fixture.repo, "push", "origin", "feature/wrong-base")
	advanceTestBranchAfterFeatureFork(t, fixture)

	botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
	manager := NewFixWorkspaceManager(filepath.Join(t.TempDir(), "fix-worktrees"), func(_ context.Context, caseID, repo string) (string, error) {
		if caseID != "case-1" || repo != "api" {
			return "", errors.New("unexpected repository request")
		}
		return fixture.repo, nil
	})
	lease, err := manager.Prepare(context.Background(), "case-1", "attempt-1", "test", BotRef{Path: botPath}, []byte(`{"source_baselines":{"api":"feature/wrong-base"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.bindings) != 1 {
		t.Fatalf("bindings = %+v", lease.bindings)
	}
	binding := lease.bindings[0]
	if binding.BaseBranch != "feature/wrong-base" || binding.BaseCommit != featureCommit || binding.TargetEnvironmentBranch != "test" {
		t.Fatalf("binding = %+v, want feature/wrong-base@%s -> test", binding, featureCommit)
	}
	if got := strings.TrimSpace(runGitTest(t, binding.Worktree, "rev-parse", "HEAD")); got != featureCommit {
		t.Fatalf("worktree HEAD = %s, want %s", got, featureCommit)
	}
	if got := strings.TrimSpace(runGitTest(t, fixture.repo, "branch", "--show-current")); got != "feature/wrong-base" {
		t.Fatalf("source checkout branch changed to %q", got)
	}

	wrong := PhaseResult{Outcome: PhaseOutcomeFixPushed, CodeChanges: []CodeChange{{Repo: "api", BaseBranch: "test", FixCommit: featureCommit, TargetEnvironmentBranch: "test", PushRemote: "origin"}}}
	if err := lease.ValidateResult(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "locked source baseline") {
		t.Fatalf("wrong reported baseline accepted: %v", err)
	}

	runGitTest(t, binding.Worktree, "switch", "-c", "fix/bug-1")
	if err := os.WriteFile(filepath.Join(binding.Worktree, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, binding.Worktree, "add", "fix.txt")
	runGitTest(t, binding.Worktree, "commit", "-m", "fix")
	fixCommit := strings.TrimSpace(runGitTest(t, binding.Worktree, "rev-parse", "HEAD"))
	valid := PhaseResult{Outcome: PhaseOutcomeFixPushed, CodeChanges: []CodeChange{{Repo: "api", BaseBranch: "feature/wrong-base", FixCommit: fixCommit, TargetEnvironmentBranch: "test", PushRemote: "origin"}}}
	if err := lease.ValidateResult(context.Background(), valid); err != nil {
		t.Fatalf("valid locked-base fix rejected: %v", err)
	}
	if !strings.Contains(lease.Prompt(), binding.Worktree) || !strings.Contains(lease.Prompt(), featureCommit) {
		t.Fatalf("prompt does not carry locked binding: %s", lease.Prompt())
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binding.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dedicated worktree retained: %v", err)
	}
}

func TestFixWorkspaceLeaseRejectsSelfReportedWrongBranchAndMergeHistory(t *testing.T) {
	fixture := newGitFixture(t)
	runGitTest(t, fixture.repo, "switch", "-c", "feature/wrong-base")
	if err := os.WriteFile(filepath.Join(fixture.repo, "feature.txt"), []byte("unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repo, "add", "feature.txt")
	runGitTest(t, fixture.repo, "commit", "-m", "unrelated feature")
	advanceTestBranchAfterFeatureFork(t, fixture)
	botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
	manager := NewFixWorkspaceManager(filepath.Join(t.TempDir(), "fix-worktrees"), func(context.Context, string, string) (string, error) {
		return fixture.repo, nil
	})
	runGitTest(t, fixture.repo, "push", "origin", "feature/wrong-base")
	lease, err := manager.Prepare(context.Background(), "case-2", "attempt-2", "test", BotRef{Path: botPath}, []byte(`{"source_baselines":{"api":"feature/wrong-base"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()
	binding := lease.bindings[0]

	branchMismatch := PhaseResult{Outcome: PhaseOutcomeFixPushed, CodeChanges: []CodeChange{{Repo: "api", BaseBranch: "feature/wrong", FixCommit: binding.BaseCommit, TargetEnvironmentBranch: "test", PushRemote: "origin"}}}
	if err := lease.ValidateResult(context.Background(), branchMismatch); err == nil || !strings.Contains(err.Error(), "locked source baseline") {
		t.Fatalf("self-reported wrong branch accepted: %v", err)
	}

	runGitTest(t, binding.Worktree, "switch", "-c", "fix/with-merge")
	runGitTest(t, binding.Worktree, "merge", "--no-ff", "origin/test", "-m", "merge unrelated environment history")
	mergeCommit := strings.TrimSpace(runGitTest(t, binding.Worktree, "rev-parse", "HEAD"))
	merged := PhaseResult{Outcome: PhaseOutcomeFixPushed, CodeChanges: []CodeChange{{Repo: "api", BaseBranch: "feature/wrong-base", FixCommit: mergeCommit, TargetEnvironmentBranch: "test", PushRemote: "origin"}}}
	if err := lease.ValidateResult(context.Background(), merged); err == nil || !strings.Contains(err.Error(), "merge or disconnected commit") {
		t.Fatalf("merge history accepted: %v", err)
	}
}

func TestFixWorkspaceManagerDefaultsMissingSourceBaselineToEnvironmentBranch(t *testing.T) {
	fixture := newGitFixture(t)
	botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
	manager := NewFixWorkspaceManager(filepath.Join(t.TempDir(), "fix-worktrees"), func(context.Context, string, string) (string, error) {
		return fixture.repo, nil
	})
	lease, err := manager.Prepare(context.Background(), "case-3", "attempt-3", "test", BotRef{Path: botPath}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()
	if len(lease.bindings) != 1 || lease.bindings[0].Repo != "api" || lease.bindings[0].BaseBranch != "test" || lease.bindings[0].TargetEnvironmentBranch != "test" {
		t.Fatalf("bindings=%+v, want api test -> test", lease.bindings)
	}
}

func TestFixWorkspaceManagerDefaultsBlankApprovedBaselineToEnvironmentBranch(t *testing.T) {
	fixture := newGitFixture(t)
	botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
	manager := NewFixWorkspaceManager(filepath.Join(t.TempDir(), "fix-worktrees"), func(context.Context, string, string) (string, error) {
		return fixture.repo, nil
	})
	lease, err := manager.Prepare(context.Background(), "case-blank", "attempt-blank", "test", BotRef{Path: botPath}, []byte(`{"source_baselines":{"api":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()
	if len(lease.bindings) != 1 || lease.bindings[0].BaseBranch != "test" {
		t.Fatalf("bindings=%+v, want blank approval resolved to test", lease.bindings)
	}
}

func TestFixWorkspaceLeaseRequiresEveryApprovedRepository(t *testing.T) {
	lease := &FixWorkspaceLease{bindings: []fixWorkspaceBinding{{Repo: "api"}, {Repo: "web"}}}
	err := lease.ValidateResult(context.Background(), PhaseResult{Outcome: PhaseOutcomeFixPushed, CodeChanges: []CodeChange{{Repo: "api"}}})
	if err == nil || !strings.Contains(err.Error(), "approval locked 2") {
		t.Fatalf("err=%v", err)
	}
}

func writeFixWorkspaceBranchMap(t *testing.T, environment, repo, branch string) string {
	t.Helper()
	root := t.TempDir()
	references := filepath.Join(root, "skills", "routing", "references")
	if err := os.MkdirAll(references, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "environments:\n  " + environment + ":\n    repos:\n      " + repo + ": \"" + branch + "\"\n"
	if err := os.WriteFile(filepath.Join(references, "env-branch-map.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func advanceTestBranchAfterFeatureFork(t *testing.T, fixture gitFixture) {
	t.Helper()
	runGitTest(t, fixture.repo, "switch", "test")
	if err := os.WriteFile(filepath.Join(fixture.repo, "target.txt"), []byte("target branch advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repo, "add", "target.txt")
	runGitTest(t, fixture.repo, "commit", "-m", "advance target environment")
	runGitTest(t, fixture.repo, "push", "origin", "test")
	runGitTest(t, fixture.repo, "switch", "feature/wrong-base")
}
