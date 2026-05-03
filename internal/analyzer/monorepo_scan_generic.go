// monorepo_scan_generic.go —— 通用 services/<x> 类 wrapper 探测路径。
// 不止跨 stack(同一 wrapper 下 go / node / java 混着也认),
// 命中后停在第一个 candidate(避免 services/ + packages/ 都有时双倍展开)。
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

var serviceDirCandidates = []string{"services", "service", "packages", "apps", "modules", "components"}

// 子目录里有这些任一文件 = 看着是独立服务
var serviceManifestFiles = []string{"package.json", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts", "pyproject.toml", "setup.py", "composer.json", "Cargo.toml", "Dockerfile"}

func detectGenericServiceDir(repoPath string) []SubmoduleHint {
	var hints []SubmoduleHint
	seen := map[string]bool{}
	for _, parent := range serviceDirCandidates {
		parentPath := filepath.Join(repoPath, parent)
		entries, err := os.ReadDir(parentPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			subFull := filepath.Join(parentPath, e.Name())
			stack := stackFromManifest(subFull)
			if stack == "" {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(parent, e.Name()))
			if seen[rel] {
				continue
			}
			seen[rel] = true
			role := RecommendRole(stack, e.Name(), subFull)
			hints = append(hints, SubmoduleHint{
				Name:    e.Name(),
				SubPath: rel,
				Stack:   stack,
				Role:    role.Role,
				Reason:  parent + "/<x> + " + stack + " manifest + " + role.Reason,
			})
		}
		// 命中第一个 candidate 即停 — 避免 services/ + packages/ 都有时双倍展开
		if len(hints) > 0 {
			break
		}
	}
	return hints
}

// stackFromManifest 看子目录里有什么 manifest 文件,推断子服务的 stack;无任一 manifest 返回空串。
// 跨 6 个 detect* 路径共用 —— monorepo_scan.go 能保留只有 dispatch + type 是因为本函数住在这里。
func stackFromManifest(dir string) string {
	check := func(file, stack string) string {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return stack
		}
		return ""
	}
	if s := check("package.json", "node"); s != "" {
		return s
	}
	if s := check("go.mod", "go"); s != "" {
		return s
	}
	if s := check("pom.xml", "java"); s != "" {
		return s
	}
	if s := check("build.gradle", "java"); s != "" {
		return s
	}
	if s := check("build.gradle.kts", "java"); s != "" {
		return s
	}
	if s := check("pyproject.toml", "python"); s != "" {
		return s
	}
	if s := check("setup.py", "python"); s != "" {
		return s
	}
	if s := check("composer.json", "php"); s != "" {
		return s
	}
	if s := check("Cargo.toml", "rust"); s != "" {
		return s
	}
	// 没标准 manifest 但有 Dockerfile 也算独立服务,stack 待定
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return "go" // 兜底,用户可在 wizard 改
	}
	_ = serviceManifestFiles // 防止未引用变量警告(列表只作为文档)
	return ""
}
