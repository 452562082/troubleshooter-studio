// loader.go —— 入口 + applyDefaults。子文件:
//
//	migrate.go   migrateObservabilityEndpoints / migrateLegacyConfigCenter(老 schema → 新)
//	validate.go  Validate(强校验,失败拒生)+ idPattern + sortedKeys
//	health.go    HealthCheck(语义提示,警告级)拆 5 个 health_*.go
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
	migrateObservabilityEndpoints(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(c *SystemConfig) {
	for i := range c.Repos {
		if c.Repos[i].Analysis.ShallowDepth == 0 {
			c.Repos[i].Analysis.ShallowDepth = 50
		}
	}
}
