package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// projectRoot 返回 troubleshooter-studio 仓库根目录（便于测试定位 templates/ 与 examples/）
func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// tests live in internal/generator, so root is ../..
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func loadCfg(t *testing.T, rel string) *config.SystemConfig {
	t.Helper()
	cfg, err := config.Load(filepath.Join(projectRoot(t), rel))
	if err != nil {
		t.Fatalf("load %s: %v", rel, err)
	}
	return cfg
}

// TestGenerate_MultiSource_ConfigMapRoutesPerService 验证多源场景下 config-map.yaml
// 每个服务的 mcp_server 字段按它所属 repo 的 config_source 选对应源的 MCP key,
// 且副源服务多带一行 config_source 字段标记。
func TestGenerate_MultiSource_ConfigMapRoutesPerService(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	// 在 shop-system 基础上注入第二源 + 把 product-service 重路由到副源
	cfg.Infrastructure.ConfigCenters = append(cfg.Infrastructure.ConfigCenters, config.ConfigCenter{
		ID:   "legacy-nacos",
		Type: "nacos",
		Endpoints: []config.ConfigCenterEndpoint{
			{Env: "dev", Addr: "legacy-nacos-dev:8848", NamespaceHint: "legacy-dev"},
			{Env: "staging", Addr: "legacy-nacos-stg:8848", NamespaceHint: "legacy-stg"},
			{Env: "prod", Addr: "legacy-nacos-prod:8848", NamespaceHint: "legacy-prod"},
		},
	})
	for i := range cfg.Repos {
		if cfg.Repos[i].Name == "product-service" {
			cfg.Repos[i].ConfigSource = "legacy-nacos"
		}
	}

	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))

	// 2026-05-15 方案 B 后:nacos 不渲染 mcp_server,所有 nacos 服务都标 runtime: nacos-http。
	// 主源 / 副源的区分还有效(config_source 字段下面单独验证),只是不再通过 mcp_server 名字承载。
	if !strings.Contains(cm, "runtime: nacos-http") {
		t.Errorf("config-map should mark nacos services as runtime: nacos-http,\ngot:\n%s", cm)
	}
	// 反向护栏:不应再出现任何 nacos mcp 名(mcp 不存在,渲染了 LLM 会去找)
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, bad := range []string{
			`mcp_server: "shop-nacos-` + env + `"`,
			`mcp_server: "shop-nacos-legacy-nacos-` + env + `"`,
		} {
			if strings.Contains(cm, bad) {
				t.Errorf("config-map 不应再渲染 nacos mcp 名 %q(方案 B 已删 mcp 注册)", bad)
			}
		}
	}

	// 副源服务多一行 config_source: "legacy-nacos"
	if !strings.Contains(cm, `config_source: "legacy-nacos"`) {
		t.Errorf("副源服务应带 config_source 字段标记")
	}

	// 多源块 sources: 应被声明
	if !strings.Contains(cm, "sources:") {
		t.Errorf("多源场景 config-map 应有 sources: 块")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func loadConfigMap(t *testing.T, path string) map[string]map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root struct {
		Environments map[string]map[string]map[string]any `yaml:"environments"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return root.Environments
}

func TestGenerate_Nacos_Shop(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	g := New(cfg, tr, out)
	if err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}

	// 结构断言
	wsRoot := filepath.Join(out, "templates", "workspace-template")
	must := []string{
		"SOUL.md", "IDENTITY.md", "USER.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md",
		"skills/routing/SKILL.md",
		"skills/routing/references/env-branch-map.yaml",
		"skills/routing/references/env-domain-map.yaml",
		"skills/routing/references/config-map.yaml",
		"skills/routing/references/repo-stack-map.yaml",
		"skills/routing/references/repo-path-map.yaml",
		"skills/config-executor/SKILL.md",
		"skills/redis-runtime-query/SKILL.md",
		"skills/mongodb-runtime-query/SKILL.md",
		"skills/es-runtime-query/SKILL.md",
		"skills/diagram-generator/SKILL.md",
	}
	for _, rel := range must {
		if _, err := os.Stat(filepath.Join(wsRoot, rel)); err != nil {
			t.Errorf("expected file missing: %s (%v)", rel, err)
		}
	}

	// scripts/ 目录已不再生成 —— install / self-test / uninstall 全部由原生 Go
	// 实现(agent.InstallNativeOpenclaw / SelfTestOpenclaw / UninstallNativeOpenclaw)
	if _, err := os.Stat(filepath.Join(out, "scripts")); err == nil {
		t.Errorf("scripts/ 目录不应存在(install/self-test/uninstall 已 port 到 Go)")
	}

	// config-map 应标 config_center: nacos
	cm := readFile(t, filepath.Join(wsRoot, "skills/routing/references/config-map.yaml"))
	if !strings.Contains(cm, "config_center: nacos") {
		t.Errorf("config-map should declare nacos center:\n%s", cm)
	}

	// 2026-05-15 方案 B 后:nacos 不走 MCP,config-map 里 nacos 服务标 runtime: nacos-http,
	// 不再渲染 mcp_server 字段。改成验证 runtime: nacos-http 标签出现(且 per-service 都有)。
	if !strings.Contains(cm, "runtime: nacos-http") {
		t.Errorf("config-map should mark nacos services as runtime: nacos-http,\ngot:\n%s", cm)
	}
	// 反向护栏:不应再出现 shop-nacos-<env> 这种 mcp 名(mcp 不存在,渲染了 LLM 会去找)
	for _, env := range []string{"dev", "staging", "prod"} {
		bad := "shop-nacos-" + env
		if strings.Contains(cm, bad) {
			t.Errorf("config-map 不应再渲染 nacos mcp 名 %q(方案 B 已删 mcp 注册)", bad)
		}
	}

	// mysql 和 kafka 现在已在 shop-system 中启用，验证它们存在
	for _, s := range []string{"mysql-runtime-query", "kafka-runtime-query"} {
		if _, err := os.Stat(filepath.Join(wsRoot, "skills", s)); err != nil {
			t.Errorf("skill %s should be generated: %v", s, err)
		}
	}
}

// 配置中心 prompt 派生由 agent.DerivePrompts 验证,
// 见 internal/agent/install_prompts_test.go(每 env 独立凭证)。

func TestGenerate_Apollo(t *testing.T) {
	cfg := loadCfg(t, "examples/apollo-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))
	if !strings.Contains(cm, "config_center: apollo") {
		t.Errorf("config-map should declare apollo")
	}
	if !strings.Contains(cm, "appId:") || !strings.Contains(cm, "namespaces:") {
		t.Errorf("apollo config-map missing expected fields")
	}

	// C5: Apollo 走 HTTP 脚本，产物必须含 apollo_config.py
	if _, err := os.Stat(filepath.Join(out, "templates/workspace-template/skills/config-executor/scripts/apollo_config.py")); err != nil {
		t.Errorf("apollo_config.py should exist: %v", err)
	}
	// install.sh 已被 InstallNativeOpenclaw 替换;Apollo 的 prompt / creds.json
	// 写入由 install_prompts_test.go 和 install_native_openclaw_test.go 覆盖。
	// SKILL.md 必须指向脚本
	skillMD := readFile(t, filepath.Join(out, "templates/workspace-template/skills/config-executor/SKILL.md"))
	if !strings.Contains(skillMD, "apollo_config.py") {
		t.Errorf("config-executor SKILL.md should reference apollo_config.py")
	}
}

func TestGenerate_Consul(t *testing.T) {
	cfg := loadCfg(t, "examples/consul-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))
	if !strings.Contains(cm, "config_center: consul") {
		t.Errorf("config-map should declare consul")
	}
	if !strings.Contains(cm, "kv_prefix:") || !strings.Contains(cm, "default_context:") {
		t.Errorf("consul config-map missing expected fields")
	}

	// C5: Consul 走 HTTP 脚本
	if _, err := os.Stat(filepath.Join(out, "templates/workspace-template/skills/config-executor/scripts/consul_config.py")); err != nil {
		t.Errorf("consul_config.py should exist: %v", err)
	}
	// Consul 的 prompt 集合 / creds.json 由 agent 包测试覆盖,这里只验证
	// generator 仍出脚本素材
	skillMD := readFile(t, filepath.Join(out, "templates/workspace-template/skills/config-executor/SKILL.md"))
	if !strings.Contains(skillMD, "consul_config.py") {
		t.Errorf("config-executor SKILL.md should reference consul_config.py")
	}
}

func TestGenerate_ClawhubLock(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(out, "templates/workspace-template/.clawhub/lock.json")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock.json missing: %v", err)
	}
	var lock struct {
		Version int `json:"version"`
		Skills  map[string]struct {
			Version     string `json:"version"`
			InstalledAt int64  `json:"installedAt"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse lock.json: %v", err)
	}
	if lock.Version != 1 {
		t.Errorf("lock.version expected 1, got %d", lock.Version)
	}
	// shop-troubleshooter.yaml 的 skills_whitelist 列了 8 个 skills
	wantSkills := []string{"routing", "config-executor", "redis-runtime-query", "mongodb-runtime-query", "es-runtime-query", "mysql-runtime-query", "kafka-runtime-query", "diagram-generator"}
	for _, s := range wantSkills {
		entry, ok := lock.Skills[s]
		if !ok {
			t.Errorf("lock.json missing skill %q", s)
			continue
		}
		if entry.InstalledAt == 0 {
			t.Errorf("%s.installedAt should be non-zero", s)
		}
		if entry.Version == "" {
			t.Errorf("%s.version should be non-empty", s)
		}
	}
	// 不在白名单里的 skill 不应出现
	for _, s := range []string{"nonexistent-skill"} {
		if _, ok := lock.Skills[s]; ok {
			t.Errorf("lock.json should not contain disabled skill %q", s)
		}
	}
}

