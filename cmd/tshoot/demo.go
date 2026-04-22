package main

import (
	"flag"
	"fmt"
	"os"

	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	keep := fs.Bool("keep", false, "保留 demo 目录，不自动清理")
	sysFlag := fs.String("i", "", "可选：自定义 system.yaml（默认 examples/shop-system.yaml）")
	reposFlag := fs.String("repos-root", "", "可选：自定义 repos 根目录（默认 examples/fake-repos）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tmplRoot := resolveTemplateDir()
	examplesDir := resolveExamplesDir()

	sysPath := *sysFlag
	if sysPath == "" {
		sysPath = filepath.Join(examplesDir, "shop-system.yaml")
	}
	reposRoot := *reposFlag
	if reposRoot == "" {
		reposRoot = filepath.Join(examplesDir, "fake-repos")
	}
	if _, err := os.Stat(sysPath); err != nil {
		return fmt.Errorf("demo system.yaml 未找到: %s (%w)\n提示：templates / examples 都优先从可执行文件旁 / CWD 取；都不在会从二进制内嵌的 embed.FS extract 出来。", sysPath, err)
	}
	if _, err := os.Stat(reposRoot); err != nil {
		return fmt.Errorf("demo repos-root 未找到: %s (%w)", reposRoot, err)
	}

	demoDir, err := os.MkdirTemp("", "tshoot-demo-*")
	if err != nil {
		return err
	}
	if !*keep {
		defer func() {
			fmt.Printf("\n[cleanup] rm -rf %s（用 --keep 可保留）\n", demoDir)
			_ = os.RemoveAll(demoDir)
		}()
	}

	fmt.Println("=== tshoot demo ===")
	fmt.Printf("  system.yaml: %s\n", sysPath)
	fmt.Printf("  repos-root:  %s\n", reposRoot)
	fmt.Printf("  demo out:    %s\n", demoDir)

	// 1) validate（config.Load 会做结构校验）
	cfg, err := config.Load(sysPath)
	if err != nil {
		return fmt.Errorf("[1/3] validate 失败: %w", err)
	}
	fmt.Println("\n[1/3] validate ✓ system.yaml 结构合法")

	// 改写 output_dir 到 demo 目录
	outDir := filepath.Join(demoDir, "out")
	cfg.Generation.OutputDir = outDir

	// 2) plan（干跑，不写盘，展示将生成什么）
	g := generator.New(cfg, tmplRoot, outDir)
	planRes, err := g.BuildPlan("")
	if err != nil {
		return fmt.Errorf("[2/3] plan: %w", err)
	}
	fmt.Printf("[2/3] plan     ✓ %d 个 skill / %d 个待创建文件\n",
		len(planRes.SkillsIncluded), len(planRes.FilesCreate))

	// 3) gen（真落盘到 demoDir/out）。
	// 注意：BuildPlan 会把 g.OutputDir 改成它自己的临时目录，需要在 Generate 前恢复。
	g.OutputDir = outDir
	if err := g.Generate(); err != nil {
		return fmt.Errorf("[3/3] gen: %w", err)
	}
	s := g.Summary
	fmt.Printf("[3/3] gen      ✓ 产物写入 %s (%d 个文件)\n", outDir, s.FilesWritten)

	// 打印产物目录树(2 层深度)
	fmt.Println("\n产物概览:")
	printTree(outDir, "", 2)

	// 打开指引
	fmt.Println("\n── 看看 tshoot 做了什么 ────────────────────────────────────────")
	fmt.Printf("  cat '%s/scripts/install.sh' | head -80       # 安装脚本\n", outDir)
	fmt.Printf("  cat '%s/templates/workspace-template/IDENTITY.md'   # 机器人身份 + 典型求助示例\n", outDir)
	fmt.Printf("  cat '%s/templates/workspace-template/skills/routing/SKILL.md'  # 主 skill\n", outDir)
	fmt.Println()
	fmt.Println("  想看 analyze 从 fake-repos 抽到什么配置线索：")
	fmt.Printf("    %s analyze -i %s --repos-root %s\n", os.Args[0], sysPath, reposRoot)
	fmt.Println()
	fmt.Println("  想看 multi-target（claude-code/cursor/embedded）各长啥样：")
	fmt.Println("    在自己的 system.yaml 的 generation.targets 里加上它们，再跑 tshoot gen")
	_ = reposRoot // 预留给未来 analyze/doctor 集成
	return nil
}

// printTree 打印目录树，depth 控制深度（0=只打当前层）
func printTree(root, prefix string, depth int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for i, e := range entries {
		last := i == len(entries)-1
		branch := "├── "
		childPre := prefix + "│   "
		if last {
			branch = "└── "
			childPre = prefix + "    "
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Println(prefix + branch + name)
		if e.IsDir() && depth > 0 {
			printTree(filepath.Join(root, e.Name()), childPre, depth-1)
		}
	}
}
