package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

// candidateRoots 返回一组"可能放 marker 的目录",按优先级排序。
// 先是 repoPath 本身(单仓库场景);再是 depth-1 直接子目录(monorepo 场景:truss/
// 这种根下没 go.mod,但 commerce/ user/ 各有 go.mod)。
//
// 跳过点开头目录(.git/.github)、常见忽略目录(vendor/node_modules)、明显非源码目录
// (docs/ scripts/ test/ 等),避免被"文档里的 package.json 示例"误导。不递归,保持 O(1) 子目录数。
func candidateRoots(repoPath string) []string {
	roots := []string{repoPath}
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return roots
	}
	skip := map[string]bool{
		"vendor": true, "node_modules": true, "docs": true, "doc": true,
		"scripts": true, "script": true, "test": true, "tests": true,
		"examples": true, "example": true, "testdata": true, "third_party": true,
		"build": true, "dist": true, "out": true, "target": true,
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || skip[name] {
			continue
		}
		roots = append(roots, filepath.Join(repoPath, name))
	}
	return roots
}

// stackMarkerMatrix 保持跟 DetectStack 一致的命中顺序,其它 detector 也能复用这张表。
var stackMarkerMatrix = []struct {
	stack   string
	markers []string
}{
	{"go", []string{"go.mod", "go.sum"}},
	{"java", []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}},
	{"python", []string{"pyproject.toml", "requirements.txt", "Pipfile", "setup.py"}},
	{"node", []string{"package.json"}},
	{"php", []string{"composer.json", "composer.lock"}},
}

// findStackRoot 在 repoPath + 直接子目录里找第一个放了 stack marker 的目录,
// 同时返回它的 stack。都没找到返回 "", ""。DetectStack / DetectRole /
// DetectFramework 共用,保证三个检测器落在同一个"有效根"上,避免"stack 识别用根、
// framework 识别用子目录"这种错位。
func findStackRoot(repoPath string) (effectiveRoot, stack string) {
	for _, root := range candidateRoots(repoPath) {
		for _, s := range stackMarkerMatrix {
			for _, m := range s.markers {
				if _, err := os.Stat(filepath.Join(root, m)); err == nil {
					return root, s.stack
				}
			}
		}
	}
	return "", ""
}

// DetectStack 按"根目录 marker 文件"规则猜测一个仓库的技术栈,让用户不用手填。
// 命中顺序:Go 最稳(go.mod 几乎不会误判)→ Java → Python → Node → PHP。
// 都没命中返回空串,调用方应回退到"要求用户手选"。
//
// 检测先看仓库根,根没命中再看 depth-1 子目录(支持 monorepo:truss 这种根下没
// go.mod,但 commerce/ user/ 各有 go.mod 的仓库)。不递归更深,主要为了快 + 避免
// 把 vendor/node_modules 里的标记文件误当成主 stack。
//
// 如果一个 mono-repo 同时有多个语言,返回"看起来最主要"的那个(顺序优先:go > java > ...);
// 对这种用户可以手动覆盖 yaml.repos[].stack。
func DetectStack(repoPath string) string {
	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		return ""
	}
	_, stack := findStackRoot(repoPath)
	return stack
}
