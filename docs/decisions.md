# Decisions Log

本项目重大设计决策的演进记录。**不是 changelog**(那是 git log 的事),而是「为什么是现在这样,我们考虑过什么,踩过什么坑」。读 commit message 拼不出来整个故事时翻这里。

格式仿 ADR(Architecture Decision Record),每条 4 段:**背景 → 决策 → 后果 → 演进**。新的决策**追加在后面**,**不改老条目**(过时了就标 SUPERSEDED 指向新条目)。

---

## 2026-04 · 两层产品定位

**背景**:项目刚起步时定位模糊 —— 是"桌面 app"还是"CLI"还是"机器人本身"?

**决策**:两层架构。
- **上层(本仓库)**:研制环境。CLI(`tshoot`)/ 桌面 app / HTTP server **三入口并列**,共享 internal/ 包。
- **下层(产出物)**:`tshoot apply` 生成一个**独立可运行的排障机器人** —— skills + MCP + 话术,脱离 studio 在用户的 OpenClaw / Claude Code / Cursor / Codex CLI 里跑。

**后果**:
- 修一个 build 逻辑要 verify 三个入口都没坏(测试覆盖三套)。
- 产出物可独立分发(用户改完 yaml `tshoot apply` 产物丢运维同事)。

---

## 2026-05-15 · MCP 软约束哲学(用户校准)

**背景**:本会话审计每家 mcp 时发现 mysql / redis / kafka / postgres / grafana 等都**暴露写工具**(insert/update/delete/produce_message/update_dashboard 等)。最初打算在 install 层硬约束 —— 给上游包传 `--read-only` / `--disable-write` / `ALLOW_*_OPERATION=false` 等 flag,从 mcp 层禁掉写工具。

**决策**:**不在 mcp 层硬约束,改用 SKILL 文档软约束**(LLM prompt 级"严禁调用 X 工具")。

**理由**(用户校准):
1. 硬约束失去**紧急人工指令**口子 —— 用户在 IDE 里明确说"清一下这条脏数据"时机器人也调不了
2. 现代 LLM + prompt engineering 已经能可靠不调被 SKILL 标"禁调"的工具(参考 kafka produce_message 实战)
3. 简化 install 流程,少一层开关 = 少一类配置错配

**后果**:
- 每家 mcp builder 不传 `--read-only` 等 flag,workspace SKILL 文档加"软约束清单"
- routing/SKILL.md.tmpl 加专门段「grafana mcp 写工具软约束」
- mongo `--read-only` 是例外(已有 flag,不主动去掉),但 SKILL 说明书 "工具仍在 tool list 暴露,只是运行时拦截"

**演进**:目前应用到 mysql / redis / kafka / postgres / mongo / grafana / clickhouse 7 家。如果将来某次 prompt injection / hallucination 导致真实事故,可重新考虑。

---

## 2026-05-15 · Nacos 接入回归方案 B(HTTP API 主路径)

**背景**:本项目 nacos 接入演进 3 轮,每轮都把上一轮的方案撕掉:

- **5d5a139**:`config-executor` SKILL 走 HTTP API(`scripts/nacos_config.py`),临时绕路修 `nacos-mcp-router` 能力错配(router 是市场管理工具不读 KV)
- **23d503a**:换 `nacos-mcp-server` 做 MCP 主路径 + HTTP fallback。install 阶段一次性 login 拿 token bake 到 mcp 启动参数,要求 nacos 端调长 `nacos.core.auth.plugin.nacos.token.expire.seconds` 到 10 年
- **8d05068(终局)**:回 HTTP API 主路径,**完全删 nacos mcp 注册**

**为什么 23d503a 那条路走不通**(三层根因):
1. 官方 `nacos-mcp-server` 只接 `--access_token` CLI,token 在 mcp 进程生命周期内固定,LLM 收 401 没办法重启 mcp 换 token
2. truss 现场 nacos 是 2.3.0 + 运维不调 TTL,token 5h 过期后 mcp 401,bake 方案沦为"装一次满血几小时然后默默降级到 fallback"
3. 跟产品定位「AI 排障机器人长期跑」根本冲突 — install 永远是个 patch,不是终局

