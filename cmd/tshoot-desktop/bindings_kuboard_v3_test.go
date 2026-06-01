package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKuboardV3Cookie(t *testing.T) {
	got := kuboardV3Cookie("admin", "keyid.secret")
	want := "KuboardUsername=admin; KuboardAccessKey=keyid.secret"
	if got != want {
		t.Errorf("cookie = %q, want %q", got, want)
	}
}

func TestIsSystemNamespace(t *testing.T) {
	for _, ns := range []string{"kube-system", "kube-public", "kuboard-system"} {
		if !isSystemNamespace(ns) {
			t.Errorf("%q should be system ns", ns)
		}
	}
	for _, ns := range []string{"default", "base-dev", "app"} {
		if isSystemNamespace(ns) {
			t.Errorf("%q should NOT be system ns", ns)
		}
	}
}

func TestK8sListNames(t *testing.T) {
	body := []byte(`{"kind":"NamespaceList","items":[{"metadata":{"name":"a"}},{"metadata":{"name":"b"}},{"metadata":{"name":""}}]}`)
	got, err := k8sListNames(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("names = %v, want [a b] (empty filtered)", got)
	}
}

// TestKuboardDetectVersion:正向识别 v4 —— 只有 tree 返 200 且 body 含 {data:{treeItems}}
// 才判 v4;其余(404/401/403/5xx/200 但非 v4 JSON)一律 v3。
func TestKuboardDetectVersion(t *testing.T) {
	const v4Body = `{"data":{"treeItems":[{"id":"u1","name":"c1","children":[]}]}}`
	cases := []struct {
		name       string
		treeStatus int
		treeBody   string
		want       string
	}{
		{"404 ⇒ v3", http.StatusNotFound, "", "v3"},
		{"401 ⇒ v3 (旧逻辑会误判 v4)", http.StatusUnauthorized, "", "v3"},
		{"403 ⇒ v3 (Cloudflare/鉴权)", http.StatusForbidden, "", "v3"},
		{"500 ⇒ v3", http.StatusInternalServerError, "", "v3"},
		{"200 但非 v4 JSON ⇒ v3", http.StatusOK, `<html>cloudflare challenge</html>`, "v3"},
		{"200 但 JSON 不含 treeItems(键缺失)⇒ v3", http.StatusOK, `{"data":{}}`, "v3"},
		{"200 且 v4 形态 ⇒ v4", http.StatusOK, v4Body, "v4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree") {
					if c.treeStatus != http.StatusOK {
						w.WriteHeader(c.treeStatus)
						return
					}
					_, _ = w.Write([]byte(c.treeBody))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()
			got := kuboardDetectVersion(context.Background(), srv.Client(), srv.URL, "key")
			if got != c.want {
				t.Errorf("detect=%q want %q", got, c.want)
			}
		})
	}
}

// TestKuboardDetectVersion_TransportError:传输层错误(连不上/Cloudflare 首连抖动)应判 v3,
// 而非旧逻辑的 v4。用一个立即关闭的 server 制造连接错误。
func TestKuboardDetectVersion_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	client := srv.Client()
	srv.Close() // 关掉后再请求 → 传输错误
	if got := kuboardDetectVersion(context.Background(), client, url, "key"); got != "v3" {
		t.Errorf("传输错误应判 v3, got %q", got)
	}
}

