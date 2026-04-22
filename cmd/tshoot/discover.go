package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"encoding/json"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
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
		for _, r := range strings.Split(*rootsFlag, ",") {
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
	fmt.Printf("%-20s  %-12s  %-12s  %3s envs  %3s repos  %3s skills  %s\n",
		"SYSTEM_ID", "TARGET", "MODIFIED", " ", " ", " ", "PATH")
	for _, b := range bots {
		fmt.Printf("%-20s  %-12s  %-12s  %5d      %5d       %5d      %s\n",
			b.Meta.SystemID, b.Meta.Target,
			strings.Split(b.ModTime, " ")[0], // 只显示日期部分
			b.EnvCount, b.RepoCount, b.SkillCount, b.Path)
	}
	return nil
}

// ── apply：用新 yaml 应用到已装机器人 ────────────────────────────
// tshoot apply -i <new.yaml> --path <agent-path> [--dry-run] [--format text|json]
// 流程：读 new yaml → 根据 agent.Path 的 tshoot.json 识别 target → 重 render → rsync 回 path
//
//	preserve_on_regenerate 里的用户手改自动保留。
