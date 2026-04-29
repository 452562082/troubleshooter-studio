<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

**AI 排障机器人工作台** — 用 yaml 描述你的微服务系统,工具链产出"装到 OpenClaw / Claude Code / Cursor"开箱即用的排障机器人。

两层项目:
- **上层(此仓库)**:研制环境 — `system.yaml` 建模 + 仓库扫描 + 校验 + 生成 + 部署 + 管理。三个入口:**桌面 app(Wails)** / **CLI** / **HTTP API(`tshoot serve`)**。
- **下层(产出物)**:完整可运行的 AI 排障机器人 — 10+ skills(routing / config-executor / redis / mongodb / mysql / es / kafka / tracing / elk-log / diagram-generator) + 多环境 MCP + 标准故障话术。脱离 studio 独立运行。

部署到三种 AI 平台,**全程原生 Go,无 bash 依赖**:

| 目标平台 | 落地位置 | 进入方式 |
|---|---|---|
| **OpenClaw** | `~/.openclaw/workspace/<name>/` + 注入 `~/.openclaw/openclaw.json` | OpenClaw 客户端选 agent |
| **Claude Code** | `~/.claude/agents/<name>.md` + `skills/<name>/` | 任意项目里 `@<name>` 调用 |
| **Cursor** | `~/.cursor/agents/<name>.md` + `skills/<name>/` | Cursor AI 侧栏选用 |

`system.yaml` 的 `generation.targets` 勾哪些就出哪些,一次 gen 全产出。

## 30 秒试跑

**桌面 app(推荐)**:

```bash
git clone <此仓库> && cd troubleshooter-studio
make desktop-app
open dist/TroubleshooterStudio.app
```

打开后:左侧「创建向导」8 步生成新机器人 → 末步一键部署。首次启动 macOS Gatekeeper 会拦(没签名),右键 App → 打开 → 确认一次即放行。

**纯 CLI**:

```bash
go build -o bin/tshoot ./cmd/tshoot
./bin/tshoot demo                                # 内置 examples 走一遍 validate → plan → gen
./bin/tshoot install --path dist/shop --target openclaw   # 装到本机
```

模板和示例已 `go:embed` 进二进制,`bin/tshoot` 拷到任何位置都能跑,不依赖仓库 checkout。

## 适配的系统架构

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

适配 **多服务 × 多环境的微服务架构**,`repos[].role` 自由组合(`frontend` / `gateway` / `backend` / `infra` / `shared`)。基础设施按需启用:

- **可观测性**: Grafana / Prometheus / Loki / Jaeger / ELK / SkyWalking / Tempo
- **数据层**: Redis / MongoDB / ES / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse
- **配置源**: Nacos / Apollo / Consul / Kubernetes ConfigMap / 纯环境变量
- **技术栈**: Go / Java / PHP / Python / Node(React/Vue/Next.js/Nuxt)

不适用:Serverless / FaaS、单体应用。

## 桌面 app

入口:`make desktop-app && open dist/TroubleshooterStudio.app`。核心动作:

| 页面 | 做什么 |
|---|---|
| 🏠 首页 | 概览 + 下一步推荐 |
| 🤖 已装机器人 | 扫本机部署的机器人(`tshoot.json` 锚点 + Spotlight 全盘扫),编辑配置直接生效、重新生成、导出 yaml、卸载 |
| 🧙 创建向导 | 8 步表单 → `system.yaml` → 末步一键部署(草稿存 localStorage) |
| 📝 YAML 调试器 | 粘贴 yaml 做语法检查 + 生成计划预演 |
| 🔍 仓库分析 | 扫代码识别服务名 + 配置中心线索,跟 yaml 对比一键生成补丁片段 |
| 📜 日志 | 全工作台过程日志(install / analyze / chat 等) |

部署全程原生 Go(`agent.InstallNativeOpenclaw` / `InstallNative`),没有 bash 子进程。OpenClaw 凭证通过 UI 表单收集 + Keychain 持久化,一次填好后续不再问。

## CLI 用法

```bash
go build -o bin/tshoot ./cmd/tshoot

# 1. 交互向导
./bin/tshoot init -o system.yaml

# 2. 校验
./bin/tshoot validate -i system.yaml

# 3. 扫仓库(可选 --auto-clone 自动浅克隆)
./bin/tshoot analyze -i system.yaml --repos-root ./repos -o analysis.json

# 4. 预览(不落盘)
./bin/tshoot plan -i system.yaml --analysis analysis.json

# 5. 生成 staging 产物
./bin/tshoot gen -i system.yaml --analysis analysis.json

# 6. 装到本机(原生 Go,无 bash)
./bin/tshoot install --path dist/<id> --target openclaw [--env-file <.env>] [--skip-gateway-restart]
./bin/tshoot install --path dist/<id>-claude-code --target claude-code
./bin/tshoot install --path dist/<id>-cursor --target cursor

# 7. 自检 / 卸载(仅 openclaw)
./bin/tshoot self-test --path dist/<id>
./bin/tshoot uninstall --path dist/<id>

# 8. 已装机器人迭代
./bin/tshoot discover                                       # 扫本机
./bin/tshoot apply -i new.yaml --path <agent-path>          # 原地更新已装机器人
./bin/tshoot upgrade -i system.yaml                         # 备份 + 重 gen + diff
```

