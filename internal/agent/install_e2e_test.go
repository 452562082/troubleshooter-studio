// install_e2e_test.go —— 三个 IDE target(claude-code / cursor / codex)装机整链 E2E。
//
// 此前测试格局:
//   - install_native_test.go       只测"文件拷贝"那一步,用手搓的 3 文件 staging
//   - install_native_openclaw_*    OpenClaw 单 target 装得很细
//   - 三 IDE target 各自的 MCP merge / creds.json / 卸载链 没有任何端到端覆盖
//
// 此 E2E 真跑 generator(从 examples/shop-troubleshooter.yaml 起步),走完整链:
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

// loadShopCfg 从 examples/shop-troubleshooter.yaml 读 cfg。yaml 里 generation.targets 写的是
// openclaw,但 GenerateClaudeCode/Cursor/Codex 不读这个字段,我们直接调它们三个产出
// 三家 staging。yaml 选这份是因为:① workspace_name=shop-bot 是 ASCII,生成的 agent
// 文件名干净;② nacos+grafana+loki 都开了,MCP merge 会真触发(不止派生一两条 server)。
func loadShopCfg(t *testing.T) (*config.SystemConfig, []byte) {
	t.Helper()
	yamlPath := filepath.Join(projectRoot(t), "examples", "shop-troubleshooter.yaml")
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
	g.TroubleshooterYAMLSource = yamlSrc

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
	// 三家 staging 都是 agents/<NAME>.<ext>(claude/cursor=.md;codex=.toml)。
	if _, err := os.Stat(filepath.Join(staging, "agents")); err != nil {
		t.Fatalf("staging missing agents/: %v", err)
	}
	return staging
}

// (旧版 codex CLI stub 已删除:codex MCP 现在嵌入 ~/.codex/agents/<name>.toml 内联段,
//  不再走 `codex mcp add/remove/list`。e2e 直接读 agent toml 文件断言。)

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

// fakeCreds 拼一份覆盖 shop-troubleshooter.yaml 里所有派生 env 变量的凭证 map。
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

// expectedMCPKeys 列出 shop-troubleshooter.yaml 应该派生出来的所有 MCP server key。
// 命名规则见 install_naming.go::mcpKeyForAgent + buildMCPServersForCfg:
//
//	prefix = MCPKeyPrefix() = "shop"
//	grafana per env: shop-grafana-<env>
//
// 注 1:loki MCP 已合并进 grafana MCP(2026-05),query_loki_* 工具由 grafana mcp-grafana-npx
// 提供;不再单独注册 shop-loki-<env>。
//
// 注 2:nacos per env(plan D):自研本地 MCP 脚本 `uv run --script nacos_mcp.py`。
// fakeCreds 给齐三个 env 的 CC_ADDR/USER/PASS,IDE PruneEmpty 下凭据全 → 三个 env 都注册。
// 详见 install_native_mcp_common.go::BuildMCPServers。
//
// shop-troubleshooter.yaml 有 dev / staging / prod 三个环境。
func expectedMCPKeys() []string {
	envs := []string{"dev", "staging", "prod"}
	out := []string{}
	for _, e := range envs {
		out = append(out, "shop-grafana-"+e)
		out = append(out, "shop-nacos-"+e)
	}
	return out
}

