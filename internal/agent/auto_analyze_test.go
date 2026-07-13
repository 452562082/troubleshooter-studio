package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestAutoAnalyzeCacheKeyIncludesTopologySchemaAndRepositoryHeads(t *testing.T) {
	alpha := filepath.Join(t.TempDir(), "alpha")
	beta := filepath.Join(t.TempDir(), "beta")
	initAutoAnalyzeGitRepo(t, alpha)
	initAutoAnalyzeGitRepo(t, beta)
	expanded := map[string]string{"beta": beta, "alpha": alpha}
	cfg := autoAnalyzeCacheConfig()

	first := autoAnalyzeCacheKey(cfg, expanded)
	material := autoAnalyzeCacheMaterialFor(cfg, expanded)
	if material.SchemaVersion != topology.SchemaVersion {
		t.Fatalf("cache material schema=%q, want %q", material.SchemaVersion, topology.SchemaVersion)
	}
	if same := autoAnalyzeCacheKey(cfg, map[string]string{"alpha": alpha, "beta": beta}); same != first {
		t.Fatalf("same paths and HEADs produced different keys:\nfirst=%q\nsame=%q", first, same)
	}

	commitAutoAnalyzeRepo(t, alpha, "head-change.txt", "changed")
	if changed := autoAnalyzeCacheKey(cfg, expanded); changed == first {
		t.Fatalf("changed HEAD reused cache key %q", changed)
	}
}

func TestAutoAnalyzeCacheKeyUsesDeterministicUnavailableHeadSentinels(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := autoAnalyzeCacheConfig()
	missingKey := autoAnalyzeCacheKey(cfg, map[string]string{"repo": missing})
	missingMaterial := autoAnalyzeCacheMaterialFor(cfg, map[string]string{"repo": missing})
	if got := missingMaterial.Repositories[0].Head; got != "missing" {
		t.Fatalf("missing head sentinel=%q", got)
	}

	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	notGitKey := autoAnalyzeCacheKey(cfg, map[string]string{"repo": missing})
	notGitMaterial := autoAnalyzeCacheMaterialFor(cfg, map[string]string{"repo": missing})
	if got := notGitMaterial.Repositories[0].Head; got != "not-git" {
		t.Fatalf("not-git head sentinel=%q", got)
	}
	if notGitKey == missingKey {
		t.Fatal("missing and not-git repository states reused a cache key")
	}
}

func TestAutoAnalyzeCacheKeyInvalidatesTopologyRelevantConfiguration(t *testing.T) {
	root := t.TempDir()
	paths := map[string]string{"web": filepath.Join(root, "web"), "api": filepath.Join(root, "api")}
	base := autoAnalyzeCacheConfig()
	baseKey := autoAnalyzeCacheKey(base, paths)

	tests := map[string]func(*config.SystemConfig){
		"service_names": func(cfg *config.SystemConfig) { cfg.Repos[0].ServiceNames = []string{"web-v2"} },
		"role":          func(cfg *config.SystemConfig) { cfg.Repos[0].Role = config.RoleGateway },
		"api_domain":    func(cfg *config.SystemConfig) { cfg.Environments[0].APIDomain = "api-v2.mall.test" },
		"web_domain":    func(cfg *config.SystemConfig) { cfg.Environments[0].WebDomain = "web-v2.mall.test" },
		"override":      func(cfg *config.SystemConfig) { cfg.ServiceTopology.Overrides[0].Path = "/v2/orders" },
		"repo_alias":    func(cfg *config.SystemConfig) { cfg.Repos[0].Name = "web-repo-v2" },
		"k8s_alias": func(cfg *config.SystemConfig) {
			cfg.Infrastructure.Observability.K8sRuntime.ServiceMap[0].Namespace = "web-v2"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := autoAnalyzeCacheConfig()
			mutate(changed)
			if got := autoAnalyzeCacheKey(changed, paths); got == baseKey {
				t.Fatalf("configuration change %q reused cache key %q", name, got)
			}
		})
	}

	secret := "cache-secret-must-not-leak"
	withSecret := autoAnalyzeCacheConfig()
	withSecret.System.Name = secret
	if key := autoAnalyzeCacheKey(withSecret, paths); strings.Contains(key, secret) {
		t.Fatalf("cache key exposed configuration contents: %q", key)
	}
}

func TestAutoAnalyzeCacheKeyCanonicalJSONAvoidsDelimiterCollisions(t *testing.T) {
	root := t.TempDir()
	left := map[string]string{
		"a": filepath.Join(root, "x") + "\x1fhead=missing\x1fb=" + filepath.Join(root, "y"),
	}
	right := map[string]string{
		"a": filepath.Join(root, "x"),
		"b": filepath.Join(root, "y"),
	}
	cfg := autoAnalyzeCacheConfig()
	if leftKey, rightKey := autoAnalyzeCacheKey(cfg, left), autoAnalyzeCacheKey(cfg, right); leftKey == rightKey {
		t.Fatalf("distinct repository mappings collided: %q", leftKey)
	}
}

func autoAnalyzeCacheConfig() *config.SystemConfig {
	return &config.SystemConfig{
		System:       config.System{ID: "mall", Name: "Mall"},
		Environments: []config.Environment{{ID: "dev", APIDomain: "api.mall.test", WebDomain: "web.mall.test"}},
		Repos: []config.Repo{
			{Name: "web", Stack: "node", Role: config.RoleFrontend, ServiceNames: []string{"web"}},
			{Name: "api", Stack: "go", Role: config.RoleBackend, ServiceNames: []string{"api"}},
		},
		ServiceTopology: config.ServiceTopology{Overrides: []config.ServiceTopologyOverride{{
			Action: "confirm", FromService: "web", ToService: "api", Protocol: "http", Method: "GET", Path: "/orders",
		}}},
		Infrastructure: config.Infrastructure{Observability: config.Observability{K8sRuntime: config.K8sRuntime{
			ServiceMap: []config.K8sRuntimeServiceMapEntry{{Env: "dev", Service: "web", Namespace: "web"}},
		}}},
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
