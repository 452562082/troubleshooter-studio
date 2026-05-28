package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func TestSplitNacosAddr(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort string
	}{
		{"nacos:8848", "nacos", "8848"},
		{"13.112.112.196:8848", "13.112.112.196", "8848"},
		{"http://nacos-prod:8849", "nacos-prod", "8849"},
		{"https://nacos-prod:8849/nacos", "nacos-prod", "8849"},
		{"nacos-host", "nacos-host", "8848"},      // 无 port → 默认 8848
		{"http://bare-host", "bare-host", "8848"}, // scheme 但无 port
		{"  nacos:8848/  ", "nacos", "8848"},      // 前后空白 + 尾斜杠
	}
	for _, c := range cases {
		h, p := splitNacosAddr(c.in)
		if h != c.wantHost || p != c.wantPort {
			t.Errorf("splitNacosAddr(%q) = (%q,%q), want (%q,%q)", c.in, h, p, c.wantHost, c.wantPort)
		}
	}
}

func TestCfgUsesNacosMCP(t *testing.T) {
	yes := &config.SystemConfig{Infrastructure: config.Infrastructure{
		ConfigCenters: []config.ConfigCenter{{Type: "apollo"}, {Type: "nacos"}},
	}}
	if !CfgUsesNacosMCP(yes) {
		t.Error("有 nacos config center 应返回 true")
	}
	no := &config.SystemConfig{Infrastructure: config.Infrastructure{
		ConfigCenters: []config.ConfigCenter{{Type: "apollo"}, {Type: "consul"}},
	}}
	if CfgUsesNacosMCP(no) {
		t.Error("无 nacos 应返回 false")
	}
}

func TestEnsureNacosMCPScript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// 防 darwin os.UserHomeDir 偶尔读其它变量
	t.Setenv("USERPROFILE", home)

	var logged string
	got, err := EnsureNacosMCPScript(func(s string) { logged = s })
	if err != nil {
		t.Fatalf("EnsureNacosMCPScript: %v", err)
	}
	want := filepath.Join(home, ".tshoot", "scripts", "nacos_mcp.py")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("脚本未落地: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("脚本应可执行,mode=%v", info.Mode())
	}
	data, _ := os.ReadFile(got)
	// 内嵌内容应含 MCP server 标记 + PEP 723 头
	if !strings.Contains(string(data), "nacos MCP server") || !strings.Contains(string(data), "/// script") {
		t.Errorf("extract 内容不像 nacos_mcp.py(前 80 字: %.80s)", data)
	}
	if !strings.Contains(logged, "nacos MCP 脚本就位") {
		t.Errorf("onLog 未收到就位日志,got %q", logged)
	}

	// 幂等 + 覆盖:再跑一次应成功(无条件覆盖)
	if _, err := EnsureNacosMCPScript(nil); err != nil {
		t.Fatalf("第二次 EnsureNacosMCPScript: %v", err)
	}
}
