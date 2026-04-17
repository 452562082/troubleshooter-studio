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

	// 先渲染到临时目录
	tmpDir, err := os.MkdirTemp("", "factory-cursor-*")
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

	// 5) install.sh
	if err := os.WriteFile(filepath.Join(outDir, "install.sh"), []byte(cursorInstallSh(g.Ctx)), 0o755); err != nil {
		return err
	}

	return nil
}

func cursorInstallSh(ctx *Context) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# %s 排障机器人 — Cursor IDE 一键安装
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

# 1. .cursorrules —— 已存在则备份
if [ -f "$DST/.cursorrules" ]; then
  cp "$DST/.cursorrules" "$DST/.cursorrules.bak.$TS"
  echo "  · 已备份原 .cursorrules → .cursorrules.bak.$TS"
fi
cp "$SRC/.cursorrules" "$DST/.cursorrules"
echo "  · .cursorrules"

# 2. .cursor/rules/ —— 合并（同名 .mdc 覆盖，其余保留）
mkdir -p "$DST/.cursor/rules"
if [ -d "$SRC/.cursor/rules" ]; then
  for f in "$SRC/.cursor/rules/"*.mdc; do
    [ -f "$f" ] || continue
    cp "$f" "$DST/.cursor/rules/"
    echo "  · .cursor/rules/$(basename "$f")"
  done
fi

# 3. skills/ —— 合并（同名子目录整体覆盖，其余保留）
mkdir -p "$DST/skills"
for d in "$SRC/skills/"*/; do
  [ -d "$d" ] || continue
  name="$(basename "$d")"
  rm -rf "$DST/skills/$name"
  cp -R "$d" "$DST/skills/$name"
  echo "  · skills/$name"
done

# 4. scripts/ —— 合并
if [ -d "$SRC/scripts" ]; then
  mkdir -p "$DST/scripts"
  cp -R "$SRC/scripts/"* "$DST/scripts/" 2>/dev/null || true
  echo "  · scripts/"
fi

echo ""
echo "✓ 安装完成。用 Cursor 打开 $DST 即可加载规则。"
`, ctx.System.Name)
}

func buildCursorRules(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s 排障机器人\n\n", ctx.System.Name))
	sb.WriteString("# 由 troubleshooter-factory 生成，目标平台：Cursor\n")
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
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "description:") {
					desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					break
				}
			}
		}
		sb.WriteString(fmt.Sprintf("- **%s** — %s\n", e.Name(), desc))
	}

	return sb.String(), nil
}
