// 三个 count* 给 GenSummary 字段填值,放一起方便调用方一眼看到"总览口径"在哪算。
package generator

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
)

func countSkills(outputDir string) int {
	skillsDir := filepath.Join(outputDir, "templates", "workspace-template", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func countFiles(outputDir string) int {
	n := 0
	_ = filepath.WalkDir(outputDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func countOverrides(m map[string]map[string]analyzer.Finding) int {
	n := 0
	for _, byEnv := range m {
		n += len(byEnv)
	}
	return n
}
