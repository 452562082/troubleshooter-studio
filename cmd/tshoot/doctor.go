package main

import (
	"flag"
	"fmt"
	"strings"

	"encoding/json"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/doctor"
)

func runDoctor(args []string) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	input := fs.String("i", "", "troubleshooter.yaml 路径 (必填)")
	reposRoot := fs.String("repos-root", "", "仓库 checkout 根目录 (可选，留空则只做静态检查)")
	format := fs.String("format", "text", "text / json")
	fix := fs.Bool("fix", false, "对机器可修复的 issue 生成 yaml patch，显示 diff 并询问是否写回（自动备份为 .bak.<ts>）")
	yes := fs.Bool("y", false, "配合 --fix 使用：跳过交互确认直接写回")
	if err := fs.Parse(args); err != nil {
		return 1, err
	}
	if *input == "" {
		return 1, fmt.Errorf("-i is required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return 1, err
	}
	rep, err := doctor.Check(cfg, *reposRoot)
	if err != nil {
		return 1, err
	}
	switch *format {
	case "json":
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return 1, err
		}
		fmt.Println(string(data))
	default:
		printDoctorText(rep)
	}

	if *fix {
		if err := applyDoctorFixes(*input, rep.Issues, *yes); err != nil {
			return 1, err
		}
	}

	errs, _, _ := rep.Counts()
	if errs > 0 {
		return 2, nil
	}
	return 0, nil
}

// applyDoctorFixes 读 troubleshooter.yaml、对所有有 FixKey 的 issue 生成 patch，
// 让用户 review 一遍，确认后走行级精确替换写回，并备份原文件。
func applyDoctorFixes(yamlPath string, issues []doctor.Issue, skipConfirm bool) error {
	patches, err := doctor.PlanFixes(yamlPath, issues)
	if err != nil {
		return fmt.Errorf("plan fixes: %w", err)
	}
	if len(patches) == 0 {
		fmt.Println("\n[fix] 无机器可修复的 issue（missing-repo / origin-mismatch / service-drift 等只能人工处理）")
		return nil
	}
	fmt.Println("\n[fix] 将应用以下 patch:")
	for _, p := range patches {
		fmt.Printf("  line %d  %s:  %s  →  %s    (来自 %s)\n", p.Line, p.Path, p.From, p.To, p.FromIssue)
	}
	if !skipConfirm {
		fmt.Print("\n确认写回 " + yamlPath + " ？[y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("[fix] 已取消，未改动文件")
			return nil
		}
	}
	backup, err := doctor.ApplyAndWrite(yamlPath, patches)
	if err != nil {
		return fmt.Errorf("write %s: %w", yamlPath, err)
	}
	fmt.Printf("[ok] 已写回 %s（%d 项）；备份 %s\n", yamlPath, len(patches), backup)
	fmt.Println("下一步：tshoot validate -i " + yamlPath + " 确认无误，再 tshoot upgrade 重生成")
	return nil
}

func printDoctorText(rep *doctor.Report) {
	errs, warns, infos := rep.Counts()
	if len(rep.Issues) == 0 {
		fmt.Println("[ok] 无漂移 — troubleshooter.yaml 与代码一致")
		fmt.Println("下一步：放心 tshoot gen 生成机器人产物")
		return
	}
	for _, i := range rep.Issues {
		icon := "·"
		switch i.Severity {
		case doctor.SeverityError:
			icon = "✘"
		case doctor.SeverityWarning:
			icon = "⚠"
		case doctor.SeverityInfo:
			icon = "ℹ"
		}
		fmt.Printf("%s [%s] %s\n", icon, i.Category, i.Target)
		fmt.Printf("   %s\n", i.Message)
		if i.Suggest != "" {
			fmt.Printf("   ↳ 建议：%s\n", i.Suggest)
		}
	}
	fmt.Printf("\n合计：%d error / %d warning / %d info\n", errs, warns, infos)
	switch {
	case errs > 0:
		fmt.Println("下一步：按每条 ↳ 建议修正 troubleshooter.yaml，然后 tshoot upgrade 重跑 + 对比 diff")
	case warns > 0:
		fmt.Println("下一步：可选修正上述 warning（或暂时忽略），tshoot gen 仍可照常进行")
	default:
		fmt.Println("下一步：info 级别无需处理；tshoot gen 正常")
	}
}
