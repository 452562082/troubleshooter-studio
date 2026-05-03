package config

import (
	"fmt"
	"strings"
)

// checkGeneration:生成可行性检查 —— 未知 skill / 已 whitelist 但 ds 没启用 / preserve 越狱 / model 字段不消费等。
func checkGeneration(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	targets := c.Generation.ResolvedTargets()

	// 未知 skill
	for _, s := range c.Generation.SkillsWhitelist {
		if !knownSkills[s] {
			out = append(out, HealthIssue{
				Severity: "warn",
				Category: "generation",
				Field:    "generation.skills_whitelist",
				Message:  fmt.Sprintf("skills_whitelist 含未知 skill 名 %q,生成时会被忽略", s),
				Hint:     "已知 skill:" + strings.Join(sortedSkillNames(), ", "),
			})
		}
	}

	// 已 whitelist 的 *-runtime-query 但对应 data_store 没启用 → 会被静默跳过
	dsCheck := func(skill, dsType string) {
		for _, w := range c.Generation.SkillsWhitelist {
			if w == skill && !dataStoreEnabled(c, dsType) {
				out = append(out, HealthIssue{
					Severity: "info",
					Category: "generation",
					Field:    "generation.skills_whitelist",
					Message:  fmt.Sprintf("%s 在白名单但 data_stores.%s 未启用,该 skill 会被跳过", skill, dsType),
				})
			}
		}
	}
	dsCheck("redis-runtime-query", "redis")
	dsCheck("mysql-runtime-query", "mysql")
	dsCheck("mongodb-runtime-query", "mongodb")
	dsCheck("es-runtime-query", "elasticsearch")
	dsCheck("postgresql-runtime-query", "postgresql")
	dsCheck("clickhouse-runtime-query", "clickhouse")

	// preserve_on_regenerate 含越狱路径
	for _, p := range c.Generation.PreserveOnRegenerate {
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "generation",
				Field:    "generation.preserve_on_regenerate",
				Message:  fmt.Sprintf("preserve_on_regenerate 项 %q 含绝对路径或 .. 跳出 workspace,不安全", p),
			})
		}
	}

	// targets 不含 openclaw 但配了 agent.model:模型字段对 claude-code/cursor 不消费
	hasOpenclaw := false
	hasOther := false
	for _, t := range targets {
		switch t {
		case "openclaw":
			hasOpenclaw = true
		case "claude-code", "cursor":
			hasOther = true
		}
	}
	if !hasOpenclaw && hasOther && c.Agent.Model != "" {
		out = append(out, HealthIssue{
			Severity: "info",
			Category: "generation",
			Field:    "agent.model",
			Message:  "agent.model 仅 openclaw 消费,目前 targets 不含 openclaw,该字段不生效",
		})
	}

	return out
}

// dataStoreEnabled 给 checkGeneration 用:data_store 类型 t 是否被启用。
func dataStoreEnabled(c *SystemConfig, t string) bool {
	for _, ds := range c.Infrastructure.DataStores {
		if ds.Type == t && ds.Enabled {
			return true
		}
	}
	return false
}