func expectedAgentNames(cfg *config.SystemConfig) []string {
	return []string{cfg.ResolveID(), cfg.System.ID + "-validator", cfg.System.ID + "-fixer"}
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

			// claude-code 迁移路径覆盖:模拟"装过老版本"——预先在
			// ~/.claude/settings.json 里塞一个跟即将派生的 key 同名的 mcpServer
			// (老 bug 把 MCP 写到这里,Claude Code 看不到)。装完新版后这个残留
			// 应被清掉(避免新位置 .claude.json 跟老位置 settings.json 并存,导致
			// 卸载时漏清)。
			if target == "claude-code" {
				oldDir := filepath.Join(fakeHome, ".claude")
				if err := os.MkdirAll(oldDir, 0o755); err != nil {
					t.Fatalf("mkdir legacy claude dir: %v", err)
				}
				legacy := map[string]any{
					"hooks": map[string]any{"keep": "this"}, // 非 MCP 字段必须保留
					"mcpServers": map[string]any{
						"shop-grafana-dev": map[string]any{"command": "stale-bin"},
						"user-custom-mcp":  map[string]any{"command": "user-keeps-this"},
					},
				}
				data, _ := json.MarshalIndent(legacy, "", "  ")
				if err := os.WriteFile(filepath.Join(oldDir, "settings.json"), data, 0o644); err != nil {
					t.Fatalf("seed legacy settings.json: %v", err)
				}
			}

			// codex 不再走 CLI 注入(MCP 嵌入 agent toml 内联段),无需 stub。
			// grafana/loki MCP 现在走 npx mcp-grafana-npx,IDE 启动时按需拉,不在 install 时下二进制 — 测试不再需要 fake binary。

			// ── 1) generator 出 staging ───────────────────────────────────────
			staging := buildStaging(t, cfg, yamlSrc, target)

			// ── 2) InstallNative:把 staging 拷到 fakeHome/.<root>/ ─────────────
			if err := agent.InstallNative(staging, target); err != nil {
				t.Fatalf("InstallNative: %v", err)
			}
			rootDir := filepath.Join(fakeHome, "."+rootName(target))
			agentNames := expectedAgentNames(cfg)
			// agent 人格文件 / skills / scripts / tshoot.json 锚点。
			// claude-code / cursor → agents/<name>.md;codex → agents/<name>.toml
			for _, name := range agentNames {
				mustExist(t, agentMDLocationFor(rootDir, target, name))
				mustExist(t, filepath.Join(rootDir, "skills", name))
				metaPath := filepath.Join(rootDir, "skills", name, discover.MetaFilename)
				if name == cfg.ResolveID() {
					mustExist(t, metaPath)
				} else if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
					t.Fatalf("internal validator agent should not expose discover meta: %s", metaPath)
				}
			}

			// ── 3) MergeMCPIntoIDESettings:写 settings/mcp.json/codex toml ────
			creds := fakeCreds()
			if err := agent.MergeMCPIntoIDESettings(target, cfg, creds, nil); err != nil {
				t.Fatalf("MergeMCPIntoIDESettings: %v", err)
			}

			// 验证三家各自的写入位置 + 文件 mode 必须 0o600
			// (mcpServers env 段嵌入 plaintext creds,world-readable 0o644 是真 leak)
			assertFileMode := func(t *testing.T, path string, want os.FileMode) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat %s: %v", path, err)
				}
				if got := info.Mode().Perm(); got != want {
					t.Errorf("%s mode want %#o, got %#o(plaintext creds 文件不能 world-readable)", path, want, got)
				}
			}
			switch target {
			case "claude-code":
				// 注意:claude-code 的 MCP 写到 $HOME/.claude.json(dotfile),不是
				// rootDir/settings.json —— Claude Code CLI 启动时只读这个 dotfile,
				// settings.json 给 hooks/permissions/env 用,不读 mcpServers 字段。
				dotPath := filepath.Join(fakeHome, ".claude.json")
				assertJSONHasMCPKeys(t, dotPath, expectedKeys)
				assertFileMode(t, dotPath, 0o600)

				// 迁移路径断言:旧位置 settings.json 里跟新派生 key 同名的残留应被清掉,
				// 但用户自加的 user-custom-mcp 和 hooks 字段必须保留。
				legacyPath := filepath.Join(rootDir, "settings.json")
				legacy := readJSON(t, legacyPath)
				if hooks, _ := legacy["hooks"].(map[string]any); hooks == nil || hooks["keep"] != "this" {
					t.Errorf("迁移不应碰 hooks 字段;实际 legacy=%v", legacy)
				}
				legacyServers, _ := legacy["mcpServers"].(map[string]any)
				if _, stillThere := legacyServers["shop-grafana-dev"]; stillThere {
					t.Errorf("迁移没清掉 settings.json 里残留的 shop-grafana-dev")
				}
				if _, kept := legacyServers["user-custom-mcp"]; !kept {
					t.Errorf("迁移误删了用户自加的 user-custom-mcp")
				}
			case "cursor":
				cursorPath := filepath.Join(rootDir, "mcp.json")
				assertJSONHasMCPKeys(t, cursorPath, expectedKeys)
				assertFileMode(t, cursorPath, 0o600)
			case "codex":
				// codex MCP 嵌入 agent toml 内联 [mcp_servers.<key>] 段,不再走全局 config.toml。
				for _, name := range agentNames {
					path := agentMDLocationFor(rootDir, target, name)
					assertCodexAgentTOMLHasMCPKeys(t, path, expectedKeys)
					assertFileMode(t, path, 0o600)
				}
			}

			// ── 4) discover.Scan:BotsPage 同款扫描应该能找到这个机器人 ─────────
			scanRoot := filepath.Join(rootDir, "skills")
			agents, err := discover.Scan([]string{scanRoot})
			if err != nil {
				t.Fatalf("discover.Scan: %v", err)
			}
			if len(agents) != 1 {
				t.Fatalf("scan 应找到 1 个机器人(内部含排障+验证+修复 agent),实际 %d", len(agents))
			}
			if agents[0].Meta.SystemID != cfg.System.ID || agents[0].Meta.Target != target {
				t.Errorf("scan meta 不对:%+v", agents[0].Meta)
			}
			if len(agents[0].Meta.InternalAgents) != 3 {
				t.Errorf("scan meta should include internal agents, got %+v", agents[0].Meta.InternalAgents)
			}
			installedDir := agents[0].Path

			// ── 5) reinstall:再装一次,旧 agent 人格文件应被备份成 .bak.<ts> ──────
			if err := agent.InstallNative(staging, target); err != nil {
				t.Fatalf("reinstall: %v", err)
			}
			for _, name := range agentNames {
				path := agentMDLocationFor(rootDir, target, name)
				bakMatches, _ := filepath.Glob(path + ".bak.*")
				if len(bakMatches) == 0 {
					t.Errorf("reinstall 应为 %s 生成 .bak.<ts> 备份,实际为空", path)
				}
			}

			// claude-code 双清覆盖:模拟"用户手上还有更早期版本的 settings.json 残留没经
			// 过 install 路径迁移"——直接把一条 truss-* 塞回 settings.json,看 uninstall
			// 能不能两边(.claude.json 新位置 + settings.json 老位置)都清干净。
			// 这条断言唯一能覆盖 cleanIDEMCPServers 里 "if t == TargetClaudeCode 再清一次老位置"
			// 那条分支(install 路径里 prune 已把 settings.json 清空,uninstall 时本来无残留可清)。
			if target == "claude-code" {
				legacyPath := filepath.Join(rootDir, "settings.json")
				legacy := readJSON(t, legacyPath)
				servers, _ := legacy["mcpServers"].(map[string]any)
				if servers == nil {
					servers = map[string]any{}
				}
				servers["shop-grafana-dev"] = map[string]any{"command": "stale-bin"}
				legacy["mcpServers"] = servers
				data, _ := json.MarshalIndent(legacy, "", "  ")
				if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
					t.Fatalf("re-seed legacy mcp残留: %v", err)
				}
			}

			// ── 6) UninstallNative:文件清掉 + IDE MCP 配置摘掉 ────────────────
			res, err := agent.UninstallNative(installedDir, target)
			if err != nil {
				t.Fatalf("UninstallNative: %v", err)
			}
			for _, name := range agentNames {
				path := agentMDLocationFor(rootDir, target, name)
				// agent 人格文件应该被删
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("uninstall 后 agent 人格文件 %s 还在", path)
				}
				// .bak 也应清干净
				leftover, _ := filepath.Glob(path + ".bak.*")
				if len(leftover) != 0 {
					t.Errorf("uninstall 应清掉 .bak 备份,残留 %v", leftover)
				}
				// skills/<name>/ 整目录被搬到 ~/.Trash(或 RemoveAll fallback);现位置应不存在
				if _, err := os.Stat(filepath.Join(rootDir, "skills", name)); !os.IsNotExist(err) {
					t.Errorf("uninstall 后 skills/%s/ 还在", name)
				}
			}
			// MCP 摘除清单:claude/cursor 才检查 res.MCPRemoved(它们走 settings.json 显式删 keys);
			// codex 不再单独清 MCP —— 因为 MCP 内联在 agent toml 里,toml 文件被删 = MCP 跟着没。
			if target != "codex" && len(res.MCPRemoved) == 0 {
				t.Errorf("uninstall MCPRemoved 应非空(系统派生了 %d 个 server)", len(expectedKeys))
			}

			// 三家各自二次断言:settings.json / mcp.json prefix 全清,或 agent toml 整文件已删
			switch target {
			case "claude-code":
				assertJSONNoMCPPrefix(t, filepath.Join(fakeHome, ".claude.json"), cfg.System.ID+"-")
				// 双清:刚才手塞回 settings.json 的 shop-grafana-dev 也要被 uninstall 清掉
				assertJSONNoMCPPrefix(t, filepath.Join(rootDir, "settings.json"), cfg.System.ID+"-")
			case "cursor":
				assertJSONNoMCPPrefix(t, filepath.Join(rootDir, "mcp.json"), cfg.System.ID+"-")
			case "codex":
				for _, name := range agentNames {
					assertCodexAgentTOMLAbsent(t, agentMDLocationFor(rootDir, target, name))
				}
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

// agentMDLocationFor 返回 agent 文件在 <rootDir> 下的实际路径:
//   - claude-code / cursor → <rootDir>/agents/<name>.md
//   - codex            → <rootDir>/agents/<name>.toml(TOML subagent)
func agentMDLocationFor(rootDir, target, name string) string {
	ext := ".md"
	if target == "codex" {
		ext = ".toml"
	}
	return filepath.Join(rootDir, "agents", name+ext)
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

// assertCodexAgentTOMLHasMCPKeys 验证 ~/.codex/agents/<name>.toml 内 [mcp_servers.<key>] 段
// 全部期望 server 名都注入了 + GRAFANA_URL/PASSWORD/USERNAME 之类的 env 占位非空。
//
// 不引 toml 库:做字符串包含检查就够。codex 注册产物结构稳定,字符串匹配不会误判。
func assertCodexAgentTOMLHasMCPKeys(t *testing.T, tomlPath string, want []string) {
	t.Helper()
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read codex agent toml %s: %v", tomlPath, err)
	}
	content := string(data)
	for _, k := range want {
		header := "[mcp_servers." + k + "]"
		if !strings.Contains(content, header) {
			t.Errorf("codex agent toml 没注入 %q (找不到 %s)", k, header)
		}
	}
}

// assertCodexAgentTOMLAbsent 验证 agent toml 文件已被 uninstall 删除。MCP server keys
// 跟着 toml 文件一起没,因为它们是嵌入在 agent toml 内联段而非全局 config。
func assertCodexAgentTOMLAbsent(t *testing.T, tomlPath string) {
	t.Helper()
	if _, err := os.Stat(tomlPath); !os.IsNotExist(err) {
		t.Errorf("uninstall 后 codex agent toml 应不存在: %s", tomlPath)
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
	yamlPath := filepath.Join(projectRoot(t), "examples", "apollo-troubleshooter.yaml")
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

	// apollo-troubleshooter.yaml: system.id=bank, 无 agent.id / workspace_name → ResolveID = bank-troubleshooter
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
