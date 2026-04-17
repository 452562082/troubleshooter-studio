package skillscaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew_MinimalScaffold(t *testing.T) {
	tr := t.TempDir()
	dst, err := New(Options{TemplateRoot: tr, Name: "my-skill"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := filepath.Join(tr, "workspace", "skills", "my-skill")
	if dst != want {
		t.Errorf("dst: got %q, want %q", dst, want)
	}
	data, err := os.ReadFile(filepath.Join(dst, "SKILL.md.tmpl"))
	if err != nil {
		t.Fatalf("read SKILL.md.tmpl: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: my-skill") {
		t.Error("SKILL.md.tmpl should contain name front matter")
	}
	if !strings.Contains(content, "description: TODO") {
		t.Error("SKILL.md.tmpl should contain TODO description by default")
	}
	// 默认不创建 scripts/refs
	if _, err := os.Stat(filepath.Join(dst, "scripts")); err == nil {
		t.Error("scripts dir should not exist by default")
	}
	if _, err := os.Stat(filepath.Join(dst, "references")); err == nil {
		t.Error("references dir should not exist by default")
	}
}

func TestNew_WithDescriptionAndDirs(t *testing.T) {
	tr := t.TempDir()
	dst, err := New(Options{
		TemplateRoot: tr,
		Name:         "x-runtime",
		Description:  "这是自定义描述",
		WithScripts:  true,
		WithRefs:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dst, "SKILL.md.tmpl"))
	if !strings.Contains(string(data), "description: 这是自定义描述") {
		t.Error("custom description should be in SKILL.md.tmpl")
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts", "README.md")); err != nil {
		t.Errorf("scripts/README.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "references", "README.md")); err != nil {
		t.Errorf("references/README.md missing: %v", err)
	}
}

func TestNew_RejectExisting(t *testing.T) {
	tr := t.TempDir()
	if _, err := New(Options{TemplateRoot: tr, Name: "dup"}); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{TemplateRoot: tr, Name: "dup"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second call should fail with 'already exists', got %v", err)
	}
}

func TestNew_ValidateName(t *testing.T) {
	tr := t.TempDir()
	bad := []string{"", "Upper", "has space", "1bad-start", "bad_underscore"}
	for _, n := range bad {
		if _, err := New(Options{TemplateRoot: tr, Name: n}); err == nil {
			t.Errorf("expected error for invalid name %q", n)
		}
	}
	good := []string{"a", "a-b", "x-runtime-query"}
	for _, n := range good {
		if _, err := New(Options{TemplateRoot: tr, Name: n}); err != nil {
			t.Errorf("expected success for name %q, got %v", n, err)
		}
	}
}

func TestNew_RequiresTemplateRoot(t *testing.T) {
	_, err := New(Options{Name: "x"})
	if err == nil {
		t.Error("expected error when TemplateRoot empty")
	}
}
