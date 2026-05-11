《Agent使用与部署文档》

Agent名称: AI 排障机器人工作台(troubleshooter-studio)
文档版本: V1.0
最后更新日期: 2026 年 5 月 11 日

---

一、快速开始

1. 前提条件:

   所需账号 / 权限:
   - GitLab 项目 read 权限(下载 release 用)
   - 公司各监控系统的只读账号:Grafana / Nacos / Jaeger / ELK / 数据库
   - (可选)Apple Developer Account 或 GitLab Project Access Token(发布场景才需要)

   运行环境要求:
   - macOS 14+:桌面 app(.dmg / .app)+ CLI + 全部 4 个 AI 平台 IDE 都支持
   - Linux:只 CLI + 4 个 AI 平台 IDE
   - Windows:只 CLI + Claude Code / Cursor(Codex CLI 和 OpenClaw 当前 Win 支持不完整)

   依赖软件:
   - Node.js 20+ + npm(MCP 启动需 npx)
   - uv(Python 工具链,nacos/jaeger/clickhouse MCP 需,brew install uv 或 curl 一键装)
   - AI 平台 IDE(Claude Code / Cursor / Codex CLI 任选 1+,或 OpenClaw)

2. 部署步骤:

   步骤 1:装 troubleshooter-studio
   ──────────────────────────────────────────
   macOS 一行命令(推荐,无 Gatekeeper 弹窗):

     # 公开项目
     curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

     # 私有项目
     export GITLAB_TOKEN=glpat-xxx
     curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
       https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

   自动下最新 release dmg → 装到 /Applications/ → 启动。

   或图形装 dmg:从 GitLab Release 下载 TroubleshooterStudio-vX.Y.Z.dmg.zip,双击解压 → 双击 .dmg →
   拖 .app 到 Applications。第一次报"已损坏"双击 dmg 里的"双击解锁.command"放行。

   CLI(macOS / Linux / Windows):从 Release 下 tshoot-vX.Y.Z-<os>-<arch>,
     chmod +x 后拷到 /usr/local/bin/tshoot

   步骤 2:配置目标系统(桌面 app 创建向导)
   ──────────────────────────────────────────
   打开桌面 app → 点"创建向导"→ 跟着 10 步问答填:

     1. 系统 ID + 显示名称
     2. 部署目标(勾选要装到哪个/哪些 AI 平台 — 可多选)
     3. 环境(dev / test / prod 等,每个环境独立配置)
     4. 仓库(本地路径或远程 git URL,支持 monorepo + git submodule umbrella)
     5. 服务列表(可"代码扫描"按钮自动反推 service_names)
     6. 配置中心(Nacos / Apollo / Consul / k8s ConfigMap / 纯环境变量)
     7. 数据层(勾选启用的:MongoDB / Redis / MySQL / PostgreSQL / ES / ClickHouse 等)
     8. 可观测性(Grafana / Prometheus / Loki / Jaeger / ELK / SkyWalking / Tempo)
     9. 消息 / 项目管理(可选,飞书 IM / 飞书项目)
    10. 最后一步:一键部署 → troubleshooter.yaml 落盘 + 自动装到目标 AI 平台

   完成后桌面 app"已装机器人"页面看到新机器人卡片,显示部署位置 + 健康状态。

   步骤 3:启动 AI 平台对应入口
   ──────────────────────────────────────────
   - Claude Code:任意项目目录 `claude` → `@<机器人名>` 调 subagent
   - Cursor:AI 侧栏 → 选 Custom Agent → 启用 MCP(Settings → MCP)
   - Codex CLI:终端 `codex` → 主 chat 说 "spawn the <机器人名> agent ..."(自然语言派生 subagent)
   - OpenClaw:启动 OpenClaw 客户端 → agent 列表选(装完必须重启 OpenClaw 才出现)

---

二、使用指南

1. 如何启动 / 调用 Agent:

   排障场景(主路径)— 跟机器人对话,描述症状即可。示例对话:

   ```
   工程师:prod commerce 5xx 突增,14:23 开始

   机器人:
   [ctx: prod / commerce / Step 1 收前提]
   我先确认 ±5 分钟内的变更...

   [Step 2 timeline] 14:18 nacos U:
      - downstream.user.timeout: 30s → 3s
      - risk: timeout_decreased severity: high

   [Step 6 故障快报] confidence: high
      根因:user 服务调用超时从 30s 改 3s,
            下游 p99 > 新阈值 → 全 timeout 雪崩
      P0 处置:nacos 回滚配置到上一版本
      预计恢复:1-2 分钟

   [Step 7 沉淀] 已 sink 到 known-errors.local.yaml,
                 下次同类故障会自动命中
   ```

   输入 / 输出说明:

   输入格式 / 要求:
   - 自然语言描述症状(中文 / 英文都支持)
   - 必填:env + service + 时间窗(机器人会反问追问)
   - 可选:故障开始时间、影响的服务、错误日志片段、trace_id
   - 不必给具体 metric 数值或截图(机器人会自己拉)

   输出格式样例:
   - 结构化故障快报([ctx: <env>/<service>/<阶段>] 进度条 + 时间轴 + 根因 + 处置建议 + confidence)
   - P0 命令带 PRE/EXEC/POST 三段(查现状 → 执行 → 验证)
   - confidence=high 时自动尾部带"已 sink 到 known-errors.local.yaml"

