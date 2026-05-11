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
	"fmt"
	"os"
	"os/exec"
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
	mcpPrefix := cfg.MCPKeyPrefix() // MCP server key 用短前缀(system.id),跟 inject/IDE 一致
	if hasAgentEntry(ocData, agentID) {
		add(fmt.Sprintf("agents.list 含 %s", agentID), "PASS", cfgPath)
	} else {
		add(fmt.Sprintf("agents.list 含 %s", agentID), "FAIL", "openclaw.json 里没找到这个 agent —— 重跑部署")
	}

	servers := getMCPServers(ocData)
	required := requiredMCPKeys(cfg, mcpPrefix)
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
			key := mcpKeyForAgent(mcpPrefix, "nacos", cc.ID, e.ID)
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

	// 配置源 HTTP 探活:apollo / consul / kuboard 都是 HTTP API。URL 字段按 type 不同:
	//   - kuboard: ep.URL          (GUI wizard 写的 url 字段)
	//   - apollo:  ep.MetaURL      (apollo meta_url)
	//   - consul:  ep.Host         (consul host)
	//   - nacos / 其它:    ep.Addr (兜底,裸 host:port)
	// 之前一律读 ep.Addr,kuboard 部署后 self-test 永远显示 "URL 缺失,跳过探活"
	// (用户实测撞过)—— 因为 GUI 把 kuboard URL 写到了 .URL 字段,Addr 是空。
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		urls := map[string]string{}
		for _, ep := range cc.Endpoints {
			// 按 type 取该 type 真正的 URL 字段;空时回落 Addr(老 schema 兼容)
			var a string
			switch cc.Type {
			case "kuboard":
				a = strings.TrimSpace(ep.URL)
			case "apollo":
				a = strings.TrimSpace(ep.MetaURL)
			case "consul":
				a = strings.TrimSpace(ep.Host)
			}
			if a == "" {
				a = strings.TrimSpace(ep.Addr)
			}
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
		probeGrafanaLike(ctx, servers, cfg.Environments, "grafana", mcpPrefix, add)
	}
	if cfg.Infrastructure.Observability.Loki.Enabled {
		probeGrafanaLike(ctx, servers, cfg.Environments, "loki", mcpPrefix, add)
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

	// 工具链探活:nacos/grafana/loki/lark MCP 都靠 uvx / npx 起子进程,本机缺这俩 PATH
	// 时所有 MCP 调用都跑不起来。装完一眼看出来比 Day 1 调 MCP 第一次失败再排好。
	checkToolchain(cfg, add)

	return res, nil
}

// checkToolchain 看 cfg 里哪些 MCP 用 uvx / npx,逐个 which 探活;缺则 FAIL + 给 brew/nvm 安装提示。
func checkToolchain(cfg *config.SystemConfig, add func(name, status, detail string)) {
	needUvx := false
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type == "nacos" {
			needUvx = true
			break
		}
	}
	needNpx := cfg.Infrastructure.Observability.Grafana.Enabled ||
		cfg.Infrastructure.Observability.Loki.Enabled
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			needNpx = true
			break
		}
	}
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			needNpx = true
			break
		}
	}

	if needUvx {
		if path, err := exec.LookPath("uvx"); err == nil {
			add("uvx 可用", "PASS", path)
		} else {
			add("uvx 可用", "FAIL", "PATH 里没找到 uvx;装 uv:`brew install uv` 或 `curl -LsSf https://astral.sh/uv/install.sh | sh`(nacos-mcp 跑不起来)")
		}
	}
	if needNpx {
		if path, err := exec.LookPath("npx"); err == nil {
			add("npx 可用", "PASS", path)
		} else {
			add("npx 可用", "FAIL", "PATH 里没找到 npx;装 Node:`brew install node` 或 `nvm install --lts`(grafana/loki/lark MCP 跑不起来)")
		}
	}
}