**决策**(方案 B):
- 砍掉 `buildNacos` 整段,install 阶段**不再注册 nacos mcp**
- `config-executor` SKILL 主路径走 `scripts/nacos_config.py`,每次调用脚本自己 login → 用 token → 丢弃,token TTL 短长无所谓
- 凭据从 `<workspace>/scripts/.env` 读(`CC_ADDR_<ENV>` / `CC_USER_<ENV>` / `CC_PASS_<ENV>`)
- 同时清理 apollo / consul 的 `mcp_server` 字段死引用(`BuildMCPServers` 从来没实现过 apollo/consul mcp,但 routing 模板渲染了这个字段,LLM 按字段调撞 ENOENT)

**后果**:
- LLM 失去 mcp 原生 tool-call 体验(nacos 调用要写一行 bash 跑 python),但 nacos 在排障里调用频次低(每 session 1-3 次),代价可接受
- 凭据流向:wizard → install 写 `scripts/.env` → SKILL `source scripts/.env` 取值传给脚本 `--server / --username / --password`
- routing config-map.yaml 加 `runtime: nacos-http` / `runtime: apollo-http` / `runtime: consul-http` 字段(同 `runtime: kuboard-http` 同款模式)

**演进**:如果将来 nacos 出官方支持 username/password 自动 refresh 的 mcp,可重新走 mcp 主路径。

---

## 2026-05-15 · feishu_project 禁用 mcp 注册 + 停收凭据

**背景**:install 注册了 `@lark-project/mcp v0.0.1`,但实际是字节内部 prototype 包:
- npm 元数据 repository / homepage / readme **全空**(maintainer 全 `@bytedance.com` 个人邮箱)
- 主版本号 0.0.x → 稳定性 / 工具集 / 协议都可能未来 break
- 架构是 stdio→HTTPS 透明代理(转发 `https://project.feishu.cn/mcp_server/v1`),工具集**完全由飞书服务端控制**
- `MCP_USER_TOKEN` 经 `X-Mcp-Token` header 传,从命名看是 user-scoped token,飞书规范 2h 过期,bake 同 nacos 失效坑

**决策**(B 方案):
- `buildFeishuProject` warn + skip 注册(yaml schema 保留 `platform=feishu_project` 作合法值,等正式版)
- `install_prompts.go` **停收** `MCP_USER_TOKEN`(收完没人用 = 诈骗式收集)
- wizard askBool 文案加"实验性,目前不会真接入"提示
- `answers.go` 即便 `FeishuProjectEnabled=true` 也渲染 `enabled: false` + 注释

**等条件**(未来重启用):
- 字节发 v1.x 正式版(有 README + 公开 repo + 长期 token 续期机制),**或**
- 真有项目跟踪需求 → 补 `feishu-project-query` SKILL + `scripts/feishu_project.py` 调飞书项目 OpenAPI(同 nacos 方案 B 模式)

---

## 2026-05-15 · rabbitmq mcp 禁用注册(上游两候选都坏)

**背景**:runtime probe(stdio handshake + tools/list)实测两个 PyPI 候选都跑不起来:

1. **`amq-mcp-server-rabbitmq`** (AWS amazon-mq 维护):源码 line 9 写死 `from fastmcp.server.auth import BearerAuthProvider`,但 fastmcp 任何版本(2.7 / 2.14.7 / 3.3 都验过)的 `fastmcp.server.auth` 都**没有** `BearerAuthProvider` 这个 export(大概率早期改名为 `JWTVerifier` 等)。`uvx --with "fastmcp==2.14.7"` 硬钉 2.x 最新也撞 ImportError → 死局。GitHub main 分支同款 broken,0 issue 反馈
2. **`rabbitmq-mcp-server`** (guercheLE 社区):依赖声明缺一堆 — 撞 tabulate / tomli / requests 全 `ModuleNotFoundError`,补丁堆补丁

**决策**(同 nacos / feishu_project 方案 B):
- `buildRabbitMQ` warn + skip 注册
- SKILL `rabbitmq-runtime-query` 主路径走 RabbitMQ HTTP Management API(端口 15672,RabbitMQ 团队官方维护极稳定)
- curl + Basic Auth 覆盖所有排障查询(队列长度 / consumer lag / exchange 绑定 / 节点健康 / alarm 状态),凭据从 install creds 的 `RABBITMQ_USER_<ENV>` / `RABBITMQ_PASS_<ENV>` 读

**等条件**:社区出能跑通的 rabbitmq mcp 包且工具集对得上排障需求。

---

## 2026-05-15 · postgres mcp 包迁移(@modelcontextprotocol → @henkey)

**背景**:`@modelcontextprotocol/server-postgres` 维护者 2025-07 明确 archive 不再修。之前不换的唯一理由是它默认 `READ ONLY transaction` 包裹查询。

