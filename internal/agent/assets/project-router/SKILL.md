---
name: tshoot-router
description: 可选的 AI 排障机器人项目路由入口。遇到排障或修复请求时检查当前仓库是否已绑定机器人；未绑定项目必须静默退出并继续普通工作，不得打断用户。
---

# Troubleshooter 项目路由

本 skill 是 {{TARGET}} 用户级安装中唯一的通用排障入口。它只负责识别当前项目是否已经配置排障机器人，不拥有未绑定项目，也不能让排障机器人影响普通 Codex 工作。业务排障、验证和修复 Agent 都是系统专属执行器，不能在未确认项目归属前直接选择。

## 强制路由

1. 在用户当前工作目录运行：

   ```bash
   python3 "{{ROUTER_SCRIPT}}" --cwd "$PWD"
   ```

2. 先处理 JSON 中的 `action`：
   - `continue_without_troubleshooter`：当前项目未绑定机器人。这是正常结果，必须静默停止本 skill，并继续处理用户原始任务；使用普通代码读取、排查、修改和测试能力，不调用任何业务排障 Agent 或其 MCP。
   - `use_troubleshooter`：只有同时满足 `status=matched` 且 `allowed=true` 时才进入下一步。
   - `request_binding_choice`：仅当同一项目存在多个冲突绑定时，展示 `candidates` 请用户选择。
   - `request_system_correction`：仅当用户明确传入 `--system` 但系统不存在时，说明该显式名称无效。
3. `action=use_troubleshooter` 时，根据意图从 `agents` 精确选择 ID：
   - 默认排障、定位原因：`troubleshooter`
   - 复现、验证、修复后回归：`validator`
   - 只有用户明确要求改代码、提交或推送：`fixer`
4. 必须调用返回的完整 Agent ID，不得换成名称相似的另一个 Agent。

用户明确点名系统、但当前目录不在该项目中时，可以运行：

```bash
python3 "{{ROUTER_SCRIPT}}" --cwd "$PWD" --system "<system_id 或 system_name>"
```

## 未配置项目默认旁路

- `reason=no_project_binding` 或 `reason=no_installed_bots` 时，必须按 `continue_without_troubleshooter` 处理并静默旁路。不得要求用户提供 system_id，不得建议部署机器人，不得报告路由失败，也不得停止或缩减用户原始任务。
- 静默旁路后可以正常读取当前仓库、诊断和修改代码；“不允许”只针对业务排障 Agent 及其专属 MCP，不限制普通 Codex 能力。

## 已配置项目 Fail closed

- `ambiguous`：列出 `candidates`，请用户明确选择；不要自行挑一个。
- 不得因为“报错/慢/失败/修复”等通用词命中任意业务 Agent。
- `matched` 但 `allowed=false` 时不得调用跨系统 Agent。
- 路由完成前不得调用业务 Agent 的 MCP 或读取它的 routing 映射。

Studio 故障闭环会在任务上下文的“选定机器人”中显式携带 `system_id/agent_id`，这是确定性绑定，不需要用 cwd 猜测。
