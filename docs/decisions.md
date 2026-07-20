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

---

## 2026-07-16 · 最终浏览器截图必须通过执行端附件契约交付

**背景**：HostVerifier 已能冻结本轮最终 PNG，但仅把宿主临时绝对路径写入 evaluator prompt 并不能保证 Agent 真正看到图片。Codex 有显式 `--image`；Claude Code 需要把临时目录加入可读范围后通过 Read 加载；OpenClaw 的公共 `agent` CLI 没有图片参数，但会对其已配置工作区内、prompt 引用的图片路径执行原生 image load。仅传路径还会让 evaluator 把临时目录回显到 `observed_behavior`、`expected_behavior` 或 `gaps`，进而落入 Case。

**决策**：`BrowserCoordinator` 通过独立的 `PhaseAttachmentExecutor` 交付冻结 PNG，执行器必须先校验绝对路径、只读权限、PNG 签名、大小和 SHA256。Codex 使用 `--image`；Claude Code 使用 `--add-dir` 并明确要求 Read；OpenClaw 在已配置的主 Agent 工作区创建短期 0700 目录与 0400 PNG，使其原生 prompt image load 可读取，完成后校验身份并清理。OpenClaw 的 validator/fixer 是主 Agent 内部角色，后台命令继续指向实际注册的主 Agent ID，不能把内部角色 ID 当作 `openclaw agent --agent` 的配置 ID。最终 YAML 若包含附件文件或临时目录路径，一律以 `browser_evaluator_result_invalid` 失败关闭，不进入 Case；不支持附件契约的执行器不得退回“仅提示路径”。

**后果**：三个受支持 target 的 evaluator 都有真实读取渲染截图的路径，结构化 Network/console 仍只作为辅助证据；临时宿主路径不会持久化。代价是每个 target 需要维护独立附件适配和清理测试，OpenClaw 必须拥有可写入短期视图的本地已配置工作区。未来若 OpenClaw 公共 CLI 提供稳定的原生附件参数，应改用该参数并删除工作区短期视图，但仍保留摘要、路径回显拒绝和清理门槛。

---

## 2026-07-17 · Chromium 准备迁出故障 Case 生命周期

**背景**：2026-07-16 的宿主浏览器决策确定了 runtime 归 Studio 持有，但首次实现仍由 `HostVerifier.Execute` 在 validation / regression attempt 内懒安装 Playwright 与 Chromium。网络下载、npm 安装和启动探测因此会表现为 Case 长时间“验证中”，既无法区分正常下载与卡死，也让基础设施故障污染业务验证状态。

**决策**：Playwright / Chromium 是 Studio 基础工具，其安装、升级、探测和修复不得由故障 Case 触发。Studio 启动后独立异步准备固定版本 runtime，并通过全局 `browser-runtime:status` 展示安装阶段和下载进度；已有通过 probe 的发布目录在 RuntimeManager 构造时立即恢复为 ready。`HostVerifier.Execute` 只调用无副作用的 `RequireReady`，不安装、不修复，也不等待并发安装。Web Bug 的 Case 创建、重置、validation continuation 和部署后 regression 必须在事务或阶段调度前检查 runtime ready；未就绪时保持原 Case 状态并返回稳定错误。全局“重新准备”入口同样不绑定 Case 或 attempt。非 Web Bug 不受此门禁影响。

这项决策仅 supersede 2026-07-16 ADR 中“首次使用在 Web attempt 内承担下载等待”的时序后果，不改变 Studio 宿主持有、固定版本、真实 launch probe、session 安全、证据冻结和 release smoke 门禁。Chromium 不写入生成机器人工作区；桌面发布包仍可按平台体积策略选择预置或首次启动准备，但无论采用哪种分发方式，准备都必须先于 Web Case。

**后果**：故障闭环中的“验证中”重新只表示真实业务验证，用户可在 Case 外看到依赖安装、Chromium 下载百分比和 probe 状态；基础工具失败不会创建空 Case，也不会被误判为缺少业务证据。代价是 Studio 增加一个有独立状态、重试和升级语义的宿主生命周期任务，前后端都必须维护 Web Bug 门禁；首次启动仍可能需要较长下载时间，但该等待不再占用 Agent 或 Case attempt。

---

## 2026-07-17 · 桌面发布包预置固定 Chromium runtime

**背景**：Chromium 准备迁出 Case 后，旧桌面包仍要在首次启动时联网下载 Playwright 的 Chromium 与 headless shell。国内 CDN 速度不稳定，下载可能超过一小时；失败的 Playwright 临时下载也不能可靠断点续传，用户会把首次启动准备理解成重复安装或卡死。macOS 的标准 dmg 拖拽安装没有可信 post-install 钩子，无法在“拖入 Applications”后执行联网安装。

**决策**：macOS `desktop-app` / `desktop-dmg` 在发布机构建阶段运行固定版本 RuntimeManager，完成 npm 安装、Chromium 下载、真实 launch、本地页面和 PNG probe 后，才把版本化 runtime 复制到 `.app/Contents/Resources/browser-runtime/`。Studio 首次启动优先从 App 内置目录校验并原子导入用户管理目录，不执行联网下载；内置 runtime 缺失的开发裸二进制和历史包保留启动时联网兜底。内置资源若存在但校验失败，必须以稳定错误失败关闭，不能静默联网替换被篡改的包。CI 按操作系统、架构和 runtime/worker 内容缓存发布 runtime，并检查 ready marker 与 browsers 目录进入 App。

**拒绝方案**：使用 macOS `.pkg` post-install 联网下载会改变现有 dmg 拖拽分发方式，而且安装脚本仍受现场网络影响；直接从只读 App Resources 运行则让修复、版本切换和用户目录权限语义分叉；每个机器人部署时安装 Chromium 会重复大依赖，也错误地把 Studio 宿主能力下沉到 Agent 工作区。

