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
//
// monorepo 支持:根目录没 manifest 会回落到 depth-1 子目录(跟 DetectStack / DetectRole
// 用同一个 findStackRoot,保证三者命中同一个子目录不会错位)。
func DetectFramework(repoPath, stack string) string {
	effectiveRoot, _ := findStackRoot(repoPath)
	if effectiveRoot == "" {
		effectiveRoot = repoPath
	}
	switch stack {
	case "go":
		return detectGoFramework(effectiveRoot)
	case "java":
		return detectJavaFramework(effectiveRoot)
	case "python":
		return detectPythonFrameworkFromManifest(effectiveRoot)
	case "node":
		return detectNodeFramework(effectiveRoot)
	case "php":
		return detectPhpFramework(effectiveRoot)
	}
	return ""
}

// Go 框架识别分三档匹配,精度优先于召回:
//
//   第 1 档 "完整框架"(自带路由 + 中间件 + 配置):kratos / go-zero / kitex / hertz /
//     goframe / iris / beego / micro。命中直接返,不再看其它。
//   第 2 档 "HTTP router":gin / echo / fiber / chi。
//   第 3 档 "RPC only":grpc。只在前两档都没命中时才用,避免"同时依赖 grpc + gin"
//     的常规 HTTP 服务被误判成 grpc。
//
// go.mod 里 require (...) 块的 indirect 依赖(// indirect)不算 —— 那是传递依赖,
// 不代表本项目直接用这个框架。解析出一组直接 require 的 module path,再按完整匹配
// (不是 substring)去比。这样 truss 这种"只拉了 grpc 当 RPC 协议库"的项目会被标
// 成 grpc(第 3 档);拉了 gin 的项目会被标成 gin,不会因为 indirect 里有 grpc 就误判。
//
// 如果想进一步精确区分 "grpc 服务 / grpc 客户端",需要扫代码(.proto / grpc.NewServer),
// 代价高,暂不做 —— 现阶段标 grpc 表示"跟 grpc 协议有关",由 routing skill 下一步
// 决定怎么用。
func detectGoFramework(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return ""
	}
	directDeps := parseDirectRequires(string(data))

	// 每档内的顺序只在"同档多命中"时决定返哪个 —— 理论上项目引多个完整框架很罕见。
	tier1 := []struct{ fw, path string }{
		{"kratos", "github.com/go-kratos/kratos"},
		{"go-zero", "github.com/zeromicro/go-zero"},
		{"kitex", "github.com/cloudwego/kitex"},
		{"hertz", "github.com/cloudwego/hertz"},
		{"goframe", "github.com/gogf/gf"},
		{"iris", "github.com/kataras/iris"},
		{"beego", "github.com/beego/beego"},
		{"micro", "go-micro.dev"},
	}
	tier2 := []struct{ fw, path string }{
		{"gin", "github.com/gin-gonic/gin"},
		{"echo", "github.com/labstack/echo"},
		{"fiber", "github.com/gofiber/fiber"},
		{"chi", "github.com/go-chi/chi"},
	}
	tier3 := []struct{ fw, path string }{
		{"grpc", "google.golang.org/grpc"},
	}

	for _, tier := range [][]struct{ fw, path string }{tier1, tier2, tier3} {
		for _, m := range tier {
			if directDeps[m.path] || hasPathPrefix(directDeps, m.path+"/") {
				return m.fw
			}
		}
	}
	return ""
}

// parseDirectRequires 扫 go.mod 文本,返回一个 set(key 是 module path),
// 只包含 "直接 require" 的模块 —— // indirect 标注的传递依赖不计。
//
// 处理三种写法:
//
//	// 1. block 形式,indirect 在行尾注释里
//	require (
//	    github.com/foo/bar v1.0.0
//	    github.com/baz/qux v2.0.0 // indirect
//	)
//
//	// 2. 单行 require
//	require github.com/alpha/beta v0.1.0
//
//	// 3. 单行 require + indirect
//	require github.com/x/y v1.0.0 // indirect
//
// replace / exclude / retract / toolchain 段一律忽略(可能引入 module path 但不代表本
// 项目用这个库)。注释 // 之后的部分一律忽略,除非正好是 "indirect"(用于排除)。
func parseDirectRequires(goModText string) map[string]bool {
	out := map[string]bool{}
	inRequireBlock := false
	// 小状态机:行粒度,遇 require ( 进 block,遇 ) 出 block;其它 (replace/exclude)
	// 也是块式但我们直接忽略整块。
	inOtherBlock := false
	for _, raw := range strings.Split(goModText, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if inOtherBlock {
			if line == ")" {
				inOtherBlock = false
			}
			continue
		}
		if inRequireBlock {
			if line == ")" {
				inRequireBlock = false
				continue
			}
			if path, ok := parseRequireLine(line); ok {
				out[path] = true
			}
			continue
		}
		// 块外语句
		switch {
		case strings.HasPrefix(line, "require ("):
			inRequireBlock = true
		case strings.HasPrefix(line, "replace (") ||
			strings.HasPrefix(line, "exclude (") ||
			strings.HasPrefix(line, "retract ("):
			inOtherBlock = true
		case strings.HasPrefix(line, "require "):
			// 单行 require
			tail := strings.TrimPrefix(line, "require ")
			if path, ok := parseRequireLine(tail); ok {
				out[path] = true
			}
		}
	}
	return out
}

// parseRequireLine 解析一条 require 里的依赖行,返回 module path;
// indirect 注释存在时返回 false(直接 require 才算"本项目使用该框架")。
//
// 例:
//
//	"github.com/foo/bar v1.2.3"               → ("github.com/foo/bar", true)
//	"github.com/foo/bar v1.2.3 // indirect"   → ("", false)
//	"github.com/foo/bar v1.2.3 // some note"  → ("github.com/foo/bar", true)
func parseRequireLine(line string) (string, bool) {
	// 切出注释
	comment := ""
	if i := strings.Index(line, "//"); i >= 0 {
		comment = strings.TrimSpace(line[i+2:])
		line = strings.TrimSpace(line[:i])
	}
	// 只要注释里精确是 "indirect" 就算传递依赖(Go 工具生成的标记);
	// 其它注释(作者留言)不影响
	if comment == "indirect" {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 1 {
		return "", false
	}
	return fields[0], true
}

// hasPathPrefix 判定 set 里是否存在 "prefix/*" 形式的键。
// 处理 module path 带大版本后缀的情况:"github.com/go-kratos/kratos/v2" 要能被
// "github.com/go-kratos/kratos/" 前缀匹上。
func hasPathPrefix(set map[string]bool, prefix string) bool {
	for k := range set {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
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
