// install_prompts.go —— 从 troubleshooter.yaml 推导 openclaw 部署需要哪些凭证字段。
//
// 多源 schema:遍历 cfg.Infrastructure.ConfigCenters,每个源独立产 prompt 集合,
// 命名空间通过 envVar(prefix, source.id, env) 区隔。详见 install_naming.go。
//
// 历史:之前由 deploy.ParseInstallPrompts 扫 install.sh 里的 read_var 调用拿这份
// 列表;install.sh 已干掉,改成直接照 cfg 派生(跟原 install.sh 模板 1:1 对齐,
// 字段名 / 顺序 / Secret 标都不变,UI 表单不用改)。
package agent

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
)

// DerivePrompts 按 troubleshooter.yaml 派生需要交互收集的凭证。
// 顺序:每个 config_centers 源依次走自己的字段块 → grafana / jaeger / elk / model / lark / feishu。
func DerivePrompts(cfg *config.SystemConfig) []deploy.Prompt {
	var out []deploy.Prompt
	add := func(name, prompt string, secret bool) {
		out = append(out, deploy.Prompt{Name: name, Prompt: prompt, Secret: secret})
	}

	envs := cfg.Environments

	// ── 多源配置中心,逐个产 prompt ──
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		sourcePrefix := configCenterLabel(cc)
		switch cc.Type {
		case "nacos":
			for _, e := range envs {
				add(envVar("CC_ADDR", cc.ID, e.ID), "NACOS 地址 ("+sourcePrefix+e.ID+") [host:port]: ", false)
				add(envVar("CC_USER", cc.ID, e.ID), "NACOS 用户名 ("+sourcePrefix+e.ID+") []: ", false)
				add(envVar("CC_PASS", cc.ID, e.ID), "NACOS 密码 ("+sourcePrefix+e.ID+") []: ", true)
			}
		case "apollo":
			for _, e := range envs {
				add(envVar("APOLLO_META", cc.ID, e.ID), "Apollo meta URL ("+sourcePrefix+e.ID+") [http://apollo-xxx:8080]: ", false)
				add(envVar("APOLLO_TOKEN", cc.ID, e.ID), "Apollo Open API token ("+sourcePrefix+e.ID+") []: ", true)
			}
		case "consul":
			for _, e := range envs {
				add(envVar("CONSUL_HOST", cc.ID, e.ID), "Consul host ("+sourcePrefix+e.ID+") [host:port 或 http://host:port]: ", false)
				add(envVar("CONSUL_TOKEN", cc.ID, e.ID), "Consul ACL token ("+sourcePrefix+e.ID+") []: ", true)
			}
		case "env-vars":
			// 静态连接串:per env per data store(注:env-vars 跟 data_stores 是系统级,
			// 不是源级 —— 这里仍然按 source 命名空间隔离 prompt,但同源内逻辑跟之前一致)
			for _, e := range envs {
				for _, ds := range cfg.Infrastructure.DataStores {
					if !ds.Enabled {
						continue
					}
					add(
						envVar("STATIC_"+strings.ToUpper(ds.Type), cc.ID, e.ID),
						ds.Type+" 地址 ("+sourcePrefix+e.ID+") [host:port 或 URI]: ",
						false,
					)
				}
			}
		case "kuboard":
			// Kuboard:走 Kuboard 自家 HTTP API。每 env 一份 URL + 鉴权。
			// 鉴权二选一:API 访问凭证(免账密,推荐)或 username+password。两条都收一遍,bot 端按
			// "access_key 优先"取。cluster/namespace/configmap 改为 per-service,从 service_map 读,
			// 不再 install 时问。
			for _, e := range envs {
				add(envVar("KUBOARD_URL", cc.ID, e.ID), "Kuboard URL ("+sourcePrefix+e.ID+") [https://kuboard.example.com]: ", false)
				add(envVar("KUBOARD_ACCESS_KEY", cc.ID, e.ID), "Kuboard API 访问凭证 ("+sourcePrefix+e.ID+",留空走账密): ", true)
				add(envVar("KUBOARD_USER", cc.ID, e.ID), "Kuboard 用户名 ("+sourcePrefix+e.ID+",已填 access_key 可留空) []: ", false)
				add(envVar("KUBOARD_PASS", cc.ID, e.ID), "Kuboard 密码 ("+sourcePrefix+e.ID+",已填 access_key 可留空) []: ", true)
			}
		}
	}

	// ── Grafana ──(系统级,不分 source;每个 env 独立凭证)
	// 鉴权两路:API key(service account token,Grafana 9.1+ 推荐)/ basic auth。
	// 都收 — BuildMCPServers 优先 API key,空则回 user/pass。用户填一种即可。
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			add("GRAFANA_URL_"+up, "Grafana URL ("+e.ID+") []: ", false)
			add("GRAFANA_API_KEY_"+up, "Grafana API Key / Service Account Token ("+e.ID+") [留空=改用用户名密码]: ", true)
			add("GRAFANA_USER_"+up, "Grafana 用户名 ("+e.ID+",API key 已填则可留空) []: ", false)
			add("GRAFANA_PASS_"+up, "Grafana 密码 ("+e.ID+",API key 已填则可留空) []: ", true)
		}
	}

	// ── Jaeger ──
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range envs {
			add(
				"JAEGER_URL_"+strings.ToUpper(e.ID),
				"Jaeger URL ("+e.ID+") [http://jaeger-xxx:16686]: ",
				false,
			)
		}
	}

	// ── ELK ──
	if cfg.Infrastructure.Observability.ELK.Enabled {
		add("ELK_USERNAME", "ELK 用户名(共用,留空=无鉴权) []: ", false)
		add("ELK_PASSWORD", "ELK 密码(共用) []: ", true)
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			add("KIBANA_URL_"+up, "Kibana URL ("+e.ID+") [留空=不用]: ", false)
			add("ELK_ES_URL_"+up, "Elasticsearch URL ("+e.ID+") [http://es-xxx:9200]: ", false)
		}
	}

	// ── 模型 ──
	defaultModel := cfg.Agent.ModelForTarget("openclaw")
	add("MODEL", "Agent 模型 ["+defaultModel+"]: ", false)

	// ── messaging:lark ──
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			add("LARK_APP_ID", "Lark APP_ID []: ", false)
			add("LARK_APP_SECRET", "Lark APP_SECRET []: ", true)
			break
		}
	}

	// ── project tracking:feishu_project ──
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			add("MCP_USER_TOKEN", "Feishu MCP User Token []: ", false)
			break
		}
	}

	return out
}

// configCenterLabel 给 prompt 文案用的"源标识"前缀。单源迁移路径不展示前缀
// (沿用老 prompt 文案);多源场景前缀化让用户分清是哪个源。
func configCenterLabel(cc config.ConfigCenter) string {
	if cc.ID == "" || cc.ID == "default" {
		return ""
	}
	return cc.ID + "/"
}

