// install_native_openclaw.go —— openclaw 部署的原生 Go 实现。干 5 件事:
//
//	(1) 探测 brew/apt 依赖(GUI 不便 sudo,只警告,装由用户自己来)
//	(2) creds map 经入参传进来,落 <staging>/scripts/.env 持久化(删 .env 即重置)
//	(3) 安装 workspace 到 ~/.openclaw/workspace/<name>/
//	(4) 改写 ~/.openclaw/openclaw.json 注入 agent + MCP servers
//	(5) 重启 gateway
//
// 取消 / 流式日志走 ctx + onLog callback。CLI 与桌面端共享同一份。
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// InstallOpenclawOptions 给 InstallNativeOpenclaw 的选项。零值合理。
type InstallOpenclawOptions struct {
	// Creds:UI 收集到的凭证 map(key 跟 DerivePrompts 返回的 Name 对齐)。
	// 同时也会被 ReadEnvFile 出来的 .env 老凭证 merge:Creds 优先,.env 兜底。
	Creds map[string]string

	// OnLog:每行进度回调,nil 表示不回调。模拟原 install.sh 的 stdout 流。
	OnLog func(line string)

	// SkipGatewayRestart:测试用 —— 跳过 `openclaw gateway restart`,避免在 dev
	// 机器上(真装了 openclaw 的)被测试误触重启。生产路径不要设。
	SkipGatewayRestart bool
}

