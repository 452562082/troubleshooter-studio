package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExamplesValidate(t *testing.T) {
	root := projectRoot(t)
	examples, err := filepath.Glob(filepath.Join(root, "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(examples) == 0 {
		t.Fatal("no examples/*.yaml files found")
	}
	for _, path := range examples {
		path := path
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			if _, err := Load(path); err != nil {
				t.Fatalf("example %s should validate: %v", path, err)
			}
		})
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		next := filepath.Dir(wd)
		if next == wd {
			t.Fatal("project root not found")
		}
		wd = next
	}
}