**后果**：正式 dmg 安装后首次启动不再等待网络，故障闭环页面最多短暂显示本地导入；发布包和 CI artifact 会明显增大，发布 job 首次构建也需要完成真实浏览器下载与 probe。Playwright 版本升级必须同时刷新发布缓存键、App 结构检查和真实 smoke；裸二进制的联网路径继续作为开发兼容能力，不代表正式分发体验。

---

## 2026-07-17 · 浏览器 runtime 身份同时包含 Studio worker 修订

**背景**：runtime 目录原先只使用 Playwright 版本 `1.61.1`。Studio 修复宿主 worker 后，已发布目录仍可能被旧 App 或旧缓存当作同一 runtime；反过来，新 App 校验到 worker 内容不同又会把已安装目录标为损坏，无法通过普通启动准备原子替换。

**决策**：runtime 身份采用 `<playwright-version>-r<studio-revision>`，首个修订为 `1.61.1-r1`。npm 依赖和 Chromium 版本仍固定为 Playwright `1.61.1`，`rN` 只在 worker、sanitizer、宿主协议或 probe 契约变化时递增。桌面 bundle、CI 结构检查和用户管理目录都使用完整 runtime 身份。

**后果**：修复版 App 会把新 worker 作为独立且可验证的基础工具导入，不会静默复用旧 worker，也不要求把“内容不匹配”伪装成同版本在线修复。代价是本地可能短期保留上一个 runtime 目录；后续可在确认无运行中旧 App 后增加有界历史版本清理，但不得删除当前或仍被进程使用的目录。

---

## 2026-07-17 · 渲染浏览器额外网络依赖必须显式配置

**背景**：部分 Web 应用除主站和 API 外还依赖 CDN/资源 origin，甚至会在资源请求被拒绝时中断页面交互初始化。Studio 只把 `web_domain`/`api_domain`/登录 origin 交给宿主代理时，元素虽可见，点击或搜索事件却可能被应用自身丢弃。同时，自动允许任意公网 origin 会扩大不可审计的网络边界。

**决策**：环境新增 `browser_allowed_origins`，只接受不含路径、查询、fragment 或凭据的绝对 HTTP(S) origin。解析器将它与 Web/API/登录 origin 合并、规范化和去重，宿主代理仍对每次 DNS/IP/重定向执行原有校验；不从页面、Agent 计划或失败请求自动扩展白名单。需要 Service Worker 才能正常运行的应用可在临时 context 内启用，其所有对外连接仍必须经过 Studio 拥有的认证代理。

**后果**：页面必需的第三方资源可在不放开公网通配的前提下完成渲染与交互；配置者需要把真正必需的 origin 纳入 YAML，新增依赖域时也需同步更新配置。

---

## 2026-07-17 · 回归失败回环，回归通过后同步解决 Bug 工单

**背景**：持久化状态机已经能在 `still_reproduces` 后递增 `cycle_number` 并创建下一轮排障 attempt，但工作台仍把六个阶段画成一次性直线，用户容易把“回归”理解为无条件终点。同时 `fixed_verified` 只关闭本地 Case，没有把来源禅道 Bug 转为已解决，闭环真源和工单平台会出现状态分叉。

**决策**：回归仍复现时保持同一 Case，携带本轮回归证据进入下一轮“排障 → 修复 → 合并 → 部署 → 回归”；只有回归结果为 `fixed_verified` 才形成工单解决意图。桌面宿主在 Case 提交成功后异步调用禅道 `bugs/{id}/resolve`，固定使用 `resolution=fixed`，并在动作前后读取工单状态：已解决或已关闭时直接幂等成功，响应不确定但复查已解决时也按成功处理。Case 的 `fixed_verified` 是可恢复意图；Studio 启动后和运行期间定期扫描该状态并补偿未完成的工单同步。外部同步不阻塞回归 Agent 的完成回调，也不把平台故障伪装成回归失败。界面分别展示“工单同步中”和“工单已解决”，并明确画出回归失败的下一轮回环。

**后果**：业务 Bug 仍存在时会继续自动排障，不会停在 `still_reproduces`；来源工单只有新鲜回归通过后才会进入已解决，重复回调和丢失响应不会重复执行已完成动作。代价是本地闭环完成与远端工单同步之间存在一个短暂、可见且可重试的最终一致窗口；禅道不可用或登录过期时 Case 保持 `fixed_verified`，工单状态显示待同步，恢复连接后由补偿任务继续。

---

## 2026-07-18 · Bug 收件箱移除改为可恢复历史归档

**背景**：禅道“指派给我”同步只返回当前待处理工单。旧实现把不在新结果中的本地 Bug 直接删除，因此已解决、已关闭或被重新指派的工单会在下一次同步后从 Studio 消失。工作流 Case 虽仍在 SQLite 中，但缺少标题、详情和附件入口，用户无法从 Bug 工单页查看完整闭环历史。

**决策**：同步结果缺失不再物理删除 Bug，而是把完整本地快照从 `active` 收件箱移动到 `history`，记录归档时间和原因并保留附件缓存。来源状态为 `resolved`/`closed` 时同样自动进入历史。Bug 工单页提供“收件箱/历史”切换；从历史进入故障闭环时展示最新终态 Case、证据和时间线。若来源工单重新打开或重新指派回来，后续同步用新鲜来源快照清除归档标记，使其自动回到收件箱。平台配置删除仍不删除已接收历史。

**后果**：已解决工单和故障闭环审计记录在本机持续可查，重新同步不会让历史消失，重新打开也无需人工恢复。代价是 `bugs.json` 和附件缓存会随历史增长；后续如增加清理能力，必须是独立、明确确认的历史保留策略，不能复用收件箱同步的缺失语义进行删除。

---

## 2026-07-18 · 复现证据作为排障阶段强绑定输入

**背景**：Web Bug 的验证阶段已经通过宿主浏览器完成真实复现，但旧协议只保存请求摘要，且排障 attempt 仅收到验证结论文本。排障 Agent 因此无法稳定回答“哪个用户动作触发了哪个请求、请求从哪段前端代码发起、对应哪个 request/trace”，只能再次打开浏览器或依赖静态猜测；重复复现既浪费时间，也可能因数据和页面状态变化得到不同事实。

