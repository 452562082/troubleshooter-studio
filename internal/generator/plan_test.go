package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 首次 plan：existingDir 不存在 → 全部文件都应为 create
func TestBuildPlan_FirstTime(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, "")
	plan, err := g.BuildPlan("/tmp/nonexistent-dir-for-plan-test")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.System != "shop" {
		t.Errorf("System: got %q", plan.System)
	}
	if plan.ConfigCenter != "nacos" {
		t.Errorf("ConfigCenter: got %q", plan.ConfigCenter)
	}
	if len(plan.FilesCreate) == 0 {
		t.Error("expected files to create on first-time plan")
	}
	if len(plan.FilesModify) != 0 || len(plan.FilesRemove) != 0 {
		t.Errorf("first-time plan should have 0 modify/remove; got %d / %d",
			len(plan.FilesModify), len(plan.FilesRemove))
	}
	if len(plan.Preserved) != 0 {
		t.Errorf("no existing dir → no preserved files; got %d", len(plan.Preserved))
	}
	if len(plan.PriorOverrides) != 0 {
		t.Errorf("no existing dir → no prior overrides; got %d", len(plan.PriorOverrides))
	}
	if plan.ConfigMap.Total == 0 {
		t.Error("config-map projection total should be > 0")
	}
	// shop 示例现在有 10 个 skills（含 tracing-query + elk-log-query）
	if len(plan.SkillsIncluded) < 8 {
		t.Errorf("SkillsIncluded: expected >= 8, got %d: %+v", len(plan.SkillsIncluded), plan.SkillsIncluded)
	}
}

func TestBuildPlan_SkippedSkills(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	// 把 whitelist 改小，让一些被 skip
	cfg.Generation.SkillsWhitelist = []string{"routing", "diagram-generator"}
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, "")
	plan, err := g.BuildPlan("/tmp/nonexistent-plan-skip")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SkillsIncluded) != 2 {
		t.Errorf("expected 2 included skills, got %d", len(plan.SkillsIncluded))
	}
	if len(plan.SkillsSkipped) == 0 {
		t.Error("expected some skills to be skipped")
	}
	for _, s := range plan.SkillsSkipped {
		if !strings.Contains(s.Reason, "skills_whitelist") {
			t.Errorf("skill %s should be skipped for whitelist reason, got %q", s.Name, s.Reason)
		}
	}
}

func TestBuildPlan_WithPriorOverridesAndPreserved(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-system.yaml")
	tr := filepath.Join(projectRoot(t), "templates")

	// 先生成一份 baseline 作为 existing
	existing := t.TempDir()
	if err := New(cfg, tr, existing).Generate(); err != nil {
		t.Fatal(err)
	}

	// 手改 config-map：把 order-worker/dev 从 inferred 改成 verified（无 source）→ 应被捕获为 prior override
	cmPath := filepath.Join(existing, "templates/workspace-template/skills/routing/references/config-map.yaml")
	orig := readFile(t, cmPath)
	mut := strings.Replace(orig,
		"      order-worker:\n        namespaceId: \"dev\"\n        group: \"DEFAULT_GROUP\"\n        dataId: \"{service}.yaml\"\n        mcp_server: \"shop-nacos-mcp-server-dev\"\n        status: inferred",
		"      order-worker:\n        namespaceId: \"shop-dev\"\n        group: \"WORKER\"\n        dataId: \"worker.yaml\"\n        mcp_server: \"shop-nacos-mcp-server-dev\"\n        status: verified",
		1)
	if mut == orig {
		t.Fatalf("could not mutate config-map")
	}
	if err := os.WriteFile(cmPath, []byte(mut), 0o644); err != nil {
		t.Fatal(err)
	}

	// 手改 SOUL.md（在 preserve_on_regenerate 里）
	soulPath := filepath.Join(existing, "templates/workspace-template/SOUL.md")
	if err := os.WriteFile(soulPath, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// plan 应识别出 1 prior override + 3 preserved
	g := New(cfg, tr, existing)
	plan, err := g.BuildPlan(existing)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PriorOverrides) != 1 {
		t.Errorf("expected 1 prior override, got %d: %+v", len(plan.PriorOverrides), plan.PriorOverrides)
	}
	if plan.PriorOverrides[0].Service != "order-worker" || plan.PriorOverrides[0].Env != "dev" {
		t.Errorf("prior override identity wrong: %+v", plan.PriorOverrides[0])
	}
	// preserve_on_regenerate 有 SOUL.md/USER.md/CHECKLIST.md 三项
	if len(plan.Preserved) != 3 {
		t.Errorf("expected 3 preserved files, got %d: %v", len(plan.Preserved), plan.Preserved)
	}
	// 有 prior override 被应用后，files 应为 0 变动（plan 等价 gen + preserve）
	if len(plan.FilesCreate) != 0 || len(plan.FilesModify) != 0 || len(plan.FilesRemove) != 0 {
		t.Errorf("with preserve, plan should be no-op; got C%d M%d R%d",
			len(plan.FilesCreate), len(plan.FilesModify), len(plan.FilesRemove))
	}
	// projection 应含 1 verified(prior)
	if plan.ConfigMap.VerifiedFromPrior != 1 {
		t.Errorf("expected 1 verified(prior), got %d", plan.ConfigMap.VerifiedFromPrior)
	}
}

func TestBuildPlan_Apollo(t *testing.T) {
	cfg := loadCfg(t, "examples/apollo-system.yaml")
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, "")
	plan, err := g.BuildPlan("")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ConfigCenter != "apollo" {
		t.Errorf("ConfigCenter: got %q", plan.ConfigCenter)
	}
	if plan.ConfigMap.Total == 0 {
		t.Error("expected projection rows for apollo example")
	}
}
