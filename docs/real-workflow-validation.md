# 真实模型故障闭环验证

日期：2026-09-15。

## 实测结果

| 平台 | 完整闭环 | 实际工具事件 | 修复提交 |
|---|---|---:|---|
| Codex | 通过 | 40 | `9ac34b7c98b6` |
| Claude Code | 通过 | 60 | `e40fe109f659` |
| Cursor | 通过 | 84 | `4737793cd5c0` |
| OpenCode（DeepSeek V4 Flash） | 通过 | 38 | `421894e63d73` |

四个平台均已通过真实在线模型完整闭环：最终状态 `submitted`，两次授权、两次阶段执行、目标分支独立验收、原始目录保持不变及 SQLite 重开全部通过。

OpenCode 重新验证使用已配置的 DeepSeek 账号，模型为 `deepseek/deepseek-v4-flash`。完整测试耗时 71.61 秒，实际工具事件 38 次，修复提交为 `421894e63d7358616281f3e0f580ab1be3b33f53`。仅通过进程内配置选择模型，未更改持久模型设置。结果文件：`/tmp/tshoot-opencode-deepseek-retry-reports/opencode.json`；测试日志：`/tmp/tshoot-opencode-deepseek-retry.log`。

此前免费模型测试记录：OpenCode 使用临时进程配置指定内置免费模型，没有更改用户的持久模型或账号配置。Big Pickle 实际完成了读文件和命令调用，但在单阶段 8 分钟限制内没有返回最终排障结果；MiMo-V2.5 Free 也在 8 分钟内未返回有效结果。两次均未进入修复授权阶段，不能计为这两个模型的完整闭环通过。匿名 HTTP 最小请求收到 403（error code 1010）。本次 DeepSeek 的成功不代表这些免费入口已恢复。

此前对照测试：脱离 Studio，在空临时目录直接调用 OpenCode CLI，要求 MiMo-V2.5 Free 仅回复 `OK` 且不执行工具，同样在 45 秒内未返回，随后自动清理进程。这说明当时等待响应的问题能够独立于工作台闭环复现；尚不能仅凭该现象确定供应商、网络或 CLI 内部的具体根因。

工程检查：全量 `go test ./... -race`、最后调整后的 bughub 模块竞态回归、四平台证据目录落盘测试、结果格式负向测试、覆盖率门禁、`make lint build desktop-app` 通过。构建产物为 `bin/tshoot` 与 `dist/TroubleshooterStudio.app`，未安装或重启现有应用。

## 验证方法

`TestWorkflowRealModelLive` 把真实平台 CLI 和在线模型接入生产 `AgentPhaseRunner`、`CaseOrchestrator`、SQLite 和 Git 服务。故障为临时 Python 仓库中的排序方向错误，测试输入最初确实失败。测试不伪造模型输出，不直接伪造阶段完成，也不访问真实业务系统。

提交使用仅监听 `127.0.0.1` 的临时 Git 服务及 bare 仓库。模型只写已授权的独立修复目录；本机 Git 服务写远程端，避免 `file://` 远程要求模型越过沙箱目录边界。业务仓库、真实 Bug 工单和现有应用进程不受影响。

通过标准：

- 模型实际读取源码并定位根因，状态停在等待修复授权。
- 独立授权后修改隔离仓库、测试、提交并推送修复分支。
- 第二次授权后由 Studio 提交到目标分支。
- 重复授权不产生重复任务或重复推送。
- 从目标分支另行克隆后运行原始验收测试，测试文件未被修改。
- 原始工作目录和 HEAD 不变；SQLite 关闭重开后保留提交状态及两次授权。

## 已确认并修复的问题

1. **进度标记和说明文字混入最终结果**：真实 Cursor、Claude 返回会把阶段进度或说明放在 YAML 前。现在识别已知进度前缀以及末尾唯一的 YAML 代码块，再执行原有严格字段与业务校验。额外字段、多份结果和尾部混杂文本继续拒绝。
2. **证据目录环境变量未传入子进程**：此前目录仅出现在提示词，工具使用 `$STUDIO_EVIDENCE_STAGING_DIR` 时值为空。现在四平台的普通执行、附件执行均传递真实目录，同时保留原有认证及平台环境。四平台落盘测试已覆盖。
3. **最终证据文件核对不足**：提示词补充要求逐一核对文件确已保存，不能将计划文件名或未保存的终端输出当成附件。缺失文件仍会被服务端严格拦截。

修复检查点与最终结果不一致时，系统仍拒绝提交；此次不降低提交、测试证据及检查点一致性要求。

## 运行

```bash
TSHOOT_LIVE_WORKFLOW_TARGETS=codex TSHOOT_LIVE_WORKFLOW_REPORT_DIR=/tmp/tshoot-workflow-reports go test ./internal/bughub -run '^TestWorkflowRealModelLive$' -count=1 -v -timeout 18m
```

可以用逗号选择多个平台，并相应增加总超时。默认测试不调用在线模型。报告只保存通过敏感信息检查的模型最终输出及临时检查点；通过时生成对应平台 JSON 报告。

这是实际在线模型处理受控故障的工程验收，不代表任意业务故障均能定位，也不替代修复上线后的人工验收。
