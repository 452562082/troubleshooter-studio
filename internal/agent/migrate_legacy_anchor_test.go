package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyAnchorsMigratesCodexTomlPrimaryAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	staging := filepath.Join(home, ".tshoot", "codex", "base")
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
	must(os.WriteFile(filepath.Join(staging, "tshoot.json"), []byte(`{"schema_version":1,"system_id":"base","target":"codex"}`), 0o644))
	must(os.MkdirAll(filepath.Join(home, ".codex", "agents"), 0o755))
	must(os.MkdirAll(filepath.Join(home, ".codex", "skills", "base-troubleshooter"), 0o755))
	must(os.WriteFile(filepath.Join(home, ".codex", "agents", "base-troubleshooter.toml"), []byte("name = \"base-troubleshooter\"\n"), 0o644))

	if got := MigrateLegacyAnchors(); got != 1 {
		t.Fatalf("MigrateLegacyAnchors migrated %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "base-troubleshooter", "tshoot.json")); err != nil {
		t.Fatalf("codex primary meta not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "base-fixer", "tshoot.json")); !os.IsNotExist(err) {
		t.Fatalf("fixer should not get migrated discover meta, err=%v", err)
	}
}
