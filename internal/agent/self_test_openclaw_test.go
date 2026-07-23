package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestRequiredMCPKeys_CodeGraph(t *testing.T) {
	cfg := &config.SystemConfig{CodeIntelligence: config.CodeIntelligence{Enabled: true, Provider: "codegraph"}}
	if got := requiredMCPKeys(cfg, "shop"); !slices.Contains(got, "shop-codegraph") {
		t.Fatalf("%v", got)
	}
}

// init() stub probeMCPFunc / toolchainLookPath — self-test 测试会因 cfg 注册一堆 mcp,真 probe
// 会因 CI 没装 npx/uvx 全 FAIL。stub 返成功结果,让 self-test 走 happy path。个别测试可局部 override 测 FAIL。
// toolchainLookPath 同理:CI 的 golang docker 镜像没装 node/uv,真 LookPath npx/uvx 必 FAIL,
// 拖垮 happy-path 自检断言;stub 成命中,保留生产 LookPath 行为只在测试里替换。
func init() {
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Tools: []string{"fake_tool_a", "fake_tool_b", "codegraph_explore"}}
	}
	toolchainLookPath = func(name string) (string, error) {
		return "/fake/bin/" + name, nil
	}
}

func TestSelfTestOpenclaw_CodeGraphIndexWarningDoesNotFailOverall(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	staging, _ := setupCodeGraphSelfTestOpenclaw(t, repoPath)
	binaries := make(chan string, 1)
	setCodeGraphRunnerForTest(t, func(_ context.Context, binary string, _ ...string) ([]byte, error) {
		binaries <- binary
		return codeGraphStatusJSON(repoPath, false, 0, 0, 0, "missing"), nil
	})

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("optional CodeGraph index warning must not fail self-test: %#v", res.Checks)
	}
	if binary := <-binaries; binary != "/managed/codegraph" {
		t.Fatalf("CodeGraph binary = %q, want command extracted from openclaw.json", binary)
	}
	assertCodeGraphSelfTestRows(t, res.Checks, "PASS", "WARN")
}

func TestSelfTestOpenclaw_CodeGraphMissingExploreToolFails(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	staging, _ := setupCodeGraphSelfTestOpenclaw(t, repoPath)
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Tools: []string{"codegraph_status"}}
	}
	t.Cleanup(func() { probeMCPFunc = old })
	setCodeGraphRunnerForTest(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return codeGraphStatusJSON(repoPath, true, 12, 84, 103, "complete"), nil
	})

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("missing runtime-observed codegraph_explore must fail self-test: %#v", res.Checks)
	}
	assertCodeGraphSelfTestRows(t, res.Checks, "FAIL", "PASS")
}

func TestSelfTestOpenclaw_CodeGraphMalformedPresentSpecFails(t *testing.T) {
	repoPath := makeCodeGraphSourceRepo(t, "main.go")
	staging, fakeHome := setupCodeGraphSelfTestOpenclaw(t, repoPath)
	configPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	root := readJSON(t, configPath)
	servers := getMap(root, "mcp", "servers")
	servers["shop-codegraph"] = "not-an-object"
	writeJSON(t, configPath, root)

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("present but malformed CodeGraph spec must fail self-test: %#v", res.Checks)
	}
	statusByName := make(map[string]string, len(res.Checks))
	for _, check := range res.Checks {
		statusByName[check.Name] = check.Status
	}
	if statusByName["mcp.servers 齐全(1 项)"] != "PASS" || statusByName["mcp probe shop-codegraph"] != "FAIL" {
		t.Fatalf("required-key check should pass while malformed runtime probe fails: %#v", res.Checks)
	}
}