**决策**：宿主 browser worker 在执行验证计划时同步采集有界的因果 Network 证据：每条记录绑定 `action_id` 和开始时间，保存资源类型、成功/失败结果、耗时、脱敏 URL、request/trace 标识，以及 CDP 提供的 initiator 类型和有界调用栈。不得保存请求/响应正文、Cookie、Authorization 或原始 headers；调用栈只记录脚本 URL、函数名和行列号，源码映射仅在对应版本的 source map/构建产物可用时由后续排障完成。该协议变化将 browser runtime 身份升级为 `1.61.1-r9`。

验证成功复现后，状态机把验证 attempt、场景摘要和已登记证据的 artifact ID/SHA256/环境/版本/request/trace 持久化到首轮排障输入；回归仍复现时，用同一结构把回归 attempt、新部署版本、差异观察和新鲜证据绑定到下一轮排障输入。排障 runner 启动前必须从安全证据库重新校验这些绑定，将不可变副本和 `validation-evidence-manifest.json` 放入本次 staging，并明确要求 Agent 先消费该清单；绑定缺失、摘要不一致或文件损坏均失败关闭，不能回退为重新跑浏览器来掩盖交接错误。后续运行时查询和源码定位应复用这些事实，并区分动态证据与静态推断。

**后果**：一次复现即可同时服务验证和首轮排障，多仓库定位可从“动作 → 请求 → request/trace → 后端服务 → 仓库”收敛，前端无埋点时也能获得浏览器侧的原始发起栈。证据量会增加，但受字段白名单、条数/长度限制和秘密扫描约束；若生产构建未提供匹配版本的 source map，只能定位到 bundle URL 与行列号，不能把这种限制伪装成完整源码定位。

---

## 2026-07-18 · 前端源码定位复用部署映射并显式降级精度

**背景**：环境已经支持 `deployment_verification.k8s.deployments_by_repo`，但该映射原先只在人工部署后的版本验证中使用。排障 Agent 即使拿到浏览器 initiator bundle 行列号，也不知道哪个前端仓库、哪个 Deployment 和哪个已部署 revision 对应本次复现；没有 source map 时容易误用仓库当前 HEAD，或把静态文本搜索结果当成精确源码定位。

**决策**：排障阶段复用同一份 Case 绑定机器人配置和只读 K8s reader，筛选 `frontend`/`admin` 仓库后生成不可变 `frontend-runtime-manifest.json`。每个仓库依次尝试配置的 commit annotation/image label、常见 OCI/K8s revision 元数据、镜像 digest、非 `latest` 镜像 tag，最后降级到 repo/sub_path；每一级记录来源、实际精度和限制。Agent 必须把该清单与冻结 Network initiator 证据一起使用：只有同时匹配部署版本与 bundle URL 的 source map 才能报告精确源文件行列号；否则按 deployed revision、image candidate、repository candidate 逐级降级，并在结论中明示精度。K8s 读取或配置加载失败不得阻断排障，而是生成 `unavailable`/仓库级输入；任何降级都不得伪装成精确结论。

**后果**：用户只需维护前端仓库与 Deployment 映射，排障就能优先在真实部署 revision 上收敛，多仓库场景不会默认全仓盲搜；没有构建产物或 source map 也能得到可审计的低精度候选。该能力不能凭 Deployment 映射凭空恢复 minified 源码，精确行号仍依赖构建阶段保留并绑定 source map。后续若增加 source map 注册服务，应扩展同一清单而不是另建一套仓库/版本映射。

---

## 2026-07-18 · 调用链定位采用运行版本、结构化精度和静态异步导航

**背景**：前端运行时清单只覆盖 frontend/admin，后端代码取证仍可能落到本地当前 HEAD；调查输出只有根因文本，界面无法区分真实 span、部署版本源码和静态候选。Chromium 已保存 bundle 行列号，却没有保存脚本声明的 source map 候选，也没有安全的离线反解工具。正式服务拓扑只建模 HTTP/gRPC，trace 在 Kafka/RabbitMQ 边界中断时缺少跨仓库导航。

**决策**：此前的 `frontend-runtime-manifest.json` 能力扩展为 `runtime-code-manifest.json`，覆盖所有 runtime repository，并继续按 Deployment revision、image digest/tag、repository 逐级降级。存在 deployed revision 时，Agent 必须用只读 `git cat-file`、`git grep <revision>` 和 `git show <revision>:<path>` 取证；当前 CodeGraph 只有在 HEAD 等于 deployed revision 时才能证明运行代码。调查协议新增有界 `call_chain`，每一跳必须声明 `runtime_verified`、`source_mapped`、`deployed_revision`、`static_candidate` 或 `unavailable`，Studio 严格解析并在工作台独立展示。

宿主 browser worker 同时监听 CDP `Debugger.scriptParsed`，只把 http/https/file 的非内联 source map 候选 URL 经脱敏、限长后绑定到 initiator frame；协议修订升级为 `1.61.1-r10`。生成物提供纯本地 Source Map v3 VLQ 反解器，禁止仅凭候选 URL 或 bundle 坐标报告精确源码。仓库扫描另外生成 `async-topology.yaml`，仅收录 Kafka/RabbitMQ 的 literal producer/consumer destination 和代码位置；该文件只用于 trace 中断后的导航，必须用 message id、trace link 或同窗口两侧日志验证。动态 destination、未传播关联 ID、缺失 source map 和外部观测系统能力继续作为显式 gap，不由 Studio 伪造补齐。

**后果**：同一份结构化结果可从浏览器动作、前端 bundle、入口服务、后端服务一直展示到仓库和部署 revision，并诚实标记每跳精度；多仓库搜索优先落在实际运行版本，异步链路断点也有稳定候选入口。精确程度仍受外部构建是否保留匹配 source map、K8s 是否暴露 revision、trace/message id 是否传播限制。新增静态扫描是导航索引而非运行事实，任何仅由它补出的边都不能升级为已验证调用。

---

## 2026-07-18 · 部署版本改为可选自动证据并复用可观测性 K8s 映射

