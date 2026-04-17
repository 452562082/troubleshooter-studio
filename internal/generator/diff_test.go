package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiff_NoChange(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}
	rep, err := Diff(out, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 0 {
		t.Errorf("expected no file changes when diffing against self, got %d", len(rep.Files))
	}
	if len(rep.ConfigMapChanges) != 0 {
		t.Errorf("expected no row changes, got %d", len(rep.ConfigMapChanges))
	}
}

func TestDiff_DetectsFileAndRowChanges(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	tr := filepath.Join(projectRoot(t), "templates")

	oldDir := t.TempDir()
	newDir := t.TempDir()
	if err := New(cfg, tr, oldDir).Generate(); err != nil {
		t.Fatal(err)
	}
	if err := New(cfg, tr, newDir).Generate(); err != nil {
		t.Fatal(err)
	}

	// 改动 new 的 SOUL.md + config-map 一行
	soul := filepath.Join(newDir, "templates/workspace-template/SOUL.md")
	if err := os.WriteFile(soul, []byte("different soul\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cm := filepath.Join(newDir, "templates/workspace-template/skills/routing/references/config-map.yaml")
	orig := readFile(t, cm)
	mut := strings.Replace(orig,
		"      order-worker:\n        namespaceId: \"dev\"\n        group: \"DEFAULT_GROUP\"\n        dataId: \"{service}.yaml\"\n        mcp_server: \"nacos-mcp-server-dev\"\n        status: inferred",
		"      order-worker:\n        namespaceId: \"shop-dev\"\n        group: \"FIXED\"\n        dataId: \"fixed.yaml\"\n        status: verified",
		1)
	if err := os.WriteFile(cm, []byte(mut), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Diff(oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	// 至少有 2 个文件变更（SOUL.md + config-map.yaml）
	if len(rep.Files) < 2 {
		t.Errorf("expected >=2 file changes, got %d: %+v", len(rep.Files), rep.Files)
	}

	// 必须有 order-worker/dev 的 status-change 记录
	found := false
	for _, r := range rep.ConfigMapChanges {
		if r.Env == "dev" && r.Service == "order-worker" && r.Kind == "status-change" {
			found = true
			if r.OldStatus != "inferred" || r.NewStatus != "verified" {
				t.Errorf("status direction wrong: %v → %v", r.OldStatus, r.NewStatus)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected order-worker/dev status-change in config-map changes: %+v", rep.ConfigMapChanges)
	}
}

func TestDiff_RowRemoved(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	tr := filepath.Join(projectRoot(t), "templates")
	oldDir := t.TempDir()
	newDir := t.TempDir()
	if err := New(cfg, tr, oldDir).Generate(); err != nil {
		t.Fatal(err)
	}
	// new: 只有 dev env，构造人工 config-map
	if err := New(cfg, tr, newDir).Generate(); err != nil {
		t.Fatal(err)
	}
	cm := filepath.Join(newDir, "templates/workspace-template/skills/routing/references/config-map.yaml")
	trimmed := `config_center: nacos
environments:
  dev:
      order-service:
        namespaceId: "shop-dev"
        group: "SHOP_ORDER"
        dataId: "order-service.yaml"
        status: verified
`
	if err := os.WriteFile(cm, []byte(trimmed), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Diff(oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, r := range rep.ConfigMapChanges {
		if r.Kind == "removed" {
			removed++
		}
	}
	if removed == 0 {
		t.Errorf("expected removed rows, got 0. changes: %+v", rep.ConfigMapChanges)
	}
}
