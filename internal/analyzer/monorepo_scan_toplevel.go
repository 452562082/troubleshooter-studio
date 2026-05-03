// monorepo_scan_toplevel.go —— 顶层平铺多服务探测路径(跨语言 monorepo / 老项目结构常见)。
// 风险:tools/helpers/utils 子目录可能也有 manifest 但不是服务。所以必须额外验证
// "可部署信号" —— Dockerfile / main 入口 / 框架启动文件,任一命中才算。
//
// 跟其他 detect* 路径的关键差别:
//   - .gitmodules / pom modules / cmd/<x>/main.go / services/<x> wrapper:声明性 monorepo,
//     manifest 出现即可信。
//   - 顶层平铺:只有"信号 + manifest 双命中"才认。
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

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
			continue
		}
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
//
// 都不命中 → 大概率是 lib/tool/sample,不进 detectTopLevelServices 的服务列表
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
