# 《Agent 创新、提效提报申请表》

**提报部门**:娱乐部
**提报日期**:2026 年 5 月 11 日
**申请编号**:AGENT-20260511-AI排障机器人工作台

---

## 第一部分:申请人及 Agent 信息

| 字段 | 内容 |
|---|---|
| **1. Agent 名称** | AI 排障机器人工作台(troubleshooter-studio) |
| **2. 主要开发者** | xiaolong |
| **3. 协作开发者** | _(待填:姓名/工号 + 分工比例)_ |
| **4. 主要开发者 TG** | 13666015490 |
| **5. 预期申请等级** | ☑ **S 级(全公司通用)** ── 跨技术栈、跨配置中心、跨 AI 平台、产品逻辑零业务耦合,任何研发团队 yaml 配一份即可启用 |
| **6. 人均提效价值预估** | 见下方详述 |
| **7. 预期 Skill 等级** | **L3**(能完整自主完成"症状→根因→处置建议"端到端排障任务,人介入只在 P0 命令执行前确认) |
| **8. 组内成员及提效贡献比例** | _(待填,5-10 名)_ |

### 6. 人均提效价值预估(详)

| 维度 | 改进前 | 改进后 | 提升幅度 |
|---|---|---|---|
| **MTTR(平均故障恢复时间)** | 30-50 分钟(手动翻 Grafana / SSH 到 pod / grep 日志 / 翻 PR) | 5-10 分钟(描述症状 → 拿到结构化时间线 + 嫌疑变更 + 处置建议) | **↓ 55%** |
| **故障复盘工时** | 每次 1-2 人小时手工汇总监控截图 + 日志摘录 + 时间线 | 故障快报自动产出含 confidence + diff_risks + 处置链 + 沉淀草稿 | **↓ 70% 重复劳动** |
| **新人排障胜任度** | 需要 6 个月经验才能独立处理生产 5xx | 入职 2 周内可独立处理 80% 常见故障(机器人引导走 6 步流程) | **可独立处理常见故障比例 ↑ 80%** |
| **历史经验复用率** | 故障复盘文档归档后没人查 | sink_postmortem.py 自动沉淀到 known-errors.local.yaml,Step 1.3 grep 优先命中 → 复发故障跳过 6 步直走 next_actions | **复发故障识别率 ≈ 90%** |

#### 量化样本(以娱乐部 prod 一次真实事件估算)
- 某次 user 服务 5xx 突增:工程师按传统流程 35 分钟定位到"nacos 改 downstream.user.timeout=3s",回滚解决
- 用本工作台同样事件:`recent-changes` 自动标 timeout_decreased risk,**3-5 分钟内**给出"回滚 nacos 配置"建议,加上人工确认/执行约 8 分钟
- **单次事件节省 ~25 分钟** × 每月人均 4 起类似事件 ≈ 月省 100 分钟/人

---

## 第二部分:Agent 详情说明

### 1. 核心解决的问题或需求

#### 业务痛点
1. **技术栈孤岛**:公司各部门监控标准不一,跨部门调用链路(gRPC/HTTP)在出现排障时,数据对齐极度困难
2. **排障经验难以沉淀**:高级工程师的排障思路无法标准化,每次故障在"重复造轮子"找原因;新人接手需手把手带 3-6 个月
3. **大模型落地难**:纯通用大模型不理解公司内部监控指标(Metrics)、配置中心数据结构、业务依赖图,无法直接落地实操
4. **多 AI 平台割裂**:Claude Code / Cursor / Codex CLI / OpenClaw 各 IDE 接入方式不同,同一团队不同人用不同平台,无法共享排障能力

#### 本工作台的解法
- **yaml 配置驱动**:`troubleshooter.yaml` 描述微服务系统(环境/仓库/服务/配置中心/可观测性/数据层),工具链一键产出"装到 4 个 AI 平台开箱即用"的排障机器人
- **结构化路由**:routing skill 12 张映射表(env→分支/域名/配置/日志 app/MCP 名/依赖图/数据库 schema/known-errors),静态查表毫秒返回,不靠 LLM 猜
- **流程固化**:incident-investigator 6 步主线(症状→时间轴→横向→纵向→三向交叉→根因)+ Step 7 沉淀,任何排障问题先走流程不直跳工具
- **闭环沉淀**:故障复盘自动产出 known-errors pattern,跨 `tshoot upgrade` 保留,下次同类故障 Step 1.3 grep 即命中跳过 6 步流程
- **MCP 全栈接入**:13 种 MCP × N 个环境,自动派生注册到对应 IDE 配置

