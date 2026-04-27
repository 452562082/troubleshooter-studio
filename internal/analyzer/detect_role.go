package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DetectRole 按根目录文件线索猜仓库角色:frontend / backend / gateway / infra / shared。
//
// 规则(命中优先级):
//  1. package.json 的 dependencies 里有 vue/react/svelte/next/nuxt/vite 等典型前端框架
//     → frontend
//  2. nginx.conf / Caddyfile / traefik.toml / envoy*.yaml 等网关配置 → gateway
//  3. go.mod / pom.xml / requirements.txt / package.json 都有但不是前端 → backend
//     (大部分后端服务都命中这里)
//  4. 只有 Dockerfile / Makefile / k8s yaml,没有源码 → infra
//  5. 都不命中 → "" 让调用方回落默认值
//
// 注:这是启发式,会有误判。用户可以手改 yaml 覆盖;也可以通过后续扩展 framework
// 字段细化。role 主要给 routing skill 生成时一个语义分组用,不必完全精确。
func DetectRole(repoPath string) string {
	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		return ""
	}
	// monorepo 支持:如果根目录没 stack marker,回落到 depth-1 子目录里第一个有 marker 的。
	// 这样 truss/ 这种"根没 go.mod 但 commerce/ 下有"的仓库也能识别 role。
	effectiveRoot, _ := findStackRoot(repoPath)
	if effectiveRoot == "" {
		effectiveRoot = repoPath
	}
	exists := func(p string) bool {
		_, err := os.Stat(filepath.Join(effectiveRoot, p))
		return err == nil
	}

	// 1) 前端识别:package.json + 前端框架 dep
	if pkgPath := filepath.Join(effectiveRoot, "package.json"); fileExists(pkgPath) {
		if isFrontendPackageJSON(pkgPath) {
			return "frontend"
		}
		// 有 package.json 但不是前端:node 后端(express/fastify/nest)走 backend
		return "backend"
	}

	// 2) 网关识别:常见代理/网关配置文件
	gatewayMarkers := []string{
		"nginx.conf", "Caddyfile", "traefik.toml", "traefik.yaml", "traefik.yml",
		"envoy.yaml", "envoy.yml", "haproxy.cfg", "kong.yaml",
	}
	for _, m := range gatewayMarkers {
		if exists(m) {
			return "gateway"
		}
	}

	// 3) 后端识别:任何主流后端语言 marker
	backendMarkers := []string{
		"go.mod", "pom.xml", "build.gradle", "build.gradle.kts",
		"pyproject.toml", "requirements.txt", "Pipfile", "setup.py",
		"composer.json", "Cargo.toml",
	}
	for _, m := range backendMarkers {
		if exists(m) {
			return "backend"
		}
	}

	// 4) 纯 infra:只有 Docker/k8s/Makefile 一类没源码
	infraMarkers := []string{
		"Dockerfile", "docker-compose.yaml", "docker-compose.yml",
		"Makefile", "kustomization.yaml", "Chart.yaml",
	}
	hasInfra := false
	for _, m := range infraMarkers {
		if exists(m) {
			hasInfra = true
			break
		}
	}
	if hasInfra {
		return "infra"
	}

	return ""
}

// isFrontendPackageJSON 解析 package.json 的 dependencies + devDependencies,
// 判断是不是前端工程(React/Vue/Next/Nuxt/Svelte/Vite 等特征框架)。
func isFrontendPackageJSON(pkgPath string) bool {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	feMarkers := []string{
		"react", "next", "preact", "gatsby",
		"vue", "nuxt", "@vue/cli-service",
		"svelte", "sveltekit", "@sveltejs/kit",
		"@angular/core",
		"solid-js",
		"vite", "webpack",
	}
	check := func(m map[string]string) bool {
		if m == nil {
			return false
		}
		for _, name := range feMarkers {
			if _, ok := m[name]; ok {
				return true
			}
		}
		return false
	}
	return check(pkg.Dependencies) || check(pkg.DevDependencies)
}

// fileExists 定义在 walker.go 里,这里复用同包。
