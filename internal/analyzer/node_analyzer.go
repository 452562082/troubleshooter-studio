package analyzer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type nodeAnalyzer struct {
	configCenter string
}

func NewNodeAnalyzer() Analyzer { return &nodeAnalyzer{} }

func NewNodeAnalyzerWithCC(configCenter string) Analyzer {
	return &nodeAnalyzer{configCenter: configCenter}
}

func (nodeAnalyzer) Stack() string { return "node" }

type packageJSONFull struct {
	Name         string            `json:"name"`
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
	DevDeps      map[string]string `json:"devDependencies"`
}

// FrontendMeta 前端项目的额外元信息，附加到 RepoAnalysis.Warnings 或 Findings 中
type FrontendMeta struct {
	Framework string   `json:"framework,omitempty"` // next / nuxt / vite / cra / angular / vue-cli / unknown
	BuildTool string   `json:"build_tool,omitempty"`
	APIURLs   []string `json:"api_urls,omitempty"` // 从 .env.* 中提取的 API 基地址
}

func (n nodeAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "node",
		RepoPath: repoPath,
		Verified: true,
	}
	pkgPath := filepath.Join(repoPath, "package.json")
	if !fileExists(pkgPath) {
		ra.Warnings = append(ra.Warnings, "package.json not found")
		return ra, nil
	}
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		ra.Warnings = append(ra.Warnings, "read package.json: "+err.Error())
		return ra, nil //nolint:nilerr // 只读 package.json 失败不致命,继续用 ra
	}
	var pkg packageJSONFull
	if err := json.Unmarshal(data, &pkg); err == nil && pkg.Name != "" {
		ra.ServiceNames = append(ra.ServiceNames, pkg.Name)
	}

	// 框架识别 → Notes(信息性发现,非异常)
	framework, buildTool := detectFrontendFramework(pkg)
	if framework != "" {
		ra.Notes = append(ra.Notes, "frontend_framework="+framework)
	}
	if buildTool != "" {
		ra.Notes = append(ra.Notes, "build_tool="+buildTool)
	}

	// .env.* 扫描：提取 API URL + 配置中心变量
	envFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return strings.HasPrefix(base, ".env")
	})
	for _, f := range envFiles {
		rel, _ := filepath.Rel(repoPath, f)
		envData, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		kv := parseDotEnv(string(envData))

		// 提取前端 API URL 变量 → Notes(扫描事实,不是警告)
		apiURLs := extractAPIURLs(kv, framework)
		for _, u := range apiURLs {
			ra.Notes = append(ra.Notes, "api_url["+rel+"]="+u)
		}

		// 如果有配置中心变量（极少数前端直连 Nacos/Apollo），也抽取
		if n.configCenter != "" && n.configCenter != "none" {
			finding, err := ScanDotEnv(f, rel, n.configCenter)
			if err == nil && finding != nil {
				ra.Findings = append(ra.Findings, *finding)
			}
		}
	}

	srcFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		if strings.Contains(rel, "node_modules") {
			return false
		}
		ext := strings.ToLower(filepath.Ext(rel))
		return ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx" || ext == ".vue"
	})
	for _, f := range srcFiles {
		info, err := os.Stat(f)
		if err != nil || info.Size() > 512*1024 {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repoPath, f)
		for _, ep := range extractAPIEndpointPaths(string(b)) {
			ra.Notes = append(ra.Notes, "api_endpoint["+rel+"]="+ep)
		}
	}

	return ra, nil
}

func detectFrontendFramework(pkg packageJSONFull) (framework, buildTool string) {
	allDeps := map[string]bool{}
	for k := range pkg.Dependencies {
		allDeps[k] = true
	}
	for k := range pkg.DevDeps {
		allDeps[k] = true
	}

	switch {
	case allDeps["next"]:
		framework = "next"
	case allDeps["nuxt"] || allDeps["nuxt3"]:
		framework = "nuxt"
	case allDeps["@angular/core"]:
		framework = "angular"
	case allDeps["vite"]:
		framework = "vite"
		if allDeps["react"] {
			framework = "vite-react"
		} else if allDeps["vue"] {
			framework = "vite-vue"
		}
	case allDeps["react-scripts"]:
		framework = "cra"
	case allDeps["@vue/cli-service"]:
		framework = "vue-cli"
	case allDeps["react"]:
		framework = "react"
	case allDeps["vue"]:
		framework = "vue"
	}

	// build tool 从 scripts 推断
	for _, cmd := range pkg.Scripts {
		switch {
		case strings.Contains(cmd, "next "):
			buildTool = "next"
		case strings.Contains(cmd, "nuxt "):
			buildTool = "nuxt"
		case strings.Contains(cmd, "vite "):
			buildTool = "vite"
		case strings.Contains(cmd, "vue-cli-service"):
			buildTool = "vue-cli"
		case strings.Contains(cmd, "react-scripts"):
			buildTool = "react-scripts"
		case strings.Contains(cmd, "ng "):
			buildTool = "angular-cli"
		case strings.Contains(cmd, "webpack"):
			buildTool = "webpack"
		}
		if buildTool != "" {
			break
		}
	}
	return
}

// extractAPIURLs 从 .env 的 KV 中提取前端常见的 API 基地址变量
func extractAPIURLs(kv map[string]string, framework string) []string {
	// 各框架约定的公开环境变量前缀
	prefixes := []string{
		"NEXT_PUBLIC_", // Next.js
		"NUXT_PUBLIC_", // Nuxt 3
		"VITE_",        // Vite
		"VUE_APP_",     // Vue CLI
		"REACT_APP_",   // CRA
	}
	// 常见 API URL 关键词
	apiKeywords := []string{"API_URL", "API_BASE", "API_HOST", "API_ENDPOINT", "BASE_URL", "BACKEND_URL", "SERVER_URL"}

	var urls []string
	for k, v := range kv {
		if !strings.HasPrefix(v, "http") {
			continue
		}
		upper := strings.ToUpper(k)
		matched := false
		for _, pfx := range prefixes {
			if strings.HasPrefix(upper, pfx) {
				for _, kw := range apiKeywords {
					if strings.Contains(upper, kw) {
						matched = true
						break
					}
				}
			}
		}
		// 也接受无前缀但包含 API_URL 等关键词的
		if !matched {
			for _, kw := range apiKeywords {
				if strings.Contains(upper, kw) {
					matched = true
					break
				}
			}
		}
		if matched {
			urls = append(urls, v)
		}
	}
	return urls
}

func extractAPIEndpointPaths(src string) []string {
	re := regexp.MustCompile(`(?i)(?:fetch|axios\.(?:get|post|put|patch|delete)|request)\s*\(\s*['"]([^'"]+)['"]`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		path := strings.TrimSpace(m[1])
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "http") {
			continue
		}
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}