func TestGenerate_ClawhubLock_EmptySkills(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	// 清空白名单 + 禁用所有 data stores，还要清掉 config center，
	// 确保没有 skills 目录会被生成
	cfg.Generation.SkillsWhitelist = []string{"__none__"}
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(out, "templates/workspace-template/.clawhub/lock.json")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock.json should exist even with no skills: %v", err)
	}
	var lock struct {
		Version int                    `json:"version"`
		Skills  map[string]interface{} `json:"skills"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Version != 1 {
		t.Errorf("version expected 1, got %d", lock.Version)
	}
	if len(lock.Skills) != 0 {
		t.Errorf("expected empty skills, got %v", lock.Skills)
	}
}

// assertExists 检查一组相对路径都存在于 base 下，否则报告缺失。
func assertExists(t *testing.T, base string, rels []string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
			t.Errorf("expected %s under %s (%v)", rel, base, err)
		}
	}
}

// TestGenerate_MultiTargets_All 覆盖 4 target 全开的共享 staging 路径：
// openclaw 跑完后，其产物目录被复用为 SharedStaging，后续 target 不再重复渲染 workspace。
// 对每个 target 目录断言关键产物存在。
func TestGenerate_MultiTargets_All(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := filepath.Join(t.TempDir(), "sys")
	tr := filepath.Join(projectRoot(t), "templates")

	g := New(cfg, tr, out)

	// openclaw
	if err := g.Generate(); err != nil {
		t.Fatalf("openclaw: %v", err)
	}
	g.SharedStaging = g.OutputDir

	// non-openclaw targets 复用 staging
	if err := g.GenerateClaudeCode(); err != nil {
		t.Fatalf("claude-code: %v", err)
	}
	if err := g.GenerateCursor(); err != nil {
		t.Fatalf("cursor: %v", err)
	}

	assertExists(t, out, []string{
		"templates/workspace-template/SOUL.md",
		"templates/workspace-template/tshoot.json",
	})
	// examples/shop-troubleshooter.yaml 的 agent.workspace_name = "shop-bot",直接做 slug
	// install.sh 已删除 —— 装到 ~/.claude|cursor/ 现在由 agent.InstallNative 完成
	assertExists(t, out+"-claude-code", []string{
		"agents/shop-bot.md",
		"skills/routing/SKILL.md",
	})
	assertExists(t, out+"-cursor", []string{
		"agents/shop-bot.md",
		"skills/routing/SKILL.md",
	})
}

// TestGenerate_MultiTargets_NoOpenclaw 覆盖"非 openclaw 独占"路径：
// 调用方先把 workspace 渲染到一个临时 staging，再跑各 target。
// openclaw 产物目录不会被创建。
func TestGenerate_MultiTargets_NoOpenclaw(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := filepath.Join(t.TempDir(), "sys")
	tr := filepath.Join(projectRoot(t), "templates")

	g := New(cfg, tr, out)

	// 建 staging，跑一次 Generate 到里面
	staging := t.TempDir()
	origOut := g.OutputDir
	g.OutputDir = staging
	if err := g.Generate(); err != nil {
		t.Fatalf("stage workspace: %v", err)
	}
	g.OutputDir = origOut
	g.SharedStaging = staging

	if err := g.GenerateClaudeCode(); err != nil {
		t.Fatalf("claude-code: %v", err)
	}

	// openclaw 目录不应存在
	if _, err := os.Stat(out); err == nil {
		t.Errorf("openclaw output dir %s should NOT exist when openclaw not in targets", out)
	}

	// 其它 target 产物存在(install.sh 已挪到 InstallNative,产物里只剩纯素材)
	assertExists(t, out+"-claude-code", []string{"agents/shop-bot.md"})
}

func TestGenerate_WithAnalysis_UpgradesInferredToVerified(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")

	g := New(cfg, tr, out)

	// 注入合成 finding：给 order-worker 填充具体值，走 LoadAnalysis 正规路径
	analysisJSON := `{
  "schema_version": "0.1",
  "config_center": "nacos",
  "repos": [{
    "name": "order-service",
    "stack": "go",
    "service_names": ["order-service", "order-worker"],
    "findings": [{
      "config_center": "nacos",
      "source_file": "synthetic.yaml",
      "data_id": "order-worker.yaml",
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
	if err := g.LoadAnalysis(ap); err != nil {
		t.Fatalf("load analysis: %v", err)
	}
	if err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}

	rows := loadConfigMap(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))
	row := rows["dev"]["order-worker"]
	if row["status"] != "verified" {
		t.Errorf("order-worker/dev status expected verified, got %v", row["status"])
	}
	if row["dataId"] != "order-worker.yaml" {
		t.Errorf("order-worker/dev dataId expected order-worker.yaml, got %v", row["dataId"])
	}
}
