package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Load(path string) (*SystemConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes 从内存里的 yaml 内容解析 + 校验 + 套默认值。
// 用途:桌面 app 的 Wails binding、API handler、内存管线都不想为每次校验写临时文件。
func LoadFromBytes(data []byte) (*SystemConfig, error) {
	var cfg SystemConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	// migrate 必须在 validate 之前 —— 老 yaml 单源 schema 走完 migrate 才符合新 schema。
	migrateLegacyConfigCenter(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// migrateLegacyConfigCenter 把老 schema 的 infrastructure.config_center(单数)
// 迁到新 schema 的 infrastructure.config_centers(数组)。
//
// 规则:
//  1. 已有 ConfigCenters[]:以新字段为准,把 ConfigCenter 同步成 ConfigCenters[0] 副本
//     (供老 template / 老代码做"主源"读)。
//  2. 只有 ConfigCenter.Type:把它包成单元素 ConfigCenters,id 默认 "default";
//     ConfigCenter 字段保留作为同份数据的副本。
//  3. 都为空:跳过。
//
// 注意:刻意 NOT 清掉 ConfigCenter —— 大量 *.tmpl 模板和老代码读 .Infrastructure.ConfigCenter,
// 维持成"主源(ConfigCenters[0])镜像"让它们不破。多源感知逻辑(prompts/MCP/install)
// 必须显式遍历 ConfigCenters,这层在编码层面已分离开,不会回退。
//
// 同时给每个 repo 兜底 ConfigSource:
//   - 已写就保留(新 yaml 直接用)
//   - 空时绑到 ConfigCenters[0].id(老 yaml 隐式只用一个源)
//   - ConfigCenters 也空时 ConfigSource 保持空(无配置中心场景)
func migrateLegacyConfigCenter(cfg *SystemConfig) {
	if len(cfg.Infrastructure.ConfigCenters) == 0 && cfg.Infrastructure.ConfigCenter.Type != "" {
		legacy := cfg.Infrastructure.ConfigCenter
		if legacy.ID == "" {
			legacy.ID = "default"
		}
		cfg.Infrastructure.ConfigCenters = []ConfigCenter{legacy}
	}
	// 保持 ConfigCenter 作为 ConfigCenters[0] 的镜像(老 template/代码兼容点)
	if len(cfg.Infrastructure.ConfigCenters) > 0 {
		cfg.Infrastructure.ConfigCenter = cfg.Infrastructure.ConfigCenters[0]
	} else {
		cfg.Infrastructure.ConfigCenter = ConfigCenter{}
	}

	if len(cfg.Infrastructure.ConfigCenters) == 0 {
		return
	}
	defaultSource := cfg.Infrastructure.ConfigCenters[0].ID
	for i := range cfg.Repos {
		if cfg.Repos[i].ConfigSource == "" {
			cfg.Repos[i].ConfigSource = defaultSource
		}
	}
}

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

	// ── 配置中心:多源 schema ──
	// kuboard:走 Kuboard HTTP API 读 ConfigMap(用户没 kubeconfig、只能拿到
	// Kuboard URL+账密的常见场景)。原 kubernetes 类型(走 kubectl + ~/.kube/config)
	// 因为大多数普通研发拿不到 kubeconfig 而下线 —— 老 yaml 触发 validate 错。
	validCCTypes := map[string]bool{
		"none": true,
		"nacos": true, "apollo": true, "consul": true,
		"env-vars": true, "kuboard": true,
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
				hint = " (注:kubernetes 类型已下线;走 kubectl+kubeconfig 的场景门槛太高,改用 kuboard 类型用 Kuboard URL+账密读 ConfigMap)"
			}
			return fmt.Errorf("infrastructure.config_centers[%s].type=%q not supported (valid: nacos/apollo/consul/env-vars/kuboard/none)%s", cc.ID, cc.Type, hint)
		}
		if cc.Type != "none" && cc.Type != "env-vars" && cc.Type != "kuboard" {
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

	validTargets := map[string]bool{"openclaw": true, "claude-code": true, "cursor": true}
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// 不强制 sort,error 信息里展示哪些 id 即可,顺序无所谓
	return out
}

func applyDefaults(c *SystemConfig) {
	for i := range c.Repos {
		if c.Repos[i].Analysis.ShallowDepth == 0 {
			c.Repos[i].Analysis.ShallowDepth = 50
		}
	}
}
