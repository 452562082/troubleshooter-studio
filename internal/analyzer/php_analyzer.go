package analyzer

import (
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

func (p phpAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "php",
		RepoPath: repoPath,
		Verified: true,
	}

	// 服务名直接用目录 basename(api-truss / order-service 这种)。
	// 不从 composer.json "name" 字段读 —— 那个字段是 Packagist 包名(vendor/package),
	// PHP 项目多用框架 skeleton 模板(hyperf/hyperf-skeleton、laravel/laravel、
	// symfony/skeleton),很多开发者不改就直接用,读出来会把"框架 skeleton 名"
	// 当成服务名,误导 config-map / 部署名匹配。Go 语言 module path 最后一段通常
	// 等于服务名是 Go 社区约定,PHP 没这个习惯。
	composerPath := filepath.Join(repoPath, "composer.json")
	if fileExists(composerPath) {
		ra.ServiceNames = append(ra.ServiceNames, filepath.Base(repoPath))
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
