package platform

import (
	"path/filepath"
	"testing"
)

func TestOpenCodeRootUsesXDG(t *testing.T) {
	home := t.TempDir()
	for _, value := range []string{"", "relative", filepath.Join(home, "custom")} {
		t.Setenv("XDG_CONFIG_HOME", value)
		want := filepath.Join(home, ".config", "opencode")
		if filepath.IsAbs(value) {
			want = filepath.Join(value, "opencode")
		}
		if got := OpenCodeRoot(home); got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	}
	for _, target := range []string{"claude-code", "cursor", "codex", "opencode"} {
		if !Supported(target) {
			t.Fatal(target)
		}
	}
	if Supported("openclaw") || Supported("unknown") {
		t.Fatal("unsupported platform accepted")
	}
}
