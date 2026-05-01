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
//   - <staging>/templates/workspace-template/tshoot.json(承载 SystemYAML)
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
		SchemaVersion: 1,
		SystemID:      cfg.System.ID,
		SystemName:    cfg.System.Name,
		Target:        "openclaw",
		SystemYAML:    string(yamlBytes),
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

func TestInstallNativeOpenclaw_FreshInstall(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	creds := map[string]string{
		"CC_ADDR_DEV":      "nacos-dev.example.com:8848",
		"CC_ADDR_PROD":     "nacos-prod.example.com:8848",
		"CC_USER_DEV":      "nacos",
		"CC_PASS_DEV":      "nacos-pwd",
		"CC_USER_PROD":     "nacos",
		"CC_PASS_PROD":     "nacos-pwd",
		"GRAFANA_URL_DEV":  "http://grafana-dev.example.com",
		"GRAFANA_URL_PROD": "http://grafana-prod.example.com",
		"GRAFANA_USER_DEV": "admin",
		"GRAFANA_PASS_DEV": "admin-pwd",
		"GRAFANA_USER_PROD": "admin",
		"GRAFANA_PASS_PROD": "admin-pwd",
		"MODEL":            "openai/gpt-5",
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
	servers := getMap(data, "mcp", "servers")
	for _, key := range []string{
		"shop-nacos-mcp-server-dev", "shop-nacos-mcp-server-prod",
		"shop-grafana-mcp-server-dev", "shop-grafana-mcp-server-prod",
	} {
		if _, ok := servers[key]; !ok {
			t.Errorf("mcp.servers missing %s", key)
		}
	}
	// per-env addr 注入正确
	nacDev := servers["shop-nacos-mcp-server-dev"].(map[string]any)["env"].(map[string]any)
	if nacDev["NACOS_ADDR"] != "nacos-dev.example.com:8848" {
		t.Errorf("nacos-mcp-server-dev addr wrong: %v", nacDev["NACOS_ADDR"])
	}
	if nacDev["NACOS_USERNAME"] != "nacos" {
		t.Errorf("nacos-mcp-server-dev username should be CC_USER_DEV(per-env), got %v", nacDev["NACOS_USERNAME"])
	}

	// .env 持久化
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
		"CC_ADDR_MAIN_NACOS_DEV":          "nacos-dev:8848",
		"CC_ADDR_MAIN_NACOS_PROD":         "nacos-prod:8848",
		"CC_USER_MAIN_NACOS_DEV":          "u",
		"CC_PASS_MAIN_NACOS_DEV":          "p",
		"CC_USER_MAIN_NACOS_PROD":         "u",
		"CC_PASS_MAIN_NACOS_PROD":         "p",
		"KUBOARD_URL_LEGACY_KUBOARD_DEV":  "https://kuboard-dev.example.com",
		"KUBOARD_USER_LEGACY_KUBOARD_DEV": "admin",
		"KUBOARD_PASS_LEGACY_KUBOARD_DEV": "secret",
		"KUBOARD_URL_LEGACY_KUBOARD_PROD":  "https://kuboard-prod.example.com",
		"KUBOARD_USER_LEGACY_KUBOARD_PROD": "admin",
		"KUBOARD_PASS_LEGACY_KUBOARD_PROD": "secret",
	}
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{
		Creds: creds, SkipGatewayRestart: true,
	}); err != nil {
		t.Fatal(err)
	}

	// nacos MCP key 带 source.id
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	servers := getMap(data, "mcp", "servers")
	for _, key := range []string{"shop-nacos-mcp-server-main-nacos-dev", "shop-nacos-mcp-server-main-nacos-prod"} {
		if _, ok := servers[key]; !ok {
			t.Errorf("缺多源 nacos MCP key %s; servers=%v", key, mapKeys(servers))
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

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	if _, ok := servers["shop-nacos-mcp-server-dev"]; !ok {
		t.Errorf("install 应注入 nacos-mcp-server-dev")
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