**背景**：机器人创建向导同时要求在环境高级配置和可观测性中填写集群、Namespace、Deployment 映射，形成两份会漂移的运行时定位数据。故障闭环又要求用户在点击“已部署”时手工填写版本号和每仓 commit；现场通常拿不到这些构建信息，版本读取端点或 K8s metadata 不可用时，流程会停在 `deployment_unverified`，即使回归 Agent 本身可以直接验证业务 Bug 是否修复。

**决策**：版本号和 commit 从用户输入中移除。部署确认后，Studio 可通过 HTTP 或只读 K8s 运行时自动尝试采集版本；采集完整且匹配时保存精确证据，未配置、不可读、未暴露或只能部分识别时保存 `unavailable` 诊断并以空版本继续回归。只有显式版本接口或显式 K8s revision 字段明确读到不一致值时才阻断并进入 `deployment_unverified`。回归的新鲜业务证据是最终修复证明，版本信息只是可选增强，不再是人工门槛。

K8s 定位优先复用 `observability.k8s_runtime.service_map` 的环境、服务、集群、Namespace 和 workload；创建机器人向导不再展示“故障闭环高级配置”，也不再生成第二份 `deployment_verification.k8s` 定位或版本字段。环境没有显式 `deployment_verification` 且已启用 K8s 运行时可观测性时，Studio 自动使用这套只读映射采集可选版本证据。旧 YAML 中显式的 HTTP、K8s 或 manual 配置继续兼容，显式 manual 仍可关闭自动采集；可观测性映射存在且唯一时覆盖旧 K8s 定位值，映射跨多个位置或无法唯一归并时不猜测，版本采集降级为 unavailable。此决策 supersede 同日“前端源码定位复用部署映射”中把 `deployment_verification.k8s.deployments_by_repo` 作为主要用户维护来源的部分，不改变运行时源码定位必须显式标注精度的约束。

**后果**：用户只需维护一套 K8s 运行时映射，创建机器人和故障闭环都不再要求掌握构建版本；版本系统缺失不会阻塞闭环，最终仍只有回归通过才能解决 Bug 工单。代价是版本不可用时无法用 commit 强绑定回归，此时系统必须明确展示采集缺口，并依靠当前 attempt 的时间、环境、请求/trace 标识和新鲜截图等证据保证回归不是复用历史结果。旧配置仍可读取，但不会由新向导继续复制。

---

## 2026-07-18 · 用户级 IDE 机器人采用单一项目路由与 fail-closed 归属门禁

**背景**：Claude Code、Cursor 和 Codex 只从用户级目录发现 Agent。每部署一个系统都会增加一组排障、验证和修复 Agent；若每组都用“报错/慢/失败/修复”等通用描述参与自动匹配，在任意代码项目中都可能启动错误系统的机器人、加载错误 MCP 和仓库映射。把 Agent 改成项目级安装不符合三家 IDE 的稳定发现位置，也会让多项目复用和升级复杂化。

**决策**：每个 IDE 用户目录只安装一份共享 `tshoot-router` skill，所有系统专属 Agent 的 description 改为显式路由后或明确点名才可调用。`tshoot.json` schema v2 增加 `project_repositories`，保存可共享的仓库 URL、机器本地路径和 monorepo sub-path；路由器先按当前目录位于配置本地路径的强证据匹配，再按规范化 Git remote 匹配。显式 `system_id/system_name/agent_id` 是最高优先级，只允许用户明确点名或 Studio Case 的“选定机器人”上下文使用。最高分不存在时返回 `unmatched`，多个系统同分时返回 `ambiguous`，均不得模糊猜测。每个业务 Agent 在调用 MCP、读取业务映射或修改代码前还必须用 `--expect-agent` 再校验一次归属，形成第二道 fail-closed 门禁。老 metadata 缺少 v2 字段时，路由器只回退到 `~/.tshoot/config.json` 的 `repo_paths_by_system` 本地路径，不恢复模糊匹配。

Studio 故障闭环已经把选定机器人、system 和内部 role 持久化到 Case/attempt，后台启动仍以该显式绑定为准，不依赖 Studio 自身 cwd。OpenClaw 由客户端或 Studio 直接选择已注册主 Agent，不参与这套 IDE 自动发现路由。

**后果**：同一用户可以安装多套系统机器人，Codex 等 IDE 在项目内提出通用排障请求时会先收敛到唯一系统，无法唯一确定时宁可停下也不会串台；验证和修复 role 也只能属于同一系统。共享路由 skill 没有 `tshoot.json`，不会在 Studio 中多出一张机器人卡片，卸载单台机器人也无需删除它。代价是首次采用该机制需要重新部署机器人以写入 v2 仓库元数据和新版 Agent prompt；尚未配置本地路径且仓库没有可比 remote 的项目必须显式给出 system_id。

---

## 2026-07-18 · 创建向导以派生资源目录收口四类部署配置

**背景**：代码仓库、配置源、数据层和可观测性分别维护服务名、环境和资源映射。单源后端项目基本可用，但多仓库、多配置源和前端 workload 会出现重复填写和数据断层：仓库步骤放行缺少 stack 的仓库而后端最终失败；副 Nacos/Apollo/Consul 没有完整预读；数据层只有自动识别没有手工补录；K8s runtime 可以临时复用 Kuboard 凭据读取资源，但生成 YAML 只读取可观测性表单，形成“界面成功、产物缺连接”。

**决策**：先保持现有 YAML schema 兼容，在向导内部建立派生资源目录和统一部署就绪度。资源目录区分 repository、业务 service、运行时 workload 和配置源 instance；第一阶段以 fail-closed 校验阻止会生成错误产物的状态，以显式降级处理可选能力，并提供按环境/服务的覆盖矩阵。当前 schema 尚不能表达的多个远程配置中心副源组合不再伪装为完整支持，先在界面阻止并说明；后续再把 `(env, service) -> source instance` 和服务级 source 归属固化到 schema。K8s runtime 复用配置源连接时，生成器必须使用同一份连接数据。数据层允许手工补录自动发现缺口。

