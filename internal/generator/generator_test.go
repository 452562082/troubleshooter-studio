package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
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

	// plan D:nacos 走 MCP 主路径,所有 nacos 服务标 runtime: nacos-mcp。
	// 主源 / 副源的区分通过 config_source 字段承载(下面单独验证),不在 config-map 里硬编码 mcp_server 名。
	if !strings.Contains(cm, "runtime: nacos-mcp") {
		t.Errorf("config-map should mark nacos services as runtime: nacos-mcp,\ngot:\n%s", cm)
	}
	// 反向护栏:config-map 不出 mcp_server 字段(用户选 runtime: nacos-mcp 风格;mcp_server 名
	// 在 SKILL 文档里讲命名规律,config-map 不硬编码,避免 agent-id 前缀漂移)。
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, bad := range []string{
			`mcp_server: "shop-nacos-` + env + `"`,
			`mcp_server: "shop-nacos-legacy-nacos-` + env + `"`,
		} {
			if strings.Contains(cm, bad) {
				t.Errorf("config-map 不应渲染 mcp_server 字段 %q(plan D 用 runtime: nacos-mcp 标签)", bad)
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

func TestGenerate_One2AllConfigMapWithK8sRuntimeServiceMap(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{{
		ID:   "one2all",
		Type: "one2all",
		Endpoints: []config.ConfigCenterEndpoint{{
			Env:   "dev",
			URL:   "http://one2all.example.com/mcp",
			Token: "o2a_test",
		}},
		ServiceMap: map[string]map[string]config.ServiceMapEntry{
			"dev": {
				"order-service": {
					ClusterID: "1",
					Namespace: "default",
					ConfigMap: "base-config,app-config",
				},
			},
		},
	}}
	for i := range cfg.Repos {
		cfg.Repos[i].ConfigSource = "one2all"
	}
	cfg.Infrastructure.Observability.K8sRuntime = config.K8sRuntime{
		Enabled:  true,
		Provider: "one2all",
		ServiceMap: []config.K8sRuntimeServiceMapEntry{{
			Env:           "dev",
			Service:       "order-service",
			ClusterID:     "1",
			Namespace:     "default",
			Workload:      "order-service",
			LabelSelector: "app=order-service",
		}},
	}

	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))
	rows := loadConfigMap(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/config-map.yaml"))
	for _, want := range []string{
		"runtime: one2all-mcp",
		`mcp_server: shop-one2all`,
		`cluster_id: "1"`,
		`namespace: "default"`,
		`configmaps: "base-config,app-config"`,
	} {
		if !strings.Contains(cm, want) {
			t.Fatalf("config-map missing %q:\n%s", want, cm)
		}
	}
	row := rows["dev"]["order-service"]
	if row["runtime"] != "one2all-mcp" || row["cluster_id"] != "1" || row["configmaps"] != "base-config,app-config" {
		t.Fatalf("unexpected one2all config-map row: %#v", row)
	}
}

func TestGenerate_FrontendEntryMap(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	fm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
	if !strings.Contains(fm, "frontend_entries:") {
		t.Fatalf("frontend-entry-map should contain frontend_entries root, got:\n%s", fm)
	}
	if !strings.Contains(fm, "candidate_downstream:") {
		t.Errorf("frontend-entry-map should include candidate_downstream, got:\n%s", fm)
	}
}

func TestGenerate_FrontendEntryMapIncludesAnalysisEndpoints(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	for i := range cfg.Repos {
		if cfg.Repos[i].Name != "mall-web" {
			cfg.Repos[i].ServiceNames = nil
		}
	}
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, out)
	g.LoadAnalysisReport(analyzer.Report{
		Repos: []analyzer.RepoAnalysis{
			{
				Name: "mall-web",
				Notes: []string{
					"api_endpoint[src/api.ts]=/api/orders",
					"api_endpoint[src/pay.ts]=/api/payments/submit",
					"api_endpoint[src/ignored.ts]=/healthz",
				},
			},
		},
	})
	if err := g.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	fm := readFile(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
	for _, want := range []string{
		`endpoint_paths:`,
		`- "/api/orders"`,
		`- "/api/payments/submit"`,
		`path_candidates:`,
	} {
		if !strings.Contains(fm, want) {
			t.Fatalf("frontend-entry-map missing %q:\n%s", want, fm)
		}
	}
	if strings.Contains(fm, "/healthz") {
		t.Fatalf("frontend-entry-map should not include non-api endpoint:\n%s", fm)
	}
	entries := loadFrontendEntryMap(t, filepath.Join(out, "templates/workspace-template/skills/routing/references/frontend-entry-map.yaml"))
	mallWeb, ok := entries.FrontendEntries["mall-web"]
	if !ok {
		t.Fatalf("frontend-entry-map missing mall-web entry: %#v", entries.FrontendEntries)
	}
	if !stringSliceContains(mallWeb.EndpointPaths, "/api/orders") {
		t.Fatalf("mall-web endpoint_paths missing /api/orders: %#v", mallWeb.EndpointPaths)
	}
	if !stringSliceContains(mallWeb.EndpointPaths, "/api/payments/submit") {
		t.Fatalf("mall-web endpoint_paths missing /api/payments/submit: %#v", mallWeb.EndpointPaths)
	}
	if stringSliceContains(mallWeb.EndpointPaths, "/healthz") {
		t.Fatalf("mall-web endpoint_paths should not contain /healthz: %#v", mallWeb.EndpointPaths)
	}
	orders, ok := mallWeb.PathCandidates["/api/orders"]
	if !ok {
		t.Fatalf("mall-web path_candidates missing /api/orders: %#v", mallWeb.PathCandidates)
	}
	if _, ok := orders.CandidateServices.([]interface{}); !ok {
		t.Fatalf("/api/orders candidate_services should be a list, got %T (%#v)", orders.CandidateServices, orders.CandidateServices)
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

type frontendEntryMapFixture struct {
	FrontendEntries map[string]frontendEntryFixture `yaml:"frontend_entries"`
}

type frontendEntryFixture struct {
	EndpointPaths  []string                        `yaml:"endpoint_paths"`
	PathCandidates map[string]pathCandidateFixture `yaml:"path_candidates"`
}

type pathCandidateFixture struct {
	CandidateServices interface{} `yaml:"candidate_services"`
}

func loadFrontendEntryMap(t *testing.T, path string) frontendEntryMapFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var root frontendEntryMapFixture
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return root
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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

	// plan D:nacos 走 MCP 主路径,config-map 里 nacos 服务标 runtime: nacos-mcp。
	if !strings.Contains(cm, "runtime: nacos-mcp") {
		t.Errorf("config-map should mark nacos services as runtime: nacos-mcp,\ngot:\n%s", cm)
	}
	// 反向护栏:不应再残留 nacos-http(plan D 已切 MCP 主路径;nacos-http 只在 SKILL fallback 文档里提)
	if strings.Contains(cm, "runtime: nacos-http") {
		t.Errorf("config-map 不应再出现 runtime: nacos-http(plan D 已切 nacos-mcp)")
	}

	// mysql 和 kafka 现在已在 shop-system 中启用，验证它们存在
	for _, s := range []string{"mysql-runtime-query", "kafka-runtime-query"} {
		if _, err := os.Stat(filepath.Join(wsRoot, "skills", s)); err != nil {
			t.Errorf("skill %s should be generated: %v", s, err)
		}
	}

	// nacos_mcp.py 是 config-executor 的静态脚本资产,应进产物;但它的 pytest 文件
	// test_nacos_mcp.py 是仓库侧 CI 用的开发产物,generator 必须过滤掉不进 bot workspace。
	ceScripts := filepath.Join(wsRoot, "skills/config-executor/scripts")
	if _, err := os.Stat(filepath.Join(ceScripts, "nacos_mcp.py")); err != nil {
		t.Errorf("nacos_mcp.py should be generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ceScripts, "test_nacos_mcp.py")); err == nil {
		t.Errorf("test_nacos_mcp.py 不应进 bot workspace(generator 应过滤 test_*.py)")
	}

	frontendSkill := filepath.Join(wsRoot, "skills", "frontend-repro-investigator")
	if _, err := os.Stat(filepath.Join(frontendSkill, "SKILL.md")); err != nil {
		t.Errorf("frontend-repro-investigator skill should be generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(frontendSkill, "scripts", "har_analyzer.py")); err != nil {
		t.Errorf("har_analyzer.py should be generated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(frontendSkill, "scripts", "test_har_analyzer.py")); err == nil {
		t.Errorf("test_har_analyzer.py should not be generated")
	}

	ii := readFile(t, filepath.Join(wsRoot, "skills", "incident-investigator", "SKILL.md"))
	if !strings.Contains(ii, "frontend-repro-investigator") {
		t.Errorf("incident-investigator should hand client symptoms to frontend-repro-investigator")
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
	// shop-troubleshooter.yaml 的 skills_whitelist 应完整写入 lock.json。
	wantSkills := []string{
		"routing",
		"incident-investigator",
		"config-executor",
		"redis-runtime-query",
		"mongodb-runtime-query",
		"es-runtime-query",
		"mysql-runtime-query",
		"kafka-runtime-query",
		"tracing-query",
		"elk-log-query",
		"frontend-repro-investigator",
		"diagram-generator",
	}
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

func TestGenerate_FrontendReproArtifacts(t *testing.T) {
	cfg := loadCfg(t, "examples/three-tier-troubleshooter.yaml")
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	root := filepath.Join(out, "templates/workspace-template")
	assertExists(t, root, []string{
		"skills/frontend-repro-investigator/SKILL.md",
		"skills/frontend-repro-investigator/scripts/har_analyzer.py",
		"skills/routing/references/frontend-entry-map.yaml",
	})
	assertNotExists(t, root, []string{
		"skills/frontend-repro-investigator/scripts/test_har_analyzer.py",
	})
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

func assertNotExists(t *testing.T, base string, rels []string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(base, rel)); err == nil {
			t.Fatalf("%s should not exist", rel)
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

	// copyDirRecursive (claude-code / cursor 路径) 也必须过滤 test_*.py。脚本被复制两份:
	// skills/config-executor/scripts/ 和顶层 scripts/,两处都不应出现 test 文件。
	for _, base := range []string{out + "-claude-code", out + "-cursor"} {
		for _, rel := range []string{
			"skills/config-executor/scripts/test_nacos_mcp.py",
			"scripts/test_nacos_mcp.py",
		} {
			if _, err := os.Stat(filepath.Join(base, rel)); err == nil {
				t.Errorf("%s/%s 不应存在(copyDirRecursive 应过滤 test_*.py)", base, rel)
			}
		}
	}
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
