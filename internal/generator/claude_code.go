package generator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GenerateClaudeCode 输出 Claude Code 格式：
//   - CLAUDE.md（合并 SOUL + IDENTITY + AGENTS + CHECKLIST + TOOLS）
//   - skills/（原样保留所有 skill 目录）
//   - scripts/（辅助脚本保留）
//   - install.sh（把上述产物一键安装到指定项目根）
//
// 不生成 self-test.sh / uninstall.sh / .clawhub 等 OpenClaw 特有文件
func (g *Generator) GenerateClaudeCode() error {
	outDir := g.OutputDir + "-claude-code"
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

	// 2) 合并 CLAUDE.md
	claudeMD, err := buildClaudeMD(wsRoot, g.Ctx)
	if err != nil {
		return fmt.Errorf("build CLAUDE.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil {
		return err
	}

	// 3) 拷贝 skills/
	skillsSrc := filepath.Join(wsRoot, "skills")
	skillsDst := filepath.Join(outDir, "skills")
	if err := copyDirRecursive(skillsSrc, skillsDst); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	// 4) 拷贝辅助脚本到 scripts/（resolve_runtime 等）
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	scriptsDst := filepath.Join(outDir, "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, scriptsDst); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// 5) install.sh —— 从 templates/claude-code/install.sh.tmpl 渲染
	installSrc := filepath.Join(g.TemplateRoot, "claude-code", "install.sh.tmpl")
	if err := g.renderFile(installSrc, filepath.Join(outDir, "install.sh")); err != nil {
		return fmt.Errorf("install.sh: %w", err)
	}
	if err := os.Chmod(filepath.Join(outDir, "install.sh"), 0o755); err != nil {
		return err
	}

	return nil
}

func buildClaudeMD(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s 排障机器人\n\n", ctx.System.Name)
	sb.WriteString("> 由 troubleshooter-factory 生成，目标平台：Claude Code\n\n")

	// SOUL
	if data, err := os.ReadFile(filepath.Join(wsRoot, "SOUL.md")); err == nil {
		sb.WriteString("---\n\n")
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	// IDENTITY
	if data, err := os.ReadFile(filepath.Join(wsRoot, "IDENTITY.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	// AGENTS（工作原则 + 故障快报模板）
	if data, err := os.ReadFile(filepath.Join(wsRoot, "AGENTS.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	// CHECKLIST
	if data, err := os.ReadFile(filepath.Join(wsRoot, "CHECKLIST.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	// TOOLS
	if data, err := os.ReadFile(filepath.Join(wsRoot, "TOOLS.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	// Skills 索引
	sb.WriteString("---\n\n## Skills 索引\n\n")
	sb.WriteString("以下 skills 目录包含排障所需的映射表、脚本和执行流程文档。\n")
	sb.WriteString("排障时请按 SKILL.md 中的执行流程操作，不要跳过步骤。\n\n")

	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		desc := ""
		if data, err := os.ReadFile(skillMD); err == nil {
			// 从 front matter 提取 description
			for line := range strings.SplitSeq(string(data), "\n") {
				if rest, ok := strings.CutPrefix(line, "description:"); ok {
					desc = strings.TrimSpace(rest)
					break
				}
			}
		}
		fmt.Fprintf(&sb, "- **%s** — %s\n", e.Name(), desc)
	}
	sb.WriteString("\n详细用法见各 skill 目录下的 `SKILL.md`。\n")

	return sb.String(), nil
}

func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, _ := d.Info()
		mode := os.FileMode(0o644)
		if info != nil {
			mode = info.Mode()
		}
		return os.WriteFile(target, data, mode)
	})
}
