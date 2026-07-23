package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
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
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromBytes(yamlBytes)
	if err != nil {
		t.Fatal(err)
	}
	g := generator.New(cfg, tr, dir)
	g.TshootVersion = "test"
	g.TroubleshooterYAMLSource = yamlBytes
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "templates", "workspace-template"), yamlBytes
}

func TestWriteTSFMetaIncludesInternalAgentContract(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SystemConfig{}
	cfg.System.ID = "base"
	cfg.System.Name = "Base"
	cfg.Agent.ID = "base-troubleshooter"
	cfg.Repos = []config.Repo{{Name: "base-api", URL: "git@github.com:acme/base.git", SubPath: "services/api"}}
	repoPath := filepath.Join(dir, "base")
	if err := writeTSFMeta(dir, "codex", cfg, []byte("system:\n  id: base\n"), "test-version", map[string]string{"base-api": repoPath}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, discover.MetaFilename))
	if err != nil {
		t.Fatal(err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.AgentID != "base-troubleshooter" || meta.Role != discover.RoleTroubleshooter {
		t.Fatalf("meta missing primary agent fields: %+v", meta)
	}
	if len(meta.InternalAgents) != 3 {
		t.Fatalf("internal agents = %+v", meta.InternalAgents)
	}
	if meta.InternalAgents[1].ID != "base-validator" || meta.InternalAgents[2].ID != "base-fixer" {
		t.Fatalf("internal agents wrong: %+v", meta.InternalAgents)
	}
	if meta.SchemaVersion != 2 || len(meta.ProjectRepositories) != 1 {
		t.Fatalf("project ownership metadata missing: %+v", meta)
	}
	if got := meta.ProjectRepositories[0]; got.Name != "base-api" || got.LocalPath != repoPath || got.SubPath != "services/api" {
		t.Fatalf("project repository wrong: %+v", got)
	}
}

func TestApplyDryRun_NoDiskWrites(t *testing.T) {
	// 场景:agent 已装,用户改了 SOUL.md,用原 yaml 跑 Apply --dry-run,
	// 不应写盘(用户手改先保留),也不更新 tshoot.json。
	// 注:整文件 preserve 机制已删 —— 真 apply 会按模板覆盖 SOUL.md,
	// 这里只验证 dry-run 的"不动盘"语义。
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

func TestApplyRealOverwritesAndUpdatesMeta(t *testing.T) {
	// 场景:真 apply(非 dry-run),所有模板派生文件按模板覆盖,tshoot.json 被更新。
	// 整文件 preserve 已删 —— 模板更新不再被 SOUL/USER/CHECKLIST 阻塞。
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
	// 模板派生文件被覆盖回模板渲染版本(用户手改不再受保护)
	got, _ := os.ReadFile(soulPath)
	if string(got) == userEdit {
		t.Error("SOUL.md 应该被模板覆盖,实际还是用户手改版")
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
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-troubleshooter.yaml"))
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

func TestApply_AutoAnalyzeTopologyReachesGeneratedWorkspace(t *testing.T) {
	yamlBytes, repoPaths := applyTopologyFixture(t)
	cfg, err := config.LoadFromBytes(yamlBytes)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	g := generator.New(cfg, filepath.Join(projectRoot(t), "templates"), stage)
	g.TroubleshooterYAMLSource = yamlBytes
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(stage, "templates", "workspace-template")

	_, err = Apply(discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "mall", SystemName: "Mall", Target: "openclaw"},
		Path: agentPath,
	}, ApplyOptions{
		NewYAML:        yamlBytes,
		TemplateRoot:   filepath.Join(projectRoot(t), "templates"),
		RepoLocalPaths: repoPaths,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	refs := filepath.Join(agentPath, "skills", "routing", "references")
	assertGeneratedTopologyEdge(t, filepath.Join(refs, "service-topology.yaml"))
	assertGeneratedTopologyEvidence(t, filepath.Join(refs, "endpoint-evidence.yaml"))
	assertGeneratedTopologySource(t, filepath.Join(agentPath, discover.MetaFilename), yamlBytes)
}

func TestImportAndApply_AutoAnalyzeTopologyReachesGeneratedStaging(t *testing.T) {
	yamlBytes, repoPaths := applyTopologyFixture(t)
	dest := t.TempDir()
	_, err := ImportAndApply(yamlBytes, "openclaw", dest, ApplyOptions{
		TemplateRoot:   filepath.Join(projectRoot(t), "templates"),
		RepoLocalPaths: repoPaths,
	})
	if err != nil {
		t.Fatalf("ImportAndApply: %v", err)
	}
	workspace := filepath.Join(dest, "templates", "workspace-template")
	refs := filepath.Join(workspace, "skills", "routing", "references")
	assertGeneratedTopologyEdge(t, filepath.Join(refs, "service-topology.yaml"))
	assertGeneratedTopologyEvidence(t, filepath.Join(refs, "endpoint-evidence.yaml"))
	assertGeneratedTopologySource(t, filepath.Join(workspace, discover.MetaFilename), yamlBytes)
}

func applyTopologyFixture(t *testing.T) ([]byte, map[string]string) {
	t.Helper()
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "three-tier-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	overrides := `service_topology:
  overrides:
    - action: confirm
      from_service: mall-web
      to_service: mall-bff
      protocol: http
      method: GET
      path: /api/orders
    - action: reject
      from_service: mall-web
      to_service: mall-order
      protocol: http
      method: GET
      path: /candidate-only
`
	source := strings.Replace(string(yamlBytes), "service_topology:\n  overrides: []\n", overrides, 1)
	if source == string(yamlBytes) {
		t.Fatal("failed to inject service_topology overrides")
	}
	root := t.TempDir()
	web := filepath.Join(root, "mall-web")
	bff := filepath.Join(root, "mall-bff")
	order := filepath.Join(root, "mall-order")
	for _, path := range []string{web, bff, order} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoPaths := map[string]string{"mall-web": web, "mall-bff": bff, "mall-order": order}
	cfg, err := config.LoadFromBytes([]byte(source))
	if err != nil {
		t.Fatalf("load topology source fixture: %v", err)
	}
	seedApplyTopologyCache(t, cfg, repoPaths, &analyzerpipe.Result{Topology: topology.Snapshot{
		SchemaVersion: topology.SchemaVersion,
		Services: []topology.ServiceDescriptor{
			{Repo: "mall-web", Service: "mall-web", Role: config.RoleFrontend},
			{Repo: "mall-bff", Service: "mall-bff", Role: config.RoleGateway},
			{Repo: "mall-order", Service: "mall-order", Role: config.RoleBackend},
		},
		Edges: []topology.CandidateEdge{
			{FromEndpoint: "mall-web:out", ToEndpoint: "mall-bff:in", FromService: "mall-web", ToService: "mall-bff", Protocol: "http", Method: "GET", Path: "/api/orders", Status: "automatic", Confidence: .98, Reasons: []string{"method_path_exact"}},
			{FromEndpoint: "mall-bff:out", ToEndpoint: "mall-order:in", FromService: "mall-bff", ToService: "mall-order", Protocol: "http", Method: "POST", Path: "/internal/orders", Status: "automatic", Confidence: .97, Reasons: []string{"method_path_exact"}},
			{FromEndpoint: "mall-bff:out", ToEndpoint: "mall-order:in", FromService: "mall-bff", ToService: "mall-order", Protocol: "http", Method: "POST", Path: "/candidate-only", Status: "candidate", Confidence: .74, Reasons: []string{"candidate_cache"}},
			{FromEndpoint: "mall-web:out", ToEndpoint: "mall-order:in", FromService: "mall-web", ToService: "mall-order", Protocol: "http", Method: "GET", Path: "/rejected-only", Status: "rejected", Confidence: .91, Reasons: []string{"candidate_cache", "human_override_reject"}},
		},
	}})
	return []byte(source), repoPaths
}

func seedApplyTopologyCache(t *testing.T, cfg *config.SystemConfig, repoPaths map[string]string, result *analyzerpipe.Result) {
	t.Helper()
	key := autoAnalyzeCacheKey(cfg, repoPaths)
	autoAnalyzeCacheMu.Lock()
	previous, existed := autoAnalyzeCache[key]
	autoAnalyzeCache[key] = &autoAnalyzeCacheEntry{result: result, at: time.Now()}
	autoAnalyzeCacheMu.Unlock()
	t.Cleanup(func() {
		autoAnalyzeCacheMu.Lock()
		defer autoAnalyzeCacheMu.Unlock()
		if existed {
			autoAnalyzeCache[key] = previous
		} else {
			delete(autoAnalyzeCache, key)
		}
	})
}

func assertGeneratedTopologyEdge(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated topology: %v", err)
	}
	for _, want := range []string{`from: "mall-web"`, `to: "mall-order"`, `status: "automatic"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated topology missing %q:\n%s", want, data)
		}
	}
}

func assertGeneratedTopologyEvidence(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated topology evidence: %v", err)
	}
	for _, want := range []string{`status: "candidate"`, `status: "rejected"`, `"candidate_cache"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated topology evidence missing %q:\n%s", want, data)
		}
	}
}

func assertGeneratedTopologySource(t *testing.T, path string, source []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated tshoot.json: %v", err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse generated tshoot.json: %v", err)
	}
	if !bytes.Equal([]byte(meta.TroubleshooterYAML), source) {
		t.Fatalf("embedded troubleshooter_yaml changed source bytes:\n--- got ---\n%s\n--- want ---\n%s", meta.TroubleshooterYAML, source)
	}
	for _, want := range []string{"service_topology:", "action: confirm", "action: reject"} {
		if !bytes.Contains([]byte(meta.TroubleshooterYAML), []byte(want)) {
			t.Fatalf("embedded troubleshooter_yaml missing source override %q:\n%s", want, meta.TroubleshooterYAML)
		}
	}
	for _, forbidden := range []string{"candidate_cache", `status: "candidate"`, `status: "rejected"`, "/rejected-only"} {
		if bytes.Contains([]byte(meta.TroubleshooterYAML), []byte(forbidden)) {
			t.Fatalf("auto-analyze candidate evidence %q leaked into troubleshooter_yaml:\n%s", forbidden, meta.TroubleshooterYAML)
		}
	}
}

func TestImportAndApply_DryRunNoWrite(t *testing.T) {
	dest := t.TempDir()
	yamlBytes, _ := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-troubleshooter.yaml"))
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

func TestApply_CodeGraphDisabledDoesNothing(t *testing.T) {
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	previousEnsure := ensureCodeGraphForDeploy
	previousPrepare := prepareCodeGraphForDeploy
	ensureCodeGraphForDeploy = func(func(string)) (string, error) {
		t.Fatal("disabled CodeGraph deployment called installer")
		return "", nil
	}
	prepareCodeGraphForDeploy = func(context.Context, CodeGraphIndexOptions) CodeGraphIndexReport {
		t.Fatal("disabled CodeGraph deployment called index manager")
		return CodeGraphIndexReport{}
	}
	t.Cleanup(func() {
		ensureCodeGraphForDeploy = previousEnsure
		prepareCodeGraphForDeploy = previousPrepare
	})

	result, err := Apply(ag, ApplyOptions{
		NewYAML:      yamlBytes,
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.CodeGraph != nil {
		t.Fatalf("CodeGraph report = %#v, want nil", result.CodeGraph)
	}
}

func TestApply_CodeGraphFailureWarnsAndStillDeploys(t *testing.T) {
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)
	yamlBytes = enableCodeGraphForApplyTest(t, yamlBytes)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	previousEnsure := ensureCodeGraphForDeploy
	previousPrepare := prepareCodeGraphForDeploy
	ensureCodeGraphForDeploy = func(func(string)) (string, error) {
		return "", errors.New("checksum mismatch")
	}
	prepareCodeGraphForDeploy = func(context.Context, CodeGraphIndexOptions) CodeGraphIndexReport {
		t.Fatal("index manager called after installer failure")
		return CodeGraphIndexReport{}
	}
	t.Cleanup(func() {
		ensureCodeGraphForDeploy = previousEnsure
		prepareCodeGraphForDeploy = previousPrepare
	})

	var logs []string
	result, err := Apply(ag, ApplyOptions{
		NewYAML:      yamlBytes,
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
		OnLog:        func(line string) { logs = append(logs, line) },
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result == nil || result.CodeGraph == nil {
		t.Fatalf("Apply() result = %#v, want successful result with CodeGraph fallback report", result)
	}
	if result.CodeGraph.Total != 4 {
		t.Fatalf("CodeGraph total = %d, want all 4 repositories by default", result.CodeGraph.Total)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "checksum mismatch") || !strings.Contains(joined, "rg/read fallback") {
		t.Fatalf("logs = %q, want checksum failure and rg/read fallback", joined)
	}
}

func TestApply_CodeGraphReportReturned(t *testing.T) {
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)
	yamlBytes = enableCodeGraphForApplyTest(t, yamlBytes)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	previousEnsure := ensureCodeGraphForDeploy
	previousPrepare := prepareCodeGraphForDeploy
	ensureCodeGraphForDeploy = func(func(string)) (string, error) { return "/fake/codegraph", nil }
	prepareCodeGraphForDeploy = func(_ context.Context, opts CodeGraphIndexOptions) CodeGraphIndexReport {
		if opts.BinaryPath != "/fake/codegraph" || opts.SystemID != "shop" {
			t.Fatalf("index options = %#v", opts)
		}
		return CodeGraphIndexReport{
			Ready: 1,
			Total: 1,
			Repos: []CodeGraphRepoResult{{
				Name: "order-service", Status: "ready", FileCount: 12, NodeCount: 34, EdgeCount: 56,
			}},
		}
	}
	t.Cleanup(func() {
		ensureCodeGraphForDeploy = previousEnsure
		prepareCodeGraphForDeploy = previousPrepare
	})

	result, err := Apply(ag, ApplyOptions{
		NewYAML:      yamlBytes,
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	codeGraph, ok := fields["codegraph"].(map[string]any)
	if !ok || codeGraph["ready"] != float64(1) || codeGraph["total"] != float64(1) {
		t.Fatalf("serialized codegraph = %#v", fields["codegraph"])
	}
	repos, ok := codeGraph["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("serialized repos = %#v", codeGraph["repos"])
	}
	repo := repos[0].(map[string]any)
	if repo["file_count"] != float64(12) || repo["node_count"] != float64(34) || repo["edge_count"] != float64(56) {
		t.Fatalf("serialized repo counts = %#v", repo)
	}
}

func TestApply_CodeGraphDryRunDoesNothing(t *testing.T) {
	stage := t.TempDir()
	agentPath, yamlBytes := genExisting(t, stage)
	yamlBytes = enableCodeGraphForApplyTest(t, yamlBytes)
	ag := discover.DiscoveredAgent{
		Meta: discover.Meta{SchemaVersion: 1, SystemID: "shop", SystemName: "Shop", Target: "openclaw"},
		Path: agentPath,
	}
	previousEnsure := ensureCodeGraphForDeploy
	previousPrepare := prepareCodeGraphForDeploy
	ensureCodeGraphForDeploy = func(func(string)) (string, error) {
		t.Fatal("dry-run called installer")
		return "", nil
	}
	prepareCodeGraphForDeploy = func(context.Context, CodeGraphIndexOptions) CodeGraphIndexReport {
		t.Fatal("dry-run called index manager")
		return CodeGraphIndexReport{}
	}
	t.Cleanup(func() {
		ensureCodeGraphForDeploy = previousEnsure
		prepareCodeGraphForDeploy = previousPrepare
	})

	result, err := Apply(ag, ApplyOptions{
		NewYAML:      yamlBytes,
		TemplateRoot: filepath.Join(projectRoot(t), "templates"),
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.CodeGraph != nil {
		t.Fatalf("CodeGraph report = %#v, want nil", result.CodeGraph)
	}
}

func TestImportAndApply_MultiTargetCacheAvoidsSecondIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	yamlBytes, err := os.ReadFile(filepath.Join(projectRoot(t), "examples", "shop-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	yamlBytes = enableCodeGraphForApplyTest(t, yamlBytes)
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCodeGraphTest(t, "init", "-q", repoRoot)
	runGitForCodeGraphTest(t, "-C", repoRoot, "add", "main.go")
	runGitForCodeGraphTest(t, "-C", repoRoot, "-c", "user.name=CodeGraph Test", "-c", "user.email=codegraph@example.invalid", "commit", "-qm", "initial")

	const systemID = "shop"
	InvalidateCodeGraphIndexCache(systemID)
	t.Cleanup(func() { InvalidateCodeGraphIndexCache(systemID) })
	previousEnsure := ensureCodeGraphForDeploy
	previousPrepare := prepareCodeGraphForDeploy
	previousRunner := runCodeGraphCommand
	previousInstallNative := installNativeForApply
	previousMergeMCP := mergeMCPIntoIDESettingsForApply
	ensureCodeGraphForDeploy = func(func(string)) (string, error) { return "/fake/codegraph", nil }
	prepareCodeGraphForDeploy = PrepareCodeGraphIndexes
	var nativeInstalls atomic.Int32
	installNativeForApply = func(_ string, target string) error {
		if target != "claude-code" {
			t.Fatalf("native install target = %q, want claude-code", target)
		}
		nativeInstalls.Add(1)
		return nil
	}
	var mcpMerges atomic.Int32
	mergeMCPIntoIDESettingsForApply = func(target string, _ *config.SystemConfig, _ map[string]string, _ func(string)) error {
		if target != "claude-code" {
			t.Fatalf("MCP merge target = %q, want claude-code", target)
		}
		mcpMerges.Add(1)
		return nil
	}
	var commands atomic.Int32
	runCodeGraphCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		call := commands.Add(1)
		if len(args) > 0 && args[0] == "status" {
			if call == 1 {
				return codeGraphStatusJSON(repoRoot, false, 0, 0, 0, "missing"), nil
			}
			return codeGraphStatusJSON(repoRoot, true, 1, 2, 1, "complete"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		ensureCodeGraphForDeploy = previousEnsure
		prepareCodeGraphForDeploy = previousPrepare
		runCodeGraphCommand = previousRunner
		installNativeForApply = previousInstallNative
		mergeMCPIntoIDESettingsForApply = previousMergeMCP
	})

	opts := ApplyOptions{
		TemplateRoot:   filepath.Join(projectRoot(t), "templates"),
		RepoLocalPaths: map[string]string{"order-service": repoRoot},
	}
	first, err := ImportAndApply(yamlBytes, "openclaw", t.TempDir(), opts)
	if err != nil {
		t.Fatalf("first ImportAndApply() error = %v", err)
	}
	firstCommandCount := commands.Load()
	if firstCommandCount == 0 || first.CodeGraph == nil || first.CodeGraph.Ready != 1 {
		t.Fatalf("first result = %#v, commands = %d", first, firstCommandCount)
	}
	second, err := ImportAndApply(yamlBytes, "claude-code", t.TempDir(), opts)
	if err != nil {
		t.Fatalf("second ImportAndApply() error = %v", err)
	}
	if first.Target != "openclaw" || second.Target != "claude-code" {
		t.Fatalf("deployment targets = %q then %q", first.Target, second.Target)
	}
	if nativeInstalls.Load() != 1 || mcpMerges.Load() != 1 {
		t.Fatalf("IDE seams called native=%d mcp=%d, want 1 each", nativeInstalls.Load(), mcpMerges.Load())
	}
	for _, userConfigPath := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")} {
		if _, err := os.Stat(userConfigPath); !os.IsNotExist(err) {
			t.Fatalf("stubbed IDE deployment mutated user config %s: %v", userConfigPath, err)
		}
	}
	if commands.Load() != firstCommandCount {
		t.Fatalf("commands after second deployment = %d, want cache reuse at %d", commands.Load(), firstCommandCount)
	}
	if second.CodeGraph == nil || !reflect.DeepEqual(second.CodeGraph, first.CodeGraph) {
		t.Fatalf("second report = %#v, first = %#v", second.CodeGraph, first.CodeGraph)
	}
}

func enableCodeGraphForApplyTest(t *testing.T, yamlBytes []byte) []byte {
	t.Helper()
	const disabled = "code_intelligence:\n  enabled: false"
	const enabled = "code_intelligence:\n  enabled: true"
	if !strings.Contains(string(yamlBytes), disabled) {
		t.Fatal("test fixture does not contain disabled code_intelligence block")
	}
	return []byte(strings.Replace(string(yamlBytes), disabled, enabled, 1))
}
