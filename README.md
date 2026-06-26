<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

AI 排障机器人工作台。用 `troubleshooter.yaml` 描述一个微服务系统，生成可安装到 OpenClaw、Claude Code、Cursor、Codex CLI 的排障机器人。

## 项目模型

| 层级 | 说明 |
|---|---|
| 本仓库 | 研制环境：CLI、桌面 app、HTTP server 三入口共享 `internal/`，负责建模、扫描、校验、生成、部署 |
| 产出物 | 独立运行的排障机器人：skills、MCP、路由表、故障话术，安装后脱离 studio 使用 |

## 从哪里开始

| 目标 | 入口 |
|---|---|
| 第一次跑通 | [下载与安装](#下载与安装) → [入口](#入口) → [部署目标](#部署目标) |
| 看机器人能力 | [机器人能力](#机器人能力) → [排障链路](docs/troubleshooting-flow.md) |
| 建模新系统 | [适配范围](#适配范围) → [Monorepo / Umbrella](#monorepo--umbrella) → [示例](examples/shop-troubleshooter.yaml) |
| 维护代码 | [贡献指南](CONTRIBUTING.md) → [决策记录](docs/decisions.md) |
| 配 CI / 发版 | [CI / Release](docs/CI-RELEASE.md) |

## 入口

| 入口 | 用途 |
|---|---|
| 桌面 app | 推荐给个人使用；覆盖建模、扫描、部署、已装管理、工作目录浏览 |
| CLI `tshoot` | 推荐给脚本、SSH、CI；覆盖 yaml 计算和 4 平台安装 |

## 部署目标

`generation.targets` 决定安装到哪些平台。

| 平台 | 部署位置 | 使用方式 | MCP 配置 |
|---|---|---|---|
| OpenClaw | `~/.openclaw/workspace/<name>/` | 客户端 agent 列表选择，安装后需重启客户端 | `~/.openclaw/openclaw.json` |
| Claude Code | `~/.claude/agents/<name>.md` + `~/.claude/skills/<name>/` | 项目内 `@<name>` 调 subagent | `~/.claude.json` |
| Cursor | `~/.cursor/agents/<name>.md` + `~/.cursor/skills/<name>/` | AI 侧栏选 Custom Agent，MCP 需在 Settings 启用 | `~/.cursor/mcp.json` |
| Codex CLI | `~/.codex/agents/<name>.toml` + `~/.codex/skills/<name>/` | 在 `codex` 中用自然语言派生 subagent | agent toml 内联 `[mcp_servers.*]` |

凭据位置：

- OpenClaw：`~/.openclaw/<id>-creds.json`
- Claude Code / Cursor / Codex：`~/.tshoot/<id>-creds.json`

Codex 需要网络访问时，安装流程会自动 patch `~/.codex/config.toml` 的 `[sandbox_workspace_write].network_access` 并备份原文件。

## 下载与安装

Release 同步发布到 GitHub 和 GitLab，任选一个源。

### 桌面 app，macOS 推荐安装

```bash
# GitLab 源，最新版
curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

# 私有 GitLab 项目
export GITLAB_TOKEN=glpat-xxx
curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

# GitHub 源
curl -fsSL https://raw.githubusercontent.com/452562082/troubleshooter-studio/main/scripts/install.sh | SOURCE=github bash

# 指定版本
curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | VERSION=v0.9.23 bash
curl -fsSL https://raw.githubusercontent.com/452562082/troubleshooter-studio/main/scripts/install.sh | SOURCE=github VERSION=v0.9.23 bash
```

脚本会下载 dmg、安装到 `/Applications/`、清理 quarantine 并启动应用。Release 页：

- [GitHub Releases](https://github.com/452562082/troubleshooter-studio/releases)
- [GitLab Releases](https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/releases)

### 桌面 app，手动安装 dmg

1. 下载 `TroubleshooterStudio-vX.Y.Z.dmg.zip`
2. 用 macOS 自带 Archive Utility 解压
3. 打开 `.dmg`，把 `.app` 拖到 `Applications`
4. 首次打开若提示“已损坏”，运行 dmg 内的解锁脚本，或执行：

```bash
xattr -d com.apple.quarantine /Applications/TroubleshooterStudio.app
```

### CLI

下载 `tshoot-vX.Y.Z-<os>-<arch>`，Windows 版本自带 `.exe`。

```bash
# macOS / Linux
chmod +x tshoot-vX.Y.Z-darwin-arm64
sudo mv tshoot-vX.Y.Z-darwin-arm64 /usr/local/bin/tshoot
tshoot --help
```

```powershell
# Windows PowerShell
Move-Item tshoot-vX.Y.Z-windows-amd64.exe C:\Users\<you>\bin\tshoot.exe
tshoot --help
```

## 从源码构建

```bash
git clone <repo> && cd troubleshooter-studio
```

桌面 app：

```bash
xcode-select --install
brew install go node
make desktop-app
open dist/TroubleshooterStudio.app
```

CLI：

```bash
make
./bin/tshoot demo
./bin/tshoot init -o troubleshooter.yaml
./bin/tshoot gen -i troubleshooter.yaml -o ./out
./bin/tshoot install --path ./out --target openclaw
```

Linux / Windows 当前只支持 CLI。`make wails-gen`、`make icon` 是贡献者任务。

## 适配范围

<p align="center">
  <img src="assets/architecture.svg" alt="适配架构" width="900"/>
</p>

| 类型 | 支持 |
|---|---|
| 服务角色 | frontend、gateway、backend、middleware、admin、mobile、common-lib、infra、docs |
| 可观测性 | Grafana、Prometheus、Loki、Jaeger、Tempo、ELK、SkyWalking、Kuboard/K8s |
| 数据层 | Redis、MongoDB、Elasticsearch、MySQL、PostgreSQL、Kafka、RabbitMQ、ClickHouse |
| 配置源 | Nacos、Apollo、Consul、Kuboard、Kubernetes ConfigMap、环境变量 |
| 技术栈 | Go、Java、PHP、Python、Node/React/Vue/Next.js/Nuxt |

不适用：Serverless / FaaS、单体应用。

## Monorepo / Umbrella

子服务以 git submodule 挂在 umbrella 仓库下时，用 `parent_repo` + `parent_path` 建模：

```yaml
repos:
  - name: platform
    url: https://git.example.com/org/platform.git
    role: backend
  - name: payments
    url: https://git.example.com/org/payments.git
    parent_repo: platform
    parent_path: services/payments
    role: backend
```

工作台会按 umbrella pin 的 commit 分析子模块，避免把子模块 main HEAD 当成生产代码。

## 桌面 app 页面

| 页面 | 用途 |
|---|---|
| 首页 | 概览和下一步建议 |
| 已装机器人 | 诊断、编辑 yaml、预演、应用、浏览目录、重新生成、卸载 |
| 创建向导 | 10 步表单生成 `troubleshooter.yaml` 并部署 |
| YAML 沙盒 | 校验、健康检查、生成计划、产物预览 |
| 代码扫描 | 反推服务名、配置中心、依赖图、数据 schema |
| 日志 | install、analyze、系统事件日志 |

## CLI 子命令

| 命令 | 功能 |
|---|---|
| `init` | 交互生成 `troubleshooter.yaml` |
| `validate` | 校验 yaml |
| `analyze` | 扫代码，抽取服务、配置中心、依赖图、schema |
| `plan` / `diff` / `watch` | 干跑、diff、文件变化重跑 |
| `gen` | 生成 staging 产物 |
| `install` / `self-test` / `uninstall` | 安装、自检、卸载 |
| `discover` | 扫本机已装机器人 |
| `apply` | 用新 yaml 原地更新已装机器人 |
| `upgrade` | 备份、重生成、diff |
| `doctor` | 声明与代码实态漂移检测，支持 `--fix` |
| `demo` | 零配置试跑 |
| `skill new` | 新建 skill 模板 |

典型流程：

```bash
./bin/tshoot init -o troubleshooter.yaml
./bin/tshoot validate -i troubleshooter.yaml
./bin/tshoot analyze -i troubleshooter.yaml --repos-root ./repos -o analysis.json
./bin/tshoot gen -i troubleshooter.yaml --analysis analysis.json
./bin/tshoot install --path dist/<id> --target openclaw
```

## 机器人能力

生成的 skill 集合按 yaml 裁剪。真源在 [templates/workspace/skills](templates/workspace/skills)，部署后以产物 `AGENTS.md` 为准。

| 能力 | skill |
|---|---|
| 路由 | `routing`：env 到域名、分支、配置、日志 app、MCP、依赖图、schema 的映射 |
| 主流程 | `incident-investigator`：症状、时间轴、横向、纵向、多向交叉、根因、沉淀 |
| 最近变更 | `recent-changes`：K8s rollout、配置 history、git log 聚合 |
| 配置中心 | `config-executor`：Nacos、Apollo、Consul、Kuboard、Kubernetes ConfigMap、环境变量 |
| 可观测性 | `k8s-runtime-query`、`tracing-query`、`tempo-query`、`skywalking-query`、`elk-log-query` |
| 数据层 | `redis`、`mongodb`、`es`、`mysql`、`postgresql`、`kafka`、`rabbitmq`、`clickhouse` runtime query |
| 图表 | `diagram-generator`：Mermaid 转 PNG/SVG |

Nacos 当前走自研本地 MCP：`nacos_mcp.py` 在运行时登录并刷新 token，MCP 不可用时回落 HTTP 脚本。`config_centers.endpoints[].addr` 必须填 API 端口，默认 `:8848`，不要填 dashboard/UI 端口。

## Doctor 漂移检测

`tshoot doctor` 检查声明与代码实态不一致：

- `missing-repo`
- `origin-mismatch`
- `stack-mismatch`
- `service-drift`
- `config-center-drift`
- `config-center-unused`
- `data-store-unused`
- `undeclared-env-profile`

可自动修复的 issue 用 `--fix` 行级替换，并备份到 `troubleshooter.yaml.bak.<ts>`。

## 常用构建命令

```bash
make                 # CLI: bin/tshoot
make web             # 前端 dist -> internal/webui/dist/
make desktop-app     # macOS .app
make desktop-dmg     # 分发 dmg
make desktop         # 桌面裸二进制
make release         # 多平台 CLI 二进制
make release-notes   # dry-run changelog
make test            # go test -race -cover ./...
make lint            # go vet + gofmt + vue-tsc
make clean           # 清理 bin/ 和 dist/
```

发版走 CI，见 [docs/CI-RELEASE.md](docs/CI-RELEASE.md)。

## 目录结构

```text
cmd/tshoot/             CLI 入口
cmd/tshoot-desktop/     Wails 桌面 app
api/                    HTTP handler
web/                    Vue 3 + Vite 前端
internal/config/        yaml schema 与校验
internal/analyzer*/     仓库扫描与分析 pipeline
internal/generator/     模板渲染、diff、plan、IDE agent 生成
internal/agent/         install、self-test、uninstall、MCP、creds
internal/doctor/        漂移检测
internal/upgrade/       备份、重生成、diff
internal/cchub/         配置中心客户端
internal/dsprobe/       数据层连通性探测
internal/labelprobe/    Loki label 探测
internal/openclaw/      OpenClaw 探测
internal/aitools/       Claude Code / Cursor / Codex 探测
internal/mcpcfg/        MCP 配置生成
internal/skillscaffold/ skill 脚手架
templates/              机器人 workspace 模板
examples/               示例 yaml 与 fake repos
schema/                 troubleshooter schema
```

## 已知限制

- macOS 桌面 app 未签名/公证，首次打开需清 quarantine。
- 代码扫描依赖模式识别，配置驱动、注解驱动、自定义包装层重的项目需要手补。
- downstream 识别覆盖 HTTP、gRPC、服务发现工厂、Java `@FeignClient`、Python `requests/httpx`；配置文件驱动 RPC 需要手补。
- schema 识别覆盖主流 ORM；裸 SQL、冷门 ORM、自定义命名约定需要手补。
- 识别精度参考：Go 70-80%，Java 60-70%，Python 60%，Node 50%。
