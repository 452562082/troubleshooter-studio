// install_native_openclaw.go —— openclaw 部署的原生 Go 实现。干 5 件事:
//   (1) 探测 brew/apt 依赖(GUI 不便 sudo,只警告,装由用户自己来)
//   (2) creds map 经入参传进来,落 <staging>/scripts/.env 持久化(删 .env 即重置)
//   (3) 安装 workspace 到 ~/.openclaw/workspace/<name>/
//   (4) 改写 ~/.openclaw/openclaw.json 注入 agent + MCP servers
//   (5) 重启 gateway
//
// 取消 / 流式日志走 ctx + onLog callback。CLI 与桌面端共享同一份。
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if _, err := exec.LookPath("uvx"); err != nil {
		// uvx 只 nacos MCP 用到,缺了不致命
		log("[dep] uvx 没装(nacos-mcp-router 跑不动);brew install uv 或 https://astral.sh/uv/install.sh")
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
	} else if _, err := exec.LookPath("openclaw"); err == nil {
		c := exec.CommandContext(ctx, "openclaw", "gateway", "restart")
		if err := c.Run(); err != nil {
			log(fmt.Sprintf("[warn] openclaw gateway restart 失败:%v;请手动 `openclaw gateway restart`,或退出再开 OpenClaw 客户端", err))
		} else {
			log("[ok] openclaw gateway 已重启,新 agent 立即可用")
		}
	} else {
		log("[info] 未检测到 openclaw CLI(桌面 app 用户的常见状态)—— **请手动退出再打开 OpenClaw 客户端** 才能在 agent 列表里看到新装的 bot;或装 openclaw CLI 后跑 `openclaw gateway restart`")
	}
	return nil
}

