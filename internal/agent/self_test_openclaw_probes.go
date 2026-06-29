// self_test_openclaw_probes.go —— openclaw 自检的网络/MCP 探活 helper 集合。
//
// SelfTestOpenclaw(主流程在 self_test_openclaw.go)用这些 helper 跑配置中心 / 可观测性
// 的连通性探活,以及从 mcp.servers map 里反查注入字段:
//   - tcpProbe:nacos / consul 这类裸 TCP 服务
//   - probeURLByEnv:apollo/kuboard/jaeger/elk/k8s_runtime/prometheus 共用 HTTP /api/health
//   - probeGrafanaLike:grafana/loki HTTP /api/health(带 basic auth)
//   - requiredMCPKeys:cfg → 期望的 MCP key 列表(跟 injectMCPServers 镜像)
//   - getMCPServers / mcpEnv:从 root 反向查 mcp.servers.<key>.env.<envKey>

package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

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
// agentID 加在所有 key 前缀,跟 injectMCPServers / install_native_mcp 三平台命名统一。
func requiredMCPKeys(cfg *config.SystemConfig, agentID string) []string {
	withAgent := func(name string) string {
		if agentID == "" {
			return name
		}
		return agentID + "-" + name
	}
	var out []string
	// nacos(plan D):自研本地 MCP 脚本,每 source × env 一个实例,跟 buildNacos 镜像。
	// openclaw injectMCPServers 走 PruneEmpty=false 全注册,所以所有 source×env 都该在。
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range cfg.Environments {
			out = append(out, withAgent(mcpKey("nacos", cc.ID, e.ID)))
		}
	}
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("grafana-"+e.ID))
		}
	}
	// loki MCP 已合并进 grafana MCP(同款 mcp-grafana-npx 二进制本就含 query_loki_*),
	// 不再单独注册 loki-<env>。validate 阶段强制 Loki.Enabled ⇒ Grafana.Enabled,
	// 这里也就没"独立 loki" 期望了。
	// jaeger / elk:2026-05 都从 curl 占位升级到真 MCP(uvx opentelemetry-mcp /
	// npx @elastic/mcp-server-elasticsearch),openclaw injectMCPServers 必注册。
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("jaeger-"+e.ID))
		}
	}
	if cfg.Infrastructure.Observability.ELK.Enabled {
		for _, e := range cfg.Environments {
			out = append(out, withAgent("elk-"+e.ID))
		}
	}
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			out = append(out, withAgent("lark-openapi"))
			break
		}
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		if !ds.Enabled || !dataStoreRegistersMCP(ds.Type) {
			continue
		}
		for _, e := range cfg.Environments {
			unique := dsEndpointsUnique(ds, e.ID)
			if len(unique) == 0 {
				out = append(out, withAgent(mcpKey(ds.Type, "", e.ID)))
				continue
			}
			single := len(unique) <= 1
			for _, ep := range unique {
				sourceID := ""
				if !single {
					sourceID = ep.sourceID
				}
				out = append(out, withAgent(mcpKey(ds.Type, sourceID, e.ID)))
			}
		}
	}
	// 注:feishu_project 不在 requiredMCPKeys —— 2026-05-15 审计后暂时禁用 mcp 注册
	// (@lark-project/mcp v0.0.1 是字节内部 prototype),buildFeishuProject 仅打 warn。
	// yaml 仍合法,字节发正式版后翻 buildFeishuProject 即可重启用,届时这里也补回 FeishuProjectMcp。
	return out
}

func dataStoreRegistersMCP(typ string) bool {
	switch typ {
	case "mongodb", "postgresql", "elasticsearch", "redis", "mysql", "doris", "clickhouse", "kafka":
		return true
	case "rabbitmq":
		return false // 方案 B:凭据仍收,但 MCP 不注册,SKILL 走 HTTP Management API
	default:
		return false
	}
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

// probeGrafanaLike 探活 grafana / loki 的 HTTP /api/health。
// 注意:第 5 个参数原叫 agentID,实际语义是"MCP key 前缀",必须用 cfg.MCPKeyPrefix()
// (=system.id 短前缀,如 "truss")拼 key,而不是 cfg.ResolveID()(完整 agent 标识,
// 如 "truss-troubleshooter")—— install 路径 mcpKeyForAgent 第一参也是用 mcpPrefix,
// 两边必须一致,否则 self-test 永远查不到 mcp.servers.<key>.env.GRAFANA_URL,
// 误报 "GRAFANA_URL 缺失"(用户实测撞过)。这里把参数名改回 mcpPrefix 防再次踩坑。
func probeGrafanaLike(
	ctx context.Context,
	servers map[string]any,
	envs []config.Environment,
	prefix string,
	mcpPrefix string,
	add func(name, status, detail string),
) {
	client := &http.Client{Timeout: 6 * time.Second}
	for _, e := range envs {
		key := prefix + "-" + e.ID
		if mcpPrefix != "" {
			key = mcpPrefix + "-" + key
		}
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
		case http.StatusOK, http.StatusUnauthorized, http.StatusForbidden:
			add(prefix+" "+e.ID, "PASS", fmt.Sprintf("HTTP %d", resp.StatusCode))
		default:
			add(prefix+" "+e.ID, "FAIL", fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
	}
}
