package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestAutoAnalyzeCacheKeyIncludesTopologySchemaAndRepositoryHeads(t *testing.T) {
	alpha := filepath.Join(t.TempDir(), "alpha")
	beta := filepath.Join(t.TempDir(), "beta")
	initAutoAnalyzeGitRepo(t, alpha)
	initAutoAnalyzeGitRepo(t, beta)
	expanded := map[string]string{"beta": beta, "alpha": alpha}

	first := autoAnalyzeCacheKey("mall", expanded)
	if !strings.Contains(first, "\x1ftopology-schema="+topology.SchemaVersion) {
		t.Fatalf("cache key lacks topology schema version: %q", first)
	}
	if same := autoAnalyzeCacheKey("mall", map[string]string{"alpha": alpha, "beta": beta}); same != first {
		t.Fatalf("same paths and HEADs produced different keys:\nfirst=%q\nsame=%q", first, same)
	}

	commitAutoAnalyzeRepo(t, alpha, "head-change.txt", "changed")
	if changed := autoAnalyzeCacheKey("mall", expanded); changed == first {
		t.Fatalf("changed HEAD reused cache key %q", changed)
	}
}

func TestAutoAnalyzeCacheKeyUsesDeterministicUnavailableHeadSentinels(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	notGit := filepath.Join(t.TempDir(), "plain-directory")
	if err := os.MkdirAll(notGit, 0o755); err != nil {
		t.Fatal(err)
	}

	key := autoAnalyzeCacheKey("mall", map[string]string{"missing": missing, "plain": notGit})
	if !strings.Contains(key, "\x1fmissing="+missing+"\x1fhead=missing") {
		t.Fatalf("cache key lacks missing sentinel: %q", key)
	}
	if !strings.Contains(key, "\x1fplain="+notGit+"\x1fhead=not-git") {
		t.Fatalf("cache key lacks not-git sentinel: %q", key)
	}
}

func initAutoAnalyzeGitRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runAutoAnalyzeGit(t, path, "init", "--quiet")
	runAutoAnalyzeGit(t, path, "config", "user.email", "tshoot@example.test")
	runAutoAnalyzeGit(t, path, "config", "user.name", "Tshoot Test")
	commitAutoAnalyzeRepo(t, path, "README.md", "fixture")
}

func commitAutoAnalyzeRepo(t *testing.T, path, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runAutoAnalyzeGit(t, path, "add", name)
	runAutoAnalyzeGit(t, path, "commit", "--quiet", "-m", name)
}

func runAutoAnalyzeGit(t *testing.T, path string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", path}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
}