`tshoot`(无参)打印欢迎页;`tshoot --help` 列全部子命令;`--format=json` 给 CI 消费。

`OpenClaw` 凭证:`tshoot install` 优先读 `--env-file`,否则读 `<staging>/scripts/.env`。空凭证不阻塞 install,产物结构正确,后面手填 .env 重跑即可。

### 多目标输出

```yaml
generation:
  targets: [openclaw, claude-code, cursor]
```

```bash
./bin/tshoot gen -i system.yaml
# → dist/<id>/             OpenClaw staging
# → dist/<id>-claude-code/ Claude Code staging
# → dist/<id>-cursor/      Cursor staging
```

每份 staging 自带 `README.md`(能力清单 / 部署提示 / 升级卸载),`tshoot install` 内部读 staging 的 `tshoot.json` 反推 cfg。

## 子命令一览

| 命令 | 功能 |
|---|---|
| `init` | 交互式向导生成 `system.yaml` |
| `validate` | 校验 `system.yaml` 语法与字段完整性 |
| `analyze` | 扫代码,抽取 service_names + 配置中心线索 |
| `doctor` | 对比声明 vs 代码实态,报告漂移(支持 `--fix` 行级精确替换) |
| `plan` | 干跑 gen,展示 skills / files / overrides / config-map 分布 |
| `watch` | 文件变化时自动重跑 plan |
| `gen` | 生成 staging 产物(自带 preserve 保护人工行 + 写 `tshoot.json` 锚点) |
| `diff` | 精确到文件 + 行级的新旧产物对比 |
| `upgrade` | 备份 + 重 gen + diff 一步到位 |
| `discover` | 扫本机 `tshoot.json` 锚点列出已装机器人 |
| `apply` | 用新 yaml 原地更新已装机器人(preserve 保留人工行) |
| `install` | 把 staging 装到本机最终位置(openclaw / claude-code / cursor;原生 Go,无 bash) |
| `self-test` | openclaw 部署后自检(workspace / openclaw.json / mcp.servers / TCP+HTTP 探活) |
| `uninstall` | 卸载 openclaw agent(workspace 移到 Trash + 摘 openclaw.json + 清 creds.json) |
| `serve` | 启动 Web UI(HTTP API + 前端,功能等价桌面 app) |
| `demo` | 零配置试跑:用内置 examples 走一遍 validate → plan → gen → install |
| `skill new` | 在模板库脚手架新 skill |

## 配置源 / 可观测性 / 数据层

**配置源**(5 种):

| 类型 | analyzer 抽取 | 运行时后端 |
|---|---|---|
| `nacos` | YAML + properties + .env | MCP(`nacos-mcp-router`) |
| `apollo` | 同上 | HTTP 脚本(`apollo_config.py`) |
| `consul` | 同上 | HTTP 脚本(`consul_config.py`) |
| `kubernetes` | .env + YAML | `kubectl get`(`resolve_runtime_k8s.py`) |
| `env-vars` | .env | 安装时直接填连接串(`resolve_runtime_static.py`) |

每个 env 注册独立 MCP 实例(如 `nacos-mcp-server-dev` / `nacos-mcp-server-prod`),agent 通过 `config-map.yaml` 的 `mcp_server` 字段选实例,不需手动切换。每个 env 都使用独立用户名 / 密码,install.sh 按 env 逐一询问。

**可观测性**(7 项):Grafana / Prometheus(via Grafana) / Loki(via Grafana) / Jaeger / ELK / SkyWalking / Tempo。

**数据层**(9 种,**严格只读**):Redis / MongoDB / Elasticsearch / MySQL / PostgreSQL(MCP) + Kafka / RocketMQ / RabbitMQ(CLI / HTTP API) + ClickHouse(`readonly=1`)。`skills_whitelist` 按需勾选。

## 技术栈分析器

| 栈 | 识别来源 | 框架检测 | 配置扫描 |
|---|---|---|---|
| `go` | `go.mod` | — | YAML |
| `java` | `pom.xml` | Spring profile | YAML + properties |
| `node` | `package.json` | Next.js / Nuxt / Vite / CRA / Vue CLI / Angular | `.env.*`(API URL) |
| `php` | `composer.json` | — | `.env.*` + YAML |
| `python` | `pyproject.toml` / `setup.py` | Django / FastAPI / Flask / Tornado / Sanic | `.env.*` + YAML |

`analyze --auto-clone` 自动浅克隆缺失仓库;`--branch` 指定分支。

## Doctor 漂移检测

8 种规则:`missing-repo` / `origin-mismatch` / `stack-mismatch` / `service-drift` / `config-center-drift` / `config-center-unused` / `data-store-unused` / `undeclared-env-profile`。每条 issue 带 `↳ 建议:` 可执行修复动作。

