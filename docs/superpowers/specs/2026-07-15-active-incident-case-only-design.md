# 故障闭环仅展示进行中 Case 设计

## 背景

故障闭环页面当前会把同一 Bug 的进行中 Case、`fixed_verified` 完成 Case、`legacy_archived` 历史归档和 `reset_archived` 重置归档全部放入“故障 Cases”列表。每次重置或开启新一轮都会新增持久化 Case，列表会持续增长；当页面只需要处理当前故障时，这些终态记录增加了视觉负担，也让用户需要在当前工作和审计历史之间反复辨认。

本次目标不是删除归档数据，而是让工作台只呈现可继续处理的非终态 Case。终态记录继续保留在 SQLite 中，供恢复、指标和审计逻辑使用，但不再进入故障闭环页面的可见工作流。

## 已确认需求

- 页面仅展示非终态的进行中 Case。
- `fixed_verified`、`legacy_archived`、`reset_archived` 均不展示。
- 移除左侧“故障 Cases”列表列；当前 Case 生命周期改为单列全宽布局。
- 没有进行中 Case 时，整个生命周期区域隐藏。
- 没有进行中 Case 时，上方机器人操作区只提供“开启故障闭环”。
- 有进行中 Case 时，上方机器人操作区同时提供“进入故障闭环”和“重新开始故障闭环”。
- 移除“重置关系”卡片及跳转归档 Case 的入口。
- 归档数据暂时保留，不在本次实现自动删除、定期清理或保留期策略。

## 方案比较

### 方案 1：页面层只认进行中 Case，并移除列表列（采用）

`IncidentWorkbenchPage` 继续获取完整 Case 集合，但只把 `activeCaseForBug(incidentWorkflow.cases.value, tickets.selectedID.value)` 的结果作为可展示 Case。`BugCaseLifecycle` 不再承担多 Case 导航，仅负责一个当前进行中 Case 的生命周期和详情。

优点：工作对象唯一，页面状态与操作语义一致；不改变后端、数据库和恢复逻辑；可以彻底移除会持续增长的归档列表。代价：归档记录不再能从该页面打开。

### 方案 2：仅在 `BugCaseLifecycle` 内过滤终态 Case

保留页面上层的终态选择和详情状态，只在子组件隐藏列表项。改动看似较小，但上方操作区、焦点逻辑和子组件可能引用不同 Case，容易出现“界面没显示，操作仍指向归档”的状态错位。

### 方案 3：后端接口仅返回进行中 Case

从 `ListIncidentCases` 源头过滤终态记录。虽然减少前端数据，但会改变恢复、提醒、指标和其他读取方对完整 Case 集合的依赖，超出本次界面简化范围。

## 交互设计

### 1. 有进行中 Case

- 机器人操作区显示“已有进行中的 Case · 当前状态值”，实际状态值来自进行中 Case 的 `status`。
- 显示“进入故障闭环”和“重新开始故障闭环”。
- 生命周期区域直接展示该 Case，不再显示左侧 Cases 列表、Case 数量或列表选择态。
- 当前 Case 标题区保留手动刷新入口，承接原列表标题区的刷新能力。
- 生命周期、阶段操作、过程时间线和证据详情保持现有内容和行为。

### 2. 没有进行中 Case

- 机器人操作区显示“尚无进行中的 Case”。
- 只显示“开启故障闭环”。
- 生命周期区域完全不渲染，不显示终态详情，也不显示占据页面空间的空状态卡。
- 已存在的终态 Case 不改变“开启”语义；新建流程仍由后端幂等和冲突规则保证不会产生非法重复的进行中 Case。

### 3. 状态变化

- Case 进入 `fixed_verified`、`legacy_archived` 或 `reset_archived` 后，立即不再满足展示条件，生命周期区域随响应式状态更新隐藏。
- 主动重置进行中 Case 时，旧 Case 转为 `reset_archived` 后允许生命周期区域短暂隐藏；接替 Case 创建并返回后自动成为新的展示对象。
- 新建或重开成功后，沿用现有滚动和焦点逻辑，自动进入并聚焦新 Case 的当前操作或标题。
- 不向用户展示旧归档 Case 作为重开目标；当没有进行中 Case 时，用户通过“开启故障闭环”开始新一轮。

## 组件与数据流

### `web/src/pages/IncidentWorkbenchPage.vue`

- 完整 `incidentWorkflow.cases` 仍保留在控制器中。
- `selectedActiveCase` 继续由 `activeCaseForBug` 计算。
- 可展示 Case 只取 `selectedActiveCase`，不再回退到 `newestSelectedCase` 或任意终态 Case。
- 可展示详情仅在 `incidentWorkflow.detail.case.id` 等于进行中 Case ID 时成立。
- `openPreferredCase`、`enterIncidentCase`、按钮条件、生命周期渲染和焦点目标均以进行中 Case 为准；终态 Case 不再作为页面层主动选择或展示目标，控制器内部既有的完整集合与快照行为保持不变。
- 没有进行中 Case 时，启动按钮走现有 `startNewCase` 流程；不需要读取终态 Case 详情。
- 进行中 Case 的“重新开始”继续走现有 `restartIncidentCase` 和确认弹窗，旧 Case 在确认后归档并由新 Case 接替。