**决策**:换 `@henkey/postgres-mcp-server v1.0.5+`。
- 跟 mysql / redis / kafka 同款 SKILL 软约束哲学,READ ONLY transaction 那条理由不成立
- env-based(`POSTGRES_CONNECTION_STRING`)→ 凭据不再落 args(`~/.claude.json` 不再泄漏 DSN)
- 17 个工具(consolidated)排障表达力增强:`pg_execute_query` / `pg_monitor_database` / `pg_debug_database` / `pg_analyze_database` / `pg_manage_query` 等
- SKILL 软约束禁调 `pg_execute_mutation` / `pg_execute_sql`(可写)

**建议同时下沉到 PG 端**:DSN 用只读 role,即便 LLM 误调 mutation,PG 端也会拒(软约束 → PG 端硬约束兜底,不强制要求)。

---

## 2026-05-15 · MCP probe 工程化进 self_test_openclaw

**背景**:本会话 rabbitmq fastmcp 案例暴露:install 显示 "success" 仅代表 mcp.servers 注册到 IDE config,**不代表 mcp 进程能起、tools 能列**。truss 现场 nacos / 本次 rabbitmq 都是这类 silent failure,靠人工 probe 才发现。

**决策**:把审计期间手写的 `/tmp/mcp-probe/probe.py` 工程化进 Go 原生 self-test:
- 新文件 `internal/agent/self_test_mcp_probe.go` 实现 stdio MCP client(initialize + notifications/initialized + tools/list)
- 集成进 `SelfTestOpenclaw`,对每家 mcp 跑 probe,PASS / WARN / FAIL 三档输出
- timeout 60s 给 npx/uvx 冷启动 + handshake 留 headroom
- 不真调任何工具(无副作用),不强制工具名清单跟 SKILL 对照(doc 漂移检测是单独工具的事)
- 顺带覆盖凭据热验证:凭据错 → 进程起不来或 tools/list 拒绝 → probe FAIL 自动暴露

**后果**:下一次某家 mcp 上游包升级 break(类比 amazon-mq 撞 fastmcp 3.x),self-test 立刻 FAIL 给具体 stderr,用户能 1 步定位问题包,不再像 rabbitmq 那次需要人工手写 probe 才发现。

---

## 2026-05-18 · MCP 不注册的两种情况分类校准

**背景**:本会话 5 月 15 日做了一系列"砍 mcp 走 SKILL"决策(nacos / feishu_project / rabbitmq),分类时把这三家都笼统标为"已禁用",造成下游文档(AGENTS.md / CONTRIBUTING.md / decisions.md / 代码注释)反复出现"被禁的 mcp(nacos / feishu_project / rabbitmq)必须有替代访问方式"这类描述。

5 月 18 日用户校准时指出:**rabbitmq 跟 feishu_project 语义完全不同** — rabbitmq 有 HTTP Management API 完整替代(能力可用),feishu_project 无替代凭据也停收(能力当前缺失)。

**决策**:严格区分两种"不注册 mcp"语义:

### 3a 方案 B(有替代,能力完整可用)

上游 mcp 包不可用 / 不适合产品定位,但**有成熟的 HTTP API 替代**走 SKILL 主路径:

| 后端 | 不注册 mcp 理由 | 替代路径 | 凭据流 |
|---|---|---|---|
| nacos | bake token 不能 refresh + 官方包跟"长期跑"定位冲突 | `scripts/nacos_config.py` 每次自己 login | wizard 仍收 `CC_ADDR_<ENV>` 等,写 `<workspace>/scripts/.env`,SKILL `source` 后用 |
| apollo | 生态暂无稳定 MCP 包 | `scripts/apollo_config.py --agent-id --env` | wizard 仍收,写 `~/.openclaw/<agent-id>-creds.json` |
| consul | 生态暂无稳定 MCP 包 | `scripts/consul_config.py --agent-id --env` | 同 apollo |
| **rabbitmq** | 上游两个 PyPI 包都坏(amazon-mq 引用 fastmcp 不存在的 API + guercheLE 缺一堆 dep) | `curl + :15672/api/queues` HTTP Management API(RabbitMQ 官方维护) | wizard 仍收 `RABBITMQ_USER_<ENV>` 等,写 .env / creds.json |

routing/config-map.yaml 标 `runtime: <type>-http` 字段告诉 LLM 走脚本。