---

### 2. 核心功能与运作逻辑

#### 2.1 两层架构

```
┌──────────────────────────────────────────────────────────────────┐
│  上层:研制环境(troubleshooter-studio)                          │
│  ┌────────┐  ┌─────────┐  ┌────────┐  ┌────────┐  ┌──────────┐ │
│  │ wizard │→ │analyzer │→ │  gen   │→ │install │→ │ discover │ │
│  │ 10 步  │  │ 5 栈扫描│  │渲产物  │  │4 平台  │  │扫已装agent│ │
│  └────────┘  └─────────┘  └────────┘  └────────┘  └──────────┘ │
│                          ↓                                       │
│                  troubleshooter.yaml                             │
└──────────────────────────────────────────────────────────────────┘
                          ↓ tshoot install
┌──────────────────────────────────────────────────────────────────┐
│  下层:产出物 —— 排障机器人(脱离 studio 独立运行)              │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │  skill 集合(按 yaml 动态裁剪,19 个 skill 候选)          │ │
│  │  routing / incident-investigator / recent-changes        │ │
│  │  + config-executor(nacos/apollo/consul/k8s ConfigMap)    │ │
│  │  + 9 个数据层 runtime-query(mongo/pg/redis/...)         │ │
│  │  + 5 个 obs query(jaeger/elk/tempo/skywalking/k8s)       │ │
│  ├────────────────────────────────────────────────────────────┤ │
│  │  13 种 MCP × N env(自动注册到对应 IDE 配置文件)         │ │
│  │  grafana(prom+loki+tempo)/ jaeger / elasticsearch        │ │
│  │  / elk / mongo / postgres / redis / mysql / clickhouse   │ │
│  │  / nacos / lark / feishu_project                         │ │
│  ├────────────────────────────────────────────────────────────┤ │
│  │  4 个 AI 平台部署位置:                                    │ │
│  │  Claude Code / Cursor / Codex CLI / OpenClaw             │ │
│  └────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

#### 2.2 用户视角的排障流程

```
工程师:"prod commerce 5xx 突增"
   ↓
[Step 1] 收齐前提 + 历史 grep
   - 自动读 known-errors.{yaml,local.yaml} 找已知 pattern
   - 命中 → 直走 next_actions,跳过 2-6 步
   ↓
[Step 2] 时间轴对齐(最高 ROI)
   - timeline.py 拉 git log + K8s rollout + nacos history 三合一
   - 标 ±5min "强相关变更",自动扫 12 类配置 risk + 5 类代码 risk
   - 输出:1 条 nacos U 含 risk: timeout_decreased severity: high
   ↓
[Step 3] 横向:同 env 别 service 是否也炸 → 孤立 vs 广播
[Step 4] 纵向:沿依赖图追下游 cascade_check
[Step 5] 多向交叉:trace + log + metric + 代码 + 数据
[Step 6] 根因 + 处置 + 故障快报(P0 命令带 PRE/EXEC/POST)
[Step 7] confidence=high → 自动 sink_postmortem.py 沉淀 pattern
   ↓
下次同症状 Step 1.3 直接命中,跳过 2-6 步
```

#### 2.3 关键自动化

- **变更归因自动化**:recent-changes 同时扫**配置 diff 12 类危险模式**(timeout↓/replicas↓/database_endpoint_changed 等)+ **代码 diff 5 类**(retry 装饰器删除/熔断删/缓存注解删/SQL 通配前缀添加/async→sync)
- **故障经验沉淀闭环**:每次 confidence=high 排障完,LLM 抽象成 `pattern` regex 追加到 `known-errors.local.yaml`,跨升级保留,机器人"越用越懂"
- **跨平台部署**:`tshoot gen` 一次产出,`tshoot install --target X` 装到 4 个平台,凭据 0o600 不泄漏
- **运维自动化**:`tshoot doctor` 8 类漂移检测;`tshoot upgrade` 备份 + 重 gen + diff;`tshoot apply` 原地更新已装机器人

---

### 3. 主要使用的技术/工具/平台

#### 后端 / CLI
- **Go 1.25**(单二进制,跨平台:macOS/Linux/Windows × amd64/arm64)
- **Wails v2**(macOS 桌面 app,WebView2/WKWebView 原生窗口)
- **`//go:embed`** 把模板 + 示例打进二进制,无外部依赖

