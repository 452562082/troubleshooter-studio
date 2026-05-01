<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

**AI 排障机器人工作台** —— 用 yaml 描述你的微服务系统,工具链产出"装到 OpenClaw / Claude Code / Cursor / Codex CLI"开箱即用的排障机器人。

两层项目:

- **上层(此仓库)**:研制环境 —— 桌面 app(Wails) / CLI / HTTP API 三个入口,做 `system.yaml` 建模、仓库扫描、校验、生成、部署、管理。
- **下层(产出物)**:完整可运行的 AI 排障机器人 —— **skill 集合按 yaml 配置动态裁剪**:固定核心(`routing` 路由 + `incident-investigator` 主流程 + `recent-changes` 变更聚合)+ 按你的配置源 / 数据层 / 可观测性勾选自动启用的 runtime-query 系列(redis / mongodb / es / kafka / k8s / tracing / 等)+ 多环境 MCP + 标准故障话术。脱离 studio 独立运行。

## 部署到 4 个 AI 平台

全程原生 Go,无 bash 依赖。`system.yaml` 的 `generation.targets` 勾哪些就出哪些,一次 gen 全产出。

| 平台 | 部署位置 | 进入方式 | MCP 注册位置 |
|---|---|---|---|
| **OpenClaw** | `~/.openclaw/workspace/<name>/` | OpenClaw 客户端选 agent | `~/.openclaw/openclaw.json` |
| **Claude Code** | `~/.claude/agents/<name>.md` + `~/.claude/{skills,scripts}/<name>/` | 任意项目 `@<name>` 调用 | `~/.claude/settings.json` |
| **Cursor** | `~/.cursor/agents/<name>.md` + `~/.cursor/{skills,scripts}/<name>/` | AI 侧栏选 Custom Agent | `~/.cursor/mcp.json` |
| **Codex CLI** | `~/.codex/agents/<name>.md` + `~/.codex/{skills,scripts}/<name>/` | `@<name>` 调用 | `~/.codex/config.toml`(走 `codex mcp add`) |

## 快速开始

**桌面 app(推荐)**:

```bash
git clone <此仓库> && cd troubleshooter-studio
make desktop-app
open dist/TroubleshooterStudio.app
```

10 步「创建向导」生成新机器人 → 末步一键部署。首次启动 macOS Gatekeeper 会拦(没签名),右键 App → 打开 → 确认即放行。

**纯 CLI**:

```bash
go build -o bin/tshoot ./cmd/tshoot
./bin/tshoot demo                                          # 用内置 examples 走一遍
./bin/tshoot install --path dist/<id> --target openclaw    # 装到本机
```

模板和示例已 `go:embed` 进二进制,二进制拷到任何位置都能跑。

## 适配的系统架构

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

- **角色**: `frontend` / `gateway` / `backend` / `middleware` / `admin` / `mobile` / `common-lib` / `infra` / `docs`
- **可观测性**: Grafana / Prometheus(via Grafana) / Loki(via Grafana) / Jaeger / ELK / SkyWalking / k8s 运行时(Kuboard)
- **数据层(只读)**: Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse
- **配置源**: Nacos(MCP) / Apollo / Consul / Kuboard / Kubernetes ConfigMap / 纯环境变量
- **技术栈**: Go / Java / PHP / Python / Node(React/Vue/Next.js/Nuxt)

不适用:Serverless / FaaS、单体应用。

## 桌面 app 页面速览

| 页面 | 做什么 |
|---|---|
| 🏠 首页 | 概览 + 下一步推荐 |
| 🤖 已装机器人 | 扫本机部署的机器人,诊断 / 编辑 yaml + 预演 + 应用 / 浏览工作目录 / 重新生成并刷新 / 卸载 |
| 🧙 创建向导 | 10 步表单 → `system.yaml` → 末步一键部署(草稿存 localStorage) |
| 📝 YAML 沙盒 | yaml 验证 + 健康检查 + 生成计划干跑 + 产物预览 |
| 🔍 代码扫描 | 扫代码反推服务名 + 配置中心 + 依赖图 + 数据 schema,差异一键回填 yaml |
| 📜 日志 | 全工作台过程日志(install / analyze / 系统事件) |

部署全程原生 Go,凭证持久化到 `~/.openclaw/<id>-creds.json`(OpenClaw)+ `~/.tshoot/<id>-creds.json`(IDE 平台通用),仓库本地路径存 `~/.tshoot/config.json`。

## CLI 用法

