# 开发指南

改动前阅读 [AGENTS.md](AGENTS.md) 和[架构决策](docs/decisions.md)。当前支持 Claude Code、Cursor、Codex CLI、OpenCode，产品流程为排障、修复和提交。

## 环境与检查

- Go：以 [go.mod](go.mod) 为准。
- Node：以 [.nvmrc](.nvmrc) 为准；使用 nvm 时执行 `nvm install && nvm use`。
- golangci-lint：2.12.2，与 CI 一致；非默认路径通过 `GOLANGCI_LINT=/path/to/golangci-lint` 指定。
- Python：CI 使用 3.12，建议创建虚拟环境后安装依赖。

```bash
python3 -m pip install -r scripts/requirements-test.txt
go install golang.org/x/vuln/cmd/govulncheck@v1.5.0
npm ci --prefix web --ignore-scripts
make ci
```

`make ci` 包含 vet、lint、格式、类型检查、模块一致性、CLI 构建、依赖审计、Go 竞态测试、覆盖率、共享脚本测试及前端测试/构建。涉及桌面端时另运行 `make desktop-app`；改 Wails binding 后运行 `make wails-gen`。真实平台验收见[测试指南](docs/testing.md)。

Go 拓扑集成测试也会调用 Python 查询脚本，因此运行 `go test` 的环境同样需要
`scripts/requirements-test.txt` 中的依赖。CI 各 job 相互隔离，Go 测试 job 和脚本
测试 job 必须分别准备 Python，不能依赖 runner 预装的 PyYAML。

## 目录

| 路径 | 职责 |
|---|---|
| `cmd/tshoot/`、`cmd/tshoot-desktop/`、`api/` | CLI、桌面和 HTTP 入口 |
| `web/` | 前端页面与交互 |
| `internal/agent/` | 原生平台安装、MCP 与运行时诊断 |
| `internal/bughub/` | 工单、阶段执行、授权、Git 和持久化恢复 |
| `internal/config/`、`internal/initwizard/` | 配置模型与 CLI 向导 |
| `internal/analyzer/`、`internal/topology/` | 仓库扫描与跨仓服务关系 |
| `internal/generator/`、`templates/` | 产物生成与机器人模板 |
| `internal/cchub/`、`internal/doctor/` | 配置中心与声明漂移检查 |

## 改动要求

- **CLI**：命令、帮助文本与 README 同步。
- **Schema**：同步 `schema/`、Go 解析校验、前端导入导出和 `examples/`。
- **MCP**：更新 builder、`requiredMCPKeys` 和 skill；安装后实际探测协议及 `tools/list`，不能凭 README 猜工具名。
- **CodeGraph**：固定版本和逐平台 SHA256；升级跑 `scripts/test-codegraph-smoke.sh`，分别检查工具协议和索引完成状态。
- **删除能力**：连同凭据收集、向导、生成与部署引用一起清理；只保留必要的历史数据兼容。
- **架构变化**：追加简短 ADR，说明原因与边界；被取代决策明确标记。临时设计、执行日志和机器专属路径不进入长期文档。

MCP 软约束、替代访问方式与字段互斥规则统一见 [AGENTS.md](AGENTS.md)。

## 测试要求

新增路径至少覆盖成功与失败；MCP builder 必须有注册与禁用/跳过的 negative tests。代码修改执行对应回归，提交前跑完整检查；纯文档改动检查命令、链接和内容一致性即可。

覆盖率以 [scripts/check-go-coverage.sh](scripts/check-go-coverage.sh) 为准：

| 包 | 最低覆盖率 |
|---|---:|
| api | 50% |
| cmd/tshoot | 0.7% |
| internal/agent | 60% |
| internal/analyzer | 33% |
| internal/analyzerpipe | 40% |
| internal/generator | 65% |
| internal/deploy | 80% |
| internal/dsprobe | 9% |
| internal/doctor | 70% |
| internal/userconfig | 22% |

故障闭环必须覆盖非法状态迁移、重复命令幂等、两次独立授权、SQLite 重开、跨进程恢复和多仓库部分完成。恢复外部副作用前核对真实状态；Git 测试只用临时仓库与本地 bare remote，不连接业务远端或修改开发工作区。证据、错误和事件测试需断言 token、Cookie、Authorization、密码及 URL userinfo 不被泄露。

拓扑扫描使用离线 fixture；覆盖单仓失败、证据不足、确定性排序、人工覆盖与 YAML 往返。只有正式关系参与导航，兼容图由同一正式图投影；缓存测试包含配置摘要和仓库 HEAD 变化。普通单测不能隐式 clone、下载工具或调用在线模型。

## 提交与发布

使用 `feat:`、`fix:`、`refactor:` 或 `docs:` 描述最终改动。按具体文件暂存，避免误提交构建产物或删除 `internal/webui/dist/.gitkeep`：

```bash
git add <具体文件>
git diff --cached --check
git status --short
```

不要使用 `git add -A`。在 `test` 或功能分支验证后通过 PR/MR 合入 `main`；`main` 会触发发版，规则见 [CI 与发版](docs/CI-RELEASE.md)。
