package generator

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateOpenCodeRoles(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	cfg.Agent.TargetModels = map[string]string{"opencode": "provider/model"}
	out := filepath.Join(t.TempDir(), "bot")
	g := New(cfg, filepath.Join(projectRoot(t), "templates"), out)
	if err := g.GenerateTarget("opencode"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []struct{ name, role string }{{"shop-bot", "troubleshooter"}, {"shop-fixer", "fixer"}} {
		data, err := os.ReadFile(filepath.Join(out+"-opencode", "agents", role.name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		var front map[string]any
		parts := strings.SplitN(string(data), "---", 3)
		if len(parts) != 3 {
			t.Fatal("missing frontmatter")
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &front); err != nil {
			t.Fatal(err)
		}
		if front["mode"] != "all" || front["model"] != "provider/model" {
			t.Fatal(front)
		}
		if !strings.Contains(string(data), OpenCodeSkillsRoot+"/"+role.name) {
			t.Fatal("role skills not scoped")
		}
		assertAgentMetaRole(t, filepath.Join(out+"-opencode", "agents-meta", role.name, "tshoot.json"), role.name, role.role)
	}
	files, err := os.ReadDir(filepath.Join(out+"-opencode", "agents"))
	if err != nil || len(files) != 2 {
		t.Fatalf("agents=%v err=%v", files, err)
	}
	assertExists(t, out+"-opencode", []string{"skills/routing/SKILL.md", "skills/incident-investigator/SKILL.md", "skills/bug-fixer/SKILL.md"})
	if _, err := os.Stat(filepath.Join(out+"-opencode", "scripts/test_nacos_mcp.py")); err == nil {
		t.Fatal("test script deployed")
	}
}
