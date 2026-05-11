《Agent创新、提效提报申请表》

提报部门: 娱乐部
提报日期: 2026 年 5 月 11 日
申请编号: AGENT-20260511-AI排障机器人工作台

---

第一部分:申请人及Agent信息

1. Agent名称: AI 排障机器人工作台(troubleshooter-studio)

2. 主要开发者(姓名/工号): xiaolong

3. 协作开发者(如有,并注明开发者分工比例,会作为奖金分配依据):
   ____________________(待填)

4. 主要开发者/协作开发者TG: 13666015490

5. 预期申请等级: ☑ S级(全公司通用) □ A级(部门通用) □ B级(小组通用)
   理由:无业务耦合,跨技术栈(Go/Java/Python/Node/PHP)、跨配置中心(Nacos/Apollo/Consul/k8s/env)、
   跨数据层(9 类)、跨 4 个 AI 平台(Claude Code/Cursor/Codex/OpenClaw),任何研发团队 yaml 配一份即可启用

6. 人均提效价值预估:
   - 全公司 MTTR(平均故障恢复时间)预计缩短 55%(从 38 分钟降到 17 分钟,2 周观察期实测)
   - 降低跨部门协同沟通成本(减少故障复盘中 73% 的重复性数据搜集工作,从 1.5 人小时降到 0.4 人小时)
   - 赋能非技术/初级运维人员,使其具备处理 80% 常见系统故障的能力(新人独立完成排障比例从 0% 提到 60%)
   - 复发故障识别率从 30%(靠人脑记忆)升到 92%(known-errors.local.yaml 自动沉淀 + 下次 grep 命中)

7. 预期Skill等级(L1-L4): L3
   能完整自主完成"症状 → 时间轴 → 横向 → 纵向 → 三向交叉 → 根因 → 处置建议 → 经验沉淀"端到端排障任务,
   人介入只在 P0 命令执行前确认

8. 组内成员及提效贡献比例,5-10 名(比例作为提效奖金分配依据):

   花名                  TG号                     提效比例
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________
   ____________________  ____________________     __________

---

第二部分:Agent详情说明

1. 核心解决的问题或需求(请描述业务痛点):

   ① 技术栈孤岛:公司各部门监控标准不一,跨部门调用链路(gRPC/HTTP)在出现排障时,数据对齐极度困难

   ② 排障经验难以沉淀:高级工程师的排障思路无法标准化,导致每次故障都在"重复造轮子"找原因;
      新人接手需高级工程师手把手带 3-6 个月才能独立处理生产故障

   ③ 大模型落地难:纯通用大模型不理解公司内部监控指标(Metrics)、配置中心数据结构、业务依赖图,
      无法直接落地实操,需要大量上下文工程

   ④ 多 AI 平台割裂:Claude Code / Cursor / Codex CLI / OpenClaw 各 IDE 的 agent 定义和 MCP 注册方式不同,
      同一团队不同人用不同平台,无法共享排障能力

2. 核心功能与运作逻辑(附两张流程图):

下面两张流程图用 **Mermaid** 格式描述,GitLab markdown 自带渲染,飞书文档 / Notion / Typora / Confluence 也都原生支持。如果你的查看环境不渲染 Mermaid,可以把代码块整段复制到 <https://mermaid.live> 在线导出 PNG / SVG。

### 流程图 1:两层架构 + 4 平台部署矩阵

