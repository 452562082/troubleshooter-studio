// role_hint_python.go —— Python 仓库角色推断:requirements.txt / pyproject.toml / Pipfile / setup.py。
//   含 fastapi / flask / django / sanic / aiohttp → backend
//   只有 setup.py + 无 web 框架 → common-lib
//   含 scrapy(爬虫)/ celery(异步任务)→ middleware
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

func roleFromPython(repoPath string) RoleHint {
	check := func(p string) string {
		data, err := os.ReadFile(filepath.Join(repoPath, p))
		if err != nil {
			return ""
		}
		return strings.ToLower(string(data))
	}
	combined := check("requirements.txt") + "\n" + check("pyproject.toml") + "\n" + check("Pipfile") + "\n" + check("setup.py")
	if combined == "\n\n\n" {
		return RoleHint{}
	}

	if strings.Contains(combined, "scrapy") {
		return RoleHint{Role: "middleware", Reason: "含 scrapy(爬虫 worker)"}
	}
	if strings.Contains(combined, "celery") {
		return RoleHint{Role: "middleware", Reason: "含 celery(异步任务)"}
	}
	for _, fw := range []string{"fastapi", "flask", "django", "sanic", "aiohttp", "tornado", "starlette", "bottle"} {
		if strings.Contains(combined, fw) {
			return RoleHint{Role: "backend", Reason: "Python 后端框架: " + fw}
		}
	}
	if _, err := os.Stat(filepath.Join(repoPath, "setup.py")); err == nil {
		return RoleHint{Role: "common-lib", Reason: "setup.py 存在但无 web 框架,看着是 Python 包"}
	}
	if _, err := os.Stat(filepath.Join(repoPath, "pyproject.toml")); err == nil {
		return RoleHint{Role: "common-lib", Reason: "pyproject.toml 但无 web 框架"}
	}
	return RoleHint{}
}
