package config

type System struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type Agent struct {
	// ID 是机器人在各 AI 平台里的稳定标识(OpenClaw `agents.list[*].id` /
	// Claude Code / Cursor subagent 名)。空时由 ResolveID 推导成 "<system.id>-troubleshooter"
	// 兼容老 yaml,保持产物文件名(`<id>-creds.json` / agents.list 等)不破。
	ID            string `yaml:"id,omitempty"`
	Name          string `yaml:"name"`
	WorkspaceName string `yaml:"workspace_name"`
	// Model 是"默认"模型 id(provider/modelID 格式);
	// 只有 openclaw 和 embedded 两个 target 实际消费模型:
	//   - openclaw:写进 install.sh 的 MODEL 默认值,gateway 读它路由
	//   - embedded:Studio 内嵌对话直连这个 provider / model
	// claude-code / cursor 不消费 —— 用户在这两家客户端自己挑模型,本字段仅作文档
	Model string `yaml:"model"`
	// TargetModels 为 target 级别的模型覆盖(可选)。同时勾 openclaw + embedded
	// 但想给它俩用不同 provider / 不同模型时用:
	//   target_models:
	//     openclaw: anthropic/claude-sonnet-4-6
	//     embedded: deepseek/deepseek-chat
	// 没指定时该 target 回落到上面的 agent.model。
	// claude-code / cursor 写在这里也会被忽略(那俩 target 不消费模型)。
	TargetModels map[string]string `yaml:"target_models,omitempty"`
}

// ResolveID 返回 agent 在各 AI 平台的最终 ID:
//   - 优先 yaml 显式指定的 agent.id
//   - 空时回落到 agent.workspace_name(兼容老 yaml:那时候 workspace_name 同时承担"目录名"
//     和"AI 平台标识"两职,典型值 shop-bot)
//   - 再空回落到 "<system.id>-troubleshooter"(全新 wizard 默认命名)
//
// 这条链让"删了 workspace_name 字段、改用 agent.id"的迁移对老 yaml 透明:
// shop-troubleshooter.yaml(workspace_name=shop-bot, 无 agent.id)→ ID 仍 = shop-bot,
// 不会突然变成 shop-troubleshooter 让旧的 ~/.claude/agents/shop-bot.md 成孤儿。
func (s *SystemConfig) ResolveID() string {
	if id := s.Agent.ID; id != "" {
		return id
	}
	if w := s.Agent.WorkspaceName; w != "" {
		return w
	}
	return s.System.ID + "-troubleshooter"
}

// ResolveWorkspaceName 返回 OpenClaw workspace 目录名(~/.openclaw/workspace/<这里>):
//   - 优先 yaml 显式指定的 agent.workspace_name(老 yaml 兼容)
//   - 空时跟 ResolveID() 共享同一标识(新 wizard 不再单独 emit workspace_name)
func (s *SystemConfig) ResolveWorkspaceName() string {
	if w := s.Agent.WorkspaceName; w != "" {
		return w
	}
	return s.ResolveID()
}

// MCPKeyPrefix 返回 MCP server key 的前缀。
//
// 跟 ResolveID() 区别:ResolveID() 返回完整 agent 标识(如 "<system.id>-troubleshooter"),
// 用于 agent.md 文件名 / skills 目录名 / openclaw agents.list[i].id 这些"用户可见标识符";
// MCPKeyPrefix() 返回**短**标识(优先 system.id),用于 mcpServers 的 key 前缀,避免
// "agent_id + server_type + env" 拼起来超过 IDE 60 字符 tool name 限制。
//
// 例:system.id=truss → MCP key 形如 "truss-grafana-mcp-server-dev"(20 字),
// 留 40 字给 tool 名;若用 ResolveID() = "truss-troubleshooter" → 33 字 prefix,
// grafana 的 get_dashboard_panel_queries(27 字)拼起来 60+ 超限。
//
// system.id 为空(理论上不该出现,Loader 强制必填)时回退 ResolveID,保证非空。
func (s *SystemConfig) MCPKeyPrefix() string {
	if id := s.System.ID; id != "" {
		return id
	}
	return s.ResolveID()
}

// ModelForTarget 给 target-aware 消费点(install.sh 模板 / llmchat)提供的便捷访问:
// 优先 target_models[target],回落到 agent.model。target 为空串时直接返回 agent.model。
func (a Agent) ModelForTarget(target string) string {
	if target != "" {
		if m, ok := a.TargetModels[target]; ok && m != "" {
			return m
		}
	}
	return a.Model
}

type Environment struct {
	ID                     string                       `yaml:"id"`
	Aliases                []string                     `yaml:"aliases"`
	APIDomain              string                       `yaml:"api_domain"`
	WebDomain              string                       `yaml:"web_domain"`
	IsProd                 bool                         `yaml:"is_prod"`
	DeploymentVerification DeploymentVerificationConfig `yaml:"deployment_verification,omitempty"`
}

type Generation struct {
	// TargetHost 是 v0.x 的单目标遗留字段,新 yaml 用 Targets 数组替代。
	// 仅保留读路径(ResolvedTargets() 兜底);新生成的 yaml 不再写出这个字段。
	TargetHost      string   `yaml:"target_host,omitempty"`
	Targets         []string `yaml:"targets"` // 多目标：openclaw / claude-code / ...
	SkillsWhitelist []string `yaml:"skills_whitelist"`
}

// ResolvedTargets 返回最终目标列表（兼容 target_host 单值和 targets 数组）
func (g Generation) ResolvedTargets() []string {
	if len(g.Targets) > 0 {
		return g.Targets
	}
	if g.TargetHost != "" {
		return []string{g.TargetHost}
	}
	return []string{"openclaw"}
}

type TshootTemplateRef struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

type Meta struct {
	SchemaVersion     string            `yaml:"schema_version"`
	TshootTemplateRef TshootTemplateRef `yaml:"tshoot_template_ref"`
}

type SystemConfig struct {
	System           System           `yaml:"system"`
	Agent            Agent            `yaml:"agent"`
	Environments     []Environment    `yaml:"environments"`
	Repos            []Repo           `yaml:"repos"`
	CodeIntelligence CodeIntelligence `yaml:"code_intelligence,omitempty"`
	ServiceTopology  ServiceTopology  `yaml:"service_topology,omitempty"`
	Infrastructure   Infrastructure   `yaml:"infrastructure"`
	Generation       Generation       `yaml:"generation"`
	Meta             Meta             `yaml:"meta"`
}
