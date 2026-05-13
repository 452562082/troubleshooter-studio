// install_native_openclaw_mcp.go —— openclaw 部署:把各类 MCP server 注入
// ~/.openclaw/openclaw.json 的 mcp.servers map。
//
// 派生逻辑(nacos × env / grafana / loki / lark / feishu / jaeger / elk / 数据层)收口在
// install_native_mcp_common.go::BuildMCPServers,跟 IDE 三家共用同一份。本文件只剩
// "把 servers 写到 root["mcp"]["servers"]" 这层 openclaw.json 专属容器逻辑。
//
// 区别:openclaw 走 PruneEmpty=false(留全 schema 让 agent 自决);IDE 反过来。
// 老的 IncludeRawObsCurl 在 jaeger / elk 都迁到真 MCP 后已删。

package agent

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// injectMCPServers 按 cfg 的 infra 开关往 mcp.servers map 里塞每条 MCP 配置。
// 全量重写匹配前缀的旧条目(避免 env 删了 / 切了 config-center 类型留下死引用)。
//
// ocHome:openclaw 用户目录(~/.openclaw),用于 ensure grafana mcp 二进制下载到
// <ocHome>/bin/mcp-grafana,并把 BuildMCPServers 输出的 __GRAFANA_MCP_BIN__ 占位
// sentinel 替换成绝对路径 — 否则 spawn 时报 ENOENT。
func injectMCPServers(
	root map[string]any,
	cfg *config.SystemConfig,
	get func(string) string,
	ocHome string,
) error {
	// MCP server key 用短 prefix(system.id),跟 IDE 平台对齐 + 避免 tool 名超 60 字限制。
	// 清老版本下载到 <ocHome>/bin/ 的 mcp-grafana 孤儿二进制(改 npx 后留着没用)
	removeLegacyGrafanaBin(ocHome)

	// uvx 探测,跟 IDE 路径同款 — 缺 uv 时 nacos/jaeger/clickhouse 都启不来。
	if CfgUsesUvx(cfg) {
		if err := CheckUvxAvailable(); err != nil {
			fmt.Fprintf(os.Stderr, "[warn] openclaw install:\n%v\n", err)
		}
	}

	// kafka 走 binary 启动(tuannvm/kafka-mcp-server)。同 IDE 路径:PATH 没就自动下载到
	// ~/.tshoot/bin/,失败不阻塞 warn 给手动指引。详见 EnsureKafkaMCPInstalled 注释。
	kafkaBinPath := ""
	if CfgUsesKafkaMCP(cfg) {
		var err error
		kafkaBinPath, err = EnsureKafkaMCPInstalled(func(line string) {
			fmt.Fprintln(os.Stderr, line)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warn] openclaw install:\n%v\n", err)
		}
	}

	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:            cfg.MCPKeyPrefix(),
		PruneEmpty:         false, // 留全 schema,agent 自决
		KafkaMCPBinaryPath: kafkaBinPath,
	}, get)

	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		root["mcp"] = mcp
	}
	existing, _ := mcp["servers"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
		mcp["servers"] = existing
	}
	// 重灌:同名覆盖 + 按 agentID 前缀清死引用(env 缩容 / 切配置中心类型 / system.id 改名等
	// 场景),跟 IDE 路径同款。用户手加同前缀别名会被一起清,打 [info] 让用户感知。
	agentPrefix := cfg.MCPKeyPrefix() + "-"
	for k := range existing {
		if !strings.HasPrefix(k, agentPrefix) {
			continue
		}
		if _, want := servers[k]; want {
			continue
		}
		delete(existing, k)
		fmt.Fprintf(os.Stderr, "[info] openclaw:清掉死引用 mcp.servers.%s(本次 cfg 不再生成)\n", k)
	}
	maps.Copy(existing, servers)
	return nil
}