**拒绝方案**：立即替换整个 YAML schema，会同时影响 importer、三类入口、生成器和已部署机器人，迁移风险过大；继续只优化四页卡片布局，则无法解决重复身份和生成断层。

**后果**：旧 YAML 与已部署机器人保持兼容，向导先获得一致的正确性门禁和能力反馈；用户可以明确知道当前是基础、标准还是完整部署。代价是第一阶段资源目录仍是派生数据，多个同类型配置源和环境维度 source 归属需第二阶段 schema 迁移。详细设计与验收标准见 `docs/wizard-resource-catalog-design.md`。

---

## 2026-07-18 · Schema 0.2 固化资源目录并禁止凭据写入 YAML/草稿

**背景**：第一阶段覆盖矩阵能发现问题，但真实路由仍由 `repos[].config_source` 决定，无法表达同仓服务拆分或同一服务在 dev/prod 使用不同配置实例。另一方面，向导已经有系统钥匙串 binding，却仍把 secret 同时写入 YAML 和 `localStorage`，既扩大泄露面，也让部署错误地依赖 YAML 反填凭据。

**决策**：Schema 0.2 新增顶层 `resource_catalog`。`services[]` 用稳定 ID 关联 repository，并以环境 map 引用配置源实例、数据层实例和 workload；`workloads[]` 独立表达前端/移动端等没有配置中心 service 的运行时身份。`data_stores[]` 增加实例 ID。生成物路由按 `(env, service)` 优先读取资源目录，旧仓库级 source 仅兼容回退；0.1 YAML 在 loader 中自动派生目录和数据层 ID。图形向导的服务归属同步升级为环境维度，同仓服务可以分源，不再压扁成仓库众数。

所有标记为 secret 的配置中心、数据层和可观测性字段，新 YAML 只写环境变量引用。桌面向导把实际值按 system/资源/环境/字段保存到 OS keychain，并在草稿持久化前剥离；部署直接从向导内存和共享连接解析结果构建 credentials，不再依赖 YAML 中的 secret。loader/prefill 必须忽略 `{{...}}` 引用，避免把引用字符串当成真实密码写进 MCP 环境。

**边界**：后端和 YAML 已支持任意 source instance ID，包括同类型多个实例；但图形向导的远程配置预读缓存仍按 type/env 组织，多个 Nacos/Apollo/Consul 实例继续 fail closed，直至缓存和探测状态升级为 source-instance 维度。数据层实例 ID 已正式化，当前自动扫描仍按 type 聚合。这里选择显式限制而不是把两个实例的资源列表混合后错误生成。

**后果**：环境级配置路由和前端 workload 身份成为可校验真源，YAML 可以安全分享而不携带 secret，旧配置继续加载。代价是从 YAML 单独导入到另一台机器时必须重新提供凭据（或由目标机器钥匙串已有值恢复），这是安全存储从可移植配置中分离后的预期行为。

---

## 2026-07-18 · 向导资源状态升级为实例身份并对运行时映射交叉校验

**背景**：Schema 0.2 已能表达配置源和数据层实例 ID，但向导预读缓存、部署凭据和部分界面状态仍以 type 为 key。两个 Nacos 或两个 Redis 因而可能在编辑、导入或 MCP 注册时覆盖。资源目录、K8s runtime 和 Loki 又各自保存 workload/service 名称，缺少交叉校验时同一服务可被路由到互相矛盾的 Deployment。

**决策**：Nacos/Apollo/Consul 的凭据、预读结果、namespace、dataId 和服务归属统一以 `config_centers[].id` 为身份，type 只决定协议；同类型新增实例按 `type-2`、`type-3` 分配稳定 ID。Kuboard 的资源选择状态和 one2all 的全局 endpoint 仍各自只支持一个连接实例，向导不提供重复入口，导入重复实例时 fail closed。数据层状态保存 `id -> type`，人工补录在整个系统范围分配 ID，部署阶段按实例生成独立 credential namespace 和 MCP key。单类型单实例仍保留旧 env/MCP 命名，避免无意义迁移。YAML 导入、草稿和重新生成必须保留显式 ID，不得再归一化回 type。

后端校验同时把 `resource_catalog` 作为运行时身份真源：K8s service map 只能引用已知 service/workload，`(env, service)` 不得重复，显式 workload 名必须与目录一致；Loki service map 也只能引用目录身份。缺少可选映射允许降级，显式冲突则拒绝生成或部署。

**后果**：同类型多实例从界面编辑到实际 MCP 注册形成完整闭环，资源目录、K8s 与日志路由不会静默分叉。自动扫描对无法从配置文本区分的同类型第二连接仍需要用户补录实例；这是证据精度限制，而不是把两个连接合并成一个虚假实例。此决策 supersede 上一条 Schema 0.2 决策中“图形向导远程预读仍按 type fail closed、数据层仅按 type 聚合”的边界描述。

---

## 2026-07-18 · 排障阶段消费冻结复现证据并发布可恢复的七步进度

**背景**：验证 Agent 已经完成真实复现并把证据强绑定给排障 attempt，但生成的 Codex 顶层 skill 索引仍按工作区全部 skill 构建，会把验证专用的 `attachment-evidence-verifier`、`api-verifier` 和 `bug-verifier` 暴露给排障 Agent。Agent 因而可能再次进入验证流程，甚至读取安装后不存在的相对路径。同时工作台只展示命令和工具事件，用户看不到七步排障主线当前走到哪里；刷新页面后实时事件也会丢失。

**决策**：排障提示和 `incident-investigator` 都把 Studio 的 `validation-evidence-manifest.json` 设为最高优先级输入：存在冻结证据时不得调用验证专用 skill 或重新操作浏览器，证据损坏或不足只能形成明确 gap。Codex 顶层 skill 索引按 troubleshooter role 过滤，与实际安装目录保持一致。排障 Agent 在进入七步主线的每一步前发送固定 `TSHOOT_STEP` 标记；Studio 只接受预定义 phase、序号和 key 的严格组合，把它解析为 `phase_step` 事件，本地映射可信展示名称，不把标记当作最终 YAML。事件继续写入 `runs.json` 兼容投影，Case 详情只恢复当前 attempt 最近 100 条经过类型、字段和长度白名单处理的事件，原始协议、工具参数和命令输出不进入页面。