#### 前端(桌面 app + Web UI)
- **Vue 3 + Vite**
- **vue-tsc**(类型检查)+ **vitest**(单测)
- **Pinia / VueUse**

#### MCP 协议 / AI 平台
- **MCP(Model Context Protocol)** —— Anthropic 提出的 LLM 工具调用协议
- **Claude Code / Cursor / Codex CLI / OpenClaw** —— 4 个 AI 平台,各家有独立 agent 定义格式(`.md` / `.toml` / `mcp.json` / `openclaw.json`)
- **13 种 MCP**:`@elastic/mcp-server-elasticsearch` / `mcp-mongo-server` / `@modelcontextprotocol/server-postgres` / `mcp-grafana-npx`(grafana/loki/prom/tempo 统一)/ `uvx nacos-mcp-router` / `uvx opentelemetry-mcp` / `uvx mcp-clickhouse` / `@gongrzhe/server-redis-mcp` / `@benborla29/mcp-server-mysql` / `@larksuiteoapi/lark-mcp` / `@lark-project/mcp`

#### 监控 / 配置中心 / 数据层接入
- **可观测性**:Grafana / Prometheus / Loki / Jaeger / Tempo / ELK / SkyWalking / Kuboard k8s 运行时
- **配置源**:Nacos / Apollo / Consul / Kubernetes ConfigMap / 纯环境变量
- **数据层**(只读):Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse

#### 工程化 / DevOps
- **GitLab CI/CD**(test stage 并行 go:lint/go:test/web + manual release 三 button)
- **scripts/changelog.sh + scripts/release.sh** 自动生成 release notes + 版本号管理(强制 CI 触发,本地禁)
- **golangci-lint v2.0.2 / vitest 12 文件 133 用例 / Go 全包覆盖率监控**

---

### 4. 预期适用范围

#### 部门维度
| 部门 | 适用度 | 备注 |
|---|---|---|
| 娱乐部(开发部门) | ✅ 完全适用 | 已在生产 prod/test/dev 三环境部署验证 |
| 其它后端研发部门 | ✅ 完全适用 | yaml 配一份即可,无业务耦合 |
| 测试 / QA | ✅ 适用 | 排查测试环境问题、回归故障复现 |
| 运维 / SRE | ✅ 适用 | 一线值班接告警后第一时间排查工具 |
| 数据 / 算法 | ⚠ 部分适用 | 数据层 mcp 可查 mongo/pg/es/ck,但模型训练故障场景不覆盖 |
| 产品 / 设计 | ❌ 不适用 | 工具面向技术问题排查,非产品流程问题 |

#### 技术栈维度(代码扫描识别精度)
| 技术栈 | 服务名识别 | 数据 schema 识别 | 依赖图识别 |
|---|---|---|---|
| Go | 70-80% | GORM 主流 ORM 90%+ | gRPC + HTTP client 高识别率 |
| Java | 60-70% | JPA / MyBatis 主流 80% | @FeignClient 高识别率 |
| Python | 60% | SQLAlchemy 70% | requests / httpx 中等 |
| Node | 50% | TypeORM / Mongoose 60% | axios / fetch 中等 |
| PHP | 50% | Eloquent / Doctrine 60% | Guzzle 中等 |

**不适用**:Serverless / FaaS、单体应用、纯前端项目

---

## 第三部分:提交材料清单

- [x] 附件一:《Agent 设计与验证报告》(见下方)
- [x] 附件二:《Agent 使用与部署文档》(见下方)
- [x] 附件三:演示方式说明 —— **桌面 app(macOS) + CLI(全平台)**
  - 一行命令装(macOS):`curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash`
  - 源码 + 文档:`https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio`
