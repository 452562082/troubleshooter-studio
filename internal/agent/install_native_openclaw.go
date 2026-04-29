// install_native_openclaw.go —— openclaw 部署的原生 Go 实现,替代之前 ~500 行的
// scripts/install.sh + 嵌入式 Python 块。
//
// 历史:install.sh 干 5 件事 —— (1) 装 brew/apt 依赖 (2) 交互收集凭证
// (3) 安装 workspace 到 ~/.openclaw/workspace/<name>/ (4) Python 改写
// ~/.openclaw/openclaw.json 注入 agent + MCP servers (5) 重启 gateway。
//
// 现在 (1) 改成"探测+警告"(GUI 不便 sudo,让用户自己装更稳),(2) 由 Studio
// UI 表单收集 → 走 creds map 传进来,(3)(4)(5) 全部原生 Go。这样:
//   - 桌面端"一键部署 openclaw"完全不需要 bash subshell
//   - CLI 用户(`tshoot agent install`)也走同一份代码,不再两套实现漂移
//   - 取消 / 流式日志通过 ctx + onLog callback 走,跟原 RunInstallStreaming 行为一致
//
// 不动的:WriteEnvFile / ReadEnvFile 仍写 <staging>/scripts/.env(凭证持久化),
// 删 .env 等同"重置凭证";只是 install.sh 没了,改由本函数 source 同一份 .env。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
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
//  1. 从 stagingDir/tshoot.json 反读 system.yaml → cfg
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

	// 1) 读 tshoot.json 拿 system.yaml
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
	if _, err := os.Stat(wsDir); err == nil {
		ts := time.Now().Format("20060102-150405")
		bk := filepath.Join(home, ".Trash", meta.SystemID+"-troubleshooter-workspace-"+ts)
		if err := os.MkdirAll(filepath.Dir(bk), 0o755); err == nil {
			if err := os.Rename(wsDir, bk); err == nil {
				log(fmt.Sprintf("[backup] 旧 workspace 移到 %s", bk))
			} else {
				// 跨盘 rename 失败 → 直接 RemoveAll 兜底,workspace 重生成无损失
				_ = os.RemoveAll(wsDir)
				log(fmt.Sprintf("[backup] rename to Trash 失败(%v),已直接清掉旧 workspace", err))
			}
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
	if err := injectMCPServers(ocData, cfg, get, log); err != nil {
		return err
	}
	if err := writeJSONFile(cfgPath, ocData, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	log(fmt.Sprintf("[ok] %s 已更新(agents.list + mcp.servers)", cfgPath))

	// 6) creds.json:多源场景下任一源属于 apollo/consul/env-vars/k8s 就要写
	needsCredsFile := false
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if needsCreds(cc.Type) {
			needsCredsFile = true
			break
		}
	}
	if needsCredsFile {
		credsPath := filepath.Join(ocHome, agentID+"-creds.json")
		credsData, _ := readJSONOrEmpty(credsPath) // 旧的合并而非覆盖,允许多 agent 共存
		writeCredsByType(credsData, cfg, get)
		if err := writeJSONFile(credsPath, credsData, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", credsPath, err)
		}
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
	if opts.SkipGatewayRestart {
		log("[skip] gateway restart 被显式跳过(测试模式)")
	} else if _, err := exec.LookPath("openclaw"); err == nil {
		c := exec.CommandContext(ctx, "openclaw", "gateway", "restart")
		if err := c.Run(); err != nil {
			log(fmt.Sprintf("[warn] openclaw gateway restart 失败:%v;手动跑一遍", err))
		} else {
			log("[ok] openclaw gateway 已重启")
		}
	} else {
		log("[warn] 未检测到 openclaw CLI,跳过 gateway 重启;请手动 `openclaw gateway restart`")
	}
	return nil
}

// ── 内部辅助 ─────────────────────────────────────────────────────────────

// loadCfgFromTshoot 从 dir 找 tshoot.json(两个候选位置)反读出 cfg + meta。
// 两个位置:
//   - <dir>/tshoot.json                              ← claude-code/cursor staging,或已部署的 openclaw workspace
//   - <dir>/templates/workspace-template/tshoot.json ← openclaw staging
//
// openclaw 故意在子目录写,避免 discover.Scan 扫到 staging 时跟已装 workspace
// 重复。所以查询时也分两路。
func loadCfgFromTshoot(dir string) (*config.SystemConfig, discover.Meta, error) {
	candidates := []string{
		filepath.Join(dir, discover.MetaFilename),
		filepath.Join(dir, "templates", "workspace-template", discover.MetaFilename),
	}
	var (
		data []byte
		err  error
	)
	for _, p := range candidates {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, discover.Meta{}, fmt.Errorf("read tshoot.json under %s: %w", dir, err)
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, meta, fmt.Errorf("parse tshoot.json: %w", err)
	}
	cfg, err := config.LoadFromBytes([]byte(meta.SystemYAML))
	if err != nil {
		return nil, meta, fmt.Errorf("system.yaml in tshoot.json invalid: %w", err)
	}
	return cfg, meta, nil
}

func readJSONOrEmpty(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid JSON at %s: %w", path, err)
	}
	return out, nil
}

func writeJSONFile(path string, data any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	enc, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, enc, mode)
}

