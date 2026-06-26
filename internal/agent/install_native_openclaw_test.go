package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// setupOpenclawStaging 给 InstallNativeOpenclaw 准备最小可跑的 staging:
//   - <staging>/templates/workspace-template/SOUL.md(workspace 占位文件,
//     install 拷过去后能让 InstallNative 走完文件复制路径)
//   - <staging>/templates/workspace-template/tshoot.json(承载 TroubleshooterYAML)
//
// 同时把 t.Setenv("HOME") 指向新 tmp,把所有 ~/.openclaw 落地到 t.TempDir(),
// 测试结束自动清。
func setupOpenclawStaging(t *testing.T, cfg *config.SystemConfig) (stagingDir, fakeHome string) {
	t.Helper()
	fakeHome = t.TempDir()
	t.Setenv("HOME", fakeHome)

	stagingDir = filepath.Join(t.TempDir(), "staging")
	wsTpl := filepath.Join(stagingDir, "templates", "workspace-template")
	if err := os.MkdirAll(wsTpl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsTpl, "SOUL.md"), []byte("# SOUL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	meta := discover.Meta{
		SchemaVersion:      1,
		SystemID:           cfg.System.ID,
		SystemName:         cfg.System.Name,
		Target:             "openclaw",
		TroubleshooterYAML: string(yamlBytes),
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(wsTpl, discover.MetaFilename), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

// nacosCfg:典型的 nacos shared-creds + grafana enabled cfg,覆盖 install 的
// agents.list / mcp.servers / .env 写入主路径。带必备的 meta.schema_version
// (config.LoadFromBytes 验证用)。
func nacosCfg() *config.SystemConfig {
	cfg := &config.SystemConfig{
		System: config.System{ID: "shop", Name: "Shop"},
		Agent: config.Agent{
			// 不显式设 WorkspaceName / ID,让 ResolveID() 走 "<system.id>-troubleshooter"
			// 兜底,跟新 wizard 默认行为一致(shop → shop-troubleshooter)。
			Name:  "Shop Bot",
			Model: "openai/gpt-4",
		},
		Environments: []config.Environment{
			{ID: "dev"}, {ID: "prod"},
		},
		Meta: config.Meta{SchemaVersion: "0.1"},
	}
	cfg.Infrastructure.ConfigCenter = config.ConfigCenter{Type: "nacos"}
	cfg.Infrastructure.Observability.Grafana = config.Grafana{Enabled: true}
	return cfg
}

// TestFindOpenclawCLI_PathHit:CLI 在 PATH 里时 LookPath 直接命中,不进 fallback。
// 主流场景:用户从 shell 跑 tshoot install,zsh PATH 含 /opt/homebrew/bin。
func TestFindOpenclawCLI_PathHit(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "openclaw")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := findOpenclawCLI()
	if got != binPath {
		t.Errorf("findOpenclawCLI() = %q, want %q", got, binPath)
	}
}

// TestFindOpenclawCLI_AllMiss:PATH 不含 + 所有 fallback 绝对路径也不存在时
// 返回空串(让上层走 [info] 文案分支,不 panic 不报 error)。
// 用 t.Setenv("HOME") 指向 empty tempdir 让 ~/.npm-global 和 ~/.local 候选 miss;
// /opt/homebrew/bin/openclaw 和 /usr/local/bin/openclaw 在 CI runner 上一般不存在。
// 本地开发机若装了 openclaw 会让这条 assert 失败 — 用 t.Skip 兜底,不死锁本地跑。
func TestFindOpenclawCLI_AllMiss(t *testing.T) {
	for _, p := range []string{"/opt/homebrew/bin/openclaw", "/usr/local/bin/openclaw"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("本机 %s 存在,跳过 all-miss assert(主流装机场景,不验空串路径)", p)
		}
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	if got := findOpenclawCLI(); got != "" {
		t.Errorf("findOpenclawCLI() = %q, want \"\"", got)
	}
}

func TestInstallNativeOpenclaw_FreshInstall(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	creds := map[string]string{
		"CC_ADDR_DEV":       "nacos-dev.example.com:8848",
		"CC_ADDR_PROD":      "nacos-prod.example.com:8848",
		"CC_USER_DEV":       "nacos",
		"CC_PASS_DEV":       "nacos-pwd",
		"CC_USER_PROD":      "nacos",
		"CC_PASS_PROD":      "nacos-pwd",
		"GRAFANA_URL_DEV":   "http://grafana-dev.example.com",
		"GRAFANA_URL_PROD":  "http://grafana-prod.example.com",
		"GRAFANA_USER_DEV":  "admin",
		"GRAFANA_PASS_DEV":  "admin-pwd",
		"GRAFANA_USER_PROD": "admin",
		"GRAFANA_PASS_PROD": "admin-pwd",
		"MODEL":             "openai/gpt-5",
	}
	err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{Creds: creds, SkipGatewayRestart: true})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// workspace 装到 ~/.openclaw/workspace/<name>/
	wsDir := filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")
	if _, err := os.Stat(filepath.Join(wsDir, "SOUL.md")); err != nil {
		t.Errorf("workspace SOUL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, discover.MetaFilename)); err != nil {
		t.Errorf("workspace tshoot.json missing: %v", err)
	}

	// openclaw.json 应有 agents.list[shop-troubleshooter] + mcp.servers
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	// H1 回归护栏:openclaw.json 的 mcp.servers env 段含 plaintext creds,必须 0o600
	// (对齐 IDE 三家 install_e2e_test.go 的 0o600 断言)。world-readable 0o644 是真 leak。
	if st, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("stat openclaw.json: %v", err)
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf("openclaw.json 权限必须 0o600(含 plaintext creds),got %o", st.Mode().Perm())
	}
	data := readJSON(t, cfgPath)
	agents := getList(data, "agents", "list")
	if len(agents) != 1 {
		t.Fatalf("agents.list want 1 entry, got %d: %+v", len(agents), agents)
	}
	a := agents[0].(map[string]any)
	if a["id"] != "shop-troubleshooter" {
		t.Errorf("agent.id want shop-troubleshooter, got %v", a["id"])
	}
	if a["model"] != "openai/gpt-5" {
		t.Errorf("agent.model want openai/gpt-5(creds 优先), got %v", a["model"])
	}
	if !strings.HasSuffix(a["workspace"].(string), "/.openclaw/workspace/shop-troubleshooter") {
		t.Errorf("agent.workspace path wrong: %v", a["workspace"])
	}

	// mcp.servers per env
	//
	// plan D(本次):nacos 走自研本地 MCP 脚本 `uv run --script ~/.tshoot/scripts/nacos_mcp.py`,
	// 脚本运行时自己 login + refresh。所以 grafana 和 nacos mcp 都应注册。详见
	// install_native_mcp_common.go::BuildMCPServers 决策注释。
	servers := getMap(data, "mcp", "servers")
	for _, key := range []string{
		"shop-grafana-dev", "shop-grafana-prod",
		"shop-nacos-dev", "shop-nacos-prod",
	} {
		if _, ok := servers[key]; !ok {
			t.Errorf("mcp.servers missing %s", key)
		}
	}
	// nacos mcp 应走 uv script,凭据在 env,绝不 bake access_token(防回到 23d503a)
	if nacosSpec, ok := servers["shop-nacos-dev"].(map[string]any); ok {
		if nacosSpec["command"] != "uv" {
			t.Errorf("nacos mcp command 应为 uv,got %v", nacosSpec["command"])
		}
		args, _ := nacosSpec["args"].([]any)
		for _, a := range args {
			if s, _ := a.(string); strings.Contains(s, "access_token") {
				t.Errorf("nacos mcp args 不应含 access_token(plan D 走 env + 运行时 refresh): %v", args)
			}
		}
	}

	// .env 持久化(nacos 凭据走 env → Python HTTP API 运行时读)
	envFile := filepath.Join(staging, "scripts", ".env")
	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Errorf("scripts/.env missing: %v", err)
	}
	if !strings.Contains(string(envBytes), "CC_ADDR_DEV='nacos-dev.example.com:8848'") {
		t.Errorf(".env should contain creds:\n%s", envBytes)
	}
	info, _ := os.Stat(envFile)
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode want 0600, got %o", info.Mode().Perm())
	}
}

