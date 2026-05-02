// install_e2e_test.go —— 三个 IDE target(claude-code / cursor / codex)装机整链 E2E。
//
// 此前测试格局:
//   - install_native_test.go       只测"文件拷贝"那一步,用手搓的 3 文件 staging
//   - install_native_openclaw_*    OpenClaw 单 target 装得很细
//   - 三 IDE target 各自的 MCP merge / creds.json / 卸载链 没有任何端到端覆盖
//
// 此 E2E 真跑 generator(从 examples/shop-system.yaml 起步),走完整链:
//
//	gen → InstallNative → MergeMCPIntoIDESettings → discover.Scan
//	    → reinstall(.bak) → UninstallNative → 再 Scan 应为空
//
// codex 这条会 shell-out 到 `codex mcp add/remove/list`;为了不依赖真二进制,
// 我们在 PATH 前置一份 Bash 写的 stub,带状态文件模拟真行为。
package agent_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// projectRoot 反推到仓库根(本测试在 internal/agent/),用来定位 templates/ 和 examples/。
func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// loadShopCfg 从 examples/shop-system.yaml 读 cfg。yaml 里 generation.targets 写的是
// openclaw,但 GenerateClaudeCode/Cursor/Codex 不读这个字段,我们直接调它们三个产出
// 三家 staging。yaml 选这份是因为:① workspace_name=shop-bot 是 ASCII,生成的 agent
// 文件名干净;② nacos+grafana+loki 都开了,MCP merge 会真触发(不止派生一两条 server)。
func loadShopCfg(t *testing.T) (*config.SystemConfig, []byte) {
	t.Helper()
	yamlPath := filepath.Join(projectRoot(t), "examples", "shop-system.yaml")
	src, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	cfg, err := config.LoadFromBytes(src)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return cfg, src
}

// buildStaging 跑一次 generator 出三家 IDE 的 staging 目录。返回 staging 绝对路径。
//
// generator 内部约定:OutputDir 传 "<base>",然后 GenerateClaudeCode 会写到
// "<base>-claude-code"。这里 base 用 t.TempDir 的子目录,target 后缀按 generator
// 内部行为算出来。
func buildStaging(t *testing.T, cfg *config.SystemConfig, yamlSrc []byte, target string) string {
	t.Helper()
	tmpl := filepath.Join(projectRoot(t), "templates")
	base := filepath.Join(t.TempDir(), "stage")
	g := generator.New(cfg, tmpl, base)
	g.TshootVersion = "e2e-test"
	g.SystemYAMLSource = yamlSrc

	var err error
	switch target {
	case "claude-code":
		err = g.GenerateClaudeCode()
	case "cursor":
		err = g.GenerateCursor()
	case "codex":
		err = g.GenerateCodex()
	default:
		t.Fatalf("unknown target %q", target)
	}
	if err != nil {
		t.Fatalf("generate %s: %v", target, err)
	}
	staging := base + "-" + target
	if _, err := os.Stat(filepath.Join(staging, "agents")); err != nil {
		t.Fatalf("staging missing agents/: %v", err)
	}
	return staging
}

