package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

func TestNativeRedeployRetiresValidators(t *testing.T) {
	for _, platform := range []string{"claude-code", "cursor", "codex"} {
		t.Run(platform, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			target, _ := ParseIDETarget(platform)
			root := target.RootDir(home)
			staging := setupClaudeStaging(t, "base-troubleshooter")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"base-troubleshooter", "base-fixer", "base-validator"} {
				must(os.WriteFile(filepath.Join(staging, "agents", id+target.UserAgentExt()), []byte("name = \""+id+"\"\n"), 0o644))
			}
			meta := discover.Meta{SchemaVersion: 2, SystemID: "base", AgentID: "base-troubleshooter", Role: discover.RoleTroubleshooter, Target: platform, InternalAgents: []discover.InternalAgent{
				{ID: "base-troubleshooter", Role: discover.RoleTroubleshooter}, {ID: "base-validator", Role: discover.RoleValidator}, {ID: "base-fixer", Role: discover.RoleFixer},
			}}
			raw, err := json.Marshal(meta)
			must(err)
			must(os.WriteFile(filepath.Join(staging, discover.MetaFilename), raw, 0o644))
			legacy := []string{filepath.Join("agents", "base-validator"+target.UserAgentExt()), filepath.Join("skills", "base-validator", "SKILL.md"), filepath.Join("scripts", "base-validator", "helper.py")}
			for _, rel := range legacy {
				must(os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
				must(os.WriteFile(filepath.Join(root, rel), []byte("legacy contents"), 0o600))
			}
			unrelated := filepath.Join(root, "agents", "other-validator"+target.UserAgentExt())
			must(os.WriteFile(unrelated, []byte("other bot"), 0o644))
			// Repeated deployment must neither reinstall the retired role nor lose backups.
			must(InstallNative(staging, platform))
			must(InstallNative(staging, platform))
			for _, rel := range legacy {
				if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
					t.Fatalf("retired artifact still active: %s (%v)", rel, err)
				}
				matches, err := filepath.Glob(filepath.Join(root, ".retired-agents", "*", rel))
				must(err)
				if len(matches) != 1 {
					t.Fatalf("want one recoverable backup for %s: %v", rel, matches)
				}
				contents, err := os.ReadFile(matches[0])
				must(err)
				if string(contents) != "legacy contents" {
					t.Fatal("backup contents changed")
				}
			}
			for _, id := range []string{"base-troubleshooter", "base-fixer", "other-validator"} {
				_, err := os.Stat(filepath.Join(root, "agents", id+target.UserAgentExt()))
				must(err)
			}
			raw, err = os.ReadFile(filepath.Join(root, "skills", "base-troubleshooter", discover.MetaFilename))
			must(err)
			must(json.Unmarshal(raw, &meta))
			if len(meta.InternalAgents) != 2 {
				t.Fatalf("wrong active agents: %+v", meta.InternalAgents)
			}
		})
	}
}

func TestCodexConfigExcludesRetiredValidator(t *testing.T) {
	cfg := &config.SystemConfig{}
	cfg.System.ID = "base"
	cfg.Agent.ID = "base-troubleshooter"
	if got := codexAgentNamesForConfig(cfg); !reflect.DeepEqual(got, []string{"base-troubleshooter", "base-fixer"}) {
		t.Fatalf("runtime agents: %v", got)
	}
}
