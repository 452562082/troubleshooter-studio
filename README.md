<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-factory" width="720"/>
</p>

# troubleshooter-factory

为任意业务系统生成一个定制化的 **AI 排障机器人一键部署包**。

输入一份 `system.yaml`（系统身份 / 环境清单 / 仓库列表 / 基础组件配置），一键产出四种部署形态的机器人包，**每种都自带 `install.sh` 一键部署**：

- **OpenClaw 安装包** — `bash install.sh` 部署到 OpenClaw（工作区 + self-test.sh + MCP 配置）
- **Claude Code** — `bash install.sh <project-dir>` 将 `CLAUDE.md` + `skills/` 装入项目根
- **Cursor IDE** — `bash install.sh <project-dir>` 将 `.cursorrules` + `.cursor/rules/` + `skills/` 装入项目根
- **Standalone Web 聊天** — `bash install.sh`（venv + pip install）或 `docker compose up --build`，不依赖任何 AI 平台，自带聊天界面

按需在 `generation.targets` 里勾选，一次 `gen` 全部生成。

## 30 秒试跑

```bash
# clone 仓库后本地构建（templates + examples 已经 embed 进二进制，bin/factory 可以拷到任何位置用）
git clone <此仓库> && cd troubleshooter-factory
go build -o bin/factory ./cmd/factory
./bin/factory demo
```

或本地装到 `$GOPATH/bin`：

```bash
cd troubleshooter-factory
go install ./cmd/factory         # $GOBIN/factory（任意目录跑 `factory demo`）
```

看清 factory 能出什么再决定要不要接入自己的系统。无需任何凭证 / 仓库 checkout。

> 仓库发布到 GitHub 后，可以用 `go install github.com/<owner>/troubleshooter-factory/cmd/factory@latest` 一行装；目前是本地仓库，请走上面两种方式。

## 适配的系统架构

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

本工具适配 **多服务 × 多环境的微服务架构**。架构不固定，`repos[].role` 自由组合：

| 架构模式 | repos 组合 | 典型场景 |
|---|---|---|
| 前端 + BFF + 后端 | `frontend` + `gateway` + `backend` × N | 电商 / 社交 / SaaS |
| 前端 + 后端（无 BFF） | `frontend` + `backend` × N | 中小系统 / 内部工具 |
| 纯后端 API（无前端） | `backend` × N | B2B 接口 / 内部服务集群 |
| API 网关 + 微服务 | `gateway` + `backend` × N | Kong / APISIX 统一入口 |
| Monorepo（单仓多服务） | 1 repo + 多 `service_names` | 大厂 monorepo |
| 事件驱动（MQ 串联） | `backend` × N + Kafka / RocketMQ | 无同步调用，纯异步 |

基础设施按需启用：

- **可观测性** — Grafana / Prometheus / Loki / Jaeger / ELK / SkyWalking / Tempo（共 7 项）
- **数据层** — Redis / MongoDB / ES / MySQL / PostgreSQL / Kafka / RocketMQ / RabbitMQ / ClickHouse（共 9 项）
- **技术栈** — Go / Java / PHP / Python / Node（React / Vue / Next.js / Nuxt）

### 支持的配置源

| 配置源 | `config_center.type` | 排障链路 |
|---|---|---|
| Nacos / Apollo / Consul | `nacos` / `apollo` / `consul` | MCP 或 HTTP 脚本读配置 → 解析连接串 → 查数据层 |
| Kubernetes ConfigMap / Secret | `kubernetes` | kubectl get → 解析连接串 → 查数据层 |
| 纯环境变量 / .env 文件 | `env-vars` | 安装时直接填连接串 → 查数据层（跳过配置读取） |

### 不适用的场景

| 场景 | 原因 |
|---|---|
| Serverless / FaaS | 无长驻服务概念，排障模型不匹配 |
| 单体应用（非微服务） | 工具为多服务 × 多环境设计，单体用不上大部分能力 |

## 两种使用方式

### 方式 A：Web UI（推荐新用户）

**生产模式（单二进制，一条命令就能用）：**

