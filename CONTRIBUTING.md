# 贡献指南

欢迎给 **troubleshooter-studio** 提 issue / merge request。本项目是「AI 排障机器人工作台」,改动会直接影响用户产出的机器人质量,所以**优先看一遍 [`docs/decisions.md`](docs/decisions.md)** 了解项目演进 + 设计哲学。

## 提交前必做

```bash
make test       # go test ./... -race(本项目要求 race-free)
make lint       # go vet + gofmt -l(diagnostic 风格问题不阻塞 commit,但 PR review 会指出)
make build      # 至少能编出 bin/tshoot
```

### git 操作 gotcha:**不要用 `git add -A`**

本仓库 `internal/webui/dist/` 是 npm build 产物目标 + 一个常驻 `.gitkeep`(让 `//go:embed all:dist` 在 fresh clone / CI 上有文件可匹配)。`.gitignore:24` 用 `/internal/webui/dist/*` + `!.gitkeep` 例外。

**但**:`.gitignore` 例外只防 `git add` 加 ignored 文件,**不防 `git add -A` 把已 tracked 文件的 delete 记下来**。`make desktop-app` 期间 web build 会清盘 + 重写 dist 产物,`.gitkeep` 短暂"消失"(实际是 mtime 变);此时若用 `git add -A`,git 把 `.gitkeep` 当 delete 记进 commit,CI fresh clone 跑 `go vet ./internal/webui/...` 就挂 `pattern all:dist: no matching files found`(本会话已踩 2 次:`7b38829` / `b3a5f98`)。

**改用**:`git add <具体路径>`,显式列你改的文件。如果非要全量,先 `git status` 看一遍 staged delete 列表里没 `.gitkeep`。

### Node / web build 在 CI 上的兼容性

- `.github/workflows/ci.yml` 用 `actions/setup-node@v4` 锁 `node-version: '20'`,绕开 host node 25+ 的兼容问题(GitHub-hosted runner 默认 node 比较新,直接用会撞 esbuild / vitest 的 bug)。
- `npm ci` 加 `--ignore-scripts` 跳 esbuild postinstall(node 25+ 上 `node install.js` SIGKILL bug;esbuild 子包 `@esbuild/<os>-<arch>` npm 自动按平台拉,postinstall 只是验证,跳过不影响 build)。
- `npm test` 用 `vitest run --pool=forks`(不是 `threads`):node 25+ worker_threads + vitest 4 偶发死锁,forks 走 child_process 绕开。
- 详见 `.github/workflows/ci.yml` web job 内嵌注释。

## 项目结构速查

| 目录 | 用途 | 改这里需注意 |
|---|---|---|
| `cmd/tshoot/` | CLI 入口(用户命令) | 改 CLI 行为同步看 [`README.md`](README.md) CLI 子命令段是否要更新 |
| `cmd/tshoot-desktop/` | 桌面 app(Wails)入口 | 跟 CLI 共享 `internal/`,主要改 Web 前端调用 |
| `api/` | HTTP server(三入口之一) | 改 router / handler 一定加 `api/handler_test.go` 测试 |
| `internal/agent/` | install / self-test / mcp builder | **改 mcp 接入必看** [`docs/decisions.md`](docs/decisions.md) "MCP 软约束哲学" |
| `internal/generator/` | yaml → workspace 模板渲染 | 改 SKILL.md.tmpl 同步 fixture 测试 |
| `internal/config/` | yaml schema + 校验 | 改 schema 同步 `schema/troubleshooter.schema.yaml` + examples |
| `internal/cchub/` | 配置中心 HTTP API client(nacos / apollo / consul) | nacos 2.x/3.x probe 设计见 [`docs/decisions.md`](docs/decisions.md) |
| `internal/doctor/` | 漂移检测规则 | 加新规则在 `internal/doctor/rules/` 下加文件 + 单测 |
| `templates/workspace/skills/` | 生成的机器人 SKILL 模板 | 改 SKILL 工具名要跑 `tshoot self-test` 验证 mcp probe |
| `docs/decisions.md` | ADR 风格决策记录 | **大重构 / 删 mcp / 换包**要追加一条 |

## 常见改动流程

### 加新 MCP 接入(类比 grafana / mongo 等)

