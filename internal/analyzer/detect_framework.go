package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DetectFramework 猜仓库主要用的 web / app 框架。只做主流几家,精确度优先。
// stack 是 DetectStack 的结果(go/java/python/node/php),不同栈看不同 manifest。
//
// 返回规则:命中直接返回;都没命中或 stack 空返回 ""(UI 显示"(未识别)")。
// 多框架并存时返回最"主要"的一个(通常按引入顺序 / popularity)。
func DetectFramework(repoPath, stack string) string {
	switch stack {
	case "go":
		return detectGoFramework(repoPath)
	case "java":
		return detectJavaFramework(repoPath)
	case "python":
		return detectPythonFrameworkFromManifest(repoPath)
	case "node":
		return detectNodeFramework(repoPath)
	case "php":
		return detectPhpFramework(repoPath)
	}
	return ""
}

// Go: 读 go.mod 的 require 段
func detectGoFramework(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return ""
	}
	s := string(data)
	// 顺序很重要:扫到任一条就返回,排前的优先
	markers := []struct {
		framework string
		imports   []string
	}{
		{"kratos", []string{"github.com/go-kratos/kratos"}},
		{"gin", []string{"github.com/gin-gonic/gin"}},
		{"echo", []string{"github.com/labstack/echo"}},
		{"fiber", []string{"github.com/gofiber/fiber"}},
		{"chi", []string{"github.com/go-chi/chi"}},
		{"grpc", []string{"google.golang.org/grpc"}},
		{"go-zero", []string{"github.com/zeromicro/go-zero"}},
	}
	for _, m := range markers {
		for _, imp := range m.imports {
			if strings.Contains(s, imp) {
				return m.framework
			}
		}
	}
	return ""
}

// Java: pom.xml / build.gradle 里查框架 artifact id
func detectJavaFramework(repoPath string) string {
	candidates := []string{"pom.xml", "build.gradle", "build.gradle.kts"}
	var buf []byte
	for _, f := range candidates {
		if d, err := os.ReadFile(filepath.Join(repoPath, f)); err == nil {
			buf = append(buf, d...)
			buf = append(buf, '\n')
		}
	}
	if len(buf) == 0 {
		return ""
	}
	s := string(buf)
	if strings.Contains(s, "spring-boot-starter") || strings.Contains(s, "org.springframework.boot") {
		return "spring-boot"
	}
	if strings.Contains(s, "spring-webmvc") || strings.Contains(s, "spring-webflux") {
		return "spring"
	}
	if strings.Contains(s, "micronaut") {
		return "micronaut"
	}
	if strings.Contains(s, "quarkus") {
		return "quarkus"
	}
	if strings.Contains(s, "dropwizard") {
		return "dropwizard"
	}
	return ""
}

// Python: requirements.txt / pyproject.toml / Pipfile
func detectPythonFrameworkFromManifest(repoPath string) string {
	var buf []byte
	for _, f := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py"} {
		if d, err := os.ReadFile(filepath.Join(repoPath, f)); err == nil {
			buf = append(buf, d...)
			buf = append(buf, '\n')
		}
	}
	if len(buf) == 0 {
		return ""
	}
	s := strings.ToLower(string(buf))
	// 顺序按大小框架优先:django/fastapi > flask > starlette
	if strings.Contains(s, "django") {
		return "django"
	}
	if strings.Contains(s, "fastapi") {
		return "fastapi"
	}
	if strings.Contains(s, "flask") {
		return "flask"
	}
	if strings.Contains(s, "starlette") {
		return "starlette"
	}
	if strings.Contains(s, "tornado") {
		return "tornado"
	}
	if strings.Contains(s, "sanic") {
		return "sanic"
	}
	return ""
}

// Node: package.json 的 dependencies
func detectNodeFramework(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	all := map[string]string{}
	for k, v := range pkg.Dependencies {
		all[k] = v
	}
	for k, v := range pkg.DevDependencies {
		all[k] = v
	}
	// 前端框架优先(DetectRole 已经把纯前端识别成 frontend,但 node 后端也可能引了 vue/react 的 SSR)
	frameworks := []struct {
		pkg, name string
	}{
		{"next", "nextjs"},
		{"nuxt", "nuxt"},
		{"@sveltejs/kit", "sveltekit"},
		{"@angular/core", "angular"},
		{"vue", "vue"},
		{"react", "react"},
		{"svelte", "svelte"},
		{"@nestjs/core", "nestjs"},
		{"express", "express"},
		{"fastify", "fastify"},
		{"koa", "koa"},
		{"@hapi/hapi", "hapi"},
	}
	for _, f := range frameworks {
		if _, ok := all[f.pkg]; ok {
			return f.name
		}
	}
	return ""
}

// PHP: composer.json 的 require
func detectPhpFramework(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "composer.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Require map[string]string `json:"require"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	m := pkg.Require
	if _, ok := m["laravel/framework"]; ok {
		return "laravel"
	}
	if _, ok := m["symfony/symfony"]; ok {
		return "symfony"
	}
	if _, ok := m["symfony/http-kernel"]; ok {
		return "symfony"
	}
	if _, ok := m["yiisoft/yii2"]; ok {
		return "yii"
	}
	if _, ok := m["cakephp/cakephp"]; ok {
		return "cakephp"
	}
	return ""
}
