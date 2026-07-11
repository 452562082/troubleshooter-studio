# 持久化故障闭环状态机设计

日期：2026-07-11

## 1. 背景

当前排障工作台已经具备验证 Agent、排障 Agent、修复 Agent，以及工单同步、附件取证、代码修复分支和运行记录。但整个过程仍主要依赖 `InvestigationRun` 和 Agent 最终文本串联，缺少一个显式、持久化、可恢复的闭环状态机。

这会带来以下问题：

- 验证、排障、修复、等待部署和回归之间的状态隐含在文本中，应用重启后难以准确恢复。
- 用户重复点击可能重复启动 Agent，未来加入合并动作后还可能重复 merge 或 push。
- 修复分支推送后，没有结构化记录合并授权、环境分支 merge commit、部署版本和回归 attempt。
- 回归仍复现时，证据容易与第一次验证割裂，无法形成连续的诊断循环。
- 当前 `runs.json` 以整文件读写保存运行记录，不适合作为多阶段事务、幂等和审计的长期真源。

## 2. 目标与边界

### 2.1 目标

- 用一个持久化 Case 贯穿验证、排障、修复、合并、人工部署和回归验证。
- 在保证证据和根因准确率的前提下，提高自动推进比例。
- 明确每一步由 Agent、Studio、用户还是外部部署系统负责。
- 所有副作用动作可审计、可恢复、幂等。
- 回归仍复现时复用原 Case 和新证据进入下一轮排障，而不是创建孤立流程。

### 2.2 明确边界

- Studio 不控制部署平台。
- 修复完成后，只有用户明确授权，Studio 才能把修复分支合并到环境分支并推送。
- 环境分支推送后由人工部署。
- 用户通知“已部署”只触发部署版本校验；只有确认运行版本包含目标 commit 后，才启动回归验证。
- 生产环境写操作和部署始终保留人工审批。
- 本设计不引入 Temporal、Argo 等外部工作流服务。

## 3. 方案选择

选择由 Studio 实现本地持久化状态机，Agent 作为单阶段执行器。

未选择的方案：

- 继续从 Agent 文本推断全流程状态：改动小，但重启恢复、幂等、审计和跨仓库合并不可靠。
- 引入外部工作流引擎：长任务能力强，但对当前单机桌面产品增加了不必要的部署和运维成本。

## 4. 总体架构

### 4.1 组件

#### CaseOrchestrator

状态机唯一写入口，负责：

- 校验合法状态转换。
- 检查阶段输入和授权。
- 生成、校验幂等键。
- 在同一事务内写状态快照和追加事件。
- 启动对应 PhaseRunner 或外部动作。

任何 Wails binding、Agent 回调和后台恢复任务都不能绕过 CaseOrchestrator 直接修改 Case 状态。

#### CaseStore

基于本地 SQLite 保存 Case、attempt、事件、证据索引、代码变更、授权和部署观察。

默认位置：

```text
~/.tshoot/bugs/workflows.db
~/.tshoot/bugs/artifacts/<case-id>/...
```

HAR、截图、录屏、日志导出等大文件继续保存在文件系统；数据库只保存路径、SHA256、媒体类型、来源、时间和脱敏状态。

#### PhaseRunner

适配现有验证、排障和修复 Agent。每次执行只接收一个阶段的结构化输入，并返回对应阶段的结构化结果，不允许 Agent 隐式跨阶段改变流程状态。

#### GitIntegrationService

负责：

- 检查工作区和目标环境分支。
- 校验授权绑定的 fix commit。
- 校验测试结果对应当前 commit。
- 预检合并冲突。
- 合并修复分支、记录 merge commit 并通过 SSH 推送环境分支。
- 多仓库时逐仓库记录结果，不把部分成功伪装成整体成功。

#### DeploymentVerifier

根据环境和项目配置选择版本识别方式：

- 应用版本接口。
- K8s Deployment 镜像标签、annotation 或 revision。
- 运行时 build/commit 信息。
- 无自动来源时，接收用户提供的部署版本证明。

只有观测到的运行版本包含期望 commit，才允许进入回归验证。

#### CaseEventBridge

把状态变化和阶段事件推送给 Wails 前端。前端刷新或 Studio 重启后，从 CaseStore 重新加载状态，不依赖内存事件补齐历史。

## 5. 数据模型

### 5.1 IncidentCase

表示一条完整故障闭环，主要字段包括：

- `id`
- `bug_id`、`source`
- `system_id`、`environment`
- `status`
- `cycle_number`
- `current_attempt_id`
- `selected_bot_key`
- `created_at`、`updated_at`、`closed_at`
- `version`

