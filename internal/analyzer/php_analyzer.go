package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type phpAnalyzer struct {
	configCenter string
}

func NewPHPAnalyzer(configCenter string) Analyzer {
	return &phpAnalyzer{configCenter: configCenter}
}

func (phpAnalyzer) Stack() string { return "php" }

type composerJSON struct {
	Name string `json:"name"`
}

func (p phpAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "php",
		RepoPath: repoPath,
		Verified: true,
	}

	// 1) composer.json → name
	composerPath := filepath.Join(repoPath, "composer.json")
	if fileExists(composerPath) {
		if name := readComposerName(composerPath); name != "" {
			ra.ServiceNames = append(ra.ServiceNames, name)
		}
	} else {
		ra.Warnings = append(ra.Warnings, "composer.json not found")
	}

	if p.configCenter == "" || p.configCenter == "none" {
		return ra, nil
	}

	// 2) .env / .env.example / .env.* → 配置中心 + 数据库连接
	envFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return strings.HasPrefix(base, ".env")
	})
	for _, f := range envFiles {
		rel, _ := filepath.Rel(repoPath, f)
		finding, err := ScanDotEnv(f, rel, p.configCenter)
		if err != nil {
			ra.Warnings = append(ra.Warnings, "scan "+rel+": "+err.Error())
			continue
		}
		if finding != nil {
			ra.Findings = append(ra.Findings, *finding)
		}
	}

	// 3) YAML 配置（有些 PHP 项目也用 yaml）
	yamlFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		low := strings.ToLower(rel)
		return (strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml")) &&
			!strings.Contains(low, "vendor/")
	})
	for _, f := range yamlFiles {
		rel, _ := filepath.Rel(repoPath, f)
		finding, err := ScanYAML(f, rel, p.configCenter)
		if err != nil {
			ra.Warnings = append(ra.Warnings, "scan "+rel+": "+err.Error())
			continue
		}
		if finding != nil {
			if prof := extractEnvProfile(rel); prof != "" {
				finding.EnvProfile = prof
			}
			ra.Findings = append(ra.Findings, *finding)
		}
	}

	return ra, nil
}

// readComposerName 从 composer.json 的 "name" 字段取服务名
// 格式通常是 "vendor/package"，取 package 部分
func readComposerName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c composerJSON
	if err := json.Unmarshal(data, &c); err != nil {
		return ""
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ""
	}
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[idx+1:]
	}
	return name
}