```bash
# 1. 交互向导
./bin/tshoot init -o system.yaml

# 2. 校验
./bin/tshoot validate -i system.yaml

# 3. 扫仓库(可选 --auto-clone 自动浅克隆)
./bin/tshoot analyze -i system.yaml --repos-root ./repos -o analysis.json

# 4. 干跑
./bin/tshoot plan -i system.yaml --analysis analysis.json
./bin/tshoot diff -i system.yaml                    # 跟现有产物的精确 diff

# 5. 生成 staging
./bin/tshoot gen -i system.yaml --analysis analysis.json

# 6. 装到本机
./bin/tshoot install --path dist/<id> --target openclaw [--env-file <.env>]

# 7. 已装机器人迭代
./bin/tshoot discover                                       # 扫本机
./bin/tshoot apply -i new.yaml --path <agent-path>          # 原地更新
./bin/tshoot doctor -i system.yaml --repos-root ./repos     # 漂移检测
./bin/tshoot upgrade -i system.yaml                         # 备份 + 重 gen + diff
```

`--format=json` 给 CI 消费;`tshoot --help` 列全部子命令。CLI 的 `install` 目前支持 `openclaw / claude-code / cursor`(codex 走桌面 app)。

## 子命令速查

| 命令 | 功能 |
|---|---|
| `init` | 交互式向导生成 `system.yaml` |
| `validate` | 校验语法与字段完整性 |
| `analyze` | 扫代码:抽 service_names + 配置中心 + 依赖图 + 数据 schema |
| `doctor` | 对比声明 vs 代码实态,报告 8 类漂移(支持 `--fix` 行级精确替换) |
| `plan` / `diff` / `watch` | 干跑预览 / 精确 diff / 文件变化重跑 |
| `gen` | 生成 staging 产物 |
| `apply` | 用新 yaml 原地更新已装机器人(preserve 保留手改) |
| `upgrade` | 备份 + 重 gen + diff 一步到位 |
| `discover` | 扫本机已装机器人 |
| `install` / `self-test` / `uninstall` | 装到本机 / openclaw 自检 / 卸载 |
| `serve` | 启 Web UI(HTTP + 前端) |
| `demo` | 零配置试跑 |
| `skill new` | 在模板库脚手架新 skill |

## 排障机器人具备什么能力(产出物)

skill 集合**按 yaml 动态裁剪**,实际产出取决于你勾选的配置源 / 数据层 / 可观测性 / `generation.skills_whitelist`。能装的 skill 全集:

| skill | 何时启用 | 干什么 |
|---|---|---|
| `routing` | 总是 | 11 张映射表(env → 域名 / 分支 / 配置 / 日志 app / MCP 名 / 依赖图 / 表 schema / known-errors),静态查表毫秒返回 |
| `incident-investigator` | 总是 | 固化"症状 → 时间轴 → 横向 → 纵向 → 三向交叉 → 根因"6 步流程,任何报障先走这套 |
| `recent-changes` | 总是 | 故障窗口 ±5min K8s rollout / 配置变更 / git log 三合一聚合 |
| `config-executor` | 总是(后端按 yaml `infrastructure.config_centers` 动态切) | 5 种后端:nacos(MCP)/ apollo / consul / kuboard / 静态环境变量,按 namespace/group/dataId 读配置 + 历史 + diff |
| `k8s-runtime-query` | `observability.k8s_runtime.enabled: true` | Kuboard v4 HTTP API 查 pod / service / deployment / events / logs(只读) |
| `tracing-query` | `observability.jaeger.enabled: true`(或同类) | Jaeger 按 trace_id / service / 时间窗查 spans |
| `redis-runtime-query` | `data_stores[type=redis].enabled: true` | 运行时按 entity ID 反查;连接串从配置中心动态解析,mcporter 临时拉起 ad-hoc MCP |
| `mongodb-runtime-query` | `data_stores[type=mongodb].enabled: true` | 同上,query / aggregate / count / listCollections |
| `es-runtime-query` | `data_stores[type=elasticsearch].enabled: true` | 同上 |
| `kafka-runtime-query` | `data_stores[type=kafka].enabled: true` | 通过 kafka CLI 或 mcporter 看 topic / consumer-group / 积压 |
| `diagram-generator` | 总是 | Mermaid 文本 → PNG/SVG(走 mermaid.ink) |

未启用 / 未勾选的 skill **直接不生成**,产物里就没那个目录;`generation.skills_whitelist` 还能进一步收窄(已启用的 skill 也可以排除掉)。完整能力 + 限制说明见部署完成后产物里的 `AGENTS.md`。

## HTTP API 用法