// injectAgent 把 agent 注入 openclaw.json 的 agents.list,已存在(按 id 匹配)就不重复加。
func injectAgent(root map[string]any, id, name, model, workspace string) error {
	agents, _ := root["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
		root["agents"] = agents
	}
	listAny := agents["list"]
	list, _ := listAny.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if existID, _ := m["id"].(string); existID == id {
				return nil
			}
		}
	}
	list = append(list, map[string]any{
		"id":        id,
		"name":      name,
		"model":     model,
		"workspace": workspace,
	})
	agents["list"] = list
	return nil
}

// injectMCPServers 按 cfg 的 infra 开关往 mcp.servers map 里塞每条 MCP 配置。
// 全量重写匹配前缀的旧条目(避免 env 删了 / 切了 config-center 类型留下死引用)。
//
// 命名加 agent-id 前缀(如 truss-bot-nacos-mcp-server-prod),跟 Claude Code/Cursor 的
// install_native_mcp.buildMCPServersForCfg 三平台命名统一,routing config-map.yaml 里
// mcp_server 字段三平台共用同一个值。多个 system 共存同一台机器不撞名。
func injectMCPServers(
	root map[string]any,
	cfg *config.SystemConfig,
	get func(string) string,
	log func(string),
) error {
	agentID := cfg.ResolveID()
	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		root["mcp"] = mcp
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		mcp["servers"] = servers
	}

	envs := cfg.Environments

	// 多源配置中心:nacos 类型逐源 × env 注册独立 MCP 实例。
	// k8s/env-vars/apollo/consul 不走 MCP(走 creds.json + 配套 python 脚本)。
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range envs {
			env := map[string]any{
				"NACOS_ADDR":     get(envVar("CC_ADDR", cc.ID, e.ID)),
				"NACOS_USERNAME": get(envVar("CC_USER", cc.ID, e.ID)),
				"NACOS_PASSWORD": get(envVar("CC_PASS", cc.ID, e.ID)),
			}
			servers[mcpKeyForAgent(agentID, "nacos-mcp-server", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env":     env,
			}
		}
	}

	// grafana per env
	gf := cfg.Infrastructure.Observability.Grafana
	if gf.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			}
			servers[mcpKeyForAgent(agentID, "grafana-mcp-server", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": env,
			}
		}
	}

	// loki per env(走 grafana mcp 但禁用 dashboard/datasource 那批 capability)
	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			}
			servers[mcpKeyForAgent(agentID, "loki-mcp-server", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-search", "--disable-dashboard", "--disable-datasource",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": env,
			}
		}
	}

	// jaeger per env(没独立 MCP,只记 URL 给 agent 直查)
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[mcpKeyForAgent(agentID, "jaeger", "", e.ID)] = map[string]any{
				"command": "curl",
				"args":    []any{},
				"env": map[string]any{
					"JAEGER_URL": get("JAEGER_URL_" + up),
				},
				"_note": "Jaeger 无独立 MCP；此条目仅记录 URL 供 agent 通过 curl/HTTP 直查",
			}
		}
	}

	// elk per env
	if cfg.Infrastructure.Observability.ELK.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[mcpKeyForAgent(agentID, "elk", "", e.ID)] = map[string]any{
				"command": "curl",
				"args":    []any{},
				"env": map[string]any{
					"KIBANA_URL":  get("KIBANA_URL_" + up),
					"ES_URL":      get("ELK_ES_URL_" + up),
					"ES_USERNAME": get("ELK_USERNAME"),
					"ES_PASSWORD": get("ELK_PASSWORD"),
				},
				"_note": "ELK 无独立 MCP；此条目仅记录 URL 供 agent 直查 ES API",
			}
		}
	}

	// messaging:lark
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			lk := "lark-openapi"
			if agentID != "" {
				lk = agentID + "-" + lk
			}
			servers[lk] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuite/lark-openapi-mcp"},
				"env": map[string]any{
					"APP_ID":     get("LARK_APP_ID"),
					"APP_SECRET": get("LARK_APP_SECRET"),
				},
			}
			break
		}
	}

	// project tracking:feishu_project
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			fk := "FeishuProjectMcp"
			if agentID != "" {
				fk = agentID + "-" + fk
			}
			servers[fk] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@lark-project/mcp", "--domain", "https://project.feishu.cn"},
				"env": map[string]any{
					"MCP_USER_TOKEN": get("MCP_USER_TOKEN"),
				},
			}
			break
		}
	}
	_ = log
	return nil
}

