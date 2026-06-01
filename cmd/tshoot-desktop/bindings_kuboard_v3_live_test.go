package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestKuboardV3Live 对真实 Kuboard v3 实例端到端验证 v3 适配。默认 skip;
// 设环境变量才跑:
//
//	KUBOARD_URL=https://kuboard.guadd.fun \
//	KUBOARD_USER=admin \
//	KUBOARD_KEY=<密钥ID>.<密钥> \
//	KUBOARD_CLUSTER=jw-was-k8s-test \
//	go test ./cmd/tshoot-desktop/ -run TestKuboardV3Live -v
func TestKuboardV3Live(t *testing.T) {
	base := os.Getenv("KUBOARD_URL")
	user := os.Getenv("KUBOARD_USER")
	key := os.Getenv("KUBOARD_KEY")
	cluster := os.Getenv("KUBOARD_CLUSTER")
	if base == "" || user == "" || key == "" || cluster == "" {
		t.Skip("set KUBOARD_URL/USER/KEY/CLUSTER to run live v3 test")
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if v := kuboardDetectVersion(ctx, client, base, key); v != "v3" {
		t.Fatalf("detect version = %q, want v3", v)
	}
	res, err := kuboardListResourcesV3(ctx, client, base, user, key, cluster)
	if err != nil {
		t.Fatalf("kuboardListResourcesV3: %v", err)
	}
	if len(res.Clusters) != 1 || res.Clusters[0].Name != cluster {
		t.Fatalf("want 1 cluster %q, got %+v", cluster, res.Clusters)
	}
	nsCount := len(res.Clusters[0].Namespaces)
	if nsCount == 0 {
		t.Fatalf("no namespaces returned; notes=%v", res.Notes)
	}
	cmTotal := 0
	for _, ns := range res.Clusters[0].Namespaces {
		cmTotal += len(ns.ConfigMaps)
	}
	t.Logf("v3 list OK: cluster=%s namespaces=%d configmaps(total)=%d notes=%v",
		cluster, nsCount, cmTotal, res.Notes)

	// kuboardSetup v3 分支 + 规范化访问器(pods)+ 单 cm 读
	s, err := kuboardSetup(context.Background(), base, key, user, "", cluster)
	if err != nil {
		t.Fatalf("kuboardSetup(v3): %v", err)
	}
	defer s.cancel()
	if s.version != "v3" {
		t.Fatalf("kuboardSetup version = %q, want v3", s.version)
	}
	pods, err := s.listK8sObjects("pods", "default", "")
	if err != nil {
		t.Fatalf("listK8sObjects(pods): %v", err)
	}
	t.Logf("v3 pods OK: default ns pods=%d", len(pods))

	data, err := kuboardV3ConfigMapData(s.ctx, s.client, base, s.cookie, cluster, "default", "user-config")
	if err != nil {
		t.Fatalf("kuboardV3ConfigMapData: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("user-config cm has no data")
	}
	t.Logf("v3 configmap OK: default/user-config data keys=%d", len(data))
}
