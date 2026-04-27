// install_prompts.go —— 从 system.yaml 推导 openclaw 部署需要哪些凭证字段。
//
// 历史:之前由 deploy.ParseInstallPrompts 扫 install.sh 里的 read_var 调用
// 拿到这份列表;现在 install.sh 已干掉,改成直接照 cfg 派生(跟原 install.sh
// 模板 1:1 对齐,字段名 / 顺序 / Secret 标都不变,UI 表单不用改)。
//
// 字段命名规范:
//   - per_env 凭证:VAR_<ENV>(ENV 大写),如 CC_ADDR_DEV / GRAFANA_URL_PROD
//   - 共享凭证:不带 _<ENV> 后缀,如 CONFIG_CENTER_USERNAME
package agent

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
)

// DerivePrompts 按 system.yaml 里 infrastructure 的开关 / 类型派生需要交互收集的凭证。
// 顺序跟原 install.sh 模板对齐(从配置中心 → 可观测性 → 模型 → messaging),
// 让"参照 .env 老文件 / 老用户对照来填"的体验不变。
func DerivePrompts(cfg *config.SystemConfig) []deploy.Prompt {
	var out []deploy.Prompt
	add := func(name, prompt string, secret bool) {
		out = append(out, deploy.Prompt{Name: name, Prompt: prompt, Secret: secret})
	}

	cc := cfg.Infrastructure.ConfigCenter
	envs := cfg.Environments

	// ── 配置中心 ──
	switch cc.Type {
	case "nacos":
		if cc.PerEnvCredentials {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				add("CC_ADDR_"+up, "NACOS 地址 ("+e.ID+") [host:port]: ", false)
				add("CC_USER_"+up, "NACOS 用户名 ("+e.ID+") []: ", false)
				add("CC_PASS_"+up, "NACOS 密码 ("+e.ID+") []: ", true)
			}
		} else {
			add("CONFIG_CENTER_USERNAME", "NACOS 用户名（所有 env 共用）[]: ", false)
			add("CONFIG_CENTER_PASSWORD", "NACOS 密码（所有 env 共用）[]: ", true)
			for _, e := range envs {
				add("CC_ADDR_"+strings.ToUpper(e.ID), "NACOS 地址 ("+e.ID+") [host:port]: ", false)
			}
		}
	case "apollo":
		if cc.PerEnvCredentials {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				add("APOLLO_META_"+up, "Apollo meta URL ("+e.ID+") [http://apollo-xxx:8080]: ", false)
				add("APOLLO_TOKEN_"+up, "Apollo Open API token ("+e.ID+") []: ", true)
			}
		} else {
			add("APOLLO_TOKEN", "Apollo Open API token（所有 env 共用，留空=未开启鉴权）[]: ", true)
			for _, e := range envs {
				add("APOLLO_META_"+strings.ToUpper(e.ID), "Apollo meta URL ("+e.ID+") [http://apollo-xxx:8080]: ", false)
			}
		}
	case "consul":
		if cc.PerEnvCredentials {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				add("CONSUL_HOST_"+up, "Consul host ("+e.ID+") [host:port 或 http://host:port]: ", false)
				add("CONSUL_TOKEN_"+up, "Consul ACL token ("+e.ID+") []: ", true)
			}
		} else {
			add("CONSUL_TOKEN", "Consul ACL token（所有 env 共用，留空=无 ACL）[]: ", true)
			for _, e := range envs {
				add("CONSUL_HOST_"+strings.ToUpper(e.ID), "Consul host ("+e.ID+") [host:port 或 http://host:port]: ", false)
			}
		}
	case "env-vars":
		// 静态连接串:per env per data store
		for _, e := range envs {
			eup := strings.ToUpper(e.ID)
			for _, ds := range cfg.Infrastructure.DataStores {
				if !ds.Enabled {
					continue
				}
				add(
					"STATIC_"+strings.ToUpper(ds.Type)+"_"+eup,
					ds.Type+" 地址 ("+e.ID+") [host:port 或 URI]: ",
					false,
				)
			}
		}
	case "kubernetes":
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			add("K8S_CONTEXT_"+up, "K8s context ("+e.ID+") [留空=当前 context]: ", false)
			add("K8S_NAMESPACE_"+up, "K8s namespace ("+e.ID+") [default]: ", false)
			add("K8S_CONFIGMAP_"+up, "ConfigMap 名称 ("+e.ID+") [app-config]: ", false)
			add("K8S_SECRET_"+up, "Secret 名称 ("+e.ID+") [留空=不用]: ", false)
		}
	}

	// ── Grafana ──
	if cfg.Infrastructure.Observability.Grafana.Enabled {
		if cfg.Infrastructure.Observability.Grafana.PerEnvCredentials {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				add("GRAFANA_URL_"+up, "Grafana URL ("+e.ID+") []: ", false)
				add("GRAFANA_USER_"+up, "Grafana 用户名 ("+e.ID+") []: ", false)
				add("GRAFANA_PASS_"+up, "Grafana 密码 ("+e.ID+") []: ", true)
			}
		} else {
			add("GRAFANA_USERNAME", "Grafana 用户名（所有 env 共用）[]: ", false)
			add("GRAFANA_PASSWORD", "Grafana 密码（所有 env 共用）[]: ", true)
			for _, e := range envs {
				add("GRAFANA_URL_"+strings.ToUpper(e.ID), "Grafana URL ("+e.ID+") []: ", false)
			}
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
		add("ELK_USERNAME", "ELK 用户名（共用，留空=无鉴权）[]: ", false)
		add("ELK_PASSWORD", "ELK 密码（共用）[]: ", true)
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
