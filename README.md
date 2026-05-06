<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

**AI 排障机器人工作台** —— 用 yaml 描述微服务系统,工具链产出"装到 OpenClaw / Claude Code / Cursor / Codex CLI"开箱即用的排障机器人。

两层项目:

- **上层(此仓库)**:研制环境,做 `system.yaml` 建模、仓库扫描、校验、生成、部署、管理
- **下层(产出物)**:完整可运行的排障机器人 —— skill 集合按 yaml 动态裁剪,固定核心 + 按配置 / 数据层 / 可观测性勾选的 runtime-query + 多环境 MCP + 标准故障话术,脱离 studio 独立运行

## 三个入口

| 入口 | 能力 | 适用 |
|---|---|---|
| **桌面 app** (Wails) | 完整(建模 / 扫描 / 部署 / 已装管理 / 工作目录浏览) | 个人用,推荐新用户 |
| **CLI** (`tshoot`) | 完整 yaml 计算 + 装机能力(4 平台齐全) | 脚本 / SSH / CI |
| **HTTP API** (`tshoot serve`) | 仅 yaml 计算子集(validate / plan / gen / doctor / schema)| CI 集成 / 浏览器模式 / 接到自家平台。**不含装机** —— 改活 workspace 必须在该机器本地 |

## 部署到 4 个 AI 平台

全程原生 Go,无 bash 依赖。`generation.targets` 勾哪些就出哪些,一次 gen 全产出。

