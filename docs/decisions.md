# 架构决策

本文保留当前设计及其原因。2026-09-15 按用户要求精简文档，已撤销功能的设计、一次性计划和重复验收日志从当前文档树移除；完整演进保存在 Git 历史中。后续重大变化追加简短决策，明确替代关系。

## 产品边界

Studio 负责配置建模、仓库扫描、生成、部署和故障闭环；生成物可脱离 Studio 在 Claude Code、Cursor、Codex CLI、OpenCode 中使用。四平台共用排障和修复角色，账号由各平台管理。

工作台只做 **排障、修复、提交**。自动复现、验证 Agent、浏览器业务回归和应用部署已撤销，OpenClaw 不再是生成或部署目标。工程测试、MCP 诊断、运行版本取证和工单登录保留。

原因：业务复现和验收交给人或既有测试系统，减少 Agent 不可靠执行对排障主线的影响。旧任务和附件可读，旧自动阶段只作迁移兼容，不重新执行。

## MCP：软约束与真实探测

默认不在 builder 层禁用写工具，以保留用户明确授权的应急操作入口；已有上游只读设置保留。具体操作仍受角色、阶段和用户授权限制。

安装后以协议握手及 `tools/list` 判断可用性，覆盖 stdio 和 HTTP；文件写入成功不能代替 runtime probe。探测具有超时和取消边界，不执行业务写工具。

| 能力 | 当前决定 | 原因 |
|---|---|---|
| Nacos | 自研本地 MCP，运行时登录及刷新 token；HTTP 脚本兜底 | 安装时固定 token 会过期 |
| Apollo | HTTP/API 主路径，继续收凭据 | 已有可用替代，无需虚假注册 MCP |
| Consul | 原 HTTP-only 决策 SUPERSEDED | 见下文 2026-09-15 Consul、SkyWalking 接入 |
| RabbitMQ | HTTP Management API，继续收凭据 | 旧候选启动失败原因 SUPERSEDED，见下文凭据绑定与 URL 兼容限制 |
| feishu_project | 不注册 MCP，停收凭据 | 当前无成熟接入或替代能力 |
| PostgreSQL | `@henkey/postgres-mcp-server` | 替换已归档的旧包，凭据通过环境传入 |

routing 用 `mcp_server` 表示直接 MCP，或用 `runtime` 选择执行器，两者互斥。Nacos 使用 `runtime: nacos-mcp`。builder 拆分与测试入口见 [AGENTS.md](../AGENTS.md)。

## 故障闭环：持久化与两次授权

SQLite 保存 Case、输入快照、阶段记录、授权、证据和追加式事件。每个阶段先创建再认领；完成意图先落盘，再提交结果，避免刷新或进程重启造成重复执行。

修复授权绑定仓库范围和开发基线，修复在隔离工作区进行，使用用户个人 Git 身份。合并是第二次授权，绑定修复 commit 和目标 HEAD；目标变化后需要重新确认。恢复前检查远端真实状态，不盲目重跑推送。Git 命令执行错误不能当成合并冲突。

提交成功只表示代码已合并推送；部署和业务验收在工作台外完成。非代码根因记录人工处置建议。证据不足、质疑根因和方案重评均保留，不能以猜测结果推进。详见[故障闭环](incident-workflow.md)。

## 证据与平台隔离

工单正文、附件和工具输出是数据，不是授权。附件绑定 Case、轮次、阶段及内容摘要；拒绝越界路径、符号链接和缺失文件，敏感信息不得进入可公开日志。

机器人按仓库路径和 Git remote 路由，未绑定项目静默旁路，归属冲突时明确处理。后台任务只使用配置中的仓库与机器人能力。Codex 使用隔离的运行配置；四平台均传递真实证据目录并校验最终结构化结果。

Cursor 通过 Agent CLI 执行。OpenCode 通过 `run --format json --agent` 执行，只有有效最终正文、成功退出且无错误时接受结果。安装/卸载只处理当前机器人命名空间，保留其他配置；OpenCode 合并 JSON/JSONC 并遵循 `XDG_CONFIG_HOME`。

## 源码与服务拓扑

CodeGraph 是可选仓库内符号、调用关系和影响面查询，不替代扫描器或跨仓拓扑。版本和逐平台摘要固定；安装探测与索引完成检查分开。索引不可用或分支不一致时回退文件读取，不能冒充运行时证据。

跨仓关系以端点证据和正式服务拓扑为准；仅高置信或人工确认的关系进入自动导航。候选、过期与拒绝关系保留原因，人工覆盖优先。兼容依赖图由正式图投影，避免维护两份真源。

## 创建向导与资源目录

创建过程分为“选择项目 → 运行方式 → 排障能力 → 确认并创建”。名称和内部标识自动派生；高级设置折叠，已添加连接仍执行检查，不能因隐藏界面绕过校验。

