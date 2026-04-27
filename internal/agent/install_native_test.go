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
