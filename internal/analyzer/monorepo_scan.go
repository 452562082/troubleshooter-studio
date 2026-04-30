// monorepo_scan.go —— 自动识别 monorepo 子模块
//
// 用户给一个仓库本地路径,本函数扫顶层 + 一两层目录,看有没有"多服务"标志:
//   - Node:  package.json:workspaces / lerna.json:packages / pnpm-workspace.yaml / turbo.json
//   - Java:  parent pom.xml 含 <modules>
//   - Go:    cmd/<x>/main.go(每个 <x> 一个独立可执行)
//   - 通用:  services/<x>/ / packages/<x>/ / apps/<x>/ / modules/<x>/ 子目录里各自有 package.json/go.mod/pom.xml
//
// 命中即返回每个子模块的 {Name, SubPath, Stack, RoleHint};Wizard UI 据此给"一键拆成 N 行"按钮。
package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SubmoduleHint 一个子模块的探测结果。
type SubmoduleHint struct {
	Name    string `json:"name"`     // 推荐的服务名(从子目录名 / package.json:name 抽)
	SubPath string `json:"sub_path"` // 相对仓库根的路径
	Stack   string `json:"stack"`    // 该子模块自身的 stack(可能跟父仓库不同,如 web/admin 是 node)
	Role    string `json:"role"`     // 推荐角色(走 RecommendRole 同款规则)
	Reason  string `json:"reason"`   // 命中证据
	// URL 仅在 .gitmodules 路径下非空 —— 那是真"独立 git repo + 子目录共置"场景,
	// 每个 submodule 有自己的 git URL。其它检测路径(workspaces / pom modules / cmd 多入口 /
	// top-level services)是"同一仓库子目录",共用父仓 URL,本字段空。
	// Split 时:URL 非空 → 当独立仓库展开(自己的 url + 自己的本地路径 + 无 sub_path);
	//          URL 空 → 当同仓子目录展开(父 url + 父本地路径 + 各自 sub_path)。
	URL string `json:"url,omitempty"`
}

// DetectSubmodules 扫 repoPath 顶层,返回 0 / 1 / N 条子模块。
//   返回 0 条 = 不是 monorepo,UI 不显示拆分按钮
//   返回 1 条 = 边界情况(如 services/ 下只有 1 个),仍当 monorepo 处理(为 future-proof)
//   返回 N 条 = monorepo,UI 弹"检测到 N 个子模块,一键拆分"
//
// 不要求绝对完备:命中常见模式即可,非典型 monorepo 用户走"+ 子模块"手动加。
func DetectSubmodules(repoPath string) []SubmoduleHint {
	if repoPath == "" {
		return nil
	}
	if _, err := os.Stat(repoPath); err != nil {
		return nil
	}

	// 1. Git submodules(.gitmodules):umbrella repo 显式声明的子模块,可信度最高
	if hints := detectGitSubmodules(repoPath); len(hints) > 0 {
		return hints
	}
	// 2. Node workspaces:package.json:workspaces / lerna.json / pnpm-workspace.yaml / turbo.json
	if hints := detectNodeWorkspaces(repoPath); len(hints) > 0 {
		return hints
	}
	// 3. Java multi-module:parent pom.xml 含 <modules>
	if hints := detectJavaModules(repoPath); len(hints) > 0 {
		return hints
	}
	// 4. Go: cmd/<x>/main.go(命中多个就是 monorepo)
	if hints := detectGoCmdDirs(repoPath); len(hints) > 1 {
		return hints
	}
	// 5. 通用:services/ / packages/ / apps/ / modules/ 下各子目录有自己的 manifest 文件
	if hints := detectGenericServiceDir(repoPath); len(hints) > 1 {
		return hints
	}
	// 6. 顶层平铺多服务:repo 根直接挂多个有 manifest 的子目录(非 services/ wrapper),
	// 跨语言 monorepo / 老项目结构常见。命中 ≥ 2 才认。
	if hints := detectTopLevelServices(repoPath); len(hints) > 1 {
		return hints
	}
	return nil
}

