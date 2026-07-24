package bughub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixWorkspaceManagerLocksRemoteCommitWhenLocalBranchIsBehind(t *testing.T) {
	fixture := newGitFixture(t)
	runGitTest(t, fixture.repo, "switch", "-c", "feature/approved")
	runGitTest(t, fixture.repo, "push", "-u", "origin", "feature/approved")
	localCommit := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "HEAD"))

	publisher := filepath.Join(t.TempDir(), "publisher")
	runGitTest(t, filepath.Dir(publisher), "clone", fixture.remote, publisher)
	runGitTest(t, publisher, "config", "user.name", "Remote Publisher")
	runGitTest(t, publisher, "config", "user.email", "publisher@example.test")
	runGitTest(t, publisher, "switch", "feature/approved")
	if err := os.WriteFile(filepath.Join(publisher, "remote.txt"), []byte("remote-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, publisher, "add", "remote.txt")
	runGitTest(t, publisher, "commit", "-m", "advance approved branch remotely")
	remoteCommit := strings.TrimSpace(runGitTest(t, publisher, "rev-parse", "HEAD"))
	runGitTest(t, publisher, "push", "origin", "feature/approved")
	if remoteCommit == localCommit {
		t.Fatal("remote fixture did not advance")
	}

	botPath := writeFixWorkspaceBranchMap(t, "test", "api", "test")
	manager := NewFixWorkspaceManager(filepath.Join(t.TempDir(), "fix-worktrees"), func(context.Context, string, string) (string, error) {
		return fixture.repo, nil
	})
	lease, err := manager.Prepare(
		context.Background(),
		"case-remote-only",
		"attempt-remote-only",
		"test",
		BotRef{Path: botPath},
		[]byte(`{"source_baselines":{"api":"feature/approved"}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close(context.Background()) }()

	if len(lease.bindings) != 1 {
		t.Fatalf("bindings = %+v", lease.bindings)
	}
	binding := lease.bindings[0]
	if binding.BaseCommit != remoteCommit {
		t.Fatalf("locked commit = %s, want remote %s", binding.BaseCommit, remoteCommit)
	}
	if got := strings.TrimSpace(runGitTest(t, binding.Worktree, "rev-parse", "HEAD")); got != remoteCommit {
		t.Fatalf("fix workspace HEAD = %s, want remote %s", got, remoteCommit)
	}
	if got := strings.TrimSpace(runGitTest(t, fixture.repo, "rev-parse", "refs/heads/feature/approved")); got != localCommit {
		t.Fatalf("source local branch moved to %s, want unchanged %s", got, localCommit)
	}
}
