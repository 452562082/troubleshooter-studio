# 贡献指南

本项目会生成真实给开发/SRE 使用的排障机器人。改动前先看 [docs/decisions.md](docs/decisions.md)，尤其是 MCP 接入、软约束、self-test probe 的决策。

## 提交前检查

```bash
make test   # go test ./... -race
make lint   # go vet + gofmt -l + vue-tsc
make build  # 至少能编出 bin/tshoot
```

不要用 `git add -A`。`internal/webui/dist/.gitkeep` 是 `go:embed all:dist` 的占位文件，web build 会清理 dist；`git add -A` 容易把 `.gitkeep` 的删除误提交。改用：

```bash
git add <具体文件>
git status --short
```

## 目录速查

| 路径 | 用途 | 改动注意 |
|---|---|---|
| `cmd/tshoot/` | CLI 入口 | 改 CLI 行为时同步 README |
| `cmd/tshoot-desktop/` | Wails 桌面 app 入口 | 改 binding 后跑 `make wails-gen` |
| `api/` | HTTP handler | 改 router/handler 加 `api/handler_test.go` |
| `internal/agent/` | install、self-test、MCP builder | 改 MCP 必看 decisions 里的软约束和 probe |
| `internal/generator/` | yaml 到 workspace 渲染 | 改模板跑 generator 测试 |
| `internal/config/` | yaml schema 与校验 | 改 schema 同步 `schema/` 和 examples |
| `internal/cchub/` | 配置中心客户端 | nacos 逻辑看 plan D 决策 |
| `internal/doctor/` | 漂移检测 | 新规则放 `internal/doctor/rules/` 并加测试 |
| `templates/workspace/skills/` | 生成机器人的 skill 模板 | 工具名以 runtime probe 为准 |
| `docs/decisions.md` | ADR 记录 | 大重构、砍能力、换包只追加新条目 |

## 常见改动流程

### 加 MCP 接入

1. 在对应 builder 文件加函数：
   - 数据层：`internal/agent/install_native_mcp_data_stores.go`
   - 可观测性：`internal/agent/install_native_mcp_obs.go`
   - 消息/协作：`internal/agent/install_native_mcp_messaging.go`
2. 起 runtime probe，拿真实 `tools/list`。不要按 README 猜工具名。
3. 更新对应 `templates/workspace/skills/<x>-runtime-query/SKILL.md.tmpl`，写清只读主路径和写工具软约束。
4. 同步 `requiredMCPKeys` 期望清单。

CodeGraph 还必须固定 release version 和逐平台 SHA256；升级前先运行 `scripts/test-codegraph-smoke.sh`。runtime probe 必须确认 `tools/list` 含 `codegraph_explore`；索引 probe 与 MCP probe 分开，源码仓库要求验证 `complete/fileCount/nodeCount`。普通单测使用 fake CLI，禁止隐式联网下载。
5. 测试：

```bash
go test ./internal/agent/ -run TestBuildMCPServers
go test ./internal/agent/ -run TestSelfTestOpenclaw
```

### 加 SKILL

```bash
tshoot skill new <name>
```

然后补：

- `templates/workspace/skills/<name>/SKILL.md.tmpl`
- frontmatter `description:`，LLM 靠它决定是否调用
- 生成/裁剪相关测试

### 改 yaml schema

1. 改 `schema/troubleshooter.schema.yaml`
2. 改 `internal/config/` 解析和校验
3. 改 `examples/*.yaml`
4. 改 `internal/generator/` 渲染
5. 必要时给 `tshoot doctor` 加漂移规则

### 删或砍能力

1. 在 `docs/decisions.md` 追加 ADR，不改旧条目；旧方案用 `SUPERSEDED` 指向新条目。
2. 不再使用的凭据，从 `install_prompts.go`、wizard、answers 渲染一起停收。
3. 若只是暂不接入，wizard 文案要明确“实验性/当前未实现”。

## MCP 决策底线

- MCP 层默认不硬禁写工具，靠 SKILL 软约束；已有上游 `--read-only` 的例外不主动移除。
- install 成功不代表 MCP 可用，必须以 self-test runtime probe 为准。
- builder skip 分两类：
  - 有 HTTP/API 替代且能力完整：凭据仍收，routing 用 `runtime: <type>-http`
  - 无替代且能力缺失：凭据停收，wizard 标实验性
- nacos 是例外：当前走自研本地 MCP `nacos_mcp.py`，routing 用 `runtime: nacos-mcp`，HTTP 脚本只做 fallback。

## 测试要求

底线：

```bash
go test ./...
```

建议全量：

