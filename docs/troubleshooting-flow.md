# 排障机器人排障链路全景

> **用途**：让开发者 / SRE / PM 在 30 分钟内理解机器人在收到"X 报错 / 慢 / 不通 / 突增 / 失败"
> 类问题时的完整决策链 —— 从入口 skill 路由到根因输出 + 沉淀回写的全过程,以及每一步用到的
> 映射表 / 脚本 / 反幻觉护栏。
>
> **最后更新**:2026-05-13
>
> **源文件**(任何流程调整请同步改这两份):
> - 主编排:`templates/workspace/skills/incident-investigator/SKILL.md.tmpl`
> - 路由映射:`templates/workspace/skills/routing/SKILL.md.tmpl` + `references/*.yaml`

---

## 0. 入口与 skill 全景

```
用户问题(三入口都收)
   ├─ CLI:    tshoot
   ├─ 桌面:   Wails .app
   └─ HTTP:   sidecar mode
        │
        ▼
   Claude Agent + workspace/skills 全部加载到上下文
        │
        ▼
   LLM 看到 "X 报错 / 慢 / 不通 / 突增 / 失败" 症状词
        │
        ▼
   ★ incident-investigator(主编排) ──► 编排其它 skill,不直接跳具体 skill
```

**skills 一览**(`templates/workspace/skills/`,共 18 个):

| 类别 | skill | 角色 |
|---|---|---|
| **主线** | `incident-investigator` | 7 步排障编排器,入口 skill |
| **路由** | `routing` | env → 分支 / 域名 / 配置 / 日志 app / MCP 名 / 依赖图 / schema 映射表 |
| **链路取证** | `tracing-query` / `tempo-query` / `skywalking-query` | jaeger / tempo / skywalking 三选一 |
| **日志取证** | `elk-log-query` | ELK + grafana-loki mcp |
| **数据取证** | `mongodb / mysql / postgresql / redis / es / clickhouse / kafka / rabbitmq -runtime-query` | 8 种数据层各一个 |
| **K8s 取证** | `k8s-runtime-query` | ns-snapshot / pod logs / events / rollout |
| **代码取证** | `recent-changes` | git log / diff + 关键代码定位 |
| **配置变更** | `config-executor` | 改 prod(回滚 / nacos 改回 / 重启)的安全包装 |
| **辅助** | `diagram-generator` | 故障传导图 / 依赖图可视化 |

**为什么入口必须是 incident-investigator,不允许 LLM 直接跳具体 skill**:
- 直接跳 `tracing-query` → 跳过时间轴对齐,变更撞窗错过,容易把表象当根因
- 直接跳 `mongodb-runtime-query` → 跳过反偏科兜底,数据/逻辑类问题最容易只查日志+指标偏科
- 直接跳 `recent-changes` → 跳过依赖图传导分析,真因在下游时方向错

---

## 1. 主线 7 步(incident-investigator)