**后果**：复现和排障职责不再反复横跳，排障从同一份不可变事实开始；界面能明确显示“第 N/7 步”和已完成步骤，并在刷新后恢复，同时保留下面的命令/工具日志。旧 Agent 若尚未重新部署，Studio 阶段提示仍会阻止验证回退；重新部署后其根 skill 索引也会彻底移除错误入口。OpenClaw 当前只返回最终结果，无法提供同粒度实时步骤时不会伪造进度，仍以已有实时事件能力为准。

---

## 2026-07-19 · BrowserPlan 负向文本断言升级宿主运行时修订

**背景**：缺失或“不应展示”类 Web Bug 需要表达文本不得可见。BrowserPlan 新增 `not_visible_text` 后，内嵌 browser worker 的协议和执行逻辑已经变化；若仍沿用 `1.61.1-r11`，打包工具会命中同名旧缓存，完整性校验正确地将新旧 worker 差异识别为运行时损坏，导致桌面 bundle 无法生成。

**决策**：BrowserPlan 文本断言只允许 `visible_text` 与 `not_visible_text`，宿主对后者等待所有匹配文本均不可见。由于 worker 协议发生变化，browser runtime 身份升级为 `1.61.1-r12`；Playwright 与 Chromium 仍固定在 `1.61.1`。旧 `r11` 目录不删除、不原地修改，新版本通过独立目录安装、真实 Chromium probe 和原子发布生成 `r12`，继续保持 worker/sanitizer 字节完整性校验。

**后果**：升级后的 Studio 和 macOS 打包流程不会把旧 worker 缓存误认为当前运行时，也不需要用户手动清缓存；首次准备 `r12` 会执行一次依赖准备和 Chromium probe，随后可复用。未来任何 worker、sanitizer、宿主协议或 probe 契约变化仍必须同步递增 `rN`。

---

## 2026-07-19 · 修复阶段由 Studio 锁定环境分支并使用专用 worktree（SUPERSEDED：见“修复基线与环境集成分离”）

**背景**：环境路由已经把 `test` 映射到 `base-test`，但修复 Agent 过去仍在业务仓库当前 checkout 中执行无起点的 `git checkout -b` / `git switch -c`。当用户正位于功能分支时，新修复分支会继承功能分支基线；Agent 即使在结果中自报 `base_branch: base-test`，Studio 也只做字段间一致性检查，无法发现提交祖先实际来自错误基线，最终在合并环境分支时产生大量无关冲突。

**决策**：修复阶段启动前，Studio 必须读取已部署机器人 `env-branch-map.yaml`，按目标环境解析每个仓库的明确环境分支，并通过用户配置解析对应本地仓库。Studio 精确 fetch `origin/<环境分支>`、锁定其 commit SHA，在 Studio 管理目录创建 detached 专用 worktree，再把仓库、worktree、环境分支、锁定 SHA 和 remote 作为强约束交给修复 Agent。Agent 只能在该 worktree 中从锁定 SHA 创建修复分支，不得修改原 checkout，也不得 merge/rebase 当前功能分支。

修复结果进入后续合并前，Studio 以配置和 Git 图为准重新校验：仓库必须属于目标环境映射，声明的 base/target branch 必须等于配置分支，remote 必须匹配，`merge-base(锁定 SHA, fix commit)` 必须精确等于锁定 SHA，且锁定点之后的每个提交必须是单父线性提交。仅靠 Agent 自报字段不再构成基线证明。缺少映射、本地仓库、远端分支或 fetch 失败时 fail closed，不猜测当前分支；阶段结束后回收专用 worktree。

**后果**：用户当前打开哪个功能分支都不会再影响修复基线，错误基线会在合并和推送到环境分支之前被拒绝，原业务 checkout 的未提交改动保持不变。代价是修复阶段启动需要访问远端并为目标环境映射的仓库准备临时 worktree；网络、映射或本地仓库配置不完整时会明确阻断修复，而不是生成表面成功但不可合并的分支。

---

## 2026-07-19 · Studio 后台 Codex 使用仓库白名单而非用户目录发现

**背景**：机器人产物中的 `repo-path-map.yaml` 可能因旧部署或未重新生成而缺少 `local_path`。排障 Agent 曾以 `find /Users/<user>` 作为兜底寻找仓库，递归进入 `Library/Containers`、照片、文稿和下载目录，导致 macOS 把 Studio 识别为访问“其他 App 数据”并逐项弹出 TCC 授权。交互批准既不适合后台自动闭环，也扩大了排障源码读取边界。

**决策**：故障闭环每个 attempt 由 Studio 在证据 staging 中生成不可变 `repository-access-manifest.json`。排障阶段只把当前 system 在本机配置的有效绝对仓库路径列为只读根；修复阶段只把 Studio 从环境分支锁定的专用 worktree 列为可写根。Codex 后台执行改用命名 permission profile：最小系统运行文件可读、机器人 workspace 与 staging 可写、清单仓库按声明读写，其余用户目录默认不可读，同时固定 `approval_policy=never`。路径缺失只能形成配置 gap，禁止用 `find`、`fd`、`locate`、glob 或递归 `ls` 扫描 `/Users`、`$HOME` 或其上级目录。

**后果**：后台 Codex 不再需要用户逐项批准 App Data、照片、文稿等 macOS 权限；即使模型忽略提示并尝试扫描主目录，也会在 Codex 文件系统 sandbox 内先收到 `Operation not permitted`，不会触发系统隐私弹窗。已配置仓库、冻结证据和专用修复 worktree 保持可用。未配置路径时排障会明确提示回到 Studio 补仓库映射，宁可降低定位能力也不越权搜索。

---