func TestInstallNativeOpenclaw_ReinstallBackupsWorkspace(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	// 先装一次,再造一份"用户的本地手改"放到 workspace 里,然后再装一次
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	wsDir := filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")
	userMark := filepath.Join(wsDir, "USER_EDIT.md")
	if err := os.WriteFile(userMark, []byte("user kept this"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}

	// 新 workspace 不该再有 USER_EDIT.md(被搬走 + 重铺模板)
	if _, err := os.Stat(userMark); !os.IsNotExist(err) {
		t.Errorf("re-install should replace workspace; USER_EDIT.md still there: err=%v", err)
	}
	// 旧 workspace 应该出现在 ~/.Trash/<id>-troubleshooter-workspace-<ts>/
	trash := filepath.Join(fakeHome, ".Trash")
	entries, _ := os.ReadDir(trash)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "shop-troubleshooter-workspace-") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("旧 workspace 应当被搬到 ~/.Trash/shop-troubleshooter-workspace-<ts>/, 但 trash 目录下没找到匹配项: %v", entries)
	}
}

func TestInstallNativeOpenclaw_AgentNotDuplicated(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	agents := getList(data, "agents", "list")
	if len(agents) != 1 {
		t.Errorf("二次 install 后 agents.list 应仍只有 1 条(去重),got %d", len(agents))
	}
}

