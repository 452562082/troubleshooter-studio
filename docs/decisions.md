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

> **⚠️ SUPERSEDED by [2026-05-27 · Nacos plan D](#2026-05-27--nacos-plan-d自研本地-mcp运行时-refresh-token)**:方案 B 不再是 nacos 现状。本条所述"删 mcp 注册、HTTP 脚本为主路径"已被 plan D(自研本地 MCP + 运行时 refresh)取代;HTTP 脚本降为 fallback。本条保留作历史。

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
| ~~nacos~~ → **见 plan D** | _(SUPERSEDED 2026-05-27)_ nacos 已改走自研本地 MCP,**重新注册 mcp**;HTTP 脚本降为 fallback | `nacos_mcp.py`(主)/ `scripts/nacos_config.py`(fallback) | 凭据进 MCP env(`NACOS_*`),HTTP fallback 才读 `.env` |
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

## 2026-05-27 · Nacos plan D(自研本地 MCP,运行时 refresh token)

**背景**:[2026-05-15 方案 B](#2026-05-15--nacos-接入回归方案-bhttp-api-主路径) 把 nacos 退回 HTTP 脚本、删掉 mcp 注册,代价是 LLM 失去原生 tool-call 体验(每次 nacos 调用要写一行 bash)。方案 B 那条的「演进」里也留了口子:"如果将来 nacos 出官方支持 username/password 自动 refresh 的 mcp,可重新走 mcp 主路径"。本次没等官方 —— 自己写了一个。

用户问"能不能不改 upstream、有没有更好的方式",评估了三条路:(A) fork 官方 `nacos-mcp-server` 加 refresh、(B) sidecar token 注入代理、(C) 自研本地小 MCP。fork 验证过可行(本地 commit 不 push),但上游 README 明写"v3 only / 要 nacos 3.0+",给它 PR 加 v1 兼容大概率被拒;与其养 fork 等 merge,不如承认这是 truss 私有逻辑留自己仓里。

**为什么 plan D 能走通而 23d503a 不能**:23d503a 的死因是 token 在 **install 阶段**一次性 bake、进程内固定不刷新 → 5h 后 401。plan D 把 token 生命周期管理从 install 挪进 **MCP 进程运行时**:脚本自己拿 username/password 跑 login + 后台按 `tokenTtl*0.8` refresh + 401 强制 re-login。token 短 TTL 完全无所谓,nacos 端零配合。这是真根因修复,不是又一个 patch。

**决策**(plan D):
- 自研 `templates/workspace/skills/config-executor/scripts/nacos_mcp.py`(PEP 723 自含依赖,`uv run --script`;install 时 `EnsureNacosMCPScript` 从 embed extract 到 `~/.tshoot/scripts/`,target 无关稳定路径,仿 `EnsureKafkaMCPInstalled`)
- 重写 `buildNacos`:`uv run --script` 启动,凭据走 MCP env(`NACOS_HOST/PORT/USERNAME/PASSWORD`,不进 args/ps),**不 bake token**
- 仅用 `/v1` endpoint(nacos 2.x/3.x 通用),绕开上游 `/v3 admin` 要 nacos 3.0+ 的限制;只暴露排障实际用的 4 个只读工具(get_config / list_configs / get_config_history / list_config_history)
- 工具参数用 `namespaceId`(跟 config-map / nacos 控制台同名,LLM 零改名;内部映射 v1 wire 的 `tenant`)
- 健壮性:startup login 失败不崩照常 serve(后台 retry);`_request` 对 token-ready 限时等待快速报错不 hang;`httpx(trust_env=False)` 不被内网代理劫持;401 突发锁内 single-flight re-login
- config-map `runtime: nacos-http` → `nacos-mcp`;config-executor / routing SKILL 切 MCP 主路径,保留 `nacos_config.py` 作 MCP 不可用时 fallback

**后果**:
- nacos 重新有原生 tool-call 体验;long-running 机器人不再受 token TTL 约束
- 新依赖:`uv` 必须在 PATH(self-test `checkToolchain` 会查 uvx;缺了 nacos MCP 起不来回落 HTTP)
- 翻转了一批方案 B 的 guard(install / generator / self-test 测试 + AGENTS.md / CONTRIBUTING.md / README 文档)
- 验证:19 个 Python 行为测试 + Go 测试;本地假 nacos 端到端验证 refresh 轮换 + 401 兜底 + namespaceId 落 wire。truss 现场 e2e 待真环境(可用 `NACOS_REFRESH_SECONDS=60` 快速验证不等 5h)

**演进**:若官方 `nacos-mcp-server` 将来支持 username/password 自动 refresh + v1 endpoint,可考虑切回官方包减少自维护;在那之前 plan D 是终态。fork 分支(`~/tmp/nacos-mcp-server-fork`,本地未 push)留作上游 PR 的潜在素材。

---

## 2026-06-26 · 探活 TLS:带凭据默认校验 + 自签 opt-in

**背景**:连通性探活(`dsprobe` / `labelprobe`)一律 `InsecureSkipVerify: true`,但探活会发真凭据(Grafana/ES 的 basic auth、Bearer token)。中间人用伪造证书即可截获这些凭据 —— 跳过校验 = 凭据裸奔。但直接翻成"永远校验"又会打断大量内网自签证书部署的探活(自签在内网很普遍,这也是当初加 `InsecureSkipVerify` 的原因)。

**决策**:**按"是否发凭据"分流**,逃生口走环境变量,**不动 schema/wizard/UI**。
- 无凭据(纯连通性探测):继续跳过校验 —— 没秘密可被偷,自签误报没必要。
- 带凭据:**默认校验证书**(`TLSConfigForProbe(hasCreds=true)` 返 `MinVersion: TLS1.2`,不 skip)。
- 逃生口:确属内网自签 + 带鉴权,用户 `export TSHOOT_INSECURE_TLS=1` 显式放行。探活撞 x509 错时,错误信息直接提示这个开关。

**后果**:
- 统一 helper `dsprobe.TLSConfigForProbe` + 常量 `dsprobe.InsecureTLSEnv`,`probe_http.go` / `probe_es.go` / `labelprobe/loki.go` 三处复用(labelprobe 反向 import dsprobe,无环)。
- 行为变更:之前自签 + 带鉴权能直接探通的用户,现在需 export 一次环境变量。属有意为之(安全默认优先,逃生口兜底)。
- 验证:`internal/dsprobe/tls_test.go` 锁定三态(无凭据 skip / 带凭据校验 / opt-in 放行)。

**演进**:`cmd/tshoot-desktop/bindings_{one2all,kuboard,kuboard_configmap}.go` 的 runtime 数据拉取(发 one2all Bearer / kuboard accessKey)同样复用 `dsprobe.TLSConfigForProbe(true)`,行为统一(默认校验 + 同一 opt-in 开关)。`bindings_kuboard_v3_live_test.go` 是手动 live test,保留原 skip 不动。若将来要做"自签证书指纹 pin"而非全放行,可在 helper 里扩展。

---

## 2026-06-26 · 排障链路两处脱节修复 + skill 脚本链护栏

**背景**:审计产出物(机器人)排障链路时发现两处"文档/格式 vs 实际脱节"(项目反复踩、但 self-test 只 probe MCP 没覆盖到 skill 脚本链):

1. **cascade_check 解析器吃不下生成器产出**:`service-dependency-map.yaml.tmpl` 渲染的是 **block 列表**(`downstream:` 换行 `- x`),但 `cascade_check.py` 的 `parse_dep_map` 只认 inline `[a,b]`,block 被静默跳过 → 自动生成的依赖图 downstream 永远解析为空 → incident-investigator **Step 4(沿依赖图追下游)对几乎所有部署静默空转**。runtime 实测确认。
2. **incident-investigator 脚本路径漂移**:Step 2/3 写 `scripts/timeline.py` / `scripts/k8s_query.py`,但这俩脚本在 `recent-changes/` 和 `k8s-runtime-query/` 的 scripts 目录,不在 incident-investigator/scripts/ → 机器人按文档跑 file-not-found(脚本自身用 `detect_workspace_root` 健壮,纯文档错)。

**决策**:
- `parse_dep_map` 同时支持 block + inline 列表(block 是生成器默认产出,**核心路径不是 nice-to-have**)。
- incident-investigator 5 处脚本调用统一成 workspace 根相对 `skills/<owning-skill>/scripts/<f>.py`(跟 recent-changes SKILL 既有约定一致);Step 2/3 依赖的 recent-changes / k8s-runtime-query 是可选 skill,加 `{{if hasSkill}}` 守卫,被 whitelist 砍掉时走手动降级 else 分支,不渲染破引用。
- **补 CI 级护栏**(self-test 只覆盖 MCP 的空白):
  - `internal/generator/chain_integrity_test.go::TestSkillScriptPathsExist` —— 渲染 fixture,遍历所有生成的 SKILL.md,断言引用的每个脚本路径真实存在(两场景:全 skill + 守卫场景)。
  - 同文件 `TestDepMapParserHandlesGeneratedFormat` —— exec python3 跑真实 `parse_dep_map` 吃 block 样本,锁死"生成器格式 ↔ 解析器"契约(无 python3 自动 skip)。
  - `test_cascade_check.py` —— parser block/inline/空列表的 idiomatic 单测。

**后果**:Step 4 级联追下游对自动生成依赖图恢复有效;按文档跑脚本不再 file-not-found;脚本路径漂移 / 格式脱节今后 `go test` 直接拦下。

**演进**:cascade_check 仍假设下游同 namespace(`--namespace-default`),多 ns 部署会漏跨 ns 下游(脚本注释自承局限),留作后续(需 dependency-map 扩 per-service namespace 字段)。`parse_dep_map` 是手写文本解析器(刻意不引 PyYAML),若将来 dependency-map 形态更复杂可考虑切 PyYAML。

---

## 2026-07-03 · 桌面端关闭隐藏 + 菜单栏/托盘后台入口

**背景**:工作台是 Wails v2 桌面壳。用户希望关闭窗口后仍能在后台运行,并可从小图标重新打开。直接引入 `github.com/getlantern/systray` 的 macOS 实现会和 Wails 同时定义 Objective-C `AppDelegate`,链接时报 `duplicate symbol _OBJC_CLASS_$_AppDelegate`。

**决策**:
- Wails 主窗口启用 `HideWindowOnClose`,点击关闭按钮只隐藏窗口,不退出进程。
- macOS 不使用第三方 systray,改用本仓库轻量 Objective-C `NSStatusItem` 实现菜单栏入口,避免接管 AppDelegate。
- Windows 使用 `github.com/getlantern/systray` 注册托盘菜单;Linux 先 no-op,避免 CI/用户环境必须安装 `gtk3` / appindicator 开发库。
- 托盘/菜单栏只提供两个动作:`打开工作台` 和 `退出`,不引入开机自启、最小化到托盘设置项等额外行为。

**后果**:
- 桌面端可以关闭窗口后继续运行;后台部署、扫描、自测等长任务不会因窗口关闭而被中断。
- macOS 菜单栏状态项显示 `TS`,菜单项负责重新显示主窗口或真正退出应用。
- Windows 新增 systray 依赖;该依赖仅由 windows build tag 文件引用,不会让 Linux Go 测试依赖托盘系统库。
- 新增 `newDesktopOptions` 单测锁定关闭隐藏行为,桌面编译验证覆盖 macOS 原生状态项链接。

**演进**:若 Wails v2 后续暴露稳定的系统托盘配置入口,可用 Wails 官方入口替换当前 macOS `NSStatusItem` 和 Windows systray 分支,但需要先确认不会重新引入 AppDelegate 冲突。

---

### 2026-07-10 · CodeGraph 作为可选故障期代码图谱，保留 analyzer 主路径

**背景**：现有 regex analyzer 缺少符号、调用和影响面证据；CodeGraph 是 Tree-sitter/SQLite/MCP 能力，不是 LSP。

**决策**：CodeGraph 仅显式 opt-in；使用一个共享 MCP 和 `projectPath`，固定 v1.3.1/SHA，查询前同步，self-test 将 MCP probe 与 index probe 分开，并关闭遥测。

**拒绝方案**：每仓库一个 MCP、嵌入 analyzer、自动 checkout/worktree、启用隐藏工具。

**后果**：增加 200 MB+ 磁盘占用和 `.codegraph` 生命周期管理；图边是启发式的，分支不一致时置信度降低；保留稳定的 `rg`/`read` fallback，并按固定版本升级流程维护。

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

---

## 2026-07-11 · 跨仓库端点目录作为服务拓扑真源

**背景**：系统配置能列出多个代码仓库，但仓库名和粗粒度依赖不足以回答“前端哪个请求经过哪个 BFF 接口，再进入哪个后端入口”。CodeGraph 擅长单仓库符号和调用关系，不能仅凭多个独立索引可靠推断跨服务网络边；运行时 trace 又不保证在部署建模阶段可用。排障机器人需要一份离线可重建、能解释每条跨仓库边且允许人工校准的服务拓扑。

**决策**：以各仓库扫描得到的标准化端点目录为机器真源。扫描器产出带方向、协议、方法/路径或 RPC 方法、目标提示和源码位置的 endpoint；确定性 matcher 只根据规范化路由、服务/主机别名和白名单 transform 生成候选并给出固定理由，不使用 LLM 评分。人工在工作台确认、拒绝或补录后，只把决定持久化到 `service_topology.overrides`；每次分析将 override 合并到可重建证据中，人工决定优先。正式图只接纳 `automatic`、`confirmed`、`manual`，完整 endpoint、candidate、rejected、stale 和仓库失败状态保留在 evidence 文件。

为兼容现有排障链，`service-dependency-map.yaml` 不再独立推断，而由同一正式服务图投影；查询按入口路由最多遍历三跳、截断环，并把端点位置和理由交给后续仓库内分析。启用 CodeGraph 时，服务拓扑先选定仓库和入口端点，CodeGraph 再用明确的 `projectPath` 做仓库内符号、调用链和影响面取证。

**拒绝方案**：

- 仅用跨仓库正则搜索字符串：无法稳定区分调用端和接收端，也无法表达歧义、人工决定与过期证据。
- 把所有仓库交给一个完整代码图自动推边：跨进程 HTTP/gRPC 边通常不在语言调用图中，成本更高且仍需服务发现、网关改写和人工语义。
- 继续维护独立的粗粒度 dependency map：会让机器人导航图与端点证据漂移，无法解释某条服务边来自哪个接口。

**后果**：获得可离线回归、可解释且可人工治理的跨仓库正式图；单仓库失败只降级为 partial evidence，不阻断部署。代价是每种语言、框架和网关写法都需要持续维护 scanner fixture；服务别名、rewrite 规则变化后可能产生 candidate 或 stale override，使用者需要在工作台复查。异步消息拓扑暂不纳入首版，运行时 trace 仍高于静态拓扑，静态查询失败时保留 `routing` / `rg` / 文件读取 fallback。

---

## 2026-07-12 · SQLite 状态机作为故障闭环真源

**背景**：验证、排障和修复原先由 `InvestigationRun` 与 Agent 最终文本串联。流程跨越用户授权、多个 Git 仓库、人工部署和应用重启后，文本无法可靠表达当前阶段、幂等身份、授权范围与已经发生的副作用；整文件 `runs.json` 也无法提供事务和乐观并发控制。

**决策**：Studio 以本地 SQLite 保存 IncidentCase、PhaseAttempt、证据索引、代码变更、授权、部署观察和追加式 TransitionEvent，并由 `CaseOrchestrator` 作为唯一写入口。Agent 是结构化的单阶段 runner，不能从自由文本隐式跨阶段。修复和环境分支合并分别需要一次持久化用户授权；合并授权精确绑定修复 commit、目标分支和目标 HEAD。Studio 只合并并推送环境分支，不控制部署；人工通知部署完成后，manual、HTTP 或 K8s 只读 verifier 必须证明当前环境包含全部目标 commit，才能复用验证 Agent 运行带新鲜证据要求的回归。旧 `runs.json` 一次性导入为只读 `legacy_archived` Case，不从历史文本推断可恢复状态。

**拒绝方案**：继续从 prompt/最终文本推断状态，无法保证重启恢复、事务边界和副作用幂等；引入 Temporal、Argo 等外部工作流引擎，虽然适合分布式长任务，但给当前单机桌面产品增加额外服务、部署和运维成本。

**后果**：Case 可跨进程恢复，重复按钮和通知可安全重放，两个授权和每次外部动作都有审计证据；多仓库部分成功、push 不确定和版本不匹配不会被伪装成成功。代价是 Studio 必须坚持 one-writer 编排、维护 SQLite schema migration 和兼容性测试，并为事务崩溃窗口、幂等冲突、secret redaction、Git 临时 remote、部署版本与回归证据新鲜度持续维护离线回归套件。大证据文件仍在受控 artifact 目录，SQLite 只保存元数据和摘要。

**演进**：若未来产品变为多节点服务或需要跨机器长周期调度，再以现有事件和幂等契约评估外部工作流引擎；在单机 Studio 边界内，SQLite 继续作为闭环真源。

---

## 2026-07-12 · 故障闭环副作用恢复与运行时验证安全加固

**背景**：全分支审查发现四个闭环边界：首次 `reproduced` 可缺少回归基线；修复 Agent 可能在 push 后、完成回调前崩溃；HTTP 版本接口可能被配置为本机/metadata SSRF；部署提醒仅凭环境名猜生产属性。公共 artifact 入口也缺少与 Agent staging 一致的大小上限。

**决策**：`reproduced` 强制要求完整 observed/expected behavior 和至少一份当前 attempt 已注册 artifact，旧 Case 缺基线时从 `deployment_verified` fail-safe 到 `waiting_evidence`，并把 continuation 指回 validation；选择回归基线时跳过无 artifact 的旧 attempt。SQLite schema v4 新增 `fix_checkpoints`，只登记 attempt 绑定的 opaque staging locator；修复 Agent push 前写 `prepared` manifest，push 后写 `pushed`，正常完成和重启恢复都以 SSH remote fix ref 精确等于 commit 为唯一真源。临时 remote unavailable 只做有界 reconcile，保留 running attempt、completion intent 和 checkpoint，不重跑 Agent；明确 ref mismatch 才事务进入 `fix_failed`、消费 checkpoint并清 staging。schema v5 为 attempt 增加完成命令摘要和原子运行 claim：已提交 completion 在任何 Git/证据检查前按完整身份重放，后续可变 CodeChange 不参与重放身份；Start 先预留 runner-owned 取消句柄，再原子 claim runnable attempt 与 fix checkpoint，executor 启动前复核 claim 和 context。调用方 context 只约束同步调度，Start 成功后的长任务由 runner 自身 context 持有，避免编排器调度 timeout 的 defer cancel 误杀 Agent；显式 Cancel 仍可终止。HTTP verifier 默认执行逐 Dial DNS/IP 策略、禁代理和 TLS dial 旁路；环境可用 `allow_private` 对精确配置 host 显式开放内网，但 link-local、已知 metadata、unspecified 和 multicast 永远拒绝；该开关本身进入 verifier canonical snapshot/fingerprint。提醒只读取 `is_prod`，解析失败不提醒。所有 artifact 捕获统一限制 16 MiB，并在 fstat 前检后再做 N+1 有界读取。

**后果**：部署回归不会再因首次证据空洞卡死；push 后崩溃可恢复，local-only/remote 暂不可读保持可恢复但不会推进，remote exact ref 缺失、删除、漂移和多仓不完整会失败关闭；完成回调跨重启或远端后续变化仍可精确幂等重放，取消与启动竞争不会让 executor 越过取消边界。内网版本接口仍有显式可配置出口，但启用 `allow_private` 需要评审对应环境 URL。代价是维护 schema v4/v5 migration、checkpoint manifest 协议、staging orphan sweep、completion reconcile、run claim 和更多竞争/网络策略测试。

**演进**：若后续需要带认证的版本接口，凭据必须走独立 secret store 和受控 transport，不能把 header、userinfo 或 token 写入 verifier 配置、checkpoint或事件。

## 2026-07-13 · Bug 输入与故障闭环拆分，重置采用新 Case

**背景**：Bug 平台管理、工单详情与持久化闭环共用一个页面，旧运行记录和新 Case 容易被理解为两套流程。

**决策**：`/bugs` 只管理和浏览工单，`/incidents` 是唯一主动闭环入口；同一 Bug 最多一个未结束 Case。重置不回滚外部副作用、不删除历史，而是在同一 SQLite 事务中把旧 Case 标为 `reset_archived` 并创建关系明确的新 Case，从验证阶段重新开始。

**后果**：导航和职责更清晰，重置可审计且可跨重启恢复；代价是新增 schema v6、跨 Case 原子命令和更多竞态/幂等测试。

---

## 2026-07-13 · Case 重置的 Agent 停止采用持久化 outbox

**背景**：Case 归档和接替 Case 创建发生在 SQLite 事务内，但停止旧 Agent 是事务外 runner 调用。仅在重置后查询审计事件再调用 `Cancel`，无法阻止两个 Studio 进程同时观察到“尚未取消”并重复调用；runner API 又没有幂等键，因此不能宣称 exactly-once。

**决策**：schema v7 新增 `reset_cancellation_operations`。当重置确实把运行中 attempt 置为 cancelled 时，在同一重置事务内写入严格绑定 reset key、旧 Case、attempt 和完整请求 fingerprint 的 `pending` operation。执行者必须通过 SQLite 条件更新把它原子 claim 为 `claimed` 后才能调用 runner；成功或失败只持久化固定 outcome code，不保存底层错误。其他进程看到 `claimed` 时不接管、不重试，而是报告结果 unknown 并要求人工检查；若 claim 所有者崩溃，该状态保持为人工处置边界。v6 升级时，已有 `case_reset` 必须同步回填 operation：只有旧审计的 key、Case、attempt、actor、事件类型和固定 outcome 全部匹配，才恢复为 succeeded/failed；缺少或不匹配一律回填 claimed/unknown，绝不生成可执行的 pending。重置事务已提交后，接替 Case 调度失败也不再让结构化 binding reject，而是返回保留下来的 Case 与 `reset_replacement_start_failed` warning；兼容旧入口仍可返回 error，但必须同时保留 Case。

**后果**：跨进程并发不会重复调用没有幂等能力的 runner，重放能确定读取 succeeded、failed 或 claimed/unknown；派生身份碰撞会按幂等冲突拒绝。代价是 claim 后崩溃无法自动判断 Agent 是否已停止，也不能安全重试，只能向用户暴露结构化 warning。若未来 runner 提供可靠幂等键，可再引入带 lease 的重试和恢复。

---

## 2026-07-16 · 验证浏览器由 Studio 宿主持有

**背景**：生成工作区已有可选的 `browser_collect.mjs`，但 Agent CLI 沙箱不保证能启动 Chromium，Playwright 也默认未安装；持久化 validation / regression 因此可能只重放 API，再把“没有 in-app browser”当作运行环境限制。Web Bug 即使有可访问页面和明确步骤，也没有产品级保证能产出本轮渲染截图、脱敏 Network / console 和可恢复的执行记录。与此同时，把 Cookie、Authorization、storageState 或原始 Playwright trace 交给 Agent 工作区，会绕过 Case artifact、安全路径和秘密扫描边界。

**决策**：Web validation / regression 使用 Studio 宿主浏览器架构。phase resolver 必须把这两个阶段切换到已安装的 validator 角色；缺少 validator 时返回 `validator_not_installed`，不回退排障工作区。validator 先输出严格声明式 `BrowserPlan`，`BrowserCoordinator` 校验后交给 Studio `HostVerifier`，由固定版本 Playwright 1.61.1 / Chromium 在宿主执行；locator 首次失败只允许修改失败及后续 locator 一次，最后再由 validator 根据宿主执行报告和冻结的 artifact refs 输出 `ValidationResult`。

浏览器能力边界如下：

- runtime 只在 Studio 管理目录保留一份固定依赖；安装成功必须经过真实 Chromium launch、本地页面和非空 PNG probe，不能用 npm 成功代替 runtime ready。
- BrowserPlan 仅允许 `goto`、`click`、`fill`、`press`、`select`、`wait_for` 和 `screenshot`，禁止任意 JavaScript/evaluate、XPath、扩展、文件上传、凭据、Cookie、header 和 storageState；URL、DNS/IP、redirect、私有 origin 与 `is_prod` 策略都由宿主重新校验，生产自动模式只允许导航、等待和截图。
- HostVerifier 只登记 PNG、脱敏 Network、console 和 `browser-actions.json`，不登记原始 HAR/trace；Runner 仍做 artifact 安全打开、fstat/SHA256/大小限制和第二道敏感信息扫描。Web 结论要成功推进，必须存在 HostVerifier 确认的本轮最终渲染截图；除登录表单禁止截图外，执行失败至少保留失败现场截图和明确步骤。
- 检测到登录后返回 `browser_login_required`，用户只在 Studio 打开的可见浏览器中手动完成 SSO/MFA。storageState 按 system/environment/application origin 绑定，使用 AES-GCM 加密；密钥进入系统 Keychain/Credential Manager/Secret Service，加密文件仅当前用户可读。无可用 keyring 时只保留内存 session，不降级明文落盘。
- Browser plan、reservation、result manifest 和 artifact digest 绑定 Case/cycle/attempt；只读、非生产且完整校验的中断计划最多安全重跑一次，已完成 manifest 重放不重复 worker 或证据登记。

**拒绝方案**：

- 在每个 Agent 工作区直接安装并启动 Playwright：实现表面简单，但仍受 Codex/Claude/OpenClaw 沙箱和平台进程权限影响，每个工作区重复依赖；更重要的是 session、URL 策略、生产动作和证据冻结散落在不可信执行边界。现有 `browser_collect.mjs` 因此只保留为脱离持久化 Case 的手动兼容路径。
- 外接 Browserless / Playwright Grid：可以集中维护浏览器，但给当前单机桌面产品引入外部服务、网络可用性、租户隔离、浏览器 session 凭据和运维依赖；在没有远程执行产品边界前不采用。
- 继续把用户附件/HAR/API 当 Web 验证默认降级：这些证据可用于非 Web 或历史分析，但不能证明当前页面真实渲染结果，也无法满足回归新鲜度门槛。

**后果**：Codex、Claude Code 和 OpenClaw 的 Web 验证共享同一宿主、安全和恢复语义；登录秘密不进入 Case，截图可经 Case/artifact binding 安全预览，浏览器系统故障也不会伪装成用户业务资料缺口。代价是 Studio 首次使用需要下载固定 Playwright/Chromium，占用额外磁盘和安装时间，并维护跨平台 browser process、keyring、journal、脱敏器与升级校验；一次 Web attempt 通常需要规划和结论两次 Agent 调用，locator 修正时增加第三次，token 和延迟均上升。

**发布门禁与演进**：默认测试只运行 fake runtime、HostVerifier/协调器/恢复测试和离线 Node worker，不联网下载 Chromium。发布候选必须在有 Node/npm 网络访问的机器显式执行 `TSHOOT_BROWSER_SMOKE=1 scripts/test-browser-worker.sh`，证明固定 runtime 能安装并启动本地页面、最终 PNG 非空、Network/console 不含 Cookie/Authorization/测试秘密且 action trace 含完成步骤；未通过或无法下载时必须记录为未解决 release gate，不能用离线测试代替。升级 Playwright/Chromium 必须同步固定版本并重新通过 probe、离线安全测试和真实 smoke。若未来引入远程浏览器服务，必须先重新评估租户隔离、session 密钥、证据归属和网络信任边界，再追加新 ADR。