- [x] 其他:已有 v0.9.0 稳定版本发布,内含 6 个 commits 的 changelog

---

## 开发者承诺
本人承诺本申请表及附件内容真实、准确、完整,并同意所提交的 Agent 进入公司评审与公示流程。

**主要开发者签字**:__________    **日期**:2026 年 5 月 11 日

---

# 附件一:《Agent 设计与验证报告》

**Agent 名称**:AI 排障机器人工作台(troubleshooter-studio)
**报告日期**:2026 年 5 月 11 日
**对应申请编号**:AGENT-20260511-AI排障机器人工作台

---

## 一、设计概述

### 1. 系统架构图 / 核心工作流

完整架构图见仓库 `assets/architecture.svg`(可直接打开 GitLab 上的源码查看)。

**核心工作流**:
```
研制期(开发部门一次性配置)
   ↓
troubleshooter.yaml(系统建模:环境 / 仓库 / 服务 / 配置中心 / 可观测性 / 数据层)
   ↓
tshoot validate / analyze / gen → staging 产物
   ↓
tshoot install --target {openclaw|claude-code|cursor|codex} → 装到本机
   ↓
排障期(工程师日常使用)
   ↓
工程师描述症状 → 机器人走 7 步流程 → 结构化故障快报 + 沉淀
```

### 2. 核心模块 / 功能说明

| 模块 | 功能 |
|---|---|
| **`internal/config/`** | troubleshooter.yaml schema + 加载校验(8 类 health check) |
| **`internal/analyzer/`** | 5 栈代码扫描(Go/Java/Python/Node/PHP)× 6 配置源识别(Nacos/Apollo/Consul/k8s/env-vars/static) |
| **`internal/generator/`** | 模板渲染 + IDE 三家 agent 原生 prompt(claude-code/cursor/codex)+ codex toml region marker 幂等替换 |
| **`internal/agent/`** | 原生 Go install/self-test/uninstall + 4 平台共享的 MCP 派生(`BuildMCPServers`)+ 凭据写入 |
| **`internal/doctor/`** | 8 类漂移检测 + --fix 行级精确替换 |
| **`internal/upgrade/`** | 备份 + 重 gen + diff |
| **`internal/discover/`** | 扫 tshoot.json 锚点识别本机已装机器人 |
| **`internal/cchub/`** | 配置中心客户端 hub(nacos/apollo/consul + 连接池缓存) |
| **`internal/dsprobe/`** | 数据层连通性测试(redis/mongo/es/mysql/pg/kafka/mq/clickhouse) |
| **`cmd/tshoot-desktop/`** | Wails v2 桌面 app(WebView + Vue 前端 + Go binding) |
| **`templates/workspace/skills/`** | 19 个 skill 模板(按 yaml 动态裁剪) |
| **`templates/workspace/skills/incident-investigator/scripts/sink_postmortem.py`** | 自动沉淀 known-errors.local.yaml |
| **`templates/workspace/skills/recent-changes/scripts/timeline.py`** | 三路 history 合并 + 17 类危险模式分类 |

---

## 二、验证方法与数据

### 1. 测试环境说明

- **CI 端**:GitLab CI/CD(go test -race -cover / golangci-lint / vue-tsc / vite build / Wails .app bundle 校验)
- **测试套件**:Go 全包 100+ 单测 + e2e(install_e2e_test.go 覆盖 4 平台装机流程)+ vitest 12 文件 133 用例(前端校验逻辑)
- **生产验证**:娱乐部 prod / test / dev 三环境,2 周线上观察期(2026-04-27 ~ 2026-05-11)

### 2. 测试用例与结果(关键 5 例)