`version` 用于乐观并发控制，避免两个界面操作覆盖彼此。

### 5.2 PhaseAttempt

每次验证、排障、修复或回归都是独立 attempt：

- `id`、`case_id`
- `cycle_number`
- `phase`: `validation | investigation | fix | regression`
- `mode`: 验证阶段使用 `reproduce | regression`
- `status`
- `agent_target`、`bot_key`
- `input_json`、`output_json`
- `parent_attempt_id`
- `started_at`、`finished_at`
- `error_code`、`error_message`

用户补充资料后再次执行 Agent，应创建新的子 attempt，并通过 `parent_attempt_id` 关联，而不是覆盖原结果。

### 5.3 EvidenceArtifact

证据记录不可变：

- `id`、`case_id`、`attempt_id`
- `kind`
- `path_or_reference`
- `sha256`
- `captured_at`
- `environment`、`version`
- `request_id`、`trace_id`
- `redaction_status`

回归验证必须采集新证据，不能复用首次验证 artifact 冒充回归结果。

### 5.4 CodeChange

按仓库记录：

- `repo`
- `base_branch`
- `fix_branch`
- `fix_commit`
- `test_evidence`
- `target_environment_branch`
- `merge_base_head`
- `merge_commit`
- `push_remote`
- `push_status`

### 5.5 Approval

记录：

- `kind`: `start_fix | merge_environment_branch`
- `actor`
- `approved_at`
- `case_version`
- `scope_json`
- `fix_commits`
- `target_branches`

授权只对记录中的 commit 和目标分支有效。commit、目标分支或 Case 版本变化后必须重新授权。

### 5.6 DeploymentObservation

记录：

- `environment`
- `expected_commits`
- `user_notified_at`
- `verification_source`
- `observed_version`
- `observed_images`
- `verified_at`
- `result`: `matched | mismatched | unavailable`

### 5.7 TransitionEvent

追加式审计事件：

- `case_id`
- `from_status`、`to_status`
- `event_type`
- `actor_type`、`actor_id`
- `idempotency_key`
- `payload_json`
- `created_at`

状态快照用于查询和展示，TransitionEvent 用于审计、恢复和问题分析。

## 6. 状态机

主流程：

```text
pending_validation
  -> validating
  -> waiting_evidence | reproduced | not_reproduced
  -> investigating
  -> waiting_evidence | root_cause_ready
  -> waiting_fix_approval
  -> fixing
  -> fix_failed | fix_pushed
  -> waiting_merge_approval
  -> merging
  -> merge_conflict | waiting_deployment
  -> deployment_unverified | deployment_verified
  -> regression_validating
  -> fixed_verified | still_reproduces | waiting_evidence
```

当回归结果为 `still_reproduces` 时：

1. `cycle_number + 1`。
2. 保存本轮部署版本和新回归证据。
3. 生成“原根因/修复未能解释哪些现象”的差分输入。
4. 回到 `investigating`，不创建新的孤立 Case。

### 6.1 自动推进

- 首次验证 `reproduced` 且无关键缺口时进入排障。
- 验证和排障这两个只读阶段发生 Agent 进程异常或超时时，同阶段最多自动重试一次。修复、合并和 push 等可能产生副作用的阶段不得盲目自动重试，必须先检查外部状态。
- 回归 `still_reproduces` 时，携带新证据进入下一轮排障。

### 6.2 用户授权

- `root_cause_ready` 后，用户允许才启动修复 Agent。
- `fix_pushed` 后，用户允许才合并并推送环境分支。
- 关键证据缺失时，用户选择补证、标记无法取得，或按现有证据继续；选择会限制后续置信度和允许动作。

### 6.3 外部等待

- 环境分支推送后进入 `waiting_deployment`。
- 用户可以点击“已部署，开始验证”或发送同义自然语言。
- Studio 先执行 DeploymentVerifier；版本不匹配时不启动回归 attempt。

## 7. 验证 Agent 复用

首次验证和修复后回归使用同一个验证 Agent，但使用不同 mode 和独立 attempt。

首次验证输入：

```yaml
mode: reproduce
bug_steps: []
expected_behavior: "..."
target_environment: test
```

回归输入：

```yaml
mode: regression
original_reproduction: "..."
original_evidence_refs: []
expected_fix_commits: []
observed_deployment_version: "..."
target_environment: test
```

回归结果：

- `fixed_verified`: 相同场景通过，且部署版本验证通过。
- `still_reproduces`: 相同问题仍存在，必须包含新鲜证据。
- `insufficient_info`: 环境、版本、测试数据或权限不足。

验证 Agent 不读取业务源码、不判断根因，也不能用首次验证证据代替回归证据。

