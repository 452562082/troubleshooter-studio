# AGENTS.md — 给开发本项目的 AI 用

> **这份文件给 AI(Claude Code / Codex / Cursor 等)看,不是给本项目产出的机器人看。**
> 产出物机器人的 AGENTS.md 在 `templates/workspace/AGENTS.md.tmpl`。

## 项目本质(30 秒理解)

**troubleshooter-studio** = AI 排障机器人工作台(两层):

1. **上层(本仓库)**:研制环境 — CLI(`tshoot`)/ 桌面 app / HTTP server 三入口并列,共享 `internal/`,做 yaml 建模 / 仓库扫描 / 校验 / 生成 / 部署
2. **下层(产出物)**:`tshoot apply` 生成一个独立可运行的排障机器人(skills + MCP + 话术),装到 OpenClaw / Claude Code / Cursor / Codex CLI

## 改东西前必读

1. **决策演进**:[`docs/decisions.md`](docs/decisions.md) — 本项目踩过的坑 + 为什么是现在这样
2. **当前规范流程**:[`CONTRIBUTING.md`](CONTRIBUTING.md) — 加 mcp / 加 SKILL / 改 schema 怎么做
3. **测试要求**:CONTRIBUTING.md "测试要求" 段(覆盖率门槛 + 必加 negative test)

## 关键模式(踩过坑总结)

### 1. MCP 软约束哲学(2026-05-15 校准)

**不要**在 mcp 层硬约束写工具(`--read-only` / `ALLOW_*_OPERATION=false` 等)。**用 SKILL 文档软约束**:
- LLM prompt 级"严禁调用 X 工具"
- 留口子给紧急人工指令
- 简化 install,减少配置错配

应用在 mysql / redis / kafka / postgres / mongo / grafana / clickhouse 等。例外:`--read-only` 已经传的不主动去掉(如 mongo)。

### 2. install 后 MCP 必须 runtime probe(2026-05-15 工程化)

`internal/agent/self_test_mcp_probe.go` 自动跑 stdio probe + tools/list。**改 mcp builder / 加新 mcp 必须保证 probe 通过**。

trustchain:
- doc-level("看 README + 源码 grep")命中率 ~60%
- runtime probe("起 mcp + tools/list")命中率 ~95%
- **永远以 runtime probe 为准**(本会话 5 处 SKILL 死引用都是 README vs 实际脱节)

### 3. 禁用 mcp 时也要补 fallback(2026-05-15)

被禁的 mcp(nacos / feishu_project / rabbitmq)必须有**替代访问方式**:
- nacos → SKILL `config-executor` 内 Python HTTP API(`scripts/nacos_config.py`)
- rabbitmq → SKILL `rabbitmq-runtime-query` 内 HTTP Management API(端口 15672)
- feishu_project → **暂无**,凭据收集也停了(B 方案,等字节 v1.x 正式版)

砍 mcp 时**别只 disable builder**,必须确认 SKILL 文档有替代调用方式 + 凭据流通(`scripts/.env` / `creds.json`)对齐。

### 4. install_native_mcp_*.go 按类型拆分(2026-05-15 重构)

不要在单文件塞所有 builder。当前拆分:
- `install_native_mcp_common.go` — helper + BuildMCPServers 总入口 + 数据层共享 helper
- `install_native_mcp_obs.go` — grafana / jaeger / elk
- `install_native_mcp_data_stores.go` — 8 家数据层 + 总分发
- `install_native_mcp_messaging.go` — lark / feishu_project

加新 mcp 选对应文件;不确定就 ping 一下结构。

### 5. routing 字段两选一(2026-05-15)

`templates/workspace/skills/routing/references/config-map.yaml.tmpl` 渲染时,**`mcp_server` 字段** 跟 **`runtime` 字段**互斥:
- 有 `mcp_server: <name>` → 走 MCP(grafana / jaeger / elk / 数据层 / lark 用)
- 有 `runtime: <type>-http` → 走 SKILL HTTP 脚本(nacos / apollo / consul / kuboard 用)

**写 routing 模板时不要两个都给**(2026-05-15 apollo/consul `mcp_server` 死字段就是这种错配)。

## 工程化护栏

| 文件 | 作用 |
|---|---|
| `internal/agent/self_test_mcp_probe.go` | 每次 install 后自动 probe 所有 mcp,上游 break 立刻 FAIL |
| `internal/agent/self_test_openclaw_probes.go::requiredMCPKeys` | 期望注册的 mcp 清单,改 build 函数同步改这里 |
| `api/handler_test.go` | HTTP 入口测试(0% → 59.2%,2026-05-18) |
| `internal/generator/preserve_test.go` | yaml prior overrides 保护测试(改 SKILL 模板时跑) |
| `examples/*-troubleshooter.yaml` | 多种 config_center 类型 yaml fixture(改 schema 时跑) |

## 常见反模式 — 别这么写

1. ❌ **install 显示 success 就认为 mcp 能用** — install 只保证 `mcp.servers` 写进 IDE config,不保证进程能起。**必须跑 self-test mcp probe**
2. ❌ **看上游 README 写工具名** — README 跟实际包暴露经常脱节(本会话 5 处死引用)。**起 mcp probe 拿 tools/list**
3. ❌ **install 收凭据但没人用** — 诈骗式收集,**install_prompts 不收 → wizard 也别问** → answers.go 也别渲染。三处对齐
4. ❌ **改 schema 不改 example yaml** — 用户拿 example 当起点,字段不一致 → wizard 第一步就 valid 失败
5. ❌ **大重构不写 decisions.md** — 后人看 git log 拼不出来的事,3 个月后自己都忘
6. ❌ **加 mcp builder 不在 `requiredMCPKeys` 同步加** — `tshoot self-test` 会 FAIL "mcp.servers 不齐"

## 测试速查

```bash
# 全跑
go test ./... -race

# 关键模块
go test ./internal/agent/ -run TestBuildMCPServers   # mcp 注册
go test ./internal/agent/ -run TestSelfTestOpenclaw  # self-test
go test ./internal/generator/ -run TestGenerate      # yaml → workspace 渲染
go test ./api/                                        # HTTP 入口

# 覆盖率
go test ./... -cover
```

## 决策追溯

每次大重构 / 砍能力 / 换包 → 追加 [`docs/decisions.md`](docs/decisions.md) 一条 ADR(背景 → 决策 → 后果 → 演进)。**不改老条目**,过时标 SUPERSEDED 指向新条目。

最近的决策演进(2026-05):
- nacos 接入回归方案 B(HTTP API 主路径)+ apollo/consul 死字段清理
- feishu_project 禁用 mcp 注册 + 停收凭据(B 方案)
- rabbitmq mcp 禁用注册(上游两候选都坏)
- postgres mcp 包迁移(@modelcontextprotocol → @henkey)
- MCP probe 工程化进 self_test_openclaw
- install_native_mcp_common.go 拆分(1103 行 → 4 文件)
- MCP 软约束哲学(用户校准)

读完后改东西心里有数。有疑问 → 翻 commit log 找近半年大改动的 commit message 都写得很详。
