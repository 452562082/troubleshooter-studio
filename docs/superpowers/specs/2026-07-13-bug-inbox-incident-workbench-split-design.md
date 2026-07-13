# Bug 工单与故障闭环工作台拆分设计

日期：2026-07-13

## 背景

当前 `BugWorkbenchPage` 同时承担 Bug 平台配置、工单收件箱、机器人选择、故障 Case 列表和完整闭环操作。页面内部再使用“Bug 收件箱 / 故障闭环”标签切换，导致侧边栏只有一个入口、职责不清，用户容易把旧验证/排障运行记录与新的持久化故障闭环理解为两套并行流程。

本设计将工单管理与故障处理拆成两个一级入口。Bug 工单是事实输入和详情浏览入口；故障闭环是选择任意 Bug 后执行验证、排障、修复、人工部署等待和回归验证的唯一主动流程。

## 目标

- 左侧栏提供独立的“Bug 工单”和“故障闭环”入口。
- Bug 工单页完整展示工单内容，但不执行主动排障或 Case 操作。
- 故障闭环页可以选择任意已同步或主动拉取的 Bug 开始处理。
- 同一 Bug 默认最多存在一个未结束 Case，重复请求不重复启动 Agent。
- 用户可以将未结束闭环重置，从验证阶段重新开始，同时保留完整审计历史。
- 页面刷新、应用重启和异步 Agent 回调竞争下仍保持确定行为。

## 非目标

- 不删除旧 `InvestigationRun` 数据或兼容读取能力；旧记录继续只读展示。
- 不回滚已经推送的 Git 分支、环境分支合并或人工部署。
- 不改变修复授权、合并授权、人工部署通知和版本校验的既有边界。
- 不增加应用自动部署能力。
- 不允许从自由文本隐式跳过 Case 阶段。

## 方案选择

采用两个独立路由页面并共享数据与展示组件：

- `/bugs` 加载 `BugInboxPage`。
- `/incidents` 加载 `IncidentWorkbenchPage`。

拒绝继续让一个大页面通过 mode 或内部标签承担两套职责。虽然复用原页面的改动更少，但会延续状态耦合、重复导航和后续维护困难。共享逻辑通过 composable 与小组件实现，不复制平台、工单和机器人状态代码。

## 信息架构

### 左侧栏

新增两个并列一级入口：

- `Bug 工单`：描述为“同步工单平台，查看完整 Bug 详情”。
- `故障闭环`：描述为“选择 Bug，完成验证、排障、修复和回归”。

移除原页面内部“Bug 收件箱 / 故障闭环”标签。路由路径决定当前职责，侧边栏高亮只匹配对应入口。

### Bug 工单页

页面包含：

- Bug 平台配置、授权登录、后台同步和主动拉取。
- 工单搜索、列表和选择。
- 标题、来源、产品、模块、类型、严重程度、优先级、指派人、提交人、时间、系统、浏览器、关键词、描述和复现步骤。
- 附件缩略图与预览。
- 旧 `InvestigationRun` 的默认折叠只读历史，可切换历史阶段并复制结果，不提供停止、补充、旧修复或其他写操作。
- “进入故障闭环”按钮，跳转到 `/incidents?bug_id=<encoded-id>`。

### 故障闭环页

页面采用三块语义区域：

1. Bug 选择区：列出全部已接收 Bug，支持搜索和选择任意工单。
2. 当前处理区：展示所选 Bug 摘要、机器人选择、当前 Case 阶段和唯一主要操作。
3. 证据与历史区：展示验证证据、根因、代码变更、测试、授权、部署观察、事件时间线和该 Bug 的历史 Case。

选择 Bug 后把 `bug_id` 写入 URL 查询参数。直接打开链接、刷新页面或应用 keep-alive 恢复时，都以 URL 为选择真源；参数不存在或已失效时显示可恢复空态，不猜测其它 Bug。

## 共享前端边界

从现有大页面提取以下职责：