```mermaid
flowchart TB
    classDef studio fill:#e3f2fd,stroke:#1976d2,stroke-width:2px
    classDef target fill:#fff3e0,stroke:#f57c00,stroke-width:2px
    classDef share fill:#f3e5f5,stroke:#7b1fa2,stroke-width:1.5px

    subgraph L1["上层:troubleshooter-studio 研制工作台(部门管理员一次性配置)"]
        direction LR
        W["wizard<br/>10 步问答<br/>→ troubleshooter.yaml"]:::studio
        A["analyzer<br/>5 栈代码扫描<br/>→ service+依赖+schema"]:::studio
        G["gen<br/>模板渲染<br/>→ staging 产物"]:::studio
        I["install --target X<br/>装到目标 AI 平台"]:::studio
        W --> A --> G --> I
    end

    subgraph L2["下层:排障机器人(脱离 studio 独立运行)"]
        direction TB
        subgraph Plats["4 个 AI 平台部署位置(任选 1+)"]
            direction LR
            OC["OpenClaw<br/>~/.openclaw/workspace/<br/>+ openclaw.json<br/>(mcp.servers)"]:::target
            CC["Claude Code<br/>~/.claude/agents/&lt;n&gt;.md<br/>+ ~/.claude.json<br/>(mcpServers)"]:::target
            CU["Cursor<br/>~/.cursor/agents/&lt;n&gt;.md<br/>+ ~/.cursor/mcp.json<br/>(mcpServers)"]:::target
            CX["Codex CLI<br/>~/.codex/agents/&lt;n&gt;.toml<br/>(TOML 内嵌<br/>[mcp_servers.*])"]:::target
        end
        Skills["共享 skill 集合(按 yaml 动态裁剪,19 候选)<br/>routing / incident-investigator / recent-changes<br/>+ config-executor + 9 数据层 + 5 obs + diagram"]:::share
        MCPs["13 种 MCP × N env<br/>grafana(+loki+prom+tempo)/ jaeger / es / elk<br/>mongo / pg / redis / mysql / clickhouse<br/>nacos / lark / feishu_project"]:::share

        OC --> Skills
        CC --> Skills
        CU --> Skills
        CX --> Skills
        Skills --> MCPs
    end

    I -.装到.-> OC
    I -.装到.-> CC
    I -.装到.-> CU
    I -.装到.-> CX
```

### 流程图 2:排障 7 步主流程 + 经验沉淀闭环

```mermaid
flowchart TB
    classDef step fill:#e8f5e9,stroke:#388e3c,stroke-width:1.5px
    classDef fast fill:#fff3e0,stroke:#f57c00,stroke-width:2px
    classDef start fill:#e3f2fd,stroke:#1976d2,stroke-width:2px
    classDef loop fill:#fce4ec,stroke:#c2185b,stroke-width:2px,stroke-dasharray:5

    Start(["工程师描述症状<br/>env + service + 时间窗 + 关键词"]):::start
    S1{"Step 1.3 grep<br/>known-errors.yaml(模板)<br/>+ known-errors.local.yaml(沉淀)<br/>是否命中 pattern?"}:::step
    Fast["✨ 快路径<br/>直走 next_actions<br/>跳过 Step 2-6<br/>⏱ 30s-2min 出处置建议"]:::fast
    S2["Step 2 timeline 三路合并<br/>git log + K8s rollout + nacos history<br/>→ 自动标 ±5min 内 17 类 risk<br/>(配置 12 类 + 代码 5 类)"]:::step
    S3["Step 3 横向<br/>同 env 别 service<br/>孤立 vs 广播?"]:::step
    S4["Step 4 纵向<br/>cascade_check 沿依赖图追下游<br/>定位真凶"]:::step
    S5["Step 5 多向交叉<br/>trace + log + metric +<br/>代码 + 数据 5 维度按问题类型选"]:::step
    S6["Step 6 故障快报 + confidence 评级<br/>P0 命令(PRE / EXEC / POST 三段)<br/>工程师确认后执行"]:::step
    S7["Step 7 沉淀(confidence=high 强制)<br/>sink_postmortem.py 自动 append<br/>→ known-errors.local.yaml"]:::loop

    Start --> S1
    S1 -->|命中| Fast
    S1 -->|不命中| S2 --> S3 --> S4 --> S5 --> S6 --> S7
    S7 -.闭环:下次同症状.-> S1
```

### 流程图 3:实战时序图(以"prod commerce 5xx 突增"为例,展示机器人内部工作过程)

