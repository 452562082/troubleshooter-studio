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

   ── 流程图 1:两层架构 + 4 平台部署 ─────────────────────────────────────

   【上层:troubleshooter-studio(研制工作台,部门管理员一次性配置)】

      ① wizard(10 步问答)      → 产出 troubleshooter.yaml(系统建模)
      ② analyzer(5 栈代码扫描)  → 产出 service / 依赖 / schema 反推
      ③ gen(模板渲染)           → 产出 staging(templates/workspace-template/)
      ④ install --target X       → 装到 4 个 AI 平台之一或多个

                                   ↓ install --target

   【下层:排障机器人(部署到 AI 平台,脱离 studio 独立运行)】

   4 个 AI 平台部署位置对照:

   | 平台         | 主目录                              | MCP 配置位置                        |
   | ------------ | ----------------------------------- | ----------------------------------- |
   | OpenClaw     | ~/.openclaw/workspace/<name>/       | ~/.openclaw/openclaw.json 的 mcp.servers |
   | Claude Code  | ~/.claude/agents/<name>.md          | ~/.claude.json 的 mcpServers        |
   | Cursor       | ~/.cursor/agents/<name>.md          | ~/.cursor/mcp.json 的 mcpServers    |
   | Codex CLI    | ~/.codex/agents/<name>.toml         | TOML 内嵌 [mcp_servers.*] 段        |

   4 个平台共享同一份 skill + MCP 配置内容,只是写入各家原生格式。

   下层组件:

   - **skill 集合**(按 yaml 动态裁剪,19 个候选)
     - routing —— 12 张映射表:env → 分支/域名/配置/log/MCP/依赖等
     - incident-investigator —— 7 步主流程,含 Step 7 沉淀闭环
     - recent-changes —— 三路合并(git + k8s + 配置)+ 17 类危险模式自动分类
     - config-executor —— nacos / apollo / consul / k8s ConfigMap
     - 9 个数据层 runtime-query —— mongo / pg / redis / mysql / es / kafka / mq / clickhouse
     - 5 个 obs query —— jaeger / elk / tempo / skywalking / k8s
     - diagram-generator —— Mermaid → PNG/SVG

   - **13 种 MCP × N 个 env**(自动注册到对应平台配置文件)
     - 可观测:grafana / loki / prometheus / tempo(统一 mcp-grafana-npx)、jaeger、elasticsearch、elk
     - 数据库:mongodb / postgresql / redis / mysql / clickhouse
     - 其他:nacos / lark / feishu_project

   ── 流程图 2:排障 7 步主流程 + 经验沉淀闭环 ────────────────────────

   入口:工程师描述症状(env + service + 时间窗 + 关键词)

      ↓

   **Step 1.3** —— grep 历史 known-errors
   - 模板内置:known-errors.yaml
   - 本团队沉淀(跨升级保留):known-errors.local.yaml
   - 是否有 pattern 命中?

   - 分支 A —— **命中** → 【快路径】直走 next_actions,跳过 Step 2-6,⏱ 30s-2min 出处置建议(本流程结束)
   - 分支 B —— **不命中** → 进入完整 6 步流程,继续往下 ↓

   **Step 2** —— timeline 三路合并
   - git log 最近 commits
   - K8s rollout history
   - nacos / 配置中心 history
   - 自动标 ±5min 内 17 类 risk(配置 12 类 + 代码 5 类)

      ↓

   **Step 3** —— 横向(同 env 别 service:孤立故障 vs 广播故障?)

      ↓

   **Step 4** —— 纵向(cascade_check 沿依赖图追下游,定位真凶)

      ↓

   **Step 5** —— 多向交叉(trace + log + metric + 代码 + 数据 5 维度按问题类型选)

      ↓

   **Step 6** —— 故障快报 + confidence 评级
   - P0 命令带 PRE / EXEC / POST 三段
   - 工程师确认后执行

      ↓

   **Step 7** —— 沉淀(confidence=high 强制)
   - sink_postmortem.py 自动 append 到 known-errors.local.yaml
   - 内容:pattern + typical_cause + next_actions + mitigation

      ↓

   **闭环**:下次同症状 → 回到 Step 1.3 grep → 直接命中 → 走快路径
   (机器人越用越懂,本团队特有故障模式逐月沉淀)

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
