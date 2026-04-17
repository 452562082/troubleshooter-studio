package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeAnalyzer_FakeRepo_FrameworkAndAPI(t *testing.T) {
	a := NewNodeAnalyzerWithCC(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/web-frontend", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.ServiceNames) != 1 || ra.ServiceNames[0] != "web-frontend" {
		t.Errorf("ServiceNames: got %v", ra.ServiceNames)
	}

	// 框架识别
	hasFramework := false
	for _, w := range ra.Warnings {
		if w == "frontend_framework=next" {
			hasFramework = true
		}
	}
	if !hasFramework {
		t.Errorf("expected frontend_framework=next in warnings: %v", ra.Warnings)
	}

	// build tool
	hasBuildTool := false
	for _, w := range ra.Warnings {
		if w == "build_tool=next" {
			hasBuildTool = true
		}
	}
	if !hasBuildTool {
		t.Errorf("expected build_tool=next in warnings: %v", ra.Warnings)
	}

	// API URL 提取
	hasDevAPI := false
	hasProdAPI := false
	for _, w := range ra.Warnings {
		if strings.Contains(w, "api-dev.shop.example.com") {
			hasDevAPI = true
		}
		if strings.Contains(w, "api.shop.example.com") {
			hasProdAPI = true
		}
	}
	if !hasDevAPI {
		t.Errorf("expected dev API URL in warnings: %v", ra.Warnings)
	}
	if !hasProdAPI {
		t.Errorf("expected prod API URL in warnings: %v", ra.Warnings)
	}
}

func TestNodeAnalyzer_MissingPackageJSON(t *testing.T) {
	a := NewNodeAnalyzerWithCC("")
	ra, err := a.Analyze(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Warnings) == 0 {
		t.Error("expected warning about missing package.json")
	}
}

func TestDetectFrontendFramework(t *testing.T) {
	cases := []struct {
		name      string
		deps      map[string]string
		scripts   map[string]string
		wantFw    string
		wantBuild string
	}{
		{
			name:      "next.js",
			deps:      map[string]string{"next": "^14.0.0", "react": "^18.0.0"},
			scripts:   map[string]string{"build": "next build"},
			wantFw:    "next",
			wantBuild: "next",
		},
		{
			name:      "nuxt",
			deps:      map[string]string{"nuxt": "^3.0.0", "vue": "^3.0.0"},
			scripts:   map[string]string{"build": "nuxt build"},
			wantFw:    "nuxt",
			wantBuild: "nuxt",
		},
		{
			name:      "vite-react",
			deps:      map[string]string{"vite": "^5.0.0", "react": "^18.0.0"},
			scripts:   map[string]string{"dev": "vite dev"},
			wantFw:    "vite-react",
			wantBuild: "vite",
		},
		{
			name:      "vue-cli",
			deps:      map[string]string{"vue": "^3.0.0", "@vue/cli-service": "^5.0.0"},
			scripts:   map[string]string{"build": "vue-cli-service build"},
			wantFw:    "vue-cli",
			wantBuild: "vue-cli",
		},
		{
			name:      "cra",
			deps:      map[string]string{"react": "^18.0.0", "react-scripts": "^5.0.0"},
			scripts:   map[string]string{"build": "react-scripts build"},
			wantFw:    "cra",
			wantBuild: "react-scripts",
		},
		{
			name:      "angular",
			deps:      map[string]string{"@angular/core": "^17.0.0"},
			scripts:   map[string]string{"build": "ng build"},
			wantFw:    "angular",
			wantBuild: "angular-cli",
		},
		{
			name:   "plain react (no framework)",
			deps:   map[string]string{"react": "^18.0.0"},
			wantFw: "react",
		},
		{
			name:   "plain vue",
			deps:   map[string]string{"vue": "^3.0.0"},
			wantFw: "vue",
		},
		{
			name: "no deps",
			deps: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := packageJSONFull{Dependencies: tc.deps, Scripts: tc.scripts}
			fw, bt := detectFrontendFramework(pkg)
			if fw != tc.wantFw {
				t.Errorf("framework: got %q, want %q", fw, tc.wantFw)
			}
			if bt != tc.wantBuild {
				t.Errorf("buildTool: got %q, want %q", bt, tc.wantBuild)
			}
		})
	}
}

func TestExtractAPIURLs(t *testing.T) {
	kv := map[string]string{
		"NEXT_PUBLIC_API_URL":      "http://api.example.com",
		"NEXT_PUBLIC_WS_URL":       "ws://ws.example.com",
		"NEXT_PUBLIC_CDN_URL":      "https://cdn.example.com",
		"VUE_APP_API_BASE":         "http://vue-api.example.com",
		"VITE_API_ENDPOINT":        "http://vite-api.example.com",
		"REACT_APP_BACKEND_URL":    "http://react-api.example.com",
		"API_URL":                  "http://generic-api.example.com",
		"DB_HOST":                  "localhost",
		"NEXT_PUBLIC_ANALYTICS_ID": "UA-12345",
	}
	urls := extractAPIURLs(kv, "next")
	// 应该提取出以 http 开头 + 包含 API URL 关键词的
	if len(urls) < 4 {
		t.Errorf("expected >= 4 API URLs, got %d: %v", len(urls), urls)
	}
	// DB_HOST（非 http）和 ANALYTICS_ID（非 API 关键词）不应出现
	for _, u := range urls {
		if u == "localhost" || strings.Contains(u, "UA-") {
			t.Errorf("should not include %q", u)
		}
	}
}

func TestNodeAnalyzer_DotEnvConfigCenter(t *testing.T) {
	dir := t.TempDir()
	// 构造一个罕见场景：前端直连 Nacos
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test-fe"}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(`
NACOS_ADDR=nacos:8848
NACOS_DATA_ID=fe-config.yaml
NEXT_PUBLIC_API_URL=http://api.test.com
`), 0o644)
	a := NewNodeAnalyzerWithCC(CenterNacos)
	ra, err := a.Analyze(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 应同时提取 config center finding + API URL warning
	if len(ra.Findings) != 1 {
		t.Errorf("expected 1 finding (nacos from .env), got %d", len(ra.Findings))
	}
	hasAPI := false
	for _, w := range ra.Warnings {
		if strings.Contains(w, "api.test.com") {
			hasAPI = true
		}
	}
	if !hasAPI {
		t.Errorf("expected API URL in warnings: %v", ra.Warnings)
	}
}
