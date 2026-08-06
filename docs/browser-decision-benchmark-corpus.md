# 自主浏览器验证 Benchmark Corpus

该工具不启动 Chromium、不访问网络。它可以只评估已有脱敏 corpus，也可以只读扫描 Studio 历史库和 artifact 元数据，自动生成脱敏 corpus。它用于在提高 `browser_decision_rollout.percentage` 前执行统一门禁。

## 运行

```bash
go run ./cmd/tshoot-browser-benchmark -corpus /absolute/path/corpus.json > report.json
```

从本机真实历史自动生成 corpus：

```bash
go run ./cmd/tshoot-browser-benchmark \
  -collect-root ~/.tshoot/bugs \
  -output /absolute/path/browser-benchmark-corpus.json
```

采集成功只表示 corpus 文件已经生成，不表示质量门禁通过。生成后必须再用 `-corpus` 执行门禁。CI 需要精确区分退出码时应先构建二进制再执行；`go run` 会把子程序的退出码 2 包装成自身的退出失败。

退出码：

- `0`：corpus 合法且全部门禁通过；
- `1`：文件、JSON、schema、样本或阈值非法；
- `2`：报告已生成，但一个或多个质量/安全门禁未通过。

## Corpus v1

顶层必须是一个 JSON 对象：

```json
{
  "version": 1,
  "samples": []
}
```

`samples` 最多 100000 条，文件最大 8 MiB。空数组是合法 corpus，但会以缺少各测试集和状态动作覆盖明确失败，不能形成空样本通过。未知字段、尾随 JSON、重复 ID、路径型 ID、非法 conclusion 和负数计数都会拒绝。可选 `thresholds` 必须完整提供六个 0–1 数值；省略时使用设计文档默认门槛。

每条样本只允许保存以下脱敏聚合字段：

```json
{
  "id": "ordinary-case-001-run-1",
  "suite": "ordinary",
  "repeat_group": "ordinary-case-001",
  "autonomous_completed": true,
  "baseline_autonomous_completed": false,
  "conclusion": "reproduced",
  "current_evidence": true,
  "manual_reproduction_selected": false,
  "capability_gap_recorded": false,
  "locator_triggered_manual": false,
  "state_actions": 4,
  "wrong_element_actions": 0,
  "high_risk_wrong_actions": 0,
  "unauthorized_production_actions": 0
}
```

允许的 suite 是 `ordinary`、`recipe_v3`、`locator_corpus`。允许的成功 conclusion 是 `reproduced`、`not_reproduced`、`fixed_verified`、`still_reproduces`。一致性组至少包含三次执行才进入统计。

Corpus 不得包含 URL、页面文本、截图、locator、动作值、模型输出、artifact 路径、Case/Bug 原始 ID 或凭据。`id` 和 `repeat_group` 应使用离线生成的不透明标识。

## 自动采集的证明边界

采集器以只读模式打开已有 `workflows.db`，兼容 v11、v12 和当前 v13；每个版本都必须通过自身 schema marker 与完整 fingerprint 校验。它不会创建数据库、修改权限、切换 journal mode 或执行迁移。低于 v11、未来版本或 fingerprint 不匹配时直接拒绝。

`runs.json` 只用于读取与 Attempt ID 绑定的 `browser_decision_loop_metrics`。输出的样本 ID 和重复组 ID 由带域分隔的 SHA-256 派生，原始 Case、Bug、Attempt ID 不进入 corpus。采集器只接受以下 Host 可证明事实：

- validation/regression Attempt 与同 ID 投影事件的绑定；
- 完整的自主循环终态、场景合同 SHA256 和 Recipe 版本；
- 已注册且非 pending 的证据 artifact 元数据，以及 Attempt 输出中非空的证据声明；
- 精确 `browser_capability_gap` 与通过 Attempt 绑定校验的 `manual_reproduction_gate`；
- Host 记录的状态动作、错误元素、高风险错误动作和生产未授权动作计数。

旧事件缺少任一安全计数时整条跳过，不把缺失值猜成 0。所有未被 effect 确认的有状态动作保守计为错误元素动作；冻结场景动作还计为高风险错误动作；生产环境的任何有状态动作计为未授权生产动作。这一口径允许误报失败，但不允许伪造安全通过。

采集摘要使用稳定跳过原因：`no_projected_run`、`no_autonomous_metric`、`malformed_autonomous_metric`、`scenario_binding_missing`、`invalid_derived_sample` 和 `duplicate_projected_run`。摘要只含计数，不含业务身份或页面数据。

## 当前基线（2026-08-04）

对本机 `~/.tshoot/bugs` 的实际只读采集扫描到 595 个 Attempt，其中 226 个属于 validation/regression；这 226 个都没有新版自主循环指标，因此自主样本为 0，门禁按预期失败，缺少 ordinary、locator、Recipe v3、三次重复一致性和状态动作 corpus。该结果不是功能失败，而是证明默认 0% rollout 下尚无真实自主样本，不能提高灰度比例。
