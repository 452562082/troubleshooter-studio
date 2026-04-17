package gitclone

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupBareRepo 在 t.TempDir() 里搭一个包含两个分支的 bare 仓库，返回其路径
// 没有 git 可执行时自动 t.Skip
func setupBareRepo(t *testing.T) string {
	t.Helper()
	if !Available() {
		t.Skip("git not available in PATH")
	}

	work := t.TempDir()
	src := filepath.Join(work, "source")
	bare := filepath.Join(work, "origin.git")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// 初始化源仓库 + 2 次提交在 main 分支
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(src, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(src, "add", ".")
	runGit(src, "commit", "-m", "init")

	// 开 develop 分支
	runGit(src, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(src, "DEV.md"), []byte("dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(src, "add", ".")
	runGit(src, "commit", "-m", "dev-change")

	// 回 main
	runGit(src, "checkout", "main")

	// clone 为 bare
	if err := exec.Command("git", "clone", "--bare", src, bare).Run(); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	return bare
}

func TestClone_DefaultBranch(t *testing.T) {
	bare := setupBareRepo(t)
	dest := filepath.Join(t.TempDir(), "repo")
	if err := Clone(Options{URL: bare, Dest: dest, Depth: 1}); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("expected README.md on default branch: %v", err)
	}
}

func TestClone_SpecificBranch(t *testing.T) {
	bare := setupBareRepo(t)
	dest := filepath.Join(t.TempDir(), "repo")
	if err := Clone(Options{URL: bare, Dest: dest, Branch: "develop", Depth: 1}); err != nil {
		t.Fatalf("Clone develop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "DEV.md")); err != nil {
		t.Errorf("expected DEV.md on develop branch: %v", err)
	}
}

func TestClone_InvalidBranch(t *testing.T) {
	bare := setupBareRepo(t)
	dest := filepath.Join(t.TempDir(), "repo")
	err := Clone(Options{URL: bare, Dest: dest, Branch: "ghost", Depth: 1})
	if err == nil {
		t.Error("expected error for nonexistent branch")
	}
}

func TestClone_MissingRequired(t *testing.T) {
	if err := Clone(Options{}); err == nil {
		t.Error("expected error when URL missing")
	}
	if err := Clone(Options{URL: "x"}); err == nil {
		t.Error("expected error when Dest missing")
	}
}

func TestAvailable(t *testing.T) {
	// 只要机器能跑 go test，基本都有 git
	if !Available() {
		t.Skip("git not available")
	}
}

func TestReadOrigin(t *testing.T) {
	bare := setupBareRepo(t)
	dest := filepath.Join(t.TempDir(), "repo")
	if err := Clone(Options{URL: bare, Dest: dest, Depth: 1}); err != nil {
		t.Fatal(err)
	}
	origin, err := ReadOrigin(dest)
	if err != nil {
		t.Fatalf("ReadOrigin: %v", err)
	}
	if origin != bare {
		t.Errorf("origin: got %q, want %q", origin, bare)
	}
}

func TestReadOrigin_NotGitRepo(t *testing.T) {
	if !Available() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	_, err := ReadOrigin(dir)
	if err != ErrNotGitRepo {
		t.Errorf("expected ErrNotGitRepo, got %v", err)
	}
}
