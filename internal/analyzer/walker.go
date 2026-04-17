package analyzer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// walkFiles 遍历仓库，按 include 过滤 + 跳过 .git/node_modules/vendor/target 等
func walkFiles(root string, include []string, match func(path string) bool) ([]string, error) {
	var out []string
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"target": true, "build": true, "dist": true,
		".idea": true, ".vscode": true, ".gradle": true,
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无法访问
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if len(include) > 0 {
			matched := false
			for _, inc := range include {
				if strings.HasPrefix(rel, strings.TrimSuffix(inc, "/")) {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
		if match(rel) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