### 3b 真禁用(无替代,能力当前缺失)

上游 mcp 包还是 prototype + 我们也没补 HTTP 脚本替代,能力**当前不可用**:

| 后端 | 状态 | 等条件 |
|---|---|---|
| **feishu_project** | mcp 禁(@lark-project/mcp v0.0.1 字节内部 prototype)+ 凭据停收(诈骗式收集)+ wizard 标"实验性,选 Y 也不接入" | 字节发 v1.x 正式版 + 我们补完 SKILL |

**后果**:
- AGENTS.md / CONTRIBUTING.md / 代码注释统一加 3a / 3b 区分,避免后人混淆
- 加新 mcp builder skip 时**先确认是哪类**:有可行的 HTTP API 替代 → 3a(凭据仍收 + SKILL 升主路径);无替代 → 3b(凭据停收 + wizard 加实验性提示)
- self_test_openclaw 的 `requiredMCPKeys` 自动跟着 builder 实现走,不需要因为 3a/3b 区分而额外处理(builder skip 后两类都不进 requiredMCPKeys)

**判别清单**(改 mcp 决策时对照):
- 凭据是否 install_prompts 仍收?→ 仍收 = 3a,停收 = 3b
- SKILL 是否有 HTTP 替代脚本 / 真实 fallback?→ 有 = 3a,无 = 3b
- routing/config-map.yaml 是否标 `runtime: <type>-http`?→ 标 = 3a,不标(平台不在 config_centers 之类映射里)= 3b

**演进**:如果某天 feishu_project 补了 OpenAPI Python 脚本 + SKILL,从 3b 升级到 3a(同 nacos 方案 B);如果上游 rabbitmq mcp 修好了 → 重新走 mcp 路径,从 3a 升回方案 A(install 注册 mcp)。

---

## 2026-05-15 · install_native_mcp_common.go 拆分

**背景**:单文件 1103 行,塞了 14 个 builder + helper + 注释。本会话改了 nacos/postgres/rabbitmq/feishu_project 4 家 mcp,每次都得 grep 巨型文件定位,review/merge conflict 风险高。

**决策**:按 mcp 类型拆 4 个文件:
- `install_native_mcp_common.go` (592 行) — helper + BuildMCPServers 总入口
- `install_native_mcp_obs.go` (128 行) — grafana / jaeger / elk
- `install_native_mcp_data_stores.go` (352 行) — 8 家数据层 + 总分发
- `install_native_mcp_messaging.go` (68 行) — lark / feishu_project

纯重构,行为零变化。`mcpBuilder` 类型(unexported)在 common.go 定义,其它文件用同 package 直接复用。

---

## 2026-05-18 · install 链全栈 timeout 兜底(防 UI"卡死"无终点)

**背景**:用户反复抱怨桌面 app 部署"卡 5+ 分钟永远不结束",反复 killall 重试。日志面板按设计反馈,但用户能等到的"安装在动"信号太弱——只要某一步无 emit,UI 看着就像死锁。系统排查后定位多个**无 timeout / 无 ctx 控制点**:任一卡住整链路无法返回。

**决策**:install 全链路每段配硬上限 timeout,**层层防御**,坏一层下一层兜底:

| 阶段 | timeout | 内部机制 |
|------|---------|---------|
| `fixGUIPath` 拿 login shell PATH | 5s | `exec.CommandContext` 直接 deadline |
| `EnsureKafkaMCPInstalled` 拉 GitHub tarball | 90s | `http.Client.Timeout` |
| `RunAutoAnalyze` 跑 analyzer | 60s | `select + time.After` + ctx 透传 |
| `openclaw gateway restart` | 30s | `context.WithTimeout` 包 ctx |
| `RunInstall` 桌面 binding 总耗时 | 5min | `context.WithTimeout(a.ctx, 5min)` |
| `SelfTestAgent` 桌面 binding 总耗时 | 120s | 同上 |
| MCP probe 单家 | 15s(降自 60s)| 见下条 |

**护栏原则**:每层超时不阻塞 install 本体——比如 `openclaw gateway restart` 超时只让"新 agent 没自动 reload",用户**重启 OpenClaw 客户端**即可,workspace + json + creds 都已落地。`auto-analyze` 超时让 `service-dependency-map / data-schema-map` 留空,可后续 BotsPage 重 gen。

