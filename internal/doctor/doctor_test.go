package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/gitclone"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func loadShop(t *testing.T) *config.SystemConfig {
	t.Helper()
	cfg, err := config.Load(filepath.Join(projectRoot(t), "examples", "shop-system.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// 帮助：按 category 过滤
func issuesByCategory(rep *Report, cat string) []Issue {
	var out []Issue
	for _, i := range rep.Issues {
		if i.Category == cat {
			out = append(out, i)
		}
	}
	return out
}

func hasCategory(rep *Report, cat string) bool {
	return len(issuesByCategory(rep, cat)) > 0
}

func TestCheck_MissingRepo(t *testing.T) {
	cfg := loadShop(t)
	rep, err := Check(cfg, "/tmp/definitely-not-existing-dir-xyz")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	issues := issuesByCategory(rep, CatMissingRepo)
	if len(issues) != len(cfg.Repos) {
		t.Errorf("expected missing-repo for each repo (%d), got %d", len(cfg.Repos), len(issues))
	}
	for _, i := range issues {
		if i.Severity != SeverityError {
			t.Errorf("missing-repo should be error, got %s", i.Severity)
		}
	}
}

func TestCheck_NoReposRoot_SkipsRepoChecks(t *testing.T) {
	cfg := loadShop(t)
	rep, err := Check(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if hasCategory(rep, CatMissingRepo) {
		t.Errorf("should not emit missing-repo when reposRoot is empty")
	}
	// 没扫描仓库也就不应报 data-store-unused / config-center-unused
	if hasCategory(rep, CatDataStoreUnused) || hasCategory(rep, CatConfigCenterUnused) {
		t.Errorf("should not emit usage warnings without any scan")
	}
}

func TestCheck_ServiceDrift_DeclaredNotFound(t *testing.T) {
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesByCategory(rep, CatServiceDrift)
	found := false
	for _, i := range issues {
		if strings.Contains(i.Message, "order-worker") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected service-drift for order-worker (declared but not in go.mod)")
	}
}

func TestCheck_StackMismatch(t *testing.T) {
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	// 人为把 order-service 的 stack 改成 java（但实际是 go，有 go.mod）
	for i := range cfg.Repos {
		if cfg.Repos[i].Name == "order-service" {
			cfg.Repos[i].Stack = "java"
		}
	}
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCategory(rep, CatStackMismatch) {
		t.Errorf("expected stack-mismatch, got issues: %+v", rep.Issues)
	}
}

func TestCheck_ConfigCenterUnused(t *testing.T) {
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	// 改声明成 apollo，但 fake-repos 里是 nacos 配置 → registry 用 apollo scanner 扫不到。
	// 多源 schema:loadShop 已 migrate 老 yaml 到 ConfigCenters[0],改这里。
	cfg.Infrastructure.ConfigCenters[0].Type = "apollo"
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCategory(rep, CatConfigCenterUnused) {
		t.Errorf("expected config-center-unused, got: %+v", rep.Issues)
	}
}

func TestCheck_UndeclaredProfile(t *testing.T) {
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	// 去掉 dev 环境 → product-service 的 bootstrap-dev.yml 里的 "dev" profile 就成了未声明
	cfg.Environments = cfg.Environments[1:] // 保留 staging/prod
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCategory(rep, CatUndeclaredProfile) {
		t.Errorf("expected undeclared-env-profile, got: %+v", rep.Issues)
	}
}

func TestCheck_ConfigCenterNone_WithFindings(t *testing.T) {
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	cfg.Infrastructure.ConfigCenters[0].Type = "none"
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	// type=none 时 analyzer 不扫 config，所以不会报 drift
	// 但会报 config-center-unused? 不会（因为 ccType=none 被跳过）
	if hasCategory(rep, CatConfigCenterUnused) {
		t.Errorf("config_center=none should not trigger unused warning")
	}
}

func TestCheck_CleanApolloExample(t *testing.T) {
	cfg, err := config.Load(filepath.Join(projectRoot(t), "examples", "apollo-system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos-apollo")
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	// apollo 示例干净，不该有 error
	errs, _, _ := rep.Counts()
	if errs > 0 {
		t.Errorf("apollo example should have 0 errors, got issues: %+v", rep.Issues)
	}
	// 但应该没有 config-center-unused（因为 fake-repos-apollo 的确有 apollo 配置）
	if hasCategory(rep, CatConfigCenterUnused) {
		t.Errorf("apollo fake repo has apollo config, should not be marked unused")
	}
}

// 构造一个有实际 origin 的 repo dir：init + 设一个 origin URL + 放些文件让 analyzer 可扫
func makeRepoWithOrigin(t *testing.T, originURL string) string {
	t.Helper()
	if !gitclone.Available() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("remote", "add", "origin", originURL)
	// 放一个 go.mod 让 analyzer 能跑通
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "init")
	return dir
}

func TestCheck_OriginMatches(t *testing.T) {
	if !gitclone.Available() {
		t.Skip("git not available")
	}
	origin := "git@github.com:x/y.git"
	repoDir := makeRepoWithOrigin(t, origin)
	reposRoot := filepath.Dir(repoDir)
	repoName := filepath.Base(repoDir)

	cfg := &config.SystemConfig{
		System:       config.System{ID: "x", Name: "X"},
		Agent:        config.Agent{Name: "a", WorkspaceName: "a", Model: "m"},
		Environments: []config.Environment{{ID: "dev", APIDomain: "x", IsProd: false}},
		Repos: []config.Repo{{
			Name: repoName, URL: origin, Stack: "go",
			EnvBranches: map[string]string{"dev": "main"},
		}},
		Generation: config.Generation{TargetHost: "openclaw"},
		Meta:       config.Meta{SchemaVersion: "0.1"},
	}
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	if hasCategory(rep, CatOriginMismatch) {
		t.Errorf("unexpected origin-mismatch: %+v", rep.Issues)
	}
}

func TestCheck_OriginMismatch(t *testing.T) {
	if !gitclone.Available() {
		t.Skip("git not available")
	}
	actualOrigin := "git@github.com:real/repo.git"
	declaredURL := "git@github.com:wrong/repo.git"
	repoDir := makeRepoWithOrigin(t, actualOrigin)
	reposRoot := filepath.Dir(repoDir)
	repoName := filepath.Base(repoDir)

	cfg := &config.SystemConfig{
		System:       config.System{ID: "x", Name: "X"},
		Agent:        config.Agent{Name: "a", WorkspaceName: "a", Model: "m"},
		Environments: []config.Environment{{ID: "dev", APIDomain: "x", IsProd: false}},
		Repos: []config.Repo{{
			Name: repoName, URL: declaredURL, Stack: "go",
			EnvBranches: map[string]string{"dev": "main"},
		}},
		Generation: config.Generation{TargetHost: "openclaw"},
		Meta:       config.Meta{SchemaVersion: "0.1"},
	}
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesByCategory(rep, CatOriginMismatch)
	if len(issues) != 1 {
		t.Fatalf("expected 1 origin-mismatch, got %d: %+v", len(issues), rep.Issues)
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("should be warning, got %s", issues[0].Severity)
	}
	if !strings.Contains(issues[0].Message, "wrong/repo") {
		t.Errorf("message should reference declared URL: %q", issues[0].Message)
	}
}

func TestCheck_OriginCrossProtocolMatch(t *testing.T) {
	if !gitclone.Available() {
		t.Skip("git not available")
	}
	// origin 用 https 写，system.yaml 用 ssh 写 —— 应视为同一
	origin := "https://github.com/x/y.git"
	declared := "git@github.com:x/y.git"
	repoDir := makeRepoWithOrigin(t, origin)
	reposRoot := filepath.Dir(repoDir)
	repoName := filepath.Base(repoDir)

	cfg := &config.SystemConfig{
		System:       config.System{ID: "x", Name: "X"},
		Agent:        config.Agent{Name: "a", WorkspaceName: "a", Model: "m"},
		Environments: []config.Environment{{ID: "dev", APIDomain: "x", IsProd: false}},
		Repos: []config.Repo{{
			Name: repoName, URL: declared, Stack: "go",
			EnvBranches: map[string]string{"dev": "main"},
		}},
		Generation: config.Generation{TargetHost: "openclaw"},
		Meta:       config.Meta{SchemaVersion: "0.1"},
	}
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	if hasCategory(rep, CatOriginMismatch) {
		t.Errorf("cross-protocol should canonicalize equal; issues: %+v", rep.Issues)
	}
}

func TestCheck_OriginNotGitRepo(t *testing.T) {
	// 使用 examples/fake-repos/order-service 目录（没有 .git，不是 git 仓库）
	cfg := loadShop(t)
	reposRoot := filepath.Join(projectRoot(t), "examples", "fake-repos")
	rep, err := Check(cfg, reposRoot)
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesByCategory(rep, CatOriginMismatch)
	// 预期给 info 级"不是 git 仓库"提示，但不应是 warning
	for _, i := range issues {
		if i.Severity != SeverityInfo {
			t.Errorf("non-git repo should be info, got %s: %+v", i.Severity, i)
		}
	}
}

func TestDetectStack(t *testing.T) {
	root := projectRoot(t)
	cases := map[string]string{
		filepath.Join(root, "examples", "fake-repos", "order-service"):   "go",
		filepath.Join(root, "examples", "fake-repos", "product-service"): "java",
		filepath.Join(root, "examples", "fake-repos", "web-frontend"):    "node",
	}
	for path, want := range cases {
		got := detectStack(path)
		if got != want {
			t.Errorf("detectStack(%s): got %q, want %q", filepath.Base(path), got, want)
		}
	}
}