- 工单数据 composable：加载、同步、主动拉取、查询、选择和附件预览。
- 平台配置组件：只在 Bug 工单页使用。
- Bug 列表组件：两个页面共享，接收列表、当前 ID、加载与错误状态，通过事件返回选择。
- Bug 详情组件：Bug 工单页使用完整模式；故障闭环页使用摘要模式。
- 机器人选择组件：只在故障闭环页使用，复用已有平台映射、环境选择和 direct-launch 判断。
- Case 生命周期组件：继续复用 `BugCaseLifecycle`，但按所选 Bug 过滤 Case。

共享组件不持有后端副作用；页面或 composable 调用 bridge，并通过明确 props / emits 传递状态。

## Case 选择与唯一性规则

选中 Bug 后按以下顺序决定视图：

1. 存在未结束 Case：打开更新时间最新的未结束 Case。
2. 不存在未结束 Case但存在历史 Case：打开最新历史 Case，并提供“新建一轮排障”。
3. 从未创建 Case：显示机器人选择和“开始故障闭环”。

终态包括 `fixed_verified`、`legacy_archived` 和新增的 `reset_archived`。其它状态均视为未结束，包括等待用户补充、等待授权、等待部署和失败后可恢复状态。

`CaseOrchestrator` 继续作为唯一写入口。创建 Case 时在同一 SQLite 写事务中检查该 Bug 是否已有未结束 Case：

- 若有，返回已有 Case，不创建 attempt，不重复调度 Agent。
- 若无，创建新 Case。

该检查与创建必须处于同一写事务，不能只依赖前端按钮禁用。命令继续使用 `expected_version` 和 `idempotency_key`；同一请求重放返回同一结果。

## 重置闭环

### 用户行为

未结束 Case 显示次要危险操作“重置闭环”。点击后弹出确认框，明确展示：

- Bug 和 Case 编号。
- 当前阶段与正在运行的 attempt。
- 当前 Agent 会被停止。
- 原 Case 的证据和审计会保留。
- 已推送分支、合并和人工部署不会被回滚。
- 将创建新 Case 并从验证阶段重新开始。

确认框默认聚焦取消按钮，支持 Escape 关闭并在关闭后恢复触发按钮焦点。提交期间按钮禁用，错误在对话框和页面 live region 中可见。

### 数据模型

新增 Case 终态：

```text
reset_archived
```

`incident_cases` 增加两个可空关系字段：

- `reset_from_case_id`：新 Case 指向被重置 Case。
- `superseded_by_case_id`：旧 Case指向接替它的新 Case。

新增字段必须进入 schema migration、clone/scan、存储测试、前端类型和归一化逻辑。关系只表达重置来源，不替代 TransitionEvent 审计。

### 原子命令

新增显式 `ResetIncidentCase` 命令，输入至少包含：

- `case_id`
- `expected_version`
- `new_case_id`
- `idempotency_key`
- `actor_id`
- 继续使用的 `bot_key`

命令在一个 SQLite 写事务中：

1. 校验旧 Case 存在、版本匹配、不是 `legacy_archived`、`reset_archived` 或其它终态。
2. 若当前 attempt 未结束，将其标记为 cancelled，并清除旧 Case 的 current attempt 指针。
3. 将旧 Case 转为 `reset_archived`，设置 `closed_at` 和 `superseded_by_case_id`，写入 `case_reset` 事件。
4. 创建新 Case，设置 `reset_from_case_id`，沿用 Bug、系统、环境和所选机器人，写入 `case_created_from_reset` 事件。
5. 提交事务后，由 phase runner 调度新 Case 的验证 attempt。

运行中 Agent 的进程取消由 runner 执行，但持久化事务是最终真源。晚到的旧 Agent 完成回调因 Case version、attempt status 和 run claim 不匹配而不得推进旧 Case。若新验证调度失败，新 Case进入既有可恢复状态并记录失败事件；不得恢复旧 Case或再次创建新 Case。

重置命令重复调用时，通过幂等键返回第一次创建的新 Case。使用过期版本或旧 Case 已由其它命令推进时返回冲突，并由前端刷新最新状态。

## API 与桥接

桌面 binding 和 TypeScript bridge 增加 `ResetIncidentCase`。它返回新 Case；事件总线继续发送版本化 Case snapshot，使故障闭环页选择新 Case并刷新详情。

现有 `StartIncidentCase` 增加“已有活动 Case 则返回已有 Case”的后端约束。前端仍做友好判断，但不作为一致性保证。

