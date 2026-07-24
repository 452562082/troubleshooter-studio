# 跨仓库服务拓扑设计

## 背景

多仓库配置解决了“机器人能找到哪些代码”，但没有完整解决“这些代码如何串成一次真实请求”。现有 `service-dependency-map.yaml` 主要由部署期正则扫描得到服务级下游关系，能支撑 cascade/runtime 检查，但缺少前端请求、网关 rewrite、后端入站路由之间的端点级配对。因此机器人常把前端、BFF/网关和后端视为彼此孤立的仓库。

CodeGraph 负责单仓库内部的符号和调用关系，不负责跨仓库协议边界。本设计在 analyzer 与 CodeGraph 之间增加“端点目录 + 跨仓匹配引擎”，把以下路径变成可解释、可确认的拓扑：

```text
前端组件 -> HTTP 请求 -> 网关/BFF 路由 -> 后端 Handler -> 仓库内部调用
```

## 目标

1. 部署时从多个本地仓库自动发现入站和出站端点。
2. 按服务名、host、method、path 和 rewrite 规则构造跨仓候选调用边。
3. 服务级展示拓扑，端点级保存代码位置、匹配理由和置信度。
4. 用户可在工作台确认、拒绝、修正或补充关系；人工结论写回源 YAML。
5. 排障机器人可从失败 URL 或服务名出发，沿有界调用路径逐跳取证。
6. 自动扫描失败、拓扑陈旧或关系不确定时不阻断部署，并稳定降级。

## 非目标

- 不构建函数级跨仓调用图；函数级分析仍由选定仓库内的 CodeGraph 完成。
- 不引入独立图数据库。
- 不用 LLM 决定正式调用关系。
- 不自动 checkout、切换分支或创建业务仓库 worktree。
- 不持续后台扫描，也不在每次排障前全量刷新。
- 首版不将 Kafka、RabbitMQ 等异步事件并入同步调用图。
- 不因自动结果变化而静默删除人工确认关系。

## 已选方案

采用“端点目录 + 确定性匹配 + 人工覆盖”方案：

```text
多个仓库
  -> 各语言端点扫描器
  -> Endpoint Catalog
  -> 跨仓匹配和评分
  -> Candidate Edges
  -> 人工 confirm/reject/add
  -> 服务级拓扑 + 端点证据
  -> routing / incident-investigator / CodeGraph
```

未选择：

- 仅增强现有服务级正则扫描：实现快，但无法解释端点配对，也难处理重复路由和网关 rewrite。
- 全量语义图谱：能力上限高，但会把 AST、CodeGraph、trace 和图存储同时引入首版，成本与误判治理过重。

## 配置契约

自动扫描产物不写入主配置。`troubleshooter.yaml` 只保存人工决定：

```yaml
service_topology:
  overrides:
    - action: confirm
      from_service: mall-web
      to_service: mall-bff
      protocol: http
      method: GET
      path: /api/orders

    - action: reject
      from_service: mall-web
      to_service: legacy-bff
      protocol: http
      method: GET
      path: /api/orders

    - action: add
      from_service: mall-bff
      to_service: mall-order
      protocol: http
      method: POST
      path: /internal/orders
```

规则：

- 当配置中至少有两个可运行 service node 且存在本地仓库路径时，自动发现默认执行；单仓库或只有 common-lib/infra 仓库时跳过，不增加单独的启用开关。
- `action` 只能是 `confirm`、`reject` 或 `add`。
- service 必须能解析到 `repos[].service_names` 或 repo 的有效服务名。
- HTTP method 统一转为大写；path 必须是以 `/` 开头的标准化模板。
- override 的语义键为 `from_service + to_service + protocol + method + normalized path`，不依赖易变化的内部哈希 ID。
- 合并优先级为：人工 `reject` > 人工 `add/confirm` > 高置信自动边 > 中低置信候选边。
- 已确认边在新扫描中失去证据时保留，但状态变为 `stale`，等待用户复核。

schema、Go config 类型、wizard import/export、draft round-trip 和 prior override 保护必须同步更新。

## 数据模型

### Endpoint

每个仓库独立产出端点事实：

