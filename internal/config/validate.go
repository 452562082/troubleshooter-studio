// validate.go —— SystemConfig 强校验:必填字段缺、引用未知 env、类型枚举非法等;
// 一旦报错 generator 直接拒生。跟 health.go 的 HealthCheck(语义提示)分工:
// validate 失败 yaml 跑不动,health 失败只是提示用户。
package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Validate(c *SystemConfig) error {
	if c.System.ID == "" {
		return fmt.Errorf("system.id required")
	}
	if !idPattern.MatchString(c.System.ID) {
		return fmt.Errorf("system.id must match [a-z0-9][a-z0-9-]*, got %q", c.System.ID)
	}
	if c.System.Name == "" {
		return fmt.Errorf("system.name required")
	}
	if c.Agent.Name == "" {
		return fmt.Errorf("agent.name required")
	}
	// workspace_name / model 仅 openclaw target 消费;其它 target(claude-code / cursor)
	// 不读这两个字段,所以只在勾了 openclaw 时才强制必填。
	hasOpenclaw := false
	for _, t := range c.Generation.ResolvedTargets() {
		if t == "openclaw" {
			hasOpenclaw = true
			break
		}
	}
	if hasOpenclaw {
		// workspace_name 可空,有 system.id / agent.id 就能 ResolveWorkspaceName() 出来;
		// 老 yaml 里显式写了 workspace_name 的也兼容。完全空才拦。
		if c.ResolveWorkspaceName() == "" {
			return fmt.Errorf("openclaw target 需要 system.id / agent.id / agent.workspace_name 至少一个非空")
		}
		if c.Agent.Model == "" {
			return fmt.Errorf("agent.model required (openclaw target)")
		}
	}

	if len(c.Environments) == 0 {
		return fmt.Errorf("environments must have at least 1 entry")
	}
	envIDs := map[string]bool{}
	for i, env := range c.Environments {
		if env.ID == "" {
			return fmt.Errorf("environments[%d].id required", i)
		}
		if envIDs[env.ID] {
			return fmt.Errorf("duplicate environment id: %s", env.ID)
		}
		envIDs[env.ID] = true
	}

	if c.CodeIntelligence.Enabled && c.CodeIntelligence.Provider == "" {
		return fmt.Errorf("code_intelligence.provider required when enabled")
	}
	if p := c.CodeIntelligence.Provider; p != "" && p != CodeIntelligenceProviderCodeGraph {
		return fmt.Errorf("code_intelligence.provider=%q invalid (valid: codegraph)", p)
	}

	// ── 配置中心:多源 schema ──
	// kuboard:走 Kuboard HTTP API 读 ConfigMap(用户没 kubeconfig、只能拿到
	// Kuboard URL+账密的常见场景)。原 kubernetes 类型(走 kubectl + ~/.kube/config)
	// 因为大多数普通研发拿不到 kubeconfig 而下线 —— 老 yaml 触发 validate 错。
	validCCTypes := map[string]bool{
		"none":  true,
		"nacos": true, "apollo": true, "consul": true,
		"env-vars": true, "kuboard": true, "one2all": true,
	}
	sourceIDs := map[string]bool{}
	for i, cc := range c.Infrastructure.ConfigCenters {
		if cc.ID == "" {
			return fmt.Errorf("infrastructure.config_centers[%d].id required (多源场景必填,单源迁移会自动设 default)", i)
		}
		if !idPattern.MatchString(cc.ID) {
			return fmt.Errorf("infrastructure.config_centers[%d].id must match [a-z0-9][a-z0-9-]*, got %q", i, cc.ID)
		}
		if sourceIDs[cc.ID] {
			return fmt.Errorf("duplicate config_center id: %s", cc.ID)
		}
		sourceIDs[cc.ID] = true
		if !validCCTypes[cc.Type] {
			hint := ""
			if cc.Type == "kubernetes" {
				hint = " (注:kubernetes 类型已下线;走 kubectl+kubeconfig 的场景门槛太高,改用 kuboard 类型用 Kuboard URL+账密读 ConfigMap;或 one2all 类型走 one2all-remote MCP)"
			}
			return fmt.Errorf("infrastructure.config_centers[%s].type=%q not supported (valid: nacos/apollo/consul/env-vars/kuboard/one2all/none)%s", cc.ID, cc.Type, hint)
		}
		if cc.Type != "none" && cc.Type != "env-vars" && cc.Type != "kuboard" && cc.Type != "one2all" {
			for j, ep := range cc.Endpoints {
				if !envIDs[ep.Env] {
					return fmt.Errorf("infrastructure.config_centers[%s].endpoints[%d].env unknown: %s", cc.ID, j, ep.Env)
				}
			}
		}
	}

	// ── 仓库 ──
	repoNames := map[string]bool{}
	for i, r := range c.Repos {
		if r.Name == "" {
			return fmt.Errorf("repos[%d].name required", i)
		}
		if repoNames[r.Name] {
			return fmt.Errorf("duplicate repo name: %s", r.Name)
		}
		repoNames[r.Name] = true
		if r.URL == "" {
			return fmt.Errorf("repos[%s].url required", r.Name)
		}
		if r.Stack == "" {
			return fmt.Errorf("repos[%s].stack required", r.Name)
		}
		if r.Role != "" {
			validRoles := map[string]bool{
				RoleBackend: true, RoleFrontend: true, RoleGateway: true,
				RoleMiddleware: true, RoleCommonLib: true, RoleMobile: true,
				RoleAdmin: true, RoleInfra: true, RoleDocs: true,
			}
			if !validRoles[r.Role] {
				return fmt.Errorf("repos[%s].role=%q invalid (valid: backend/frontend/gateway/middleware/common-lib/mobile/admin/infra/docs)", r.Name, r.Role)
			}
		}
		if r.SubPath != "" {
			if strings.HasPrefix(r.SubPath, "/") {
				return fmt.Errorf("repos[%s].sub_path=%q must be relative (no leading slash)", r.Name, r.SubPath)
			}
			if strings.Contains(r.SubPath, "..") {
				return fmt.Errorf("repos[%s].sub_path=%q must not contain '..' (no parent traversal)", r.Name, r.SubPath)
			}
		}
		for envID := range r.EnvBranches {
			if !envIDs[envID] {
				return fmt.Errorf("repos[%s].env_branches references unknown env: %s", r.Name, envID)
			}
		}
		// config_source 必须引用一个真实源(除非整个系统就没声明配置中心)
		if r.ConfigSource != "" && !sourceIDs[r.ConfigSource] {
			return fmt.Errorf("repos[%s].config_source=%q references unknown config_centers[].id (有效 id: %v)", r.Name, r.ConfigSource, sortedKeys(sourceIDs))
		}
	}

	for i := range c.Environments {
		if err := validateDeploymentVerification(c.Environments[i], repoNames); err != nil {
			return fmt.Errorf("environments[%s].deployment_verification: %w", c.Environments[i].ID, err)
		}
	}

	if err := validateServiceTopology(c); err != nil {
		return err
	}

	validTargets := map[string]bool{"openclaw": true, "claude-code": true, "cursor": true, "codex": true}
	targets := c.Generation.ResolvedTargets()
	for _, t := range targets {
		if !validTargets[t] {
			return fmt.Errorf("generation.targets: %q not supported (valid: openclaw, claude-code, cursor)", t)
		}
	}

	if c.Meta.SchemaVersion == "" {
		return fmt.Errorf("meta.schema_version required")
	}
	return nil
}

