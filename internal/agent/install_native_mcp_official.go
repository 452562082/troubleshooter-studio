package agent

import (
	"net/url"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func usesOfficialMCP(cfg *config.SystemConfig, name string) bool {
	if name == "skywalking" {
		return cfg.Infrastructure.Observability.SkyWalking.Enabled
	}
	if name == "consul" {
		for _, cc := range cfg.Infrastructure.ConfigCenters {
			if cc.Type == "consul" {
				return true
			}
		}
	}
	return false
}

func (b *mcpBuilder) buildConsul(servers map[string]any) {
	binary := b.opts.OfficialMCPBinaryPaths["consul"]
	if binary == "" {
		return
	}
	for _, cc := range b.cfg.Infrastructure.ConfigCenters {
		if cc.Type != "consul" {
			continue
		}
		for _, e := range b.cfg.Environments {
			var host, token string
			for _, ep := range cc.Endpoints {
				if ep.Env == e.ID {
					host, token = ep.Host, ep.Token
					break
				}
			}
			host = firstNonEmpty(b.get(envVar("CONSUL_HOST", cc.ID, e.ID)), host)
			token = firstNonEmpty(b.get(envVar("CONSUL_TOKEN", cc.ID, e.ID)), token)
			if strings.TrimSpace(host) == "" {
				continue
			}
			if !strings.Contains(host, "://") {
				host = "http://" + host
			}
			u, err := url.Parse(host)
			if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				continue
			}
			servers[b.keyFor("consul", cc.ID, e.ID)] = map[string]any{
				"command": binary, "args": []any{"stdio"},
				"env": b.envBlock(map[string]any{"CONSUL_HTTP_ADDR": u.String(), "CONSUL_HTTP_TOKEN": token, "CONSUL_MCP_SERVER_READ_GITHUB_RESOURCES": "false", "CONSUL_ENTERPRISE": "false"}),
			}
		}
	}
}

func (b *mcpBuilder) buildSkyWalking(servers map[string]any) {
	sw := b.cfg.Infrastructure.Observability.SkyWalking
	binary := b.opts.OfficialMCPBinaryPaths["skywalking"]
	if !sw.Enabled || binary == "" {
		return
	}
	for _, e := range b.cfg.Environments {
		raw, user, pass := sw.URLByEnv[e.ID], "", ""
		for _, ep := range sw.Endpoints {
			if ep.Env == e.ID {
				raw, user, pass = firstNonEmpty(ep.URL, raw), ep.User, ep.Pass
				break
			}
		}
		up := strings.ToUpper(e.ID)
		raw = firstNonEmpty(b.get("SKYWALKING_URL_"+up), raw)
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		if u.User != nil {
			user = u.User.Username()
			pass, _ = u.User.Password()
			u.User = nil
		}
		user = firstNonEmpty(b.get("SKYWALKING_USER_"+up), user)
		pass = firstNonEmpty(b.get("SKYWALKING_PASS_"+up), pass)
		args := []any{"stdio", "--sw-url", u.String()}
		if user != "" || pass != "" {
			args = append(args, "--sw-username", "${SKYWALKING_USER}", "--sw-password", "${SKYWALKING_PASS}")
		}
		servers[b.keyFor("skywalking", "", e.ID)] = map[string]any{"command": binary, "args": args, "env": b.envBlock(map[string]any{"SKYWALKING_USER": user, "SKYWALKING_PASS": pass})}
	}
}
