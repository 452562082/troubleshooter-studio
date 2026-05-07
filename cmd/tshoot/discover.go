package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"encoding/json"

	"github.com/xiaolong/troubleshooter-studio/internal/aitools"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	rootsFlag := fs.String("roots", "", "逗号分隔的额外扫描路径，追加到默认 roots 后")
	format := fs.String("format", "text", "text | json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	roots := discover.DefaultRoots()
	if *rootsFlag != "" {
		for r := range strings.SplitSeq(*rootsFlag, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				roots = append(roots, r)
			}
		}
	}

	bots, err := discover.Scan(roots)
	if err != nil {
		return err
	}
	enrichBotsWithIDEStatus(bots)
	bots = appendGhostBots(bots)

	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(bots)
	}

	if len(bots) == 0 {
		fmt.Printf("没发现已装机器人（扫描的路径：%s）\n", strings.Join(roots, ", "))
		fmt.Println("可能原因：1) 没装过；2) 装到别处（用 --roots 指定扫描根）")
		return nil
	}

	fmt.Printf("发现 %d 个已装机器人\n\n", len(bots))
	fmt.Printf("%-20s  %-12s  %-12s  %3s envs  %3s repos  %3s skills  %-7s  %s\n",
		"SYSTEM_ID", "TARGET", "MODIFIED", " ", " ", " ", "STATUS", "PATH")
	for _, b := range bots {
		fmt.Printf("%-20s  %-12s  %-12s  %5d      %5d       %5d      %-7s  %s\n",
			b.Meta.SystemID, b.Meta.Target,
			strings.Split(b.ModTime, " ")[0], // 只显示日期部分
			b.EnvCount, b.RepoCount, b.SkillCount, statusLabel(b), b.Path)
	}
	return nil
}

// enrichBotsWithIDEStatus 跟桌面 bindings_repo.go DiscoverBots 同款逻辑:一次性 detect
// 三家 IDE,for 每个 bot 按 target 查表填 IDEAvailable。openclaw 始终视为 available。
//
// 不抽到 internal/discover 是因为 discover 不依赖 aitools(单向依赖,纯 Scan 不掺探测);
// 这层 enrichment 由调用方(CLI / 桌面 binding)各自做。
func enrichBotsWithIDEStatus(bots []discover.DiscoveredAgent) {
	if len(bots) == 0 {
		return
	}
	ideInstalled := detectIDEInstalled()
	for i := range bots {
		bots[i].IDEAvailable = ideInstalled[bots[i].Meta.Target]
	}
}

// appendGhostBots 把 ~/.tshoot/config.json deployed_bots 里有但 scan 没找到的
// 追加为 ghost=true 占位。跟桌面 bindings_repo.go DiscoverBots 同款语义。
// IDEAvailable 也按对应 target 填,允许 CLI 用户区分"被外部删了" vs "IDE 卸了"。
func appendGhostBots(bots []discover.DiscoveredAgent) []discover.DiscoveredAgent {
	seen := map[string]bool{}
	for _, b := range bots {
		seen[userconfig.DeployedBotKey(b.Meta.SystemID, b.Meta.Target)] = true
	}
	ghosts := userconfig.ListDeployedBots()
	if len(ghosts) == 0 {
		return bots
	}
	ideInstalled := detectIDEInstalled()
	for key, entry := range ghosts {
		if seen[key] {
			continue
		}
		bots = append(bots, discover.DiscoveredAgent{
			Meta: discover.Meta{
				SystemID:   entry.SystemID,
				SystemName: entry.SystemName,
				Target:     entry.Target,
			},
			Path:         entry.Path,
			IDEAvailable: ideInstalled[entry.Target],
			Ghost:        true,
		})
	}
	return bots
}

// detectIDEInstalled 一次性探测三家 IDE 安装状态,返回 target → bool 表。
// openclaw 始终 true(产品自带,不靠探测)。enrichBotsWithIDEStatus + appendGhostBots
// 各自调一次（共两次）—— 一次 discover 内 ~6 进程 spawn,可接受;cache 化留给后续真有
// 性能需求再做。
func detectIDEInstalled() map[string]bool {
	return map[string]bool{
		"openclaw":    true,
		"claude-code": aitools.DetectClaudeCode().Installed,
		"cursor":      aitools.DetectCursor().Installed,
		"codex":       aitools.DetectCodex().Installed,
	}
}

// statusLabel 给文本输出渲染单 bot 的状态:OK / NO-IDE(ide 没了)/ GHOST(目录没了)。
// JSON 输出走结构化字段 IDEAvailable + Ghost 自取。
func statusLabel(b discover.DiscoveredAgent) string {
	if b.Ghost {
		return "GHOST"
	}
	if !b.IDEAvailable {
		return "NO-IDE"
	}
	return "OK"
}

// ── apply：用新 yaml 应用到已装机器人 ────────────────────────────
// tshoot apply -i <new.yaml> --path <agent-path> [--dry-run] [--format text|json]
// 流程：读 new yaml → 根据 agent.Path 的 tshoot.json 识别 target → 重 render → rsync 回 path
//
//	模板派生文件按最新模板覆盖;config-map 中 status=verified 且无 source 的人工行保留。