func validateDeploymentVerification(env Environment, repoNames map[string]bool) error {
	cfg := env.DeploymentVerification
	switch cfg.EffectiveProvider() {
	case DeploymentVerificationProviderManual:
		if !cfg.HTTP.IsZero() || !cfg.K8s.IsZero() {
			return fmt.Errorf("manual provider must not include http or k8s blocks")
		}
	case DeploymentVerificationProviderHTTP:
		if !cfg.K8s.IsZero() {
			return fmt.Errorf("http provider must not include k8s block")
		}
		if strings.TrimSpace(cfg.HTTP.URL) == "" {
			return fmt.Errorf("http.url required")
		}
		if strings.TrimSpace(cfg.HTTP.JSONPointer) == "" {
			return fmt.Errorf("http.json_pointer required")
		}
		if !strings.HasPrefix(strings.TrimSpace(cfg.HTTP.JSONPointer), "/") {
			return fmt.Errorf("http.json_pointer must be an RFC 6901 pointer starting with '/'")
		}
	case DeploymentVerificationProviderK8s:
		if !cfg.HTTP.IsZero() {
			return fmt.Errorf("k8s provider must not include http block")
		}
		if strings.TrimSpace(cfg.K8s.Cluster) == "" {
			return fmt.Errorf("k8s.cluster required")
		}
		if strings.TrimSpace(cfg.K8s.Namespace) == "" {
			return fmt.Errorf("k8s.namespace required")
		}
		if len(cfg.K8s.DeploymentsByRepo) == 0 {
			return fmt.Errorf("k8s.deployments_by_repo required")
		}
		if strings.TrimSpace(cfg.K8s.CommitAnnotation) == "" && strings.TrimSpace(cfg.K8s.ImageLabel) == "" {
			return fmt.Errorf("k8s.commit_annotation or image_label required")
		}
		if strings.TrimSpace(cfg.K8s.CommitAnnotation) != "" && strings.TrimSpace(cfg.K8s.ImageLabel) != "" {
			return fmt.Errorf("k8s.commit_annotation and image_label are mutually exclusive")
		}
		for repo, deployment := range cfg.K8s.DeploymentsByRepo {
			if !repoNames[repo] {
				return fmt.Errorf("k8s.deployments_by_repo references unknown repo %q", repo)
			}
			if strings.TrimSpace(deployment) == "" {
				return fmt.Errorf("k8s.deployments_by_repo[%s] required", repo)
			}
		}
	default:
		return fmt.Errorf("provider=%q invalid (valid: manual/http/k8s)", cfg.Provider)
	}
	return nil
}

