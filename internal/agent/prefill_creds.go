// prefill_creds.go —— 从 system.yaml 抽 install 阶段需要的凭证 / URL 默认值。
//
// 背景:GUI wizard 把用户在 Step 7 填的 URL / 账密 / token 直接写进 yaml 的 endpoints[]。
// 但 install 阶段(InstallNativeOpenclaw / RunInstall)只认 .env 里的环境变量(KUBOARD_URL_DEV
// 这种)。如果用户:
//   - 走 BotsPage 的"导入 yaml 一键部署"  → 没经过 wizard Step 7 表单,creds 是空的
//   - 走 Editor 的"修改 yaml 后部署"      → 同上
// 这时如果不从 yaml 抽默认值,用户会被反复要求"再填一遍"已经在 yaml 里写过的内容。
//
// PrefillCredsFromYAML 解决这个:
//   - 输入: cfg(已经 LoadFromBytes 过,migrate 已跑)
//   - 输出: env var key(KUBOARD_URL_DEV 等) → value
//   - 用法: caller 把这份 map 跟用户表单填的 creds 合并,**用户填的优先**
//          (用户在 UI 改了的值不要被 yaml 覆盖)
package agent

import (
	"maps"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// PrefillCredsFromYAML 抽出 yaml endpoints[] / *_by_env / 凭证字段 → install env var map。
// 安全约束:只抽非空字段;对应 prompt 不存在(组件未 enabled)的 key 不输出。
func PrefillCredsFromYAML(cfg *config.SystemConfig) map[string]string {
	out := map[string]string{}
	put := func(k, v string) {
		if k == "" || v == "" {
			return
		}
		out[k] = v
	}

	// ── 配置中心:per source × env ──
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		for _, ep := range cc.Endpoints {
			if ep.Env == "" {
				continue
			}
			switch cc.Type {
			case "nacos":
				put(envVar("CC_ADDR", cc.ID, ep.Env), ep.Addr)
				put(envVar("CC_USER", cc.ID, ep.Env), ep.User)
				put(envVar("CC_PASS", cc.ID, ep.Env), ep.Pass)
			case "apollo":
				put(envVar("APOLLO_META", cc.ID, ep.Env), ep.MetaURL)
				put(envVar("APOLLO_TOKEN", cc.ID, ep.Env), ep.Token)
			case "consul":
				put(envVar("CONSUL_HOST", cc.ID, ep.Env), ep.Host)
				put(envVar("CONSUL_TOKEN", cc.ID, ep.Env), ep.Token)
			case "kuboard":
				put(envVar("KUBOARD_URL", cc.ID, ep.Env), ep.URL)
				put(envVar("KUBOARD_ACCESS_KEY", cc.ID, ep.Env), ep.AccessKey)
				put(envVar("KUBOARD_USER", cc.ID, ep.Env), ep.User)
				put(envVar("KUBOARD_PASS", cc.ID, ep.Env), ep.Pass)
			}
		}
	}

	obs := cfg.Infrastructure.Observability

	// ── Grafana(系统级,per env)──
	if obs.Grafana.Enabled {
		// 优先 endpoints[](GUI 新 schema),fallback URLByEnv(老 schema 已被 loader 反向迁移)
		for _, ep := range obs.Grafana.Endpoints {
			up := strings.ToUpper(ep.Env)
			if up == "" {
				continue
			}
			put("GRAFANA_URL_"+up, ep.URL)
			put("GRAFANA_USER_"+up, ep.User)
			put("GRAFANA_PASS_"+up, ep.Pass)
			put("GRAFANA_API_KEY_"+up, ep.APIKey)
		}
		for env, url := range obs.Grafana.URLByEnv {
			up := strings.ToUpper(env)
			if _, exists := out["GRAFANA_URL_"+up]; !exists {
				put("GRAFANA_URL_"+up, url)
			}
		}
	}

	// ── Jaeger ──
	if obs.Jaeger.Enabled {
		for _, ep := range obs.Jaeger.Endpoints {
			put("JAEGER_URL_"+strings.ToUpper(ep.Env), ep.URL)
		}
		for env, url := range obs.Jaeger.URLByEnv {
			up := strings.ToUpper(env)
			if _, exists := out["JAEGER_URL_"+up]; !exists {
				put("JAEGER_URL_"+up, url)
			}
		}
	}

	// ── ELK ──
	if obs.ELK.Enabled {
		for _, ep := range obs.ELK.Endpoints {
			up := strings.ToUpper(ep.Env)
			put("KIBANA_URL_"+up, ep.KibanaURL)
			put("ELK_ES_URL_"+up, ep.ESURL)
			// ELK 用户名/密码是系统级共享(install_prompts 里 ELK_USERNAME / ELK_PASSWORD,不带 env 后缀),
			// 取第一条非空的当默认
			if ep.User != "" {
				if _, exists := out["ELK_USERNAME"]; !exists {
					out["ELK_USERNAME"] = ep.User
				}
			}
			if ep.Pass != "" {
				if _, exists := out["ELK_PASSWORD"]; !exists {
					out["ELK_PASSWORD"] = ep.Pass
				}
			}
		}
		for env, url := range obs.ELK.KibanaByEnv {
			up := strings.ToUpper(env)
			if _, exists := out["KIBANA_URL_"+up]; !exists {
				put("KIBANA_URL_"+up, url)
			}
		}
		for env, url := range obs.ELK.ESByEnv {
			up := strings.ToUpper(env)
			if _, exists := out["ELK_ES_URL_"+up]; !exists {
				put("ELK_ES_URL_"+up, url)
			}
		}
	}

	// ── 模型 ──
	if m := cfg.Agent.ModelForTarget("openclaw"); m != "" {
		put("MODEL", m)
	}

	return out
}

// MergeCredsWithPrefill 把 prefill 默认值跟 user 提交的 creds 合并:
//   - user 提交的 key 即便 value 是空字符串也保留(可能用户故意清空,e.g. 切了鉴权方式)
//   - prefill 只填 user 没提交的 key
//
// caller 用法:
//   userCreds := <来自 UI 表单>
//   final := agent.MergeCredsWithPrefill(userCreds, agent.PrefillCredsFromYAML(cfg))
//   pass final to RunInstall / InstallNativeOpenclaw
func MergeCredsWithPrefill(user, prefill map[string]string) map[string]string {
	out := make(map[string]string, len(user)+len(prefill))
	maps.Copy(out, prefill)
	maps.Copy(out, user) // user wins (即便空值也覆盖,用户主动清空算意图)
	return out
}