| 用例 | 输入 | 期望输出 | 实测 |
|---|---|---|---|
| **U1 配置型故障归因** | "prod commerce 5xx 突增,14:23 开始" | 自动标 ±5min 内 nacos 改 downstream.user.timeout=3s,risk: timeout_decreased severity: high,处置:回滚 nacos | ✅ 3.2 分钟出快报,confidence=high |
| **U2 代码型故障归因** | "user 服务 OOM + 5xx,刚发版" | 自动标 git commit 含 @Cacheable 删除,risk: cache_annotation_removed severity: high,处置:kubectl rollout undo | ✅ 4.1 分钟出快报 |
| **U3 历史故障复用** | 同 U1 症状再次触发 | Step 1.3 grep known-errors.local.yaml 命中 timeout_decreased pattern,直走 next_actions 跳过 6 步 | ✅ 45 秒给出"回滚 nacos 配置 X"建议 |
| **U4 跨服务级联** | "user 服务 OOM 后 commerce 也开始报 timeout" | 主动跑 cascade_check 沿依赖图追,识别 commerce 是受害者非凶手 | ✅ 自动标 commerce 为 downstream affected,根因仍指向 user 服务 |
| **U5 跨平台部署一致性** | `tshoot install --target openclaw / claude-code / cursor / codex` | 4 个平台 mcpServers/mcp.servers 同款 13 家 MCP key,凭据 0o600,行为字节级一致 | ✅ e2e 测试 4 target 全过 |

**详细测试记录**:见仓库 `internal/agent/install_native_mcp_common_test.go`(BuildMCPServers 全家桶端到端验证 + 4 target 一致性)、`internal/agent/install_e2e_test.go`(install→discover→reinstall→uninstall 全链)。

### 3. 效能对比数据(2 周观察期 2026-04-27 ~ 2026-05-11)

| 指标 | 改进前(2 周对照期) | 改进后(2 周观察期) | 提升 |
|---|---|---|---|
| 平均 MTTR(故障从报警到处置完成) | 38 分钟 | 17 分钟 | **↓ 55%** |
| 单次故障人工汇总工时 | 1.5 人小时 | 0.4 人小时 | **↓ 73%** |
| 复发故障被快速识别比例 | 30%(靠人脑记忆) | 92%(known-errors grep 命中) | **↑ 207%** |
| 新人独立完成排障 | 0% | 60%(跟着 6 步流程走) | n/a |

---

## 三、局限性及风险说明

### 1. 已知局限或边界条件

- **代码扫描精度依赖通用模式识别**:配置驱动 / 注解驱动 / 自定义包装层重的项目命中率会下降,缺漏部分需在桌面 app 编辑器手补
- **不支持 Serverless / FaaS** —— 没有 pod / 配置中心 / 仓库扫描的概念,本工作台不适配
- **告警 webhook 入口缺失**:当前 100% 靠工程师手打症状启动,实战中告警响起到工程师描述要 30s-2min 黄金时间
- **业务 KPI 接入空白**:技术故障的最终判断是"业务影响多大"(DAU/订单/支付成功率),当前未接业务侧指标
- **Codex 沙箱需手工配置**:`~/.codex/config.toml` 全局需加 `[sandbox_workspace_write] network_access = true`,install 时探测+提示但不主动改用户全局 config
- **代码 diff 危险模式 5 类是 RegExp 级别**:语义级漏识别(如复杂重构后的 retry 逻辑等价但代码不一样)

### 2. 安全性、合规性说明

- **凭据保护**:`troubleshooter.yaml` 不含明文凭据,只占位 `{{ENV_VAR}}`;真值在 install 阶段从 wizard localStorage / `--env-file` 注入到 IDE config 0o600 文件
- **MCP 全只读**:mongodb / postgres / redis / mysql 都传 `--read-only`,grafana/loki 用 `--disable-write/admin/alerting` 屏蔽写操作,ES MCP 包默认 RO
- **Codex 沙箱**:agent toml `sandbox_mode=workspace-write` 限制 agent 只能动当前工作区文件,无法触碰系统其它位置
- **凭据不上传**:`tshoot install` 全本机操作,凭据 0o600 文件不通过任何网络服务
- **审计 trail**:所有 release 走 GitLab CI manual button,版本号决策有 audit trail(谁点的/什么时间/哪个 commit),git history 干净统一

**报告撰写人**:xiaolong
**审核人(如有)**:__________

---

# 附件二:《Agent 使用与部署文档》

**Agent 名称**:AI 排障机器人工作台(troubleshooter-studio)
**文档版本**:V1.0
**最后更新日期**:2026 年 5 月 11 日

---

## 一、快速开始

### 1. 前提条件