```
Step 1 收前提 + 症状验证 + 历史 grep
   │      ├─ 1.1 entity ID + 时间窗 + env + service       (任缺只反问一个)
   │      ├─ 1.1b 批量接口 N×M 数量词强制澄清              (双重门槛触发)
   │      ├─ 1.2 baseline 30s 验证症状还在不在
   │      └─ 1.3 grep known-errors{.yaml, .local.yaml}    → 命中跳过 2-6 走 next_actions
   ▼
Step 2 时间轴对齐(最高 ROI)
   │      └─ scripts/timeline.py --since 1h --incident-time T
   │         输出 ±5min 内 deploy / 配置改 / git commit
   │         event.diff_risks 自动分类(timeout_decreased / replicas_decreased 等高危直接定型)
   ▼
Step 3 横向扫(孤立 vs 广播)
   │      └─ scripts/k8s_query.py ns-snapshot --namespace <ns>
   │         只主角异常 → 孤立(Step 4) | 多服务一起异常 → 广播(查 cluster events / kube-system)
   ▼
Step 4 沿依赖图追下游
   │      └─ scripts/cascade_check.py --service <主角>
   │         读 service-dependency-map.yaml → 探每个下游健康
   │         verdict 分:
   │           isolated_downstream    → 把那个下游当主角递归 Step 4
   │           widespread_downstream  → 共享依赖,停递归
   │           all_healthy            → 真因在主角自身,Step 5
   │           no_downstream_in_map   → 依赖图没填,confidence 锁中
   ▼
Step 5 多向交叉(按 "5 维证据表" 选维度,最低 3 维起步)
   │      ├─ ★ 通用输出契约:每个子段输出 evidence_seen + missing_critical_evidence
   │      │   - critical 判定:缺了它差分诊断里至少 1 候选 explains_all_obs 会翻 no
   │      │   - 非空 → Step 6 入口强制 ASK_USER 三分支:
   │      │     (a) 帮取到 → 加入 evidence_seen   |
   │      │     (b) unavailable → confidence 上限锁中 + 快报明示风险
   │      │     (c) 用户主动跳 → confidence 上限锁低 + 快报明示"未取证据"
   │      │   - 跟 Step 6 差分诊断闭环(全候选 explains_all_obs=no → 反推补 missing)
   │      ├─ 5.1 trace + log + metric 三向
   │      │     └─ scripts/triangulate.py(客观对齐,不让 LLM 心算 correlation)
   │      │        输出 confidence: high/medium/low + consensus_service(直接照搬,不主观推)
   │      │        critical_span: status=error 最深 span / 没 error 找最长 duration
   │      ├─ 5.2 代码取证(数据/逻辑/可用性类必查)
   │      │     ├─ recent-changes: git log --since "2h" / git diff HEAD~5..HEAD
   │      │     └─ grep 关键报错串 → Read 完整函数 → 落到具体 file:line
   │      ├─ 5.3 数据取证(数据/状态/逻辑类必查)
   │      │     └─ <type>-runtime-query: db 当前值 / SQL / 三层对账
   │      │        证据必须给 表名 + 查询条件 + 行数 + 字段值对比
   │      └─ 5.4 网关 response_body(数据/逻辑类必查)
   │            ├─ 拉网关日志 → response_body 完整内容
   │            ├─ partial-success 接口按 results[i].code / error 分组
   │            └─ 三层对账:调用方说 vs response.results.length vs db 行数
   ▼
Step 6 根因 + 处置建议
   │      ├─ ★ 候选假设差分诊断(前置必跑,所有问题类型通用)
   │      │   - 列 ≥2 个候选根因(推荐 3),至少 1 个与初始直觉方向相反(防确认偏差)
   │      │   - 每个候选给 supports / refutes / explains_all_obs(yes/no) / verdict
   │      │   - 全部 explains_all_obs=no → 反推到 Step 5 对应子段补 missing_critical_evidence
   │      │     走 Step 5 通用契约的 ASK_USER 三分支(a/b/c),不允许直接锁低收尾
   │      │   - 仅 1 个 confirmed + explains_all_obs=yes → 取该候选作为根因
   │      │   - 多个 confirmed 决断不了 → confidence 锁中 + 列"区分最小补证"(同 missing_critical_evidence 格式)
   │      │   - 跳过条件:Step 1.3 known-errors 命中且证据完全吻合 typical_cause 不矛盾
   │      ├─ 置信度量化(高/中/低,按维度数 + 时间轴 + 依赖图打分)
   │      ├─ 反偏科兜底:数据/逻辑类只查日志+指标 → confidence 锁低
   │      ├─ 结论自检 2 条
   │      │   (a) 推 stub → 镜像源码 / k8s manifest / 启动日志 三选一实锤
   │      │   (b) 引 trace tag framework code → response_body 业务字段二次确认
   │      ├─ 回滚 vs 热修 决策树(数据已脏禁止盲回滚)
   │      └─ P0 命令三段安全网(PRE 看现状 / EXEC / POST 验证),low 禁止给改 prod 命令
   ▼
Step 7 沉淀(confidence=high 必跑)
          └─ scripts/sink_postmortem.py
             把本次故障抽成 pattern 追加到 known-errors.local.yaml
             (pattern 用关键词级 regex / typical_cause 写机制级 / next_actions 通用)
             下次同模式 Step 1.3 grep 命中 → 跳过 Step 2-6
```

**每步关联的脚本/数据源**(脚本带 `🗂` 标注实际 owner skill,不一定在 incident-investigator 下):

