package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	tshoot "github.com/xiaolong/troubleshooter-studio"
)

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// resolveTemplateDir 按优先级定位 templates 根目录：
//  1. 可执行文件旁的 templates/
//  2. 当前工作目录下的 templates/
//  3. 以上都不在（例如 `go install` 出来的二进制 + 在任意目录跑）→ 从 embed.FS
//     extract 到一个每进程复用的临时目录
func resolveTemplateDir() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "templates")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(wd, "templates")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// embed fallback
	if dir, err := extractEmbeddedTemplates(); err == nil {
		return dir
	}
	// 最后兜底，返回一个不存在的路径，让调用方的 os.Stat 报清晰错误
	wd, _ := os.Getwd()
	return filepath.Join(wd, "templates")
}

// resolveExamplesDir 对齐 resolveTemplateDir，但负责 examples/ 根目录。
func resolveExamplesDir() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "examples")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(wd, "examples")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if dir, err := extractEmbeddedExamples(); err == nil {
		return dir
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "examples")
}

// extract 缓存：同一进程 extract 一次、复用；避免 serve 场景每次请求都 extract
var (
	embeddedTemplatesDir string
	embeddedExamplesDir  string
)

func extractEmbeddedTemplates() (string, error) {
	if embeddedTemplatesDir != "" {
		if _, err := os.Stat(embeddedTemplatesDir); err == nil {
			return embeddedTemplatesDir, nil
		}
	}
	dir, err := extractEmbeddedFS(tshoot.TemplatesFS, "templates", "tshoot-templates-")
	if err != nil {
		return "", err
	}
	embeddedTemplatesDir = dir
	return dir, nil
}

func extractEmbeddedExamples() (string, error) {
	if embeddedExamplesDir != "" {
		if _, err := os.Stat(embeddedExamplesDir); err == nil {
			return embeddedExamplesDir, nil
		}
	}
	dir, err := extractEmbeddedFS(tshoot.ExamplesFS, "examples", "tshoot-examples-")
	if err != nil {
		return "", err
	}
	embeddedExamplesDir = dir
	return dir, nil
}

// extractEmbeddedFS 把 embed.FS 里 rootSubdir 下的所有文件写到一个新的 tmp 目录，返回该目录路径。
// 跳过 .DS_Store 之类的隐藏系统文件。
func extractEmbeddedFS(fsys embed.FS, rootSubdir, tmpPrefix string) (string, error) {
	tmp, err := os.MkdirTemp("", tmpPrefix+"*")
	if err != nil {
		return "", err
	}
	err = fs.WalkDir(fsys, rootSubdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(rootSubdir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if strings.HasSuffix(rel, ".DS_Store") {
			return nil
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") || strings.HasSuffix(rel, ".py") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("extract embed %s: %w", rootSubdir, err)
	}
	return tmp, nil
}

// ── discover：扫本机已装机器人 ───────────────────────────────────
// tshoot discover [--roots <path1>,<path2>] [--format text|json]
// 默认扫 ~/.openclaw/workspace + CWD(discover.DefaultRoots 的语义)。