// ── Git submodules (.gitmodules) ──

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
		// .gitmodules 里每个 submodule 是独立 git repo,各自有 URL —— 抽出来,split
		// 时各行用自己的 URL,不再"全用父仓 URL"。
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

// ── 顶层平铺多服务 ──

// 顶层目录里"看着不像服务"的过滤名单(常见辅助目录,不进 service 列表)
var topLevelExcludeDirs = map[string]bool{
	"docs": true, "doc": true, "wiki": true, "specs": true, "design": true,
	"scripts": true, "script": true, "tools": true, "tool": true, "bin": true,
	"test": true, "tests": true, "testing": true, "e2e": true, "fixtures": true,
	"vendor": true, "node_modules": true, "dist": true, "build": true, "target": true,
	"assets": true, "static": true, "public": true,
	"deploy": true, "deployment": true, "infra": true, "infrastructure": true,
	"helm": true, "kubernetes": true, "k8s": true, "kustomize": true, "manifests": true, "terraform": true,
	"config": true, "configs": true, "examples": true, "example": true, "demo": true,
}

func detectTopLevelServices(repoPath string) []SubmoduleHint {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return nil
	}
	var hints []SubmoduleHint
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || topLevelExcludeDirs[strings.ToLower(name)] {
			continue
		}
		full := filepath.Join(repoPath, name)
		stack := stackFromManifest(full)
		if stack == "" {
			continue // 没 manifest 文件,直接跳
		}
		// 顶层平铺路径风险:tools/helpers/utils/<x> 子目录可能也有 go.mod / package.json,
		// 但它们是工具不是服务。要求"可部署信号"才算 ——  Dockerfile / main 入口 / 框架启动文件
		// 任一命中。仅有 manifest 文件不够。
		// (services/<x> wrapper / .gitmodules / pom modules / cmd/<x>/main.go 这些走自己的检测路径,
		// 它们的"可部署性"是声明性的;只有这条平铺路径需要靠"信号"猜)
		if !hasDeployableEntry(full, stack) {
			continue
		}
		role := RecommendRole(stack, name, full)
		hints = append(hints, SubmoduleHint{
			Name:    name,
			SubPath: name,
			Stack:   stack,
			Role:    role.Role,
			Reason:  "顶层子目录含 " + deployableSignalReason(full, stack) + " + " + role.Reason,
		})
	}
	return hints
}

// hasDeployableEntry 判断 dir 是否看着像可部署服务(而非纯库 / 工具):
//   - Dockerfile  最强信号(任何 stack 都通用)
//   - go:    main.go / cmd/<x>/main.go
//   - node:  package.json:scripts.start / dev 含服务端启动命令(non-build)
//   - java:  pom.xml 含 spring-boot-starter-web / spring-cloud-gateway / packaging=war
//   - python: manage.py / app.py / main.py / wsgi.py / asgi.py
//   - php:   public/index.php / artisan / index.php
//   都不命中 → 大概率是 lib/tool/sample,不进 detectTopLevelServices 的服务列表
func hasDeployableEntry(dir, stack string) bool {
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return true
	}
	switch strings.ToLower(stack) {
	case "go":
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err == nil {
			return true
		}
		if cmds, err := os.ReadDir(filepath.Join(dir, "cmd")); err == nil {
			for _, c := range cmds {
				if !c.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(dir, "cmd", c.Name(), "main.go")); err == nil {
					return true
				}
			}
		}
	case "node":
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			low := strings.ToLower(string(data))
			// scripts 含服务端启动命令的关键字(避开 build/test 等)
			for _, kw := range []string{"\"start\"", "\"dev\"", "\"serve\"", "node ", "ts-node", "nodemon", "next start", "nuxt start", "nest start", "fastify"} {
				if strings.Contains(low, kw) {
					return true
				}
			}
		}
	case "java":
		data, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
		if err != nil {
			data, _ = os.ReadFile(filepath.Join(dir, "build.gradle"))
		}
		low := strings.ToLower(string(data))
		for _, kw := range []string{"spring-boot-starter-web", "spring-webflux", "spring-cloud-starter-gateway", "<packaging>war</packaging>", "spring-boot-maven-plugin"} {
			if strings.Contains(low, kw) {
				return true
			}
		}
	case "python":
		for _, f := range []string{"manage.py", "app.py", "main.py", "wsgi.py", "asgi.py", "server.py"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return true
			}
		}
	case "php":
		for _, f := range []string{"public/index.php", "artisan", "index.php"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return true
			}
		}
	}
	return false
}

