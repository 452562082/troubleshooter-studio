package agent

func reconcileCodeGraphServer(existing, wanted map[string]any, agentID string) {
	key := "codegraph"
	if agentID != "" {
		key = agentID + "-" + key
	}
	if server, ok := wanted[key]; ok {
		existing[key] = server
		return
	}
	delete(existing, key)
}

func (b *mcpBuilder) buildCodeGraph(servers map[string]any) {
	if !b.cfg.CodeIntelligence.UsesCodeGraph() || b.opts.CodeGraphBinaryPath == "" {
		return
	}
	servers[b.keyFixed("codegraph")] = map[string]any{
		"command": b.opts.CodeGraphBinaryPath,
		"args":    []any{"serve", "--mcp"},
		"env": b.envBlock(map[string]any{
			"CODEGRAPH_TELEMETRY": "0",
			"DO_NOT_TRACK":        "1",
		}),
	}
}
