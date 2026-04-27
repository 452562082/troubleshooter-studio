package analyzer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// walkFiles 遍历仓库，按 include 过滤 + 跳过 .git/node_modules/vendor/target 等
//
// 目录跳过规则:
//   - 以 . 开头的目录整体跳(.git / .cache / .vscode / .gradle / .idea / .goroot...)
//     truss 这种仓库把 Go module cache 放 content/.cache/go-mod/,不跳会把几百个
//     vendored module 的 go.mod 当成子服务误报成 service_names。
//   - 明确的源码忽略目录:node_modules / vendor / target / build / dist / testdata
func walkFiles(root string, include []string, match func(path string) bool) ([]string, error) {
	var out []string
	skipDirs := map[string]bool{
		"node_modules": true, "vendor": true,
		"target": true, "build": true, "dist": true,
		"testdata": true, "third_party": true,
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // 遍历遇权限错/符号链接失效,跳过该文件不中断
		}
		if d.IsDir() {
			name := d.Name()
			// 跳所有 . 开头的目录(但根本身可能叫 .foo,不要把自己跳掉)
			if p != root && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if skipDirs[name] {
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