func TestInstallNativeOpenclaw_CredsJSONForApollo(t *testing.T) {
	cfg := &config.SystemConfig{
		System: config.System{ID: "x", Name: "X"},
		Agent: config.Agent{
			Name: "X Bot", Model: "openai/gpt-4",
		},
		Environments: []config.Environment{{ID: "dev"}},
		Meta:         config.Meta{SchemaVersion: "0.1"},
	}
	cfg.Infrastructure.ConfigCenter = config.ConfigCenter{Type: "apollo"}
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	creds := map[string]string{
		"APOLLO_META_DEV":  "http://apollo-dev:8080",
		"APOLLO_TOKEN_DEV": "abc",
	}
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{Creds: creds, SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}

	// apollo cc 应写 ~/.openclaw/<id>-troubleshooter-creds.json
	credsPath := filepath.Join(fakeHome, ".openclaw", "x-troubleshooter-creds.json")
	cdata := readJSON(t, credsPath)
	apollo, ok := cdata["apollo"].(map[string]any)
	if !ok {
		t.Fatalf("creds.json missing apollo section: %+v", cdata)
	}
	dev, ok := apollo["dev"].(map[string]any)
	if !ok {
		t.Fatalf("apollo.dev missing")
	}
	if dev["meta_url"] != "http://apollo-dev:8080" {
		t.Errorf("apollo.dev.meta_url want http://apollo-dev:8080, got %v", dev["meta_url"])
	}
	if dev["token"] != "abc" {
		t.Errorf("apollo.dev.token want abc(per-env APOLLO_TOKEN_DEV), got %v", dev["token"])
	}

	// creds.json 必须 0600
	info, err := os.Stat(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("creds.json mode want 0600, got %o", info.Mode().Perm())
	}
}

func TestInstallNativeOpenclaw_NacosNoCredsFile(t *testing.T) {
	// nacos type 凭证全跑 mcp.servers env,不应额外写 creds.json
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(fakeHome, ".openclaw", "shop-troubleshooter-creds.json")
	if _, err := os.Stat(credsPath); !os.IsNotExist(err) {
		t.Errorf("nacos cc 不应该写 creds.json, but got err=%v", err)
	}
}

// 多源场景:nacos + kuboard 同时注册,MCP key 带 source.id 命名空间不冲突,
// kuboard 走 creds.json 的三层结构(kuboard.<source>.<env>),nacos 走 mcp.servers
func TestInstallNativeOpenclaw_MultiSourceMix(t *testing.T) {
	cfg := nacosCfg() // 已带 nacos id="" + grafana enabled
	// 替换为显式多源:main-nacos + legacy-kuboard;kuboard 的 cluster/ns/cm 走 service_map per-service
	cfg.Infrastructure.ConfigCenters = []config.ConfigCenter{
		{ID: "main-nacos", Type: "nacos"},
		{
			ID:   "legacy-kuboard",
			Type: "kuboard",
			ServiceMap: map[string]map[string]config.ServiceMapEntry{
				"dev": {
					"user": {Cluster: "dev-cluster", Namespace: "default", ConfigMap: "app-config"},
				},
				"prod": {
					"user": {Cluster: "prod-cluster", Namespace: "prod", ConfigMap: "app-config"},
				},
			},
		},
	}
	cfg.Infrastructure.ConfigCenter = config.ConfigCenter{} // 清掉避免重复 marshal
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	creds := map[string]string{
		"CC_ADDR_MAIN_NACOS_DEV":           "nacos-dev:8848",
		"CC_ADDR_MAIN_NACOS_PROD":          "nacos-prod:8848",
		"CC_USER_MAIN_NACOS_DEV":           "u",
		"CC_PASS_MAIN_NACOS_DEV":           "p",
		"CC_USER_MAIN_NACOS_PROD":          "u",
		"CC_PASS_MAIN_NACOS_PROD":          "p",
		"KUBOARD_URL_LEGACY_KUBOARD_DEV":   "https://kuboard-dev.example.com",
		"KUBOARD_USER_LEGACY_KUBOARD_DEV":  "admin",
		"KUBOARD_PASS_LEGACY_KUBOARD_DEV":  "secret",
		"KUBOARD_URL_LEGACY_KUBOARD_PROD":  "https://kuboard-prod.example.com",
		"KUBOARD_USER_LEGACY_KUBOARD_PROD": "admin",
		"KUBOARD_PASS_LEGACY_KUBOARD_PROD": "secret",
	}
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{
		Creds: creds, SkipGatewayRestart: true,
	}); err != nil {
		t.Fatal(err)
	}

	// plan D:nacos 走自研本地 MCP 脚本(单源 / 多源都注册,每 source×env 一个实例)。
	// 多源场景 source ID = "main-nacos",所以 key 形如 shop-nacos-main-nacos-<env>。
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	servers := getMap(data, "mcp", "servers")
	for _, key := range []string{
		"shop-nacos-main-nacos-dev", "shop-nacos-main-nacos-prod",
	} {
		if _, ok := servers[key]; !ok {
			t.Errorf("plan D 下多源 nacos 应注册 mcp,缺 %s(servers=%v)", key, keysOf(servers))
		}
	}
	// legacy-kuboard 不应注册 MCP(kuboard 走 creds.json + python script)
	for k := range servers {
		if strings.Contains(k, "legacy-kuboard") {
			t.Errorf("kuboard 不该走 MCP,但意外注册了 %s", k)
		}
	}

	// kuboard 凭证写到 ~/.openclaw/<id>-troubleshooter-creds.json,顶层 kuboard 三层
	credsPath := filepath.Join(fakeHome, ".openclaw", "shop-troubleshooter-creds.json")
	cdata := readJSON(t, credsPath)
	kbTop, ok := cdata["kuboard"].(map[string]any)
	if !ok {
		t.Fatalf("creds.json 缺 kuboard section: %+v", cdata)
	}
	bySource, ok := kbTop["legacy-kuboard"].(map[string]any)
	if !ok {
		t.Fatalf("creds.json kuboard.legacy-kuboard 缺失;got=%+v", kbTop)
	}
	dev, ok := bySource["dev"].(map[string]any)
	if !ok {
		t.Fatalf("creds.json kuboard.legacy-kuboard.dev 缺失;got=%+v", bySource)
	}
	if dev["url"] != "https://kuboard-dev.example.com" || dev["username"] != "admin" || dev["password"] != "secret" {
		t.Errorf("kuboard creds 顶层字段错: %+v", dev)
	}
	// cluster/namespace/configmap 走 service_map per-service
	svcMap, ok := dev["service_map"].(map[string]any)
	if !ok {
		t.Fatalf("kuboard creds.dev.service_map 缺失;got=%+v", dev)
	}
	user, ok := svcMap["user"].(map[string]any)
	if !ok {
		t.Fatalf("kuboard creds.dev.service_map.user 缺失;got=%+v", svcMap)
	}
	if user["cluster"] != "dev-cluster" || user["namespace"] != "default" || user["configmap"] != "app-config" {
		t.Errorf("kuboard creds.dev.service_map.user 错: %+v", user)
	}
}

