package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 场景 1：手改 config-map 的 inferred 行 → 重 gen → 保留
func TestPreserve_ConfigMapManualOverride(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("first gen: %v", err)
	}

	cmPath := filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml")
	// 手改：把 order-worker/dev 从 inferred 改为具体 verified 行（不带 source 字段）
	original := readFile(t, cmPath)
	mutated := strings.Replace(original,
		"      order-worker:\n        namespaceId: \"dev\"\n        group: \"DEFAULT_GROUP\"\n        dataId: \"{service}.yaml\"\n        mcp_server: \"shop-nacos-dev\"\n        status: inferred",
		"      order-worker:\n        namespaceId: \"shop-dev\"\n        group: \"SHOP_WORKER\"\n        dataId: \"order-worker.yaml\"\n        status: verified",
		1)
	if mutated == original {
		t.Fatalf("failed to locate inferred block to mutate:\n%s", original)
	}
	if err := os.WriteFile(cmPath, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}

	// 再 gen
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("re-gen: %v", err)
	}

	rows := loadConfigMap(t, cmPath)
	row := rows["dev"]["order-worker"]
	if row["status"] != "verified" {
		t.Errorf("after re-gen, order-worker/dev should still be verified, got %v", row["status"])
	}
	if row["dataId"] != "order-worker.yaml" {
		t.Errorf("manual dataId should be preserved, got %v", row["dataId"])
	}
	if row["group"] != "SHOP_WORKER" {
		t.Errorf("manual group should be preserved, got %v", row["group"])
	}
}

// 场景 2：手改 SOUL.md → 重 gen → 被模板覆盖(整文件 preserve 已删,模板更新优先)
func TestPreserve_TemplateDerivedFileOverwritten(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}
	soulPath := filepath.Join(out, "templates/workspace-template/SOUL.md")
	if err := os.WriteFile(soulPath, []byte("custom soul\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, soulPath); got == "custom soul\n" {
		t.Error("SOUL.md should be overwritten by template, but kept user edit")
	}
}

// 场景 3：analyzer finding 与人工 override 冲突 → analyzer 胜
func TestPreserve_AnalyzerWinsOverPriorOverride(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	// 第一次：无 analysis，order-service/dev 会是 inferred
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}

	// 手改：order-service/dev 写个假值
	cmPath := filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml")
	orig := readFile(t, cmPath)
	mut := strings.Replace(orig,
		"      order-service:\n        namespaceId: \"dev\"\n        group: \"DEFAULT_GROUP\"\n        dataId: \"{service}.yaml\"\n        mcp_server: \"shop-nacos-dev\"\n        status: inferred",
		"      order-service:\n        namespaceId: \"MANUAL_NS\"\n        group: \"MANUAL_GROUP\"\n        dataId: \"manual.yaml\"\n        status: verified",
		1)
	if mut == orig {
		t.Fatalf("could not find inferred order-service block")
	}
	if err := os.WriteFile(cmPath, []byte(mut), 0o644); err != nil {
		t.Fatal(err)
	}

	// 第二次：带 analysis（真的 fake-repos 有 order-service/SHOP_ORDER/shop-dev）→ analyzer 应覆盖 manual
	analysisJSON := `{
  "schema_version": "0.1",
  "config_center": "nacos",
  "repos": [{
    "name": "order-service",
    "stack": "go",
    "service_names": ["order-service"],
    "findings": [{
      "config_center": "nacos",
      "source_file": "config/bootstrap.yaml",
      "data_id": "order-service.yaml",
      "group": "SHOP_ORDER",
      "namespace_id": "shop-dev"
    }],
    "verified": true
  }]
}`
	ap := filepath.Join(t.TempDir(), "analysis.json")
	if err := os.WriteFile(ap, []byte(analysisJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(cfg, tr, out)
	if err := g.LoadAnalysis(ap); err != nil {
		t.Fatal(err)
	}
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}

	rows := loadConfigMap(t, cmPath)
	row := rows["dev"]["order-service"]
	if row["dataId"] != "order-service.yaml" {
		t.Errorf("analyzer should win: expected dataId=order-service.yaml, got %v", row["dataId"])
	}
	if row["group"] != "SHOP_ORDER" {
		t.Errorf("analyzer should win: expected group=SHOP_ORDER, got %v", row["group"])
	}
}

// 场景 4：config_center 切换（nacos→apollo）→ 不继承 prior overrides
func TestPreserve_ConfigCenterSwitch_DropsOverrides(t *testing.T) {
	nacosCfg := loadCfg(t, "examples/shop-system.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	if err := New(nacosCfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}
	cmPath := filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml")
	orig := readFile(t, cmPath)
	mut := strings.Replace(orig,
		"status: inferred",
		"status: verified",
		1)
	if err := os.WriteFile(cmPath, []byte(mut), 0o644); err != nil {
		t.Fatal(err)
	}

	// 切换到 apollo（用同 output 目录，模拟人为切换 system.yaml）
	apolloCfg := loadCfg(t, "examples/apollo-system.yaml")
	g := New(apolloCfg, tr, out)
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	// 应该只输出 apollo 字段，prior override 不应混进来
	newCM := readFile(t, cmPath)
	if !strings.Contains(newCM, "config_center: apollo") {
		t.Errorf("expected apollo header after switch")
	}
	if strings.Contains(newCM, "group: \"DEFAULT_GROUP\"") {
		t.Errorf("nacos-style fields leaked into apollo output:\n%s", newCM)
	}
}