**为什么不一次给整链路统一 timeout**:不同步骤合理时间差异大(gateway restart 几秒 vs auto-analyze 可能 1 分钟+),统一短 timeout 误伤合理慢路径,统一长 timeout 等价没护栏。

**配套**:`install:log` event 全链路 emit(`MergeMCPIntoIDESettings` 加 `emit` helper 走 `onProgress` 而不是 stderr),前端有可见进度;前端 `useDeployFlow.ts` 把 self-test 改异步—— install 完即 toast + 跳 /bots,健康检查后台跑,用户不再盯静默条。

---

## 2026-05-18 · self-test MCP probe 三层根因修复(并发 + 进程组 + 前缀过滤)

**背景**:`[self-test] 开始自检 → 120s 超时退出` 反复出现。120s 是我加的 `SelfTestAgent` 总 timeout 兜底,真因藏在三层:

**第一层 — 串行 + 单 probe 60s timeout**:
`probeMCPServersFromConfig` for 循环跑 17 个 mcp,每个 60s(为 npx 冷启动留余量)= 最坏 1020s。
→ 改并发(goroutine + `sync.WaitGroup`),`safeAdd` 套 `sync.Mutex` 保护 `res.Checks` append。probe timeout 60s → 15s(self-test 是健康检查不替用户跑 cold install,首次起不来直接 FAIL 让用户在 IDE 真用时再冷启动)。
**理论 1020s → 15s**。

**第二层 — `cmd.Wait()` 永远等 npx 孙子进程**:
即使并发,17 个 goroutine **全部卡在 `defer { cmd.Process.Kill(); cmd.Wait() }`**。POSIX 进程模型 + npx 三层 fork(npx → node → npm → mcp),SIGKILL 顶层 npx 后,孙子持有 stdin/stdout pipe → 父 `cmd.Wait()` 等不到子进程退出 → `wg.Done()` 不触发 → `wg.Wait()` 卡 → SelfTestAgent 120s timeout 兜底砍。
→ `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` 把 npx 放独立进程组,defer 时 `syscall.Kill(-pgid, SIGKILL)` 杀整组(负号 pid → 杀进程组),孙子一并 SIGKILL,`cmd.Wait()` 立即返回。
**Windows 不支持 setpgid**:拆 `self_test_mcp_probe_unix.go` (`//go:build !windows`) + `_windows.go` (noop stub) 平台分。
保留 `cmd.Wait()` 1s 上限做 belt-and-suspenders:extreme case 即使 setpgid 没杀干净(trap SIGKILL / fd 给系统服务)也不卡。

**第三层 — self-test 探别家 agent 的 mcp 拖累报告**:
`~/.openclaw/openclaw.json` 是多 agent 共享文件,user 装过 `ai-troubleshooter` + `truss-troubleshooter` → mcp.servers 混存:本系统前缀的(`truss-grafana-dev` 等)+ 无前缀的孤儿(别家 agent / 历史包名退役如 `@larksuite/lark-openapi-mcp` 已 404)。`probeMCPServersFromConfig` 之前不按 `cfg.MCPKeyPrefix()` 过滤,把孤儿一起 probe,1 个 FAIL 拖整体报告。
→ `self_test_openclaw.go:97` 调 probe 前用 `strings.HasPrefix(k, mcpPrefix+"-")` 过滤本系统的 mcp,**self-test 跟"本系统"绑定**,孤儿不拉进来。

**验证**:用户日志 `19:45:08 → 19:45:20 = 11.6s` 完成,28 PASS / 4 WARN / 1 FAIL → 第三层修后 12 PASS / 4 WARN / 0 FAIL,真实健康度可读。

**演进**:如果某天 self-test 要 cross-agent 健康度报告(检查多个 agent 共用资源),回退第三层,但要明确"是 system-level 还是 cross-agent"语义。

---

## 2026-05-18 · auto-analyze cache + ctx 透传(install 砍 75% 重复)

**背景**:用户在 wizard step 10 一键部署 4 个 target(OpenClaw / Claude Code / Cursor / Codex)。`apply.go:94`(IDE target 走 `Apply`)+ `apply.go:258`(openclaw 走 `ImportAndApply`)各调一次 `RunAutoAnalyze`,**4 个 target 重复扫同一份 13 个 repo**——实测每次 ~5s,共 ~20s 浪费。truss 这种大 monorepo 跑满 60s timeout 时,4 次就是 4 分钟。

**决策两步**:

