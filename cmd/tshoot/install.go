package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
)

// runInstall 把 staging 包装到本机最终位置(原生 Go,无 bash 依赖)。
//
// 三种 target:
//   - openclaw     → ~/.openclaw/workspace/<name>/ + ~/.openclaw/openclaw.json 注入
//   - claude-code  → ~/.claude/agents/<name>.md + ~/.claude/skills/<name>/...
//   - cursor       → ~/.cursor/agents/<name>.md  + ~/.cursor/skills/<name>/...
//
// 凭证收集策略(只 openclaw 需要):
//   - --env-file 指向 .env(KEY=VAL 格式),逐行 export 进 creds map
//   - 默认 staging 下的 scripts/.env 已存在 → 自动读
//   - 没设 → 装出来的产物 NACOS_ADDR 等会是空,MCP server 起不来,但产物结构正确
//     (适合脚本化场景,先生成完再用其它工具回填 .env 后重跑 install)
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	stagingDir := fs.String("path", "", "staging 产物目录(必填,tshoot gen 的 -o 输出)")
	target := fs.String("target", "", "openclaw | claude-code | cursor(必填)")
	envFile := fs.String("env-file", "", "凭证 .env 文件;默认从 <staging>/scripts/.env 读")
	skipGateway := fs.Bool("skip-gateway-restart", false, "跳过 openclaw gateway restart(本机没装 openclaw CLI 时用)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stagingDir == "" || *target == "" {
		fs.Usage()
		return fmt.Errorf("--path and --target required")
	}
	abs, err := filepath.Abs(*stagingDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("staging dir 不存在:%s", abs)
	}

	switch *target {
	case "claude-code", "cursor":
		// 纯文件分发,凭证用不上;一行 Go 调用搞定
		if err := agent.InstallNative(abs, *target); err != nil {
			return err
		}
		fmt.Printf("[ok] %s 已装到用户级目录(~/.%s/agents/<name>.md 等)\n",
			*target, claudeOrCursorDir(*target))
		return nil

	case "openclaw":
		creds, err := loadInstallCreds(abs, *envFile)
		if err != nil {
			return err
		}
		opts := agent.InstallOpenclawOptions{
			Creds:              creds,
			SkipGatewayRestart: *skipGateway,
			OnLog: func(line string) {
				fmt.Println("[install]", line)
			},
		}
		if err := agent.InstallNativeOpenclaw(context.Background(), abs, opts); err != nil {
			return err
		}
		fmt.Println("[ok] openclaw agent 已部署。下一步:")
		fmt.Println("  · 打开 OpenClaw 客户端 → 选刚装好的 agent")
		fmt.Println("  · 自检:tshoot self-test --path '" + abs + "'")
		fmt.Println("  · 卸载:tshoot uninstall --path '" + abs + "'")
		return nil

	default:
		return fmt.Errorf("unknown target: %s(支持 openclaw / claude-code / cursor)", *target)
	}
}

// runSelfTest 跑 openclaw 自检(claude-code/cursor 没自检概念,装完即用)。
func runSelfTest(args []string) error {
	fs := flag.NewFlagSet("self-test", flag.ExitOnError)
	stagingDir := fs.String("path", "", "staging 产物目录或已部署的 workspace 目录(必填)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stagingDir == "" {
		fs.Usage()
		return fmt.Errorf("--path required")
	}
	abs, _ := filepath.Abs(*stagingDir)
	res, err := agent.SelfTestOpenclaw(context.Background(), abs)
	if err != nil {
		return err
	}
	for _, c := range res.Checks {
		fmt.Printf("  [%s] %s — %s\n", c.Status, c.Name, c.Detail)
	}
	if !res.OK {
		return fmt.Errorf("self-test failed")
	}
	fmt.Println("[ok] self-test passed")
	return nil
}

// runUninstall 卸载 openclaw agent(claude-code/cursor 直接 rm ~/.<dir>/agents/<name>.md
// 即可,不在 CLI 范围;后续要补再加)。
func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	stagingDir := fs.String("path", "", "staging 产物目录或已部署的 workspace(必填,用来反读 agent 名)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stagingDir == "" {
		fs.Usage()
		return fmt.Errorf("--path required")
	}
	abs, _ := filepath.Abs(*stagingDir)
	res, err := agent.UninstallNativeOpenclaw(abs)
	if err != nil {
		return err
	}
	for _, line := range res.Log {
		fmt.Println(" ", line)
	}
	return nil
}

// loadInstallCreds 优先 --env-file → 否则 <staging>/scripts/.env → 否则空 map。
// openclaw 没拿到 creds 也不阻塞 install,产物结构正确,只是 MCP env 字段是空,
// 用户后面手填 .env 重跑即可。
func loadInstallCreds(stagingDir, envFile string) (map[string]string, error) {
	if envFile != "" {
		// 把外部 .env 拷到 staging/scripts/.env,再用 deploy.ReadEnvFile 标准化解析,
		// 共用一份 quoting / 注释跳过逻辑。
		data, err := os.ReadFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("read --env-file: %w", err)
		}
		envDir := filepath.Join(stagingDir, "scripts")
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(envDir, ".env"), data, 0o600); err != nil {
			return nil, err
		}
	}
	m, err := deploy.ReadEnvFile(stagingDir)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]string{}, nil
	}
	return m, nil
}

// claudeOrCursorDir 仅用于 install 命令打印路径提示。
func claudeOrCursorDir(target string) string {
	if target == "cursor" {
		return "cursor"
	}
	return "claude"
}
