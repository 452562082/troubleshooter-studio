package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/deploy"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// runInstall 把 staging 包装到本机最终位置(原生 Go,无 bash 依赖)。
//
// 四种 target:
//   - openclaw     → ~/.openclaw/workspace/<name>/ + ~/.openclaw/openclaw.json 注入
//   - claude-code  → ~/.claude/agents/<name>.md + ~/.claude/{skills,scripts}/<name>/...
//   - cursor       → ~/.cursor/agents/<name>.md  + ~/.cursor/{skills,scripts}/<name>/...
//   - codex        → ~/.codex/agents/<name>.toml(TOML subagent 定义,
//                    https://developers.openai.com/codex/subagents) + ~/.codex/skills/<name>/
//                    + ~/.codex/scripts/<name>/。MCP 嵌入 agent toml 内联段(每个 subagent 自带)。
//
// 凭证收集策略:
//   - openclaw   :走 InstallNativeOpenclaw,凭证写到 ~/.openclaw/<id>-creds.json + 注入 MCP env
//   - IDE 三家   :--env-file 非空时,凭证一并注入到对应 IDE 的 mcpServers env(claude-code 写
//                 settings.json;cursor 写 mcp.json;codex 写到 agent toml 的 [mcp_servers.*.env]),
//                 同时镜像写 ~/.tshoot/<id>-creds.json 给 kuboard / apollo / consul / 静态环境变量
//                 等"非 MCP 走脚本"的后端用。空凭证不阻塞 install,事后手填 .env 重跑即可。
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	stagingDir := fs.String("path", "", "staging 产物目录(必填,tshoot gen 的 -o 输出)")
	target := fs.String("target", "", "openclaw | claude-code | cursor | codex(必填)")
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

	if t, err := agent.ParseIDETarget(*target); err == nil {
		if err := agent.InstallNative(abs, *target); err != nil {
			return err
		}
		// 注入 MCP + 写通用 creds.json,走跟桌面 app 一致的路径。
		// **MCP 注入即便 creds 为空也跑** — BuildMCPServers 数据层 case 有 yaml endpoints[]
		// fallback,creds 没填的字段会从 cfg.Infrastructure.DataStores[].Endpoints[] 派生。
		// 之前这里 creds==nil 整个跳过,导致 staging 没 .env 时数据层 mcp 永远注册不了
		// (踩过这个坑:claude-code staging 不带 scripts/.env,跑 install 数据层 mcp 全空)。
		// MergeMCPIntoIDESettings 是替换式合并(只删派生 keys 再覆盖),空 creds 只让 env 段空,不乱删用户别名。
		creds, _ := loadIDECredsBestEffort(abs, *envFile)
		if err := installIDESideEffects(*target, abs, creds); err != nil {
			return err
		}
		if t == agent.TargetCodex {
			fmt.Printf("[ok] %s 已装到用户级目录(~/%s/agents/<name>.toml + skills/scripts/<name>/);"+
				"codex 启动后在主 chat 里说 \"spawn the <name> agent ...\" 调用(TOML subagent,见 https://developers.openai.com/codex/subagents)\n",
				*target, t.DirName())
		} else {
			fmt.Printf("[ok] %s 已装到用户级目录(~/%s/agents/<name>.md + skills/scripts/<name>/)\n",
				*target, t.DirName())
		}
		if t == agent.TargetCodex {
			fmt.Println("    · MCP 服务器嵌入 agent toml 内联 [mcp_servers.*] 段(只在 spawn 该 subagent 时启动,不污染主 chat)")
		} else {
			fmt.Printf("    · MCP 服务器写到 %s (--env-file 提供凭证才注入)\n",
				t.MCPConfigDisplay())
		}
		return nil
	}

	switch *target {
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
		return fmt.Errorf("unknown target: %s(支持 openclaw / claude-code / cursor / codex)", *target)
	}
}

// loadIDECredsBestEffort 给 claude-code / cursor / codex 三家 install 走的凭证读取。
// --env-file 优先,否则尝试 staging/scripts/.env。读不到返回 nil(让上层调用方据此跳过
// MergeMCPIntoIDESettings/WriteIDECredsFile,避免空覆盖凭证)。失败也返回 nil:IDE 装机
// 主流程已成功(agent.md / skills 文件已落盘),凭证 best-effort 不阻塞主流程。
func loadIDECredsBestEffort(stagingDir, envFile string) (map[string]string, error) {
	creds, err := loadInstallCreds(stagingDir, envFile)
	if err != nil || len(creds) == 0 {
		return nil, err
	}
	return creds, nil
}

