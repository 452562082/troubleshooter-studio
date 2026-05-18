package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHasNetworkAccessTrue 覆盖 codex config.toml 解析边界:
//   - 段内显式 true / false / 缺失
//   - 多段共存(别的段里同名 key 不该被误识别)
//   - 段头变体([sandbox_workspace_write] vs [[xxx]])
//   - 注释 / 空行 / 大小写空格容忍
func TestHasNetworkAccessTrue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "空 config", in: "", want: false},
		{name: "段缺失", in: "[some_other]\nx = 1\n", want: false},
		{
			name: "段在但 key 缺失",
			in:   "[sandbox_workspace_write]\nwritable_paths = []\n",
			want: false,
		},
		{
			name: "显式 true",
			in:   "[sandbox_workspace_write]\nnetwork_access = true\n",
			want: true,
		},
		{
			name: "显式 false",
			in:   "[sandbox_workspace_write]\nnetwork_access = false\n",
			want: false,
		},
		{
			name: "true 但有空格 + 注释",
			in:   "# 全局 codex 配置\n[sandbox_workspace_write]\n  network_access   =   true   # 启用 MCP 出网\n",
			want: true,
		},
		{
			name: "别的段里同名 key 不算",
			in:   "[sandbox_other]\nnetwork_access = true\n[sandbox_workspace_write]\nx = 1\n",
			want: false,
		},
		{
			name: "目标段在前 + 别的段在后",
			in:   "[sandbox_workspace_write]\nnetwork_access = true\n\n[other]\ny = 2\n",
			want: true,
		},
		{
			name: "[[array]] 段头进入后退出目标段",
			in:   "[sandbox_workspace_write]\n[[some_array]]\nnetwork_access = true\n",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasNetworkAccessTrue(c.in); got != c.want {
				t.Errorf("hasNetworkAccessTrue(%q) = %v; want %v", c.in, got, c.want)
			}
		})
	}
}

// TestPatchCodexNetworkAccess 覆盖 3 种 toml 缺失场景:文件空 / 段缺 / key 缺 / key=false。
// 每个 case 的 want 都用 hasNetworkAccessTrue 反向 verify(patch 结果一定能被 parser 识别)。
func TestPatchCodexNetworkAccess(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantChanged bool
		// wantContains 是 patch 结果一定要包含的子串(光 hasNetworkAccessTrue 不够,要确认没破坏旧内容)
		wantContains []string
	}{
		{
			name:         "空文件 → 写整段",
			in:           "",
			wantChanged:  true,
			wantContains: []string{"[sandbox_workspace_write]", "network_access = true"},
		},
		{
			name:         "已有别的段 → append 整段保留旧内容",
			in:           "[some_other]\nx = 1\n",
			wantChanged:  true,
			wantContains: []string{"[some_other]", "x = 1", "[sandbox_workspace_write]", "network_access = true"},
		},
		{
			name:         "段在但缺 key → 段头后插一行",
			in:           "[sandbox_workspace_write]\nwritable_paths = []\n",
			wantChanged:  true,
			wantContains: []string{"network_access = true", "writable_paths = []"},
		},
		{
			name:         "段在且 key=false → 改值",
			in:           "[sandbox_workspace_write]\nnetwork_access = false\n",
			wantChanged:  true,
			wantContains: []string{"network_access = true"},
		},
		{
			name:         "段在且 key=true → 无改动",
			in:           "[sandbox_workspace_write]\nnetwork_access = true\n",
			wantChanged:  false,
			wantContains: []string{"network_access = true"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 已 true 的 case 由 EnsureCodexNetworkAccess 短路掉,不会进 patch;
			// 这里只跑没 true 的 case 确认 patch 结果正确(已 true case 走 E2E test)
			if hasNetworkAccessTrue(c.in) {
				return
			}
			got, changed := patchCodexNetworkAccess(c.in)
			if changed != c.wantChanged {
				t.Errorf("changed = %v; want %v", changed, c.wantChanged)
			}
			if !hasNetworkAccessTrue(got) {
				t.Errorf("patch 后 hasNetworkAccessTrue=false; result=%q", got)
			}
			for _, sub := range c.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("缺少子串 %q; result=%q", sub, got)
				}
			}
		})
	}
}

// TestEnsureCodexNetworkAccess_E2E 端到端:用真实临时目录跑 EnsureCodexNetworkAccess,
// 验证文件创建 / patch 写入 / backup 落地 / verify 通过。
func TestEnsureCodexNetworkAccess_E2E(t *testing.T) {
	t.Run("文件不存在 → 创建并写整段", func(t *testing.T) {
		dir := t.TempDir()
		changed, err := EnsureCodexNetworkAccess(dir)
		if err != nil {
			t.Fatalf("EnsureCodexNetworkAccess err = %v", err)
		}
		if !changed {
			t.Error("changed=false,应为 true(新建文件场景)")
		}
		data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatalf("read config.toml: %v", err)
		}
		if !hasNetworkAccessTrue(string(data)) {
			t.Errorf("写后 hasNetworkAccessTrue=false; content=%q", string(data))
		}
	})

	t.Run("文件已 true → 静默 noop", func(t *testing.T) {
		dir := t.TempDir()
		seed := "[sandbox_workspace_write]\nnetwork_access = true\n"
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(seed), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		changed, err := EnsureCodexNetworkAccess(dir)
		if err != nil {
			t.Fatalf("EnsureCodexNetworkAccess err = %v", err)
		}
		if changed {
			t.Error("changed=true,应为 false(已就绪场景)")
		}
		// 不应留 backup(没改动就不 backup)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "tshoot-bak") {
				t.Errorf("不该产生 backup,但找到 %s", e.Name())
			}
		}
	})

	t.Run("已有别的段 + 缺目标段 → patch 并 backup", func(t *testing.T) {
		dir := t.TempDir()
		seed := "[other]\nx = 1\n"
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		changed, err := EnsureCodexNetworkAccess(dir)
		if err != nil {
			t.Fatalf("EnsureCodexNetworkAccess err = %v", err)
		}
		if !changed {
			t.Error("changed=false,应为 true")
		}
		got, _ := os.ReadFile(cfgPath)
		if !hasNetworkAccessTrue(string(got)) {
			t.Errorf("patch 后 hasNetworkAccessTrue=false; result=%q", string(got))
		}
		if !strings.Contains(string(got), "[other]") || !strings.Contains(string(got), "x = 1") {
			t.Errorf("patch 破坏了原内容; result=%q", string(got))
		}
		// 应有 backup 落盘
		entries, _ := os.ReadDir(dir)
		bakFound := false
		for _, e := range entries {
			if strings.Contains(e.Name(), "tshoot-bak") {
				bakFound = true
				break
			}
		}
		if !bakFound {
			t.Error("有原文件却没产生 backup")
		}
	})
}
