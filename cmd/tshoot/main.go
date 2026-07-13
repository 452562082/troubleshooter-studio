package main

import (
	"fmt"
	"os"
)

// 通过 -ldflags "-X main.version=v0.2.0 -X main.commit=abcdef" 注入；未注入时保持 dev
var (
	version = "dev"
	commit  = ""
)

func main() {
	if len(os.Args) < 2 {
		printWelcome()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "--version", "-v", "version":
		if commit != "" {
			fmt.Printf("tshoot %s (%s)\n", version, commit)
		} else {
			fmt.Printf("tshoot %s\n", version)
		}
		return
	case "gen", "generate":
		if err := runGen(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "validate":
		if err := runValidate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "analyze":
		if err := runAnalyze(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "diff":
		if err := runDiff(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "doctor":
		code, err := runDoctor(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(code)
	case "plan":
		if err := runPlan(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "skill":
		if err := runSkill(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "watch":
		if err := runWatch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "upgrade":
		if err := runUpgrade(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "demo":
		if err := runDemo(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "discover":
		if err := runDiscover(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "apply":
		if err := runApply(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "install":
		if err := runInstall(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "self-test":
		if err := runSelfTest(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := runUninstall(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// printWelcome 在 `tshoot`（无参）时打印，给第一次接触的人一个清晰的起点。
// 跟 usage() 不同：usage 是完整命令手册，welcome 只告诉用户下一步该做什么。
func printWelcome() {
	fmt.Printf(`troubleshooter-studio — AI 排障机器人工作台  (version %s)

第一次使用？推荐路径:

  ★ 桌面 app(推荐,非程序员友好):
       make desktop-app                       # 仓库根跑
       open dist/TroubleshooterStudio.app     # 或 Finder 双击
       # 桌面端里一站式:扫已装机器人 / 创建向导 / 导入 yaml 部署 / 编辑应用

  ● 零配置试跑(30 秒看看产物长啥样):
       tshoot demo

  ● CLI 全流程(脚本化 / CI 场景):
       tshoot init    -o troubleshooter.yaml             # 交互向导生成 yaml
       tshoot gen     -i troubleshooter.yaml             # 生成 staging
       tshoot install --path dist/<id> --target openclaw    # 部署(原生 Go,无 bash)

已有 troubleshooter.yaml 的常用命令:
  tshoot validate -i troubleshooter.yaml                 # 校验格式
  tshoot serve --addr 127.0.0.1:8080                     # 启动 HTTP API + Web UI
  tshoot plan     -i troubleshooter.yaml                 # 预览会生成什么
  tshoot gen      -i troubleshooter.yaml                 # 真落盘 staging
  tshoot install  --path <staging> --target X    # 部署到本机(openclaw / claude-code / cursor / codex)
  tshoot self-test --path <staging>              # openclaw 部署后自检
  tshoot uninstall --path <staging>              # 卸载 openclaw agent
  tshoot doctor   -i troubleshooter.yaml                 # 检查声明 vs 实态漂移
  tshoot discover                                # 扫本机已装机器人
  tshoot apply -i new.yaml --path <p>            # 原地更新已装机器人

完整命令列表:tshoot --help
版本信息:    tshoot --version
`, version)
}

func usage() {
	fmt.Println(`troubleshooter-studio — AI 排障机器人工作台

用法:
  tshoot init [-o <troubleshooter.yaml>]                          # 交互向导生成 troubleshooter.yaml
  tshoot gen -i <troubleshooter.yaml> [-o <output_dir>] [-t <template_dir>] [--analysis <analysis.json>]
  tshoot plan -i <troubleshooter.yaml> [--analysis <analysis.json>] [--against <dir>] [--format=text|json]
  tshoot watch -i <troubleshooter.yaml> [--analysis <analysis.json>] [--interval 1s]
  tshoot serve [--addr 127.0.0.1:8080]
  tshoot analyze -i <troubleshooter.yaml> --repos-root <dir> [-o <analysis.json>] [--auto-clone] [--branch <name>]
  tshoot doctor -i <troubleshooter.yaml> [--repos-root <dir>] [--format=text|json]
  tshoot diff -i <troubleshooter.yaml> [--analysis <analysis.json>] [--against <dir>]
  tshoot upgrade -i <troubleshooter.yaml> [--analysis <analysis.json>] [--format=text|json]
  tshoot skill new <name> [-t <template_dir>] [--description "..."] [--with-scripts] [--with-references]
  tshoot demo [--keep]                                         # 零配置试跑（用内置 examples 走完整流程）
  tshoot validate -i <troubleshooter.yaml>
  tshoot discover [--roots <p1>,<p2>] [--format text|json]    # 扫本机已装机器人
  tshoot apply -i <new.yaml> --path <agent-path> [--dry-run]  # 用新 yaml 原地更新已装机器人
  tshoot install --path <staging> --target <openclaw|claude-code|cursor|codex> [--env-file <.env>] [--skip-gateway-restart]
  tshoot self-test --path <staging>                           # openclaw 部署后自检
  tshoot uninstall --path <staging>                           # 卸载 openclaw agent

子命令:
  init       交互式问答生成一份最小可用 troubleshooter.yaml
  gen        基于 troubleshooter.yaml 生成机器人产物（人工 verified 行保留,模板派生文件按模板覆盖）
  plan       干跑一次 gen，展示将生成/应用的内容与 config-map 分布（不写盘）
  watch      文件变化时自动重跑 plan（troubleshooter.yaml / templates/ / analysis.json）
  serve      启动本机 HTTP API + Web UI（默认 127.0.0.1:8080）
  analyze    扫描已 clone 的仓库，抽取 service_names 与配置中心线索
  doctor     对比 troubleshooter.yaml 声明与 analyzer 实测，报告漂移
  diff       预览本次生成相对现有产物的变化（不写盘）
  upgrade    备份现有产物到 <out>.bak.<ts>，重跑 gen（保留 config-map 人工行），输出 diff
  skill      skill 脚手架（skill new <name> 在模板库里生成新 skill 骨架）
  demo       零配置试跑：用内置 examples/shop-troubleshooter.yaml + examples/fake-repos 跑完整 pipeline
  validate   仅校验 troubleshooter.yaml
  discover   扫本机 tshoot.json 锚点，列出已装机器人
  apply      拿新 yaml 重 render + rsync 回已装 workspace（模板派生文件按模板覆盖）
  install    把 staging 装到本机最终位置(原生 Go,无 bash 依赖):
             - openclaw:   ~/.openclaw/workspace/<name>/ + 注入 openclaw.json
             - claude-code: ~/.claude/agents/<name>.md  + skills/scripts namespace 子目录
             - cursor:     ~/.cursor/agents/<name>.md   + skills/scripts namespace 子目录
             - codex:      ~/.codex/agents/<name>.toml(内嵌 [mcp_servers.*]) + ~/.codex/skills/<name>/
  self-test  openclaw 部署后自检(workspace / openclaw.json / mcp.servers / TCP+HTTP 探活)
  uninstall  卸载 openclaw agent(workspace 移到 ~/.Trash + 摘 openclaw.json + 清 creds.json)`)
}