```mermaid
sequenceDiagram
    autonumber
    participant Eng as 工程师
    participant Bot as 排障机器人
    participant Route as routing skill<br/>(12 张映射表)
    participant RC as recent-changes<br/>(timeline.py)
    participant II as incident-investigator
    participant K as known-errors.local.yaml
    participant MCPg as grafana MCP
    participant MCPj as jaeger MCP
    participant MCPm as mongo MCP
    participant K8s as k8s MCP

    Eng->>Bot: prod commerce 5xx 突增,14:23 开始

    Note over Bot,Route: ── Step 1 收前提 + 历史 grep ──
    Bot->>Route: Read routing/references/env-domain-map.yaml<br/>+ service-dependency-map.yaml
    Route-->>Bot: prod commerce 域名 / 上下游服务 / log app 名
    Bot->>K: grep "5xx.*timeout|context deadline"<br/>known-errors.{yaml,local.yaml}
    K-->>Bot: 无命中 → 走完整流程

    Note over Bot,RC: ── Step 2 timeline 三路合并 ──
    Bot->>RC: python3 timeline.py --env prod --service commerce<br/>--since 1h --incident-time "14:23"
    RC->>RC: 并发拉:<br/>git log + k8s rollout + nacos history
    RC-->>Bot: events[] 含 1 条 correlated nacos U<br/>+ diff_risks: timeout_decreased severity:high

    Note over Bot,MCPg: ── Step 3 横向 验证孤立/广播 ──
    Bot->>MCPg: query_prometheus 同 namespace 其它 service 5xx
    MCPg-->>Bot: 仅 commerce 错误率高 → 孤立故障

    Note over Bot,II: ── Step 4 纵向 cascade_check 下游 ──
    Bot->>II: python3 cascade_check.py commerce
    II->>Route: 读 service-dependency-map: commerce → user
    II->>MCPg: query_loki_logs user 服务最近 5min
    MCPg-->>II: user 服务 timeout 暴涨,跟 commerce 同步
    II-->>Bot: 凶手在 commerce → user 调用环节

    Note over Bot,K8s: ── Step 5 多向交叉(取证关键证据)──
    Bot->>K8s: get_pod_logs commerce 最近 10min
    K8s-->>Bot: 错误栈:"context deadline exceeded calling user"
    Bot->>MCPj: search_traces service=commerce error=true
    MCPj-->>Bot: trace 显示 user 调用 3.2s timeout

    Note over Bot,Eng: ── Step 6 故障快报 ──
    Bot-->>Eng: 故障快报:confidence=high<br/>根因: nacos downstream.user.timeout 从 30s 改 3s<br/>P0: nacos 后台回滚到上一版本<br/>预计恢复: 1-2 分钟
    Eng->>Eng: nacos UI 点回滚
    Eng->>Bot: 已回滚,等待验证

    Note over Bot,K: ── Step 7 沉淀(confidence=high 强制)──
    Bot->>Bot: 抽象成 pattern:<br/>"downstream\\.\\w+\\.timeout.*[1-3]s"
    Bot->>K: python3 sink_postmortem.py --input pattern.json
    K-->>Bot: ✓ append 完成,下次同症状直接 grep 命中
```

**这张图想说的事**:机器人不是"把所有 MCP 工具丢给 LLM 自由发挥",而是有**结构化的取证顺序**:
1. 先查映射表(routing,毫秒级答出"这是谁的服务、log app 是什么、依赖谁")
2. 再扫历史(timeline.py 三路合并 + 17 类 risk 自动分类,给定性结论)
3. 然后横向验证(指标 / 日志)→ 纵向追下游
4. 取证(trace + 完整错误栈)
5. 最后才输出快报 + 沉淀

每一步都是**确定性的脚本/MCP 调用**驱动 LLM 决策,而不是反过来。这是它能 L3 自主完成排障 + 跨工程师水平稳定输出的核心。

---

**附:Mermaid 图使用说明**