// v3 mock：cluster-exists + namespaces + configmaps 三类 k8s/kuboard-api 路由。
func newV3MockServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		// 版本探测:v4 tree → 404 ⇒ 判定 v3
		case strings.Contains(p, "/api/cluster.kuboard.cn/v4/"):
			w.WriteHeader(http.StatusNotFound)
		// 校验集群存在
		case strings.HasSuffix(p, "/kind/KubernetesCluster"):
			if !strings.Contains(p, "/cluster/c1/") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"c1"}}]}`))
		// 列 namespace
		case strings.HasSuffix(p, "/k8s-api/c1/api/v1/namespaces"):
			_, _ = w.Write([]byte(`{"kind":"NamespaceList","items":[{"metadata":{"name":"app"}},{"metadata":{"name":"kube-system"}}]}`))
		// 列 configmap(app ns)
		case strings.HasSuffix(p, "/namespaces/app/configmaps"):
			_, _ = w.Write([]byte(`{"kind":"ConfigMapList","items":[{"metadata":{"name":"cm1"}},{"metadata":{"name":"cm2"}}]}`))
		// 读单个 configmap
		case strings.HasSuffix(p, "/namespaces/app/configmaps/cm1"):
			_, _ = w.Write([]byte(`{"kind":"ConfigMap","data":{"DB_HOST":"db","REDIS_PORT":"6379"}}`))
		// 列 pods(app ns)
		case strings.HasSuffix(p, "/namespaces/app/pods"):
			_, _ = w.Write([]byte(`{"kind":"PodList","items":[{"metadata":{"name":"p1"}},{"metadata":{"name":"p2"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestKuboardListResourcesV3(t *testing.T) {
	srv := newV3MockServer(t)
	defer srv.Close()
	res, err := kuboardListResourcesV3(context.Background(), srv.Client(), srv.URL, "admin", "id.secret", "c1")
	if err != nil {
		t.Fatalf("kuboardListResourcesV3: %v", err)
	}
	if len(res.Clusters) != 1 || res.Clusters[0].Name != "c1" {
		t.Fatalf("clusters = %+v", res.Clusters)
	}
	// kube-system 被过滤,只剩 app
	nss := res.Clusters[0].Namespaces
	if len(nss) != 1 || nss[0].Name != "app" {
		t.Fatalf("namespaces = %+v (kube-system 应被过滤)", nss)
	}
	if strings.Join(nss[0].ConfigMaps, ",") != "cm1,cm2" {
		t.Errorf("configmaps = %v, want [cm1 cm2]", nss[0].ConfigMaps)
	}
}

func TestKuboardListResourcesV3_MissingCluster(t *testing.T) {
	srv := newV3MockServer(t)
	defer srv.Close()
	// 空集群名 → 返回 note,不报错
	res, err := kuboardListResourcesV3(context.Background(), srv.Client(), srv.URL, "admin", "id.secret", "")
	if err != nil {
		t.Fatalf("空集群名不应报错: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Errorf("空集群名应返回提示 note")
	}
	// 不存在的集群 → 报错
	if _, err := kuboardListResourcesV3(context.Background(), srv.Client(), srv.URL, "admin", "id.secret", "nope"); err == nil {
		t.Errorf("不存在的集群应报错")
	}
	// 缺 username → 报错
	if _, err := kuboardListResourcesV3(context.Background(), srv.Client(), srv.URL, "", "id.secret", "c1"); err == nil {
		t.Errorf("v3 缺 username 应报错")
	}
}

func TestListK8sObjects_V3(t *testing.T) {
	srv := newV3MockServer(t)
	defer srv.Close()
	s := &kuboardSetupResult{
		ctx: context.Background(), client: srv.Client(),
		base: srv.URL, version: "v3", cookie: "x", clusterName: "c1",
	}
	objs, err := s.listK8sObjects("pods", "app", "")
	if err != nil {
		t.Fatalf("listK8sObjects v3: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 pods, got %d", len(objs))
	}
}

// v4 normalization：{data:{list:[{data:<obj>}]}} → 拍平成 [<obj>]
func TestListK8sObjects_V4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/cluster-cache/direct") {
			_, _ = w.Write([]byte(`{"data":{"list":[{"data":{"metadata":{"name":"p1"}}},{"data":{"metadata":{"name":"p2"}}}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	s := &kuboardSetupResult{
		ctx: context.Background(), client: srv.Client(),
		base: srv.URL, version: "v4", token: "t", clusterUID: "uid",
	}
	objs, err := s.listK8sObjects("pods", "app", "")
	if err != nil {
		t.Fatalf("listK8sObjects v4: %v", err)
	}
	if len(objs) != 2 || !strings.Contains(string(objs[0]), "p1") {
		t.Fatalf("v4 normalize failed: %v", objs)
	}
}

func TestKuboardV3ConfigMapData(t *testing.T) {
	srv := newV3MockServer(t)
	defer srv.Close()
	data, err := kuboardV3ConfigMapData(context.Background(), srv.Client(), srv.URL, "ck", "c1", "app", "cm1")
	if err != nil {
		t.Fatalf("kuboardV3ConfigMapData: %v", err)
	}
	if data["DB_HOST"] != "db" || data["REDIS_PORT"] != "6379" {
		t.Errorf("cm data = %v", data)
	}
}