`tshoot serve` 启动一个 HTTP server,等价 CLI 的"yaml 计算 + 校验"子集(不含部署 / 安装 / 扫机器),给 CI 集成 / 浏览器模式 / 接到自家平台用。

```bash
./bin/tshoot serve [--port 8080]
# Web UI: http://localhost:8080
# API:    http://localhost:8080/api/
```

| 端点 | 用法 |
|---|---|
| `POST /api/validate` | body=system.yaml(`Content-Type: text/yaml`),返回是否合规 + 错误位置 |
| `POST /api/plan` | body=system.yaml,返回干跑 gen 摘要(skills / files / config-map 分布) |
| `POST /api/gen` | body=system.yaml,真生成 staging 到默认 `./dist/<id>/` |
| `POST /api/doctor` | body=system.yaml + query `?repos_root=<path>`,返回 8 类漂移 issue 列表 |
| `POST /api/prefill-creds` | body=system.yaml,返回 install 时需要哪些 env var key |
| `GET /api/schema` | 返回 `system.schema.yaml` 内容(给前端做字段提示用) |

示例(在 CI 里把 yaml 当 lint 跑):

```bash
curl -fsS -X POST -H "Content-Type: text/yaml" \
  --data-binary @system.yaml \
  http://localhost:8080/api/validate

# 漂移检测 + 代码扫描:
curl -fsS -X POST -H "Content-Type: text/yaml" \
  --data-binary @system.yaml \
  "http://localhost:8080/api/doctor?repos_root=/path/to/code"
```

**HTTP API 不支持的**(只在桌面 app 走 Wails binding):
`discover` 扫本机已装机器人 / `apply install uninstall` 装机操作 / 已装机器人配置编辑 + 工作目录浏览 + MCP 注册。HTTP 模式是"无副作用 / 跨机可用"的子集,这是有意为之 —— 改活 workspace 必须在产生它的机器上做,不通过网络远程操作。

## Doctor 漂移检测

8 种规则:`missing-repo` / `origin-mismatch` / `stack-mismatch` / `service-drift` / `config-center-drift` / `config-center-unused` / `data-store-unused` / `undeclared-env-profile`。每条 issue 带可执行修复建议;机器可处理的走 `--fix` 行级精确替换(其他行 bit-perfect 保留,自动备份到 `system.yaml.bak.<ts>`)。

## 构建

```bash
make              # CLI:bin/tshoot
make web          # 前端 dist 进 internal/webui/(go:embed)
make desktop-app  # 桌面 app .app:dist/TroubleshooterStudio.app
make release      # 多平台交叉编译 darwin/linux × amd64/arm64
make test lint    # go test -race + go vet + gofmt + vue-tsc
```

`templates/` 和 `examples/` 通过仓库根 `embed.go` 用 `//go:embed` 打进二进制,运行时优先磁盘版本,没有则解压到 `~/.tshoot/templates/`。版本号:`git describe` + `git rev-parse --short HEAD`,写入 `tshoot.json.tshoot_version` + `CFBundleVersion`。

## 目录结构

```
cmd/tshoot/             CLI 入口
cmd/tshoot-desktop/     Wails v2 桌面 app
api/                    HTTP handler(tshoot serve)
web/                    Vue 3 + Vite 前端
internal/
  config/               system.yaml schema + 加载校验
  analyzer/             5 栈 × 6 配置源仓库扫描
  analyzerpipe/         pipeline 编排 + auto-clone
  generator/            模板渲染 + preserve + diff + plan
  discover/             扫 tshoot.json 锚点识别已装机器人
  agent/                读-改-部署 + 原生 install/self-test/uninstall + IDE 凭证管理
  doctor/               漂移检测 + --fix
  upgrade/              备份 + 重 gen + diff
  webui/                前端 dist 的 //go:embed 入口
  ...
templates/              workspace/(机器人模板) + workspace-template/(子模板)
examples/               system.yaml 示例 × 多种架构 + fake-repos
schema/system.schema.yaml
```

## 已知限制

- 桌面 app 没代码签名 / 公证,macOS Gatekeeper 首次会拦
- CLI 的 `install` 暂不支持 codex(桌面 app 已支持 codex 全套)
- 服务依赖图(downstream)的 Go 扫描覆盖 truss / 老 kratos 的 `client.NewXxxClient(naming, XxxServiceName, ns)` 风格;go-zero / kratos v2 / kitex 等需要在沙盒里手补
- 服务调用链路 / 数据 schema 自动扫描精度 50-70%,主流 ORM(GORM / JPA / SQLAlchemy / TypeORM / Mongoose)命中率较高,冷门 ORM 漏的部分需手填
