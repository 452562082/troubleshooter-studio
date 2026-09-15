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

## MCP 与真实服务验收

2026-09-15 使用隔离目录、临时容器和样本数据完成以下真实读取及错误凭据检查。
这些结果覆盖连接与查询，不代表业务环境或完整模型排障已验收。

| 接入 | 真实后端 | 通过依据 |
|---|---|---|
| Grafana MCP 1.4.2 | Grafana 11.6 + Prometheus 2.55.1 | 测试数据源及实际 `up=1`；错误密码拒绝 |
| Redis MCP 0.5.1 | Redis 7.2.4 | DB 2 预置键值；错误密码不能读取 |
| MongoDB MCP 2.1.1 | MongoDB 7.0 | 通过已配置 connectionId 查询预置文档；错误密码拒绝 |
| Consul MCP 0.1.4 | Consul 1.21，启用 ACL | 读取预置 KV；错误 token 不能读取 |
| SkyWalking MCP 0.2.0 | OAP 10.3 + BanyanDB 0.9 | 官方 Python agent 上报的服务、指定 trace 和错误标记；鉴权网关拒绝错误密码 |
| Kuboard 官方 MCP / HTTP | Kuboard v4.2.2 + K3s 1.32.3 | 集群、ConfigMap 预置值和 Deployment；错误密钥/密码拒绝 |
| RabbitMQ HTTP | RabbitMQ 4.1 Management | 预置队列和消息正文，读取后重新入队；错误密码返回 401 |

Kuboard 同时通过桌面 Go 入口的账密/密钥资源树与 MCP 自动发现，以及两个共享
Python 查询脚本。真实测试修复了 v4 登录密码编码、`userSource` 和 JWT 请求头。
Redis 认证错误可能返回普通文本；必须检查正文，不能只看 `isError=false`。

部署验收按生产 builder 生成配置，先运行 initialize、tools/list，再按实际 schema
调用读取工具并核对预置值。RabbitMQ 保持 HTTP，不把候选 MCP 的启动成功算作接入。
可用显式临时 fixture 运行 Go 入口检查（常规单测跳过，不自动安装服务）：

```bash
TSHOOT_LIVE_KUBOARD_FIXTURE=/path/to/private-kuboard-fixture.json \
go test ./cmd/tshoot-desktop -run '^TestKuboardV4Real$' -count=1 -v

TSHOOT_LIVE_MCP_FIXTURE=/path/to/private-mcp-fixture.json \
TSHOOT_LIVE_MCP_REPORT_DIR=/path/to/private-report \
go test ./internal/agent -run '^TestMCPRealServicesProbe$' -count=1 -v
```

fixture 字段见对应测试文件，凭据文件权限设为 0600；协议检查产生的 `servers.json`
含临时凭据，仅供读取验收使用，不能提交。测试结束删除本轮容器、卷、网络和新拉取
镜像，并核对端口关闭、原有资源不受影响；只保留脱敏结果。
