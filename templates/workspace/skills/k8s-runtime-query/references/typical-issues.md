# K8s 运行时常见故障 → 查询路径速查

排障时按"症状"定位,不必背所有 reason。每条都给"先看哪里 / 关键判断点 / 处置方向"。

## 1. Pod 状态:Pending(永远没起来)

**先看**:`KuboardListEvents(namespace, fieldSelector=involvedObject.name=<pod>)` 里最近 Warning。

| Reason | 判断 | 处置方向 |
|---|---|---|
| `FailedScheduling` + `0/N nodes are available` | 资源不足 / 没匹配的 node | 看 spec.resources.requests 是否过大;是否绑了 nodeSelector / taint |
| `FailedScheduling` + `pod has unbound immediate PersistentVolumeClaims` | PVC 没绑成 | 看 PVC 状态、StorageClass 配置 |
| `FailedScheduling` + `node(s) had taint` | 节点 taint 没容忍 | spec.tolerations 加对应 taint 容忍 |
| (没 Events) | 调度器没收到 / API server 卡住 | 罕见,基础设施问题 |

## 2. Pod 状态:CrashLoopBackOff

**先看**:`KuboardGetPodLogs(previous=true)` 取上一次容器日志的最后 50 行。

| 日志特征 | 直接根因 |
|---|---|
| `panic: ...` (Go) / `Error: ...` 后 stack trace | 代码 bug,跟着栈到具体函数 |
| `Connection refused` / `dial tcp ... timeout` | 启动时连依赖(DB/MQ/配置中心)失败 → 检查依赖是否就位 + 网络策略 |
| `permission denied` / `cannot access ...` | 文件权限 / SecurityContext / hostPath 挂载错 |
| 静默退出 + ExitCode 137 | OOMKilled,看 limits;ExitCode 143 = SIGTERM(可能是 liveness 探针拒) |
| 静默退出 + ExitCode 0 | 应用主动退出(如启动 job 跑完就退),但 RestartPolicy=Always 拉起 → 改 Job/CronJob |

辅助看 Events 里:`BackOff`(K8s 等待重试)/ `Killing`(谁触发的杀,liveness probe?OOM?)。

## 3. Pod 状态:ImagePullBackOff / ErrImagePull

| 现象 | 处置 |
|---|---|
| Events 提到 `manifest unknown` / `not found` | 镜像 tag 写错 / 镜像没推上去,确认 tag 正确性 |
| Events 提到 `unauthorized` / `denied` | imagePullSecrets 没配 / 失效,看 secret 是否在该 ns |
| Events 提到 `dial tcp ... i/o timeout` | 镜像仓库网络不通,看 cluster 出网策略 |

## 4. Pod 状态:Running 但 Ready=False

容器在跑,但**探针失败**。看 `containerStatuses[].state.running.startedAt` + Events 里的 `Unhealthy`。

| Probe 类型 | 失败时影响 |
|---|---|
| readinessProbe | Service 不会把流量打过来;接口 503 上游分布,但不重启 |
| livenessProbe | 失败连续超阈值会重启,RestartCount 涨 |
| startupProbe | 初始化阶段,失败会延后 readiness/liveness 探测 |

排障:看应用本身的 `/health` / `/ready` 是不是返 200;startupProbe 期内"慢启动"也会被误判。

## 5. Service 后面没挂 Pod(端点空)

**症状**:接口 503 / connection refused,但 Pod 列表是 Running。

**先看**:`KuboardListServices` 取 selector,然后 `KuboardListPods(labelSelector=<selector>)` 看是否有匹配 + Ready 的 pod。

| 原因 | 判断 |
|---|---|
| Service selector 与 Pod label 不匹配 | selector 拼写错 / 标签更新没同步 |
| Pod 都 Ready=False | readinessProbe 不过 → 走 #4 |
| Service 端口与 Pod targetPort 不一致 | spec.ports[].targetPort 跟容器实际监听端口不符 |
| NetworkPolicy 阻断 | 极少见,新装集群默认放通 |

## 6. Deployment 在滚动但卡住

**先看**:`KuboardListDeployments` 看 `replicas / updatedReplicas / availableReplicas / readyReplicas` 之间关系 + Conditions。

| 现象 | 判断 |
|---|---|
| `updatedReplicas` 一直增长不动 | 新 RS 的 Pod 起不来(常见:CrashLoopBackOff / ImagePullBackOff)→ 看新 RS 那批 Pod 状态 |
| `availableReplicas < replicas` 长时间 | 新 Pod 还没进 ready 状态;readinessProbe 没过 |
| Condition `Progressing=False (ProgressDeadlineExceeded)` | K8s 已认定滚动失败,可能要回滚:看 `kubectl rollout history` |
| 老 RS 一直没缩 | 看 strategy.rollingUpdate.maxUnavailable;新 Pod 没就绪老的就不能下 |

## 7. OOMKilled

**特征**:容器 `terminated.reason=OOMKilled`,`exitCode=137`。

判断 limit 是否合理:
- 看应用真实使用(metrics-server,如果可用)
- 看应用的 GC 日志 / 启动加载量(如初始化时载入 N 万行字典)
- 启动期 OOM 通常是 `resources.requests.memory` 太低,而不是稳态用量

处置:加 limit + request,或优化应用启动期内存峰值(懒加载、分批加载)。

## 8. Pod 状态:Succeeded / Failed

只在 Job/CronJob/RestartPolicy=OnFailure 下出现:
- `Succeeded`:Job 跑完了正常退出。
- `Failed`:跑完了非 0 退出 + 重试次数耗尽。看 `logs_previous` 找原因。

## 关键命名速记(K8s 状态字段大小写敏感)

- Pod phase:`Pending / Running / Succeeded / Failed / Unknown`(大写开头)
- Wait reason:`ContainerCreating / CrashLoopBackOff / ImagePullBackOff / ErrImagePull / CreateContainerConfigError`
- Term reason:`Completed / Error / OOMKilled / ContainerCannotRun`
- Event type:`Normal / Warning`
- Common warnings:`FailedScheduling / Unhealthy / Killing / BackOff / Failed / FailedCreatePodSandBox`

## 跨 skill 协作

- **配合 `routing`**:env → cluster + namespace + label_selector,routing 给定向。
- **配合 `tracing-query`**:看到 pod 没事但接口慢 → 上 trace 看具体 span,不要在这层耗。
- **配合 `elk-log-query` / Loki**:pod 当前/上次日志 200 行不够 → 走聚合日志按 trace_id 拉全量。
- **配合 `config-executor`**:Pod 起不来怀疑配置错 → 读对应 ConfigMap / 配置中心条目对照。
