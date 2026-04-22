package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeMeta 在 dir 下写一份合法 tshoot.json,便于构造测试用例。
func writeMeta(t *testing.T, dir string, m Meta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, MetaFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanEmptyReturnsEmptySliceNotNil(t *testing.T) {
	// 复现之前 null.length 崩页的 regression:Scan 在没找到任何 agent 时
	// 必须返回 []DiscoveredAgent{} 而不是 nil slice,否则 JSON 编码出 null,
	// 跨 Wails binding 到 JS 端访问 .length 会 TypeError。
	got, err := Scan([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Scan 返回了 nil slice,JSON 编码会变 null,前端 .length 会崩")
	}
	if len(got) != 0 {
		t.Errorf("空扫描结果期望 len=0,得到 %d", len(got))
	}
	// JSON 形状必须是 [] 不是 null
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Errorf("JSON 形状错误:want [],got %s", string(b))
	}
}

func TestScanFindsAgentInRoot(t *testing.T) {
	root := t.TempDir()
	writeMeta(t, filepath.Join(root, "shop-bot"), Meta{
		SchemaVersion: 1,
		SystemID:      "shop",
		SystemName:    "Shop",
		Target:        "openclaw",
		SystemYAML: `system:
  id: shop
environments:
  - id: dev
  - id: prod
repos:
  - name: order
generation:
  skills_whitelist: [routing, config-executor]
  targets: [openclaw]
`,
	})

	agents, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	a := agents[0]
	if a.Meta.SystemID != "shop" || a.Meta.Target != "openclaw" {
		t.Errorf("meta 解错:%+v", a.Meta)
	}
	if a.EnvCount != 2 || a.RepoCount != 1 || a.SkillCount != 2 {
		t.Errorf("derive 错:envs=%d repos=%d skills=%d", a.EnvCount, a.RepoCount, a.SkillCount)
	}
}

func TestScanDedupBySystemIDAndTarget(t *testing.T) {
	// 两个 root 都指向同一个 agent 目录(或两个 root 扫到同 systemID+target 的不同文件),
	// 返回应该只有一个。
	root1 := t.TempDir()
	root2 := t.TempDir()
	m := Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"}
	writeMeta(t, filepath.Join(root1, "a"), m)
	writeMeta(t, filepath.Join(root2, "b"), m)

	agents, err := Scan([]string{root1, root2})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Errorf("want 1 dedup-ed agent, got %d", len(agents))
	}
}

func TestScanMultipleTargetsOfSameSystem(t *testing.T) {
	// 同 systemID 但不同 target 的算不同 agent,不去重。
	root := t.TempDir()
	writeMeta(t, filepath.Join(root, "a"), Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"})
	writeMeta(t, filepath.Join(root, "b"), Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "cursor"})

	agents, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 (different targets keep both), got %d", len(agents))
	}
}

func TestScanSkipsMissingRoot(t *testing.T) {
	// 不存在的 root 不算错,静默跳过。
	got, err := Scan([]string{"/nonexistent/path/hopefully", t.TempDir()})
	if err != nil {
		t.Errorf("want nil err, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空场景返回了 %d 个", len(got))
	}
}

func TestScanSkipsInvalidMeta(t *testing.T) {
	// meta 文件缺 systemID / target 的应该跳过,不抛错。
	root := t.TempDir()
	// 合法的一个
	writeMeta(t, filepath.Join(root, "ok"), Meta{SchemaVersion: 1, SystemID: "ok", Target: "openclaw"})
	// 缺 target 的
	writeMeta(t, filepath.Join(root, "bad1"), Meta{SchemaVersion: 1, SystemID: "bad1"})
	// 缺 systemID 的
	writeMeta(t, filepath.Join(root, "bad2"), Meta{SchemaVersion: 1, Target: "openclaw"})
	// 直接垃圾 JSON
	os.MkdirAll(filepath.Join(root, "garbage"), 0o755)
	os.WriteFile(filepath.Join(root, "garbage", MetaFilename), []byte("not json"), 0o644)

	agents, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Errorf("只应留 1 条合法 meta,got %d", len(agents))
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("~/foo")
	if got == "~/foo" {
		t.Error("~ 没展开")
	}
	if filepath.Base(got) != "foo" {
		t.Errorf("tail 错: %s", got)
	}
	// 非 ~ 前缀不动
	if expandHome("/tmp/x") != "/tmp/x" {
		t.Error("非 ~ 前缀不应改")
	}
}
