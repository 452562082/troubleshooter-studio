package analyzer

import (
	"os"
	"path/filepath"
)

// DetectStack 按"根目录 marker 文件"规则猜测一个仓库的技术栈,让用户不用手填。
// 命中顺序:Go 最稳(go.mod 几乎不会误判)→ Java → Python → Node → PHP。
// 都没命中返回空串,调用方应回退到"要求用户手选"。
//
// 检测只看仓库根文件,不深扫目录 —— 主要为了快 + 避免把 vendor/node_modules 里的
// 标记文件误当成主 stack(比如 go 项目里 vendor 某个 node 依赖的 package.json 不该
// 把整个仓库判为 node)。
//
// 如果一个 mono-repo 同时有多个语言,返回"看起来最主要"的那个(顺序优先);
// 对这种用户可以手动覆盖 yaml.repos[].stack。
func DetectStack(repoPath string) string {
	// 先判断路径是目录,避免 os.Stat 在后续 join 上吐奇怪错误
	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		return ""
	}
	// 每个栈对应一组根文件标记。任一命中即认定。
	stacks := []struct {
		stack   string
		markers []string
	}{
		// Go: go.mod 是最稳的标识
		{"go", []string{"go.mod", "go.sum"}},
		// Java: Maven / Gradle 二选一
		{"java", []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}},
		// Python: requirements / pyproject / poetry
		{"python", []string{"pyproject.toml", "requirements.txt", "Pipfile", "setup.py"}},
		// Node:package.json 存在即认 node
		{"node", []string{"package.json"}},
		// PHP: composer
		{"php", []string{"composer.json", "composer.lock"}},
	}
	for _, s := range stacks {
		for _, m := range s.markers {
			if _, err := os.Stat(filepath.Join(repoPath, m)); err == nil {
				return s.stack
			}
		}
	}
	return ""
}