```bash
make web && make build
./bin/factory serve --port 8080
```

`make web` 负责构建前端并拷到 embed 目标，`make build` 负责 go build。若要多平台交叉编译出 `dist/bin/factory-<os>-<arch>`，用 `make release`。

或手动四步（不用 Makefile 时）：

```bash
cd web && npm install && npm run build
cd ..
rm -rf internal/webui/dist/assets internal/webui/dist/index.html
cp -R web/dist/* internal/webui/dist/
go build -o bin/factory ./cmd/factory
./bin/factory serve --port 8080
```

启动后浏览器访问 `http://localhost:8080` 就能看到前端，同一进程也提供 `/api/*`。

未执行前端构建时 `serve` 仍可启动，首页会显示"请先构建前端"占位提示（`/api` 仍可用）。

**开发模式（两个进程、热重载）：**

```bash
# 终端 1：Go API
go build -o bin/factory ./cmd/factory
./bin/factory serve --port 8080

# 终端 2：Vite 前端（自动把 /api 代理到 :8080）
cd web && npm install && npm run dev
```

8 个可视化页面覆盖完整工作流：

| 页面 | 功能 |
|---|---|
| Home 首页 | 概览 + 推荐下一步（基于本地草稿智能提示）+ 工作流卡片 + CLI 速查 |
| Init 向导 | 7 步表单生成 system.yaml；关键字段带 ? tooltip；支持导入已有 yaml 继续编辑；草稿持久化到 localStorage，切页面 / 刷新都不丢 |
| System 编辑器 | YAML 在线编辑 + 一键验证 / Plan / Gen |
| 系统分析 | 输入 repos 路径 → 展示每 repo 的 findings |
| Plan 预览 | skills badge + files 计数 + config-map 投影 |
| 生成执行 | 数字仪表盘 + 成功 / 失败状态 + 多 target 产物清单 |
| Doctor 诊断 | 彩色 issue 卡片 + 每条带 "↳ 建议" 修复动作 |
| Diff 对比 | 文件变更列表 + config-map 行级 diff |

### 方式 B：CLI（脚本化 / CI 集成）

```bash
go build -o bin/factory ./cmd/factory
# 或在仓库根 `go install ./cmd/factory` 装到 $GOBIN/factory

# 1. 交互向导生成 system.yaml（支持 -i 从已有 yaml 预填；Ctrl+C 会把草稿保存到 ~/.factory/init-draft.yaml）
./bin/factory init -o system.yaml                   # 从零开始
./bin/factory init -i existing.yaml -o new.yaml     # 改动哪里就回答哪里，其余回车接受

# 2. 校验
./bin/factory validate -i system.yaml

# 3. 分析仓库（可选 --auto-clone 自动浅克隆缺失仓库）
./bin/factory analyze -i system.yaml \
  --repos-root ./repos --auto-clone -o analysis.json

# 4. 预览将生成什么（不落盘）
./bin/factory plan -i system.yaml --analysis analysis.json

# 5. 生成部署包
./bin/factory gen -i system.yaml --analysis analysis.json

# 6. 部署到 OpenClaw
cd dist/<system-id> && bash scripts/install.sh && bash scripts/self-test.sh
# install.sh 首次运行会交互收集凭证，保存到 scripts/.env（0600）；第二次重跑自动复用、不再问。
# 想改某个凭证：编辑 scripts/.env 后重跑；想重新问：rm scripts/.env && bash install.sh。
```

每个命令成功后 CLI 会打印「下一步：xxx」提示，不再让你猜下一条命令是什么。
`factory`（无参）打印欢迎页；`factory --version` 打印版本（可用 `-ldflags "-X main.version=..."` 注入）。

### 多目标输出

`system.yaml` 的 `generation.targets` 支持同时生成多种格式：

```yaml
generation:
  targets:
    - openclaw       # OpenClaw 安装包（install.sh + workspace）
    - claude-code    # Claude Code（CLAUDE.md + skills/）
    - cursor         # Cursor IDE（.cursorrules + .cursor/rules/*.mdc）
    - standalone     # 独立 Web 聊天（server.py + index.html + docker-compose）
```

