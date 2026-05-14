<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

**AI 排障机器人工作台** —— 用 yaml 描述微服务系统,工具链产出"装到 OpenClaw / Claude Code / Cursor / Codex CLI"开箱即用的排障机器人。

两层项目:

- **上层(此仓库)**:研制环境,做 `troubleshooter.yaml` 建模、仓库扫描、校验、生成、部署、管理
- **下层(产出物)**:完整可运行的排障机器人 —— skill 集合按 yaml 动态裁剪,固定核心 + 按配置 / 数据层 / 可观测性勾选的 runtime-query + 多环境 MCP + 标准故障话术,脱离 studio 独立运行

## 两个入口

| 入口 | 能力 | 适用 |
|---|---|---|
| **桌面 app** (Wails) | 完整(建模 / 扫描 / 部署 / 已装管理 / 工作目录浏览) | 个人用,推荐新用户 |
| **CLI** (`tshoot`) | 完整 yaml 计算 + 装机能力(4 平台齐全) | 脚本 / SSH / CI |

## 部署到 4 个 AI 平台

全程原生 Go,无 bash 依赖。`generation.targets` 勾哪些就出哪些,一次 gen 全产出。

| 平台 | 部署位置 | 进入方式 | MCP 注册位置 |
|---|---|---|---|
| **OpenClaw** | `~/.openclaw/workspace/<name>/`(整套 workspace 文件) | OpenClaw 客户端 agent 列表选(装完**重启客户端**才出现) | `~/.openclaw/openclaw.json` 的 `mcp.servers` |
| **Claude Code** | `~/.claude/agents/<name>.md` + `~/.claude/skills/<name>/` | 任意项目 `@<name>` 调 subagent | `~/.claude.json`(user-scope dotfile,Claude Code CLI 启动强绑死从这里读;`~/.claude/settings.json` 是 hooks/permissions/env 用的、不读 mcpServers) |
| **Cursor** | `~/.cursor/agents/<name>.md` + `~/.cursor/skills/<name>/` | AI 侧栏选 Custom Agent(MCP 还要去 Settings 启用) | `~/.cursor/mcp.json` 的 `mcpServers` |
| **Codex CLI** | `~/.codex/agents/<name>.toml`(TOML subagent) + `~/.codex/skills/<name>/` | 终端 `codex` 内主 chat 说 "spawn the `<name>` agent ..."(自然语言派生 subagent thread,完成后回主 chat;[官方文档](https://developers.openai.com/codex/subagents)) | 嵌入 agent toml 内联 `[mcp_servers.*]` 段(每个 subagent 自带,不污染主 chat) |

- **凭证**:`~/.openclaw/<id>-creds.json`(OpenClaw) + `~/.tshoot/<id>-creds.json`(IDE 平台,脚本读两处优先 openclaw)
- **agent 定义**:按平台原生写。Claude Code / Cursor 全塞一份 `.md`(运行环境 + 排障逻辑 + skills 索引);Codex `.toml` 瘦身配套独立 `SKILL.md`,spawn 时不烧 system prompt token
- **grafana/loki/prometheus/tempo MCP**:走 `npx -y mcp-grafana-npx`(社区 wrapper,首次跑时拉 grafana/mcp-grafana 官方 Go 二进制到 npm 缓存,跨 IDE 共享 `~/.npm/_npx/`)。这 4 家在本系统统一通过 grafana MCP 的 `query_loki_logs` / `query_prometheus` 等内置工具查询,wizard 不让独立配置(prom/loki/tempo 启用必须 grafana 启用)
- **Codex 沙箱**:agent toml 默认 `sandbox_mode=workspace-write`,但需要在 `~/.codex/config.toml` 全局加 `[sandbox_workspace_write] network_access=true`,否则 MCP 进程没出网,`tshoot install --target codex` 会探测并打印修复指引

## 下载与安装

### 桌面 app(macOS) — 一行命令(推荐,无 Gatekeeper 弹窗)

```bash
# 公开项目:
curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

# 私有项目:
export GITLAB_TOKEN=glpat-xxx
curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash
```

自动从最新 GitLab Release 下 dmg → 装到 `/Applications/` → 启动。`curl/bash/xattr/open` 是 macOS 自带签名工具,不被 Gatekeeper 拦,xattr 清完 quarantine 后 `.app` 直接放行。

### 桌面 app(macOS) — 图形装 dmg

1. 从 GitLab Release 页下 `TroubleshooterStudio-vX.Y.Z.dmg.zip`
2. 双击解压(必须用 macOS 自带 Archive Utility)
3. 双击 `.dmg` → 拖 `.app` 到 `Applications`
4. 第一次打开报"**已损坏**" → 双击 dmg 里的 `2️⃣ 双击解锁(可能要点两次).command` 一键放行
   - macOS 15+ 对未签名脚本拦一次,Terminal 显示 `killed` 再双击一次就通(无害)
   - 或命令行:`xattr -d com.apple.quarantine /Applications/TroubleshooterStudio.app`

> **为什么报"已损坏"?** 不是真损坏,是本应用未做苹果数字签名(没花 $99/年办 Apple Developer Account),macOS 14+ 把"未签名 + 来自互联网"统一报"已损坏"。dmg 里附的 `.command` 跑 `xattr -d com.apple.quarantine` 即可。

### CLI(macOS / Linux / Windows)

从 GitLab Release 按平台下 `tshoot-vX.Y.Z-<os>-<arch>`(Windows 自带 `.exe` 后缀),然后:

```bash
# macOS / Linux
chmod +x tshoot-vX.Y.Z-darwin-arm64
sudo mv tshoot-vX.Y.Z-darwin-arm64 /usr/local/bin/tshoot
tshoot --help                       # 看子命令
```

```powershell
# Windows(PowerShell):放到 PATH 里的目录,改名 tshoot.exe
Move-Item tshoot-vX.Y.Z-windows-amd64.exe C:\Users\<you>\bin\tshoot.exe
tshoot --help
```

### 从源码构建

`bin/` 和 `dist/` 都在 `.gitignore`,`git clone` 完得本地构建。

```bash
git clone <此仓库> && cd troubleshooter-studio
```

#### 桌面 app(macOS)

```bash
# 装依赖(一行齐):xcode + go + node
xcode-select --install && brew install go node

# 打 .app bundle 启动,无终端
make desktop-app
open dist/TroubleshooterStudio.app
# 或装到系统级,Launchpad / Spotlight 直接搜
cp -R dist/TroubleshooterStudio.app /Applications/
```

10 步「创建向导」→ 末步一键部署。

#### CLI(macOS / Linux / Windows)

```bash
# 装依赖:Go 1.25+(macOS 上 brew install go 即可;Linux / Windows 用各自包管理器或 https://go.dev/dl/)

make                                                       # CLI:bin/tshoot
./bin/tshoot demo                                          # 零配置:用内置 examples 走完整流程
./bin/tshoot init -o troubleshooter.yaml                   # 交互向导生成 yaml
./bin/tshoot gen -i troubleshooter.yaml -o ./out           # 出 staging 产物
./bin/tshoot install --path ./out --target openclaw        # 装到本机
```

模板和示例已 `go:embed` 进二进制,二进制拷到任何位置都能跑。`tshoot --help` 列全部子命令。

> Linux / Windows 当前**没有桌面 app**(Makefile 没适配 GTK + AppImage),只跑 CLI。
> `make wails-gen` / `make icon` 是**贡献者才需要**的开发任务,跟首次跑通无关,依赖(Wails CLI / librsvg)平时不用装。

## 适配的系统架构

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

- **角色**:`frontend` / `gateway` / `backend` / `middleware` / `admin` / `mobile` / `common-lib` / `infra` / `docs`
- **可观测性**:Grafana / Prometheus / Loki / Jaeger / Tempo / ELK / SkyWalking / k8s 运行时(Kuboard)
- **数据层**(只读):Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL / Kafka / RabbitMQ / ClickHouse
- **配置源**:Nacos(MCP)/ Apollo / Consul / Kuboard / Kubernetes ConfigMap / 纯环境变量
- **技术栈**:Go / Java / PHP / Python / Node(React/Vue/Next.js/Nuxt)

不适用:Serverless / FaaS、单体应用。

## Monorepo / Umbrella 仓库

把多个独立服务以 **git submodule** 挂在一个 umbrella 仓库下,同时各自又是**独立仓库**(常见模式:平台仓的 `.gitmodules` 引入 N 个服务子模块,各服务在 git 服务器上仍以独立 repo 存在)。用 `parent_repo` + `parent_path` 描述这种关系:

```yaml
repos:
  - name: platform               # umbrella 父仓
    url:  https://git.example.com/org/platform.git
    role: backend
  - name: payments               # 子模块;在 git 服务器上也是独立仓
    url:  https://git.example.com/org/payments.git
    parent_repo: platform        # 声明本仓挂在 platform umbrella 下
    parent_path: services/payments  # 在 platform/ 内的相对路径(不填时复用 name)
    role: backend
```

工作台对此自动适配:

- **wizard 仓库扫描**:扫到 monorepo 信号(`.gitmodules` / workspaces / pom modules)→ 弹"一键拆分"banner,把每个子模块出成独立 repo 条目并设好 `parent_repo`
- **路径解析(部署期 / analyze 期)**:拓扑排序保证 umbrella 先解析,子模块路径自动拼成 `<umbrella 路径>/<parent_path>`,不用每个子模块都重选目录
- **远程 / 本地两种来源**:在新机器导入 yaml 部署时,umbrella 子模块强制走 local 模式(代码必须由 umbrella `git submodule update --init` 提供,不能独立 clone),URL 锁死防误改身份
- **健康检查**:`parent_repo` 自指 / 引用不存在 / 成环 三种坏配置在 health check 阶段就拦下,清晰中文提示
- **跨协议 URL 比对**:ssh-with-port / scp-form / https 视作同仓(`ssh://git@host:2222/owner/repo.git` ≡ `https://host/owner/repo.git`)

## 桌面 app 页面

| 页面 | 做什么 |
|---|---|
| 🏠 首页 | 概览 + 下一步推荐 |
| 🤖 已装机器人 | 扫本机部署的机器人,诊断 / 编辑 yaml + 预演 + 应用 / 浏览工作目录 / 重新生成 / 卸载 |
| 🧙 创建向导 | 10 步表单 → `troubleshooter.yaml` → 末步一键部署(草稿存 localStorage) |
| 📝 YAML 沙盒 | yaml 验证 + 健康检查 + 生成计划干跑 + 产物预览 |
| 🔍 代码扫描 | 扫代码反推服务名 + 配置中心 + 依赖图 + 数据 schema,差异一键回填 yaml |
| 📜 日志 | 全工作台过程日志(install / analyze / 系统事件) |

## CLI 子命令

| 命令 | 功能 |
|---|---|
| `init` | 交互式向导生成 `troubleshooter.yaml` |
| `validate` | 校验语法与字段完整性 |
| `analyze` | 扫代码:抽 service_names + 配置中心 + 依赖图 + 数据 schema |
| `plan` / `diff` / `watch` | 干跑预览 / 精确 diff / 文件变化重跑 |
| `gen` | 生成 staging 产物 |
| `install` / `self-test` / `uninstall` | 装到本机 / openclaw 自检 / 卸载(支持 4 个平台) |
| `discover` | 扫本机已装机器人 |
| `apply` | 用新 yaml 原地更新已装机器人(模板派生文件按模板覆盖,config-map verified 人工行保留) |
| `upgrade` | 备份 + 重 gen + diff 一步到位 |
| `doctor` | 对比声明 vs 代码实态,8 类漂移(支持 `--fix` 行级精确替换) |
| `demo` | 零配置试跑 |
| `skill new` | 在模板库脚手架新 skill |

典型流:

```bash
./bin/tshoot init -o troubleshooter.yaml                                # 交互向导
./bin/tshoot validate -i troubleshooter.yaml                            # 校验
./bin/tshoot analyze -i troubleshooter.yaml --repos-root ./repos -o analysis.json
./bin/tshoot gen -i troubleshooter.yaml --analysis analysis.json        # 生成 staging
./bin/tshoot install --path dist/<id> --target openclaw         # 装机
```

`--format=json` 给 CI 消费;`tshoot --help` 列全部子命令。

## 排障机器人具备什么能力

skill 集合**按 yaml 动态裁剪**,产物的真源在 [`templates/workspace/skills/`](templates/workspace/skills/),部署后看产物里 `AGENTS.md` 知道这台机器实际启用了哪些。

- **🎯 路由 + 主流程**(始终启用)
  - `routing` —— env → 域名 / 分支 / 配置 / 日志 app / MCP 名 / 依赖图 / 表 schema 路由,静态查表毫秒返回。Step 1.3 同时 grep `known-errors.yaml`(模板内置)+ `known-errors.local.yaml`(用户排障沉淀,跨升级保留)
  - `incident-investigator` —— "症状 → 时间轴 → 横向 → 纵向 → 三向交叉 → 根因 → **沉淀**"7 步主流程,confidence=high 时 Step 7 强制把本次 pattern sink 到 `known-errors.local.yaml`,机器人越用越懂
  - `recent-changes` —— 故障窗口 ±5min K8s rollout / 配置 history / git log 三合一聚合,**配置 12 类 + 代码 5 类危险模式**自动分类(retry/熔断/cache 装饰器删除、SQL 通配前缀添加、async→sync、超时变小、replicas↓ 等)

- **🖼 图表渲染**(勾 Grafana 时启用)
  - `diagram-generator` —— Mermaid → PNG/SVG(画时间线 / 调用链)

- **⚙️ 配置中心查询**(按 `config_centers` 动态切后端)
  - `config-executor` —— nacos(MCP)/ apollo / consul / kuboard / Kubernetes ConfigMap / 纯环境变量;按 namespace/group/dataId 读配置 + 历史 + diff

- **📊 可观测性**(按 `observability.<x>.enabled` 启用)
  - `k8s-runtime-query` —— Kuboard v4 HTTP 查 pod / service / deployment / events / logs(只读)
  - `tracing-query` —— Jaeger 按 trace_id / service / 时间窗查 spans
  - `tempo-query` —— Tempo(Grafana Labs 追踪后端)按 trace_id / service 查 spans
  - `skywalking-query` —— SkyWalking 按 trace_id / service / 时间窗查 spans
  - `elk-log-query` —— ELK(Elasticsearch + Kibana)按 service / 时间 / 关键词 / trace_id 搜日志(Loki 替代 / 共存)

- **🗄 数据层运行时查询**(按 `data_stores[type=X].enabled` 启用,9 种全支持)
  - `redis-runtime-query` / `mongodb-runtime-query` / `es-runtime-query` / `mysql-runtime-query` / `postgresql-runtime-query` / `kafka-runtime-query` / `rabbitmq-runtime-query` / `clickhouse-runtime-query` —— 运行时按 entity ID 反查;连接串从配置中心动态解析(用户**不**需要重复填一遍)

裁剪规则:yaml 里没启用的能力 → 对应 skill 不生成。`generation.skills_whitelist` 是二次过滤(已启用基础上再剔除)。新增 skill 走 `tshoot skill new <name>`。

🧭 想看完整排障链路(7 步主流程 + 5 维证据表 + 反幻觉护栏 + 设计哲学)→ [`docs/troubleshooting-flow.md`](docs/troubleshooting-flow.md)

## Doctor 漂移检测

8 种规则:`missing-repo` / `origin-mismatch` / `stack-mismatch` / `service-drift` / `config-center-drift` / `config-center-unused` / `data-store-unused` / `undeclared-env-profile`。每条 issue 带可执行修复建议;机器可处理的走 `--fix` 行级精确替换(其他行 bit-perfect 保留,自动备份到 `troubleshooter.yaml.bak.<ts>`)。

## 构建

```bash
make              # CLI:bin/tshoot(等价 go build ./cmd/tshoot)
make web          # 前端 dist → internal/webui/dist/(go:embed 进二进制)
make desktop-app  # 桌面 .app bundle:dist/TroubleshooterStudio.app(默认推荐,Finder 双击 / open 启动无终端)
make desktop-dmg  # 把 .app 打成 .dmg 安装包:dist/TroubleshooterStudio-<ver>.dmg(分发用,用户双击挂载 → 拖到 Applications)
make desktop      # 桌面裸二进制:bin/tshoot-desktop(开发者用,直接跑会关联 Terminal)
make release      # 多平台交叉编译 darwin/linux × amd64/arm64 → dist/bin/
make release-notes       # dry-run,只打印自上次 tag 以来的 changelog(给眼睛 review,不改 git)
make release-publish     # 对已有 tag 重传 binary(需 GITLAB_TOKEN env;运维场景才用)
# ↑ 真正的发版本走 GitLab CI(main 合入即自动 release:patch;commit msg 含
#   [release:minor] / [release:major] 时自动跑对应 job,patch 让位;manual 按钮保留兜底)。
#   本地一键发布(make bump-* / tag-and-release)已删,强制 release 都过 CI。详见 docs/CI-RELEASE.md
make test         # go test -race -cover ./...
make lint         # go vet + gofmt + vue-tsc
make wails-gen    # 仅在改了 cmd/tshoot-desktop/App 的 method 签名时跑,刷新 web/wailsjs/go/(已入库)
make icon         # assets/app-icon.svg → cmd/tshoot-desktop/build/appicon.png
make clean        # 清 bin/ + dist/bin/ + 前端 dist 中间产物
```

`templates/` 和 `examples/` 通过仓库根 `embed.go` 用 `//go:embed` 打进二进制,运行时优先磁盘版本,没有则解压到 `~/.tshoot/templates/`。版本号:`git describe` + `git rev-parse --short HEAD`。

## 目录结构

```
cmd/tshoot/             CLI 入口
cmd/tshoot-desktop/     Wails v2 桌面 app(Wails binding 走 cmd/tshoot-desktop/App)
api/                    桌面 app 内部 HTTP handler(前端走 /api/* 跟后端通信,不对外暴露)
web/                    Vue 3 + Vite 前端
internal/
  config/               troubleshooter.yaml schema + 加载校验
  analyzer/             5 栈 × 6 配置源仓库扫描
  analyzerpipe/         pipeline 编排 + auto-clone
  generator/            模板渲染 + config-map snapshot + diff + plan + IDE 三家 agent 原生 prompt
  discover/             扫 tshoot.json 锚点识别已装机器人
  agent/                读-改-部署 + 原生 install/self-test/uninstall + IDE / openclaw 共用 MCP / creds 派生
  deploy/               凭证持久化:WriteEnvFile / ReadEnvFile 把 UI 表单填的凭证写到 <staging>/scripts/.env(0600),下次 import 预填
  doctor/               漂移检测 + --fix
  upgrade/              备份 + 重 gen + diff
  webui/                前端 dist 的 //go:embed 入口
  cchub/                配置中心客户端 hub(nacos / apollo / consul + 连接池缓存)
  dsprobe/              数据层连通性测试(redis / mongo / es / mysql / pg / kafka / mq / clickhouse)
  labelprobe/           Loki labels / values 拉取(给 wizard 标签映射用)
  openclaw/             OpenClaw 客户端 / CLI 探测
  aitools/              Claude Code / Cursor / Codex IDE 探测
  gitclone/             仓库浅克隆(给 analyze --auto-clone)
  initwizard/           CLI 交互向导
  mcpcfg/               MCP 配置生成
  skillscaffold/        `tshoot skill new` 模板脚手架
  userconfig/           ~/.tshoot/config.json 用户偏好(全局 reposRoot 等)
  watcher/              文件系统轮询监听(给 tshoot watch)
templates/              workspace/(机器人模板)
examples/               troubleshooter.yaml 示例 × 多种架构 + fake-repos
schema/troubleshooter.schema.yaml
```

## 已知限制

- 桌面 app 没代码签名 / 公证,macOS Gatekeeper 首次拦截 → 见上方 [下载与安装](#下载与安装) 章节
- 代码扫描精度依赖通用模式识别;**配置驱动 / 注解驱动 / 自定义包装层重的项目**命中率会下降,缺漏部分在桌面 app 编辑器手补即可
  - 服务调用图(downstream):识别 HTTP / gRPC / 服务发现工厂调用 + Java `@FeignClient` + Python `requests/httpx`;**配置文件驱动**的 RPC 注册需要手补
  - 数据 schema:识别主流 ORM(GORM / JPA / SQLAlchemy / TypeORM / Mongoose 等);裸 SQL / 冷门 ORM / 自定义命名约定 漏的部分需手补
  - 各栈识别精度参考:Go 70-80% / Java 60-70% / Python 60% / Node 50%
