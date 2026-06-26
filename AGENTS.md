# AGENTS.md - 给开发本项目的 AI

这份文件给 Claude Code / Codex / Cursor 等开发助手看。生成物机器人的说明在 `templates/workspace/AGENTS.md.tmpl`。

## 项目本质

`troubleshooter-studio` 是 AI 排障机器人工作台，分两层：

| 层级 | 说明 |
|---|---|
| 本仓库 | 研制环境：CLI `tshoot`、桌面 app、HTTP server 三入口共享 `internal/`，负责 yaml 建模、仓库扫描、校验、生成、部署 |
| 产出物 | `tshoot apply` 生成的独立排障机器人：skills、MCP、话术，安装到 OpenClaw / Claude Code / Cursor / Codex CLI |

## 改动前必读

1. [docs/decisions.md](docs/decisions.md)：设计决策和踩坑记录
2. [CONTRIBUTING.md](CONTRIBUTING.md)：当前开发流程
3. `CONTRIBUTING.md` 的“测试要求”：覆盖率门槛和 MCP negative test 要求

## 核心约束

### MCP 软约束

不要在 MCP builder 里默认硬禁写工具，例如 `--read-only`、`ALLOW_*_OPERATION=false`。写操作靠 SKILL 文档软约束，保留紧急人工指令口子。

例外：上游已传的只读能力不主动去掉，例如 mongo。

### install 后必须 runtime probe

`internal/agent/self_test_mcp_probe.go` 会跑 stdio probe 和 `tools/list`。改 MCP builder 或新增 MCP 时，以 runtime probe 为准，不以 README 或源码猜工具名。

### builder skip 分两类

| 类型 | 含义 | 当前例子 |
|---|---|---|
| 方案 B | 不注册 MCP，但有成熟 HTTP/API 替代，能力完整；凭据仍收 | apollo、consul、rabbitmq |
| 真禁用 | 不注册 MCP，且无替代能力；凭据停收 | feishu_project |

nacos 不属于方案 B。当前 nacos 走自研本地 MCP `templates/.../scripts/nacos_mcp.py`，运行时 login + refresh token；`scripts/nacos_config.py` 只是 fallback。

判断标准：

- 凭据仍收 + 有替代脚本/API：方案 B
- 凭据停收 + 无替代：真禁用

### MCP builder 文件位置

| 文件 | 内容 |
|---|---|
| `install_native_mcp_common.go` | helper、`BuildMCPServers`、数据层共享 helper |
| `install_native_mcp_obs.go` | grafana、jaeger、elk |
| `install_native_mcp_data_stores.go` | 数据层 MCP |
| `install_native_mcp_messaging.go` | lark、feishu_project |

### routing 字段互斥

`templates/workspace/skills/routing/references/config-map.yaml.tmpl` 中：

- `mcp_server: <name>`：直接调 MCP
- `runtime: <type>-(mcp|http)`：交给 `config-executor` 决定 MCP 或 HTTP fallback

不要两个字段同时给。nacos 用 `runtime: nacos-mcp`，不是 `mcp_server`。

## 工程护栏

| 文件 | 作用 |
|---|---|
| `internal/agent/self_test_mcp_probe.go` | install 后 probe MCP 是否能启动和列工具 |
| `internal/agent/self_test_openclaw_probes.go::requiredMCPKeys` | 期望注册的 MCP 清单 |
| `api/handler_test.go` | HTTP 入口测试 |
| `internal/generator/preserve_test.go` | yaml prior overrides 保护测试 |
| `examples/*-troubleshooter.yaml` | config_center 类型 fixture |

## 常见反模式

- 看到 install success 就认为 MCP 可用。必须跑 self-test probe。
- 按上游 README 写工具名。必须 runtime probe。
- install 收凭据但没人用。prompt、wizard、answers 要一起停收。
- 改 schema 不改 examples。
- 大重构不写 `docs/decisions.md`。
- 加 MCP builder 不同步 `requiredMCPKeys`。

## 测试速查

```bash
# 全量
go test ./... -race

# 关键模块
go test ./internal/agent/ -run TestBuildMCPServers
go test ./internal/agent/ -run TestSelfTestOpenclaw
go test ./internal/generator/ -run TestGenerate
go test ./api/

# 覆盖率
go test ./... -cover
```

## 决策追溯

大重构、砍能力、换包时，追加 [docs/decisions.md](docs/decisions.md) ADR。不要改老条目；过时条目标 `SUPERSEDED` 指向新条目。

最近重点：

- nacos plan D：自研本地 MCP，运行时 refresh token
- rabbitmq：MCP 禁用，走 HTTP Management API
- feishu_project：真禁用，停收凭据
- postgres：迁移到 `@henkey/postgres-mcp-server`
- MCP probe 进入 self-test
- MCP 软约束哲学