// installIDESideEffects 装完 IDE 平台的文件之后,把 cfg 派生的 mcpServers 注入到对应
// IDE 配置 + 把非 MCP 后端的凭证写到 ~/.tshoot/<id>-creds.json。跟桌面 app
// agent.Apply 流程对齐,确保 CLI 装出来的机器人功能完整(包括 kuboard / k8s 查询等
// 走脚本读 creds.json 的 skill)。
func installIDESideEffects(target, stagingDir string, creds map[string]string) error {
	cfg, err := loadStagingSystemConfig(stagingDir)
	if err != nil {
		return fmt.Errorf("read staging tshoot.json: %w", err)
	}
	if err := agent.MergeMCPIntoIDESettings(target, cfg, creds, nil); err != nil {
		return fmt.Errorf("merge mcp settings: %w", err)
	}
	if err := agent.WriteIDECredsFile(cfg, creds); err != nil {
		return fmt.Errorf("write ide creds: %w", err)
	}
	return nil
}

// loadStagingSystemConfig 从 staging 目录的 tshoot.json 读出 system_yaml 段,parse 成
// SystemConfig。CLI install 在 IDE 平台分支调它,把 cfg 喂给 MergeMCPIntoIDESettings 派生 MCP。
// 两个候选位置(谁先存在用谁,跟桌面 app bindings_deploy.go::loadStagingConfig 同款逻辑):
//   - <staging>/tshoot.json                              ← claude-code/cursor/codex staging
//   - <staging>/templates/workspace-template/tshoot.json ← openclaw staging(本路径不会走到这,IDE 装机不读)
func loadStagingSystemConfig(stagingDir string) (*config.SystemConfig, error) {
	candidates := []string{
		filepath.Join(stagingDir, discover.MetaFilename),
		filepath.Join(stagingDir, "templates", "workspace-template", discover.MetaFilename),
	}
	var data []byte
	var lastErr error
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			data = b
			break
		}
		lastErr = err
	}
	if data == nil {
		return nil, lastErr
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse tshoot.json: %w", err)
	}
	cfg, err := config.LoadFromBytes([]byte(meta.SystemYAML))
	if err != nil {
		return nil, fmt.Errorf("troubleshooter.yaml in tshoot.json invalid: %w", err)
	}
	return cfg, nil
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

// loadInstallCreds 凭证读取的 fallback 链:
//  1. --env-file <path>(显式)
//  2. <staging>/scripts/.env(deploy.ReadEnvFile 标准位置)
//  3. ~/.tshoot/openclaw/<system_id>/scripts/.env(IDE staging 不写 .env,跨 target 共享
//     openclaw 那份,wizard 已经在 openclaw 流程收过一次的 creds 不重收)
//  4. 空 map(不阻塞 install,产物结构正确,MCP env 字段空)
//
// IDE staging(claude-code/cursor/codex)默认不写 scripts/.env,只有 openclaw staging 写。
// 没这条 fallback 时,跑 `tshoot install -target claude-code` 不传 -env-file → creds 空 →
// grafana/loki/nacos/elasticsearch 等 mcp env 段全空 → mcp 启动失败。
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
	if len(m) > 0 {
		return m, nil
	}
	// fallback:从 staging 的 tshoot.json 读 system_id,试 ~/.tshoot/openclaw/<id>/scripts/.env
	if creds := tryLoadOpenclawCreds(stagingDir); creds != nil {
		fmt.Printf("    · creds fallback 自 ~/.tshoot/openclaw/%s/scripts/.env(%d 个变量)\n",
			openclawSystemIDOf(stagingDir), len(creds))
		return creds, nil
	}
	return map[string]string{}, nil
}

// tryLoadOpenclawCreds 从 staging 的 tshoot.json 读 system_id,然后读 ~/.tshoot/openclaw/<id>/scripts/.env。
// 任何一步失败 / 找不到 / 文件不存在 → 返回 nil(让上层走"空 creds"路径,不报错)。
func tryLoadOpenclawCreds(stagingDir string) map[string]string {
	id := openclawSystemIDOf(stagingDir)
	if id == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	openclawStaging := filepath.Join(home, ".tshoot", "openclaw", id)
	if _, err := os.Stat(filepath.Join(openclawStaging, "scripts", ".env")); err != nil {
		return nil
	}
	m, err := deploy.ReadEnvFile(openclawStaging)
	if err != nil || len(m) == 0 {
		return nil
	}
	return m
}

// openclawSystemIDOf 解 staging/tshoot.json 取 system.id;失败返 ""。
func openclawSystemIDOf(stagingDir string) string {
	cfg, err := loadStagingSystemConfig(stagingDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.System.ID
}
