# 故障闭环与 Agent 工作流

本文说明 Studio 如何编排一个持久化 Case。排障 Agent 的内部取证方法见[排障链路](troubleshooting-flow.md)；精确状态迁移和数据结构以 `internal/bughub/` 为准。

## 核心原则

- Bug 是外部输入，Case 是闭环状态；同一 Bug 同时最多一个活动 Case。
- SQLite 保存 Case、attempt、证据、授权和事件；`CaseOrchestrator` 是唯一状态写入口。
- Agent 一次只执行验证、方案评估、修复或回归中的一个阶段，并返回结构化结果。
- Studio 负责合并；人或外部平台负责应用部署和非代码处置。
- 修复和合并分别授权；未知副作用不得当作成功或盲目重试。
- 验证与回归使用同一场景和执行协议，回归必须采集当前 attempt 的新证据。

## 主流程

代码修复：

```text
验证 → 用户确认验证结果 → 方案评估 → 修复授权 → 修复并推送
→ 合并授权 → 合并 → 人工部署 → 回归
```

非代码问题：

```text
验证 → 用户确认验证结果 → 方案评估 → 人工处置确认 → 回归
```

回归通过后 Case 进入 `fixed_verified`；仍复现则在同一 Case 中增加 cycle 并重新方案评估。重新开始会归档旧 Case，创建新的验证 Case，不删除历史或回滚外部副作用。

首次验证得到 `reproduced` 后不会自动进入方案评估。Case 停在可审计的确认门：用户认可后才创建 investigation attempt；用户认为 Agent 对场景、操作次数、期望或证据理解有误时，可以提交反馈并创建新的 validation attempt，旧结果只保留审计。

## 责任边界

| 组件 | 负责 | 不负责 |
|---|---|---|
| UI / Wails | 提交命令、展示进度 | 直接改状态 |
| `CaseOrchestrator` | 状态迁移、授权、幂等和调度 | 分析 Bug |
| `AgentPhaseRunner` | 组装阶段输入、运行 Agent、校验结果 | 自行跨阶段 |
| Browser HostVerifier | 执行受限浏览器动作并冻结证据 | 接受任意脚本或判断业务结论 |
| Git service | 校验、合并和推送环境分支 | 部署应用 |
| Deployment observer | 只读采集运行版本 | 要求用户手填版本 |
| SQLite store | 保存快照、attempt、证据和事件 | 编排流程 |

验证和回归解析到已安装机器人的 validator 角色；方案评估和修复使用选定的基础机器人。角色缺失时明确失败并提示重新部署。

## Agent 阶段

| 阶段 | 目标 | 成功结果 |
|---|---|---|
| `validation` | 在指定环境复现并登记实际/预期表现 | `reproduced` |
| `investigation` | 只读定位唯一高置信根因和处置方向 | `root_cause_ready` |
| `fix` | 从批准基线做最小修改、测试、提交并推修复分支 | `fix_pushed` |
| `regression` | 按原场景验证已部署修复或处置 | `fixed_verified` / `still_reproduces` |

每个 attempt 均遵循：

1. 编排器先事务创建 `queued` attempt。
2. Runner 原子 claim，校验 Case、cycle、phase 和机器人。
3. Agent 只写指定证据暂存区并返回严格 YAML。
4. Runner 校验环境、证据、秘密、Git checkpoint 和新鲜度。
5. 完成意图先持久化，再由编排器事务保存结果和下一状态。

只读阶段在确认无副作用时可受控重试；修复阶段先核对远端 ref，禁止直接重跑 Agent。

## 浏览器验证

Web 验证和回归由 Studio 持有浏览器：

```text
validator 根据当前观察形成内部声明式执行策略
→ HostVerifier 执行动作并脱敏取证
→ 每个现场检查点由 validator 选择继续、提问或给结论
→ validator 根据冻结证据给结论
```

允许导航、点击、填充、按键、选择、受控文件上传、等待和截图。禁止任意 JavaScript、凭据、Cookie、任意 header、宿主路径和生产写操作。

