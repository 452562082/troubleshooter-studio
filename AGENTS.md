# AGENTS.md — 开发约束

本文件面向开发本仓库的 AI。生成机器人的说明由 `templates/workspace/AGENTS.md.tmpl` 提供。

## 项目与必读

Studio 的 CLI、桌面和 HTTP 入口共享 `internal/`；生成物安装到 Claude Code、Cursor、Codex CLI、OpenCode。故障闭环只执行排障、修复和提交。

改动前阅读 [docs/decisions.md](docs/decisions.md)、[CONTRIBUTING.md](CONTRIBUTING.md)，尤其是测试要求。操作命令、模型和路径以当前代码为准。

## MCP 约束

不在 builder 中默认硬禁写工具，如 `--read-only`、`ALLOW_*_OPERATION=false`。写操作由 skill 约束与用户授权控制；已有上游只读设置（如 MongoDB）不主动移除。

安装成功不等于 MCP 可用：必须使用 `internal/agent/self_test_mcp_probe.go` 实际探测协议和 `tools/list`。新增 builder 同步 `internal/agent/self_test_infrastructure.go` 的 `requiredMCPKeys`，覆盖注册、失败和跳过场景。

| 接入情况 | 凭据与能力 |
|---|---|
| Apollo、Consul、RabbitMQ | 不注册 MCP，有成熟 HTTP/API 替代，继续收凭据 |
| feishu_project | 当前无可用替代，停收凭据，不宣称接入成功 |
| Nacos | 自研本地 `nacos_mcp.py`，运行时登录/刷新；`nacos_config.py` 仅兜底 |

builder 文件位于 `internal/agent/`：

- `install_native_mcp_common.go`：入口与共享 helper。
- `install_native_mcp_obs.go`：Grafana、Jaeger、ELK。
- `install_native_mcp_data_stores.go`：数据层。
- `install_native_mcp_messaging.go`：飞书相关能力。

routing 的 `config-map.yaml.tmpl` 中，`mcp_server` 与 `runtime` 互斥。Nacos 使用 `runtime: nacos-mcp`，不能同时指定 `mcp_server`。

## 工程护栏

- 改配置模型同步 schema、示例、向导和导入导出。
- 删除能力同步移除 prompt、向导与 answers 的凭据收集。
- 生成器保留人工覆盖，回归见 `internal/generator/preserve_test.go`。
- 故障闭环覆盖幂等、授权、SQLite 重开、外部副作用恢复及脱敏。
- 不按故障关键词选择别的项目机器人；项目未绑定时继续普通工作。
- 不使用 `git add -A`，只暂存具体文件。
- 架构变化追加简短 ADR；被取代决策标明后继，不堆积执行日志。

## 验证

```bash
make ci
go test ./internal/agent/ -run 'TestBuildMCPServers|TestProbeMCP'
go test ./internal/generator/ -run TestGenerate
go test ./api/
```

依赖准备、覆盖率门槛和 MCP negative tests 见[开发指南](CONTRIBUTING.md)；真实平台与桌面验收见[测试指南](docs/testing.md)。