// needsCreds 决定是否需要写 <agent_id>-creds.json:nacos 不需要(已经在 mcp 里),
// 其它类型(apollo/consul/env-vars/kuboard)需要,因为 agent 直读这份 json,不走 MCP。
func needsCreds(ccType string) bool {
	switch ccType {
	case "apollo", "consul", "env-vars", "kuboard":
		return true
	}
	return false
}

// writeCredsByType 多源场景按"类型分顶层 section"组织 creds.json:
//
//	{
//	  "apollo":     { "<source-id>": { "<env>": {meta_url,token} } },
//	  "consul":     { "<source-id>": { "<env>": {host,token} } },
//	  "static":     { "<source-id>": { "<env>": {redis,mysql,...} } },
//	  "kubernetes": { "<source-id>": { "<env>": {context,namespace,...} } },
//	}
//
// 同一类型多源会平铺到同 section 下,以 source.id 二级 key 区隔。
// 单源迁移路径(source.id == "default")保留老两层结构 {<env>: ...} 兼容,
// 让老 agent 脚本无感升级。
func writeCredsByType(creds map[string]any, cfg *config.SystemConfig, get func(string) string) {
	envs := cfg.Environments

	for _, cc := range cfg.Infrastructure.ConfigCenters {
		switch cc.Type {
		case "apollo":
			writeCredsSection(creds, "apollo", cc, envs, func(e config.Environment) map[string]any {
				return map[string]any{
					"meta_url": get(envVar("APOLLO_META", cc.ID, e.ID)),
					"token":    get(envVar("APOLLO_TOKEN", cc.ID, e.ID)),
				}
			})
		case "consul":
			writeCredsSection(creds, "consul", cc, envs, func(e config.Environment) map[string]any {
				return map[string]any{
					"host":  get(envVar("CONSUL_HOST", cc.ID, e.ID)),
					"token": get(envVar("CONSUL_TOKEN", cc.ID, e.ID)),
				}
			})
		case "env-vars":
			writeCredsSection(creds, "static", cc, envs, func(e config.Environment) map[string]any {
				envSection := map[string]any{}
				for _, ds := range cfg.Infrastructure.DataStores {
					if !ds.Enabled {
						continue
					}
					envSection[ds.Type] = get(envVar("STATIC_"+strings.ToUpper(ds.Type), cc.ID, e.ID))
				}
				return envSection
			})
		case "kuboard":
			// kuboard:走 Kuboard HTTP API。每 env 一份连接凭证(url + 鉴权);
			// 鉴权二选一:access_key(API 访问凭证,推荐)或 username+password。两条都写入 creds.json,
			// 让 bot 运行时按"access_key 优先"取用。cluster/namespace/configmap 是 per-service,
			// 从 cc.ServiceMap 落到 service_map 子字段。
			writeCredsSection(creds, "kuboard", cc, envs, func(e config.Environment) map[string]any {
				row := map[string]any{
					"url":        get(envVar("KUBOARD_URL", cc.ID, e.ID)),
					"username":   get(envVar("KUBOARD_USER", cc.ID, e.ID)),
					"password":   get(envVar("KUBOARD_PASS", cc.ID, e.ID)),
					"access_key": get(envVar("KUBOARD_ACCESS_KEY", cc.ID, e.ID)),
				}
				if envSvcMap, ok := cc.ServiceMap[e.ID]; ok && len(envSvcMap) > 0 {
					svcMap := map[string]any{}
					for svc, entry := range envSvcMap {
						svcMap[svc] = map[string]any{
							"cluster":   entry.Cluster,
							"namespace": entry.Namespace,
							"configmap": entry.ConfigMap,
						}
					}
					row["service_map"] = svcMap
				}
				return row
			})
		}
	}
}