- 文件上传只能引用当前 Case 的工单附件或用户显式上传的测试文件。
- 登录由用户在 Studio 可见浏览器中完成；账号、Cookie 和 storageState 不进入 Case 文本。Studio 不根据 Cookie、localStorage、页面路径或按钮推断登录成功；用户完成登录并关闭验证浏览器后保存会话快照，必须再由用户明确确认，Studio 才创建下一次验证 Attempt。
- Web 成功结论必须有当前 attempt 的最终渲染截图。
- locator 失败是现场观察，不自动等于系统失败。Agent 可以根据冻结截图、页面结构和 Network 直接判定结果、询问用户，或生成有证据依据的后续动作；只有协议、进程或运行时故障才走系统重试。
- locator/action 自身失败不能作为 `insufficient_info` 结论；修复调用必须先根据冻结页面证据返回新的可执行策略。Worker 不按业务文案改写动作，而是优先在当前最上层弹窗、抽屉或浮层内解析真实可见控件；不同控件族独立限额扫描，唯一最佳候选才允许执行。修复耗尽后仍只有执行缺口时归为系统故障，不向用户索要下游页面证据。
- 同名控件必须通过 BrowserPlan v2 的命名 role `within` 作用域绑定到已观察的行或区域，不使用 `nth-child`、`last-child` 等位置型 CSS。定位修复候选未通过宿主协议时，Host 会把安全诊断回传给 Agent 自动纠正一次；连续失败才结束 attempt，并持久化精确诊断供界面展示。
- 生成计划前先观察全部已选端的应用入口；管理端、C 端等现场控件会统一交给验证 Agent。规划、页面定位修复或最终判定遇到业务流程歧义时返回 1–3 个具体问题，而不是猜测流程或只显示失败。问题只能收集用户掌握的业务事实，不能询问菜单、按钮文字、路由、页面结构或进入目标页的方法，也不能要求用户排查 Agent 进程、工具、附件或运行时。
- 用户回复会成为最新场景澄清；validation 和 regression 都强制作废旧 recipe，并在同一 Case 的新 attempt 中重新生成完整 `scenario_contract` 和 BrowserPlan。
- 内部执行策略连续两次未通过结构校验时，Case 保存经过安全分类的失败规则并走系统重试；无效 YAML、协议字段或 Agent 进程问题不会伪装成用户必须回答的业务问题。
- Agent 超时按 `planning`、`locator_repair`、`evaluation` 展示。定位策略超时会转入冻结证据判定；最终判定首次超时会自动使用同一份证据重试，不重新执行浏览器。

首次验证形成可接受结论后，Studio 冻结 Case 级 `ValidationRecipe`，其中包含 `scenario_contract`、场景摘要、执行策略摘要和来源 validation attempt。调度回归时，这组摘要会进入不可变的 `browser_scenario_binding`：

- 普通回归只能复用摘要完全匹配的冻结合同和执行策略；不一致时在浏览器启动前停止，不能换一个场景得出“回归通过”。
- 页面结构变化可以触发 locator 观察与修正，但修正必须保留同一合同、端范围、因果动作和证据语义。
- 回归证据必须来自当前 attempt；冻结 recipe 只保证场景一致，不能代替新证据。
- 用户明确修改业务语义时，记录合同修订并重新规划；修订不会覆盖或伪装成原始验证基线。

具体安全和协议演进见 [decisions.md](decisions.md) 中的 BrowserPlan / HostVerifier ADR。

## 授权、Git 与部署

### 修复授权

用户按仓库选择开发基线。Studio 锁定远端 commit，在自包含工作区中写入用户个人 Git 身份；无法取得个人姓名和邮箱时停止，不使用机器人作者兜底。

修复 Agent 只能推独立修复分支，并返回仓库、基线、commit、remote、目标环境分支和测试结果。远端精确 ref 是修复完成的真源。

### 合并授权

第二次授权绑定修复 commit、环境分支和授权时的目标 HEAD。目标 HEAD 变化或修复方案变化后必须重新授权。冲突和不确定 push 均保持可恢复状态，不改用户 checkout。

### 部署

Studio 不执行应用部署。用户确认已部署后，Studio 尝试只读采集版本：

