# 排障链路

本文只描述生成物中 `incident-investigator` 的排障方法。Studio 的完整 Case 编排见[故障闭环与 Agent 工作流](incident-workflow.md)；可执行真源位于：

- `templates/workspace/skills/incident-investigator/`
- `templates/workspace/skills/routing/`

## 入口与分工

故障问题先进入 `incident-investigator`，由 `routing` 确定环境、服务、仓库和数据源，再调用专业 skill。简单地址查询可直接使用 `routing`。

| 任务 | skill |
|---|---|
| 验证与回归 | `bug-verifier`、`attachment-evidence-verifier`、`api-verifier` |
| 前端证据 | `frontend-repro-investigator` |
| 代码与拓扑 | `code-intelligence-query`、`service-topology-query` |
| 变更与运行时 | `recent-changes`、`k8s-runtime-query`、`tracing-query`、`elk-log-query` |
| 配置与数据 | `config-executor`、各 `*-runtime-query` |
| 代码修复 | `bug-fixer` |

## 七步主线

### 1. 收集上下文

至少确认环境、服务或接口、对象 ID、时间窗和现象。缺信息一次只问一个；客户端问题优先获取截图、HAR、console、RUM/Sentry 或关联 trace。

先查 `known-errors.yaml` 和 `known-errors.local.yaml`。只有模式与当前证据吻合时才复用历史动作。

### 2. 对齐时间轴

优先查看故障前后的 K8s rollout、配置历史和 Git 变更。时间接近只提高优先级，不直接证明因果。

### 3. 判断影响范围

结合指标、实例状态和集群事件区分单服务、多服务或共享基础设施异常。

### 4. 追踪依赖

从正式服务拓扑和运行时 trace 沿下游定位传播路径。依赖图缺失时明确降低置信度，不能猜跨仓关系。

### 5. 多向取证

至少交叉三个相关维度：

| 症状 | 重点证据 |
|---|---|
| 数据或逻辑错误 | 数据、代码、响应、事务日志 |
| 慢或超时 | 指标、慢 span、慢查询、代码热点 |
| 5xx 或不可用 | 错误日志、错误率、近期变更、实例状态 |
| 环境或网络 | 集群事件、指标、日志、配置 |
| 浏览器问题 | 页面、Network、console、前后端关联证据 |

每轮都要说明已有证据、缺口及其对结论的影响。取不到关键证据时返回信息不足或降低置信度。

### 6. 确认根因与处置

对候选分别记录支持证据、反证和解释范围。只有一个候选能解释全部关键现象时，才能确认为根因。

- 低置信度不得给生产写命令。
- 数据已经污染时不得盲目回滚。
- 紧急操作必须包含执行前检查、执行和执行后验证。
- 代码默认值、stub 或框架行为不能冒充运行时事实。

### 7. 沉淀

仅高置信结论写入 `known-errors.local.yaml`，保存机制级模式和可复用动作，不写本次业务 ID 或敏感信息。该文件不会被 `apply` 或 `upgrade` 覆盖。

## Routing 硬约束

- datasource、数据源和配置源必须按映射选择，不能遍历试探。
- runtime host、端口和连接串以配置中心或运行时证据优先。
- 指标突变需要历史 baseline；trace 缺失先检查采样率。
- umbrella 子模块以父仓库 pin 的 commit 为准。
- CodeGraph 只提供仓库内证据，不能代替运行版本或跨仓拓扑真源。

主要映射位于 `templates/workspace/skills/routing/references/`，包括环境分支、域名、可观测性、依赖、数据源、配置源、日志和已知错误。

## 结论护栏

| 阶段 | 要求 |
|---|---|
| 输入 | 不完整就补充，不自行填值 |
| 取证 | 证据绑定环境和时间；缺口必须可见 |
| 推理 | 比较候选和反证，不用单一日志下高置信结论 |
| 处置 | 权限、风险、回滚和验证方式明确 |
| 输出 | 根因、证据、未检查范围和下一步结构化 |

规则只在真实 Case 证明有缺口时增加；长期无效或与宿主确定性校验重复的规则应删除。
