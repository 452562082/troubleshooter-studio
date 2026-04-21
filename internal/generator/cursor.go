package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateCursor 输出 Cursor IDE 格式：
//   - .cursorrules（合并排障知识，Cursor 自动读取）
//   - .cursor/rules/（每个 skill 单独一个 .mdc 文件，Cursor project rules）
//   - skills/（映射表 + 脚本）
//   - scripts/（辅助脚本）
//   - install.sh（把上述产物一键安装到指定项目根）
func (g *Generator) GenerateCursor() error {
	outDir := g.OutputDir + "-cursor"
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	wsRoot, cleanup, err := g.resolveWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()

	// 1) 生成 .cursorrules（核心排障知识合并）
	cursorRules, err := buildCursorRules(wsRoot, g.Ctx)
	if err != nil {
		return fmt.Errorf("build .cursorrules: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, ".cursorrules"), []byte(cursorRules), 0o644); err != nil {
		return err
	}

	// 2) 生成 .cursor/rules/ 目录（每个 skill 一个 .mdc 文件）
	rulesDir := filepath.Join(outDir, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return err
	}
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMDPath := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillMDPath)
		if err != nil {
			continue
		}
		// Cursor rules 用 .mdc 格式（Markdown 兼容）
		mdcPath := filepath.Join(rulesDir, e.Name()+".mdc")
		if err := os.WriteFile(mdcPath, data, 0o644); err != nil {
			return err
		}
	}

	// 3) 拷贝 skills/（含 references 映射表）
	if err := copyDirRecursive(skillsDir, filepath.Join(outDir, "skills")); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	// 4) 拷贝辅助脚本
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// 5) install.sh —— 从 templates/cursor/install.sh.tmpl 渲染
	installSrc := filepath.Join(g.TemplateRoot, "cursor", "install.sh.tmpl")
	if err := g.renderFile(installSrc, filepath.Join(outDir, "install.sh")); err != nil {
		return fmt.Errorf("install.sh: %w", err)
	}
	if err := os.Chmod(filepath.Join(outDir, "install.sh"), 0o755); err != nil {
		return err
	}

	return nil
}

func buildCursorRules(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("# 由 troubleshooter-studio 生成，目标平台：Cursor\n")
	sb.WriteString("# 本文件会被 Cursor 自动读取作为项目上下文\n\n")

	// 读取各 MD 文件合并
	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(wsRoot, name)); err == nil {
			sb.WriteString("---\n\n")
			sb.Write(data)
			sb.WriteString("\n\n")
		}
	}

	// Skills 索引
	sb.WriteString("---\n\n## 可用 Skills\n\n")
	sb.WriteString("详细规则见 `.cursor/rules/` 目录下的 .mdc 文件，映射表和脚本见 `skills/` 目录。\n\n")

	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		desc := ""
		if data, err := os.ReadFile(skillMD); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if rest, ok := strings.CutPrefix(line, "description:"); ok {
					desc = strings.TrimSpace(rest)
					break
				}
			}
		}
		fmt.Fprintf(&sb, "- **%s** — %s\n", e.Name(), desc)
	}

	return sb.String(), nil
}