- 明确与预期 commit 不一致：阻断回归。
- 匹配：记录版本并回归。
- 未配置、不可读或无法唯一识别：记录 `unavailable`，继续以新鲜业务证据回归。

HTTP 版本检查默认拒绝代理、loopback、内网、link-local 和 metadata；环境专用内网地址需对精确 host 显式启用 `allow_private`。

## 用户操作

| 场景 | 操作 | 行为 |
|---|---|---|
| 信息不足 | 补充信息并继续 | 创建父链 attempt，回到原阶段 |
| 系统或网络失败 | 重试当前验证/回归 | 保留原场景和部署绑定 |
| 页面变化 | 让 Agent 继续验证 | 获取当前页面结构并在同一合同下调整定位 |
| Agent 不理解当前流程 | 回答 Agent 的具体问题 | 以回复重建 `scenario_contract` 并创建新的验证/回归 attempt |
| 复现结论正确 | 认可验证结果 | 从冻结验证证据启动方案评估 |
| 复现结论有偏差 | 提出验证异议 | 保留旧结果审计，带反馈重新验证 |
| 需要登录 | 打开验证浏览器 | 用户完成登录并关闭窗口，保存未校验的会话快照后明确确认再继续 |
| 根因不认可 | 对根因提出异议 | 只读重评，不从验证开始 |
| 方案不认可 | 提出其他修复方案 | 重评仓库、风险、回滚和回归 |
| 已推送但想重修 | 重新修复 | 旧分支留审计，新分支从确认基线开始 |
| 非代码问题已处理 | 确认处置完成 | 保存摘要和证据后回归 |
| 当前 Agent 不应继续 | 停止当前阶段 | 取消当前 attempt，回到等待状态 |
| 完全重新开始 | 重新开始故障闭环 | 归档旧 Case，创建新 Case |

所有写动作携带 Case version 和幂等键。版本冲突时刷新快照并重新确认，不能覆盖新状态。

## 状态速查

| 分组 | 状态 |
|---|---|
| 验证 | `pending_validation`、`validating`、`waiting_evidence`、`not_reproduced`、`reproduced` |
| 方案评估 | `investigating`、`root_cause_ready`、`waiting_fix_approval`、`waiting_remediation` |
| 修复与合并 | `fixing`、`fix_failed`、`fix_pushed`、`waiting_merge_approval`、`merging`、`merge_conflict` |
| 部署与回归 | `waiting_deployment`、`deployment_unverified`、`deployment_verified`、`regression_validating`、`still_reproduces` |
| 终态 | `fixed_verified`、`legacy_archived`、`reset_archived` |

精确合法边以 `internal/bughub/workflow_transition.go` 为准。

## 证据与恢复

- 证据绑定 Case、cycle 和 attempt；回归不得复用首次验证文件冒充新结果。
- artifact 限制 16 MiB，校验路径、普通文件、大小、SHA256 和敏感信息。
- 浏览器只保存受控截图、脱敏 Network/console 和动作记录。
- completion intent、修复 checkpoint、运行 claim 和追加式事件用于跨重启恢复。
- 多仓只完成一部分、远端不可确认或状态冲突时 fail closed。
- 刷新只读取快照，不运行 Agent 或推进状态。

## 代码索引

| 主题 | 位置 |
|---|---|
| 类型与状态迁移 | `internal/bughub/workflow_types.go`、`workflow_transition.go` |
| 编排与阶段执行 | `workflow_orchestrator.go`、`workflow_phase_runner.go` |
| 浏览器协调 | `workflow_browser_*.go`、`internal/browserverify/` |
| Git、部署和恢复 | `workflow_git.go`、`workflow_deployment.go`、`workflow_recovery.go` |
| SQLite | `workflow_store.go`、`workflow_store_schema.go` |
| 桌面绑定 | `cmd/tshoot-desktop/bindings_bug_*.go` |
| 页面 | `web/src/pages/IncidentWorkbenchPage.vue`、`web/src/components/BugCase*.vue` |

修改状态、按钮、副作用或跨进程恢复时，必须补状态非法跳转、幂等、SQLite reopen/crash 和相应 fake 集成测试；架构变化追加 ADR，不改旧条目。