| Step | 脚本 / skill | 实际路径 | 关键数据源 |
|---|---|---|---|
| 1.3 | `grep` | (内置工具) | `known-errors.yaml` + `known-errors.local.yaml` |
| 2 | `timeline.py` 🗂 `recent-changes` | `skills/recent-changes/scripts/timeline.py` | git log + deploy 日志 + 配置中心 audit log |
| 3 | `k8s_query.py ns-snapshot` 🗂 `k8s-runtime-query` | `skills/k8s-runtime-query/scripts/k8s_query.py` | K8s API(via MCP) |
| 4 | `cascade_check.py` 🗂 `incident-investigator` | `skills/incident-investigator/scripts/cascade_check.py` | `service-dependency-map.yaml` + 各下游健康探测 |
| 5.1 | `triangulate.py` 🗂 `incident-investigator` | `skills/incident-investigator/scripts/triangulate.py` | grafana mcp (loki / prometheus / jaeger) |
| 5.2 | `recent-changes` + IDE 自带 grep/Read | (内置工具) | git 仓库 |
| 5.3 | `<type>-runtime-query` skill | `skills/<type>-runtime-query/` | 数据层 MCP(mongodb / mysql / ...) |
| 5.4 | `elk-log-query` 或 grafana-loki | `skills/elk-log-query/` | 网关 access log / 入口业务日志 |
| 6 | (LLM 推理 + 模板) | — | 上面所有证据 |
| 7 | `sink_postmortem.py` 🗂 `incident-investigator` | `skills/incident-investigator/scripts/sink_postmortem.py` | 写入 `known-errors.local.yaml` |

**为什么 timeline.py / k8s_query.py 不在 incident-investigator/scripts/ 下**:
脚本归属各自的"专业 skill" —— `recent-changes` owns 时间轴 / git 取证逻辑,`k8s-runtime-query` owns K8s 查询逻辑,
incident-investigator 只 owns 编排(cascade_check)+ 交叉对齐(triangulate)+ 沉淀(sink_postmortem)。
这样 skill 卸载 / 替换时职责边界清晰,不会带走属于编排器的脚本。

---

## 2. 横切机制(routing skill 提供)

排障的每一步都依赖 routing 的映射表回答 "去哪查 / 用什么 datasource / 调哪个 MCP":

| 映射表 | 用在哪步 | 关键契约 |
|---|---|---|
| `env-branch-map.yaml` | Step 5.2 代码取证选分支 | env → 分支对应 |
| `env-domain-map.yaml` | 复现请求时找域名 | env → 域名 |
| `observability-map.yaml` | Step 5.1 / 任何 trace/log/metric 调用 | **datasource_uid_by_env 排他**:必须显式传 UID,禁止挨个 datasource 试 |
| `service-dependency-map.yaml` | Step 4 cascade_check | 服务 → 下游 / 数据层 |
| `service-to-datastore-source.yaml` | Step 5.3 多 cluster 时调数据层 MCP | **service → source 排他**:多 source 必查映射,找不到锁低 |
| `repo-stack-map.yaml` | Step 5.2 代码取证 | repo → 技术栈 + **umbrella 子模块**(parent_repo)规则 |
| `data-schema-map.yaml` | Step 5.3 数据取证写 SQL | 表 schema 提示 |
| `log-app-map.yaml` | Step 5.1 ELK 日志查询 | service → app 名 |
| `config-map.yaml` | Step 2 配置变更定位 | 配置项 → 所在系统(nacos / apollo / configmap) |
| `config-propagation-delay.yaml` | Step 2 时间轴判变更相关性 | nacos 30s-2m / apollo 10s-30s / configmap 30s-90s |
| `known-errors.yaml` | Step 1.3 / 看到日志即 grep | 模板内置 13 条高频 pattern |
| `known-errors.local.yaml` | Step 1.3 / Step 7 sink 落点 | 用户私有沉淀(`.local` 后缀免被 `tshoot apply/upgrade` 覆盖) |

**routing 关键护栏**:

- **selector_chain 命中即停**:`prometheus.selector_chain` / `loki.logql_selector_chain` 按顺序试,
  N>0 立即停,**不要全跑一遍**(穷举是反模式)
- **baseline 必须 24h offset 比对**:任何指标"突变"必须用 `baseline_query_templates` 比 24h 前同时段,
  delta `>2x` 或 `+50%` 才算真突变,小于这个阈值视为正常波动**不能当根因证据**;
  周末 / 大促换 `7d offset`,渐变型用 `1h offset`
- **trace 找不到先看采样率**:`jaeger.sampling_rate < 0.5` → **不要直接说 "trace 不存在"**,
  改从日志找 `trace_id=` 字段;只有 `sampling_rate = 1.0` 仍找不到才是真不存在
- **umbrella 子模块 git pull 规则**:子模块的 `parent_repo` 字段指向 umbrella,
  **git pull 必须在 umbrella 根跑** + `git submodule update --init --recursive`,
  直接 `cd` 进子模块 pull 会拉到子模块 main HEAD,跟生产部署 commit 错位 → 代码定位看到的不是真问题代码
- **多 source 数据层 MCP 排他**:跟 grafana datasource_uid 同款契约,
  用户明确了 "哪个 service 走哪个 cluster",LLM 不该自己猜

