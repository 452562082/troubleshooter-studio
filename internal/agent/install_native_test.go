// install_native_test.go —— 验证 claude-code / cursor 的 InstallNative
// 把 staging 拷到 ~/.claude|cursor/{agents,skills,scripts}/<name>/ 的逻辑。
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupClaudeStaging 造一个 claude-code/cursor 共用的最小 staging:
//   - <staging>/agents/<name>.md
//   - <staging>/skills/example-skill/SKILL.md
//   - <staging>/scripts/helper.py
func setupClaudeStaging(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "agents", name+".md"), []byte("---\nname: "+name+"\n---\n"), 0o644))

	must(os.MkdirAll(filepath.Join(dir, "skills", "example-skill"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "skills", "example-skill", "SKILL.md"), []byte("# example skill\n"), 0o644))

	must(os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "scripts", "helper.py"), []byte("# helper\n"), 0o644))
	return dir
}

func TestInstallNative_ClaudeCode(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := setupClaudeStaging(t, "shop-bot")

	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}

	// agent .md 装到 ~/.claude/agents/
	agentMD := filepath.Join(fakeHome, ".claude", "agents", "shop-bot.md")
	body, err := os.ReadFile(agentMD)
	if err != nil {
		t.Fatalf("agent .md missing: %v", err)
	}
	if !strings.Contains(string(body), "name: shop-bot") {
		t.Errorf("agent body wrong: %s", body)
	}

	// skills 和 scripts 各自装到命名空间子目录
	skillMD := filepath.Join(fakeHome, ".claude", "skills", "shop-bot", "example-skill", "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		t.Errorf("skills 没装到 namespaced 目录: %v", err)
	}
	scriptFile := filepath.Join(fakeHome, ".claude", "scripts", "shop-bot", "helper.py")
	if _, err := os.Stat(scriptFile); err != nil {
		t.Errorf("scripts 没装到 namespaced 目录: %v", err)
	}
	routerSkill := filepath.Join(fakeHome, ".claude", "skills", projectRouterSkillName, "SKILL.md")
	routerBody, err := os.ReadFile(routerSkill)
	if err != nil {
		t.Fatalf("shared project router missing: %v", err)
	}
	if !strings.Contains(string(routerBody), filepath.ToSlash(filepath.Join(fakeHome, ".claude", "skills", projectRouterSkillName, "scripts", "resolve.py"))) {
		t.Fatalf("router does not reference installed resolver: %s", routerBody)
	}
}

func TestUninstallNativeKeepsSharedProjectRouter(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := setupClaudeStaging(t, "shop-bot")
	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(fakeHome, ".claude", "skills", "shop-bot")
	if _, err := UninstallNative(installed, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "skills", projectRouterSkillName, "SKILL.md")); err != nil {
		t.Fatalf("uninstalling one bot removed shared router: %v", err)
	}
}

func TestInstallNative_Cursor(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := setupClaudeStaging(t, "shop-bot")

	if err := InstallNative(staging, "cursor"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(fakeHome, ".cursor", "agents", "shop-bot.md")); err != nil {
		t.Errorf("cursor agent .md 没装: %v", err)
	}
}

func TestInstallNative_InstallsMultipleClaudeAgents(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(staging, "agents"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents", "shop-troubleshooter.md"), []byte("---\nname: shop-troubleshooter\n---\n"), 0o644))
	must(os.WriteFile(filepath.Join(staging, "agents", "shop-validator.md"), []byte("---\nname: shop-validator\n---\n"), 0o644))
	for _, skill := range []string{
		"api-verifier",
		"attachment-evidence-verifier",
		"bug-verifier",
		"frontend-repro-investigator",
		"incident-investigator",
		"postgresql-runtime-query",
		"recent-changes",
	} {
		must(os.MkdirAll(filepath.Join(staging, "skills", skill), 0o755))
		must(os.WriteFile(filepath.Join(staging, "skills", skill, "SKILL.md"), []byte("# "+skill+"\n"), 0o644))
	}
	must(os.MkdirAll(filepath.Join(staging, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "scripts", "helper.py"), []byte("# helper\n"), 0o644))
	must(os.WriteFile(filepath.Join(staging, "tshoot.json"), []byte(`{"schema_version":1,"system_id":"shop","target":"claude-code","agent_id":"shop-troubleshooter","role":"troubleshooter","internal_agents":[{"id":"shop-troubleshooter","role":"troubleshooter"},{"id":"shop-validator","role":"validator"}]}`), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "agents-meta", "shop-troubleshooter"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "shop-troubleshooter", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"shop","target":"claude-code","agent_id":"shop-troubleshooter","role":"troubleshooter"}`), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "agents-meta", "shop-validator"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "shop-validator", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"shop","target":"claude-code","agent_id":"shop-validator","role":"validator"}`), 0o644))

	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"shop-troubleshooter", "shop-validator"} {
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "agents", name+".md")); err != nil {
			t.Fatalf("%s agent not installed: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "scripts", name, "helper.py")); err != nil {
			t.Fatalf("%s scripts not installed: %v", name, err)
		}
		metaPath := filepath.Join(fakeHome, ".claude", "skills", name, "tshoot.json")
		body, err := os.ReadFile(metaPath)
		if name == "shop-troubleshooter" {
			if err != nil {
				t.Fatalf("%s meta missing: %v", name, err)
			}
			if !strings.Contains(string(body), `"agent_id":"shop-troubleshooter"`) || !strings.Contains(string(body), `"id":"shop-validator"`) {
				t.Fatalf("%s meta wrong: %s", name, body)
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("%s internal agent should not have discover meta, err=%v body=%s", name, err, body)
		}
	}
	assertExists := func(rel string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "skills", rel, "SKILL.md")); err != nil {
			t.Fatalf("expected skill %s: %v", rel, err)
		}
	}
	assertMissing := func(rel string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "skills", rel, "SKILL.md")); !os.IsNotExist(err) {
			t.Fatalf("skill %s should be absent, err=%v", rel, err)
		}
	}
	assertExists("shop-troubleshooter/incident-investigator")
	assertExists("shop-troubleshooter/recent-changes")
	assertExists("shop-troubleshooter/frontend-repro-investigator")
	assertMissing("shop-troubleshooter/api-verifier")
	assertMissing("shop-troubleshooter/attachment-evidence-verifier")
	assertMissing("shop-troubleshooter/bug-verifier")

	assertExists("shop-validator/api-verifier")
	assertExists("shop-validator/attachment-evidence-verifier")
	assertExists("shop-validator/bug-verifier")
	assertExists("shop-validator/frontend-repro-investigator")
	assertExists("shop-validator/postgresql-runtime-query")
	assertMissing("shop-validator/incident-investigator")
	assertMissing("shop-validator/recent-changes")
}

