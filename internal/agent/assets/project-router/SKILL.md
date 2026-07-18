---
name: tshoot-router
description: AI 排障机器人全局项目路由入口。遇到报错、失败、超时、慢、卡住、排查、验证、回归或修复请求时，先用本入口确定当前项目唯一对应的机器人；不得按通用关键词猜选业务 Agent。
---

# Troubleshooter 项目路由

本 skill 是 {{TARGET}} 用户级安装中唯一的通用排障入口。业务排障、验证和修复 Agent 都是系统专属执行器，不能在未确认项目归属前直接选择。

## 强制路由

1. 在用户当前工作目录运行：

   ```bash
   python3 "{{ROUTER_SCRIPT}}" --cwd "$PWD"
   ```

2. 只接受 JSON 中 `status=matched` 且 `allowed=true` 的结果。
3. 根据意图从 `agents` 精确选择 ID：
   - 默认排障、定位原因：`troubleshooter`
   - 复现、验证、修复后回归：`validator`
   - 只有用户明确要求改代码、提交或推送：`fixer`
4. 必须调用返回的完整 Agent ID，不得换成名称相似的另一个 Agent。

用户明确点名系统、但当前目录不在该项目中时，可以运行：

```bash
python3 "{{ROUTER_SCRIPT}}" --cwd "$PWD" --system "<system_id 或 system_name>"
```

## Fail closed

- `unmatched`：说明当前项目没有绑定已安装机器人，请用户切换到项目仓库、部署对应机器人，或明确给出 system_id。
- `ambiguous`：列出 `candidates`，请用户明确选择；不要自行挑一个。
- 不得因为“报错/慢/失败/修复”等通用词命中任意业务 Agent。
- 路由完成前不得调用业务 Agent 的 MCP、读取它的 routing 映射或开始改代码。

Studio 故障闭环会在任务上下文的“选定机器人”中显式携带 `system_id/agent_id`，这是确定性绑定，不需要用 cwd 猜测。
