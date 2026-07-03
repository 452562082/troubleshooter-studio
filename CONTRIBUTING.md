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
