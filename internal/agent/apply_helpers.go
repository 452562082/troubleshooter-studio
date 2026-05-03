// apply_helpers.go —— Apply / ImportAndApply 的工具函数:
//   - resolveApplySource:按 target 选 staging 子树 + 重启提示
//   - listRel / inList:rsync diff 用的相对路径列表 + 集合包含
//   - looksLikeFactoryArtifact:判断文件是 tshoot 生成的(可清)还是用户手工放的(保留)
//   - copyFile:保留 mode 的整文件拷贝
//   - writeTSFMeta:tshoot.json 锚点写入
package agent

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// resolveApplySource 按 target 算出"应用源"(staging 里对应的产物子树)和重启提示。
func resolveApplySource(baseOut, target string) (src, hint string) {
	switch target {
	case "openclaw":
		// agent.Path 是 ~/.openclaw/workspace/<name>/;对应产物根下的 templates/workspace-template/
		src = filepath.Join(baseOut, "templates", "workspace-template")
		hint = "若新增了 env / 切了配置中心类型,回 BotsPage 重跑一次部署(走 InstallNativeOpenclaw 重新注册 MCP + 收凭证),再 `openclaw gateway restart`;只改映射不用动。"
	case "claude-code":
		src = baseOut + "-claude-code"
		hint = "Claude Code 下次启动会自动加载用户级 ~/.claude/agents/<name>.md;正在开的 session 需要 `/clear` 或重启 `claude` CLI 才能吃到新版 subagent。"
	case "cursor":
		src = baseOut + "-cursor"
		hint = "Cursor 下次打开 AI 侧栏时会重新扫 ~/.cursor/agents/<name>.md;新建对话即可选到更新后的 Custom Agent。"
	case "codex":
		src = baseOut + "-codex"
		hint = "Codex CLI 下次启动会读 ~/.codex/agents/<name>.md;正在开的 session 需要 `/clear` 或重启才能吃到新版 agent。"
	}
	return
}

// listRel 返回 root 下所有文件的相对路径(跳过空目录)。
func listRel(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	return out, err
}

func inList(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// looksLikeFactoryArtifact 判断 workspace 下某个文件是 tshoot 一开始生成的(可以 remove)
// 还是用户手工放的(不要乱动)。按 target 区分管辖面:不同 target 产物结构不一样。
func looksLikeFactoryArtifact(rel, target string) bool {
	common := []string{"skills/", "scripts/"}
	var prefixes []string
	switch target {
	case "openclaw":
		prefixes = append(prefixes, "SOUL.md", "IDENTITY.md", "AGENTS.md", "USER.md",
			"CHECKLIST.md", "TOOLS.md", ".clawhub/")
	case "claude-code", "cursor", "codex":
		prefixes = append(prefixes, "agents/")
	default:
		return false
	}
	prefixes = append(prefixes, common...)
	for _, p := range prefixes {
		if strings.HasPrefix(rel, p) || rel == strings.TrimSuffix(p, "/") {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(src); err == nil {
		mode = info.Mode()
	}
	return os.WriteFile(dst, data, mode)
}

func writeTSFMeta(dir, target string, cfg *config.SystemConfig, yamlSrc []byte, version string) error {
	meta := map[string]any{
		"schema_version": 1,
		"tshoot_version": version,
		"system_id":      cfg.System.ID,
		"system_name":    cfg.System.Name,
		"target":         target,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"system_yaml":    string(yamlSrc),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, discover.MetaFilename), append(data, '\n'), 0o644)
}
