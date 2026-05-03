package config

import "fmt"

// checkEnvRepo:环境-仓库关系检查。
// "repo 在 env X 没填 env_branches" 既可能是"漏填了",也可能是"这个 repo 不在 env X 部署"。
// 没足够信号区分,所以只在**整张 env_branches 完全为空**时报(明显漏配),否则不打扰。
// service_names 空 → info 提示一下 analyzer 可以自动抽。
func checkEnvRepo(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	for _, r := range c.Repos {
		if len(r.EnvBranches) == 0 {
			out = append(out, HealthIssue{
				Severity: "warn",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].env_branches", r.Name),
				Message:  fmt.Sprintf("仓库 %q 完全没填 env_branches,routing 不知道该 checkout 哪个分支", r.Name),
				Hint:     "至少填一个 env→branch 映射(typical: dev: develop, prod: main)",
			})
		}
		// 只对"业务服务"角色(backend / gateway / middleware / admin)校验 service_names ——
		// frontend / mobile / common-lib / infra / docs 不需要 service_names(它们不挂配置中心 /
		// 不进 k8s deployment / 不进日志聚合),用户特意把这些角色填空是符合预期的,不该刷信息。
		if r.RequiresServiceNames() && len(r.ServiceNames) == 0 {
			out = append(out, HealthIssue{
				Severity: "info",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].service_names", r.Name),
				Message:  fmt.Sprintf("仓库 %q 没填 service_names,routing 用 repo.name 当服务名,跟实际可能不一致", r.Name),
				Hint:     "跑一遍仓库分析让 analyzer 自动抽 service_names,或手填进 yaml",
			})
		}
	}
	return out
}
