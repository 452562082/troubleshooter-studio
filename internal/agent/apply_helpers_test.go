package agent

import "testing"

// TestLooksLikeFactoryArtifact_LocalYamlPreserved 验证 *.local.yaml 后缀文件**不**被
// 当成 factory artifact —— sink_postmortem.py 写的 known-errors.local.yaml 必须能在
// tshoot apply / upgrade 后存活,否则上一轮 #1 自动 postmortem 沉淀的全部经验都会被新
// staging "孤儿清理" 干掉,沉淀功能形同虚设。
func TestLooksLikeFactoryArtifact_LocalYamlPreserved(t *testing.T) {
	cases := []struct {
		name, rel, target string
		want              bool
	}{
		// 正例:本来 .local.yaml 用例
		{"known-errors.local.yaml in skills/routing/references", "skills/routing/references/known-errors.local.yaml", "openclaw", false},
		{"known-errors.local.yaml claude-code", "skills/<name>/routing/references/known-errors.local.yaml", "claude-code", false},
		// 反面对照:同目录的 .yaml 仍是 factory artifact(模板派生,可被 apply 覆盖)
		{"known-errors.yaml IS factory", "skills/routing/references/known-errors.yaml", "openclaw", true},
		// 不带 .local.yaml 后缀的不受影响
		{"plain skills/ file", "skills/routing/SKILL.md", "openclaw", true},
		// .local.yaml 但不在 skills/ 下也不删
		{"top-level .local.yaml", "my-stuff.local.yaml", "openclaw", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := looksLikeFactoryArtifact(c.rel, c.target)
			if got != c.want {
				t.Errorf("looksLikeFactoryArtifact(%q,%q) = %v; want %v", c.rel, c.target, got, c.want)
			}
		})
	}
}
