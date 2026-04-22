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
	_, err := config.Load(*input)
	if err != nil {
		return err
	}
	fmt.Println("[ok] system.yaml is valid")
	fmt.Printf("下一步：tshoot plan -i %s                 # 预览会生成什么\n", *input)
	fmt.Printf("     或：tshoot gen  -i %s                 # 直接落盘\n", *input)
	return nil
}
