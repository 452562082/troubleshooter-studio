package aitools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectOpenCodeCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bin := t.TempDir()
	p := filepath.Join(bin, "opencode")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n[ \"$OPENCODE_DISABLE_MODELS_FETCH\" = true ] || exit 1\nprintf '1.2.22\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got := detectOpenCode(ctx)
	if !got.Installed || got.Path != p || got.Version != "1.2.22" {
		t.Fatalf("got=%+v", got)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if detectOpenCode(ctx).Installed {
		t.Fatal("broken CLI accepted")
	}
}
