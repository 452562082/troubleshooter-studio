package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteExportedYAMLRestrictsPermissionsOnNewAndExistingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "troubleshooter.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	want := []byte("system:\n  id: portable\n")
	if err := writeExportedYAML(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("mode = %o, want 600", gotMode)
	}
}