机器可处理的 issue 走 `--fix` 自动改 yaml(行级精确替换,其他行 bit-perfect 保留,自动备份到 `system.yaml.bak.<ts>`):

```bash
tshoot doctor -i system.yaml --repos-root ./repos --fix       # 显示 patch + 询问确认
tshoot doctor -i system.yaml --repos-root ./repos --fix -y    # 跳确认(CI)
```

## Preserve / Diff / Upgrade

- 手改 `config-map.yaml` 的 inferred 行为 verified(去掉 `source:` 字段),下次 `gen` 自动保留
- `generation.preserve_on_regenerate` 列表中的文件(如 `SOUL.md`)re-gen 时整体保留
- 优先级:**analyzer finding > prior manual override > inferred skeleton**
- 切换 config_center 类型自动丢弃旧 overrides,避免字段错配
- `tshoot diff` 模拟"带 preserve 的 gen",精确展示真实会发生的变化
- `tshoot upgrade` = 备份到 `<out>.bak.<ts>` + gen + diff 一步到位

## 构建与发布

```bash
# CLI
make              # go build → bin/tshoot
make web          # vite build → 拷到 internal/webui/dist/(embed)
make release      # web + 多平台交叉编译(darwin/linux × amd64/arm64) → dist/bin/

# 桌面 app(Wails v2)
make desktop      # bin/tshoot-desktop(裸二进制)
make desktop-app  # 上一步 + scripts/package-macos.sh 打 .app + .icns → dist/TroubleshooterStudio.app
make icon         # 重渲染 assets/app-icon.svg → cmd/tshoot-desktop/build/appicon.png

# 通用
make wails-gen    # 重生 web/wailsjs/(Go API 改动后跑)
make test         # go test -race -cover ./...
make lint         # go vet + gofmt + vue-tsc
make clean
```

版本号:`git describe --tags` + `git rev-parse --short HEAD` → `tshoot --version`,同步写入 `tshoot.json.tshoot_version` + macOS bundle 的 `CFBundleVersion`。

`templates/` 和 `examples/` 通过仓库根 `embed.go` 用 `//go:embed` 打进二进制,运行时优先用磁盘上的(存在则用),否则解压到 `~/.tshoot/templates/` 复用。

### Wails 构建关键点

- **必须带 build tags**: `-tags "desktop production"`
- **必须 cgo + 链 UniformTypeIdentifiers**: `CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=10.13"`
- **走 .app 包**: 裸二进制双击会被 macOS 拖到 Terminal 启动(弹终端),`make desktop-app` 产出的 `.app` 不会

## 测试 / CI

```bash
make lint         # go vet + gofmt -l + vue-tsc
make test         # 全量 + 竞态(~5s)
```

CI 流水线 `.github/workflows/ci.yml` 在每次 push / PR 自动运行。`internal/agent/` 的 install / self-test / uninstall / prompts 派生有完整 fixture 测试覆盖各 cc 类型 / observability 开关 / per-env 凭证模式。

## 目录结构

```
embed.go                    # //go:embed templates/ + examples/
Makefile
cmd/
  tshoot/                   # CLI 入口(15 子命令)
  tshoot-desktop/           # Wails v2 桌面 app
api/                        # tshoot serve 的 HTTP handler
web/                        # 前端 Vue 3 + Vite + TS
internal/
  config/                   # system.yaml 加载与校验
  analyzer/                 # 仓库扫描:5 栈 × 5 配置源
  generator/                # 模板渲染 + preserve + diff + plan
  discover/                 # 扫 tshoot.json 锚点识别已装机器人
  agent/                    # 读-改-部署闭环 + 原生 install / self-test / uninstall
  doctor/                   # 漂移检测 + --fix 行级精确替换
  upgrade/                  # 备份 + 重 gen + diff
  initwizard/               # CLI 交互向导
  skillscaffold/            # skill new 脚手架
  llmchat/                  # 多 provider LLM 直连(已装机器人对话用)
  dsprobe/                  # 数据层连通性探测(Redis/MySQL/PG/Mongo/ES/CH)
  watcher/                  # 文件变化轮询
  webui/                    # 前端 dist 的 //go:embed 入口
templates/                  # workspace/ workspace-template/
examples/                   # system.yaml × 多种架构 + fake-repos × N
schema/system.schema.yaml
assets/                     # logo.svg + app-icon.svg + architecture.svg
.github/workflows/ci.yml
```

## 已知限制

- 桌面 app 没代码签名 / 公证,macOS Gatekeeper 首次会拦(右键 → 打开确认一次即可)
- Apollo / Consul 走 HTTP 脚本而非 MCP(生态尚无稳定 MCP 包;Nacos 走 MCP)
- Node 生态不扫配置中心(极少用 Nacos / Apollo / Consul)
- 不自动推断服务调用拓扑(只做 per-repo 机械抽取)
- `tshoot init` 暂不生成 `dataid_patterns` 等高级字段(高级用户手工补)