func setupCodeGraphSelfTestOpenclaw(t *testing.T, repoPath string) (staging, fakeHome string) {
	t.Helper()
	cfg := nacosCfg()
	cfg.Infrastructure = config.Infrastructure{}
	cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: config.CodeIntelligenceProviderCodeGraph}
	cfg.Repos = []config.Repo{{
		Name: "orders", URL: "https://example.invalid/orders.git", Stack: "go",
		Analysis: config.RepoAnalysis{Enabled: boolPointer(true)},
	}}
	staging, fakeHome = setupOpenclawStaging(t, cfg)

	workspaceDir := filepath.Join(fakeHome, ".openclaw", "workspace", cfg.ResolveWorkspaceName())
	generated := makeCodeGraphSelfTestWorkspace(t, map[string]string{"orders": repoPath})
	if err := os.MkdirAll(filepath.Dir(workspaceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(generated, workspaceDir); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	writeJSON(t, configPath, map[string]any{
		"agents": map[string]any{"list": []any{map[string]any{"id": cfg.ResolveID()}}},
		"mcp": map[string]any{"servers": map[string]any{
			"shop-codegraph": map[string]any{"command": "/managed/codegraph", "args": []any{"serve", "--mcp"}},
		}},
	})
	return staging, fakeHome
}

func assertCodeGraphSelfTestRows(t *testing.T, checks []SelfTestCheck, wantMCP, wantIndex string) {
	t.Helper()
	mcpIndex, indexIndex := -1, -1
	for i, check := range checks {
		switch {
		case check.Name == "mcp probe shop-codegraph":
			mcpIndex = i
			if check.Status != wantMCP {
				t.Fatalf("MCP row = %#v, want status %s", check, wantMCP)
			}
		case check.Name == "CodeGraph 索引 orders":
			indexIndex = i
			if check.Status != wantIndex {
				t.Fatalf("index row = %#v, want status %s", check, wantIndex)
			}
		}
	}
	if mcpIndex < 0 || indexIndex < 0 || indexIndex != mcpIndex+1 {
		t.Fatalf("CodeGraph MCP/index rows must be separate and adjacent: %#v", checks)
	}
}

// withFakeMCPProbeErr 局部 stub probeMCPFunc 模拟 mcp 起不来(rabbitmq fastmcp 那种 case)。
func withFakeMCPProbeErr(t *testing.T) {
	t.Helper()
	old := probeMCPFunc
	probeMCPFunc = func(_ context.Context, _ string, _, _ []string, _ time.Duration) MCPProbeResult {
		return MCPProbeResult{Err: errFakeProbe, StderrTail: "ImportError: cannot import name 'X'"}
	}
	t.Cleanup(func() { probeMCPFunc = old })
}

var errFakeProbe = &probeErr{"fake mcp 启动失败"}

type probeErr struct{ msg string }

func (e *probeErr) Error() string { return e.msg }

// 把上一个 install 测试 setup 的"先 install 再 self-test"流程链起来,
// 确认装完即过自检。HOST 探活用空 NACOS_ADDR / GRAFANA_URL → 都给 WARN,不算 FAIL。
func TestSelfTestOpenclaw_AfterInstallAllPass(t *testing.T) {
	cfg := nacosCfg()
	staging, _ := setupOpenclawStaging(t, cfg)

	// 不传 creds → addr/url 为空,后面 nacos TCP / grafana HTTP 都会 WARN 跳过,不影响 OK
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Errorf("self-test 不应有 FAIL,got: %+v", res.Checks)
	}
	// 至少要 PASS 三项关键检查
	statusByName := map[string]string{}
	for _, c := range res.Checks {
		statusByName[c.Name] = c.Status
	}
	wantPass := []string{
		"workspace 目录",
		"agents.list 含 shop-troubleshooter",
		// 4 项 = grafana×2(dev+prod)+ nacos×2(plan D:nacos 走本地 MCP 脚本)。
		// install 阶段会把 legacy ConfigCenter 单字段迁移到 ConfigCenters,所以 buildNacos
		// 注册 nacos-dev / nacos-prod,requiredMCPKeys 也镜像要求这两个。
		"mcp.servers 齐全(4 项)",
	}
	for _, name := range wantPass {
		if statusByName[name] != "PASS" {
			t.Errorf("expected %q PASS, got %q", name, statusByName[name])
		}
	}
}

// TestSelfTestOpenclaw_MCPProbeFAIL 模拟 rabbitmq fastmcp 那种 case:install 显示 success 但
// mcp 进程秒挂(probe 返 ImportError)。self-test 必须 FAIL 给具体诊断。
func TestSelfTestOpenclaw_MCPProbeFAIL(t *testing.T) {
	withFakeMCPProbeErr(t)
	cfg := nacosCfg()
	staging, _ := setupOpenclawStaging(t, cfg)
	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("mcp probe 全 FAIL,self-test 整体应当 FAIL")
	}
	// 至少有一条 mcp probe FAIL 项 + detail 含 stderr tail(给用户具体故障定位)
	failedProbes := 0
	for _, c := range res.Checks {
		if c.Status == "FAIL" && strings.HasPrefix(c.Name, "mcp probe ") {
			failedProbes++
			if !strings.Contains(c.Detail, "stderr tail:") {
				t.Errorf("mcp probe FAIL detail 应含 stderr tail,got: %q", c.Detail)
			}
		}
	}
	if failedProbes == 0 {
		t.Errorf("应至少 1 条 mcp probe FAIL,got: %+v", res.Checks)
	}
}

func TestSelfTestOpenclaw_MissingWorkspace(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 删掉 workspace 目录
	if err := os.RemoveAll(filepath.Join(fakeHome, ".openclaw", "workspace", "shop-troubleshooter")); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("缺 workspace 应当 FAIL")
	}
	found := false
	for _, c := range res.Checks {
		if c.Name == "workspace 目录" && c.Status == "FAIL" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("应有 'workspace 目录' FAIL 项, got: %+v", res.Checks)
	}
}

func TestSelfTestOpenclaw_MissingMCPServer(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 手动从 openclaw.json 删掉一个 MCP server
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	servers := getMap(data, "mcp", "servers")
	delete(servers, "shop-grafana-prod")
	mb, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(cfgPath, mb, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("缺 MCP server 应当 FAIL")
	}
	for _, c := range res.Checks {
		if c.Name == "mcp.servers 齐全" && c.Status == "FAIL" {
			return
		}
	}
	t.Errorf("应有 'mcp.servers 齐全' FAIL 项, got: %+v", res.Checks)
}

func TestSelfTestOpenclaw_AgentMissing(t *testing.T) {
	cfg := nacosCfg()
	staging, fakeHome := setupOpenclawStaging(t, cfg)

	if err := InstallNativeOpenclaw(context.Background(), staging, InstallOpenclawOptions{SkipGatewayRestart: true}); err != nil {
		t.Fatal(err)
	}
	// 把 agents.list 清空
	cfgPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	data := readJSON(t, cfgPath)
	agents, _ := data["agents"].(map[string]any)
	agents["list"] = []any{}
	mb, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(cfgPath, mb, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SelfTestOpenclaw(context.Background(), staging)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("agents.list 缺 agent 应当 FAIL")
	}
}
