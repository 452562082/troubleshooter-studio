package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonAnalyzer_FakeRepo(t *testing.T) {
	a := NewPythonAnalyzer(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/python-service", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(ra.ServiceNames) != 1 || ra.ServiceNames[0] != "python-service" {
		t.Errorf("ServiceNames: got %v, want [python-service]", ra.ServiceNames)
	}
	// 框架识别
	hasFW := false
	for _, w := range ra.Warnings {
		if w == "python_framework=fastapi" {
			hasFW = true
		}
	}
	if !hasFW {
		t.Errorf("expected python_framework=fastapi in warnings: %v", ra.Warnings)
	}
	// .env + .env.production + settings.yaml → >= 3 findings
	if len(ra.Findings) < 3 {
		t.Fatalf("expected >= 3 findings, got %d", len(ra.Findings))
	}
	// 验证 .env.production 的 prod profile
	var prodFinding *Finding
	for i := range ra.Findings {
		if ra.Findings[i].EnvProfile == "prod" {
			prodFinding = &ra.Findings[i]
			break
		}
	}
	if prodFinding == nil {
		t.Fatal("expected finding with EnvProfile=prod")
	}
	if prodFinding.NamespaceID != "shop-prod" {
		t.Errorf("prod NamespaceID: got %q", prodFinding.NamespaceID)
	}
}

func TestPythonAnalyzer_NoConfigCenter(t *testing.T) {
	a := NewPythonAnalyzer("none")
	ra, err := a.Analyze("../../examples/fake-repos/python-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.ServiceNames) != 1 {
		t.Errorf("should still get name, got %v", ra.ServiceNames)
	}
	if len(ra.Findings) != 0 {
		t.Errorf("none should not scan, got %d findings", len(ra.Findings))
	}
}

func TestReadPyprojectName(t *testing.T) {
	cases := map[string]string{
		"[project]\nname = \"my-svc\"\nversion = \"1.0\"":     "my-svc",
		"[tool.poetry]\nname = 'poetry-svc'\nversion = \"1\"": "poetry-svc",
		"[project]\nversion = \"1.0\"\n":                      "",
		"no project section":                                  "",
	}
	for input, want := range cases {
		dir := t.TempDir()
		p := filepath.Join(dir, "pyproject.toml")
		_ = os.WriteFile(p, []byte(input), 0o644)
		got := readPyprojectName(p)
		if got != want {
			t.Errorf("readPyprojectName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReadSetupPyName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "setup.py")
	_ = os.WriteFile(p, []byte(`from setuptools import setup\nsetup(name="legacy-svc", version="1.0")`), 0o644)
	got := readSetupPyName(p)
	if got != "legacy-svc" {
		t.Errorf("got %q, want legacy-svc", got)
	}
}

func TestDetectPythonFramework(t *testing.T) {
	cases := []struct {
		name   string
		req    string
		manage bool
		want   string
	}{
		{"fastapi", "fastapi>=0.104\nuvicorn", false, "fastapi"},
		{"django", "Django>=4.0\n", false, "django"},
		{"django-managepy", "", true, "django"},
		{"flask", "flask>=3.0\n", false, "flask"},
		{"none", "requests>=2.0\n", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.req != "" {
				_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(tc.req), 0o644)
			}
			if tc.manage {
				_ = os.WriteFile(filepath.Join(dir, "manage.py"), []byte("# django"), 0o644)
			}
			got := detectPythonFramework(dir)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPythonAnalyzer_SettingsYAML(t *testing.T) {
	a := NewPythonAnalyzer(CenterNacos)
	ra, err := a.Analyze("../../examples/fake-repos/python-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 应该从 config/settings.yaml 抽到 YAML finding
	hasYAMLFinding := false
	for _, f := range ra.Findings {
		if strings.Contains(f.SourceFile, "settings.yaml") {
			hasYAMLFinding = true
			if f.DataID != "python-service-yaml.yaml" {
				t.Errorf("YAML finding dataId: got %q", f.DataID)
			}
		}
	}
	if !hasYAMLFinding {
		t.Error("expected finding from config/settings.yaml")
	}
}
