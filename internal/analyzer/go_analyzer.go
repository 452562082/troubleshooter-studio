package analyzer

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type goAnalyzer struct {
	configCenter string
}

func NewGoAnalyzer(configCenter string) Analyzer { return &goAnalyzer{configCenter: configCenter} }

func (goAnalyzer) Stack() string { return "go" }

func (g goAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "go",
		RepoPath: repoPath,
		Verified: true,
	}

	// 单仓库:根目录 go.mod 存在 → 取 module 最后一段作 service_name。
	// monorepo(truss 这种):根目录没 go.mod,但子目录(commerce/ user/ ...)各自
	// 都是独立 go module,walk 一次收集所有 go.mod,模块名去重后作服务名列表。
	// 这跟 yaml 里手填 service_names 的直觉一致:一个仓库如果有多个可独立部署的服务,
	// 就应该列多个服务名。
	modPath := filepath.Join(repoPath, "go.mod")
	if fileExists(modPath) {
		if name := readGoModName(modPath); name != "" {
			ra.ServiceNames = append(ra.ServiceNames, name)
		}
	} else {
		// 扫子目录 go.mod(monorepo 场景)。walk 会自动跳 vendor/.git 等噪音目录。
		modFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
			return filepath.Base(rel) == "go.mod"
		})
		seen := map[string]bool{}
		for _, mf := range modFiles {
			if name := readGoModName(mf); name != "" && !seen[name] {
				seen[name] = true
				ra.ServiceNames = append(ra.ServiceNames, name)
			}
		}
		if len(ra.ServiceNames) == 0 {
			ra.Warnings = append(ra.Warnings, "go.mod not found (root or subdirs)")
		}
	}

	if g.configCenter == "" || g.configCenter == "none" {
		return ra, nil
	}

	yamlFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		low := strings.ToLower(rel)
		return strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml")
	})
	for _, f := range yamlFiles {
		rel, _ := filepath.Rel(repoPath, f)
		finding, err := ScanYAML(f, rel, g.configCenter)
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

func readGoModName(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			full := strings.TrimSpace(strings.TrimPrefix(line, "module"))
			segs := strings.Split(full, "/")
			return segs[len(segs)-1]
		}
	}
	return ""
}