## 8. Git 合并和推送安全

执行前重新检查：

1. 授权中的 fix commit 与当前修复结果一致。
2. 环境分支 HEAD 是否相对授权时发生变化。
3. 工作区是否干净；不提交用户已有改动。
4. 测试结果是否对应当前 fix commit。
5. merge 预检是否有冲突。
6. push remote 是否是配置或当前跟踪的 SSH remote。

合并冲突时进入 `merge_conflict`，不得自动覆盖、强推或用破坏性命令解决。Push 失败时保留本地 merge commit，重试 push，不重新执行 merge。

示例幂等键：

```text
merge:<case-id>:<repo>:<fix-commit>:<target-branch>
regression:<case-id>:<deployment-version>:<scenario-hash>
```

## 9. 工作台交互

工作台采用三栏结构：

- 左侧：Case 列表、当前状态、环境和服务。
- 中间：验证、排障、修复、合并、部署、回归六阶段进度；当前阶段只显示一个主操作；下方显示过程时间线。
- 右侧：验证证据、根因报告、代码变更、测试、授权和部署观察。

`waiting_deployment` 的主操作为“已部署，开始验证”。点击后展示目标环境、期望 commit 和将要使用的版本验证方式。版本不匹配时保留在部署阶段，并明确显示期望值和实际观测值。

## 10. 错误处理

- `not_reproduced`: 停止自动推进，由用户关闭或补证。
- 排障结论不唯一：进入 `waiting_evidence`，不启动修复。
- 修复测试失败：进入 `fix_failed`，允许基于测试输出创建后续 fix attempt。
- 合并冲突：进入 `merge_conflict`，等待用户授权处理。
- Push 失败：保留 commit 并安全重试。
- 部署版本无法识别：进入 `deployment_unverified`，要求用户提供版本信息或配置 verifier。
- 重复按钮和重复自然语言通知：通过幂等键返回已有结果。
- Studio 重启：将进程中断的 attempt 标为 interrupted；只读阶段允许恢复或重试，有副作用阶段先检查外部 Git 状态再决定继续。

## 11. 测试策略

- 状态转换表测试，覆盖全部合法和非法边。
- 幂等测试，覆盖重复修复授权、合并授权、部署通知和回归触发。
- 崩溃恢复测试，覆盖验证、修复、push 和等待部署阶段。
- Git 临时仓库 E2E，覆盖正常合并、环境分支变化、冲突、push 失败和多仓库部分成功。
- DeploymentVerifier 测试，覆盖 commit 匹配、不匹配、无法识别和多仓库版本不一致。
- 全闭环 E2E：复现到 `fixed_verified`。
- 回归失败 E2E：`still_reproduces` 到下一轮排障。
- `runs.json` 一次性迁移、重复迁移和损坏输入测试。
- Wails/UI 测试，覆盖按钮权限、刷新恢复、事件乱序和错误保留。
- 敏感信息测试，确保 token、cookie、Authorization 和个人信息不进入事件数据库。

## 12. 指标

- 首次验证、排障、修复、人工等待部署和回归耗时。
- 从 Case 创建到 `fixed_verified` 的总周期。
- 自动完成阶段比例。
- 根因进入修复后的首次回归成功率。
- `still_reproduces` 比例。
- `waiting_evidence`、`merge_conflict`、`deployment_unverified` 阻塞分布。
- 每个 Case 的 Agent 重试次数、执行时长和 token 使用量。

这些指标用于找流程瓶颈，不用于绕过证据门槛自动提升置信度。

## 13. 分阶段交付

### 第一期：状态机基础

- SQLite、Case/attempt/event 数据模型。
- 从旧 `runs.json` 一次性迁移。
- 现有 Agent 输出接入 PhaseRunner。
- Case 工作台状态展示、重启恢复和事件推送。

### 第二期：修复闭环

- 修复授权。
- Git 合并授权、环境分支合并和 SSH push。
- `waiting_deployment` 和用户部署通知。
- 验证 Agent 的 regression mode。

### 第三期：准确性与效率

- DeploymentVerifier 自动识别运行版本。
- 多仓库部署一致性校验。
- 等待超时提醒。
- 闭环指标和失败模式分析。

## 14. 完成标准

- 一个 Case 能跨 Studio 重启完整走完验证、排障、修复、授权合并、人工部署通知和回归。
- 重复操作不会产生重复 merge、push 或回归。
- 未经用户授权不能启动代码修复或合并环境分支。
- 未确认部署版本包含目标 commit 时不能进入回归。
- 回归仍复现时保留原证据并进入下一 cycle。
- 所有阶段、授权、副作用和证据都可以从工作台审计。
