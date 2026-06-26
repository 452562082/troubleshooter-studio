// install_native_mcp_one2all.go —— one2all-remote MCP server(streamable-http)
//
// one2all-remote 是单一 HTTP MCP server,提供 K8s 运行时查询 + ConfigMap/Secret 读取
// + CI/CD + 文档 RAG + 任务/里程碑等 40 个工具。所有 env 通过 cluster_id 参数区分,
// 跟 nacos/grafana 这类 per-env stdio MCP 不同。
//
// 注册为 keyFixed("one2all"),单一 streamable-http 条目。凭据 ONE2ALL_MCP_URL +
// ONE2ALL_TOKEN 从 install_prompts 收集。
//
// 替代关系:
//   - kuboard config source(type:"kuboard")→ type:"one2all",读 ConfigMap/Secret 走 MCP 工具
//   - k8s_runtime kuboard provider → provider:"one2all",pod/deployment/event/log 走 MCP 工具
//   - 新增:CI/CD pipeline、文档 RAG、任务/里程碑、知识图谱等 kuboard 不具备的能力
//
// 跳过条件:
//   - 没有任何 config_center type=="one2all"
//   - PruneEmpty(IDE)且 ONE2ALL_MCP_URL 或 ONE2ALL_TOKEN 缺 → 跳过
package agent

func (b *mcpBuilder) buildOne2All(servers map[string]any) {
	if !b.cfg.UsesOne2All() {
		return
	}

	mcpURL := b.get("ONE2ALL_MCP_URL")
	token := b.get("ONE2ALL_TOKEN")
	if b.opts.PruneEmpty && (mcpURL == "" || token == "") {
		return
	}

	servers[b.keyFixed("one2all")] = map[string]any{
		"type": "streamable-http",
		"url":  mcpURL,
		"headers": map[string]string{
			"Authorization": "Bearer " + token,
			"Accept":        "application/json, text/event-stream",
		},
	}
}