func validateServiceTopology(c *SystemConfig) error {
	serviceNames := make(map[string]bool)
	for _, repo := range c.Repos {
		if !repo.IsServiceNode() {
			continue
		}
		if len(repo.ServiceNames) == 0 {
			serviceNames[repo.Name] = true
			continue
		}
		for _, serviceName := range repo.ServiceNames {
			if serviceName != "" {
				serviceNames[serviceName] = true
			}
		}
	}

	semanticKeys := make(map[string]bool)
	for i, override := range c.ServiceTopology.Overrides {
		field := fmt.Sprintf("service_topology.overrides[%d]", i)
		if override.Action != "confirm" && override.Action != "reject" && override.Action != "add" {
			return fmt.Errorf("%s.action=%q invalid (valid: confirm/reject/add)", field, override.Action)
		}
		if !serviceNames[override.FromService] {
			return fmt.Errorf("%s.from_service=%q is not an effective service name", field, override.FromService)
		}
		if !serviceNames[override.ToService] {
			return fmt.Errorf("%s.to_service=%q is not an effective service name", field, override.ToService)
		}

		override.Protocol = strings.ToLower(override.Protocol)
		override.Method = strings.ToUpper(override.Method)
		override.Path = topology.NormalizePath(override.Path)
		switch override.Protocol {
		case "http":
			if override.Method == "" || override.Path == "" || override.RPCMethod != "" {
				return fmt.Errorf("%s: http override requires method and path only", field)
			}
			if !strings.HasPrefix(override.Path, "/") {
				return fmt.Errorf("%s.path=%q must start with '/'", field, override.Path)
			}
		case "grpc":
			if override.RPCMethod == "" || override.Method != "" || override.Path != "" {
				return fmt.Errorf("%s: grpc override requires rpc_method only", field)
			}
		default:
			return fmt.Errorf("%s.protocol=%q invalid (valid: http/grpc)", field, override.Protocol)
		}

		key := override.SemanticKey()
		if semanticKeys[key] {
			return fmt.Errorf("%s has duplicate semantic key", field)
		}
		semanticKeys[key] = true
		c.ServiceTopology.Overrides[i] = override
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 不强制 sort,error 信息里展示哪些 id 即可,顺序无所谓
	return out
}
