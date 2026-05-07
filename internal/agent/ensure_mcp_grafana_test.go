package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 防回归:downloadAndExtractMCPGrafana 必须用带 Timeout 的 client。
// 历史 bug:用 http.Get 默认 client(Timeout=0)在 GitHub 出站不通的网络
// 环境下让 install 永远挂死(UI 看到"部署中"无限转)。
func TestMCPGrafanaHTTPClientHasTimeout(t *testing.T) {
	if mcpGrafanaHTTPClient.Timeout == 0 {
		t.Fatal("mcpGrafanaHTTPClient.Timeout is 0 — 网络不通时 install 会无声死锁,必须设 Timeout")
	}
	// 5 分钟是当前选的硬上限;调小没问题(更激进降级到 npx),但变成 0 / 过大就触发本测试
	if mcpGrafanaHTTPClient.Timeout > 10*time.Minute {
		t.Errorf("Timeout %v 太长,失去快速降级到 npx 的意义", mcpGrafanaHTTPClient.Timeout)
	}
}

// 防回归:慢响应必须超时,而不是无限等。
func TestDownloadHonoursTimeoutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 一直挂着不返回,模拟 GitHub 出站连接挂死
		<-r.Context().Done()
	}))
	defer srv.Close()

	// 临时把 client 替换成 50ms 超时的版本,免得测试本身跑 5 分钟
	saved := mcpGrafanaHTTPClient
	mcpGrafanaHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	defer func() { mcpGrafanaHTTPClient = saved }()

	start := time.Now()
	err := downloadAndExtractMCPGrafana(srv.URL, t.TempDir()+"/mcp-grafana")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("超时太慢 (%v) — client.Timeout 没生效", elapsed)
	}
	// 错误信息要带"失败"提示,让用户知道走 npx 兜底
	if !strings.Contains(err.Error(), "拉 mcp-grafana 失败") {
		t.Errorf("error message 缺少用户友好的中文提示:%v", err)
	}
}