func TestInstallNative_PrimaryAnchorUsesTroubleshooterWhenRootMetaIsLegacy(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(staging, "agents"), 0o755))
	for _, name := range []string{"base-fixer", "base-troubleshooter", "base-validator"} {
		must(os.WriteFile(filepath.Join(staging, "agents", name+".toml"), []byte("name = \""+name+"\"\n"), 0o644))
		must(os.MkdirAll(filepath.Join(staging, "agents-meta", name), 0o755))
	}
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "base-fixer", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"base","target":"codex","agent_id":"base-fixer","role":"fixer"}`), 0o644))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "base-troubleshooter", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"base","target":"codex","agent_id":"base-troubleshooter","role":"troubleshooter"}`), 0o644))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "base-validator", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"base","target":"codex","agent_id":"base-validator","role":"validator"}`), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "skills", "incident-investigator"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "skills", "incident-investigator", "SKILL.md"), []byte("# incident\n"), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "skills", "bug-fixer"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "skills", "bug-fixer", "SKILL.md"), []byte("# fix\n"), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "tshoot.json"), []byte(`{"schema_version":1,"system_id":"base","system_name":"Base","target":"codex"}`), 0o644))

	if err := InstallNative(staging, "codex"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(fakeHome, ".codex", "skills", "base-troubleshooter", "tshoot.json")); err != nil {
		t.Fatalf("troubleshooter meta missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fakeHome, ".codex", "skills", "base-fixer", "tshoot.json")); !os.IsNotExist(err) {
		t.Fatalf("fixer should not expose discover meta, err=%v", err)
	}
}

func TestInstallNative_BackupExistingAgent(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := setupClaudeStaging(t, "shop-bot")

	// 先装一次 → 再装一次,第一次的 agent .md 应被备份成 .bak.<ts>
	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}
	// 修改 staging 的 agent 文本,模拟"用户重新 gen 了一份新 agent"
	if err := os.WriteFile(filepath.Join(staging, "agents", "shop-bot.md"),
		[]byte("---\nname: shop-bot\nversion: v2\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}

	// 当前活的应该是 v2
	agentMD := filepath.Join(fakeHome, ".claude", "agents", "shop-bot.md")
	body, _ := os.ReadFile(agentMD)
	if !strings.Contains(string(body), "version: v2") {
		t.Errorf("活的 agent.md 应被覆盖为 v2, got: %s", body)
	}
	// 同目录应有一个 .bak.<ts> 备份
	files, _ := os.ReadDir(filepath.Join(fakeHome, ".claude", "agents"))
	hasBak := false
	for _, f := range files {
		if strings.Contains(f.Name(), ".bak.") {
			hasBak = true
			break
		}
	}
	if !hasBak {
		t.Errorf("旧 agent .md 应被备份成 .bak.<ts>, 但没找到")
	}
}

func TestInstallNative_UnsupportedTarget(t *testing.T) {
	if err := InstallNative(t.TempDir(), "openclaw"); err == nil {
		t.Errorf("InstallNative 不支持 openclaw,应返回错误")
	}
}

func TestInstallNative_MissingAgentsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir() // 空 staging,没有 agents/
	if err := InstallNative(dir, "claude-code"); err == nil {
		t.Errorf("staging 缺 agents/ 应返回错误")
	}
}