#### 所需账号 / 权限
- GitLab 项目 read 权限(下载 release 用)
- 公司各监控系统的只读账号:Grafana / Nacos / Jaeger / ELK / 数据库
- (可选)Apple Developer Account 或 GitLab Project Access Token(发布场景才需要)

#### 运行环境要求
| 平台 | 桌面 app | CLI | AI 平台 IDE |
|---|---|---|---|
| macOS 14+ | ✅ .dmg / .app | ✅ | ✅ Claude Code / Cursor / Codex / OpenClaw |
| Linux | ❌ | ✅ | ✅ Claude Code / Cursor / Codex / OpenClaw |
| Windows | ❌ | ✅ | ✅ Claude Code / Cursor |

依赖软件:
- **Node.js 20+** + npm(npx 启动 MCP 用)
- **uv**(Python 工具链,nacos / jaeger / clickhouse MCP 用,`brew install uv`)
- **AI 平台 IDE**(Claude Code / Cursor / Codex CLI 任选 1+,或 OpenClaw)

### 2. 部署步骤

#### 步骤 1:装 troubleshooter-studio

**桌面 app(推荐 macOS)**:
```bash
# 公开项目
curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

# 私有项目(需 GITLAB_TOKEN)
export GITLAB_TOKEN=glpat-xxx
curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash
```

**CLI(macOS / Linux / Windows)**:
从 GitLab Release 下 `tshoot-vX.Y.Z-<os>-<arch>`,`chmod +x` 后拷到 `/usr/local/bin/tshoot`。

#### 步骤 2:配置目标系统(创建向导)

打开桌面 app → 创建向导 → 跟着 10 步问答填:
1. 系统 ID / 名称
2. 部署目标(勾选要装到哪个/哪些 AI 平台)
3. 环境(dev/test/prod 等)
4. 仓库(本地或远程 git URL)
5. 服务列表(可扫代码自动反推)
6. 配置中心(Nacos / Apollo / Consul / k8s / 纯 env)
7. 数据层(MongoDB / Redis / MySQL / ...)
8. 可观测性(Grafana + Prometheus + Loki + Jaeger + ELK)
9. 消息 / 项目管理(可选,飞书 IM / 飞书项目)
10. 一键部署 → `troubleshooter.yaml` 落盘 + 自动装到目标 AI 平台

完成后桌面 app "已装机器人"页面看到新机器人。

#### 步骤 3:启动 AI 平台对应入口

| 平台 | 启动方式 |
|---|---|
| **Claude Code** | 任意项目目录 `claude` → `@<机器人名>` 调 subagent |
| **Cursor** | AI 侧栏 → 选 Custom Agent → 启用 MCP(Settings → MCP) |
| **Codex CLI** | `codex` → 主 chat 说 "spawn the `<机器人名>` agent ..." |
| **OpenClaw** | 启动 OpenClaw 客户端 → agent 列表选(**装完必须重启 OpenClaw 才出现**) |

---

## 二、使用指南

### 1. 如何启动 / 调用 Agent

#### 排障场景(主路径)

跟机器人对话,描述症状即可:
```
工程师:prod commerce 5xx 突增,14:23 开始

机器人:
[ctx: prod / commerce / Step 1 收前提]
我先确认 ±5 分钟内的变更...

[Step 2 timeline] 14:18 nacos U:
   - downstream.user.timeout: 30s → 3s
   - risk: timeout_decreased severity: high

[Step 6 故障快报] confidence: high
   根因:user 服务调用超时从 30s 改 3s,下游 p99 > 新阈值 → 全 timeout 雪崩
   P0 处置:nacos 回滚配置到上一版本
   预计恢复时间:1-2 分钟

[Step 7 沉淀] 已 sink 到 known-errors.local.yaml
   下次同类故障会自动命中
```

#### 输入 / 输出格式

**输入格式 / 要求**:
- 自然语言描述症状(中文 / 英文都支持)
- 可选:故障开始时间、影响的服务、错误日志片段、trace_id
- 不必给具体 metric 数值或截图(机器人会自己拉)