**A. process-level cache**:`internal/agent/auto_analyze.go` 加 `map[cacheKey]*entry`。
- `cacheKey = system.id + "\x1f" + sorted (repo name=path)`,US (Unit Separator `\x1f`) 当字段分隔符避免误命中
- TTL 5min,覆盖一次部署 wizard(实测 <2min)且让用户改 yaml 后强制重扫
- 只缓存成功结果(`err == nil && result != nil`),失败下次 retry 有机会
- 命中时 emit `[info] auto-analyze cache 命中(Xs 前的结果),复用 dependency / schema findings`

**B. ctx 透传**:`analyzerpipe.Run` 加 `ctx context.Context` 首参,for-each-repo 循环顶 `if err := ctx.Err()` 让 step 之间能取消。`RunAutoAnalyzeOptions.Ctx` 字段(妥协:Go 惯例 ctx 不放 struct,但 ApplyOptions 全链 4+ 调用方现有 opts struct 调,加字段而非改全部签名 = minimal invasive)。`RunAutoAnalyze` 内部 derive `runCtx = WithTimeout(opts.Ctx 或 background, 60s)`,真正能 cancel 底层 analyzer(之前只让上层放弃等,goroutine 后台跑死)。

**未做的部分(留 follow-up)**:`analyzer/walker.go` 的 `filepath.WalkDir` 不响应 ctx,单 repo 内部 scan 无法中断——要加 ctx 需要重构 walker.go 函数签名。step 之间取消已覆盖绝大部分场景。

**效果**:install 总耗时 ~25s → ~10s,4 个 target 后 3 次 cache 命中 ~0ms。

---

## 2026-05-18 · codex network_access 从"探测 + warn"改"自动 patch"(推翻前决策)

**前决策**:`ensure_codex_network.go` 不主动改用户全局 `~/.codex/config.toml`(怕动用户文件引连带 bug),只探测 + 打 `[warn]` 给手抄指引。

**演进原因**:每次装 codex agent 用户都得手抄 toml 3 行——重复操作 + 文案显眼让用户以为"装失败"。

**新决策**:`EnsureCodexNetworkAccess` 自动 patch,3 种缺失场景全覆盖:
1. 文件 / 段不存在 → 文件末尾 append 整段
2. 段在缺 key → 段头后插一行 `network_access = true`
3. 段在且 key=false/其它 → 替换那一行的值

**保险措施**:
- 写之前 backup 原文件到 `<path>.tshoot-bak.<YYYYMMDD-HHMMSS>`,改坏可一键 cp 恢复
- 写之后用 `hasNetworkAccessTrue` parser 反向 verify(防 patch 边角逻辑漏洞写出 parser 不认的怪 toml)
- patch 失败降级到原 warn + `codexNetworkHint` 手抄指引(权限问题等极端 case)

**SUPERSEDED 之前决策**:"不动用户全局 config.toml"——新决策接受"用户预期 = troubleshooter agent 能用,sandbox 这层放网不放写盘是排障常态"。

**测试**:`patchCodexNetworkAccess` 5 个 parser 场景 + `EnsureCodexNetworkAccess` 3 个 E2E 场景(`t.TempDir()` 真 IO,verify backup 落盘)。

---

## 历史(SUPERSEDED)

下面记录已被覆盖的历史决策,**不要按这些指引**,留给读 git log 追根溯源的人用。

### ~~2026-05 · nacos-mcp-router(已废弃)~~

SUPERSEDED by [2026-05-15 · Nacos 接入回归方案 B](#2026-05-15--nacos-接入回归方案-bhttp-api-主路径)。
原计划:nacos 走 `nacos-mcp-router` 做主 mcp。
失败原因:`nacos-mcp-router` 是 nacos MCP **市场管理工具**(searchMcpServer / installMcpServer / callMcpServerTool),不暴露 `get_config`,无法读 nacos KV → LLM 撞 silent fallback 到代码 config.yaml 当 runtime 真值(2026-05-15 truss case 第三层根因)。

### ~~2026-05 · nacos-mcp-server + bake token(已废弃)~~

SUPERSEDED by [2026-05-15 · Nacos 接入回归方案 B](#2026-05-15--nacos-接入回归方案-bhttp-api-主路径)。
原计划:换官方 `nacos-mcp-server`(提供 get_config 等只读工具),install 阶段一次性 login 拿 token bake 到 mcp 启动参数,要求 nacos 端配长 token TTL。
失败原因:运维不调 TTL 时 5h 后 mcp 401,跟产品定位「机器人长期跑」根本冲突。