### `web/src/components/BugCaseLifecycle.vue`

- 组件从“Case 导航 + 当前生命周期”收敛为“单个当前 Case 生命周期”。
- 删除 `cases` 属性、Case 列表模板、Case 选择事件和对应列表样式。
- 保留 `detail`、`pending`、`error`、主操作事件和刷新事件。
- 将刷新按钮移动到当前 Case 标题区，并保持真实按钮、可读 `aria-label`、44px 点击目标和可见焦点状态。
- 布局改为一列：当前 Case 主区域和证据详情区域按文档顺序全宽堆叠。

### `web/src/components/BugCaseArtifacts.vue`

- 删除“重置关系”卡片。
- 删除 `select-case` 事件和所有归档 Case 链接。
- 证据、根因、阶段输出、代码变更、授权记录和部署观察保持不变。

### `web/src/lib/useIncidentCase.ts`

- 保持完整 Case 集合、终态定义和持久化读取行为不变。
- `activeCaseForBug` 继续作为页面选择进行中 Case 的唯一规则。
- 不在控制器层丢弃终态数据，避免影响恢复、事件合并和其他调用方。

## 错误与并发行为

- 进行中 Case 详情尚未返回时，继续显示现有加载状态。
- 已加载详情的刷新失败继续保留当前快照和现有错误提示，用户可使用当前 Case 标题区重试。首次详情读取失败时标题尚不存在，生命周期区域显示紧凑的失败说明和 44px“重试加载”按钮，不回退展示终态 Case。
- 实时事件把当前 Case 推进到终态时，页面不保留旧详情作为回退。
- 重置期间旧 Case 已归档而新 Case 尚未创建时，生命周期区域可以短暂消失；上方操作按钮保持 pending/disabled 语义，避免并发启动。
- 新 Case 返回后，只在当前 Bug 请求仍有效时更新页面和焦点，继续沿用现有 generation、identity 和 stale-response 防护。
- 不修改后端幂等键、Case 版本检查、重置确认快照或冲突处理。

## 可访问性与响应式

- 删除列表后不保留空的 `aside`、无效列表标题或不可见选择控件。
- 当前 Case 标题和刷新按钮使用语义化标题与真实按钮。
- DOM 顺序为当前 Case 概览、阶段、操作、时间线、证据详情，与视觉顺序一致。
- “进入”“重新开始”“开启”和刷新按钮继续满足键盘操作、可见焦点和最小 44px 点击目标。
- 单列区域在 375px、768px、1024px、1440px 下不得产生横向溢出。
- 已实现的时间线折叠、长文本换行和阶段输出内部滚动保持有效。

## 测试策略

先增加失败测试，再修改生产代码：

1. 同一 Bug 同时存在一个进行中 Case 和多个终态 Case 时，只加载并展示进行中 Case。
2. 仅存在 `fixed_verified`、`legacy_archived` 或 `reset_archived` 时，生命周期区域隐藏，上方只显示“开启故障闭环”。
3. 当前 Case 通过实时快照进入终态后，生命周期区域立即消失。
4. 重置流程完成后，新进行中 Case 自动展示并获得现有进入焦点行为。
5. `BugCaseLifecycle` 不再要求 `cases` 属性，不再渲染 Case 列表，刷新按钮位于 Case 标题区。
6. 首次读取进行中 Case 失败时显示错误与重试按钮；重试成功后恢复当前 Case 生命周期。
7. `BugCaseArtifacts` 即使收到带 `reset_from_case_id` 或 `superseded_by_case_id` 的详情，也不渲染“重置关系”或归档链接。
8. 现有过程时间线、主阶段操作、阶段输出和证据卡测试保持通过。
9. 响应式用例覆盖 375px、768px、1024px、1440px 的单列布局和无横向溢出契约。
10. 运行相关组件与页面聚焦测试、Web 全量测试、`vue-tsc --noEmit` 和生产构建。

## 非目标

- 不删除任何终态 Case、Attempt、证据、授权、代码变更、部署观察或事件。
- 不实现定时任务、保留期、磁盘空间阈值或手动清理按钮。
- 不修改 SQLite schema、Case 状态机、恢复流程、指标计算或提醒扫描。
- 不提供隐藏的“查看历史”入口、归档搜索、归档数量或导出。
- 不改变“重新开始故障闭环”的确认、取消、幂等和并发保护。
- 不改变 Bug 工单列表、机器人匹配或平台同步。

## 验收标准

- 页面任何位置都不展示 `fixed_verified`、`legacy_archived` 或 `reset_archived` Case。
- 有进行中 Case 时，仅展示该 Case，并提供进入与重新开始操作。
- 没有进行中 Case 时，仅提供开启操作，生命周期区域不占页面空间。
- 左侧“故障 Cases”列和“重置关系”卡片均不存在。
- 当前 Case 仍可手动刷新；新建和重置后的进行中 Case 仍会自动进入和聚焦。
- 首次详情读取失败时存在可键盘操作的重试入口，且不会显示任何终态详情。
- 归档数据继续保留，后端行为、恢复、指标和审计逻辑没有变化。
- 375px、768px、1024px、1440px 下无新增横向溢出。
- 相关测试、Web 全量测试、TypeScript 检查和生产构建全部通过。