| 平台 | 部署位置 | 进入方式 | MCP 注册位置 |
|---|---|---|---|
| **OpenClaw** | `~/.openclaw/workspace/<name>/`(整套 workspace 文件) | OpenClaw 客户端 agent 列表选(装完**重启客户端**才出现) | `~/.openclaw/openclaw.json` 的 `mcp.servers` |
| **Claude Code** | `~/.claude/agents/<name>.md` + `~/.claude/skills/<name>/` | 任意项目 `@<name>` 调 subagent | `~/.claude/settings.json` 的 `mcpServers` |
| **Cursor** | `~/.cursor/agents/<name>.md` + `~/.cursor/skills/<name>/` | AI 侧栏选 Custom Agent(MCP 还要去 Settings 启用) | `~/.cursor/mcp.json` 的 `mcpServers` |
| **Codex CLI** | `~/.codex/agents/<name>.toml`(TOML subagent) + `~/.codex/skills/<name>/` + `~/.codex/bin/mcp-grafana`(go 二进制,grafana/loki 共用) | 终端 `codex` 内主 chat 说 "spawn the `<name>` agent ..."(自然语言派生 subagent thread,完成后回主 chat;[官方文档](https://developers.openai.com/codex/subagents)) | 嵌入 agent toml 内联 `[mcp_servers.*]` 段(每个 subagent 自带,不污染主 chat) |

凭证持久化:`~/.openclaw/<id>-creds.json`(OpenClaw)+ `~/.tshoot/<id>-creds.json`(IDE 平台通用 fallback,脚本两处优先 openclaw)。

每个 IDE 平台的 agent 定义(Claude Code/Cursor 的 `agents/<name>.md`、Codex CLI 的 `agents/<name>.toml`)是为该平台**原生写**的(不是把 OpenClaw workspace 文件机械拼贴),含平台运行环境介绍(Bash 能力 / MCP 注册位置 / skills 路径前缀)+ 通用排障逻辑(SOUL / IDENTITY / 排障入口 / 故障快报模板)+ skills 索引。

**Codex grafana/loki MCP 走 go 二进制**(不走 npx):`tshoot install --target codex` 自动从 `github.com/grafana/mcp-grafana` releases 下载预编译版到 `~/.codex/bin/mcp-grafana`。换 go 版的原因:`@leval/mcp-grafana` 这个 npm 包启动时往 stdout 打 banner 污染 JSON-RPC 流,导致 codex 握手"connection closed: initialize response";同时 codex subagent thread 默认 network=Restricted 让 npx 拉包也可能失败。go 版严格 stdio + 装好就跑,绕开两条死亡路径。下载失败会 fallback 到 npx 但会打 warning。

## 快速开始

```bash
git clone <此仓库> && cd troubleshooter-studio
```

> 首次构建必看:`bin/` 和 `dist/` 都在 `.gitignore` 里,`git clone` 完没有可执行文件 ——
> 必须先跑下方任一构建命令产出 `bin/tshoot`(CLI)或 `dist/TroubleshooterStudio.app`(桌面)
> 才能用。改 Go binding(`cmd/tshoot-desktop/App` 的 method 签名变了)才需要 `make wails-gen`
> 刷新 `web/wailsjs/go/`(已入库,普通构建不用动)。

**桌面 app**(推荐,新用户):

```bash
make desktop-app
open dist/TroubleshooterStudio.app
```

10 步「创建向导」生成新机器人 → 末步一键部署。首次启动 macOS Gatekeeper 会拦(没签名),右键 App → 打开 → 确认即放行。

**CLI**(脚本 / SSH / CI):

```bash
make                                                       # 等价 go build -o bin/tshoot ./cmd/tshoot
./bin/tshoot demo                                          # 零配置:用内置 examples 走完整流程
./bin/tshoot init -o system.yaml                           # 交互向导生成 yaml
./bin/tshoot gen -i system.yaml -o ./out                   # 出 staging 产物
./bin/tshoot install --path ./out --target openclaw        # 装到本机
```

模板和示例已 `go:embed` 进二进制,二进制拷到任何位置都能跑。`tshoot --help` 列全部 16 个子命令。

**HTTP API**(CI 集成 / 浏览器模式):

```bash
./bin/tshoot serve --port 8080
# Web UI:  http://localhost:8080
# API 端点: /api/{validate,plan,gen,doctor,prefill-creds,schema}
```

`/api/gen` 走临时目录 + Summary 即返回(不在 server 端持久化产物);需要拿 staging 文件请走 CLI 或桌面 app。

## 适配的系统架构

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

- **角色**:`frontend` / `gateway` / `backend` / `middleware` / `admin` / `mobile` / `common-lib` / `infra` / `docs`
- **可观测性**:Grafana / Prometheus / Loki / Jaeger / ELK / SkyWalking / k8s 运行时(Kuboard)
- **数据层**(只读):Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse
- **配置源**:Nacos(MCP)/ Apollo / Consul / Kuboard / Kubernetes ConfigMap / 纯环境变量
- **技术栈**:Go / Java / PHP / Python / Node(React/Vue/Next.js/Nuxt)

不适用:Serverless / FaaS、单体应用。

## 桌面 app 页面

| 页面 | 做什么 |
|---|---|
| 🏠 首页 | 概览 + 下一步推荐 |
| 🤖 已装机器人 | 扫本机部署的机器人,诊断 / 编辑 yaml + 预演 + 应用 / 浏览工作目录 / 重新生成 / 卸载 |
| 🧙 创建向导 | 10 步表单 → `system.yaml` → 末步一键部署(草稿存 localStorage) |
| 📝 YAML 沙盒 | yaml 验证 + 健康检查 + 生成计划干跑 + 产物预览 |
| 🔍 代码扫描 | 扫代码反推服务名 + 配置中心 + 依赖图 + 数据 schema,差异一键回填 yaml |
| 📜 日志 | 全工作台过程日志(install / analyze / 系统事件) |

## CLI 子命令

| 命令 | 功能 |
|---|---|
| `init` | 交互式向导生成 `system.yaml` |
| `validate` | 校验语法与字段完整性 |
| `analyze` | 扫代码:抽 service_names + 配置中心 + 依赖图 + 数据 schema |
| `plan` / `diff` / `watch` | 干跑预览 / 精确 diff / 文件变化重跑 |
| `gen` | 生成 staging 产物 |
| `install` / `self-test` / `uninstall` | 装到本机 / openclaw 自检 / 卸载(支持 4 个平台) |
| `discover` | 扫本机已装机器人 |
| `apply` | 用新 yaml 原地更新已装机器人(preserve 保留手改) |
| `upgrade` | 备份 + 重 gen + diff 一步到位 |
| `doctor` | 对比声明 vs 代码实态,8 类漂移(支持 `--fix` 行级精确替换) |
| `serve` | 启 HTTP API + Web UI |
| `demo` | 零配置试跑 |
| `skill new` | 在模板库脚手架新 skill |

典型流:

```bash
./bin/tshoot init -o system.yaml                                # 交互向导
./bin/tshoot validate -i system.yaml                            # 校验
./bin/tshoot analyze -i system.yaml --repos-root ./repos -o analysis.json
./bin/tshoot gen -i system.yaml --analysis analysis.json        # 生成 staging
./bin/tshoot install --path dist/<id> --target openclaw         # 装机
```

`--format=json` 给 CI 消费;`tshoot --help` 列全部子命令。

## HTTP API 端点

| 端点 | 用法 |
|---|---|
| `POST /api/validate` | body=system.yaml,返回是否合规 + 错误位置 + health-check 提示 |
| `POST /api/plan` | body=system.yaml,返回干跑 gen 摘要(临时目录,自动清) |
| `POST /api/gen` | body=system.yaml,真跑生成器返回 Summary(stats + 文件清单);**产物写在临时目录用完即删**,不在 server 端持久化 —— 需要拿 staging 走 CLI / 桌面 app |
| `POST /api/doctor` | body=system.yaml + `?repos_root=<path>`,返回 8 类漂移 |
| `POST /api/prefill-creds` | body=system.yaml,返回 install 时需要哪些 env var key |
| `GET /api/schema` | 返回 `system.schema.yaml`(给前端做字段提示) |

body size 限制 1 MB(超限 400);所有 handler 无鉴权,自部署到内网。

CI 示例:

```bash
curl -fsS -X POST -H "Content-Type: text/yaml" \
  --data-binary @system.yaml \
  "http://localhost:8080/api/doctor?repos_root=/path/to/code"
```

## 排障机器人具备什么能力

skill 集合**按 yaml 动态裁剪**,产物的真源在 [`templates/workspace/skills/`](templates/workspace/skills/),部署后看产物里 `AGENTS.md` 知道这台机器实际启用了哪些。

- **🎯 路由 + 主流程**(始终启用)
  - `routing` —— env → 域名 / 分支 / 配置 / 日志 app / MCP 名 / 依赖图 / 表 schema 路由,静态查表毫秒返回
  - `incident-investigator` —— "症状 → 时间轴 → 横向 → 纵向 → 三向交叉 → 根因"6 步主流程
  - `recent-changes` —— 故障窗口 ±5min K8s rollout / 配置 history / git log 三合一聚合
  - `diagram-generator` —— Mermaid → PNG/SVG(画时间线 / 调用链)

- **⚙️ 配置中心查询**(按 `config_centers` 动态切后端)
  - `config-executor` —— nacos(MCP)/ apollo / consul / kuboard / 环境变量等;按 namespace/group/dataId 读配置 + 历史 + diff

- **📊 可观测性**(按 `observability.<x>.enabled` 启用)
  - `k8s-runtime-query` —— Kuboard v4 HTTP 查 pod / service / deployment / events / logs(只读)
  - `tracing-query` —— Jaeger 按 trace_id / service / 时间窗查 spans

- **🗄 数据层运行时查询**(按 `data_stores[type=X].enabled` 启用,9 种全支持)
  - `redis-runtime-query` / `mongodb-runtime-query` / `es-runtime-query` / `mysql-runtime-query` / `postgres-runtime-query` / `kafka-runtime-query` / `rocketmq-runtime-query` / `rabbitmq-runtime-query` / `clickhouse-runtime-query` —— 运行时按 entity ID 反查;连接串从配置中心动态解析(用户**不**需要重复填一遍)

裁剪规则:yaml 里没启用的能力 → 对应 skill 不生成。`generation.skills_whitelist` 是二次过滤(已启用基础上再剔除)。新增 skill 走 `tshoot skill new <name>`。

## Doctor 漂移检测

8 种规则:`missing-repo` / `origin-mismatch` / `stack-mismatch` / `service-drift` / `config-center-drift` / `config-center-unused` / `data-store-unused` / `undeclared-env-profile`。每条 issue 带可执行修复建议;机器可处理的走 `--fix` 行级精确替换(其他行 bit-perfect 保留,自动备份到 `system.yaml.bak.<ts>`)。

## 构建

```bash
make              # CLI:bin/tshoot(等价 go build ./cmd/tshoot)
make web          # 前端 dist → internal/webui/dist/(go:embed 进二进制)
make desktop      # 桌面裸二进制:bin/tshoot-desktop
make desktop-app  # 桌面 .app bundle:dist/TroubleshooterStudio.app(Mac 双击就跑)
make release      # 多平台交叉编译 darwin/linux × amd64/arm64 → dist/bin/
make test         # go test -race -cover ./...
make lint         # go vet + gofmt + vue-tsc
make wails-gen    # 改 Go binding 后刷新 web/wailsjs/go/(平时不用动)
make icon         # assets/app-icon.svg → cmd/tshoot-desktop/build/appicon.png
make clean        # 清 bin/ + dist/bin/ + 前端 dist 中间产物
```

`templates/` 和 `examples/` 通过仓库根 `embed.go` 用 `//go:embed` 打进二进制,运行时优先磁盘版本,没有则解压到 `~/.tshoot/templates/`。版本号:`git describe` + `git rev-parse --short HEAD`。

## 目录结构

```
cmd/tshoot/             CLI 入口(16 个子命令)
cmd/tshoot-desktop/     Wails v2 桌面 app(Wails binding 走 cmd/tshoot-desktop/App)
api/                    HTTP handler(tshoot serve)
web/                    Vue 3 + Vite 前端
internal/
  config/               system.yaml schema + 加载校验
  analyzer/             5 栈 × 6 配置源仓库扫描
  analyzerpipe/         pipeline 编排 + auto-clone
  generator/            模板渲染 + preserve + diff + plan + IDE 三家 agent 原生 prompt
  discover/             扫 tshoot.json 锚点识别已装机器人
  agent/                读-改-部署 + 原生 install/self-test/uninstall + IDE / openclaw 共用 MCP / creds 派生
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
examples/               system.yaml 示例 × 多种架构 + fake-repos
schema/system.schema.yaml
```

## 已知限制

- 桌面 app 没代码签名 / 公证,macOS Gatekeeper 首次会拦,右键 → 打开放行
- 代码扫描精度依赖通用模式识别;**配置驱动 / 注解驱动 / 自定义包装层重的项目**命中率会下降,缺漏部分在桌面 app 编辑器手补即可
  - 服务调用图(downstream):识别 HTTP / gRPC / 服务发现工厂调用 + Java `@FeignClient` + Python `requests/httpx`;**配置文件驱动**的 RPC 注册需要手补
  - 数据 schema:识别主流 ORM(GORM / JPA / SQLAlchemy / TypeORM / Mongoose 等);裸 SQL / 冷门 ORM / 自定义命名约定 漏的部分需手补
  - 各栈识别精度参考:Go 70-80% / Java 60-70% / Python 60% / Node 50%