---

## 3. 取证 skill 调用拓扑(Step 5 内部)

```
                          ┌─────────────────────────────┐
                          │ Step 5 incident-investigator │
                          │  按 "5 维证据表" 选必查维度  │
                          └─────────────────────────────┘
                                        │
        ┌──────────┬──────────┬─────────┴────────┬──────────┐
        ▼          ▼          ▼                  ▼          ▼
     [链路]     [日志]     [指标]             [代码]      [数据]
        │          │          │                  │          │
   tracing-     elk-log-   grafana-           recent-    <type>-runtime-
   query        query      mcp                changes    query (×8)
        │          │      (prometheus)          │          │
        ▼          ▼          │                  ▼          ▼
   jaeger      ELK ES         ▼              git log    mongodb /
   tempo       grafana-     prometheus       grep       mysql /
   skywalking  loki                          Read       postgresql /
                                                        redis /
                                                        es /
                                                        clickhouse /
                                                        kafka /
                                                        rabbitmq
        │          │          │                  │          │
        └──────────┴──────────┴──────────────────┴──────────┘
                                │
                                ▼
                      scripts/triangulate.py
                      输出 confidence: high/medium/low
                      consensus_service: <主角名>
                      critical_span: <最深 error span>
                                │
                                ▼
                        Step 6 根因 + 自检
```

**5 维证据表**(决定 Step 5 选哪些维度):

| 症状关键词 | 必查维度 | 可选维度 |
|---|---|---|
| **数据/状态类**:数据不一致 / 余额不对 / 订单异常 / 漏发货 / 重复扣款 | db + 代码 + **网关 response_body** + 日志事务时序 | 链路 |
| **逻辑/计算类**:金额算错 / 优惠没生效 / 规则没触发 / 限流没生效 | 代码 + db + **网关 response_body**(批量接口) + 日志中间结果 | 指标 |
| **性能类**:慢 / 超时 / 卡 / P95 升高 | 指标 + 链路慢 span + db 慢查询 + 代码热点路径 | 日志 |
| **可用性类**:5xx 突增 / down / 不通 / 报错暴涨 | 日志 error pattern + 指标错误率 + 代码近期变更(`git log`) | 链路 + db(若 query 异常) |
| **环境/网络类**:某 env 不通 / 跨服务断连 / DNS 失败 | 指标 + 日志 + ns-snapshot + cluster events | 代码(若超时配置变了) |

**判断逻辑**:命中 ≥1 个关键词 → 该维度从 "可选" 升 "必查";没命中关键词 → 默认按可用性类处理。

---

## 4. 沉淀回路(机器人越用越懂的核心)

```
Step 6 confidence=high
       │
       ▼
Step 7.1 故障快报最后强制塞 "沉淀草稿" JSON
       (pattern / typical_cause / next_actions / mitigation / causation_chain)
       │
       ▼
scripts/sink_postmortem.py
       ├─ 校验 pattern 是合法 regex / 必填字段齐
       ├─ 去重:已存在同 pattern 跳过 (stderr 打 [skip])
       ├─ 首次自动创建 .local.yaml 带头注
       └─ 追加 # auto-sunk YYYY-MM-DD by incident <env>/<service>
       │
       ▼
known-errors.local.yaml
       │
       ▼
下次同模式故障 → Step 1.3 grep 命中 → 跳过 Step 2-6 直走 next_actions
       │
       ▼
带 causation_chain.check_downstream_for 的 pattern
       → 主动跑 cascade_check 看下游是否出现 chain 列的模式
       → 命中 = 真因在下游
```

**抽象原则**(避免污染速查表):

- `pattern` 写**关键词级 regex**,不写具体值(用 `[\d\.]+` 而非 `30`)
- `typical_cause` 写**机制级**描述(为什么发生),不写本次业务上下文
- `next_actions` 是**别人遇到能照做的**通用步骤,不是本次特殊操作
- `mitigation` 给临时止血,根治措施在文档
- 命中 `diff_risks` 时把 risk hint 抄进 `typical_cause` 一定不出错

**为什么写 `.local.yaml` 而不是 `known-errors.yaml`**:

- 后者是**模板内置**,`tshoot apply` / `tshoot upgrade` 重灌时**被覆盖**,沉淀清零
- `.local.yaml` 是**用户私有**(`apply_helpers` 按后缀识别免删),沉淀**永久保留**

**跳过沉淀的场景**:

- `confidence = low / medium` → 沉淀风险高(可能把推测当事实污染速查表),跳过
- 已存在相似 pattern → `sink_postmortem.py` 自动去重
- 用户在故障快报里说 "先不复盘" → 跳过