```bash
go test ./... -race
scripts/check-go-coverage.sh
scripts/test-skill-scripts.sh
go test ./internal/analyzer ./internal/analyzerpipe ./internal/generator
go test ./internal/generator -run TestGenerate_Nacos_Shop
make audit
```

`scripts/test-skill-scripts.sh` 需要 `pytest`、`PyYAML` 和 `uv`（Nacos MCP 专项测试用 `uv` 隔离安装 `mcp/httpx/respx/pytest-asyncio`）。真实 Playwright 采集链路不进默认测试，本地手动跑：

```bash
npm install --no-save --no-package-lock playwright
npx playwright install chromium
scripts/test-browser-smoke.sh
```

覆盖率门槛：

| 包 | 门槛 |
|---|---|
| `cmd/tshoot/` | 0.7% |
| `internal/agent/` | 60% |
| `internal/analyzer/` | 33% |
| `internal/analyzerpipe/` | 40% |
| `internal/generator/` | 65% |
| `internal/deploy/` | 80% |
| `internal/dsprobe/` | 9% |
| `internal/doctor/` | 70% |
| `internal/userconfig/` | 22% |
| `api/` | 50% |

`cmd/tshoot`、`internal/analyzer*`、`internal/dsprobe`、`internal/userconfig` 当前是非回归底线，先防止继续下降；后续补关键路径测试后再抬门槛。

新加路径要有 happy path。新加 MCP builder 要有注册类 positive test；禁用/跳过类要有 negative test。

持久化故障闭环的改动还必须满足：

- 状态增删或语义变化要更新完整 transition-table 测试，同时覆盖代表性非法跳转。
- 每个按钮、Agent 回调和外部副作用都要有幂等测试，证明重复请求不会重复 attempt、merge、push、部署观察或回归。
- 跨事务或进程边界的改动要覆盖 SQLite reopen/crash recovery，副作用阶段恢复前必须检查外部状态。
- Git 集成测试只使用 `t.TempDir()` 的本地仓库和 bare remote；可以用 test-only URL rewrite 模拟 SSH，禁止连接真实远端、force push 或修改开发者工作区。
- 部署测试只能使用 fake verifier、`httptest` 或 fake K8s reader；测试和 Studio 都不得执行真实应用部署。
- 证据、事件、迁移和 verifier 错误测试必须包含 token、Cookie、Authorization、password 和 URL userinfo 等脱敏 fixture，并断言数据库及 artifact 中不含原值。
- 完整闭环至少覆盖两次独立授权、部署版本 gate、新鲜回归证据、多仓库部分完成、重复通知以及重启后继续。

跨仓库服务拓扑还有以下回归要求：

- 每种新增语言或框架扫描器必须使用 `examples/fake-repos/` 下的离线 fixture，默认测试不得 clone、下载依赖或访问网络。
- 匹配结果必须给出稳定、可排序的确定性理由；禁止用 LLM 打分，也不要引入依赖遍历顺序或 map 顺序的随机性。
- happy path 之外必须覆盖单仓库扫描失败或缺少本地路径的 partial 结果，部署和生成仍应成功并在 endpoint evidence 中保留失败状态。
- `service-topology.yaml` 是正式图，`endpoint-evidence.yaml` 是完整证据；兼容用的 `service-dependency-map.yaml` 必须由同一正式图投影，并用测试防止 downstream 漂移。
- override 测试必须覆盖人工优先级、过期证据和 YAML 往返；缓存测试必须覆盖配置摘要与仓库 HEAD 变化。

## Commit Message

参考现有风格：

```text
fix nacos 接入彻底回归方案 B(HTTP API 主路径)
feat P1.1: MCP probe 工程化进 self_test_openclaw
refactor P1.3: 拆 install_native_mcp_common.go
docs P1.2: README TOC + docs/decisions.md 决策演进记录
```

要求：

- 前缀用 `fix` / `feat` / `refactor` / `docs`
- P1/P2/P3 可选
- subject 写清上下文和原因
- 事故复盘写进 body，不要只写“update”

## CI 注意

GitHub Actions 锁 Node 20.19.0（满足 Vite 8 的 Node 下限）。web job 用：

- `npm ci --ignore-scripts`
- `npm audit --audit-level=moderate`
- `vitest run --pool=forks`

这是为规避新 Node 与 esbuild/vitest 的兼容问题，见 `.github/workflows/ci.yml` 注释。

Go job 会安装固定版本的 `govulncheck` 并执行 `govulncheck ./...`；本地可用 `make audit` 复现同一类安全检查。

## License

Apache-2.0。
