package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"encoding/json"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

func runApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	input := fs.String("i", "", "新 system.yaml 路径（必填）")
	path := fs.String("path", "", "目标已装机器人的 workspace 路径（必填，含 tshoot.json 的那一层）")
	tmplDir := fs.String("t", "", "模板根（默认：可执行文件旁的 templates/ 或 embed 解压）")
	dryRun := fs.Bool("dry-run", false, "只预演不写盘")
	format := fs.String("format", "text", "text | json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *path == "" {
		fs.Usage()
		return fmt.Errorf("-i and --path required")
	}

	yamlBytes, err := os.ReadFile(*input)
	if err != nil {
		return fmt.Errorf("read %s: %w", *input, err)
	}

	// 从 path 回读 agent：用 Scan 当 1 层扫描即可拿到 DiscoveredAgent
	found, err := discover.Scan([]string{*path})
	if err != nil {
		return fmt.Errorf("scan %s: %w", *path, err)
	}
	if len(found) == 0 {
		return fmt.Errorf("no tshoot.json under %s（确认路径对）", *path)
	}
	// 最短路径那个（最贴近 path 根）
	ag := found[0]
	for _, cand := range found[1:] {
		if len(cand.Path) < len(ag.Path) {
			ag = cand
		}
	}

	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}

	res, err := agent.Apply(ag, agent.ApplyOptions{
		NewYAML:       yamlBytes,
		TemplateRoot:  tr,
		TshootVersion: version,
		DryRun:        *dryRun,
	})
	if err != nil {
		return err
	}

	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	verb := "应用"
	if *dryRun {
		verb = "预演"
	}
	fmt.Printf("%s完成：%s (%s)\n", verb, res.AgentPath, res.Target)
	fmt.Printf("  写入文件：%d\n", res.FilesWritten)
	if len(res.FilesPreserved) > 0 {
		fmt.Printf("  保留（用户手改）：%s\n", strings.Join(res.FilesPreserved, ", "))
	}
	if len(res.FilesRemoved) > 0 {
		fmt.Printf("  移除（陈旧产物）：%s\n", strings.Join(res.FilesRemoved, ", "))
	}
	if res.NeedsRestartHint != "" {
		fmt.Printf("\n💡 %s\n", res.NeedsRestartHint)
	}
	return nil
}
