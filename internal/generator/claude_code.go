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

	// 1) 先用现有逻辑渲染到临时目录（复用所有模板）
	tmpDir, err := os.MkdirTemp("", "factory-cc-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	origOut := g.OutputDir
	g.OutputDir = tmpDir
	if err := g.Generate(); err != nil {
		g.OutputDir = origOut
		return fmt.Errorf("render templates: %w", err)
	}
	g.OutputDir = origOut

	wsRoot := filepath.Join(tmpDir, "templates", "workspace-template")

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

	// 5) install.sh
	if err := os.WriteFile(filepath.Join(outDir, "install.sh"), []byte(claudeCodeInstallSh(g.Ctx)), 0o755); err != nil {
		return err
	}

	return nil
}

func claudeCodeInstallSh(ctx *Context) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# %s 排障机器人 — Claude Code 一键安装
# 由 troubleshooter-factory 生成
#
# 用法：
#   bash install.sh [target-project-dir]
#   bash install.sh ~/code/my-app
#   bash install.sh              # 默认当前目录
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
DST="${1:-.}"

if [ ! -d "$DST" ]; then
  echo "错误：目标目录不存在：$DST" >&2
  exit 1
fi

DST="$(cd "$DST" && pwd)"
TS="$(date +%%Y%%m%%d-%%H%%M%%S)"

echo "→ 安装到：$DST"

# 1. CLAUDE.md —— 已存在则备份
if [ -f "$DST/CLAUDE.md" ]; then
  cp "$DST/CLAUDE.md" "$DST/CLAUDE.md.bak.$TS"
  echo "  · 已备份原 CLAUDE.md → CLAUDE.md.bak.$TS"
fi
cp "$SRC/CLAUDE.md" "$DST/CLAUDE.md"
echo "  · CLAUDE.md"

# 2. skills/ —— 合并（同名子目录整体覆盖，其余保留）
mkdir -p "$DST/skills"
for d in "$SRC/skills/"*/; do
  [ -d "$d" ] || continue
  name="$(basename "$d")"
  rm -rf "$DST/skills/$name"
  cp -R "$d" "$DST/skills/$name"
  echo "  · skills/$name"
done

# 3. scripts/ —— 合并
if [ -d "$SRC/scripts" ]; then
  mkdir -p "$DST/scripts"
  cp -R "$SRC/scripts/"* "$DST/scripts/" 2>/dev/null || true
  echo "  · scripts/"
fi

echo ""
echo "✓ 安装完成。在 $DST 下执行 claude 即可加载机器人。"
`, ctx.System.Name)
}

func buildClaudeMD(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s 排障机器人\n\n", ctx.System.Name))
	sb.WriteString(fmt.Sprintf("> 由 troubleshooter-factory 生成，目标平台：Claude Code\n\n"))

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
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "description:") {
					desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					break
				}
			}
		}
		sb.WriteString(fmt.Sprintf("- **%s** — %s\n", e.Name(), desc))
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
