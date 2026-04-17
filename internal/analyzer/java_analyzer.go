package analyzer

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type javaAnalyzer struct {
	configCenter string
}

func NewJavaAnalyzer(configCenter string) Analyzer {
	return &javaAnalyzer{configCenter: configCenter}
}

func (javaAnalyzer) Stack() string { return "java" }

type pomProject struct {
	XMLName    xml.Name `xml:"project"`
	ArtifactID string   `xml:"artifactId"`
}

var profileRegex = regexp.MustCompile(`[-_](dev|test|staging|stg|prod|pre|uat)\.`)

func extractEnvProfile(relPath string) string {
	base := strings.ToLower(filepath.Base(relPath))
	if m := profileRegex.FindStringSubmatch(base); len(m) == 2 {
		return m[1]
	}
	return ""
}

func (j javaAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "java",
		RepoPath: repoPath,
		Verified: true,
	}

	names := map[string]bool{}
	pomFiles, _ := walkFiles(repoPath, nil, func(rel string) bool {
		return filepath.Base(rel) == "pom.xml"
	})
	for _, p := range pomFiles {
		if id := readPomArtifactID(p); id != "" {
			names[id] = true
		}
	}
	for n := range names {
		ra.ServiceNames = append(ra.ServiceNames, n)
	}
	if len(names) == 0 {
		ra.Warnings = append(ra.Warnings, "no pom.xml artifactId found")
	}

	if j.configCenter == "" || j.configCenter == "none" {
		return ra, nil
	}

	cfgFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return (strings.HasPrefix(base, "application") || strings.HasPrefix(base, "bootstrap")) &&
			(strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".properties"))
	})
	for _, f := range cfgFiles {
		rel, _ := filepath.Rel(repoPath, f)
		base := strings.ToLower(filepath.Base(rel))
		var finding *Finding
		var err error
		if strings.HasSuffix(base, ".properties") {
			finding, err = ScanProperties(f, rel, j.configCenter)
		} else {
			finding, err = ScanYAML(f, rel, j.configCenter)
		}
		if err != nil {
			ra.Warnings = append(ra.Warnings, "scan "+rel+": "+err.Error())
			continue
		}
		if finding != nil {
			finding.EnvProfile = extractEnvProfile(rel)
			ra.Findings = append(ra.Findings, *finding)
		}
	}
	return ra, nil
}

func readPomArtifactID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var p pomProject
	if err := xml.Unmarshal(data, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.ArtifactID)
}