```yaml
- id: mall-web:http:get:/api/orders
  repo: mall-web
  service: mall-web
  direction: outbound
  protocol: http
  method: GET
  path: /api/orders
  target_hint: "${API_BASE_URL}"
  location: src/api/order.ts:18
  source: axios
```

必要字段：repo、service、direction、protocol、method/path 或 gRPC method、location、source。`target_hint` 保存 host、服务发现名或配置变量等未完全解析的目标线索。扫描器只报告事实，不判断跨仓目标。

### CandidateEdge

匹配引擎连接一个 outbound endpoint 与一个 inbound endpoint：

```yaml
- from: mall-web:http:get:/api/orders
  to: mall-bff:http:get:/api/orders
  confidence: 0.92
  status: candidate
  reasons:
    - method_path_exact
    - api_base_url_maps_to_service
```

每条边必须保存：两端 endpoint、置信度、状态、正向理由、冲突或扣分理由。合法状态为 `automatic`、`candidate`、`confirmed`、`rejected`、`manual`、`stale`。

### 生成产物

- `routing/references/service-topology.yaml`：服务级上下游图和可用路径摘要，作为机器人导航入口。
- `routing/references/endpoint-evidence.yaml`：端点、代码位置、匹配理由和状态，按需展开。
- 现有 `service-dependency-map.yaml` 不废弃；其 upstream/downstream 从确认后的服务边汇总，继续服务 cascade/runtime 检查，避免两套拓扑相互冲突。

## 扫描范围

首版支持：

- Node/TypeScript：fetch、axios、Next.js proxy/rewrite、Express/Nest 路由。
- Go：net/http、Gin、Echo、Kratos 常见路由与 HTTP/gRPC client。
- Java：Spring MVC、Spring Gateway、Feign、gRPC。
- Python：Flask、FastAPI、Django 常见路由与 HTTP client。
- PHP：Laravel route、controller 和 HTTP client。
- Nginx：location、proxy_pass、rewrite。

扫描应复用现有 `APIRoute`、`DownstreamCall`、repo role 和 `service_names` 能力，但扩展为统一 Endpoint 契约。每个语言扫描器保持独立，单个扫描器失败不能污染其他仓库结果。

## 标准化与匹配

### 标准化

- `/orders/:id`、`/orders/{id}`、`/orders/[id]` 统一为 `/orders/{param}`。
- 前端 `baseURL + relativePath` 在证据充分时展开。
- 网关 prefix、strip 和 rewrite 作为显式转换步骤保留在路径证据中。
- 域名、K8s Service DNS、配置中心服务名映射到配置中的 service 名。
- HTTP 使用 `protocol + method + normalized path`；gRPC 使用 `package.Service/Method`。

### 置信度

- `0.95–1.00`：目标服务明确且 method/path 精确匹配，自动进入正式拓扑。
- `0.85–0.94`：目标服务明确，路径经过可证明的 prefix/rewrite，自动进入正式拓扑。
- `0.60–0.84`：method/path 唯一匹配，但 host 来自未完全解析的变量，进入工作台待确认。
- `< 0.60`：只有名称或模糊路径相似，不进入正式拓扑，只作为扫描提示。

以下情况扣分或拒绝：method 冲突；同一路径在多个服务重复且无 host 证据；变量无法解析；rewrite 链断裂；用户已有 reject override。匹配必须由确定性规则产生，不能调用 LLM 提升分数。

## 工作台交互

拓扑页由两部分组成：

- 服务级图：节点显示 repo/service/role，边按自动高置信、待确认、冲突或 stale 分层展示。
- 证据面板：显示 method/path、调用端和接收端代码位置、变量/rewrite 证据、评分理由。

动作：

- `确认关系` 写入 `confirm` override。
- `拒绝` 写入 `reject` override。
- `修改目标` 产生对旧边的 reject 与对新边的 add。
- `补充关系` 写入 `add` override。
- `重新分析拓扑` 重新扫描当前本地仓库，再与人工 override 合并。
- `保存确认结果` 更新当前 YAML；生成物和缓存不直接混入源配置。

高置信边默认生效但仍可查看和拒绝；中置信边不得静默影响正式拓扑。刷新时保留人工决定，失去支持证据的确认边只标 stale。

## 机器人消费流程