// InstallNativeOpenclaw 把 stagingDir 里的产物装到 ~/.openclaw/。
// stagingDir 是 generator 的产物目录(含 templates/workspace-template/ 和 tshoot.json)。
//
// 步骤:
//  1. 从 stagingDir/tshoot.json 反读 troubleshooter.yaml → cfg
//  2. 探测依赖(node/npx/python3/uvx),缺的告警但不中断
//  3. 把 .env 旧凭证 merge 进 creds(用户可以局部覆盖)
//  4. 备份并安装 workspace 到 ~/.openclaw/workspace/<workspace_name>/
//  5. 改写 ~/.openclaw/openclaw.json:注入 agent.list + mcp.servers
//  6. 写 ~/.openclaw/<agent_id>-creds.json(apollo/consul/env-vars/k8s 才有)
//  7. 写回 stagingDir/scripts/.env(凭证持久化,下次 import 直接预填)
//  8. 尽力试 `openclaw gateway restart`(没装 CLI 就跳过,不算失败)
func InstallNativeOpenclaw(ctx context.Context, stagingDir string, opts InstallOpenclawOptions) error {
	log := opts.OnLog
	if log == nil {
		log = func(string) {}
	}

	// 1) 读 tshoot.json 拿 troubleshooter.yaml
	cfg, meta, err := loadCfgFromTshoot(stagingDir)
	if err != nil {
		return err
	}

	// 2) 依赖探测
	for _, dep := range []string{"python3", "node", "npx"} {
		if _, err := exec.LookPath(dep); err != nil {
			log(fmt.Sprintf("[dep] missing: %s — MCP servers 跑起来时会报错,请用 brew/apt 装好再 retry", dep))
		} else {
			log(fmt.Sprintf("[dep] %s ok", dep))
		}
	}
	// uvx 之前给 nacos-mcp-server 用,2026-05-15 方案 B 后 nacos 走 Python HTTP API,
	// uvx 在 install 阶段不再有必装组件依赖它;留 LookPath 给未来 mcp 扩展用,缺了只是 info。
	if _, err := exec.LookPath("uvx"); err != nil {
		log("[dep] uvx 没装(目前没强依赖,nacos 走 python3 主路径;若以后接入需要 uvx 的 MCP 再装:brew install uv)")
	}

	// 3) merge .env 老凭证(Creds 优先)
	creds := map[string]string{}
	// 用 deploy.ReadEnvFile 是循环依赖(deploy 不依赖 agent 但 agent 依赖 deploy 的 Prompt 类型);
	// 这里直接读避免引依赖图分裂。.env 不存在不报错。
	if env, _ := readEnvFileSimple(stagingDir); env != nil {
		for k, v := range env {
			creds[k] = v
		}
	}
	for k, v := range opts.Creds {
		if v != "" {
			creds[k] = v
		}
	}
	get := func(k string) string { return creds[k] }

	// 4) 安装 workspace —— 优先用 yaml 显式 workspace_name(兼容老 yaml),
	// 空时回落到 agent.id(新 wizard 不再单独 emit workspace_name,跟 agent.id 共用)
	wsName := strings.TrimSpace(cfg.ResolveWorkspaceName())
	if wsName == "" {
		return fmt.Errorf("无法确定 workspace 目录名:agent.id / agent.workspace_name 至少要有一个非空")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	ocHome := filepath.Join(home, ".openclaw")
	wsDir := filepath.Join(ocHome, "workspace", wsName)
	tplSrc := filepath.Join(stagingDir, "templates", "workspace-template")
	if _, err := os.Stat(tplSrc); err != nil {
		return fmt.Errorf("staging 缺 templates/workspace-template/: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(wsDir), 0o755); err != nil {
		return err
	}
	// 已存在 → 移到 Trash(对齐 install.sh 行为,留个回收点)。
	// nanoTimestamp 防 1 秒内连点两次部署撞 wsTrash 导致第二次的 workspace 被 RemoveAll 兜底删了。
	wsTrash := filepath.Join(home, ".Trash", meta.SystemID+"-troubleshooter-workspace-"+nanoTimestamp())
	if movedTo, existed, _ := moveOutOrRemove(wsDir, wsTrash); existed {
		if movedTo != "" {
			log(fmt.Sprintf("[backup] 旧 workspace 移到 %s", movedTo))
		} else {
			log("[backup] rename to Trash 失败,已直接清掉旧 workspace")
		}
	}
	if err := copyDirAll(tplSrc, wsDir); err != nil {
		return fmt.Errorf("install workspace: %w", err)
	}
	log(fmt.Sprintf("[ok] workspace 安装到 %s", wsDir))

	// 5) 改写 ~/.openclaw/openclaw.json
	cfgPath := filepath.Join(ocHome, "openclaw.json")
	ocData, err := readJSONOrEmpty(cfgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}
	agentID := cfg.ResolveID()
	model := get("MODEL")
	if model == "" {
		model = cfg.Agent.ModelForTarget("openclaw")
	}
	if err := injectAgent(ocData, agentID, cfg.Agent.Name, model, wsDir); err != nil {
		return err
	}
	if err := injectMCPServers(ocData, cfg, get, ocHome); err != nil {
		return err
	}
	if err := writeJSONFile(cfgPath, ocData, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	log(fmt.Sprintf("[ok] %s 已更新(agents.list + mcp.servers)", cfgPath))

	// P2.4 进度反馈:从 ocData 反读出本次注册的 mcp 列表,逐个 log 让用户看到注册了哪些。
	// 主要解决"`tshoot apply` 完只打一行模糊 '已更新' 用户不知道装了几家"的体验问题。
	// 同时按 command 分类打提示:npx / uvx 类首次 IDE spawn 时会 cold install(30-60s),
	// kafka binary 类 install 阶段已经 EnsureKafkaMCPInstalled 下载过(秒级 spawn)。
	logMCPRegistered(ocData, cfg, log)

	// 6) creds.json:多源场景下任一源属于 apollo/consul/env-vars/kuboard 就要写。
	// 跟 IDE 平台共用 WriteCredsFileToHome —— 唯一区别是 homeSubdir(.openclaw vs .tshoot)。
	if err := WriteCredsFileToHome(".openclaw", cfg, get); err != nil {
		return err
	}
	// 跟旧行为对齐的 log:有真东西要写才报。WriteCredsFileToHome 全 nacos 时会 silent skip,
	// 这里 needsCreds 二次判一下决定是否打 log,避免对全 nacos 系统刷"creds 写到 ..."误导。
	hasNonNacos := false
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if needsCreds(cc.Type) {
			hasNonNacos = true
			break
		}
	}
	if hasNonNacos {
		credsPath := filepath.Join(ocHome, agentID+"-creds.json")
		log(fmt.Sprintf("[ok] creds 写到 %s (mode 0600)", credsPath))
	}

	// 7) 持久化 .env(下次 import 自动预填)
	if err := writeEnvFileSimple(stagingDir, creds); err != nil {
		// 写不进 .env 不致命(产物本身已装好),只 warn
		log(fmt.Sprintf("[warn] .env 写入失败:%v(下次 import 不会预填,需重填凭证)", err))
	} else {
		log("[ok] 凭证已保存到 scripts/.env(下次 import 自动复用)")
	}

	// 8) 尝试 reload gateway(测试可关掉)
	//
	// OpenClaw 客户端启动时只读一次 openclaw.json,新装的 agent 不会自动出现在 agent 列表 ——
	// 必须 reload 才能刷出。两条路径:
	//   a) CLI(`openclaw gateway restart`)在 PATH 里 → 自动跑(用户不用动手)
	//   b) Mac 桌面 app 用户大概率没装 openclaw CLI,fallback 文案要明说"退出再开 OpenClaw 客户端"
	//      (而不是只让用户跑 CLI 命令 —— 客户端用户压根不知道 CLI 是啥)
	if opts.SkipGatewayRestart {
		log("[skip] gateway restart 被显式跳过(测试模式)")
	} else if cli := findOpenclawCLI(); cli != "" {
		// 30s timeout 兜底:`openclaw gateway restart` 偶发挂死(OpenClaw 客户端 socket
		// 等不到、端口被占、客户端进程僵尸等),不限时会让桌面 app UI 永远卡在"部署中..."。
		// 5.5s 是正常重启耗时,30s 留 ~5× 余量;超时 fallback 到手动提示,装机本身已落地。
		restartCtx, restartCancel := context.WithTimeout(ctx, 30*time.Second)
		c := exec.CommandContext(restartCtx, cli, "gateway", "restart")
		err := c.Run()
		restartCancel()
		if err != nil {
			if restartCtx.Err() == context.DeadlineExceeded {
				log("[warn] openclaw gateway restart 超过 30s 未返回,放弃等待。装机已完成,请手动退出再打开 OpenClaw 客户端激活新 agent")
			} else {
				log(fmt.Sprintf("[warn] openclaw gateway restart 失败:%v;请手动 `openclaw gateway restart`,或退出再开 OpenClaw 客户端", err))
			}
		} else {
			log("[ok] openclaw gateway 已重启,新 agent 立即可用")
		}
	} else {
		log("[info] 未检测到 openclaw CLI(桌面 app 用户的常见状态)—— **请手动退出再打开 OpenClaw 客户端** 才能在 agent 列表里看到新装的 bot;或装 openclaw CLI 后跑 `openclaw gateway restart`")
	}

	// P2.4 结尾提示:用户首次在 IDE 里调 mcp 工具时,npx/uvx 包会冷启动下载,体感"挂了 30s"。
	// 提前告知避免用户疑惑。kafka binary 类已经在 EnsureKafkaMCPInstalled 阶段下完。
	log("[info] 首次在 OpenClaw 客户端里触发 mcp 工具调用时,npx / uvx 包会冷启动下载(单家 ~10-60s)")
	log("        正常现象 — 之后 IDE 启动 mcp 是秒级。装太久或撞错请跑 `tshoot self-test` 看 mcp probe 结果")
	return nil
}

// logMCPRegistered 从写好的 openclaw.json 反读 mcp.servers,按家逐条 log。
// 让用户清楚看到 install 给装了哪些 mcp,而不是只看到一行 "mcp.servers 已更新"。
//
// 输出形式:
//
//	[mcp] register: shop-grafana-dev (npx mcp-grafana-npx ...)
//	[mcp] register: shop-mongodb-dev (npx mcp-mongo-server ...)
//	(skip) feishu_project / rabbitmq / nacos 那些 buildXxx 内 warn skip 的,不出现在 servers map,自然不打
//
// 不打整行 args(密码会泄漏到 log),只打 command + 第一个非 -y 的 arg(通常是包名),给用户一个概念。
func logMCPRegistered(ocData map[string]any, cfg *config.SystemConfig, log func(string)) {
	mcp, _ := ocData["mcp"].(map[string]any)
	if mcp == nil {
		return
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		return
	}
	agentPrefix := cfg.MCPKeyPrefix() + "-"
	names := []string{}
	for name := range servers {
		// 只列本次 agent 注册的 — 别家 agent 用同一 ~/.openclaw/openclaw.json 时不刷别家进度
		if !strings.HasPrefix(name, agentPrefix) && name != cfg.MCPKeyPrefix()+"lark-openapi" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, _ := servers[name].(map[string]any)
		if spec == nil {
			continue
		}
		cmd, _ := spec["command"].(string)
		pkg := ""
		if args, ok := spec["args"].([]any); ok {
			for _, a := range args {
				s, _ := a.(string)
				if s == "" || strings.HasPrefix(s, "-") {
					continue
				}
				pkg = s
				break
			}
		}
		log(fmt.Sprintf("[mcp] register: %s (%s %s)", name, cmd, pkg))
	}
}

// findOpenclawCLI 在当前进程 PATH 找 openclaw CLI 二进制,找不到再 fallback 试几个
// 装机常见绝对路径。
//
// 背景:OpenClaw mac 桌面 app 启动子进程时继承的 PATH 来自 launchd 的 GUI 默认,
// 只有 /usr/bin:/bin:/usr/sbin:/sbin —— 用户 shell 里 brew prefix(/opt/homebrew/bin
// 或 /usr/local/bin)装的 CLI 在这种环境下 exec.LookPath 找不到。用户实际装了 CLI
// (`npm i -g openclaw` 落到 brew prefix 下),只是 GUI 进程的 PATH 不知道。
//
// 候选路径覆盖 Apple Silicon brew / Intel mac brew / npm-global / ~/.local 四类,
// 命中即返回绝对路径供 exec.CommandContext 直调;全部 miss 才走 fallback 文案。
//
// 返回找到的可执行文件绝对路径,找不到返回空串。
func findOpenclawCLI() string {
	if p, err := exec.LookPath("openclaw"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/opt/homebrew/bin/openclaw", // Apple Silicon brew prefix
		"/usr/local/bin/openclaw",    // Intel mac brew / Linux brew
		filepath.Join(home, ".npm-global", "bin", "openclaw"),
		filepath.Join(home, ".local", "bin", "openclaw"),
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
