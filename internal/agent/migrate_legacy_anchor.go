// migrate_legacy_anchor.go —— 把老格式的 Claude Code / Cursor 锚点(只在 staging 中间包
// `~/.tshoot/<target>/<id>/tshoot.json`)迁移到新格式的真实部署位置
// (`~/.claude/skills/<name>/tshoot.json` / `~/.cursor/skills/<name>/tshoot.json`)。
//
// 触发时机:DiscoverBots 入口调一次。幂等 —— 已经迁移过 / 真实位置没真部署 / 用户手动
// 删了 staging 都安全 no-op。
//
// 必要性:2026-04-30 起 BotsPage 的 discover 改成扫真实部署位置,不再扫 staging。如果不
// 自动迁移,老用户(已经装过 Claude Code/Cursor 机器人,但 tshoot.json 还只在 staging)
// 会突然在 BotsPage 看不到自己装过的机器人。这次迁移把 staging 那份 tshoot.json **拷贝**
// 一份到真实位置。staging 不删(Apply 工作目录还要用)。
package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// MigrateLegacyAnchors 扫描老 staging 中间包,把 tshoot.json 锚点拷到真实部署位置。
// 返回成功迁移的条目数(已经迁移过的不计)。错误吞掉(每条独立处理),不影响 discover 主流程。
func MigrateLegacyAnchors() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	migrated := 0
	for _, target := range []string{"claude-code", "cursor", "codex"} {
		stagingRoot := filepath.Join(home, ".tshoot", target)
		entries, err := os.ReadDir(stagingRoot)
		if err != nil {
			continue
		}
		var realRoot string
		switch target {
		case "claude-code":
			realRoot = filepath.Join(home, ".claude")
		case "cursor":
			realRoot = filepath.Join(home, ".cursor")
		case "codex":
			realRoot = filepath.Join(home, ".codex")
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			stagingDir := filepath.Join(stagingRoot, e.Name())
			stagingMeta := filepath.Join(stagingDir, discover.MetaFilename)
			if _, err := os.Stat(stagingMeta); err != nil {
				continue // staging 没 tshoot.json 跳过
			}
			// 推断主 agent name,用来定位真实部署位置。
			agentName, err := readStagingPrimaryAgentName(stagingDir, target)
			if err != nil || agentName == "" {
				continue
			}
			// 真实部署位置必须有 agent.md(说明 native install 真跑过了),否则不迁移
			realAgentFile := filepath.Join(realRoot, "agents", agentName+userAgentExtForLegacyTarget(target))
			if _, err := os.Stat(realAgentFile); err != nil {
				continue // 真实位置没 agent 文件 → 这个 staging 是装失败 / 没装的草稿,别误标"已装"
			}
			// 真实 skills/<name>/tshoot.json 已经存在 → 已迁移过 / 是新装的,不重复操作
			realMeta := filepath.Join(realRoot, "skills", agentName, discover.MetaFilename)
			if _, err := os.Stat(realMeta); err == nil {
				continue
			}
			// 拷过去
			if err := os.MkdirAll(filepath.Dir(realMeta), 0o755); err != nil {
				continue
			}
			if err := copyFileSimple(stagingMeta, realMeta); err != nil {
				continue
			}
			migrated++
		}
	}
	return migrated
}

// readStagingPrimaryAgentName 跟 install_native.go 的主锚点选择保持一致:
// 优先 root/agents-meta 里的 troubleshooter,再从 agents 文件名兜底。
func readStagingPrimaryAgentName(stagingDir, target string) (string, error) {
	if name := primaryAgentNameFromStagingMeta(stagingDir); name != "" {
		return name, nil
	}
	entries, err := os.ReadDir(filepath.Join(stagingDir, "agents"))
	if err != nil {
		return "", err
	}
	ext := userAgentExtForLegacyTarget(target)
	var fallback string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ext) || strings.Contains(n, ".bak.") {
			continue
		}
		name := strings.TrimSuffix(n, ext)
		if strings.Contains(strings.ToLower(name), "troubleshooter") {
			return name, nil
		}
		if fallback == "" {
			fallback = name
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", os.ErrNotExist
}

func userAgentExtForLegacyTarget(target string) string {
	if target == "codex" {
		return ".toml"
	}
	return ".md"
}
