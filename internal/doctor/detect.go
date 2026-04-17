package doctor

import (
	"os"
	"path/filepath"
)

// detectStack 基于标记文件粗判仓库技术栈；无法判断返回 ""
func detectStack(repoPath string) string {
	type marker struct {
		file  string
		stack string
	}
	markers := []marker{
		{"go.mod", "go"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"build.gradle.kts", "java"},
		{"package.json", "node"},
		{"requirements.txt", "python"},
		{"pyproject.toml", "python"},
		{"Cargo.toml", "rust"},
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(repoPath, m.file)); err == nil {
			return m.stack
		}
	}
	return ""
}