// writeCredsSection 把一个源的 (env → fields) 写到 creds[topKey] 下。
// 单源迁移(cc.id == "default"):保留老两层结构 creds[topKey][env] = fields(向后兼容)。
// 显式多源:三层结构 creds[topKey][source.id][env] = fields。
func writeCredsSection(
	creds map[string]any,
	topKey string,
	cc config.ConfigCenter,
	envs []config.Environment,
	rowFn func(config.Environment) map[string]any,
) {
	if cc.ID == "" || cc.ID == "default" {
		section := map[string]any{}
		for _, e := range envs {
			section[e.ID] = rowFn(e)
		}
		creds[topKey] = section
		return
	}
	// 多源:已有 section 合并(同 topKey 下不同源共存)
	top, _ := creds[topKey].(map[string]any)
	if top == nil {
		top = map[string]any{}
		creds[topKey] = top
	}
	bySource := map[string]any{}
	for _, e := range envs {
		bySource[e.ID] = rowFn(e)
	}
	top[cc.ID] = bySource
}

// copyDirAll:整目录拷贝,保留 mode。dst 必须不存在(由调用方保证)。
func copyDirAll(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := copyFileSimple(p, target); err != nil {
			return err
		}
		// 保留 exec 位:Linux 上某些 skill 脚本要可执行
		if info, err := d.Info(); err == nil {
			_ = os.Chmod(target, info.Mode())
		}
		return nil
	})
}

// readEnvFileSimple 跟 deploy.ReadEnvFile 一样的格式,内联避免循环依赖。
func readEnvFileSimple(stagingDir string) (map[string]string, error) {
	envPath := filepath.Join(stagingDir, "scripts", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// 去外层引号 + bash '\'' 转回 '
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			quote := val[0]
			val = val[1 : len(val)-1]
			if quote == '\'' {
				val = strings.ReplaceAll(val, `'\''`, `'`)
			}
		}
		out[key] = val
	}
	return out, nil
}

// writeEnvFileSimple 跟 deploy.WriteEnvFile 同格式;空 map 等同 no-op。
func writeEnvFileSimple(stagingDir string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	envDir := filepath.Join(stagingDir, "scripts")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(envDir, ".env")
	var sb strings.Builder
	sb.WriteString("# 由 tshoot 桌面端写入。编辑前先备份。\n")
	sb.WriteString("# 删除此文件 = 下次 import 不再预填,需重新输入凭证。\n\n")
	for k, v := range kv {
		if k == "" {
			continue
		}
		escaped := strings.ReplaceAll(v, "'", `'\''`)
		fmt.Fprintf(&sb, "%s='%s'\n", k, escaped)
	}
	return os.WriteFile(envPath, []byte(sb.String()), 0o600)
}
