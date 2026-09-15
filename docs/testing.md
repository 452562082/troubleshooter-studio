# 测试指南

工程测试、平台协议和真实模型验收分别证明不同范围。常规检查不会自动调用付费模型；安装后 MCP 诊断仍须实际执行。

## 提交前检查

依赖准备见[开发指南](../CONTRIBUTING.md)。

```bash
make ci
make desktop-app    # macOS 桌面改动
```

`make ci` 覆盖 Go 竞态测试和覆盖率、前端测试与构建、共享 Python 脚本、lint 和依赖审计。仅 lint 通过不能代替完整 CI；线上结果必须核对分支与提交。

## OpenCode 协议测试

需已安装 OpenCode CLI；使用本地模拟模型与 MCP 服务，实际检查文件读写、事件终态、Agent 识别及 MCP 握手，不使用模型账号：

```bash
TSHOOT_LIVE_OPENCODE_PROTOCOL=1 go test ./internal/agent ./internal/bughub \
  -run 'TestOpenCode(MCPProtocolLive|LocalProtocolLive)' -count=1 -v
```

## 真实模型故障闭环

需安装并配置所选平台 CLI 和模型账号。以下测试会调用该账号的在线模型；修改 `TSHOOT_LIVE_WORKFLOW_TARGETS` 可选择 `claude-code`、`cursor`、`codex`、`opencode`，多个平台以逗号分隔并增加总超时。

```bash
TSHOOT_LIVE_WORKFLOW_TARGETS=codex \
TSHOOT_LIVE_WORKFLOW_REPORT_DIR=/tmp/tshoot-workflow-reports \
go test ./internal/bughub -run '^TestWorkflowRealModelLive$' -count=1 -v -timeout 18m
```

测试通过生产执行器、状态机、SQLite 和 Git 服务处理临时 Python 仓库中的排序故障。Git 远端仅为本地测试服务，不修改业务仓库或真实工单。

通过标准：

- 真实模型读源码、定位根因并等待修复授权。
- 授权后完成隔离修复、工程测试和修复分支推送。
- 第二次授权后合并提交；重复授权不产生重复副作用。
- 独立克隆目标分支后通过原始验收测试，测试文件未被修改。
- 原始工作区保持不变，SQLite 重开后状态和授权记录完整。

2026-09-15 的受控故障验收中，四平台均通过完整闭环；OpenCode 使用已配置的 DeepSeek 账号。此前测试的免费模型入口超时，不计为通过。这些记录不保证账号持续可用，也不能代替真实业务问题的人工验收；当前结果以重新执行后的报告为准。

## 桌面连接验收

使用独立用户目录和临时服务，避免覆盖日常草稿与机器人。至少检查：

- 无配置源时手动添加连接、关联多个服务、重新扫描和草稿恢复。
- 错误地址、端口或密码阻止继续；改正后恢复，编辑过程不意外折叠。
- 修改凭据撤销旧成功状态，迟到回复不能覆盖新输入。
- Grafana 数据源认证、唯一候选自动选择、多候选选择和失效项清除。
- Kuboard/one2all 连接复用、环境定位和生成配置一致。

2026-09-15 已用真实 Redis、Grafana、Prometheus 验收上述相关路径；one2all 使用本地协议服务，不能据此宣称业务 one2all 或 Kuboard 已联调通过。部署与 MCP 可用性需要另行检查。