// installCodexStub 在测试 PATH 前置一个假 `codex` 二进制。它支持本工程会调用的三个子命令:
//
//	codex mcp add <name> [--env K=V ...] [--url <url> | -- <command> <args...>]
//	codex mcp remove <name>
//	codex mcp list
//
// 通过环境变量 STUB_STATE 指向"当前已注册"的 server 名列表文件(每行一个 name)。
// add 追加、remove 删行、list 按 codex 真实输出格式打印("Name URL/Command" 表头 + 每行)。
//
// 返回 stub 所在目录(调用方负责把它前置到 PATH)。
func installCodexStub(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("codex stub uses bash; skip on windows")
	}
	binDir := t.TempDir()
	stubPath := filepath.Join(binDir, "codex")
	stub := `#!/usr/bin/env bash
set -e
state="${STUB_STATE:-/tmp/codex-stub-state}"
log="${STUB_LOG:-/tmp/codex-stub-log}"
echo "$@" >> "$log"
case "$1 $2" in
  "mcp add")
    name="$3"
    [ -f "$state" ] && grep -v "^${name}$" "$state" > "${state}.tmp" 2>/dev/null && mv "${state}.tmp" "$state" || true
    echo "$name" >> "$state"
    exit 0
    ;;
  "mcp remove")
    name="$3"
    if [ -f "$state" ]; then
      grep -v "^${name}$" "$state" > "${state}.tmp" 2>/dev/null || true
      mv "${state}.tmp" "$state" 2>/dev/null || rm -f "$state"
    fi
    exit 0
    ;;
  "mcp list")
    echo "Name URL/Command"
    [ -f "$state" ] && cat "$state"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	// state / log 文件路径 per-test,避免并行冲突
	stateFile := filepath.Join(binDir, "state")
	logFile := filepath.Join(binDir, "log")
	t.Setenv("STUB_STATE", stateFile)
	t.Setenv("STUB_LOG", logFile)
	return binDir
}

// readJSON 读 JSON 文件到 map。文件不存在或解析失败都报 fatal。
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// fakeCreds 拼一份覆盖 shop-system.yaml 里所有派生 env 变量的凭证 map。
// 不全填会触发 buildMCPServersForCfg 的 pruneEmpty,把 env 字段全删空 ——
// 那样我们也无从断言 env 是不是注入了。
func fakeCreds() map[string]string {
	return map[string]string{
		// nacos 单源(id=default 由 LoadFromBytes 注入),env=dev/staging/prod
		// envVar("CC_ADDR", "default", "dev") = "CC_ADDR_DEV"
		"CC_ADDR_DEV":     "nacos-dev:8848",
		"CC_USER_DEV":     "nacos",
		"CC_PASS_DEV":     "nacos-dev-pass",
		"CC_ADDR_STAGING": "nacos-stg:8848",
		"CC_USER_STAGING": "nacos",
		"CC_PASS_STAGING": "nacos-stg-pass",
		"CC_ADDR_PROD":    "nacos-prod:8848",
		"CC_USER_PROD":    "nacos",
		"CC_PASS_PROD":    "nacos-prod-pass",
		// grafana / loki 共用 GRAFANA_*_<ENV>
		"GRAFANA_URL_DEV":      "https://grafana-dev.example",
		"GRAFANA_USER_DEV":     "admin",
		"GRAFANA_PASS_DEV":     "dev-pass",
		"GRAFANA_URL_STAGING":  "https://grafana-stg.example",
		"GRAFANA_USER_STAGING": "admin",
		"GRAFANA_PASS_STAGING": "stg-pass",
		"GRAFANA_URL_PROD":     "https://grafana-prod.example",
		"GRAFANA_USER_PROD":    "admin",
		"GRAFANA_PASS_PROD":    "prod-pass",
	}
}

// expectedMCPKeys 列出 shop-system.yaml 应该派生出来的所有 MCP server key。
// 命名规则见 install_naming.go::mcpKeyForAgent + buildMCPServersForCfg:
//
//	prefix = MCPKeyPrefix() = "shop"
//	nacos  per env(默认源 id=default → 不带 source 中缀):shop-nacos-<env>
//	grafana / loki per env:                           shop-grafana-<env> / shop-loki-<env>
//
// shop-system.yaml 有 dev / staging / prod 三个环境。
func expectedMCPKeys() []string {
	envs := []string{"dev", "staging", "prod"}
	out := []string{}
	for _, e := range envs {
		out = append(out, "shop-nacos-"+e, "shop-grafana-"+e, "shop-loki-"+e)
	}
	return out
}

// TestE2E_IDEInstallChain 把三家 IDE target 都跑一遍 init→gen→install→merge MCP→
// discover→reinstall→uninstall→discover 的整链。
func TestE2E_IDEInstallChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install chain uses unix paths + bash codex stub")
	}
	cfg, yamlSrc := loadShopCfg(t)
	expectedKeys := expectedMCPKeys()

	for _, target := range []string{"claude-code", "cursor", "codex"} {
		t.Run(target, func(t *testing.T) {
			fakeHome := t.TempDir()
			t.Setenv("HOME", fakeHome)

			// codex 走 CLI 注册,装个 stub 接管
			if target == "codex" {
				stubDir := installCodexStub(t)
				t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				// sanity: which codex 应该指向 stub
				if path, err := exec.LookPath("codex"); err != nil || !strings.HasPrefix(path, stubDir) {
					t.Fatalf("codex stub not on PATH: path=%s err=%v", path, err)
				}
			}

			// ── 1) generator 出 staging ───────────────────────────────────────
			staging := buildStaging(t, cfg, yamlSrc, target)

			// ── 2) InstallNative:把 staging 拷到 fakeHome/.<root>/ ─────────────
			if err := agent.InstallNative(staging, target); err != nil {
				t.Fatalf("InstallNative: %v", err)
			}
			rootDir := filepath.Join(fakeHome, "."+rootName(target))
			agentName := cfg.ResolveID() // shop-system.yaml workspace_name=shop-bot
			// agent .md / skills / scripts / tshoot.json 锚点
			mustExist(t, filepath.Join(rootDir, "agents", agentName+".md"))
			mustExist(t, filepath.Join(rootDir, "skills", agentName))
			mustExist(t, filepath.Join(rootDir, "skills", agentName, discover.MetaFilename))

			// ── 3) MergeMCPIntoIDESettings:写 settings/mcp.json/codex toml ────
			creds := fakeCreds()
			if err := agent.MergeMCPIntoIDESettings(target, cfg, creds); err != nil {
				t.Fatalf("MergeMCPIntoIDESettings: %v", err)
			}

			// 验证三家各自的写入位置
			switch target {
			case "claude-code":
				assertJSONHasMCPKeys(t, filepath.Join(rootDir, "settings.json"), expectedKeys)
			case "cursor":
				assertJSONHasMCPKeys(t, filepath.Join(rootDir, "mcp.json"), expectedKeys)
			case "codex":
				// stub 把 add 调用记到 STUB_STATE 文件;一行一个 server 名
				stateFile := os.Getenv("STUB_STATE")
				assertCodexStateHasKeys(t, stateFile, expectedKeys)
			}

			// ── 4) discover.Scan:BotsPage 同款扫描应该能找到这个机器人 ─────────
			scanRoot := filepath.Join(rootDir, "skills")
			agents, err := discover.Scan([]string{scanRoot})
			if err != nil {
				t.Fatalf("discover.Scan: %v", err)
			}
			if len(agents) != 1 {
				t.Fatalf("scan 应找到 1 个机器人,实际 %d", len(agents))
			}
			if agents[0].Meta.SystemID != cfg.System.ID || agents[0].Meta.Target != target {
				t.Errorf("scan meta 不对:%+v", agents[0].Meta)
			}
			installedDir := agents[0].Path

			// ── 5) reinstall:再装一次,旧 agent .md 应被备份成 .bak.<ts> ────────
			if err := agent.InstallNative(staging, target); err != nil {
				t.Fatalf("reinstall: %v", err)
			}
			bakMatches, _ := filepath.Glob(filepath.Join(rootDir, "agents", agentName+".md.bak.*"))
			if len(bakMatches) == 0 {
				t.Errorf("reinstall 应生成 .bak.<ts> 备份,实际为空")
			}

			// ── 6) UninstallNative:文件清掉 + IDE MCP 配置摘掉 ────────────────
			res, err := agent.UninstallNative(installedDir, target)
			if err != nil {
				t.Fatalf("UninstallNative: %v", err)
			}
			// agent .md 应该被删
			if _, err := os.Stat(filepath.Join(rootDir, "agents", agentName+".md")); !os.IsNotExist(err) {
				t.Errorf("uninstall 后 agent .md 还在")
			}
			// .bak 也应清干净
			leftover, _ := filepath.Glob(filepath.Join(rootDir, "agents", agentName+".md.bak.*"))
			if len(leftover) != 0 {
				t.Errorf("uninstall 应清掉 .bak 备份,残留 %v", leftover)
			}
			// skills/<name>/ 整目录被搬到 ~/.Trash(或 RemoveAll fallback);现位置应不存在
			if _, err := os.Stat(installedDir); !os.IsNotExist(err) {
				t.Errorf("uninstall 后 skills/%s/ 还在", agentName)
			}
			// MCP 摘除清单非空
			if len(res.MCPRemoved) == 0 {
				t.Errorf("uninstall MCPRemoved 应非空(系统派生了 %d 个 server)", len(expectedKeys))
			}

			// 三家各自二次断言:settings.json / mcp.json / codex stub state 里 prefix 全清
			switch target {
			case "claude-code":
				assertJSONNoMCPPrefix(t, filepath.Join(rootDir, "settings.json"), cfg.System.ID+"-")
			case "cursor":
				assertJSONNoMCPPrefix(t, filepath.Join(rootDir, "mcp.json"), cfg.System.ID+"-")
			case "codex":
				stateFile := os.Getenv("STUB_STATE")
				assertCodexStateNoPrefix(t, stateFile, cfg.System.ID+"-")
			}

			// ── 7) 再 scan:应零结果 ───────────────────────────────────────────
			agents2, err := discover.Scan([]string{scanRoot})
			if err != nil {
				t.Fatalf("post-uninstall scan: %v", err)
			}
			if len(agents2) != 0 {
				t.Errorf("uninstall 后 scan 应 0 结果,实际 %d", len(agents2))
			}
		})
	}
}

// rootName 把 IDE target 映射到 ~/.<这里>/ 的目录名。
// 跟 cmd/tshoot/install.go::iderootName 同款,只是测试侧不引 main 包。
func rootName(target string) string {
	switch target {
	case "claude-code":
		return "claude"
	case "cursor":
		return "cursor"
	case "codex":
		return "codex"
	}
	return target
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected path missing: %s (%v)", path, err)
	}
}

// assertJSONHasMCPKeys:settings.json / mcp.json 顶层 mcpServers map 应包含全部期望 key,
// 且每个 server 对象有 command / env 字段。
func assertJSONHasMCPKeys(t *testing.T, path string, want []string) {
	t.Helper()
	root := readJSON(t, path)
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("%s 缺 mcpServers map", path)
	}
	for _, k := range want {
		spec, ok := servers[k].(map[string]any)
		if !ok {
			t.Errorf("%s 缺 mcpServers[%q]", path, k)
			continue
		}
		if _, ok := spec["command"].(string); !ok {
			t.Errorf("%s mcpServers[%q] 缺 command 字段", path, k)
		}
		// env 应有内容(creds 拼全了,pruneEmpty 不该全删空)
		envMap, _ := spec["env"].(map[string]any)
		if len(envMap) == 0 {
			t.Errorf("%s mcpServers[%q] env 为空,凭证注入没生效", path, k)
		}
	}
}

// assertJSONNoMCPPrefix:settings.json / mcp.json 里 mcpServers 不应再有任何
// 以 prefix 开头的 key(uninstall 应已清干净)。
func assertJSONNoMCPPrefix(t *testing.T, path, prefix string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		// 没文件 = 没 MCP 残留,通过
		return
	}
	root := readJSON(t, path)
	servers, _ := root["mcpServers"].(map[string]any)
	for k := range servers {
		if strings.HasPrefix(k, prefix) {
			t.Errorf("%s 残留未清掉的 MCP key:%s", path, k)
		}
	}
}

// assertCodexStateHasKeys 验证 codex stub 的 state 文件包含全部期望 server 名。
func assertCodexStateHasKeys(t *testing.T, stateFile string, want []string) {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read codex stub state %s: %v", stateFile, err)
	}
	got := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		got[line] = true
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("codex 没 add 注册 %q;实际:%v", k, got)
		}
	}
}

// assertCodexStateNoPrefix 验证 stub state 里没有 prefix 开头的残留(uninstall 后)。
func assertCodexStateNoPrefix(t *testing.T, stateFile, prefix string) {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return // 没文件 = 已清空
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, prefix) {
			t.Errorf("codex 残留未清掉的 server: %s", line)
		}
	}
}

// TestE2E_ApolloCredsFile 单独覆盖一条 shop-system 那条链不会触发的分支:
//
//	WriteIDECredsFile —— apollo / consul / env-vars / kuboard 才会写
//	~/.tshoot/<agent_id>-creds.json,nacos-only(shop)直接 skip。
//
// 这条文件给 OpenClaw 那批"非 MCP 走脚本"的 skill 用(apollo_config.py /
// consul_config.py / kuboard 配套),IDE 平台部署时也得镜像写一份,否则脚本报
// "creds file missing"。
//
// 装 claude-code 一份就够了 —— 三家共享 WriteIDECredsFile,文件位置
// 跟 target 无关(都在 ~/.tshoot/ 下)。
func TestE2E_ApolloCredsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix paths")
	}
	yamlPath := filepath.Join(projectRoot(t), "examples", "apollo-system.yaml")
	src, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read apollo fixture: %v", err)
	}
	cfg, err := config.LoadFromBytes(src)
	if err != nil {
		t.Fatalf("parse apollo fixture: %v", err)
	}

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	staging := buildStaging(t, cfg, src, "claude-code")
	if err := agent.InstallNative(staging, "claude-code"); err != nil {
		t.Fatalf("InstallNative: %v", err)
	}

	// apollo 单源(id=default),env=dev/prod;envVar 拼出来的 key 是 APOLLO_META_DEV 等
	creds := map[string]string{
		"APOLLO_META_DEV":   "https://apollo-dev.example",
		"APOLLO_TOKEN_DEV":  "dev-token",
		"APOLLO_META_PROD":  "https://apollo-prod.example",
		"APOLLO_TOKEN_PROD": "prod-token",
	}
	if err := agent.WriteIDECredsFile(cfg, creds); err != nil {
		t.Fatalf("WriteIDECredsFile: %v", err)
	}

	// apollo-system.yaml: system.id=bank, 无 agent.id / workspace_name → ResolveID = bank-troubleshooter
	credsPath := filepath.Join(fakeHome, ".tshoot", cfg.ResolveID()+"-creds.json")
	data, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("creds.json missing at %s: %v", credsPath, err)
	}

	// 顶层应有 apollo section,里面有 dev/prod 两 env(单源 sourceID=default 走老两层结构)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse creds.json: %v", err)
	}
	apolloSec, ok := root["apollo"].(map[string]any)
	if !ok {
		t.Fatalf("creds.json 缺顶层 apollo section: %s", string(data))
	}
	for _, env := range []string{"dev", "prod"} {
		entry, ok := apolloSec[env].(map[string]any)
		if !ok {
			t.Errorf("apollo.%s section 缺失;raw: %s", env, string(data))
			continue
		}
		if got, _ := entry["meta_url"].(string); got == "" {
			t.Errorf("apollo.%s.meta_url 应非空", env)
		}
		if got, _ := entry["token"].(string); got == "" {
			t.Errorf("apollo.%s.token 应非空", env)
		}
	}

	// 文件权限应是 0600(凭证文件,不能给同机器其它用户读)
	st, err := os.Stat(credsPath)
	if err == nil && st.Mode().Perm() != 0o600 {
		t.Errorf("creds.json 权限应 0600,实际 %o", st.Mode().Perm())
	}
}