## 2026-07-19 · 修复基线与环境集成分离（SUPERSEDES “修复阶段由 Studio 锁定环境分支并使用专用 worktree”）

**背景**：把修复分支强制建立在环境分支上能避免误用当前 checkout，却破坏了长期 feature 分支的隔离语义：feature 可能刻意不包含 dev/test 的其他变更，环境分支也可能与开发基线长期分叉。修复 Agent 从环境分支创建修复会污染开发上下文，反过来从 feature 修复后直接要求 `base_branch == target_environment_branch` 又会错误拒绝合法结果。

**决策**：开始修复的用户授权必须按受影响仓库明确给出 `source_baselines`。Studio 精确 fetch 并锁定 `origin/<source baseline>`，在 detached 专用 worktree 中让 Agent 创建修复分支；目标环境分支继续由已部署机器人的 `env-branch-map.yaml` 决定。`base_branch` 表示开发基线，`target_environment_branch` 表示后续环境集成目标，两者独立且都必须与锁定值一致。修复提交仍要求以锁定开发基线为 merge-base、保持单父线性历史。

环境集成不创建或推送命名 integration 分支。Studio 从经授权的环境 HEAD 创建内部 detached 临时 worktree，在其中进行合并、测试和必要的冲突处理，得到合并提交后以 `<merge-commit>:refs/heads/<environment-branch>` 直推；用户当前 checkout 和环境分支工作区均不被切换或写入。目标 HEAD 变化会使授权失效，禁止 force push。

**后果**：feature 分支可作为每个 Case 的真实开发基线，不再被 dev/test 历史污染；同一个修复提交可以独立进入开发基线和目标环境的集成流程。用户必须在修复授权弹窗确认仓库与开发基线，缺少或不存在的远端基线 fail closed。环境合并的临时状态只存在于 Studio 管理目录，不会在远端留下 `integration/*` 垃圾分支。

---

## 2026-07-19 · 服务拓扑按服务对展示并支持服务级人工关系

**背景**：跨仓扫描产生的是端点级证据，同一对服务可能匹配数百个接口。向导过去把每条端点证据都渲染成一条“服务关系”，导致同一个来源和目标重复出现；人工补录又强制用户伪造协议、HTTP 方法和路径，混淆了服务依赖与端点证据。

**决策**：向导按 `(from_service, to_service)` 聚合展示唯一服务关系，协议、方法、路径、源码位置和匹配理由保留在右侧可滚动的端点证据列表中。`service_topology.overrides` 新增可选 `scope: service`；服务级确认、拒绝、重定向和人工新增只需要来源与目标服务，并作用于该服务对下的全部端点证据。旧的 HTTP/gRPC 路由级 override 继续兼容，且精确路由决策优先于宽泛服务级决策。纯服务级人工边进入正式服务图，但不伪造 route。

**后果**：用户确认的是实际服务依赖而不是重复接口条目，人工补录只填写两个服务；排障机器人仍能从端点证据查看具体协议、路由和代码位置。旧 YAML 无需迁移，新生成的服务级关系会显式包含 `scope: service`。

---

## 2026-07-19 · 非代码根因采用“人工处置确认 → 新鲜回归”闭环

**背景**：故障闭环过去把所有 `root_cause_ready` 都送入代码修复授权。数据异常、配置错误、K8s/网络故障、外部依赖不可用或瞬时恢复因而也会错误启动修复 Agent，生成无意义分支，或者诱导 Agent 在排障阶段直接执行高风险运行时写操作。此类问题仍需要审计、幂等和最终业务回归，但没有可绑定的 Git commit。

**决策**：排障结构化结果必须给出 `root_cause_type` 和与其唯一匹配的 `remediation.mode`。只有 `code + code_change` 进入原有“修复、合并、部署、回归”六阶段；数据、配置、基础设施和网络使用 `operator_action`，外部依赖使用 `external_recovery`，瞬时问题使用 `observe_only`，进入“验证、排障、处置、回归”四阶段。Studio 与排障 Agent只生成只读证据和最小处置建议，不自动写数据库/配置、不重启工作负载、不修改网络或外部系统。操作人必须在绑定当前 Case 版本、cycle 和 root-cause attempt 的确认框中填写实际执行摘要与证据；系统将完整建议、回滚、验证方式、实际摘要和证据写入 approval 审计记录，再以无 commit 的 remediation binding 启动原业务场景的新鲜回归。只有回归通过才能解决 Bug，仍复现则进入下一轮排障。

新增持久状态 `waiting_remediation` 与 `remediation_applied`。处置确认、回归 reservation、审计 approval 和广义 deployment observation 在一个事务中提交；若进程在提交后、启动回归前退出，启动恢复会从 reservation 精确续跑且同一进程不重复调度。旧排障结果没有新字段时按历史语义兼容为 `code + code_change`，避免已存在 Case 无法继续。

**后果**：修复 Agent 不再处理非代码根因，也不会为运行时问题伪造代码变更；用户能看到明确处置对象、建议、回滚和验证要求，并留下谁在何时做了什么的审计证据。代价是 Studio 暂不替操作人执行高风险动作，处置时长被独立统计为 `remediation`；未来若引入受控自动处置，必须为每类写操作增加单独授权、最小权限、dry-run、回滚和运行时 negative test，不能复用当前只读排障权限。

---

## 2026-07-19 · 交互定位语义升级浏览器运行时修订

**背景**：浏览器 worker 从 `locator.first()` 改为“必须唯一可见”的交互解析，并在文本填入后校验稳定值。这改变了 bundle 内置 worker 字节；若继续使用 `1.61.1-r12`，打包会复用旧 r12 目录，并被字节完整性校验正确拒绝。Makefile 原本还会在运行时准备失败后继续调用打包脚本，追加一条误导的 `BROWSER_RUNTIME_SRC required`。

**决策**：browser runtime 身份升级为 `1.61.1-r13`，Playwright 和 Chromium 依旧锁定 `1.61.1`。旧 r12 不原地修改，新 r13 通过独立临时目录、真实 probe 和原子发布生成。`desktop-app` 在运行时准备命令失败或未返回目录时立即中止，不再携带空变量进入下一层脚本。