1. 在 `internal/agent/install_native_mcp_data_stores.go`(数据层)或 `_obs.go` / `_messaging.go` 加 builder 函数
2. **跑 runtime probe 验证**:`/tmp/mcp-probe/probe.py` 起 stdio + tools/list 拿真实工具名 — 别只读 README(本项目 5 处 SKILL 死引用都是 README vs 实际暴露脱节)
3. 在 `templates/workspace/skills/<X>-runtime-query/SKILL.md.tmpl` 里列**实测**的工具集 + 软约束(写工具禁调)
4. 在 `self_test_mcp_probe.go` 的护栏不动 — 它会自动覆盖新加的 mcp
5. 测试:`go test ./internal/agent/`
6. 大重构(类比 nacos 三轮演进)→ 追加 `docs/decisions.md` 一条

### 加新 SKILL

1. `tshoot skill new <name>` 起骨架(`internal/skillscaffold/`)
2. 模板在 `templates/workspace/skills/<name>/SKILL.md.tmpl`
3. **frontmatter** 写 `description:` — LLM 看 SKILL 列表时只看 description 决定调不调,**写不好 LLM 不会找上门**

### 改 yaml schema

1. `schema/troubleshooter.schema.yaml` 改字段
2. `internal/config/` 加解析 + 校验
3. `examples/*.yaml` 跟着加 / 改字段(至少 nacos / apollo / consul 三大类 example 都过)
4. `internal/generator/` 用到新字段的地方加渲染逻辑
5. `tshoot doctor` 加漂移检测规则(如果新字段是关键耦合点)

### 删 / 砍能力

1. 跟 [`docs/decisions.md`](docs/decisions.md) 的"已废弃"区一致格式,**追加一条 SUPERSEDED**(不要改老条目)
2. 凭据 prompt 收集(`install_prompts.go`)同步删 / 注释 — 不收用户填了没用的凭据(详见 `decisions.md` "B 方案"哲学)
3. wizard askBool 文案如果还保留选项,加"实验性,当前未实现"提示

## Commit Message 规范

参考 `git log --oneline -20` 的现有风格:

```
fix nacos 接入彻底回归方案 B(HTTP API 主路径)— 23d503a 设计前提坍塌的最终终局
feat P1.1: MCP probe 工程化进 self_test_openclaw — 防 silent failure 的长期护栏
refactor P1.3: 拆 install_native_mcp_common.go 1103 行 → 4 个文件,按 SRP
docs P1.2: README TOC + 角色导览 + docs/decisions.md 决策演进记录
```

- 标 `fix` / `feat` / `refactor` / `docs` 一类前缀(中英混合 OK,本项目主要受众中文)
- 标 P1 / P2 / P3 优先级(可选,长 sprint 用)
- subject 行**自带 context** — "为什么改"比"改了什么"更重要
- body 段可长,**真实事故复盘**写清(类比 truss 现场 nacos 案例的 commit message)

## 测试要求

- **底线**:`go test ./...` 全过,新加 path 必须有 happy path 单测
- 关键模块覆盖率门槛:
  - `internal/agent/` ≥ 60%(当前 66.5%)
  - `internal/generator/` ≥ 60%(当前 63.5%)
  - `internal/deploy/` ≥ 80%(当前 88.1%)
  - `internal/doctor/` ≥ 70%(当前 79.6%)
  - `api/` ≥ 50%(当前 59.2%,2026-05 起)
- **新加 mcp builder** 必须有"它**不**注册"的 negative test(防同 rabbitmq 方案 B 路径,或 feishu_project 真禁用路径决策反悔后没护栏 — 两种 case 见 [`AGENTS.md`](AGENTS.md) "不注册 mcp 的两种情况")。反过来,**注册类的 mcp**(如 nacos plan D)要有"它**确实**注册 + 关键参数正确"的 positive test + 反 bake-token 回归护栏(见 `install_native_mcp_common_test.go`)
- **CI 不跑真 mcp probe**(没 npx/uvx),probe 测试 stub `probeMCPFunc` package var,真测自己本地起 docker 跑 `self_test_mcp_probe.go::TestSelfTestOpenclaw_MCPProbeFAIL`

## License

Apache-2.0 — 跟主要依赖(`mcp-grafana-npx` Apache-2.0 / `tuannvm/kafka-mcp-server` MIT / 等)兼容。
