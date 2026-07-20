// monorepo_scan_node.go —— Node monorepo 探测路径(workspaces / lerna / pnpm / turbo)。
// 多种声明方式优先级:pnpm-workspace.yaml → lerna.json → package.json:workspaces。
// 顶层匹配后展开 "packages/*" 单层 wildcard 到具体子目录;复杂 glob 不支持。
package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func detectNodeWorkspaces(repoPath string) []SubmoduleHint {
	patterns, source := readNodeWorkspacePatterns(repoPath)
	if len(patterns) > 0 {
		return expandWorkspacePatterns(repoPath, patterns, source)
	}
	return nil
}

// readNodeWorkspacePatterns 统一读取 workspace 声明，供普通 monorepo 提示和
// “可部署应用”识别共用，避免两条扫描链路得出不同目录集合。
func readNodeWorkspacePatterns(repoPath string) ([]string, string) {
	// pnpm-workspace.yaml
	if patterns := readPnpmWorkspace(repoPath); len(patterns) > 0 {
		return patterns, "pnpm-workspace.yaml"
	}
	// lerna.json
	if data, err := os.ReadFile(filepath.Join(repoPath, "lerna.json")); err == nil {
		var lerna struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(data, &lerna); err == nil && len(lerna.Packages) > 0 {
			return lerna.Packages, "lerna.json"
		}
	}
	// turbo.json 不直接列 packages,但 turbo 项目几乎都配 workspaces 在 package.json
	// package.json:workspaces (npm/yarn) — 可以是 string[] 或 {packages: string[]}
	if data, err := os.ReadFile(filepath.Join(repoPath, "package.json")); err == nil {
		var pkg struct {
			Workspaces json.RawMessage `json:"workspaces"`
		}
		if err := json.Unmarshal(data, &pkg); err == nil && len(pkg.Workspaces) > 0 {
			var arr []string
			if json.Unmarshal(pkg.Workspaces, &arr) == nil && len(arr) > 0 {
				return arr, "package.json:workspaces"
			}
			var obj struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal(pkg.Workspaces, &obj) == nil && len(obj.Packages) > 0 {
				return obj.Packages, "package.json:workspaces"
			}
		}
	}
	return nil, ""
}

// detectDeployableNodeApps 从 workspace 中只挑真正可独立运行、独立构建镜像的应用。
// package.json 里的包数量不是服务数量：SDK、组件库、eslint config 都不能进入
// service_names。这里采用强信号“start 脚本 + 同目录 Dockerfile”，并把仓库根应用
// 也作为一个入口计入。
func detectDeployableNodeApps(repoPath string) []SubmoduleHint {
	patterns, source := readNodeWorkspacePatterns(repoPath)
	if len(patterns) == 0 {
		return nil
	}

	hints := make([]SubmoduleHint, 0, 2)
	if ok, reason := isDeployableNodeApp(repoPath); ok {
		name := filepath.Base(filepath.Clean(repoPath))
		role := RecommendRole("node", name, repoPath)
		hints = append(hints, SubmoduleHint{
			Name:    name,
			SubPath: ".",
			Stack:   "node",
			Role:    role.Role,
			Reason:  "仓库根应用: " + reason + " + " + role.Reason,
		})
	}

	for _, hint := range expandWorkspacePatterns(repoPath, patterns, source) {
		if hint.Stack != "node" {
			continue
		}
		full := filepath.Join(repoPath, filepath.FromSlash(hint.SubPath))
		ok, reason := isDeployableNodeApp(full)
		if !ok {
			continue
		}
		hint.Reason = hint.Reason + " + " + reason
		hints = append(hints, hint)
	}
	return hints
}

func isDeployableNodeApp(dir string) (bool, string) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false, ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil || strings.TrimSpace(pkg.Scripts["start"]) == "" {
		return false, ""
	}
	if info, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil || info.IsDir() {
		return false, ""
	}
	return true, "package.json 含 start 且有独立 Dockerfile"
}

func readPnpmWorkspace(repoPath string) []string {
	data, err := os.ReadFile(filepath.Join(repoPath, "pnpm-workspace.yaml"))
	if err != nil {
		return nil
	}
	// 不引入 yaml 库,简单按行扫 "  - 'packages/*'" 模式
	lines := strings.Split(string(data), "\n")
	var patterns []string
	inPackages := false
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if strings.HasPrefix(l, "packages:") {
			inPackages = true
			continue
		}
		if inPackages {
			if !strings.HasPrefix(l, "-") && l != "" {
				break // 进入下个 key
			}
			pat := strings.TrimSpace(strings.TrimPrefix(l, "-"))
			pat = strings.Trim(pat, `'"`)
			if pat != "" {
				patterns = append(patterns, pat)
			}
		}
	}
	return patterns
}

// expandWorkspacePatterns 把 ["packages/*", "apps/*"] 展开成具体子目录列表 + 推断 hint。
// 仅支持单层 wildcard(foo/*);复杂 glob 不展开。固定路径(无 *)直接当一个子模块。
func expandWorkspacePatterns(repoPath string, patterns []string, source string) []SubmoduleHint {
	var hints []SubmoduleHint
	seen := map[string]bool{}
	for _, pat := range patterns {
		dir, leaf := filepath.Split(strings.TrimSuffix(pat, "/"))
		dir = strings.TrimSuffix(dir, "/")
		if leaf != "*" {
			fullPath := filepath.Join(repoPath, pat)
			if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
				hint := nodePackageHint(repoPath, pat, source)
				if hint.SubPath != "" && !seen[hint.SubPath] {
					seen[hint.SubPath] = true
					hints = append(hints, hint)
				}
			}
			continue
		}
		// pattern 是 "<dir>/*" → 列 <dir> 下的子目录
		listDir := filepath.Join(repoPath, dir)
		entries, err := os.ReadDir(listDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			rel := filepath.Join(dir, e.Name())
			subFull := filepath.Join(listDir, e.Name())
			// 子目录可以是 node 包(package.json)也可以是其他 stack —— 大型 monorepo
			// 经常 services/<x> 下混着 go / node / java。优先识别 stack,无 manifest 才跳。
			stack := stackFromManifest(subFull)
			if stack == "" {
				continue
			}
			var hint SubmoduleHint
			if stack == "node" {
				hint = nodePackageHint(repoPath, rel, source)
			} else {
				name := e.Name()
				role := RecommendRole(stack, name, subFull)
				hint = SubmoduleHint{
					Name:    name,
					SubPath: filepath.ToSlash(rel),
					Stack:   stack,
					Role:    role.Role,
					Reason:  source + " + " + role.Reason,
				}
			}
			if !seen[hint.SubPath] {
				seen[hint.SubPath] = true
				hints = append(hints, hint)
			}
		}
	}
	return hints
}

// nodePackageHint 优先用 package.json:name(去掉 scope @org/ 前缀),否则用目录末段。
func nodePackageHint(repoPath, subPath, source string) SubmoduleHint {
	full := filepath.Join(repoPath, subPath)
	name := filepath.Base(subPath)
	if data, err := os.ReadFile(filepath.Join(full, "package.json")); err == nil {
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Name != "" {
			n := pkg.Name
			if strings.HasPrefix(n, "@") {
				if idx := strings.Index(n, "/"); idx > 0 {
					n = n[idx+1:]
				}
			}
			name = n
		}
	}
	role := RecommendRole("node", name, full)
	return SubmoduleHint{
		Name:    name,
		SubPath: filepath.ToSlash(subPath),
		Stack:   "node",
		Role:    role.Role,
		Reason:  source + " + " + role.Reason,
	}
}