新增只读 `service-topology-query` skill，统一解析拓扑，避免多个 skill 手工解析 YAML。

1. 从用户描述、HAR、失败 URL、日志 route 或 service name 确定入口证据。
2. 标准化 method/path，查询候选路径；多条路径按人工确认、置信度、环境匹配和路径长度排序。
3. 每一跳优先用 trace 验证实际调用；无 trace 时查询网关/服务日志和 request ID，再检查 K8s/runtime。
4. 定位具体仓库和入口后调用 CodeGraph 分析仓库内部 Handler、Service 和下游调用。
5. 默认最多向下游遍历三跳并检测环；只有新增证据明确指向更深层时才继续。
6. 报告必须区分“运行时已验证”“静态高置信”“待确认”，不能把候选路径写成事实。

`incident-investigator` 与 `frontend-repro-investigator` 消费该 skill。原有 cascade 检查继续读取由正式服务边汇总的 `service-dependency-map.yaml`。

## 降级与错误处理

- 任一仓库或语言扫描器失败：记录该仓状态，其他仓库继续生成，部署不失败。
- 无拓扑：回落现有 routing、服务名、`rg/read` 和用户输入。
- 拓扑或本地分支过期：作为低置信导航，不作为根因证据。
- 服务图和当前环境 trace 冲突：以 trace 为当前运行事实，并提示刷新拓扑。
- override 引用不存在的服务：配置校验失败，避免生成幽灵节点。
- override 证据失效：保留为 stale，不自动删除。
- 多候选无法消歧：返回排序后的候选与理由，要求进一步取证，不任选一个。

## 性能与生命周期

- 部署时自动扫描一次，工作台支持手动刷新；排障时不做全量扫描。
- 复用现有 auto-analyze 的多目标部署缓存，避免 OpenClaw、Claude Code、Cursor、Codex 重复扫描同一组仓库。
- 仓库扫描并发有界；缓存键必须包含仓库路径、HEAD 和分析契约版本。
- 仅受影响仓库发生 HEAD 变化时允许增量重扫，其余端点目录复用缓存。
- 端点目录和候选边是可重建缓存；YAML override 是唯一需要长期保存的人工真值。

## 测试策略

1. 路径归一化、服务解析和置信度使用表驱动单元测试。
2. 每种首版语言提供最小离线 fixture，禁止测试隐式联网或 clone。
3. 建立 `web -> gateway/BFF -> backend` 三层 fixture，覆盖 URL 变量、rewrite、重复路由、多环境映射和 gRPC。
4. 覆盖 `confirm/reject/add/stale` 在重扫后的合并和优先级。
5. generator 使用 golden test 锁定 `service-topology.yaml`、`endpoint-evidence.yaml` 和汇总后的 `service-dependency-map.yaml`。
6. UI 测试确认、拒绝、修改目标、补边、刷新和 YAML 写回。
7. 端到端测试从 `GET /api/orders/123` 出发，验证机器人能得到 `web -> BFF -> order-service`，且每跳包含代码位置、理由和状态。
8. 覆盖单仓失败、缓存命中、HEAD 变化、循环依赖、无候选、多候选和 trace 冲突提示。

## 验收标准

给定包含前端、BFF/网关和后端的本地仓库配置，以及失败请求 `GET /api/orders/123`：

- 部署生成 `web -> BFF -> order-service` 的候选或正式服务路径。
- 每一跳都有 method/path、两端代码位置、置信度和匹配理由。
- 中置信关系出现在工作台待确认区，不进入正式服务图。
- 用户确认后 override 写回 YAML；重新部署和多目标部署结果稳定一致。
- 机器人能沿路径逐跳调用 trace/log/runtime，并在目标仓库内使用 CodeGraph。
- 任一仓库扫描失败时部署仍完成，报告明确指出拓扑缺口并保留 fallback。

## 后续演进

- 将 Kafka topic、RabbitMQ exchange/queue 等建模为独立事件拓扑，而不是伪装成同步调用边。
- 用实际 trace 统计校准静态边，但运行时观测不自动改写人工 override。
- 在端点目录稳定后评估 OpenAPI/Proto 导入和增量 AST 扫描。
- 只有在查询规模和路径算法确实超过 YAML/内存图能力后，才评估独立图存储。
