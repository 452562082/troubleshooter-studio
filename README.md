<p align="center">
  <img src="assets/logo.svg" alt="troubleshooter-studio" width="560"/>
</p>

# troubleshooter-studio

用 `troubleshooter.yaml` 描述系统，生成可安装到 OpenClaw、Claude Code、Cursor 和 Codex CLI 的 AI 排障机器人。

| 层级 | 职责 |
|---|---|
| Studio | CLI、桌面 app、HTTP server；负责建模、扫描、校验、生成、部署和故障闭环 |
| 生成物 | 独立运行的 skills、MCP、路由和话术；安装后不依赖 Studio |

## 快速开始

macOS 推荐安装桌面版：

```bash
# GitLab
curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash

# GitHub
curl -fsSL https://raw.githubusercontent.com/452562082/troubleshooter-studio/main/scripts/install.sh | SOURCE=github bash
```

私有 GitLab 源设置 `GITLAB_TOKEN`；指定版本设置 `VERSION=vX.Y.Z`。发布页：[GitHub](https://github.com/452562082/troubleshooter-studio/releases) / [GitLab](https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/releases)。

CLI 从 Release 下载对应平台二进制后：

```bash
chmod +x tshoot-vX.Y.Z-darwin-arm64
sudo mv tshoot-vX.Y.Z-darwin-arm64 /usr/local/bin/tshoot
tshoot --help
```

典型 CLI 流程：

```bash
tshoot init -o troubleshooter.yaml
tshoot validate -i troubleshooter.yaml
tshoot analyze -i troubleshooter.yaml --repos-root ./repos -o analysis.json
tshoot gen -i troubleshooter.yaml --analysis analysis.json
tshoot install --path dist/<id> --target openclaw
```

## 使用入口

| 入口 | 适用场景 |
|---|---|
| 桌面 app | 推荐；建模、扫描、部署、机器人管理、Bug 工单和故障闭环 |
| `tshoot` CLI | 脚本、SSH、CI 和四平台安装 |
| `tshoot serve` | 本地轻量 Web UI；校验、计划、生成、doctor 和 schema |

桌面页面包括：首页、已装机器人、Bug 工单、故障闭环、创建向导、YAML 沙盒、代码扫描和日志。

## 部署目标

| 平台 | 安装位置 |
|---|---|
| OpenClaw | `~/.openclaw/workspace/<name>/` |
| Claude Code | `~/.claude/agents/`、`~/.claude/skills/` |
| Cursor | `~/.cursor/agents/`、`~/.cursor/skills/` |
| Codex CLI | `~/.codex/agents/`、`~/.codex/skills/` |

OpenClaw 凭据保存在 `~/.openclaw/<id>-creds.json`，其余平台保存在 `~/.tshoot/<id>-creds.json`。Studio 会安装共享 `tshoot-router`，按当前仓库路径和 Git remote 确定机器人；无法唯一归属时停止，不按故障关键词猜测。

## 故障闭环

Studio 用持久化 Case 编排：

```text
验证 → 方案评估 → 修复/人工处置 → 合并 → 人工部署 → 回归
```

- SQLite 是 Case、证据、授权和事件的真源。
- Agent 一次只执行一个阶段；合并由 Studio 执行，应用部署由人或外部平台执行。
- 代码修复和合并分别授权；回归失败在同一 Case 中进入下一轮排障。
- 验证与回归复用同一场景和浏览器协议，但回归必须采集新证据。
- 用户可以补证、重试、质疑根因、重评方案、重新修复或重置 Case。

详见[故障闭环与 Agent 工作流](docs/incident-workflow.md)。排障 Agent 的取证方法见[排障链路](docs/troubleshooting-flow.md)。

## 建模与能力

支持：

- 服务：frontend、gateway、backend、middleware、admin、mobile、common-lib、infra。
- 可观测性：Grafana、Prometheus、Loki、Jaeger、Tempo、ELK、SkyWalking、K8s。
- 数据层：Redis、MongoDB、Elasticsearch、MySQL、Doris、PostgreSQL、Kafka、RabbitMQ、ClickHouse。
- 配置源：Nacos、Apollo、Consul、K8s ConfigMap、One2All、环境变量。
- 技术栈：Go、Java、PHP、Python、Node、React、Vue、Next.js、Nuxt。

示例见 [examples](examples/)。资源目录规则见[创建向导资源目录](docs/resource-catalog.md)。

Monorepo 子服务使用 `parent_repo` 和 `parent_path`：

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

### CodeGraph

按需开启：

```yaml
code_intelligence:
  enabled: true
  provider: codegraph
```

CodeGraph 用于仓库内符号、调用关系和影响面查询；跨仓关系由服务拓扑提供。索引不可用或分支不一致时回退到 `rg` 和文件读取。

### 服务拓扑

多仓系统会扫描 HTTP、Feign 和 gRPC 端点。只有自动高置信关系或人工确认关系进入正式拓扑；候选、拒绝和过期关系不参与自动导航。人工决定写入 `service_topology.overrides`。

## 从源码构建

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
```

常用命令：

```bash
make test          # go test -race -cover ./...
make lint          # go vet + gofmt + vue-tsc
make web
make desktop-app
make release
```

Linux 和 Windows 当前只提供 CLI。

## CLI 速查

| 命令 | 用途 |
|---|---|
| `init` / `validate` / `analyze` | 创建、校验和扫描配置 |
| `plan` / `diff` / `watch` | 预演和持续检查 |
| `gen` / `install` / `apply` | 生成、安装和更新机器人 |
| `self-test` / `doctor` | 运行时自检和声明漂移检查 |
| `discover` / `upgrade` / `uninstall` | 管理已安装机器人 |
| `serve` | 启动轻量 HTTP API 和 Web UI |
| `skill new` | 创建 skill 模板 |

## 开发文档

| 文档 | 内容 |
|---|---|
| [CONTRIBUTING.md](CONTRIBUTING.md) | 开发流程和测试要求 |
| [docs/decisions.md](docs/decisions.md) | 不可改写的架构决策记录 |
| [docs/incident-workflow.md](docs/incident-workflow.md) | 故障闭环状态和边界 |
| [docs/troubleshooting-flow.md](docs/troubleshooting-flow.md) | 排障 Agent 七步流程 |
| [docs/CI-RELEASE.md](docs/CI-RELEASE.md) | CI 和发版 |

## 已知限制

- macOS app 未签名/公证，首次打开可能需要清除 quarantine。
- 代码、拓扑和 schema 扫描基于模式识别，复杂包装或冷门框架需要人工补充。
- Serverless / FaaS 暂无专用运行时适配，可通过 HTTP、日志或外部可观测性接入。
