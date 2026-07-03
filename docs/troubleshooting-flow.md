# 排障链路

本文说明生成物机器人收到“报错 / 慢 / 不通 / 突增 / 失败 / 为什么”类问题后的默认流程。源文件：

- `templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
- `templates/workspace/skills/routing/SKILL.md.tmpl`
- `templates/workspace/skills/routing/references/*.yaml.tmpl`

最后更新：2026-06-26。

## 入口

```text
用户问题
  -> incident-investigator
  -> routing 查 env/service/source
  -> 按证据类型调用专业 skill
  -> 根因、处置、沉淀
```

排障问题必须先走 `incident-investigator`。不要直接跳 `tracing-query`、`mongodb-runtime-query` 或 `recent-changes`，否则容易漏掉时间轴、依赖传导或关键证据。

简单查询例外，例如“prod grafana 地址是什么”，直接走 `routing`。

## Skill 分工

| 类型 | skill |
|---|---|
| 主流程 | `incident-investigator` |
| 路由 | `routing` |
| 最近变更 | `recent-changes` |
| 配置 | `config-executor` |
| K8s | `k8s-runtime-query` |
| 链路 | `tracing-query`、`tempo-query`、`skywalking-query` |
| 日志 | `elk-log-query` |
| 数据 | `mongodb`、`mysql`、`postgresql`、`redis`、`es`、`clickhouse`、`kafka`、`rabbitmq` runtime query |
| 图表 | `diagram-generator` |

## 7 步主线

### 1. 收前提

目标是拿到最低限度上下文：

- env
- service / interface
- entity ID
- 时间窗
- 错误现象

缺字段时一次只问一个。客户端问题先要 HAR、RUM/Sentry 链接、console 报错或后端 trace_id 关联，不能只查后端。
客户端复现问题先走 `frontend-repro-investigator`:HAR/console/RUM 任一证据 → 提取失败请求/trace_id/静态资源问题 → 再接 backend trace/log/runtime；没有浏览器证据时不得只凭后端 trace 下结论。

先 grep：

- `known-errors.yaml`
- `known-errors.local.yaml`

命中且证据吻合时，直接走历史 next_actions。

### 2. 对齐时间轴

优先查故障时间前后变更：

- K8s rollout
- 配置中心 history
- git log / diff

故障时间 ±5 分钟内的高危变更优先级最高。典型高危包括超时变小、重试/熔断删除、replicas 下降、SQL 条件放宽、async 改 sync、缓存装饰器删除。

### 3. 横向判断

用 K8s 快照和指标判断：

- 单服务异常：进入 Step 4
- 多服务异常：先查集群事件、共享依赖、kube-system

### 4. 沿依赖图追下游

读 `service-dependency-map.yaml`，按主角服务追下游：

| 结果 | 动作 |
|---|---|
| `isolated_downstream` | 把异常下游作为新主角继续查 |
| `widespread_downstream` | 查共享依赖 |
| `all_healthy` | 回到主角自身 |
| `no_downstream_in_map` | 依赖图不足，置信度上限降到中 |

脚本路径按 owning skill 写 workspace 根相对路径，例如：

```bash
python3 skills/incident-investigator/scripts/cascade_check.py ...
python3 skills/recent-changes/scripts/timeline.py ...
python3 skills/k8s-runtime-query/scripts/k8s_query.py ...
```

### 5. 多向取证

至少 3 个维度交叉。按症状选必查维度：

| 症状 | 必查 |
|---|---|
| 数据/状态不一致 | db、代码、网关 response_body、日志事务时序 |
| 逻辑/计算错误 | 代码、db、response_body、日志中间结果 |
| 慢/超时/P95 升高 | 指标、慢 span、db 慢查询、代码热点 |
| 5xx/down/报错暴涨 | error 日志、错误率指标、近期代码变更 |
| 环境/网络问题 | 指标、日志、ns-snapshot、cluster events |
| 白屏/卡顿/上传失败/浏览器独有 | HAR、RUM、bundle 版本、console、后端关联日志 |

取证输出必须写清：

- 看到了什么证据
- 哪些关键证据缺失
- 缺失证据为什么会影响结论

缺失关键证据时，不硬凑结论。走三分支：

- 能取到：继续取证
- 取不到：置信度上限中
- 用户要求跳过：置信度上限低

### 6. 根因与处置

先做差分诊断：

| 字段 | 要求 |
|---|---|
| `supports` | 支持该候选的证据 |
| `refutes` | 反证 |
| `explains_all_obs` | 是否解释所有已知现象 |
| `verdict` | confirmed / rejected / uncertain |

只有一个候选 confirmed 且解释全部现象，才能作为根因。多个候选无法区分时，给最小补证清单。

处置要求：

- `confidence=low` 不给改生产命令
- P0 命令必须分 PRE / EXEC / POST
- 数据已脏时禁止盲回滚
- “stub”“framework code”“代码默认配置”都必须二次确认，不能直接当业务事实

### 7. 沉淀

`confidence=high` 时把本次故障写入 `known-errors.local.yaml`：

- `pattern`：关键词级 regex，不写具体业务值
- `typical_cause`：机制级原因
- `next_actions`：下次可复用动作
- `mitigation`：临时止血
- `causation_chain`：可选，下游传导模式

`.local.yaml` 不会被 `tshoot apply/upgrade` 覆盖。

## routing 契约

| 文件 | 用途 |
|---|---|
| `env-branch-map.yaml` | env 到代码分支 |
| `env-domain-map.yaml` | env 到域名 |
| `observability-map.yaml` | Grafana datasource、trace/log/metric 路由 |
| `service-dependency-map.yaml` | 服务下游关系 |
| `service-to-datastore-source.yaml` | 服务到数据源 |
| `repo-stack-map.yaml` | repo 技术栈、umbrella 子模块关系 |
| `data-schema-map.yaml` | 表结构提示 |
| `log-app-map.yaml` | service 到日志 app |
| `config-map.yaml` | 配置项到配置源 |
| `config-propagation-delay.yaml` | 配置生效延迟 |
| `known-errors.yaml` | 模板内置历史模式 |
| `known-errors.local.yaml` | 用户私有沉淀 |

硬约束：

- datasource UID 和 datastore source 必须按映射表选，不能挨个试。
- `selector_chain` 命中即停。
- 指标突变必须做 24h offset baseline；大促/周末用 7d offset。
- trace 找不到先看采样率；低采样率不能直接判定 trace 不存在。
- umbrella 子模块代码以 umbrella pin 的 commit 为准，不能直接拉子模块 main HEAD。
- 涉及 runtime host、IP、端口、连接串时，配置中心真值优先于代码仓库默认配置。

## 反幻觉护栏

| 阶段 | 护栏 |
|---|---|
| 入口 | 缺上下文一次只问一个；批量接口先澄清 N×M；客户端问题先要前端证据 |
| 取证 | 按 5 维证据表选必查维度；缺关键证据要明示 |
| 执行 | datasource/source 排他；baseline 必比；采样率先查；脚本路径必须真实存在 |
| 结论 | 必做候选差分诊断；日志一维置信度低；数据/逻辑类不能只看日志和指标 |
| 处置 | 低置信度禁改生产；P0 命令三段式；数据已脏禁盲回滚 |
| 沉淀 | 只在高置信度写 `known-errors.local.yaml`；脚本去重 |

## 维护规则

改下面内容时同步更新本文档：

- `incident-investigator` Step 增删改
- `routing` 映射表或契约变化
- 新增/删除取证 skill
- 新增反幻觉护栏
- 脚本路径或 owning skill 变化

自检命令：

```bash
ls templates/workspace/skills | wc -l
ls templates/workspace/skills/routing/references | wc -l
ls templates/workspace/skills/incident-investigator/scripts
ls templates/workspace/skills/recent-changes/scripts
ls templates/workspace/skills/k8s-runtime-query/scripts
```

## 演进原则

规则越多不等于准确率越高。只在真实 case 证明现有流程漏判时加规则；执行率长期为 0 的规则应删除或软化。

当前重点不是继续堆 prompt，而是用真实排障回放验证规则是否有效。
