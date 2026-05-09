package agent

import "testing"

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
