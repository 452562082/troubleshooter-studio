package main

import (
	"flag"
	"fmt"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-i is required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return err
	}
	fmt.Println("[ok] system.yaml is valid")

	// 健康检查:语义层缺口(可观测性 wiring 不全 / 仓库分支缺 env / 多源没指定 source 等),
	// 不阻断,但提示用户。CLI 输出按 severity 分组,error 用 ✗ / warn 用 ! / info 用 ·
	issues := config.HealthCheck(cfg)
	if len(issues) > 0 {
		fmt.Printf("\n[health-check] 发现 %d 条配置缺口/提示:\n", len(issues))
		for _, is := range issues {
			marker := "·"
			switch is.Severity {
			case "error":
				marker = "✗"
			case "warn":
				marker = "!"
			}
			if is.Field != "" {
				fmt.Printf("  %s [%s] %s — %s\n", marker, is.Category, is.Field, is.Message)
			} else {
				fmt.Printf("  %s [%s] %s\n", marker, is.Category, is.Message)
			}
			if is.Hint != "" {
				fmt.Printf("      建议:%s\n", is.Hint)
			}
		}
		fmt.Println()
	}

	fmt.Printf("下一步：tshoot plan -i %s                 # 预览会生成什么\n", *input)
	fmt.Printf("     或：tshoot gen  -i %s                 # 直接落盘\n", *input)
	return nil
}
