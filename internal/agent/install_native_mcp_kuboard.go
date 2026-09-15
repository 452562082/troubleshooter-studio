package agent

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

type kuboardMCPEndpoint struct {
	source, env, url, key string
	runtime               bool
}

// Config sources and standalone runtime connections have distinct keys. No
// network calls belong in the builder: discovery happens in the wizard, and
// explicit URLs are validated by the existing install-time runtime probe.
func kuboardMCPEndpoints(cfg *config.SystemConfig) []kuboardMCPEndpoint {
	var endpoints []kuboardMCPEndpoint
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "kuboard" {
			continue
		}
		for _, env := range cfg.Environments {
			item := kuboardMCPEndpoint{source: cc.ID, env: env.ID}
			for _, ep := range cc.Endpoints {
				if ep.Env == env.ID {
					item.url = ep.MCPURL
					item.key = ep.AccessKey
					break
				}
			}
			endpoints = append(endpoints, item)
		}
	}
	runtime := cfg.Infrastructure.Observability.K8sRuntime
	if runtime.Enabled && !strings.EqualFold(runtime.Provider, "one2all") {
		for _, env := range cfg.Environments {
			item := kuboardMCPEndpoint{env: env.ID, runtime: true}
			for _, ep := range runtime.Endpoints {
				if ep.Env == env.ID {
					item.url = ep.MCPURL
					item.key = ep.AccessKey
					break
				}
			}
			endpoints = append(endpoints, item)
		}
	}
	return endpoints
}

func (ep kuboardMCPEndpoint) serverKey(agentID string) string {
	if ep.runtime {
		return mcpKeyForAgent(agentID, "k8s-kuboard", "", ep.env)
	}
	return mcpKeyForAgent(agentID, "kuboard", ep.source, ep.env)
}

func (b *mcpBuilder) buildKuboard(servers map[string]any) {
	for _, ep := range kuboardMCPEndpoints(b.cfg) {
		credSource := ep.source
		if ep.runtime {
			credSource = ""
		}
		endpoint := firstNonEmpty(b.get(envVar("KUBOARD_MCP_URL", credSource, ep.env)), ep.url)
		key := firstNonEmpty(b.get(envVar("KUBOARD_ACCESS_KEY", credSource, ep.env)), ep.key)
		if strings.TrimSpace(endpoint) == "" || (b.opts.PruneEmpty && strings.TrimSpace(key) == "") {
			continue
		}
		servers[ep.serverKey(b.opts.AgentID)] = map[string]any{
			"type": "streamable-http", "url": endpoint,
			"headers": map[string]string{"Authorization": "Bearer " + key, "Accept": "application/json, text/event-stream"},
		}
	}
}

// DiscoverKuboardMCP upgrades only successful authenticated native endpoints.
// HTTP-only servers remain usable without pretending that MCP is available.
// Never send a short-lived login token: callers supply a persistent access key.
func DiscoverKuboardMCP(ctx context.Context, base, accessKey string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(accessKey) == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/mcp"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	result := probeMCPHTTPFunc(ctx, u.String(), map[string]string{"Authorization": "Bearer " + accessKey}, 4*time.Second)
	if result.Err != nil || len(result.Tools) == 0 {
		return ""
	}
	return u.String()
}