2. 配置项说明(如有):

   详细 yaml schema 见仓库 schema/troubleshooter.schema.yaml,核心字段:

   - system.id / name              系统标识(短 id 用作 MCP key 前缀)
   - agent.name                    机器人显示名
   - agent.target_models           各 AI 平台模型选择(claude-sonnet-4 / gpt-5 / ...)
   - generation.targets            勾选要部署的 AI 平台(可多选)
   - generation.skills_whitelist   可选,二次过滤 skill(已启用基础上再剔除)
   - environments[]                环境列表(每个含 id / api_domain / is_prod)
   - repos[]                       仓库列表(本地 / 远程,支持 monorepo + umbrella)
   - infrastructure.config_centers config_centers(可多源,nacos / apollo / consul / 等)
   - infrastructure.data_stores[]  数据层(类型 + enabled + endpoints)
   - infrastructure.observability  可观测组件(grafana / loki / prom / jaeger / ELK / ...)
   - infrastructure.messaging      消息平台(lark 等)
   - infrastructure.project_tracking 项目管理(feishu_project 等)

---

三、故障排除

1. 常见问题与解决方案:

   | 现象 | 原因 | 解决 |
   | --- | --- | --- |
   | macOS 双击 .app 报"已损坏" | 应用未做苹果数字签名,Gatekeeper 拦 | 双击 dmg 里的"双击解锁.command";或命令行 `xattr -d com.apple.quarantine /Applications/TroubleshooterStudio.app` |
   | Claude Code 看不到刚装的 agent | Claude Code 没读到 ~/.claude.json | 重启 Claude Code(命令行 `claude` 重启,GUI 退出再开) |
   | OpenClaw 找不到机器人 | OpenClaw 启动时一次性加载 agent 列表 | 重启 OpenClaw 客户端(本地 daemon + GUI) |
   | Codex 启动后 MCP 全部 ENOTFOUND | Codex 沙箱默认禁网 | `~/.codex/config.toml` 加 `[sandbox_workspace_write]` + `network_access = true`(install 时探测+提示) |
   | nacos/jaeger/clickhouse MCP 报 `spawn uvx ENOENT` | 没装 uv(uvx 不在 PATH) | macOS `brew install uv`;Linux/Windows `curl -LsSf https://astral.sh/uv/install.sh \| sh`(install 时已探测+提示) |
   | 桌面 app 部署时 mongo / redis 凭据 30s timeout | 网络不可达(VPC / 防火墙 / VPN) | `nc -vz <host> <port>` 验证 TCP 通不通;跟 MCP 配置无关,网络层面排查 |
   | Grafana MCP 报 401 Unauthorized | 用户名/密码错 或 token 失效 | 桌面 app 编辑机器人 → 重填凭据 → 测试连通性按钮(Grafana 9.1+ 推荐 Service Account Token,不再用老 admin API key) |
   | Loki/Prometheus/Tempo 配置时不能选"直连" | 设计如此:本系统这 3 家通过 grafana MCP 内置工具(query_loki_logs / query_prometheus)查,无独立 MCP 包 | 必须搭 Grafana,wizard banner 已提示;勾 Grafana checkbox + 填 URL/凭据 |
   | tshoot upgrade 后沉淀的 pattern 不见了 | 沉淀应写 known-errors.local.yaml(.yaml 是模板派生会被覆盖) | 检查 sink_postmortem.py 命令是否带 `--workspace-root` 参数指向部署后的 workspace,而不是 staging 目录 |
   | GitLab pipeline 显示 "no runners online"(但 runner 列表里在线) | runner tag 跟 .gitlab-ci.yml 对不上 / runner 没勾 "Run untagged jobs" | Settings → CI/CD → Runners → ✏ 编辑 runner:Tags 加 `macos` + 勾 ☑ Run untagged jobs(详 docs/CI-RELEASE.md 步骤 4) |

2. 联系人与支持:

   技术咨询:xiaolong(TG: 13666015490)
   反馈邮箱:____________________(待填,团队邮箱 / 飞书群链接)
   Issue 跟踪:https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/issues
   文档:仓库 README + docs/CI-RELEASE.md(发布流程)
              + docs/agent-application.md(本申请系列文档)

文档维护人: xiaolong

---

附:使用日志

   日志类型                  位置
   ─────────────────────────────────────────────────────────────────────────────────────
   install 日志              tshoot install --target X --path Y 终端输出
   gen 日志                  tshoot gen -i troubleshooter.yaml -o ./out 终端输出
   self-test 日志            tshoot self-test --path dist/<id>(openclaw 自检 nacos TCP / grafana HTTP 探活)
   discover 日志             tshoot discover --format json(扫本机已装机器人)
   排障对话日志              Claude Code:自带 history;Cursor:自带;
                            Codex CLI:~/.codex/sessions/;OpenClaw:客户端 history 页
   known-errors.local.yaml   ~/.openclaw/workspace/<name>/skills/routing/references/known-errors.local.yaml
                            (对应 IDE 平台:~/.claude/skills/<name>/routing/references/known-errors.local.yaml 等)

   提报时可附以上日志的部分内容(注意 install 日志含凭据信息,过滤掉 secret 字段)。

---

当前版本: v0.9.0(2026-05-09 发布,GitLab Release 自动生成 changelog 含 6 条改动)
代码仓库: https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio
License: ____________________(待填,内部 / 开源协议)
