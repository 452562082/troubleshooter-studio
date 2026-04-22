package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateStandalone 输出"桌面端内嵌对话"用的素材集合。
//
// 历史:早期 standalone 还走 Flask + Docker 独立部署路径,会生成 server.py /
// index.html / Dockerfile / docker-compose.yaml / install.sh / README.md /
// requirements.txt 一整套。从"桌面端原生 chat"上线(见 internal/llmchat)后,
// 独立部署不再维护 —— 删了 templates/standalone/ 下的所有部署文件,现在 standalone
// 产物就是:
//   - system-prompt.md  —— 合并所有 SOUL/IDENTITY/AGENTS/CHECKLIST/TOOLS + skill 知识
//   - skills/           —— 路由 / 映射表 / SKILL.md(内嵌对话的 prompt 拼接源)
//   - scripts/          —— config-executor 等辅助脚本(桌面端当前不跑它们,
//     保留是因为 system-prompt 里引用得到,删了会报"找不到")
//   - tshoot.json       —— discover 锚点,让 Studio 扫得到这台机器人
//
// Studio 内嵌 chat 读 system-prompt.md + 直连 LLM(llmchat 包)直接对话。
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

	// 1) system-prompt.md —— 合并 SOUL/IDENTITY/AGENTS/CHECKLIST/TOOLS + 所有 SKILL.md
	prompt, err := buildSystemPrompt(wsRoot, g.Ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "system-prompt.md"), []byte(prompt), 0o644); err != nil {
		return err
	}

	// 2) skills + scripts(scripts 拷一份,system-prompt 可能引用到)
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

	// 3) tshoot.json 锚点,让 discover 扫得到
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

// anthropicDefaultModel 原本是 server.py 里 Python 兼容层的 model id 归一器,
// server.py 删后只剩一个残留引用点:generator/funcs.go 里作为模板函数 anthropicModel
// 暴露给可能还存在的 templates/standalone/README.md.tmpl 等。但 README.md.tmpl 已删,
// 现在没有调用方。保留一个薄 pass-through 避免 funcs.go 里模板 map 报 undefined —— 等
// generator/funcs.go 清掉 anthropicModel 注册后这里也能删。
func anthropicDefaultModel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "anthropic/claude-sonnet-4-6"
	}
	return s
}
