package config

import "fmt"

// checkEnvRepo:环境-仓库关系检查。
// "repo 在 env X 没填 env_branches" 既可能是"漏填了",也可能是"这个 repo 不在 env X 部署"。
// 没足够信号区分,所以只在**整张 env_branches 完全为空**时报(明显漏配),否则不打扰。
// service_names 空 → info 提示一下 analyzer 可以自动抽。
func checkEnvRepo(c *SystemConfig) []HealthIssue {
	var out []HealthIssue
	// parent_repo 校验:必须引用存在的 repos[].name + 不能自指 + 不能成环
	repoNames := make(map[string]bool, len(c.Repos))
	for _, r := range c.Repos {
		repoNames[r.Name] = true
	}
	for _, r := range c.Repos {
		if r.ParentRepo == "" {
			continue
		}
		if r.ParentRepo == r.Name {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].parent_repo", r.Name),
				Message:  fmt.Sprintf("仓库 %q 的 parent_repo 自指 %q,不允许", r.Name, r.ParentRepo),
				Hint:     "parent_repo 应该指向另一个 umbrella(主仓)的 name,不是自己",
			})
			continue
		}
		if !repoNames[r.ParentRepo] {
			out = append(out, HealthIssue{
				Severity: "error",
				Category: "repo",
				Field:    fmt.Sprintf("repos[%s].parent_repo", r.Name),
				Message:  fmt.Sprintf("仓库 %q 的 parent_repo=%q 在 repos[] 里找不到", r.Name, r.ParentRepo),
				Hint:     "把 umbrella 仓库也加进 repos[],或清掉 parent_repo 字段",
			})
		}
	}
	// 环检测(simple DFS,深度 > 仓库总数即视为有环)
	parentOf := make(map[string]string, len(c.Repos))
	for _, r := range c.Repos {
		if r.ParentRepo != "" && repoNames[r.ParentRepo] {
			parentOf[r.Name] = r.ParentRepo
		}
	}
	for name := range parentOf {
		visited := make(map[string]bool)
		cur := name
		for hop := 0; hop <= len(c.Repos); hop++ {
			if visited[cur] {
				out = append(out, HealthIssue{
					Severity: "error",
					Category: "repo",
					Field:    fmt.Sprintf("repos[%s].parent_repo", name),
					Message:  fmt.Sprintf("仓库 %q 的 parent_repo 链路成环", name),
					Hint:     "umbrella → 子模块只能单向,不能两个仓库互指",
				})
				break
			}
			visited[cur] = true
			next, ok := parentOf[cur]
			if !ok {
				break
			}
			cur = next
		}
	}
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
		// frontend 的 service_names 是可选运行时身份，不属于配置服务必填项；mobile /
		// common-lib / infra / docs 同样不要求配置服务名，所以这里只检查 RequiresServiceNames。
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