配置源、数据库与缓存、日志与服务状态按连接组织。同一连接可关联多个服务，人工补录不会被重新扫描覆盖。Grafana 唯一数据源候选自动填入，多候选由用户选择；修改凭据或恢复草稿后重新认证，健康接口成功不能代替数据源访问成功。

Schema 0.2 的资源目录统一仓库、服务、工作负载与环境映射；旧 YAML 自动派生兼容。普通预览和草稿不保存真实 secret，桌面凭据使用钥匙串；显式导出的可部署配置可能包含凭据，不能提交仓库。详见[资源目录](resource-catalog.md)。

## CI 与维护

本地 `make ci` 对齐 CI 的 lint、类型检查、模块一致性、审计、测试和构建。Go 版本以 `go.mod` 为准，Node 以 `.nvmrc` 为准，linter 固定 2.12.2；Python 测试共享依赖清单。

2026-09-15 的 CI 修复补齐遗漏的 lint、Python requests 和依赖安全更新，并修正新版 Git 下测试数据构造差异。检查结果绑定分支与 commit；`test` 通过不代表 `main` 已更新，历史失败运行不会自动变绿。

发布只走 CI，同步两端 commit 和 tag 后再发版。具体流程见 [CI 与发版](CI-RELEASE.md)。

## 2026-09-15：官方 MCP 接入与 Kuboard 渐进兼容

Grafana 改用官方 `mcp-grafana==1.4.2` 平台包；Redis 使用官方
`redis-mcp-server==0.5.1`；MongoDB 使用官方 `mongodb-mcp-server@2.1.1`。
固定版本后以实际工具发现和查询为准。Redis 通过环境传递完整 URI，由上游 CLI
解析，避免凭据进入进程参数；MongoDB 保留已有只读策略和连接兼容处理。

Kuboard 在资源拉取后用持久访问密钥探测 `/mcp`；成功才记录可选 `mcp_url`，
生成并部署原生 HTTP MCP。配置源与独立 K8s 连接分别命名，保留旧版 HTTP、
历史与配置解析能力；401/403 不得通过更换身份绕过。未确认的官方候选不自动
替换现有后端，Nacos 登录续期仍使用自研实现。HTTP 探测遵循协商版本并即时
读取 SSE 结果，不等待连接关闭，不向重定向目标传递凭据。

安装探测按请求 ID 匹配响应，忽略工具更新通知；MongoDB 查询先取已配置
connectionId。Redis 认证失败可能返回普通 Error 文本，skill 同时检查正文，
不能把 tools/list 或 isError=false 当成后端可用的证明。

## 2026-09-15：Consul、SkyWalking 官方 MCP

Consul 使用 HashiCorp `consul-mcp-server 0.1.4`；不复用 PATH 中的旧版，避免
[0.1.0–0.1.3 的后端覆盖和令牌隔离问题](https://discuss.hashicorp.com/t/hcsec-2026-24-multiple-vulnerabilities-impacting-hashicorp-consul-mcp-server/77612)。
按配置源与环境绑定地址、ACL token，默认普通 Consul API，不设置企业分区。
关闭启动时的外部文档下载。routing 用 `runtime: consul-mcp`，HTTP 脚本兜底并
按明确的 source 读取凭据，禁止凭据缺失时跨源回退。

SkyWalking 使用 Apache `swmcp 0.2.0`，绑定 OAP URL 与可选 Basic Auth；密码由
环境变量传递。两者下载固定发布包并核对逐平台 SHA256，缓存损坏则重装。
macOS/Linux 支持 amd64、arm64；Consul 另支持 Windows amd64，SkyWalking
暂无 Windows 发布包，继续 GraphQL API。二进制可用后仍必须做 runtime probe。
上游 SkyWalking 0.2.0 包的握手版本仍报告 0.1.0，以发布包摘要确认安装版本。

RabbitMQ 的已发布 `amq-mcp-server-rabbitmq 4.0.0` 已能启动并列工具，旧的
“候选无法启动”原因不再成立。但该版本仍要求 `connect` 的工具参数携带凭据，
按 hostname/port 重建 Management URL，不保留反向代理路径；继续使用已绑定
凭据的 HTTP Management API，不按主分支 README 未发布的功能直接替换。

## 2026-09-15：Kuboard v4 真实认证契约

Kuboard v4.2.2 与临时 K3s 实测发现，登录需要 UTF-8 密码的 Base64 编码及
`userSource: dao`。登录 JWT 必须使用 `Authorization: Bearer`，持久访问密钥
继续使用 `Kb-Access-Key`；桌面资源查询、ConfigMap 和运行时脚本统一处理。
成功登录不能替代后续资源读取验收；错误凭据保持失败，不切换身份。