// deployableSignalReason 复述命中的可部署信号给 UI 展示("for 顶层子目录含 X")
func deployableSignalReason(dir, stack string) string {
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return "Dockerfile"
	}
	switch strings.ToLower(stack) {
	case "go":
		if _, err := os.Stat(filepath.Join(dir, "main.go")); err == nil {
			return "main.go"
		}
		return "cmd/<x>/main.go"
	case "node":
		return "package.json scripts.start"
	case "java":
		return "pom.xml spring-boot-starter-web"
	case "python":
		return "manage.py / app.py / main.py / wsgi.py / asgi.py"
	case "php":
		return "public/index.php / artisan"
	}
	return "manifest 文件"
}

// ── Node workspaces ──

func detectNodeWorkspaces(repoPath string) []SubmoduleHint {
	// pnpm-workspace.yaml
	if patterns := readPnpmWorkspace(repoPath); len(patterns) > 0 {
		return expandWorkspacePatterns(repoPath, patterns, "pnpm-workspace.yaml")
	}
	// lerna.json
	if data, err := os.ReadFile(filepath.Join(repoPath, "lerna.json")); err == nil {
		var lerna struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(data, &lerna); err == nil && len(lerna.Packages) > 0 {
			return expandWorkspacePatterns(repoPath, lerna.Packages, "lerna.json")
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
				return expandWorkspacePatterns(repoPath, arr, "package.json:workspaces")
			}
			var obj struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal(pkg.Workspaces, &obj) == nil && len(obj.Packages) > 0 {
				return expandWorkspacePatterns(repoPath, obj.Packages, "package.json:workspaces")
			}
		}
	}
	return nil
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
func expandWorkspacePatterns(repoPath string, patterns []string, source string) []SubmoduleHint {
	var hints []SubmoduleHint
	seen := map[string]bool{}
	for _, pat := range patterns {
		// 仅支持单层 wildcard:foo/* 这种;复杂 glob 不展开
		dir, leaf := filepath.Split(strings.TrimSuffix(pat, "/"))
		dir = strings.TrimSuffix(dir, "/")
		// 不带 wildcard 的固定路径 → 直接当成一个子模块
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

func nodePackageHint(repoPath, subPath, source string) SubmoduleHint {
	full := filepath.Join(repoPath, subPath)
	// 名字优先 package.json:name(去掉 scope @org/ 前缀),否则用目录末段
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

// ── Java multi-module ──

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

// ── Go cmd/<x>/main.go ──

func detectGoCmdDirs(repoPath string) []SubmoduleHint {
	cmdDir := filepath.Join(repoPath, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return nil
	}
	var hints []SubmoduleHint
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		mainGo := filepath.Join(cmdDir, e.Name(), "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			continue
		}
		sub := filepath.ToSlash(filepath.Join("cmd", e.Name()))
		full := filepath.Join(cmdDir, e.Name())
		role := RecommendRole("go", e.Name(), full)
		hints = append(hints, SubmoduleHint{
			Name:    e.Name(),
			SubPath: sub,
			Stack:   "go",
			Role:    role.Role,
			Reason:  "cmd/" + e.Name() + "/main.go + " + role.Reason,
		})
	}
	return hints
}

// ── 通用:services/<x> / packages/<x> / apps/<x> / modules/<x> ──

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
			// 必须含至少一个 manifest 文件才算服务
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
		// 如果某个 candidate 命中,就停 — 避免 services/ + packages/ 都有时双倍展开
		if len(hints) > 0 {
			break
		}
	}
	return hints
}

// stackFromManifest 看子目录里有什么 manifest 文件,推断子服务的 stack;无任一 manifest 返回空串。
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
