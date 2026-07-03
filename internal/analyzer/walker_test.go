package analyzer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkFilesContextStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	files, err := walkFilesContext(ctx, root, nil, func(string) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(files) != 0 {
		t.Fatalf("files should be empty after pre-cancelled context: %#v", files)
	}
}
