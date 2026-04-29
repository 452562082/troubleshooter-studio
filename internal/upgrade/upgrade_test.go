package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func loadShop(t *testing.T) *config.SystemConfig {
	t.Helper()
	cfg, err := config.Load(filepath.Join(projectRoot(t), "examples", "shop-system.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestRun_FailsWithoutExistingOutput(t *testing.T) {
	cfg := loadShop(t)
	tr := filepath.Join(projectRoot(t), "templates")
	_, err := Run(Options{
		Config:       cfg,
		TemplateRoot: tr,
		OutputDir:    "/definitely/not/existing/dir/xyz",
	})
	if err == nil || !strings.Contains(err.Error(), "existing output not found") {
		t.Errorf("expected existing-not-found error, got %v", err)
	}
}

func TestRun_BackupAndPreserve(t *testing.T) {
	cfg := loadShop(t)
	tr := filepath.Join(projectRoot(t), "templates")

	// 1) 初次 gen 作为 existing
	baseDir := t.TempDir()
	existing := filepath.Join(baseDir, "dist")
	g := generator.New(cfg, tr, existing)
	if err := g.Generate(); err != nil {
		t.Fatalf("initial gen: %v", err)
	}

	// 2) 手动改 SOUL.md，证明后续 upgrade 仍保留
	soulPath := filepath.Join(existing, "templates/workspace-template/SOUL.md")
	if err := os.WriteFile(soulPath, []byte("custom-soul-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 手动改 config-map：把 order-worker/dev 从 inferred → verified（无 source）
	cmPath := filepath.Join(existing, "templates/workspace-template/skills/routing/references/config-map.yaml")
	data, _ := os.ReadFile(cmPath)
	mut := strings.Replace(string(data),
		"      order-worker:\n        namespaceId: \"dev\"\n        group: \"DEFAULT_GROUP\"\n        dataId: \"{service}.yaml\"\n        mcp_server: \"shop-bot-nacos-mcp-server-dev\"\n        status: inferred",
		"      order-worker:\n        namespaceId: \"shop-dev\"\n        group: \"WORKER\"\n        dataId: \"worker.yaml\"\n        status: verified",
		1)
	if mut == string(data) {
		t.Fatal("mutation not applied")
	}
	if err := os.WriteFile(cmPath, []byte(mut), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3) upgrade
	res, err := Run(Options{
		Config:       cfg,
		TemplateRoot: tr,
		OutputDir:    existing,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 4) 断言
	if res.BackupPath == "" {
		t.Error("BackupPath should be set")
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Errorf("backup dir should exist: %v", err)
	}
	if !strings.HasPrefix(res.BackupPath, existing+".bak.") {
		t.Errorf("backup path format wrong: %s", res.BackupPath)
	}

	// SOUL 应仍保留
	if got, _ := os.ReadFile(soulPath); string(got) != "custom-soul-marker\n" {
		t.Errorf("SOUL.md not preserved after upgrade, got: %s", got)
	}
	// config-map 的 order-worker/dev 应 verified
	newCM, _ := os.ReadFile(cmPath)
	if !strings.Contains(string(newCM), "dataId: \"worker.yaml\"") {
		t.Error("order-worker/dev manual override not preserved")
	}

	if res.GenSummary == nil {
		t.Fatal("GenSummary missing")
	}
	if res.GenSummary.PreservedCount < 1 {
		t.Errorf("expected PreservedCount >= 1, got %d", res.GenSummary.PreservedCount)
	}
	if res.GenSummary.PriorOverridesCount != 1 {
		t.Errorf("expected 1 prior override, got %d", res.GenSummary.PriorOverridesCount)
	}
	// schema 无变动
	if res.SchemaFrom != res.SchemaTo {
		t.Errorf("schema from/to should be equal, got %s → %s", res.SchemaFrom, res.SchemaTo)
	}
}

func TestRun_SchemaMismatchWarns(t *testing.T) {
	cfg := loadShop(t)
	cfg.Meta.SchemaVersion = "0.999"
	tr := filepath.Join(projectRoot(t), "templates")
	baseDir := t.TempDir()
	existing := filepath.Join(baseDir, "dist")
	g := generator.New(cfg, tr, existing)
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Options{
		Config:       cfg,
		TemplateRoot: tr,
		OutputDir:    existing,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SchemaMigrated {
		t.Error("should flag SchemaMigrated when version differs")
	}
	if len(res.Warnings) == 0 {
		t.Error("should emit warning for schema mismatch")
	}
}
