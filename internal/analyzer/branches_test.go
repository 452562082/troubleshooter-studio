package analyzer

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListBranchesPreservesSlashesAndOnlyStripsRemoteName(t *testing.T) {
	repo := t.TempDir()
	runGitForBranchesTest(t, repo, "init", "-b", "main")
	runGitForBranchesTest(t, repo, "config", "user.name", "Test User")
	runGitForBranchesTest(t, repo, "config", "user.email", "test@example.com")
	runGitForBranchesTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	runGitForBranchesTest(t, repo, "branch", "feature/xiaolong_v1.4")
	runGitForBranchesTest(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "remote.git"))
	runGitForBranchesTest(t, repo, "remote", "add", "team", filepath.Join(t.TempDir(), "team.git"))
	runGitForBranchesTest(t, repo, "remote", "add", "team/upstream", filepath.Join(t.TempDir(), "upstream.git"))
	runGitForBranchesTest(t, repo, "update-ref", "refs/remotes/origin/feature/remote-only", "HEAD")
	runGitForBranchesTest(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGitForBranchesTest(t, repo, "update-ref", "refs/remotes/team/upstream/feature/nested-remote", "HEAD")

	want := []string{"feature/nested-remote", "feature/remote-only", "feature/xiaolong_v1.4", "main"}
	if got := ListBranches(repo); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListBranches() = %v, want %v", got, want)
	}
}

func TestListBranchesRejectsNonRepository(t *testing.T) {
	if got := ListBranches(t.TempDir()); got != nil {
		t.Fatalf("ListBranches() = %v, want nil", got)
	}
}

func runGitForBranchesTest(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	if output, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
