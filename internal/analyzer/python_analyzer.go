package analyzer

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type pythonAnalyzer struct {
	configCenter string
}

func NewPythonAnalyzer(configCenter string) Analyzer {
	return &pythonAnalyzer{configCenter: configCenter}
}

func (pythonAnalyzer) Stack() string { return "python" }

func (p pythonAnalyzer) Analyze(repoPath string, includePaths []string) (*RepoAnalysis, error) {
	ra := &RepoAnalysis{
		Name:     filepath.Base(repoPath),
		Stack:    "python",
		RepoPath: repoPath,
		Verified: true,
	}

	// 1) service name: pyproject.toml > setup.py > 目录名
	if name := readPyprojectName(filepath.Join(repoPath, "pyproject.toml")); name != "" {
		ra.ServiceNames = append(ra.ServiceNames, name)
	} else if name := readSetupPyName(filepath.Join(repoPath, "setup.py")); name != "" {
		ra.ServiceNames = append(ra.ServiceNames, name)
	}
	if len(ra.ServiceNames) == 0 {
		ra.Warnings = append(ra.Warnings, "pyproject.toml/setup.py not found or name empty")
	}

	// 2) 框架识别
	framework := detectPythonFramework(repoPath)
	if framework != "" {
		ra.Warnings = append(ra.Warnings, "python_framework="+framework)
	}

	if p.configCenter == "" || p.configCenter == "none" {
		return ra, nil
	}

	// 3) .env 扫描（Django/FastAPI 都常用 python-dotenv）
	envFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return strings.HasPrefix(base, ".env")
	})
	for _, f := range envFiles {
		rel, _ := filepath.Rel(repoPath, f)
		finding, err := ScanDotEnv(f, rel, p.configCenter)
		if err != nil {
			ra.Warnings = append(ra.Warnings, "scan "+rel+": "+err.Error())
			continue
		}
		if finding != nil {
			ra.Findings = append(ra.Findings, *finding)
		}
	}

	// 4) YAML 配置扫描（config.yaml / settings.yaml 等）
	yamlFiles, _ := walkFiles(repoPath, includePaths, func(rel string) bool {
		low := strings.ToLower(rel)
		return (strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml")) &&
			!strings.Contains(low, "venv/") && !strings.Contains(low, ".tox/") &&
			!strings.Contains(low, "__pycache__/")
	})
	for _, f := range yamlFiles {
		rel, _ := filepath.Rel(repoPath, f)
		finding, err := ScanYAML(f, rel, p.configCenter)
		if err != nil {
			ra.Warnings = append(ra.Warnings, "scan "+rel+": "+err.Error())
			continue
		}
		if finding != nil {
			if prof := extractEnvProfile(rel); prof != "" {
				finding.EnvProfile = prof
			}
			ra.Findings = append(ra.Findings, *finding)
		}
	}

	return ra, nil
}

// pyproject.toml 极简解析：取 [project] 下的 name = "xxx"
// 不引入 TOML 解析库，用正则 line-by-line
var pyprojectNameRe = regexp.MustCompile(`^name\s*=\s*["']([^"']+)["']`)

func readPyprojectName(path string) string {
	if !fileExists(path) {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	inProject := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "[project]" || line == "[tool.poetry]" {
			inProject = true
			continue
		}
		if strings.HasPrefix(line, "[") && inProject {
			break
		}
		if inProject {
			if m := pyprojectNameRe.FindStringSubmatch(line); len(m) == 2 {
				return m[1]
			}
		}
	}
	return ""
}

// setup.py 极简解析：找 name="xxx" 或 name='xxx'
var setupPyNameRe = regexp.MustCompile(`name\s*=\s*["']([^"']+)["']`)

func readSetupPyName(path string) string {
	if !fileExists(path) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := setupPyNameRe.FindSubmatch(data); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func detectPythonFramework(repoPath string) string {
	markers := []struct {
		file      string
		dep       string
		framework string
	}{
		{"", "django", "django"},
		{"", "fastapi", "fastapi"},
		{"", "flask", "flask"},
		{"", "tornado", "tornado"},
		{"", "sanic", "sanic"},
	}

	// 扫 requirements.txt / pyproject.toml / Pipfile 里的依赖名
	depFiles := []string{
		filepath.Join(repoPath, "requirements.txt"),
		filepath.Join(repoPath, "pyproject.toml"),
		filepath.Join(repoPath, "Pipfile"),
	}
	depContent := ""
	for _, f := range depFiles {
		if data, err := os.ReadFile(f); err == nil {
			depContent += "\n" + strings.ToLower(string(data))
		}
	}
	for _, m := range markers {
		if strings.Contains(depContent, m.dep) {
			return m.framework
		}
	}

	// 文件标记
	if fileExists(filepath.Join(repoPath, "manage.py")) {
		return "django"
	}
	return ""
}
