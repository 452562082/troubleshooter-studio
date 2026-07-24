# Validation Agent Design

## Goal

`tshoot` 一键部署时同时生成排障 agent 和验证 agent。验证 agent 是一等产物，出现在已装机器人列表中，并在 Bug 工单流程里先执行取证验证，再把验证报告交给排障 agent 做根因分析。

## Background

当前 Bug 工单页已有“验证 / 排障”两个输出区域，但验证阶段仍由排障 agent 的临时 prompt 执行。这个方案能跑通最小闭环，但验证能力受限：它没有独立身份、独立 skill 入口，也不会作为已装机器人出现。用户希望验证 agent 能复用排障机器人已有的配置能力，包括知识图谱、索引、MCP、仓库映射和环境映射，并额外具备复现/回归验证能力。

## Product Model

每个系统默认部署两个 agent：

| Role | Agent ID | 职责 |
|---|---|---|
| troubleshooter | `<system>-troubleshooter` | 根因分析、日志/指标/链路/配置/代码交叉验证、输出 RCA |
| validator | `<system>-validator` | 主动复现、回归验证、收集截图/Network/console/API/trace 证据、输出验证报告 |

没有 role 元数据的老产物默认视为 `troubleshooter`，保证旧安装和旧扫描结果兼容。

## Shared Capability Base

两个 agent 共享同一份生成底座：

- `skills/routing/references/*`
- `repo-path-map.yaml`
- `service-dependency-map.yaml`
- `data-schema-map.yaml`
- `frontend-entry-map.yaml`
- `observability-map.yaml`
- `config-map.yaml`
- auto-analyze 产出的依赖图、API 索引、数据层索引
- IDE / MCP 注册配置
- install 阶段收集的凭据

共享的含义是“同一份知识和工具可被两个 agent 使用”，不是复制两套 MCP server。MCP server key 继续按系统和数据源生成，避免配置膨胀。

## Validator Capabilities

验证 agent 的主入口应聚焦取证，不做根因判断：

1. 读取 Bug 工单字段：标题、复现步骤、期望结果、环境、附件、产品/模块/严重级别、操作系统、浏览器、提交人/指派人。
2. 读取附件，图片附件优先用于理解现象和页面状态。
3. 根据 routing references 定位前端入口、API、仓库、服务和环境。
4. 能打开页面或请求接口时，优先执行主动复现。
5. 收集证据：截图、Network 请求、接口响应摘要、console errors、trace IDs、request IDs。
6. 信息不足时明确输出 gaps，不臆测。
7. 修复后可复用同一流程做回归复查，输出 `fixed_verified` 或 `still_reproduces`。

输出格式固定为结构化验证报告：

```text
verification_status: reproduced | not_reproduced | insufficient_info | fixed_verified | still_reproduces
environment: <bug env / bot env>
entry:
  frontend_url: <实际入口或 ->
  api_url: <实际接口或 ->
observed_behavior: <实际看到的现象>
expected_behavior: <工单期望>
evidence:
  screenshots: []
  network: []
  console_errors: []
  trace_ids: []
  request_ids: []
gaps: []
```

## Skill Layout

新增或强化验证入口 skill：

- `skills/bug-verifier/SKILL.md.tmpl`
- 复用 `skills/frontend-repro-investigator/` 中已有的浏览器采集、HAR、console、evidence merge 脚本。

`bug-verifier` 是 validator agent 的默认入口。它可以调用 routing、frontend-repro-investigator、配置查询、日志查询、链路查询等能力，但输出必须保持验证报告，不输出 RCA。

`incident-investigator` 仍然是 troubleshooter agent 的默认入口。

## Generation Changes

generator 层引入 `AgentRole`：

```go
type AgentRole string

const (
    AgentRoleTroubleshooter AgentRole = "troubleshooter"
    AgentRoleValidator      AgentRole = "validator"
)
```

需要生成的文件：

- Claude Code：`agents/<system>-troubleshooter.md` 和 `agents/<system>-validator.md`
- Cursor：`agents/<system>-troubleshooter.md` 和 `agents/<system>-validator.md`
- Codex：`agents/<system>-troubleshooter.toml` 和 `agents/<system>-validator.toml`
- OpenClaw：同 workspace 下注册两个 agent

