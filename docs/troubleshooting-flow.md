# 排障方法

生成机器人的排障主线由 `incident-investigator` 编排，`routing` 确定项目、环境、服务和数据源。工作台的状态与授权见[故障闭环](incident-workflow.md)。

## 七步取证

| 步骤 | 要做什么 |
|---|---|
| 收集上下文 | 确认环境、接口或服务、对象、时间窗与现象；对照已知错误 |
| 对齐时间轴 | 查看 K8s rollout、配置历史和 Git 变更，时间接近不等于因果 |
| 判断影响范围 | 用指标、实例和集群状态区分单服务、多服务与共享依赖异常 |
| 追踪依赖 | 按正式拓扑和真实 trace 沿调用链定位，缺失关系不得猜测 |
| 多向取证 | 交叉相关的日志、指标、数据、响应、配置与代码，说明缺口 |
| 确认根因 | 比较候选、支持证据和反证，给出置信度与处置建议 |
| 沉淀 | 仅把高置信、可复用的机制写入 `known-errors.local.yaml` |

## 能力分工

| 证据 | skill |
|---|---|
| 已有前端截图、HAR、console 等 | `frontend-repro-investigator` |
| 源码与服务关系 | `code-intelligence-query`、`service-topology-query` |
| 变更、运行时与链路 | `recent-changes`、`k8s-runtime-query`、`tracing-query` |
| 日志与指标 | `elk-log-query`、`grafana-observability-query` |
| 配置与数据 | `config-executor`、各数据层 `*-runtime-query` |
| 已授权代码修复 | `bug-fixer` |

前端取证消费已有材料，不启动自动复现或浏览器业务验证。

## 结论边界

- 数据源按环境映射选择，不能遍历账号或连接试探。
- 运行地址和部署版本以运行时证据为准，源码默认值不能冒充现场事实。
- 指标突变需要历史基线；trace 缺失先检查采样情况。
- CodeGraph 只提供仓库内证据，不替代运行版本或跨仓拓扑。
- 缺少关键证据就补证或降低置信度，不用单一日志下高置信结论。
- 处置建议说明权限、影响与回滚方式；低置信度不得给生产写命令，数据污染时不能盲目回滚。
- 沉淀内容不含本次业务 ID 或敏感信息；`known-errors.local.yaml` 在更新机器人时保留。

执行规则见 [incident-investigator 模板](../templates/workspace/skills/incident-investigator/) 和 [routing 模板](../templates/workspace/skills/routing/)。
