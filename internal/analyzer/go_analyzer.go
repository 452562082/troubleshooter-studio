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

	modPath := filepath.Join(repoPath, "go.mod")
	if fileExists(modPath) {
		if name := readGoModName(modPath); name != "" {
			ra.ServiceNames = append(ra.ServiceNames, name)
		}
	} else {
		ra.Warnings = append(ra.Warnings, "go.mod not found")
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
