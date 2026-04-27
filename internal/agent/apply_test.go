package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// projectRoot 定位 troubleshooter-studio 仓库根(tests 在 internal/agent)。
func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// genExisting 在 dir 下 gen 出一份完整的 openclaw 产物,给 Apply 测试当"已装 agent"。
// 返回 agent.Path(即 workspace-template 目录,含 tshoot.json)。
func genExisting(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	tr := filepath.Join(projectRoot(t), "templates")
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromBytes(yamlBytes)
	if err != nil {
		t.Fatal(err)
	}
	g := generator.New(cfg, tr, dir)
	g.TshootVersion = "test"
	g.SystemYAMLSource = yamlBytes
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "templates", "workspace-template"), yamlBytes
}

func TestApplyDryRun_PreservesUserEdits(t *testing.T) {
	// 场景:agent 已装,用户改了 SOUL.md(在 preserve_on_regenerate 里),
	// 用原 yaml 跑 Apply --dry-run,应报告 SOUL.md 在 preserved 列表里,
	// 真文件内容不变(dry-run 不写盘)。
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)

	soulPath := filepath.Join(agentPath, "SOUL.md")
	userEdit := "# USER HAND-EDITED SOUL\n"
	if err := os.WriteFile(soulPath, []byte(userEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	res, err := Apply(ag, ApplyOptions{
		NewYAML:       yamlBytes,
		TemplateRoot:  filepath.Join(projectRoot(t), "templates"),
		TshootVersion: "test",
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Target != "openclaw" {
		t.Errorf("target wrong: %s", res.Target)
	}
	if !containsString(res.FilesPreserved, "SOUL.md") {
		t.Errorf("preserve_on_regenerate 里的 SOUL.md 没被标记 preserved: %+v", res.FilesPreserved)
	}
	// dry-run 不写盘,用户手改必须还在
	got, _ := os.ReadFile(soulPath)
	if string(got) != userEdit {
		t.Errorf("dry-run 动了盘:want %q, got %q", userEdit, string(got))
	}
	// tshoot.json 也不写
	if res.TSFJSONUpdated {
		t.Error("dry-run 不应该标 TSFJSONUpdated=true")
	}
}

func TestApplyRealWritesAndPreserves(t *testing.T) {
	// 场景:真 apply(非 dry-run),preserve 文件保留用户手改,
	// tshoot.json 被更新。
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)

	soulPath := filepath.Join(agentPath, "SOUL.md")
	userEdit := "# user custom soul\n"
	_ = os.WriteFile(soulPath, []byte(userEdit), 0o644)

	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	res, err := Apply(ag, ApplyOptions{
		NewYAML:       yamlBytes,
		TemplateRoot:  filepath.Join(projectRoot(t), "templates"),
		TshootVersion: "applied-version",
		DryRun:        false,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.TSFJSONUpdated {
		t.Error("真 apply 应该更新 tshoot.json")
	}
	if res.FilesWritten == 0 {
		t.Error("files_written 不该为 0")
	}
	// 用户手改保留
	got, _ := os.ReadFile(soulPath)
	if string(got) != userEdit {
		t.Errorf("preserve 失败:用户手改被覆盖\nwant %q\ngot  %q", userEdit, string(got))
	}
	// tshoot.json 里 version 更新到 applied-version
	metaData, err := os.ReadFile(filepath.Join(agentPath, "tshoot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.TshootVersion != "applied-version" {
		t.Errorf("tshoot.json version 没更新: %s", meta.TshootVersion)
	}
}

func TestApplyRejectsInvalidYAML(t *testing.T) {
	stage := t.TempDir()
	agentPath, _ := genExisting(t, stage)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", Target: "openclaw"},
		Path: agentPath,
	}
	_, err := Apply(ag, ApplyOptions{
		NewYAML:      []byte("not valid yaml: structure"),
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
	})
	if err == nil {
		t.Error("无效 yaml 应该报错")
	}
}

func TestApplyRejectsEmptyYAML(t *testing.T) {
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", Target: "openclaw"},
		Path: t.TempDir(),
	}
	_, err := Apply(ag, ApplyOptions{NewYAML: nil})
	if err == nil {
		t.Error("空 yaml 应该报错")
	}
}

func TestApplyUnknownTarget(t *testing.T) {
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", Target: "i-am-not-a-real-target"},
		Path: agentPath,
	}
	_, err := Apply(ag, ApplyOptions{
		NewYAML:      yamlBytes,
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
	})
	if err == nil {
		t.Error("未知 target 应该报错")
	}
}

func TestImportAndApply_OpenclawProducesStaging(t *testing.T) {
	// 场景:从零 import 到 openclaw target,产物应该是 staging 包(workspace
	// 模板 + tshoot.json + self-test/uninstall 辅助脚本)。
	// 注:install.sh 已迁到 InstallNativeOpenclaw,不在产物里。
	dest := t.TempDir()
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ImportAndApply(yamlBytes, "openclaw", dest, ApplyOptions{
		TemplateRoot:  filepath.Join(projectRoot(t), "templates"),
		TshootVersion: "test",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.AgentPath != dest {
		t.Errorf("agent_path:want %s got %s", dest, res.AgentPath)
	}
	// workspace-template 下应该有 tshoot.json(InstallNativeOpenclaw 既从这里
	// 反读 cfg,也是 cp 过去后被 discover.Scan 识别的锚点)
	meta := filepath.Join(dest, "templates", "workspace-template", "tshoot.json")
	if _, err := os.Stat(meta); err != nil {
		t.Errorf("tshoot.json 没生成: %v", err)
	}
}

func TestImportAndApply_DryRunNoWrite(t *testing.T) {
	dest := t.TempDir()
	yamlBytes, _ := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-system.yaml"))
	res, err := ImportAndApply(yamlBytes, "openclaw", dest, ApplyOptions{
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
		DryRun:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// dry-run 目录应该基本为空(只有 tempdir 自己)
	entries, _ := os.ReadDir(dest)
	// openclaw 的 DryRun 分支 ImportAndApply 早退,但会先 MkdirAll(dest),所以 dest 目录本身会存在
	// 但不应该有 scripts/ / templates/ 子目录
	for _, e := range entries {
		if e.Name() == "scripts" || e.Name() == "templates" {
			t.Errorf("DryRun 不应落盘,但找到了 %s", e.Name())
		}
	}
	if res.NeedsRestartHint == "" {
		t.Error("DryRun 应该给出后续步骤的提示")
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
