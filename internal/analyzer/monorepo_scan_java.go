// monorepo_scan_java.go —— Java multi-module 探测路径(parent pom.xml <modules>)。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var javaModuleRE = regexp.MustCompile(`(?s)<modules>(.*?)</modules>`)
var javaModuleEntryRE = regexp.MustCompile(`<module>\s*([^<\s]+)\s*</module>`)

func detectJavaModules(repoPath string) []SubmoduleHint {
	pomPath := filepath.Join(repoPath, "pom.xml")
	data, err := os.ReadFile(pomPath)
	if err != nil {
		return nil
	}
	block := javaModuleRE.FindSubmatch(data)
	if len(block) < 2 {
		return nil
	}
	matches := javaModuleEntryRE.FindAllStringSubmatch(string(block[1]), -1)
	if len(matches) == 0 {
		return nil
	}
	var hints []SubmoduleHint
	for _, m := range matches {
		mod := strings.TrimSpace(m[1])
		if mod == "" {
			continue
		}
		full := filepath.Join(repoPath, mod)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		role := RecommendRole("java", mod, full)
		hints = append(hints, SubmoduleHint{
			Name:    mod,
			SubPath: filepath.ToSlash(mod),
			Stack:   "java",
			Role:    role.Role,
			Reason:  "parent pom.xml <modules> + " + role.Reason,
		})
	}
	return hints
}
