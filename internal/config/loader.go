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
// 用途：桌面 app 的 Wails binding、API handler、内存管线都不想为每次校验写临时文件。
func LoadFromBytes(data []byte) (*SystemConfig, error) {
	var cfg SystemConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	// 老 yaml 把 target 写成 "standalone" —— 功能已重命名为 "embedded"(桌面端内嵌对话),
	// 这里做一次 alias 归一,不强制用户改 yaml。deduplicate 避免 "standalone + embedded"
	// 同时出现导致重复 gen。
	aliasStandaloneToEmbedded(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// aliasStandaloneToEmbedded 把 generation.targets 里的老 target 名 "standalone" 替换为
// 新名 "embedded",保留其它目标顺序不变;已有 "embedded" 不重复加。
// Meta 里 schema_version 这类字段不处理 —— 只是 targets 层面做了一次重命名。
func aliasStandaloneToEmbedded(c *SystemConfig) {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Generation.Targets))
	for _, t := range c.Generation.Targets {
		if t == "standalone" {
			t = "embedded"
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	c.Generation.Targets = out
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
	if c.Agent.WorkspaceName == "" {
		return fmt.Errorf("agent.workspace_name required")
	}
	if c.Agent.Model == "" {
		return fmt.Errorf("agent.model required")
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
		if r.Role == "" {
			return fmt.Errorf("repos[%s].role required", r.Name)
		}
		if r.Stack == "" {
			return fmt.Errorf("repos[%s].stack required", r.Name)
		}
		for envID := range r.EnvBranches {
			if !envIDs[envID] {
				return fmt.Errorf("repos[%s].env_branches references unknown env: %s", r.Name, envID)
			}
		}
	}

	ccType := c.Infrastructure.ConfigCenter.Type
	validCCTypes := map[string]bool{
		"": true, "none": true,
		"nacos": true, "apollo": true, "consul": true,
		"env-vars": true, "kubernetes": true,
	}
	if !validCCTypes[ccType] {
		return fmt.Errorf("infrastructure.config_center.type=%q not supported (valid: nacos/apollo/consul/env-vars/kubernetes/none)", ccType)
	}
	if ccType != "" && ccType != "none" && ccType != "env-vars" && ccType != "kubernetes" {
		for i, ep := range c.Infrastructure.ConfigCenter.Endpoints {
			if !envIDs[ep.Env] {
				return fmt.Errorf("infrastructure.config_center.endpoints[%d].env unknown: %s", i, ep.Env)
			}
		}
	}

	// 注:"standalone" 已重命名为 "embedded",aliasStandaloneToEmbedded 在 LoadFromBytes
	// 里把老值替成新值后才走到 Validate。这里只接受新名。
	validTargets := map[string]bool{"openclaw": true, "claude-code": true, "cursor": true, "embedded": true}
	targets := c.Generation.ResolvedTargets()
	for _, t := range targets {
		if !validTargets[t] {
			return fmt.Errorf("generation.targets: %q not supported (valid: openclaw, claude-code, cursor, embedded)", t)
		}
	}

	if c.Meta.SchemaVersion == "" {
		return fmt.Errorf("meta.schema_version required")
	}
	return nil
}

func applyDefaults(c *SystemConfig) {
	if c.Generation.OutputDir == "" {
		c.Generation.OutputDir = "./dist"
	}
	if c.Generation.MappingReviewMode == "" {
		c.Generation.MappingReviewMode = "strict"
	}
	for i := range c.Repos {
		if c.Repos[i].Analysis.ShallowDepth == 0 {
			c.Repos[i].Analysis.ShallowDepth = 50
		}
	}
}