func TestInstallNativeOpenclaw_PreservesExistingAgents(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	// 预置一份 openclaw.json,里面已经有别的 agent + 别的 MCP server
	preExisting := map[string]any{
		"agents": map[string]any{
			"list": []any{
				map[string]any{"id": "other-agent", "name": "Other", "model": "x", "workspace": "/tmp/other"},
			},
		},
		"mcp": map[string]any{
			"servers": map[string]any{
				"unrelated-mcp": map[string]any{"command": "echo"},
			},
		},
	}
	cfgDir := filepath.Join(fakeHome, ".openclaw")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mb, _ := json.MarshalIndent(preExisting, "", "  ")
	if err := os.WriteFile(filepath.Join(cfgDir, "openclaw.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}

	data := readJSON(t, filepath.Join(cfgDir, "openclaw.json"))
	agents := getList(data, "agents", "list")
	if len(agents) != 2 {
		t.Errorf("install 应保留 other-agent + 加 shop-troubleshooter, agents.list len want 2 got %d: %+v", len(agents), agents)
	}
	servers := getMap(data, "mcp", "servers")
	if _, ok := servers["unrelated-mcp"]; !ok {
		t.Errorf("install 不应清掉无关的 unrelated-mcp")
	}
	if _, ok := servers["shop-grafana-dev"]; !ok {
		t.Errorf("install 应注入 shop-grafana-dev")
	}
	// plan D:nacos 走本地 MCP 脚本。openclaw PruneEmpty=false 留全 schema 让 agent 自决填
	// 凭据,所以即使本次没传 creds,nacos mcp 也应注册(空 env 占位)。
	if _, ok := servers["shop-nacos-dev"]; !ok {
		t.Errorf("plan D 下应注入 nacos mcp,但 shop-nacos-dev 缺失(servers=%v)", keysOf(servers))
	}
}

// ── 测试小工具 ──

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func getMap(root map[string]any, keys ...string) map[string]any {
	cur := root
	for _, k := range keys {
		next, _ := cur[k].(map[string]any)
		if next == nil {
			return map[string]any{}
		}
		cur = next
	}
	return cur
}

func getList(root map[string]any, keys ...string) []any {
	if len(keys) == 0 {
		return nil
	}
	parent := getMap(root, keys[:len(keys)-1]...)
	list, _ := parent[keys[len(keys)-1]].([]any)
	return list
}
