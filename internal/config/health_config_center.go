package config

import (
	"fmt"
	"strings"
)

// checkConfigCenter:配置中心检查。
//
// 同样的 shared-instance 取舍:典型上每 env 一套 nacos 集群,但小团队也可能共享一套。
// 所以只在 endpoints 完全为空时报(明显配漏),partial 不报(可能是共享场景)。
func checkConfigCenter(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	cc := c.Infrastructure.ConfigCenters
	if len(cc) == 0 {
		return nil
	}

	// 多源时 repo 必须显式 config_source
	if len(cc) > 1 {
		for _, r := range c.Repos {
			if r.ConfigSource == "" {
				out = append(out, HealthIssue{
					Severity: "warn",
					Category: "config_center",
					Field:    fmt.Sprintf("repos[%s].config_source", r.Name),
					Message:  fmt.Sprintf("多源场景下仓库 %q 没显式 config_source,会回落到第一个源 %q,可能不对", r.Name, cc[0].ID),
					Hint:     "可选源:" + joinConfigCenterIDs(cc),
				})
			}
		}
	}

	// 类型需要 endpoints 但完全空 → error
	for _, c1 := range cc {
		if c1.Type == "none" || c1.Type == "env-vars" || c1.Type == "kuboard" || c1.Type == "one2all" {
			continue
		}
		if len(c1.Endpoints) == 0 {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "config_center",
				Field:    fmt.Sprintf("infrastructure.config_centers[%s].endpoints", c1.ID),
				Message:  fmt.Sprintf("配置中心 %q (type=%s) endpoints 完全为空,无法连接", c1.ID, c1.Type),
			})
		}
	}
	return out
}

func joinConfigCenterIDs(cc []ConfigCenter) string {
	out := []string{}
	for _, c := range cc {
		out = append(out, c.ID)
	}
	return strings.Join(out, ", ")
}
