# OpenCode 接入与验证记录

日期：2026-09-15。本机 OpenCode：1.2.22。

## 使用

1. 在创建向导勾选 OpenCode，或在 YAML 的 `generation.targets` 加入 `opencode`。
2. 完成生成和部署，已装机器人页将显示 OpenCode 下的排障、修复两个 Agent。
3. 在 Bug 工单的平台配置中添加 OpenCode 机器人映射，选择对应环境。
4. 在故障闭环选择该机器人，执行排障与已授权修复；提交授权沿用 Studio 现有流程。

模型使用 OpenCode 自己的配置。可通过 `opencode auth login` 配置账号，或在 YAML 中设置 `agent.target_models.opencode: provider/model`。不会复用其他平台的账号凭据。

默认安装目录为 `~/.config/opencode`，支持 `XDG_CONFIG_HOME`。MCP 合并已有 JSON/JSONC 文件，只管理当前机器人命名空间，并保留其他模型、供应商、MCP 条目和相关注释。重新部署与卸载会清理低优先级配置中的同名残留。

## 验证范围

- 四平台共用阶段执行、异常输出、取消、超时、重复执行及结果投影测试。
- OpenCode 生成、原生安装/卸载、自定义目录发现、JSONC 合并、凭据保护、MCP 启动失败测试。
- 真实 OpenCode CLI + 本地模拟模型：实际读取临时文件，写入并比对修复文件，验证结构化终态与用量回传。
- 真实 OpenCode CLI + 本地 MCP：实际执行 `initialize` 和 `tools/list`；通过 `agent list` 确认生成并安装的两个 Agent 被识别。
- 真实 OpenCode CLI + DeepSeek 账号（`deepseek/deepseek-v4-flash`）：完整闭环通过，耗时 71.61 秒。实际排障、两次授权、修复分支推送、目标分支提交、独立验收及 SQLite 重开均通过；原始工作目录未改变。见 [真实模型验收记录](real-workflow-validation.md)。
- 前端 59 个测试文件、602 项测试通过；类型检查、代码检查、Go 竞态回归、覆盖率门禁及共享 Python 脚本检查通过。
- CLI 与桌面构建产物：`bin/tshoot`、`dist/TroubleshooterStudio.app`。未安装到系统应用目录，也未重启现有工作台。

隔离集成测试命令：

```bash
TSHOOT_LIVE_OPENCODE_PROTOCOL=1 go test ./internal/agent ./internal/bughub -run 'TestOpenCode(MCPProtocolLive|LocalProtocolLive)' -v
```

真实模型完整闭环测试（会调用目标平台已配置的模型）：

```bash
TSHOOT_LIVE_WORKFLOW_TARGETS=opencode TSHOOT_LIVE_WORKFLOW_REPORT_DIR=/tmp/tshoot-workflow-reports go test ./internal/bughub -run '^TestWorkflowRealModelLive$' -count=1 -v -timeout 18m
```

该测试使用可观察的排序故障、临时 Git 仓库与本地 bare remote，把真实模型直接接入生产阶段执行器、状态机、SQLite 和提交服务。模型自行读代码、排障、修复、测试并推送修复分支；测试再执行两次授权、重复授权检查、提交后独立验收，以及数据库关闭重开。不会伪造模型结果或阶段完成事件，也不会写入真实业务仓库。可用逗号指定多个平台，按平台增加测试超时时间。普通测试默认跳过此项；受控故障通过也不代表所有业务故障均可正确定位。

## 尚未通过的检查

- **免费模型入口尚未通过**：此前分别测试 `opencode/big-pickle` 和 `opencode/mimo-v2.5-free`，两者均在单阶段 8 分钟限制内没有返回最终排障结果；前者确有实际读取文件、运行命令。后续使用已配置的 DeepSeek 账号通过了在线完整闭环，但未据此认定这些免费入口已恢复。测试均未更改持久模型配置。

## 后续修复的审计问题

2026-09-15 的 CI 修复已处理原先记录的依赖审计失败：更新前端锁文件后 `npm audit` 为 0 项漏洞；Go 升级到 1.25.13 并更新 `golang.org/x/text`、`golang.org/x/net` 后，`govulncheck` 不再报告可达漏洞。此次未关闭或跳过安全审计。详见 `docs/decisions.md` 的“对齐本地检查与双平台 CI 门禁”。