skills/scripts/references 仍只生成一套。两个 agent 文件引用同一套能力底座。

## Metadata And Discovery

`tshoot.json` 增加：

```json
{
  "agent_id": "base-validator",
  "system_id": "base",
  "target": "codex",
  "role": "validator"
}
```

发现逻辑去重 key 从 `systemID|target` 改为 `agentID|target` 或 `systemID|target|role`。已装机器人页展示两个机器人，并显示角色：

- `Base 排障`
- `Base 验证`

浏览目录时两个 agent 都应可见。对 Claude/Cursor/Codex，真实部署目录下仍通过 skills 子目录里的 `tshoot.json` 作为扫描锚点；如果同一 skills 目录承载两个 agent，需要扫描元数据能表达同 target 多 role。

## Install And Apply

一键部署行为：

1. 先渲染共享 workspace。
2. 为每个目标平台生成两个 agent 定义文件。
3. 安装 skills/scripts/references 一次。
4. 安装两个 agent 定义。
5. 写入同一套 MCP 配置和凭据。
6. self-test 至少校验两个 agent 定义存在，MCP probe 继续按共享 MCP server 执行。

`apply` 更新时两个 agent 都要同步更新。保留策略仍以模板派生文件覆盖、用户人工改动保护为准。

## Bug Workbench Flow

Bug 工单开始排障时流程变为：

1. 根据用户选择的排障 agent 找同系统、同 target、同环境映射的 validator agent。
2. 启动 validator agent，输出进入 `验证证据` tab。
3. validator 完成后，将验证报告注入 troubleshooter prompt。
4. 启动 troubleshooter agent，输出进入 `排障分析` tab。
5. validator 失败或信息不足时，不强行拦截排障，但把失败原因和 gaps 注入排障上下文。

如果找不到 validator agent：

- UI 明确提示“未安装对应验证 agent”。
- 允许继续仅排障，但不显示伪造的验证过程。

## UI Changes

已装机器人页：

- 显示 role 标签：`排障` / `验证`
- 支持打开实际 agent 文件或目录。

Bug 工单页：

- “选择排障机器人”仍只列 `troubleshooter` role。
- 选择后显示匹配到的验证 agent，例如：`验证：Base validator · codex · test`。
- 如果没有匹配 validator，显示缺口，不阻断手动排障。

## Error Handling

- validator 启动失败：run 标记 failed，验证 tab 显示错误，排障 tab 可继续启动 troubleshooter。
- validator 输出为空：按 `insufficient_info` 处理，gaps 写明 agent 未返回有效报告。
- validator 被用户停止：整次 run 标记 cancelled，不继续启动 troubleshooter。
- 找不到同环境 validator：提示缺少验证 agent，用户可继续仅排障。
- MCP probe 失败：沿用现有 self-test 失败展示，不单独为 validator 复制 probe。

## Testing

后端测试：

- generator 生成两个 agent 定义文件。
- validator agent 文件包含 `bug-verifier` 入口，不包含 RCA 默认话术。
- troubleshooter agent 文件保留 `incident-investigator` 入口。
- discovery 不再把同 target 的两个 role 去重。
- install 为 Claude/Cursor/Codex/OpenClaw 安装两个 agent。
- apply 更新两个 agent。
- Bug investigation 先调用 validator，再调用 troubleshooter。
- validator 缺失时给出清晰错误并允许仅排障。

前端测试：

- 已装机器人页展示两个 role。
- Bug 工单选择列表只展示排障 agent。
- 选择排障 agent 后展示匹配 validator。
- 验证 tab 只显示 validator 输出，排障 tab 只显示 troubleshooter 输出。

手动验证：

```bash
go test ./internal/generator ./internal/agent ./internal/discover ./internal/bughub ./cmd/tshoot-desktop
npm test -- --run src/pages/BugWorkbenchPage.test.ts
npm run build
```

## Non-Goals

- 不做多个验证 agent 串行/并行投票。
- 不要求 validator 自动修复 Bug。
- 不为 validator 复制独立 MCP server。
- 不在第一期做复杂的“每个平台单独配置验证 agent” UI；默认按同 system/target/env 自动匹配。

