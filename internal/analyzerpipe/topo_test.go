package analyzerpipe

import (
	"reflect"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// TestTopoSortReposByParent_NoUmbrella:没有 parent_repo 的 repos 顺序保持不变。
// 这是大多数普通项目的常态,排序不能引入意外重排。
func TestTopoSortReposByParent_NoUmbrella(t *testing.T) {
	in := []config.Repo{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	out := topoSortReposByParent(in)
	got := names(out)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("无 parent_repo 应保持原序;got %v, want %v", got, want)
	}
}

// TestTopoSortReposByParent_ParentFirst:parent 排在 child 前面,跟入参顺序无关。
// 这是 umbrella 继承编排的关键 — child 解析路径时 resolvedPaths[parent] 必须已就绪。
func TestTopoSortReposByParent_ParentFirst(t *testing.T) {
	cases := []struct {
		desc string
		in   []config.Repo
	}{
		{
			desc: "child 在 parent 之前",
			in: []config.Repo{
				{Name: "child", ParentRepo: "umbrella"},
				{Name: "umbrella"},
			},
		},
		{
			desc: "child 在 parent 之后",
			in: []config.Repo{
				{Name: "umbrella"},
				{Name: "child", ParentRepo: "umbrella"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			out := topoSortReposByParent(tc.in)
			pi, ci := indexOf(names(out), "umbrella"), indexOf(names(out), "child")
			if pi < 0 || ci < 0 || pi >= ci {
				t.Errorf("umbrella 必须在 child 之前;排序后 = %v", names(out))
			}
		})
	}
}

// TestTopoSortReposByParent_MultiChildren:一个 umbrella + N 个 children,
// umbrella 第一个,children 顺序保持原序(同层稳定)。truss 拆出 commerce/admin/api 等典型场景。
func TestTopoSortReposByParent_MultiChildren(t *testing.T) {
	in := []config.Repo{
		{Name: "commerce", ParentRepo: "truss"},
		{Name: "truss"},
		{Name: "admin", ParentRepo: "truss"},
		{Name: "api", ParentRepo: "truss"},
	}
	out := names(topoSortReposByParent(in))
	if out[0] != "truss" {
		t.Fatalf("umbrella 应排在第一;got %v", out)
	}
	// children 集合一致,顺序按 enqueued 时机(commerce / admin / api)
	wantChildren := map[string]bool{"commerce": true, "admin": true, "api": true}
	for _, n := range out[1:] {
		if !wantChildren[n] {
			t.Errorf("意外的 child 名 %q;out=%v", n, out)
		}
	}
}

// TestTopoSortReposByParent_DanglingParent:parent_repo 引用不存在的 name
// (health check 应该拦下,这里测不崩 + 仍然能跑,degrade 到追加末尾)。
func TestTopoSortReposByParent_DanglingParent(t *testing.T) {
	in := []config.Repo{
		{Name: "child", ParentRepo: "nonexistent"},
		{Name: "other"},
	}
	out := names(topoSortReposByParent(in))
	if len(out) != 2 {
		t.Fatalf("dangling parent 不应丢 repo;got %v", out)
	}
	// dangling parent 视为没 parent(indegree=0),保持原序
	if out[0] != "child" || out[1] != "other" {
		t.Errorf("dangling parent 应当 indegree=0 保留原序;got %v", out)
	}
}

// TestTopoSortReposByParent_Cycle:A→B→A 互引,两者都进不了 indegree=0 队列。
// Kahn 主循环出来 visited 不全,残留按原序追加到末尾,保证返回长度跟入参一致(不丢 repo)。
// health check 应该拦下,这里只测"不崩 + 长度对"。
func TestTopoSortReposByParent_Cycle(t *testing.T) {
	in := []config.Repo{
		{Name: "a", ParentRepo: "b"},
		{Name: "b", ParentRepo: "a"},
		{Name: "c"}, // 无关,应当正常排在前面
	}
	out := names(topoSortReposByParent(in))
	if len(out) != 3 {
		t.Fatalf("cycle 不应丢 repo;got %v", out)
	}
	if out[0] != "c" {
		t.Errorf("c 没有 parent,应排第一(indegree=0 优先);got %v", out)
	}
	// a / b 残留追加,顺序不强求(只要存在)
	tail := map[string]bool{out[1]: true, out[2]: true}
	if !tail["a"] || !tail["b"] {
		t.Errorf("环里的 a / b 应保留追加在末尾;got %v", out)
	}
}

// TestTopoSortReposByParent_StableSameLevel:同 indegree=0 的 repos 顺序保持原序,
// 给 LLM / yaml diff 一个稳定的输出。
func TestTopoSortReposByParent_StableSameLevel(t *testing.T) {
	in := []config.Repo{
		{Name: "z"}, {Name: "m"}, {Name: "a"},
	}
	out := names(topoSortReposByParent(in))
	want := []string{"z", "m", "a"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("同层应保持原序;got %v want %v", out, want)
	}
}

// TestTopoSortReposByParent_Single / Empty:边界。
func TestTopoSortReposByParent_Empty(t *testing.T) {
	if got := topoSortReposByParent(nil); len(got) != 0 {
		t.Errorf("nil 入参应返空;got %v", got)
	}
	if got := topoSortReposByParent([]config.Repo{}); len(got) != 0 {
		t.Errorf("空 slice 应返空;got %v", got)
	}
}

func TestTopoSortReposByParent_Single(t *testing.T) {
	in := []config.Repo{{Name: "solo"}}
	out := names(topoSortReposByParent(in))
	if !reflect.DeepEqual(out, []string{"solo"}) {
		t.Errorf("单 repo 直接返;got %v", out)
	}
}

// helpers
func names(rs []config.Repo) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
