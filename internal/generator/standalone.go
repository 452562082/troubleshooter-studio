package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateStandalone 输出独立 Web 聊天格式：
//   - system-prompt.md（所有排障知识合并为一个 system prompt）
//   - skills/（映射表 + 脚本）
//   - scripts/（辅助脚本）
//   - server.py / index.html / requirements.txt / Dockerfile / docker-compose.yaml / install.sh / README.md
//     这些静态资产来自 templates/standalone/（.tmpl 走 text/template 渲染，其余直接拷贝）
func (g *Generator) GenerateStandalone() error {
	outDir := g.OutputDir + "-standalone"
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

	// 1) system-prompt.md — 合并 SOUL/IDENTITY/AGENTS/CHECKLIST/TOOLS + 所有 SKILL.md
	prompt, err := buildSystemPrompt(wsRoot, g.Ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "system-prompt.md"), []byte(prompt), 0o644); err != nil {
		return err
	}

	// 2) skills + scripts
	skillsDir := filepath.Join(wsRoot, "skills")
	if err := copyDirRecursive(skillsDir, filepath.Join(outDir, "skills")); err != nil {
		return err
	}
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return err
		}
	}

	// 3) 渲染 templates/standalone/ 下的静态资产（server.py / index.html / Dockerfile / ...）
	assetSrc := filepath.Join(g.TemplateRoot, "standalone")
	if _, err := os.Stat(assetSrc); err != nil {
		return fmt.Errorf("standalone asset dir missing: %w", err)
	}
	if err := g.walkAndRender(assetSrc, outDir); err != nil {
		return fmt.Errorf("standalone assets: %w", err)
	}

	// install.sh 需要可执行权限
	if p := filepath.Join(outDir, "install.sh"); fileExists(p) {
		if err := os.Chmod(p, 0o755); err != nil {
			return err
		}
	}

	if err := g.writeTshootMeta(outDir, "standalone"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

func buildSystemPrompt(wsRoot string, ctx *Context) (string, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s 排障机器人 System Prompt\n\n", ctx.System.Name)

	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(wsRoot, name)); err == nil {
			sb.Write(data)
			sb.WriteString("\n\n---\n\n")
		}
	}

	// 嵌入所有 skill 的 SKILL.md
	sb.WriteString("# Skills 详细说明\n\n")
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if data, err := os.ReadFile(skillMD); err == nil {
			fmt.Fprintf(&sb, "## skill: %s\n\n", e.Name())
			sb.Write(data)
			sb.WriteString("\n\n---\n\n")
		}
	}

	return sb.String(), nil
}

// anthropicDefaultModel 从 system.yaml 的 agent.model 里挑出一个可直接喂给
// anthropic SDK 的 model id。system.yaml 允许 "openai-codex/gpt-5.3-codex"
// 这类厂家前缀，但 standalone 的 server.py 目前只支持 Anthropic SDK，
// 所以非 anthropic 风格的值会回落到一个合理的默认。
// 用户始终可以通过 LLM_MODEL 环境变量覆盖。也作为模板函数暴露给 server.py.tmpl。
func anthropicDefaultModel(raw string) string {
	const fallback = "claude-sonnet-4-6"
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	if rest, ok := strings.CutPrefix(s, "anthropic/"); ok {
		s = rest
	}
	if strings.HasPrefix(s, "claude-") {
		return s
	}
	return fallback
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