一次生成四种格式：

```bash
./bin/factory gen -i system.yaml
# → dist/<id>/             OpenClaw 安装包
# → dist/<id>-claude-code/ Claude Code
# → dist/<id>-cursor/      Cursor IDE
# → dist/<id>-standalone/  独立 Web 聊天
```

| 目标 | 核心产物 | 一键部署 |
|---|---|---|
| `openclaw` | install.sh + self-test.sh + workspace 模板 | `bash install.sh` → 部署到 OpenClaw |
| `claude-code` | CLAUDE.md + skills/ + **install.sh** | `bash install.sh <project-dir>`（自动备份已存在的 CLAUDE.md） |
| `cursor` | .cursorrules + .cursor/rules/*.mdc + skills/ + **install.sh** | `bash install.sh <project-dir>`（自动备份已存在的 .cursorrules） |
| `standalone` | server.py + index.html + Dockerfile + requirements.txt + docker-compose + **install.sh** | 本机：`bash install.sh`；或容器：`docker compose up --build` |

standalone 模式不依赖任何 AI 平台——只需一个 LLM API key（Claude / OpenAI），自带聊天界面和 Docker 部署。每个 target 的 `install.sh` 末尾都会打印具体的「首次对话」指引 + 3 条可粘贴的示例问题。

所有 CLI 命令都支持 `--format=json`，便于 CI/CD 管道消费。

每份产物目录都自带 `README.md`（能力清单 / 部署前凭证 / 常见问题 / 升级卸载），部署前 `cat README.md` 就有路标。

### Standalone 聊天前端的 UX（仅 standalone target）

`standalone/index.html` 是 factory 唯一直接管到 UI 层的部署形态，自带这些体验：

- **SSE 流式输出** — 后端 `server.py` 用 `anthropic.messages.stream` 逐 token 推送，前端边收边渲染
- **Markdown 渲染** — 无外部依赖的最小 parser：代码块（带「复制」按钮）/ inline code / bold / 有序无序列表 / 链接 / heading；XSS 已转义
- **对话历史持久化** — `localStorage` 按 `system.id` 分 key 保存 messages；刷新 / 关闭浏览器再打开不丢
- **流式中止** — 发送按钮在流式期间变红色「停止」，点击调 `AbortController.abort()`，已收到的内容保留并标记 `_[已停止]_`
- **清空对话** — 顶部一键重置当前系统的历史
- **默认环境下拉** — 从 `system.yaml` 的 environments 动态渲染；选定后以 `X-Default-Env` 头注入到 system prompt（服务端白名单校验防注入），机器人不用每次反问"哪个环境"

### 机器人回答质量的通用增强（4 target 共享）

- **首次打招呼自我介绍** — 用户说 hi / 你好 / ping / 问"你能干什么"时，机器人按 AGENTS.md 里的模板**动态列出自己的 skill 清单**（从 `skills_whitelist` + `infrastructure` 派生）+ 3 条可直接粘贴的示例问题
- **错误应对话术** — AGENTS.md 针对 MCP 连不上 / K8s RBAC / 凭证失效 / 配置 key 缺失 / 写操作被拒 / Grafana 403 / trace 找不到 等 7 类常见错误定义了标准话术（发生了什么 / 可能原因按概率 / 用户下一步做什么）
- **脚本 actionable 错误** — `config-executor/scripts/*.py`（resolve_runtime_static / _k8s / apollo / consul / nacos）顶层 try/except，stdout 输出结构化 JSON `{"error":..., "hint":"改 scripts/.env 的 X / 重跑 install.sh / ..."}`，机器人直接复述 hint 给用户，不糊 Python 堆栈
- **每个 skill 自检示例** — 每份 `SKILL.md` 末尾带「自检示例」段（5 个核心 skill 手写具体 case，其余 11 个通用模板），既给 LLM 参考也给维护者当 smoke test

## 子命令一览

| 命令 | 功能 | 常用场景 |
|---|---|---|
| `init` | 交互式向导生成 `system.yaml` | 新系统首次接入 |
| `validate` | 校验 `system.yaml` 语法与字段完整性 | 写完 yaml 后 |
| `analyze` | 扫描代码仓库，抽取 service_names 与配置中心线索 | 生成前补齐映射 |
| `doctor` | 对比声明 vs 代码实态，报告漂移 | 定期体检 |
| `plan` | 干跑 gen，展示 skills / files / overrides / config-map 分布 | gen 前预览 |
| `watch` | 文件变化时自动重跑 plan | 开发时持续反馈 |
| `gen` | 实际生成部署包（自带 preserve 保护人工行） | 正式落盘 |
| `diff` | 精确到文件 + 行级的新旧产物对比 | review 变更 |
| `upgrade` | 备份 + 重 gen + diff 一把过 | factory 版本升级后 |
| `serve` | 启动 Web UI（HTTP API + 前端界面） | 可视化操作 |
| `demo` | 零配置试跑：用内置 examples 走一遍 validate → plan → gen | 第一次体验 factory |
| `skill new` | 在模板库里脚手架新 skill | 扩展 agent 能力 |

## 配置源支持

| 类型 | analyzer 抽取 | 运行时后端 | config-map 字段 |
|---|---|---|---|
| `nacos` | YAML + properties + .env | MCP（`nacos-mcp-router`） | `namespaceId` / `group` / `dataId` / `mcp_server` |
| `apollo` | YAML + properties + .env | HTTP 脚本（`apollo_config.py`） | `appId` / `cluster` / `namespaces` / `meta` / `mcp_server` |
| `consul` | YAML + properties + .env | HTTP 脚本（`consul_config.py`） | `kv_prefix` / `default_context` / `host` / `mcp_server` |
| `env-vars` | .env | 安装时直接填连接串（`resolve_runtime_static.py`） | — |
| `kubernetes` | .env + YAML | kubectl（`resolve_runtime_k8s.py`） | — |

Nacos 生态有成熟 MCP 包，直接注册到 OpenClaw；Apollo / Consul 生态暂无可信 MCP，改用 Python 脚本通过官方 HTTP API 直连（零外部依赖，仅标准库）。

## 可观测性支持（7 项）

| 工具 | skill 名 | 查询方式 | 排障用途 |
|---|---|---|---|
| Grafana | 内置 MCP（per env） | 仪表盘面板查询 | 指标巡检 / 错误率趋势 |
| Prometheus | via Grafana MCP | PromQL | 实时指标 / 告警阈值验证 |
| Loki | via Grafana MCP | LogQL | 日志搜索（方案 A） |
| ELK | `elk-log-query` | ES `_search` API + Kibana | 日志搜索（方案 B，可与 Loki 共存） |
| Jaeger | `tracing-query` | Jaeger HTTP API | 链路追踪：trace ID → span 树 |
| SkyWalking | `skywalking-query` | GraphQL API | APM：服务拓扑 + trace + 慢端点 |
| Tempo | `tempo-query` | Tempo HTTP API / Grafana proxy | 链路追踪（Grafana 生态替代 Jaeger） |

## 数据层支持（9 项）

| 组件 | skill 名 | 查询方式 | 排障用途 |
|---|---|---|---|
| Redis | `redis-runtime-query` | mcporter MCP（只读） | 缓存 key / TTL / 值 |
| MongoDB | `mongodb-runtime-query` | mcporter MCP（只读） | query / aggregate / count |
| Elasticsearch | `es-runtime-query` | mcporter MCP（只读） | 索引 / DSL / 命中 |
| MySQL | `mysql-runtime-query` | mcporter MCP（只读 SELECT） | 数据一致性 / 慢查询 |
| PostgreSQL | `postgresql-runtime-query` | mcporter / psql CLI（只读） | pg_stat / 连接数 / 表大小 |
| Kafka | `kafka-runtime-query` | kafka CLI | topic / 消费积压 / 死信 |
| RocketMQ | `rocketmq-runtime-query` | mqadmin CLI | topic / consumer / 积压 / DLQ |
| RabbitMQ | `rabbitmq-runtime-query` | HTTP Management API | queue / exchange / 消息数 |
| ClickHouse | `clickhouse-runtime-query` | clickhouse-client / HTTP API（readonly=1） | OLAP 查询 / 分区 / 慢查询日志 |

> 所有数据层 skill 严格只读。用户按需在 `skills_whitelist` 中选择，未列入的不会生成。

## 多环境 MCP

每个 env 注册独立的 MCP 实例（如 `nacos-mcp-server-dev`、`nacos-mcp-server-prod`），agent 通过 `config-map.yaml` 的 `mcp_server` 字段选择正确实例，不需要人工切换。

`per_env_credentials: true` 可进一步让每个 env 使用独立用户名 / 密码 / token（默认 `false` = 共用凭证）。

## 技术栈分析器（5 栈）

| 栈 | 识别来源 | 框架检测 | 配置扫描 |
|---|---|---|---|
| `go` | `go.mod` | — | YAML |
| `java` | `pom.xml` | Spring profile | YAML + properties |
| `node` | `package.json` | Next.js / Nuxt / Vite / CRA / Vue CLI / Angular | `.env.*`（提取 API URL） |
| `php` | `composer.json` | — | `.env.*` + YAML |
| `python` | `pyproject.toml` / `setup.py` | Django / FastAPI / Flask / Tornado / Sanic | `.env.*` + YAML |

`analyze --auto-clone` 会自动浅克隆缺失仓库（需 git + 凭证），`--branch` 可指定分支。

## Doctor 漂移检测（8 种规则）

| 规则 | 级别 | 说明 |
|---|---|---|
| `missing-repo` | error | repos-root 下找不到仓库 |
| `origin-mismatch` | warning | 仓库 git origin 与声明 URL 不符（跨 ssh / https 智能归一化） |
| `stack-mismatch` | warning | go.mod / pom.xml 暗示的 stack 与声明不一致 |
| `service-drift` | warning / info | 声明的 service 未在代码中检测到，或代码中多出未声明 service |
| `config-center-drift` | warning | 代码里的配置中心类型与声明不符 |
| `config-center-unused` | warning | 声明了但所有仓库都没用到 |
| `data-store-unused` | info | 启用了但关键字未出现在 findings |
| `undeclared-env-profile` | info | 代码里的 profile 名未在 environments 中声明 |

每条 issue 都带 `↳ 建议:` 可执行修复动作（改哪一行 / 跑哪条命令）；`factory doctor` 末尾按 error / warning / info 分级给"下一步"。

对机器可处理的 issue（`stack-mismatch` / `config-center-unused` 等），可以走 `--fix` 让 factory 自动改 yaml：

```bash
factory doctor -i system.yaml --repos-root ./repos --fix        # 显示 patch 列表 + 询问确认
factory doctor -i system.yaml --repos-root ./repos --fix -y     # 跳过确认直接写回（CI 模式）
```

`--fix` 走**行级精确替换**：仅改目标行，其他行 bit-perfect 保留（空行 / 注释 / 缩进都不动），写回前自动备份到 `system.yaml.bak.<ts>`。

## Preserve / Diff / Upgrade 生命周期

- 手改 `config-map.yaml` 的 inferred 行为 verified（不带 `source:` 字段），下次 `gen` 自动保留
- `generation.preserve_on_regenerate` 列表中的文件（如 `SOUL.md`）re-gen 时整体保留
- 优先级：analyzer finding > prior manual override > inferred skeleton
- 切换 config_center 类型会自动丢弃旧 overrides，避免字段错配
- `factory diff` 模拟"带 preserve 的 gen"，精确展示真实会发生的变化
- `factory upgrade` = 自动备份到 `<out>.bak.<ts>` + gen + diff 一步到位

## Skill 脚手架

```bash
factory skill new payment-check --description "支付链路排障" --with-scripts
# → templates/workspace/skills/payment-check/
#     SKILL.md.tmpl   （含 TODO 骨架：执行流程 / 输入 / 输出 / 硬约束）
#     scripts/README.md

# 加入 system.yaml 白名单后即可生效
factory plan -i system.yaml   # 确认新 skill 出现在 "Skills included"
factory gen -i system.yaml
```

## 构建与发布

仓库根 `Makefile` 把构建流程收敛成几条命令：

```bash
make              # == make build：go build 到 bin/factory，版本号从 git tag 读
make web          # npm run build → 拷到 internal/webui/dist/（embed 目标）
make release      # web + 多平台交叉编译（darwin/linux × amd64/arm64）到 dist/bin/
make demo         # build 后立即 ./bin/factory demo
make test         # go test -race -cover ./...
make lint         # go vet + gofmt -l + vue-tsc --noEmit
make clean        # 清 bin/ dist/bin/ 和 embed 的 dist 目标
```

版本号自动注入：`git describe --tags --abbrev=0` + `git rev-parse --short HEAD` → `factory --version` 打印 `factory v0.2.0 (abcdef)`。

`templates/` 和 `examples/` 通过仓库根的 `embed.go` 用 `//go:embed` 打进二进制 —— 这样 `go install` 出来的 factory 在任何目录都能跑 demo / gen，无需 clone 仓库。运行时优先用磁盘上的 `templates/` / `examples/`（存在则用），否则从 embed 解压到 `os.TempDir()` 复用。

## 测试与 CI

本地预演（与 CI 一致）：

```bash
make lint                      # go vet + gofmt -l + vue-tsc
make test                      # 全量 + 竞态（~4s）
```

CI 流水线 `.github/workflows/ci.yml` 在每次 push / PR 上自动运行。

覆盖率（最新）：analyzer 82% / config 85% / doctor 79% / generator 78% / gitclone 93% / initwizard 87% / skillscaffold 79% / upgrade 76% / watcher 74%。
最关键的 yaml-patching 路径（`doctor.Fixer`）有行级 bit-perfect 保留测试：写回后只有目标行变，其他所有行 / 注释 / 空行不动。

## 目录结构

```
embed.go                    # 根 package，把 templates/ + examples/ 通过 go:embed 打进二进制
Makefile                    # make build / web / release / demo / test / lint / clean
cmd/factory/                # CLI 入口（12 个子命令：init / validate / analyze / doctor /
                            #   plan / watch / gen / diff / upgrade / serve / demo / skill）
api/                        # REST API（handler + router，包装 internal 包）
web/                        # 前端（Vue 3 + Vite + TypeScript，8 个页面含 Home dashboard）
internal/
  config/                   # system.yaml 加载与校验
  analyzer/                 # 仓库扫描：Go / Java / Node / PHP / Python × 5 种配置源
  generator/                # 模板渲染 + preserve + diff + plan（多 target 共享 staging）
  doctor/                   # 声明 vs 实态漂移检测 + actionable --fix（yaml 行级 bit-perfect 替换）
  upgrade/                  # 备份 + 重 gen + diff
  gitclone/                 # git clone + ReadOrigin + CanonicalURL
  initwizard/               # 交互向导 → system.yaml（-i 预填 / Ctrl+C 保草稿 / 输出带行内注释）
  skillscaffold/            # skill new 脚手架
  watcher/                  # 文件变化轮询（零外部依赖）
  webui/                    # 前端 dist 的 go:embed 入口（单二进制 serve）
templates/                  # .tmpl 模板；按 target 分组：
                            #   workspace/  scripts/  claude-code/  cursor/  standalone/
examples/                   # system.yaml 示例 × 7 + fake repos × 5
schema/system.schema.yaml   # 带完整注释的 schema 参考
assets/                     # logo.svg + architecture.svg
.github/workflows/ci.yml    # CI 门禁
```

## 已知限制

- Apollo / Consul 走 HTTP 脚本而非 MCP（生态尚无稳定 MCP 包；Nacos 走 MCP）
- Node 生态不扫配置中心（极少用 Nacos / Apollo / Consul）
- 不自动推断服务调用拓扑（只做 per-repo 机械抽取）
- `factory init` 暂不生成 `per_env_credentials`、`dataid_patterns` 等高级字段（高级用户手工补）
