// self_test_openclaw.go —— openclaw 部署后自检的原生 Go 实现,替代之前
// templates/scripts/self-test.sh.tmpl(~140 行 bash + 嵌入式 Python)。
//
// 检查项(按 severity 排序):
//  1. workspace 目录存在
//  2. ~/.openclaw/openclaw.json 里 agents.list 含本 agent
//  3. ~/.openclaw/openclaw.json 里 mcp.servers 含 cfg 期望的全部 MCP key
//  4. 配置中心连通性:nacos TCP 探活
//  5. 可观测性连通性:grafana / loki HTTP /api/health 探活
//
// 不再做的检查:diagram-generator(node 子进程)、openclaw chat smoke(CLI 子进程)
// 这俩偏"端到端 LLM 验证",不在 Studio 自检的核心职责;真要试用户在 OpenClaw
// 客户端里发一句话即可。
package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// SelfTestResult 给 UI 展示自检明细。
type SelfTestResult struct {
	Checks []SelfTestCheck `json:"checks"` // 按检查顺序;UI 顺序展示
	OK     bool            `json:"ok"`     // 任一 FAIL 即 false;PASS/WARN/SKIP 不影响
}

// SelfTestCheck 单条检查结果。Status: PASS / FAIL / WARN / SKIP。
type SelfTestCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// SelfTestOpenclaw 跑一次 openclaw 自检。dir 接受 staging 或已部署的 workspace
// (从 tshoot.json 反读 cfg)。ctx 让长 timeout HTTP 探活能被取消。
func SelfTestOpenclaw(ctx context.Context, dir string) (*SelfTestResult, error) {
	cfg, _, err := loadCfgFromTshoot(dir)
	if err != nil {
		return nil, err
	}
	res := &SelfTestResult{OK: true}
	add := func(name, status, detail string) {
		if status == "FAIL" {
			res.OK = false
		}
		res.Checks = append(res.Checks, SelfTestCheck{Name: name, Status: status, Detail: detail})
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	wsDir := filepath.Join(home, ".openclaw", "workspace", strings.TrimSpace(cfg.ResolveWorkspaceName()))
	if _, err := os.Stat(wsDir); err == nil {
		add("workspace 目录", "PASS", wsDir)
	} else {
		add("workspace 目录", "FAIL", "缺失:"+wsDir)
	}

	cfgPath := filepath.Join(home, ".openclaw", "openclaw.json")
	ocData, err := readJSONOrEmpty(cfgPath)
	if err != nil {
		add("openclaw.json", "FAIL", err.Error())
		return res, nil
	}
	agentID := cfg.ResolveID()
	if hasAgentEntry(ocData, agentID) {
		add(fmt.Sprintf("agents.list 含 %s", agentID), "PASS", cfgPath)
	} else {
		add(fmt.Sprintf("agents.list 含 %s", agentID), "FAIL", "openclaw.json 里没找到这个 agent —— 重跑部署")
	}

	servers := getMCPServers(ocData)
	required := requiredMCPKeys(cfg)
	var missing []string
	for _, k := range required {
		if _, ok := servers[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		add(fmt.Sprintf("mcp.servers 齐全(%d 项)", len(required)), "PASS", "")
	} else {
		add("mcp.servers 齐全", "FAIL", "缺失:"+strings.Join(missing, ", "))
	}

	// nacos TCP 探活:多源逐个测
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range cfg.Environments {
			key := mcpKey("nacos-mcp-server", cc.ID, e.ID)
			addr := mcpEnv(servers, key, "NACOS_ADDR")
			label := "nacos TCP " + e.ID
			if cc.ID != "" && cc.ID != "default" {
				label = "nacos TCP " + cc.ID + "/" + e.ID
			}
			if !strings.Contains(addr, ":") {
				add(label, "WARN", "NACOS_ADDR 缺失,跳过探活")
				continue
			}
			if err := tcpProbe(ctx, addr, 4*time.Second); err != nil {
				add(label, "FAIL", fmt.Sprintf("%s 不通:%v", addr, err))
			} else {
				add(label, "PASS", addr)
			}
		}
	}

	// 配置源 HTTP 探活:apollo / consul / kuboard 都是 HTTP API。URL 取自
	// cc.Endpoints[].Addr —— Addr 可能裸 host:port 或带 scheme,统一兜底成 https://
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		urls := map[string]string{}
		for _, ep := range cc.Endpoints {
			a := strings.TrimSpace(ep.Addr)
			if a == "" {
				continue
			}
			if !strings.HasPrefix(a, "http") {
				a = "https://" + a // 裸 host[:port] 兜底
			}
			urls[ep.Env] = a
		}
		switch cc.Type {
		case "apollo":
			probeURLByEnv(ctx, cfg.Environments, urls,
				"apollo "+cc.ID, "/services/config", add)
		case "consul":
			probeURLByEnv(ctx, cfg.Environments, urls,
				"consul "+cc.ID, "/v1/status/leader", add)
		case "kuboard":
			probeURLByEnv(ctx, cfg.Environments, urls,
				"kuboard "+cc.ID, "/", add)
		}
	}

	// 可观测性 HTTP 探活(/api/health 200/401/403 都视作"reachable",
	// 401/403 = 站点活着但鉴权对不上;FAIL 仅给真不通的)
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		probeGrafanaLike(ctx, servers, cfg.Environments, "grafana-mcp-server", add)
	}
	if cfg.Infrastructure.Observability.Loki.Enabled {
		probeGrafanaLike(ctx, servers, cfg.Environments, "loki-mcp-server", add)
	}
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		probeURLByEnv(ctx, cfg.Environments,
			cfg.Infrastructure.Observability.Jaeger.URLByEnv, "jaeger", "/", add)
	}
	if cfg.Infrastructure.Observability.Prometheus.Enabled &&
		!cfg.Infrastructure.Observability.Prometheus.ViaGrafana {
		// 只在直连模式下单独探活;走 Grafana 代理时 grafana 探活已覆盖
		probeURLByEnv(ctx, cfg.Environments,
			map[string]string{}, "prometheus", "/-/healthy", add)
	}
	if cfg.Infrastructure.Observability.ELK.Enabled {
		probeURLByEnv(ctx, cfg.Environments,
			cfg.Infrastructure.Observability.ELK.KibanaByEnv, "kibana", "/api/status", add)
	}
	if cfg.Infrastructure.Observability.K8sRuntime.Enabled {
		probeURLByEnv(ctx, cfg.Environments,
			cfg.Infrastructure.Observability.K8sRuntime.URLByEnv, "kuboard-runtime", "/", add)
	}
	return res, nil
}

