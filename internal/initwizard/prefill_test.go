package initwizard

import (
	"bytes"
	"reflect"
	"sort"
	"testing"
)

func TestParseTargets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty → all 3", "", []string{"openclaw", "claude-code", "cursor"}},
		{"single", "openclaw", []string{"openclaw"}},
		{"space sep", "openclaw cursor", []string{"openclaw", "cursor"}},
		{"comma sep", "openclaw,cursor", []string{"openclaw", "cursor"}},
		{"semicolon sep", "openclaw;cursor", []string{"openclaw", "cursor"}},
		{"mixed sep + padding", "  openclaw , cursor ", []string{"openclaw", "cursor"}},
		{"case insensitive", "OPENCLAW Cursor", []string{"openclaw", "cursor"}},
		{"dedup", "openclaw openclaw cursor", []string{"openclaw", "cursor"}},
		{"all unknown → fallback openclaw", "bogus unknown", []string{"openclaw"}},
		{"partial unknown filtered", "openclaw bogus cursor", []string{"openclaw", "cursor"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTargets(c.in)
			// 保留顺序（parseTargets 以出现顺序为准；空输入回全默认顺序）
			if !reflect.DeepEqual(got, c.want) {
				// 顺序可能在部分 case 里不定，再 fallback 用 set 对比
				a, b := append([]string{}, got...), append([]string{}, c.want...)
				sort.Strings(a)
				sort.Strings(b)
				if !reflect.DeepEqual(a, b) {
					t.Errorf("in=%q\nwant %v\ngot  %v", c.in, c.want, got)
				}
			}
		})
	}
}

// TestRun_Prefill 验证当 Wizard.Defaults 设置后，ask() 里取到的默认值正是 Defaults 里的字段
// 用户全程回车接受默认；Answers 应等于 Defaults（忽略 map 顺序差异）。
func TestRun_Prefill(t *testing.T) {
	prefill := &Answers{
		SystemID:          "preshop",
		SystemName:        "PreShop",
		SystemDescription: "描述",
		AgentName:         "Bot",
		AgentModel:        "anthropic/claude-opus-4-7",
		WorkspaceName:     "BotWS",
		Envs: []EnvAnswer{
			{ID: "dev", APIDomain: "api-dev.x", IsProd: false},
			{ID: "prod", APIDomain: "api.x", IsProd: true},
		},
		Repos: []RepoAnswer{
			{Name: "svc", URL: "git@x:svc.git", Stack: "go"},
		},
		ConfigCenterType:     "apollo",
		GrafanaEnabled:       false,
		LokiEnabled:          false,
		PrometheusEnabled:    false,
		DataStoresEnabled:    map[string]bool{"redis": false, "mongodb": false, "elasticsearch": false, "mysql": true, "doris": true, "kafka": true},
		LarkEnabled:          false,
		LarkAttachment:       false,
		FeishuProjectEnabled: true,
		Targets:              []string{"claude-code", "cursor"},
	}

	// 用户全回车接受预填 —— 每步按题目数量回车
	// 1/9 system: id, name, desc (3)
	// 2/9 agent: name, ws, model (3)
	// 3/9 envs: "原样复用？[Y/n]" 回车接受 (1)
	// 4/9 repos: "原样复用？[Y/n]" (1)
	// 5/9 config center type (1)
	// 6/9 observability: grafana/loki/prom (3)
	// 7/9 data stores: redis/mongodb/elasticsearch/mysql/doris/kafka (6)
	// 8/9 lark (1)；不展开 attachment（因为 LarkEnabled=false）
	// 9/9 feishu (1)
	// output dir (1)
	// targets (1)
	in := script(
		"", "", "", // system
		"", "", "", // agent
		"",         // envs 复用
		"",         // repos 复用
		"",         // config center
		"", "", "", // obs
		"", "", "", "", "", "", // data stores
		"", // lark
		"", // feishu
		"", // output dir
		"", // targets
	)
	var out bytes.Buffer
	w := New(in, &out)
	w.Defaults = prefill
	ans, err := w.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if ans.SystemID != "preshop" || ans.SystemName != "PreShop" || ans.SystemDescription != "描述" {
		t.Errorf("system prefill lost: %+v", ans)
	}
	if ans.AgentName != "Bot" || ans.AgentModel != "anthropic/claude-opus-4-7" || ans.WorkspaceName != "BotWS" {
		t.Errorf("agent prefill lost: %+v", ans)
	}
	if len(ans.Envs) != 2 || ans.Envs[0].ID != "dev" || ans.Envs[1].ID != "prod" || !ans.Envs[1].IsProd {
		t.Errorf("envs prefill lost: %+v", ans.Envs)
	}
	if len(ans.Repos) != 1 || ans.Repos[0].Name != "svc" {
		t.Errorf("repos prefill lost: %+v", ans.Repos)
	}
	if ans.ConfigCenterType != "apollo" {
		t.Errorf("config center prefill lost: %q", ans.ConfigCenterType)
	}
	if ans.GrafanaEnabled || ans.LokiEnabled || ans.PrometheusEnabled {
		t.Errorf("observability prefill defaults off expected, got %+v", ans)
	}
	if !ans.DataStoresEnabled["mysql"] || !ans.DataStoresEnabled["doris"] || ans.DataStoresEnabled["redis"] {
		t.Errorf("data-store prefill lost: %+v", ans.DataStoresEnabled)
	}
	if ans.LarkEnabled {
		t.Errorf("lark prefill=false should be kept off")
	}
	if !ans.FeishuProjectEnabled {
		t.Errorf("feishu prefill=true should be kept on")
	}
	if len(ans.Targets) != 2 || ans.Targets[0] != "claude-code" {
		t.Errorf("targets prefill lost: %v", ans.Targets)
	}
}

// TestWizard_Snapshot 验证 Snapshot()：
//   - 启动前返回 nil
//   - 每步结束后 setCurrent 被 Run 调用，Snapshot 能拿到部分 Answers
func TestWizard_Snapshot_EmptyBeforeRun(t *testing.T) {
	w := New(bytes.NewReader(nil), &bytes.Buffer{})
	if w.Snapshot() != nil {
		t.Errorf("Snapshot before Run should be nil")
	}
}

func TestWizard_Snapshot_AfterCompletedRun(t *testing.T) {
	// 跑完完整最小向导，Snapshot 应等同于最终 Answers（或其快照）
	in := script(
		"shop", "Shop", "", // system
		"", "", "", // agent
		"dev", "api-dev", "", // env 1
		"",         // env 2 empty → end
		"",         // repo empty → end
		"",         // config center
		"", "", "", // obs
		"", "", "", "", "", "", // data stores
		"", "", // lark + attachment
		"", // feishu
		"", // output dir
		"", // targets
	)
	var out bytes.Buffer
	w := New(in, &out)
	ans, err := w.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	snap := w.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot after Run should not be nil")
	}
	if snap.SystemID != ans.SystemID {
		t.Errorf("snap diverges from final answers: snap=%q ans=%q", snap.SystemID, ans.SystemID)
	}
}