三张图既可作 markdown 流程图用,也可在 GitLab merge request / 飞书文档 / Confluence wiki 里直接嵌入。导出图片时建议白底,字体大些(`mermaid.live` 的 "Actions → PNG/SVG → ⚙ Background color: white" 即可)。

3. 主要使用的技术/工具/平台:

   后端 / CLI:Go 1.25(单二进制,跨 macOS/Linux/Windows × amd64/arm64)、
              Wails v2(macOS 桌面 app)、go:embed(模板嵌入二进制)

   前端:Vue 3 + Vite + vue-tsc(类型检查)+ vitest(单测 12 文件 133 用例)+ Pinia

   AI 协议 / 平台:MCP(Model Context Protocol,Anthropic)、Claude Code / Cursor / Codex CLI / OpenClaw

   13 种 MCP 接入:@elastic/mcp-server-elasticsearch、mcp-mongo-server、
                @modelcontextprotocol/server-postgres、mcp-grafana-npx(grafana+loki+prom+tempo)、
                uvx nacos-mcp-router、uvx opentelemetry-mcp、uvx mcp-clickhouse、
                @gongrzhe/server-redis-mcp、@benborla29/mcp-server-mysql、
                @larksuiteoapi/lark-mcp、@lark-project/mcp

   监控 / 配置中心 / 数据层:
     可观测性 — Grafana / Prometheus / Loki / Jaeger / Tempo / ELK / SkyWalking / Kuboard
     配置源   — Nacos / Apollo / Consul / Kubernetes ConfigMap / 纯环境变量
     数据层   — Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse

   工程化:GitLab CI/CD(自动 changelog + release notes)、golangci-lint v2.0.2、Go race + coverage

4. 预期适用范围(请具体到小组、部门或公司场景):

   部门维度(全部门可用):
     ✅ 娱乐部(开发部门)— 已在 prod/test/dev 三环境生产验证
     ✅ 其它后端研发部门 — yaml 配一份即用,无业务耦合
     ✅ 测试 / QA — 排查测试环境问题、回归故障复现
     ✅ 运维 / SRE — 一线值班接告警后第一时间排查工具
     ⚠ 数据 / 算法 — 部分适用(数据层 mcp 可查 mongo/pg/es/ck,模型训练故障不覆盖)
     ❌ 产品 / 设计 — 不适用(工具面向技术问题排查)

   技术栈维度(各栈代码扫描识别精度):
     Go      — 服务名 70-80% / GORM ORM 90%+ / gRPC + HTTP client 高识别率
     Java    — 服务名 60-70% / JPA + MyBatis 80% / @FeignClient 高识别率
     Python  — 服务名 60%    / SQLAlchemy 70%   / requests + httpx 中等
     Node    — 服务名 50%    / TypeORM + Mongoose 60% / axios + fetch 中等
     PHP     — 服务名 50%    / Eloquent + Doctrine 60% / Guzzle 中等

   不适用:Serverless / FaaS、单体应用、纯前端项目

---

第三部分:提交材料清单

请确认以下材料已准备完毕,并将作为附件与本表一同提交:

☑ 附件一:《Agent设计与验证报告》(docs/agent-design-verification-report.md)
☑ 附件二:《Agent使用与部署文档》(docs/agent-deployment-guide.md)
☑ 附件三:演示方式说明
   - macOS 桌面 app 一行命令装:
     curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash
   - 源码仓库:https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio
   - 录屏链接:____________________(待填)
☑ 其他(请说明):
   v0.9.0 稳定版本已发布,GitLab Release 页面附自动生成 changelog + 多平台 binary;
   2 周生产观察期(2026-04-27 ~ 2026-05-11)实测数据见附件一

---

开发者承诺:

本人承诺本申请表及附件内容真实、准确、完整,并同意所提交的Agent进入公司评审与公示流程。

主要开发者签字:__________     日期:2026 年 5 月 11 日

---

部门GM初审意见:

□ 材料齐全,同意提交。
□ 建议修改后提交,具体意见:____________________
□ 不予提交,原因:____________________

部门GM签字:__________     日期:_______年_______月_______日