// probeURLByEnv 通用 HTTP 探活:遍历每个 env,GET <urls[envID]><pathSuffix>;
// urls 缺该 env → SKIP;HTTP <500 视为 reachable(401/403/404 都算站点活);≥500 才 FAIL。
// apollo / consul / kuboard / jaeger / elk / k8s_runtime / prometheus 共用。
func probeURLByEnv(
	ctx context.Context,
	envs []config.Environment,
	urls map[string]string,
	label, pathSuffix string,
	add func(name, status, detail string),
) {
	client := &http.Client{Timeout: 6 * time.Second}
	for _, e := range envs {
		url := strings.TrimRight(urls[e.ID], "/")
		if !strings.HasPrefix(url, "http") {
			add(label+" "+e.ID, "SKIP", "URL 缺失,跳过探活")
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+pathSuffix, nil)
		if err != nil {
			add(label+" "+e.ID, "FAIL", err.Error())
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			add(label+" "+e.ID, "FAIL", err.Error())
			continue
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode < 500:
			add(label+" "+e.ID, "PASS", fmt.Sprintf("HTTP %d", resp.StatusCode))
		default:
			add(label+" "+e.ID, "FAIL", fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
	}
}

// requiredMCPKeys 跟 injectMCPServers 的注入逻辑保持镜像:cfg 开关哪些 MCP,
// 这里就要哪些 key。任一缺失视为部署不完整。多源场景每个 nacos 源 × env 都要有。
func requiredMCPKeys(cfg *config.SystemConfig) []string {
	var out []string
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range cfg.Environments {
			out = append(out, mcpKey("nacos-mcp-server", cc.ID, e.ID))
		}
	}
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, "grafana-mcp-server-"+e.ID)
		}
	}
	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, "loki-mcp-server-"+e.ID)
		}
	}
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			out = append(out, "lark-openapi")
			break
		}
	}
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			out = append(out, "FeishuProjectMcp")
			break
		}
	}
	return out
}

func hasAgentEntry(root map[string]any, id string) bool {
	agents, _ := root["agents"].(map[string]any)
	if agents == nil {
		return false
	}
	list, _ := agents["list"].([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if existID, _ := m["id"].(string); existID == id {
				return true
			}
		}
	}
	return false
}

// getMCPServers 取 root["mcp"]["servers"] 嵌套 map,缺任一层就给 nil-safe 空 map。
func getMCPServers(root map[string]any) map[string]any {
	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		return map[string]any{}
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		return map[string]any{}
	}
	return servers
}

// mcpEnv 从 servers[mcpKey].env[envKey] 抽字符串(各层 nil-safe)。
func mcpEnv(servers map[string]any, mcpKey, envKey string) string {
	srv, _ := servers[mcpKey].(map[string]any)
	if srv == nil {
		return ""
	}
	envMap, _ := srv["env"].(map[string]any)
	if envMap == nil {
		return ""
	}
	v, _ := envMap[envKey].(string)
	return v
}

func tcpProbe(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func probeGrafanaLike(
	ctx context.Context,
	servers map[string]any,
	envs []config.Environment,
	prefix string,
	add func(name, status, detail string),
) {
	client := &http.Client{Timeout: 6 * time.Second}
	for _, e := range envs {
		key := prefix + "-" + e.ID
		url := strings.TrimRight(mcpEnv(servers, key, "GRAFANA_URL"), "/")
		if !strings.HasPrefix(url, "http") {
			add(prefix+" "+e.ID, "WARN", "GRAFANA_URL 缺失,跳过探活")
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/health", nil)
		if err != nil {
			add(prefix+" "+e.ID, "FAIL", err.Error())
			continue
		}
		user := mcpEnv(servers, key, "GRAFANA_USERNAME")
		pwd := mcpEnv(servers, key, "GRAFANA_PASSWORD")
		if user != "" || pwd != "" {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pwd)))
		}
		resp, err := client.Do(req)
		if err != nil {
			add(prefix+" "+e.ID, "FAIL", err.Error())
			continue
		}
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case 200, 401, 403:
			add(prefix+" "+e.ID, "PASS", fmt.Sprintf("HTTP %d", resp.StatusCode))
		default:
			add(prefix+" "+e.ID, "FAIL", fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
	}
}