---

## 5. 反幻觉护栏全景

按 "什么阶段被拦" 分层:

| 阶段 | 护栏 | 作用 |
|---|---|---|
| **入口** | 1.1 entity ID 缺只反问一个 | 防一上来连环逼问 |
| **入口** | 1.1b 批量接口 N×M 强制澄清 | 防 "10 部 182 条" 这种调用拓扑歧义直接进查询 |
| **取证选择** | 5 维证据表必查维度 | 反偏科:数据类不能只看日志+指标 |
| **取证选择** | grafana datasource UID 排他 | 防多 datasource 挨个试,只调用户选定的那个 |
| **取证选择** | service → datastore source 排他 | 多 cluster 时防瞎选 source |
| **取证执行** | selector_chain 命中即停 | 防 fallback 链当穷举 |
| **取证执行** | baseline 24h offset 必比 | 防把正常波动当突变 |
| **取证执行** | trace 拉不到先看采样率 | 防把 "采样率低" 误判成 "trace 不存在" |
| **取证执行** | umbrella git pull 规则 | 防代码定位看到 main HEAD 不是部署 commit |
| **取证执行** | **★ 子查询必输出 `missing_critical_evidence` + Step 6 入口 ASK_USER** | 防 LLM 用"看到的"凑故事忽略"应看未看"的证据;跟差分诊断对偶闭环 |
| **结论** | **★ 差分诊断 ≥2 候选 + 反证 + explains_all_obs** | **防"找证据凑单一假设",至少 1 候选必须与初始直觉相反防确认偏差** |
| **结论** | 反偏科兜底 | 数据/逻辑类只查日志+指标 → confidence 锁低 |
| **结论** | 推 stub 必须 3 选 1 实锤 | 防 "duration 短 + 0 db span" 误判 stub |
| **结论** | trace tag framework code ≠ 业务码 | 防 grpc `app.biz.code=0` 被当业务成功 |
| **置信度** | high 要 3 维交叉 + 时间轴 + 依赖图全满足 | 防高置信度通胀 |
| **处置** | P0 命令三段(PRE / EXEC / POST) | 改 prod 前必须先看现状 |
| **处置** | `confidence = low` 禁出修改 prod 命令 | 防低证据下硬操作 |
| **处置** | 数据已脏禁止盲回滚 | 防止回滚反而扩大损失 |
| **沉淀** | `confidence = high` 才 sink | 防错误模式污染 `known-errors.local.yaml` |
| **沉淀** | `sink_postmortem.py` 按 pattern 自动去重 | 防重复条目 |

---

## 6. 一句话总结

**外层(入口澄清) → 时间轴撞变更 → 横向看孤立广播 → 依赖图追下游 →
5 维交叉取证(triangulate 客观对齐) → 结论自检与置信度门控 → 写盘沉淀**。

每步都有 "反偏科 + 反幻觉" 护栏,遇到 "调用方说 X 实际 Y / batch 接口 / stub 推断 / framework code"
这类高陷阱模式都被针对性条款拦住。

"经验积累" 通过 `known-errors.local.yaml` 实现 —— sink 一条好 pattern 等于让所有未来同类问题
**跳过 Step 2-6 直接给答案**。机器人越用越懂的核心机制。

---

## 7. 维护说明

**什么时候更新本文档**:

- 给 incident-investigator SKILL.md.tmpl 加 / 删 / 改 Step → 同步更新本文档"主线 7 步"段
- routing skill 加新映射表 / 改契约 → 同步"横切机制"段
- 加新取证 skill(比如新增 `kafka-runtime-query`)→ 同步"skills 一览" + "取证拓扑"段
- 加新反幻觉护栏(典型场景:新一次故障复盘发现新陷阱)→ 同步"护栏全景"表

**版本对齐自检**(每次 review 本文档时跑):

```bash
# 1. skills 数量对齐:文档说 18 个,实际目录数应一致
ls templates/workspace/skills | wc -l

# 2. routing references 数量对齐:文档表格列数应一致
ls templates/workspace/skills/routing/references | wc -l

# 3. 排障编排涉及的 5 个脚本完整性(分散在 3 个 skill 下)
ls templates/workspace/skills/incident-investigator/scripts  # 期望:cascade_check.py / sink_postmortem.py / triangulate.py
ls templates/workspace/skills/recent-changes/scripts          # 期望:timeline.py 在内
ls templates/workspace/skills/k8s-runtime-query/scripts       # 期望:k8s_query.py 在内
```