列表接口可以继续返回全量 Case，前端按 `bug_id` 分组和过滤。若数据量达到需要分页的规模，再单独设计服务端过滤；本次不提前扩展分页协议。

## 异常与恢复

- Bug 加载失败：保留已经加载的 Case，展示错误并允许重试。
- Case 加载失败：保留当前 Bug 选择，不清空工单上下文。
- 机器人匹配失败：阻止新建或重置启动，但不影响历史浏览。
- URL Bug 不存在：显示“该工单不存在或尚未同步”，提供返回工单页和重新选择入口。
- 创建时发现已有活动 Case：打开后端返回的 Case，并提示“已打开现有闭环”。
- 重置版本冲突：关闭 pending 状态，刷新 Case，提示状态已变化。
- 重置事务失败：旧 Case 与新 Case均不得出现半完成关系。
- Agent 取消失败或进程已经退出：不回滚持久化重置；旧回调仍由 claim/version 检查阻断。
- 应用崩溃后：SQLite 中的旧/新 Case关系可恢复，phase recovery 只接管新 Case 的活动 attempt。

## 可访问性与响应式

- 两个侧边栏入口使用现有 router-link 语义和可见焦点态。
- Bug 列表项、Case 列表项和主要操作可键盘访问，选中状态不只依赖颜色。
- 重置确认框使用 `role="dialog"`、`aria-modal`、焦点圈定和焦点恢复。
- 375、768、1024、1440 像素宽度下不得产生页面级横向滚动。
- 小屏下三块区域按 Bug 选择、当前处理、证据历史的顺序纵向排列。

## 测试策略

### Go / SQLite

- transition table 覆盖所有允许进入 `reset_archived` 的非终态，以及终态/legacy 的非法重置。
- schema migration 与 reopen 测试覆盖两个关系字段。
- 同一 Bug并发 Start 只产生一个未结束 Case和一个首阶段 attempt。
- Reset 在一个事务中归档旧 Case、取消 attempt、创建新 Case并建立双向关系。
- 重复 Reset 返回同一新 Case，不重复 attempt 和 Agent 调度。
- 过期 version、已完成 Case、legacy Case 和已重置 Case拒绝重置。
- Reset 与 Agent 完成回调竞争只允许一个合法结果；旧回调不能推进归档 Case。
- 事务注入失败后不存在半归档、孤立新 Case或单向关系。
- SQLite reopen/crash recovery 只恢复新 Case。
- 原 Case 的 artifact、approval、code change 和 deployment observation 行数及内容保持不变。

### Web

- router 与侧边栏分别存在 `/bugs` 和 `/incidents`。
- Bug 工单页完整显示工单内容，不出现启动、授权、部署或重置闭环操作。
- “进入故障闭环”携带编码后的 Bug ID。
- 故障闭环页可以选择任意 Bug并同步 URL。
- URL 选择在 mount 和 keep-alive 返回时恢复。
- 已有未结束 Case自动打开且不调用 Start。
- 无 Case、历史终态和活动 Case分别显示正确主操作。
- 重置确认、pending、成功跳转、版本冲突和错误恢复。
- 键盘焦点、live region 和四个目标 viewport 的无溢出契约。

## 交付顺序

1. 扩展 Case 状态、schema 和原子 Reset 命令，完成后端测试。
2. 增加 desktop binding 与 TypeScript bridge。
3. 提取共享工单 composable 和展示组件。
4. 建立两个独立页面、路由和侧边栏入口。
5. 接入活动 Case选择、新建和重置交互。
6. 完成 Web 全量、Go race、覆盖率和生产构建验证。

## 验收标准

- 用户从左侧栏能明确进入“Bug 工单”或“故障闭环”，页面内不再重复同名标签。
- Bug 工单页只管理和浏览工单；故障闭环页是唯一主动处理入口。
- 故障闭环页可以选择任意 Bug，已有活动 Case自动恢复，无活动 Case可以开始新闭环。
- 重置后旧 Case只读可追溯，新 Case从验证开始，重复操作不会创建多个 Case。
- 任何失败和竞态都不会丢失审计、重复副作用或留下半完成重置关系。