**输出格式样例**:
- 结构化故障快报(`[ctx: <env>/<service>/<阶段>]` 进度条 + 时间轴 + 根因 + 处置建议 + confidence)
- P0 命令带 PRE/EXEC/POST 三段(查现状 → 执行 → 验证)
- `confidence=high` 自动尾部带"已 sink 到 known-errors.local.yaml"

### 2. 配置项说明

详细 yaml schema 见 `schema/troubleshooter.schema.yaml`,核心字段:
- `system.id / name` — 系统标识
- `agent.name / target_models` — 机器人名称 + 各 AI 平台的模型选择
- `generation.targets` — 勾选要部署的 AI 平台(可多选)
- `environments[]` — 环境列表(每个含 id / api_domain / is_prod)
- `repos[]` — 仓库列表(本地 / 远程,支持 monorepo + umbrella)
- `infrastructure.config_centers` — 配置中心(可多源)
- `infrastructure.data_stores[]` — 数据层(类型 + enabled + endpoints)
- `infrastructure.observability` — 可观测性组件(grafana / loki / prom / jaeger / ELK ...)
- `generation.skills_whitelist`(可选)— 二次过滤(已启用基础上再剔)

---

## 三、故障排除

### 1. 常见问题与解决方案

| 现象 | 原因 | 解决 |
|---|---|---|
| macOS 双击 .app 报"已损坏" | 应用未做苹果数字签名,Gatekeeper 拦 | 双击 dmg 里附的 `2️⃣ 双击解锁.command`,或命令行 `xattr -d com.apple.quarantine /Applications/TroubleshooterStudio.app` |
| Claude Code 看不到刚装的 agent | Claude Code 没读到 `~/.claude.json` | 重启 Claude Code(命令行 `claude` 重启,GUI 退出再开) |
| OpenClaw 找不到机器人 | OpenClaw 启动时一次性加载 agents 列表 | **重启 OpenClaw 客户端** |
| Codex 启动后 MCP 全部 ENOTFOUND | Codex 沙箱默认禁网 | `~/.codex/config.toml` 加 `[sandbox_workspace_write] network_access = true`(install 时探测+提示,按提示改) |
| nacos / jaeger / clickhouse MCP 报 spawn uvx ENOENT | 没装 uv | `brew install uv`(macOS)或 `curl -LsSf https://astral.sh/uv/install.sh | sh` |
| lark MCP 启动失败 npm 404 | 老版本(v0.5.0 前)包名错 | 升级到 v0.9.0+ 已修(`@larksuiteoapi/lark-mcp` 正确包名) |
| 桌面 app 部署时 mongo/redis 凭据 30s timeout | 网络不可达(VPC/防火墙) | `nc -vz <host> <port>` 验证;问题不在 MCP 配置 |

### 2. 联系人与支持

| 渠道 | 联系方式 |
|---|---|
| **技术咨询** | xiaolong(TG: 13666015490) |
| **反馈邮箱** | _(待填:团队邮箱 / 飞书群)_ |
| **issue 跟踪** | GitLab Issues:https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/issues |
| **文档** | 仓库 README + `docs/CI-RELEASE.md`(发布流程) |

**文档维护人**:xiaolong

---

## 附:使用日志

(部署 + 实战日志样本,可在 GitLab Issues 提交时附 `~/.openclaw/<id>-creds.json` 之外的所有日志文件)

- **install 日志**:`tshoot install --target openclaw --path dist/<id>` 终端输出
- **gen 日志**:`tshoot gen -i troubleshooter.yaml -o ./out` 终端输出
- **self-test 日志**:`tshoot self-test --path dist/<id>`(openclaw 自检 nacos/grafana TCP+HTTP 探活)
- **discover 日志**:`tshoot discover --format json`(扫本机已装机器人)
- **机器人排障对话日志**:Claude Code / Cursor 自带 history,Codex CLI `~/.codex/sessions/`,OpenClaw 客户端 history
- **沉淀的 known-errors.local.yaml**:见 `~/.openclaw/workspace/<name>/skills/routing/references/known-errors.local.yaml`(或对应 IDE 的 skills 目录)

---

**当前版本**:v0.9.0(2026-05-09 发布,GitLab Release 自动生成 changelog 含 6 条改动)
**代码仓**:`https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio`
**License**:_(待填:内部 / 开源协议)_
