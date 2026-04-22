package main

import (
	"flag"
	"fmt"
	"os"

	"encoding/json"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	reposRoot := fs.String("repos-root", "", "仓库 checkout 根目录 (必填)；每个仓库在其下以 repos[].name 命名")
	output := fs.String("o", "analysis.json", "输出文件路径")
	autoClone := fs.Bool("auto-clone", false, "缺仓库时自动 shallow clone (需要 git + 凭证)")
	cloneBranch := fs.String("branch", "", "auto-clone 时的分支名 (默认:第一个环境对应分支 > 仓库默认分支)")
	format := fs.String("format", "text", "text / json (stdout 摘要的格式，不影响 -o 输出文件)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *reposRoot == "" {
		fs.Usage()
		return fmt.Errorf("-i and --repos-root are required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return err
	}

	text := *format != "json"
	var onProgress func(string)
	if text {
		onProgress = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}
	result, err := analyzerpipe.Run(cfg, analyzerpipe.Options{
		ReposRoot:   *reposRoot,
		AutoClone:   *autoClone,
		CloneBranch: *cloneBranch,
		OnProgress:  onProgress,
	})
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(result.Report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		return err
	}

	if *format == "json" {
		summary := map[string]any{
			"output":        *output,
			"config_center": cfg.Infrastructure.ConfigCenter.Type,
			"repos":         result.PerRepo,
		}
		out, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("[ok] analysis written to %s\n", *output)
		fmt.Printf("下一步：tshoot plan -i %s --analysis %s   # 预览 findings 如何应用\n", *input, *output)
		fmt.Printf("     或：tshoot gen  -i %s --analysis %s   # 直接应用并落盘\n", *input, *output)
	}
	return nil
}
