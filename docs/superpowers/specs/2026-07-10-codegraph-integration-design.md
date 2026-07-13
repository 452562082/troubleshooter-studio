# CodeGraph 代码智能接入设计

## 背景

`troubleshooter-studio` 当前有两层代码分析能力：

- 部署期 `internal/analyzer` 使用正则和启发式扫描仓库，生成服务名、配置中心、API 路由、下游依赖、数据层和 schema 种子。
- 故障期 `incident-investigator` 通过 `git log`、`git diff`、`rg/grep` 和文件读取完成代码取证。

现有能力适合快速生成确定性的 routing 资料，但缺少函数、调用关系、动态分派和修改影响面等符号级信息。本设计引入开源项目 [`colbymchenry/codegraph`](https://github.com/colbymchenry/codegraph)，作为故障期的结构化代码取证引擎。

CodeGraph 当前实现不是 LSP。它使用 Tree-sitter/WASM 解析源码，把符号与 `calls/imports/extends/implements/references` 等边写入本地 SQLite + FTS5 图谱，并通过 MCP 暴露查询能力。跨文件解析包含名称匹配和框架专用启发式，因此结果是静态分析证据，不是编译器级真值。

## 目标

1. 为生成的排障机器人提供函数级源码、调用路径和影响面查询。
2. 复用现有机器本地仓库路径，在多仓库系统中通过一个 MCP server 查询不同仓库。
3. 保留现有 analyzer 作为部署期快速扫描主路径，CodeGraph 只增强故障期代码取证。
4. CodeGraph 失败时不阻断机器人部署和排障，稳定回退到 `rg/read`。
5. 安装、索引、MCP 和 self-test 在 OpenClaw、Claude Code、Cursor、Codex 四个目标上使用同一套派生逻辑。

## 非目标

- 不把 CodeGraph 当作 gopls、jdtls、Pyright 等语言服务器。
- 不提供完整控制流、SSA、def-use 或污点分析。
- 不替换 `internal/analyzer` 或现有 `analysis.json` 契约。
- 不让 CodeGraph 单独决定故障根因或高置信度结论。
- 不自动切换、checkout 或修改用户业务仓库分支。
- 不在首版创建按环境隔离的 git worktree 索引。
- 不在卸载机器人时删除业务仓库的 `.codegraph` 或共享 binary。

## 已选方案

采用“单个共享 MCP + `projectPath` 多仓库路由”。

```text
排障问题
  -> routing 定位 repo/local_path/env branch
  -> Git 分支一致性检查
  -> 限时 codegraph sync
  -> codegraph_explore(projectPath=<repo>)
  -> 与日志、trace、数据库和变更记录交叉
  -> 失败时 rg/read fallback
```

未选择的方案：

- 每仓库一个 MCP：能获得每仓库 watcher，但会制造大量重复工具、进程和 self-test 成本。
- 把 CodeGraph 嵌入 analyzer：会把 Node/索引生命周期耦合进 Go 生成流水线，并失去故障期按需查询优势。

## 配置契约

新增系统级配置：

```yaml
code_intelligence:
  enabled: true
  provider: codegraph
```

规则：

- `enabled` 默认 `false`，必须由用户显式开启。
- 首版唯一合法 provider 是 `codegraph`。
- CodeGraph 版本、artifact URL 和 SHA-256 固定在 Studio 代码中，不暴露到业务 YAML。
- 启用后处理所有 `repos[].analysis.enabled: true` 且存在本地路径的仓库。
- 没有本地路径、路径不存在、没有受支持源码或平台不支持时记录 SKIP/WARN，不阻断部署。
- `generation.skills_whitelist` 仍是最终裁剪开关；未包含 `code-intelligence-query` 时不生成该 skill，但配置健康检查应提示能力被裁剪。

schema、Go config 类型、examples、wizard YAML 生成和 prior overrides 必须同步更新。

## 组件设计

### 1. CodeGraph binary 管理器

新增 `EnsureCodeGraphInstalled`，职责只有安装和复用固定版本 binary：

- 按 `GOOS/GOARCH` 选择上游 release artifact。
- 下载到 `~/.tshoot` 管理目录，校验固定 SHA-256 后再解包。
- 返回可执行文件绝对路径；MCP builder 和索引管理器都只消费绝对路径。
- 缓存命中时不访问网络。
- 不执行上游 `codegraph install`，避免其修改 `~/.claude.json`、`~/.cursor/mcp.json`、`~/.codex/config.toml` 或全局说明文件。
- 不使用 `latest`，升级必须显式修改固定版本和摘要，并重新运行真实 probe。
- 不使用 `curl | sh`。

CodeGraph bundle 自带 Node runtime，磁盘占用需要在向导中明确提示。本机 macOS arm64 的 npm 展开结果约 236 MB；具体数值可能随版本和平台变化，产品文案使用“约 200 MB+”。

### 2. 索引管理器

索引管理器接收：

```go
type CodeGraphIndexOptions struct {
    BinaryPath string
    SystemID   string
    Repos      []CodeGraphRepoTarget
    OnProgress func(string)
}

type CodeGraphRepoTarget struct {
    Name string
    Path string
    Head string
}
```

每个目标仓库执行：

1. 检查路径存在且是目录。
2. 检查是否含 CodeGraph 支持的源码扩展；没有则 SKIP。
3. 运行 `codegraph status <repo> --json`。
4. 未初始化时运行 `codegraph init <repo>`，上限 120 秒。
5. 已初始化时运行 `codegraph sync <repo>`，上限 30 秒。
6. 再次读取 status，记录 `fileCount/nodeCount/edgeCount/index.state` 和耗时。

仓库间并发上限为 2，防止多个 Tree-sitter/WASM 索引同时抢占内存。单仓失败转换为结果状态，不中断其他仓库。

多目标部署使用 process-level 缓存：

```text
cache key = system.id + sorted(repo.name=absolute_path@HEAD)
```

同一部署周期内 OpenClaw、Claude Code、Cursor、Codex 复用首次索引结果。缓存只保存成功/失败报告，不复制索引；真实索引仍位于业务仓库的 `.codegraph/`。

### 3. MCP builder

`BuildMCPServers` 新增一个 CodeGraph server：

```text
key: <system-or-agent-prefix>-codegraph
command: <absolute-codegraph-binary>
args: [serve, --mcp]
env:
  CODEGRAPH_TELEMETRY=0
  DO_NOT_TRACK=1
```

不设置 `CODEGRAPH_MCP_TOOLS`，沿用上游默认的单工具表面，只暴露 `codegraph_explore`。

CodeGraph 本身的 MCP 工具全部是只读工具，本设计不额外添加硬禁写参数。builder、OpenClaw 注入、IDE 注入和 `requiredMCPKeys` 使用同一名称派生规则。

### 4. `code-intelligence-query` skill

新增 workspace skill，职责是运行时代码取证。它读取：

- `routing/references/repo-path-map.yaml`
- `routing/references/env-branch-map.yaml`
- `routing/references/repo-stack-map.yaml`

调用前流程：

1. 根据服务和 routing 选择仓库。
2. 得到绝对 `local_path`，monorepo 按既有映射处理 `sub_path`。
3. 用 `git branch --show-current`、`git rev-parse HEAD` 获取本地状态。
4. 将当前分支与目标 env 的声明分支对比。
5. 分支匹配时限时执行 `codegraph sync <repo>`。
6. 调用 `codegraph_explore`，显式传 `projectPath`，默认 `maxFiles=4`。

查询必须包含尽可能具体的错误串、接口名、业务字段或候选符号。禁止用极宽泛的问题直接生成高置信度代码结论。

### 5. incident-investigator 集成

步骤 5.2 的代码取证入口调整为：

```text
CodeGraph skill 存在 + repo path 有效 + 索引 complete + 分支匹配
  -> code-intelligence-query
否则
  -> git diff + rg + Read
```

CodeGraph 返回的行号源码是代码事实；调用边、动态分派和 blast radius 是静态分析结论，可能带启发式成分。根因仍必须和日志、trace、数据库、请求 payload 或最近变更交叉。

## 分支与 freshness 规则

### 环境分支不匹配

- 不自动 checkout 或修改用户工作区。
- 允许把本地结果作为候选线索，但必须明确标注“本地索引不是目标环境分支”。
- 代码维度置信度上限为低，不得据此给生产变更命令。
- 首版不自动创建 managed worktree；这是后续独立特性。

### Cross-project watcher 限制

CodeGraph 只有默认 session project 附加 watcher；通过 `projectPath` 打开的其他项目只缓存数据库连接。排障机器人 cwd 通常是机器人 workspace，因此业务仓库不能只依赖 auto-sync。

运行时在重要代码查询前主动执行 30 秒上限的 `codegraph sync <repo>`：

- 成功：正常查询。
- 超时/失败：允许查询旧索引，但标记 stale，并对关键文件使用 `rg/read` 复核。
- index 不存在或 `state != complete`：不调用 CodeGraph，直接 fallback。

## 错误处理与降级

| 场景 | 行为 |
|---|---|
| binary 下载/校验失败 | WARN，跳过 CodeGraph，机器人继续部署 |
| repo 路径缺失 | SKIP，提示用户补本地路径 |
| repo 无支持源码 | SKIP，不视为故障 |
| init/sync 超时 | WARN，其他仓库继续 |
| index 非 complete | fallback `rg/read` |
| MCP handshake/tools-list 失败 | self-test FAIL/WARN，本次会话 fallback |
| tool call 失败 | 本次会话停止重复调用 CodeGraph，fallback |
| 分支不匹配 | 不自动切分支，代码置信度上限低 |
| 多个歧义候选 | 用包名、栈行号、文件名缩窄查询，不任选候选 |

CodeGraph 是可选增强能力，任何失败都不能阻断核心排障机器人部署。

## Self-test 设计

### MCP 健康

沿用现有 stdio runtime probe：

1. `initialize`
2. `notifications/initialized`
3. `tools/list`
4. 断言真实工具列表含 `codegraph_explore`

不得根据 README 猜工具名。

### 索引健康

逐仓运行 `codegraph status <repo> --json`，检查：

- `initialized == true`
- `index.state == "complete"`
- `fileCount > 0`
- 含受支持源码的仓库要求 `nodeCount > 0`
- extraction version 过期时 WARN，并给重建提示

MCP probe 成功但索引缺失不能报整体健康；这两层结果必须分别展示。

## UI 与用户反馈

向导新增“启用 CodeGraph 代码智能”开关，默认关闭。说明：

- 会下载约 200 MB+ 本地工具。
- 会在所有可分析的业务仓库中创建或更新 `.codegraph/`。
- 索引数据仅存本机，不进入机器人 workspace。
- CodeGraph telemetry 在 Studio 管理模式下关闭。
- 失败不会影响基础机器人部署，代码取证会回退到原路径。

部署日志逐仓显示：

```text
[codegraph] order-service: initialized (812 files, 9,420 nodes, 3.2s)
[codegraph] user-service: synced (12 changed files, 0.8s)
[codegraph] docs: skipped (no supported source files)
[codegraph] legacy: warn (index timeout, fallback enabled)
```

完成页显示 `CodeGraph X/Y repos ready`，并为失败仓库提供重新索引入口。

## 安全与隐私

- Studio 管理的 MCP 固定 `CODEGRAPH_TELEMETRY=0` 和 `DO_NOT_TRACK=1`。
- binary 使用固定版本和 SHA-256；校验失败不得执行。
- MCP 仅暴露上游只读查询工具。
- `projectPath` 必须来自 routing 的已配置绝对路径，不接受模型自由拼接敏感系统目录。
- `.codegraph` 内只保存本地索引；不复制到生成 workspace，不进入 `troubleshooter.yaml`。
- 卸载机器人不删除共享 binary 和业务仓库索引，避免破坏其他消费者。

## 测试策略

### Go 单元与集成测试

- config/schema：默认关闭、合法 provider、非法 provider、prior override。
- binary installer：平台映射、固定版本、SHA-256 错误、缓存命中、下载失败。
- MCP builder：enabled positive、disabled negative、绝对 command、telemetry env、`requiredMCPKeys`。
- index manager：init、sync、timeout、部分失败、无源码 SKIP、并发上限、多目标缓存。
- generator：skill 生成、whitelist 裁剪、routing 引用存在、链完整性。
- self-test：MCP 健康与索引健康分离，0-node 配置仓库不误报。
- API/桌面 binding：新字段和索引进度结果可序列化。

普通 Go 测试使用 fake CodeGraph CLI，不下载真实 bundle。

### 真实 smoke test

单独脚本固定 CodeGraph 版本，覆盖：

1. 小型 Go fixture 和 Java fixture 建索引。
2. MCP handshake 和 `tools/list`。
3. 调用 `codegraph_explore` 并验证返回源码行号。
4. 无索引 fallback。
5. 分支不匹配。
6. sync 后新增符号可被查询。

提交前运行：

```bash
go test ./internal/agent/ -run 'TestBuildMCPServers|TestCodeGraph|TestSelfTestOpenclaw'
go test ./internal/generator/ -run 'TestGenerate|TestSkillScriptPathsExist'
go test ./api/
scripts/test-skill-scripts.sh
go test ./... -race
scripts/check-go-coverage.sh
```

## 文档与决策记录

- README 增加 CodeGraph 能力、安装体积、隐私、分支约束和 fallback。
- CONTRIBUTING 增加 builder/runtime/index probe 要求。
- `docs/decisions.md` 追加 ADR：选择 CodeGraph MCP、保留 analyzer、单共享 MCP、固定版本和索引健康双 probe。
- 不修改历史 ADR。

## 验收标准

1. YAML 未启用时生成物和安装行为完全不变。
2. 启用后只注册一个 CodeGraph MCP，并真实列出 `codegraph_explore`。
3. 所有 `analysis.enabled` 且有本地路径的源码仓库获得 complete 索引或可见 WARN。
4. 任一仓库索引失败不阻断其他仓库和机器人部署。
5. 机器人能用服务名定位仓库，显式传 `projectPath`，给出 `file:line` 代码证据。
6. 目标环境分支不匹配时不输出高置信度代码结论，也不自动 checkout。
7. CodeGraph 不可用时原有 `git diff + rg + Read` 路径仍可工作。
8. self-test 能区分“server 可启动”和“仓库索引可查询”。
9. 四个部署目标共享同一 builder 结果，且多目标部署只索引一次。
10. telemetry 被禁用，下载 artifact 必须通过固定 SHA-256 校验。

## 后续演进

以下内容不进入首版：

- 为 dev/prod 分支创建 Studio 管理的独立 git worktree 和索引。
- 使用 CodeGraph 输出反哺部署期 service dependency map。
- 暴露 `codegraph_status`、`callers`、`impact` 等额外 MCP 工具。
- 集中式或远程共享代码图谱服务。
- LSP/compiler 结果与 CodeGraph 图谱融合。