**后果**：用户重新执行 `make desktop-app` 时会自动准备一次 r13，无需删除缓存或手工设置 `BROWSER_RUNTIME_SRC`；后续打包可复用已验证的 r13。未来任何 worker、sanitizer、probe 或宿主协议变更仍必须同步递增 `rN`。

---

## 2026-07-20 · SPA 延迟渲染下交互定位采用有界唯一可见等待

**背景**：交互定位升级为“必须唯一可见”后，worker 先调用 `locator.count()` 和 `isVisible()` 再执行操作。这两个 API 都是即时快照，不会像 `click()` / `fill()` 一样自动等待。在 SPA 已完成页面跳转、但搜索输入框尚未 hydration 的几毫秒窗口内，合法 locator 会被在 1ms 内误判为失败。

**决策**：交互定位器对“当前没有可见匹配”进行最长 15 秒、100ms 间隔的有界轮询，与页面 Playwright 默认操作超时保持一致；出现唯一可见元素后才交给 `click()` / `fill()` 执行。一旦同时出现多个可见匹配仍立即 fail closed，不为了等待而降低唯一性约束。worker 字节发生变更，browser runtime 身份升级为 `1.61.1-r14`。

**后果**：延迟挂载的搜索框、弹层和切换后控件不再被首个空快照误判；真正不存在的控件会在固定上限内失败，多匹配仍会立即进入定位修复。旧 r13 不原地覆盖，新构建会准备并校验独立 r14。

---

## 2026-07-20 · Claude 截图附件参数必须显式终止可变参数

- **背景**：Claude Code 的 `--add-dir` 接受多个连续参数。Studio 将验证提示直接放在最后一个目录之后时，CLI 会把整段提示继续解析成目录，导致截图辅助规划、定位修复和最终证据判定在 Agent 启动前退出；上层只能看到笼统的 `browser_validator_failed`。
- **决策**：只要 Claude phase 带附件目录，就在最后一个 `--add-dir` 后追加 `--`，再传非交互 prompt。Codex 的 `--image` 继续使用相同的参数边界规则。
- **验证**：目标命令测试必须断言 prompt 前存在 `--`，避免 CLI 可变参数升级或重构再次吞掉提示。

---

## 2026-07-20 · 浏览器交互以页面实况恢复定位并保留 Agent 故障边界

**背景**：浏览器计划由验证 Agent 在页面操作前生成。即使计划结构合法，SPA 延迟渲染、自定义 tab、占位文案或组件语义变化仍可能让预生成 locator 与当前 DOM 不一致。旧链路把 Agent 附件权限、无最终输出、进程退出和页面定位失败都折叠成 `browser_validator_failed`，既无法自动恢复，也让用户只能反复重建 Case。

**决策**：模型 locator 只作为首选提示。唯一可见等待结束后，宿主从当前文档的有限交互控件集合读取 role、type、label、placeholder、name、text 和 disabled 状态，按动作兼容性与语义匹配确定候选；仅有唯一最高分候选时自动恢复。自定义文本控件只允许唯一精确可见文本恢复，多候选继续 fail closed，禁止 `first()` 猜选，禁止页面脚本执行。连续的 `fill → press` 复用已确认接受输入的同一 live locator，避免 SPA 在输入后新增同 placeholder 控件导致二次全页定位歧义。worker 协议变更使 browser runtime 升级为 `1.61.1-r16`。

验证 Agent 的附件读取、空输出、进程退出和一般失败分别持久化为稳定错误码，并记录失败发生在 planning、locator repair 或 evaluation。最终截图附件不可读时，判定阶段自动降级到已冻结的 accessibility、action、console 和 network 证据；信息仍不足则返回证据缺口，不伪造业务结论。上述 Agent 故障均允许在当前 Case 内重试。

**后果**：页面仅仅换了控件实现或渲染较慢时不再依赖模型再次猜 locator；真正歧义仍不会执行写页面操作。用户能区分权限、Agent 进程和 DOM 定位问题，并可保留当前证据直接重试。旧 r14/r15 不原地覆盖，新构建会准备并校验独立 r16。

---

## 2026-07-20 · 修复授权确认开发基线并双目标独立集成

**背景**：修复基线与环境分支已经分离，但旧流程仍把开发基线视为必须手填项，并且合并授权只把修复提交推进环境分支。用户不指定基线时流程无法启动；用户指定 feature 基线时，修复虽然从正确历史产生，却不会回写开发基线，后续开发仍可能丢失该修复。直接把整条 feature 分支合并到 test/dev 又会把无关开发历史带入环境。

**决策**：修复 Agent 启动前的授权弹窗必须展示受影响仓库并让用户确认开发基线。某仓库的基线留空时，Studio 在宿主边界把它解析为该仓库当前 Case 环境对应的分支；非桌面调用完全未提供映射时，默认选择该环境映射下的全部仓库。解析后的仓库→基线映射写入 approval scope 和 attempt input，Agent 只能从 Studio 精确 fetch、锁定的基线 SHA 创建线性修复分支。

修复分支推送后仍需要独立的合并授权。Studio 对开发基线和环境分支分别读取精确远端 HEAD、生成独立授权 key，并以修复分支为合并源分别推进两个目标。两个目标相同时只执行一次。基线推进完成后再推进环境分支；每次操作均使用 detached 临时 worktree、禁止 force push，并通过幂等检查恢复中断操作。旧的持久化合并授权没有基线字段时按历史环境单目标语义恢复，避免升级后卡死既有 Case。

**后果**：用户可以显式选择 feature 作为开发基线，也可以不填而安全回退到环境分支；修复结果最终同时进入开发基线与运行环境分支。若两条分支长期分叉，环境合并仍可能产生冲突或包含修复提交所依赖的祖先变更，因此合并授权必须展示两个目标并由用户确认。目标 HEAD 在授权后变化会使授权失效并要求重新确认；任一目标冲突或推送结果不明确都会停在可审计状态，不会继续部署。
