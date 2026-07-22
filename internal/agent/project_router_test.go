package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type routerResult struct {
	Status   string            `json:"status"`
	Allowed  bool              `json:"allowed"`
	Reason   string            `json:"reason"`
	Action   string            `json:"action"`
	Silent   bool              `json:"silent"`
	SystemID string            `json:"system_id"`
	Agents   map[string]string `json:"agents"`
}

func writeRouterMeta(t *testing.T, root, agentName, systemID, systemName string, repos []discover.ProjectRepository) {
	t.Helper()
	dir := filepath.Join(root, "skills", agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := discover.Meta{
		SchemaVersion: 2,
		SystemID:      systemID,
		SystemName:    systemName,
		AgentID:       agentName,
		Role:          discover.RoleTroubleshooter,
		InternalAgents: []discover.InternalAgent{
			{ID: agentName, Role: discover.RoleTroubleshooter},
			{ID: systemID + "-validator", Role: discover.RoleValidator},
			{ID: systemID + "-fixer", Role: discover.RoleFixer},
		},
		ProjectRepositories: repos,
		Target:              "codex",
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, discover.MetaFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runProjectRouter(t *testing.T, root, cwd string, extra ...string) routerResult {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	args := []string{filepath.Join(root, "skills", projectRouterSkillName, "scripts", "resolve.py"), "--root", root, "--cwd", cwd}
	args = append(args, extra...)
	out, err := exec.Command(python, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("router failed: %v\n%s", err, out)
	}
	var result routerResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode router result: %v\n%s", err, out)
	}
	return result
}

func TestProjectRouterMatchesLocalRepositoryAndAgentOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex")
	if err := installProjectRouter(root, TargetCodex); err != nil {
		t.Fatal(err)
	}

	shopRepo := filepath.Join(home, "src", "shop")
	billingRepo := filepath.Join(home, "src", "billing")
	for _, dir := range []string{filepath.Join(shopRepo, "cmd", "api"), billingRepo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRouterMeta(t, root, "shop-troubleshooter", "shop", "商城", []discover.ProjectRepository{{Name: "shop-api", LocalPath: shopRepo}})
	writeRouterMeta(t, root, "billing-troubleshooter", "billing", "计费", []discover.ProjectRepository{{Name: "billing-api", LocalPath: billingRepo}})

	result := runProjectRouter(t, root, filepath.Join(shopRepo, "cmd", "api"), "--expect-agent", "shop-validator")
	if result.Status != "matched" || !result.Allowed || result.SystemID != "shop" {
		t.Fatalf("expected shop match: %+v", result)
	}
	if result.Agents[discover.RoleFixer] != "shop-fixer" {
		t.Fatalf("role map missing: %+v", result.Agents)
	}

	wrong := runProjectRouter(t, root, filepath.Join(shopRepo, "cmd", "api"), "--expect-agent", "billing-troubleshooter")
	if wrong.Status != "matched" || wrong.Allowed || wrong.Reason != "expected_agent_belongs_to_another_system" {
		t.Fatalf("cross-system agent must be rejected: %+v", wrong)
	}

	unknown := runProjectRouter(t, root, t.TempDir())
	if unknown.Status != "unmatched" || unknown.Allowed || unknown.Action != "continue_without_troubleshooter" || !unknown.Silent {
		t.Fatalf("unknown project must silently continue without a troubleshooter: %+v", unknown)
	}

	explicit := runProjectRouter(t, root, t.TempDir(), "--system", "计费", "--expect-agent", "billing-fixer")
	if explicit.Status != "matched" || !explicit.Allowed || explicit.SystemID != "billing" {
		t.Fatalf("explicit system should match exact bot: %+v", explicit)
	}

	missingExplicit := runProjectRouter(t, root, t.TempDir(), "--system", "missing-system")
	if missingExplicit.Status != "unmatched" || missingExplicit.Action != "request_system_correction" || missingExplicit.Silent {
		t.Fatalf("an explicitly requested unknown system must remain visible: %+v", missingExplicit)
	}
}

func TestProjectRouterSilentlyBypassesWhenNoBotsAreInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex")
	if err := installProjectRouter(root, TargetCodex); err != nil {
		t.Fatal(err)
	}
	result := runProjectRouter(t, root, t.TempDir())
	if result.Status != "unmatched" || result.Reason != "no_installed_bots" || result.Action != "continue_without_troubleshooter" || !result.Silent {
		t.Fatalf("an installation without business bots must silently bypass: %+v", result)
	}
}

func TestProjectRouterReturnsAmbiguousInsteadOfGuessing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex")
	if err := installProjectRouter(root, TargetCodex); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "src", "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRouterMeta(t, root, "a-troubleshooter", "a", "A", []discover.ProjectRepository{{Name: "shared", LocalPath: shared}})
	writeRouterMeta(t, root, "b-troubleshooter", "b", "B", []discover.ProjectRepository{{Name: "shared", LocalPath: shared}})

	result := runProjectRouter(t, root, shared)
	if result.Status != "ambiguous" || result.Allowed || result.Action != "request_binding_choice" || result.Silent {
		t.Fatalf("equal bindings must be ambiguous: %+v", result)
	}
}

func TestProjectRouterFallsBackToLegacyUserRepoPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex")
	if err := installProjectRouter(root, TargetCodex); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "legacy-shop")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRouterMeta(t, root, "shop-troubleshooter", "shop", "商城", nil)
	configDir := filepath.Join(home, ".tshoot")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"repo_paths_by_system":{"shop":{"shop-api":` + string(mustJSONBytes(t, repo)) + `}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runProjectRouter(t, root, repo)
	if result.Status != "matched" || !result.Allowed || result.SystemID != "shop" {
		t.Fatalf("legacy repo path should match: %+v", result)
	}
}

func mustJSONBytes(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProjectRouterCanonicalizesGitRemote(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex")
	if err := installProjectRouter(root, TargetCodex); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "checkout")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "git@GitHub.com:Acme/Shop.git"}} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeRouterMeta(t, root, "shop-troubleshooter", "shop", "Shop", []discover.ProjectRepository{{Name: "shop", URL: "https://github.com/acme/shop.git"}})

	result := runProjectRouter(t, root, repo)
	if result.Status != "matched" || !result.Allowed || result.SystemID != "shop" {
		t.Fatalf("canonical remote should match: %+v", result)
	}
}

func TestInstallProjectRouterWritesSharedSkillWithoutDiscoverAnchor(t *testing.T) {
	root := t.TempDir()
	if err := installProjectRouter(root, TargetClaudeCode); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"skills/tshoot-router/SKILL.md", "skills/tshoot-router/scripts/resolve.py"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "skills", projectRouterSkillName, discover.MetaFilename)); !os.IsNotExist(err) {
		t.Fatalf("router must not look like a business bot, err=%v", err)
	}
	skill, err := os.ReadFile(filepath.Join(root, "skills", projectRouterSkillName, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(skill)
	for _, required := range []string{"continue_without_troubleshooter", "静默", "继续处理用户原始任务", "不得要求用户提供 system_id"} {
		if !strings.Contains(text, required) {
			t.Fatalf("router skill lacks unmanaged-project passthrough rule %q:\n%s", required, text)
		}
	}
}
