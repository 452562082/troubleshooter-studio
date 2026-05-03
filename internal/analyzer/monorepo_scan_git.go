// monorepo_scan_git.go —— Git submodules (.gitmodules) 探测路径。
// .gitmodules 里每个 submodule 是独立 git repo,各自有 URL —— 抽出来,split
// 时各行用自己的 URL,不再"全用父仓 URL"。可信度最高,DetectSubmodules 第一个跑。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var gitmodulePathRE = regexp.MustCompile(`(?m)^\s*path\s*=\s*(.+?)\s*$`)
var gitmoduleURLRE = regexp.MustCompile(`(?m)^\s*url\s*=\s*(.+?)\s*$`)
var gitmoduleSectionRE = regexp.MustCompile(`(?ms)^\[submodule\s+"[^"]+"\]\s*\n((?:[^\[]+\n)*)`)

func detectGitSubmodules(repoPath string) []SubmoduleHint {
	data, err := os.ReadFile(filepath.Join(repoPath, ".gitmodules"))
	if err != nil {
		return nil
	}
	var hints []SubmoduleHint
	seen := map[string]bool{}
	for _, sec := range gitmoduleSectionRE.FindAllStringSubmatch(string(data), -1) {
		body := sec[1]
		pathM := gitmodulePathRE.FindStringSubmatch(body)
		if len(pathM) < 2 {
			continue
		}
		path := strings.TrimSpace(pathM[1])
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		var url string
		if urlM := gitmoduleURLRE.FindStringSubmatch(body); len(urlM) >= 2 {
			url = strings.TrimSpace(urlM[1])
		}
		full := filepath.Join(repoPath, path)
		// 检测子模块自己的 stack(走 stackFromManifest;没 manifest 时 fallback 到 go,
		// 因为大多数 .gitmodules 用法是 Go 服务集合。用户可在 wizard 改)
		stack := stackFromManifest(full)
		if stack == "" {
			stack = "go"
		}
		name := filepath.Base(path)
		role := RecommendRole(stack, name, full)
		// 子模块的源码可能还没初始化(用户 clone 时没 --recurse-submodules);跑 RecommendRole 时
		// 路径不存在 / 是空目录,只会按 name + stack 兜底。仍然有用。
		hints = append(hints, SubmoduleHint{
			Name:    name,
			SubPath: filepath.ToSlash(path),
			URL:     url,
			Stack:   stack,
			Role:    role.Role,
			Reason:  ".gitmodules 声明 + " + role.Reason,
		})
	}
	return hints
}
