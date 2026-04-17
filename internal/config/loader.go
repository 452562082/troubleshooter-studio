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

	var cfg SystemConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
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

	validTargets := map[string]bool{"openclaw": true, "claude-code": true, "cursor": true, "standalone": true}
	targets := c.Generation.ResolvedTargets()
	for _, t := range targets {
		if !validTargets[t] {
			return fmt.Errorf("generation.targets: %q not supported (valid: openclaw, claude-code, cursor, standalone)", t)
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
