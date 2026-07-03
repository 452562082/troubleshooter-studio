package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunAnalyzeWithContextReturnsCanceled(t *testing.T) {
	root := t.TempDir()
	yamlPath := filepath.Join(root, "troubleshooter.yaml")
	outPath := filepath.Join(root, "analysis.json")
	reposRoot := filepath.Join(root, "repos")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte(`
meta:
  schema_version: "2026-05"
system:
  id: "shop"
  name: "Shop"
agent:
  name: "shop-troubleshooter"
  model: "gpt-5"
  targets: ["openclaw"]
environments:
  - id: "dev"
    api_domain: "https://api.dev.example.com"
repos:
  - name: "order-service"
    url: "https://example.com/shop/order-service.git"
    role: "backend"
    stack: "go"
infrastructure:
  config_center:
    type: "nacos"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runAnalyzeWithContext(ctx, []string{
		"-i", yamlPath,
		"--repos-root", reposRoot,
		"-o", outPath,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("analysis output should not be written after cancellation, stat err=%v", statErr)
	}
}
