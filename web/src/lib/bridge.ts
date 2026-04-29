// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一组函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法（由 wails generate module 自动产出
// wailsjs/go/main/App.ts 绑定）；浏览器里 → 回退到原来的 fetch('/api/*')。
//
// 新页面写代码只调 bridge.*，不要直接 import wailsjs 或摸 window.go，省得未来改
// 通路到处改。类型从 wailsjs/go/models 来，Go 端改了 struct 跑 make wails-gen 同步。

import * as App from '../../wailsjs/go/main/App'
import { agent, analyzerpipe, deploy, discover, generator, main } from '../../wailsjs/go/models'

export type KuboardResources = main.KuboardResources

/** Kuboard 资源拉取:登录 → 列 cluster/ns/cm 三层。仅桌面 app 可用。
 *  鉴权:accessKey(免账密,Kuboard 后台个人中心→API 访问凭证创建)优先;
 *  否则用 username+password 走 /login。loginPath 已废弃(v4 路径固定)。 */
export async function kuboardListResources(
  url: string, username: string, password: string, accessKey = '', loginPath = '',
): Promise<KuboardResources> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  return App.KuboardListResources(url, username, password, accessKey, loginPath)
}

/** 批量拉 N 个 (cluster, namespace, configmap) 的 data 字段;
 *  Step 6 数据层自动识别用,挂在 kuboard 源的服务通过这个把 cm 内容拉回来,
 *  跟 nacos 一样跑 DS_MATCHERS 匹 redis/mysql/...。仅桌面 app。 */
export type KuboardFetchBatchInput = {
  url: string,
  access_key?: string,
  username?: string,
  password?: string,
  items: { key: string, cluster: string, namespace: string, configmap: string }[],
}
export type KuboardFetchBatchItemResult = {
  key: string,
  ok: boolean,
  content?: string,
  format?: string, // 固定 "yaml-multi"
  error?: string,
}
export type KuboardFetchBatchResult = {
  items: KuboardFetchBatchItemResult[],
  notes?: string[],
}
export async function kuboardFetchConfigMaps(input: KuboardFetchBatchInput): Promise<KuboardFetchBatchResult> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  return App.KuboardFetchConfigMaps(input as any) as any
}

/** 列指定 (cluster, namespace) 下的 Deployments;返回 name + selector(matchLabels)等。
 *  向导 Step 7 给 k8s 运行时配置服务 → workload 用,选完后从 selector 自动取 label_selector。 */
export type KuboardListDeploymentsInput = {
  url: string,
  username?: string,
  password?: string,
  access_key?: string,
  cluster: string,
  namespace: string,
}
export type KuboardDeploymentInfo = {
  name: string,
  namespace: string,
  replicas?: number,
  updated_replicas?: number,
  ready_replicas?: number,
  available_replicas?: number,
  strategy?: string,
  conditions?: string[],
  selector?: string,
}
export async function kuboardListDeployments(input: KuboardListDeploymentsInput): Promise<KuboardDeploymentInfo[]> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  // Wails 生成的 KuboardListPodsInput 用 snake_case(json tag),不是 Go 的 PascalCase。
  // 传错 key Go 端会拿到空 url/access_key,直接报"鉴权:填 accessKey 或 用户名+密码"。
  return App.KuboardListDeployments({
    url: input.url,
    username: input.username || '',
    password: input.password || '',
    access_key: input.access_key || '',
    cluster: input.cluster,
    namespace: input.namespace,
  } as any) as any
}

export type DiscoveredBot = discover.DiscoveredAgent
export type ApplyResult = agent.Result
export type InstallPrompt = deploy.Prompt
export type OpenYAMLResult = main.OpenYAMLResult
export type RunInstallResult = main.RunInstallResult
export type ValidateResult = main.ValidateResult
export type GenSummary = generator.GenSummary
export type Plan = generator.Plan
export type AnalyzeResult = analyzerpipe.Result
export type RepoSummary = analyzerpipe.RepoSummary
export type DoctorReport = Record<string, unknown> // doctor.Report 字段较多且业务后续会扩,先 loose

/** 桌面 app 模式下为 true，浏览器 / dev 模式下为 false */
export function isDesktop(): boolean {
  return typeof window !== 'undefined' && window.go != null
}

/** Validate system.yaml；失败抛 Error（message 已带解析原因） */
export async function validate(yamlText: string): Promise<ValidateResult> {
  if (isDesktop()) return App.Validate(yamlText)
  const resp = await fetch('/api/validate', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `validate failed: ${resp.status}`)
  return body as ValidateResult
}

/** Plan 干跑 gen,返回 skills / files / config-map 分布;不落盘 */
export async function plan(yamlText: string): Promise<Plan> {
  if (isDesktop()) return App.Plan(yamlText)
  const resp = await fetch('/api/plan', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `plan failed: ${resp.status}`)
  return body as Plan
}

/** Analyze 扫 reposRoot 下每个仓库,抽 service_names + 配置中心线索。
 *  autoClone=true 时缺失仓库自动 shallow clone(需要 git + 凭证)。
 *  进度通过 Wails 'analyze:log' event 推流,前端订阅后展示。
 *  浏览器模式下(tshoot serve)目前没对应 handler,只能桌面用。
 */
export async function analyze(
  yamlText: string,
  reposRoot: string,
  autoClone = false,
): Promise<AnalyzeResult> {
  if (!isDesktop()) {
    throw new Error('Analyze 仅在桌面 app 可用,浏览器模式请用 CLI: tshoot analyze')
  }
  return App.Analyze(yamlText, reposRoot, autoClone)
}

/** AnalyzeV2:混合来源版,允许 per-repo 指定本地绝对路径(走 RepoPaths),
 *  没指定的仓库回落到 ReposRoot+Name 默认拼法。InitPage Step 4 的"本地 / 远程
 *  混合"模式专用。 */
export async function analyzeV2(
  yamlText: string,
  reposRoot: string,
  repoPaths: Record<string, string>,
  autoClone: boolean,
  repoName?: string,
): Promise<AnalyzeResult> {
  if (!isDesktop()) throw new Error('AnalyzeV2 仅在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.AnalyzeV2({
    yaml_text: yamlText,
    repos_root: reposRoot,
    repo_paths: repoPaths,
    auto_clone: autoClone,
    repo_name: repoName ?? '',
  } as any)
}

/** 从本地已 clone 的仓库目录里反查 origin remote URL,用于"本地模式"反填 yaml.repos[].url */
export async function getRemoteURL(repoPath: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.GetRemoteURL(repoPath)
}

// ── 用户级配置(~/.tshoot/config.json) ───────────────────────────
export interface UserConfigResult {
  default_repos_root: string    // 用户显式设过的;空串 = 没设
  resolved_repos_root: string   // 空时 fallback 到 ~/.tshoot/repos;永远非空,UI 展示用
  home_dir: string              // 当前用户 $HOME;前端据此把绝对路径折成 ~/... 展示
}

/** 读用户级配置(默认 clone 目录等)。没设过也不会 reject,返空串 + fallback。 */
export async function getUserConfig(): Promise<UserConfigResult> {
  if (!isDesktop()) return { default_repos_root: '', resolved_repos_root: '', home_dir: '' }
  return App.GetUserConfig()
}

/** 保存默认 clone 父目录。空串清除用户设置,回落到内置 fallback。 */
export async function setDefaultReposRoot(path: string): Promise<void> {
  if (!isDesktop()) return
  await App.SetDefaultReposRoot(path)
}

/** 读某 system.id 下的"仓库名 → 本地路径"映射。
 *  yaml 不含本机路径,这份从 ~/.tshoot/config.json 来,wizard 部署时会 upsert。
 *  没存过返回 {}。仅桌面 app 可用。 */
export async function getRepoPathsForSystem(systemID: string): Promise<Record<string, string>> {
  if (!isDesktop() || !systemID) return {}
  const r = await App.GetRepoPathsForSystem(systemID)
  return r || {}
}

/** 主动持久化"仓库名 → 本地路径"映射(空 map 清掉该 system 的所有路径)。
 *  ImportAndDeploy 内部会自动调,这里给 wizard"改完不立刻部署也能存"用。 */
export async function saveRepoPathsForSystem(systemID: string, paths: Record<string, string>): Promise<void> {
  if (!isDesktop() || !systemID) return
  await App.SaveRepoPathsForSystem(systemID, paths)
}

// 注:曾经的 diff() bridge + DiffPage 已删 —— 功能被 BotsPage 的"编辑配置 → 预演"
// 完全覆盖(而且那个给的是 target-aware 真实 diff,带 preserve/remove 列表)。
// 后端 App.Diff binding 暂留做 CLI 调用兼容,UI 不再经过 bridge.

/** Doctor 对比声明 vs 代码实态,reposRoot 留空只校验声明一致性 */
export async function doctor(yamlText: string, reposRoot = ''): Promise<DoctorReport> {
  if (isDesktop()) return (await App.Doctor(yamlText, reposRoot)) as unknown as DoctorReport
  const qs = reposRoot ? `?repos_root=${encodeURIComponent(reposRoot)}` : ''
  const resp = await fetch(`/api/doctor${qs}`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `doctor failed: ${resp.status}`)
  return body
}

/** Gen 真落盘；outputDir 空字符串 = 用 yaml 里的 generation.output_dir（推荐） */
export async function gen(yamlText: string, outputDir = ''): Promise<GenSummary> {
  if (isDesktop()) return App.Gen(yamlText, outputDir)
  const resp = await fetch('/api/gen', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `gen failed: ${resp.status}`)
  return body as GenSummary
}

/** DiscoverBots 扫描已装机器人；只在桌面 app 下有意义，浏览器下返回 [] */
export async function discoverBots(extraRoots: string[] = []): Promise<DiscoveredBot[]> {
  if (!isDesktop()) return []
  // Go 端 nil slice 会被 JSON 编成 null；强制兜成数组
  const r = await App.DiscoverBots(extraRoots)
  return Array.isArray(r) ? r : []
}

/** UninstallBot 卸载已装机器人:按 target 分派(openclaw / claude-code / cursor)。
 *  - openclaw:workspace 移 ~/.Trash + 摘 openclaw.json agents.list + 清 creds.json
 *  - claude-code / cursor:中间包移 ~/.Trash + 清 ~/.claude|cursor/{agents,skills,scripts}/<name>
 *  返回结果含日志,前端展示给用户看动了哪些资源。仅桌面 app 可用。 */
export type UninstallBotResult = {
  target: string,
  // openclaw 专属
  workspace_moved_to?: string,
  openclaw_json_clean?: boolean,
  creds_removed?: boolean,
  // claude-code / cursor 专属
  staging_moved_to?: string,
  user_agent_md?: string,
  user_skills_dir?: string,
  user_scripts_dir?: string,
  log?: string[],
}
export async function uninstallBot(dir: string, target: string): Promise<UninstallBotResult> {
  if (!isDesktop()) throw new Error('UninstallBot 只在桌面 app 里可用')
  return App.UninstallBot(dir, target) as any
}

/** ApplyBot 把新 yaml 应用到已装机器人的活 workspace（含 preserve 保留用户手改） */
export async function applyBot(
  agentPath: string,
  newYamlText: string,
  dryRun: boolean,
): Promise<ApplyResult> {
  if (!isDesktop()) throw new Error('ApplyBot 只在桌面 app 里可用')
  return App.ApplyBot(agentPath, newYamlText, dryRun)
}

/** 原生文件对话框：选一个 yaml 文件,返回 {path, content};取消返回空对象 */
export async function openYAML(): Promise<OpenYAMLResult> {
  if (!isDesktop()) throw new Error('OpenYAML 只在桌面 app 里可用')
  return App.OpenYAML()
}

/** 跑一次 gen 到 tmp 目录,返回所有产物文件(含内容)。
 *  比 plan() 重(真实写盘 + 读回内容),给 EditorPage 的"📂 预览产物"按钮用,
 *  让用户像文件浏览器一样点开看每个文件。仅桌面 app 可用。 */
export type GenPreviewFile = {
  path: string,
  size: number,
  binary: boolean,
  truncated?: boolean,
  content?: string,
}
export type GenPreviewResult = {
  system: string,
  config_center: string,
  targets: string[],
  skills_included: { name: string, reason?: string }[],
  skills_skipped: { name: string, reason?: string }[],
  files: GenPreviewFile[],
}
export async function genPreview(yamlText: string): Promise<GenPreviewResult> {
  if (!isDesktop()) throw new Error('GenPreview 只在桌面 app 里可用')
  return App.GenPreview(yamlText) as any
}

/** 原生目录对话框：选一个目录（用于部署目标路径 destPath），返回路径；取消返回空串 */
export async function openDir(title: string): Promise<string> {
  if (!isDesktop()) throw new Error('OpenDir 只在桌面 app 里可用')
  return App.OpenDir(title)
}

/** 把 yaml 直接部署成一个新机器人（agent.ImportAndApply 的 UI 封装）
 *
 *  repoPaths: 仓库名 → 本机绝对路径,产物里会写进 repo-path-map.yaml。
 *  system.yaml 本身不含这些路径(故意的,跨机器可分享),只有通过这里把路径送进
 *  产物。想按"仓库"部署但不关心路径的场景(比如 CLI 跑 smoke test)传空 map 即可 ——
 *  产物里的 repo-path-map.yaml 就是"未配置"占位,bot 运行时会提示用户补齐。
 */
export async function importAndDeploy(
  yamlText: string,
  target: string,
  destPath: string,
  repoPaths: Record<string, string> = {},
  ideCreds: Record<string, string> = {},
): Promise<ApplyResult> {
  if (!isDesktop()) throw new Error('ImportAndDeploy 只在桌面 app 里可用')
  return App.ImportAndDeploy(yamlText, target, destPath, repoPaths, ideCreds)
}

/** 给 target 推荐默认部署路径。embedded/openclaw 返回 ~/.tshoot/<target>/<id>/
 *  (UI 据此不让用户手填 destPath);claude-code/cursor 返回空串(UI 强制必填)。
 *  浏览器模式直接返回空,浏览器模式本来就没 home dir 概念。 */
export async function defaultDestPath(target: string, systemId: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.DefaultDestPath(target, systemId)
}

/** 扫 install.sh 里所有 read_var 调用,给 UI 渲染凭证表单 */
export async function scanInstallPrompts(outputDir: string): Promise<InstallPrompt[]> {
  if (!isDesktop()) throw new Error('ScanInstallPrompts 只在桌面 app 里可用')
  const r = await App.ScanInstallPrompts(outputDir)
  return Array.isArray(r) ? r : []
}

/** 读 scripts/.env 现存值(用于预填表单) */
export async function readEnv(outputDir: string): Promise<Record<string, string>> {
  if (!isDesktop()) return {}
  return App.ReadEnv(outputDir)
}

/** 写凭证到 scripts/.env 后 shell-out bash install.sh,返回合并日志 */
export async function runInstall(
  outputDir: string,
  creds: Record<string, string>,
): Promise<RunInstallResult> {
  if (!isDesktop()) throw new Error('RunInstall 只在桌面 app 里可用')
  return App.RunInstall(outputDir, creds)
}

/** 跑一次 self-test:校验 agent 安装完整性 + ping 各 env 配置中心/可观测性端点。
 *  返回 ok 标志 + checks 明细;部署完自动触发,把 fail/warn 摘要弹给用户。 */
export type SelfTestCheck = { name: string; status: string; detail?: string }
export type SelfTestResult = { ok: boolean; checks: SelfTestCheck[] }
export async function selfTestAgent(dir: string): Promise<SelfTestResult> {
  if (!isDesktop()) throw new Error('SelfTestAgent 只在桌面 app 里可用')
  return App.SelfTestAgent(dir) as any
}

/** 取消正在跑的 install.sh(SIGKILL 给 bash 进程组)。返回 true=成功取消,
 *  false=当前没 install 在跑(UI 可忽略)。浏览器模式无 install,直接 false。 */
export async function cancelInstall(): Promise<boolean> {
  if (!isDesktop()) return false
  return App.CancelInstall()
}

// ── Infra 凭证(配置中心 / 可观测性 / 消息平台...) ─────────────────────
// key 格式建议 "<type>:<env>:<field>",例 "nacos:dev:addr"。部署前端从钥匙串读,
// 映射成 install.sh 认的 env var(CC_ADDR_DEV 等),install.sh read_var 跳过交互。
export interface InfraCredLoadResult {
  api_key: string
  ok: boolean
  err?: string
}
export async function saveInfraCred(key: string, value: string): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCred 只在桌面 app 可用')
  await App.SaveInfraCred(key, value)
}
export async function loadInfraCred(key: string): Promise<InfraCredLoadResult> {
  if (!isDesktop()) return { api_key: '', ok: false }
  return App.LoadInfraCred(key)
}
export async function deleteInfraCred(key: string): Promise<void> {
  if (!isDesktop()) return
  await App.DeleteInfraCred(key)
}
/** 批量保存/删(一次 RPC),value 为空串 = 删 */
export async function saveInfraCredBatch(entries: Record<string, string>): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCredBatch 只在桌面 app 可用')
  await App.SaveInfraCredBatch({ entries } as { entries: Record<string, string> })
}

// ── 配置中心预加载(真实 HTTP 调用,非 mock) ─────────────────────────
// Nacos → /nacos/v1/auth/login + /nacos/v1/cs/configs
// Apollo → /openapi/v1/apps + /envs/<env>/apps/<appId>/clusters/...
// Consul → /v1/kv/<prefix>?recurse=true&keys=true
export interface CCHubEntry {
  locator: string    // dataId(nacos) / namespace name(apollo) / full kv key(consul)
  group?: string     // nacos 独有;apollo 复用此字段表 cluster
  tenant?: string    // namespace
  type?: string      // yaml / properties / json / ...
  app_id?: string    // apollo 独有
}
export interface CCHubNamespace {
  id: string         // namespace UUID(public 为空串)
  show_name: string  // 友好名,UI 下拉选项用
}
export interface CCHubResult {
  type: string
  entries: CCHubEntry[]
  namespaces?: CCHubNamespace[]  // nacos / apollo:给 UI 下拉用
  notes?: string[]
}
export interface CCHubPreloadInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  namespace?: string
  app_id?: string
  namespaces_only?: boolean  // true = 轻量模式,只列 namespaces 不拉 configs
}
/** 连目标配置中心拉清单。失败 reject 带人话错误(网络 / 鉴权 / 参数等)。 */
export async function preloadConfigCenter(input: CCHubPreloadInput): Promise<CCHubResult> {
  if (!isDesktop()) throw new Error('PreloadConfigCenter 只在桌面 app 可用')
  return App.PreloadConfigCenter(input as any) as Promise<CCHubResult>
}

// 拉单条配置内容(给"数据层自动识别"用:Step 7 从已挑的 dataId 拉原文,js-yaml 解析出数据层)
export interface CCHubFetchContentInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  namespace?: string
  group?: string
  data_id: string
  app_id?: string
}
export interface CCHubFetchContentResult {
  content: string
  format?: string  // yaml / json / properties
  notes?: string[]
}
export async function fetchConfigContent(input: CCHubFetchContentInput): Promise<CCHubFetchContentResult> {
  if (!isDesktop()) throw new Error('FetchConfigContent 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.FetchConfigContent(input as any) as Promise<CCHubFetchContentResult>
}

// 批量拉取:nacos 会复用一次 probe+login,省 N-1 次登录开销。单条失败不中断整批。
export interface CCHubFetchBatchItem {
  key: string          // 前端自定义的映射 key(如 "dev::user-service")
  namespace?: string
  group?: string
  data_id: string
  app_id?: string
}
export interface CCHubFetchBatchInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  items: CCHubFetchBatchItem[]
}
export interface CCHubFetchBatchItemResult {
  key: string
  ok: boolean
  result?: CCHubFetchContentResult
  error?: string
}
export interface CCHubFetchBatchResult {
  items: CCHubFetchBatchItemResult[]
  notes?: string[]
}
export async function fetchConfigContentBatch(input: CCHubFetchBatchInput): Promise<CCHubFetchBatchResult> {
  if (!isDesktop()) throw new Error('FetchConfigContentBatch 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.FetchConfigContentBatch(input as any) as Promise<CCHubFetchBatchResult>
}

// 数据层连通性探测:轻量 TCP dial + 最小协议握手,5s 超时,不读不写数据
export interface DSProbeInput {
  type: string                          // redis / mysql / mongodb / ...
  fields: Record<string, string>        // url / dsn / host / port / brokers / user / pass ...
}
export interface DSProbeResult {
  ok: boolean
  latency?: string                      // "120ms"
  detail?: string                       // 成功时的服务版本 / 握手 banner
  error?: string                        // 失败时的人话原因
}
export async function probeDataStore(input: DSProbeInput): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeDataStore 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.ProbeDataStore(input as any) as Promise<DSProbeResult>
}

// 给 Step 3 环境列表的 api_domain / web_domain 自动测试用。GET 一下 URL,
// < 500 都算可达(404/401/403 也算通);DNS 失败 / 拒连 / 超时 / 5xx 算不通。
export async function probeURL(url: string): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeURL 只在桌面 app 可用')
  return App.ProbeURL(url) as Promise<DSProbeResult>
}

// 给 Step 7 可观测性工具用:可选 basic auth + 可选 Bearer / API Key。
export async function probeURLAuth(url: string, user: string, pass: string, apiKey: string): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeURLAuth 只在桌面 app 可用')
  return App.ProbeURLAuth(url, user, pass, apiKey) as Promise<DSProbeResult>
}

// ── Loki 标签映射(Step 7 可观测性下的 grafana/loki 子区) ────────────────
export interface LokiAuthInput {
  grafana_url?: string
  loki_url?: string
  ds_uid?: string
  api_key?: string
  user?: string
  pass?: string
}
export interface GrafanaDatasource {
  uid: string
  name: string
  type: string
  url?: string
  is_loki: boolean
  default?: boolean
}
export interface LokiLabelsResult {
  labels: string[]
  notes?: string[]
}
export interface LokiLabelValuesResult {
  key: string
  values: string[]
  notes?: string[]
}
export async function listGrafanaDatasources(input: LokiAuthInput): Promise<GrafanaDatasource[]> {
  if (!isDesktop()) throw new Error('ListGrafanaDatasources 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const r = await App.ListGrafanaDatasources(input as any)
  return Array.isArray(r) ? r : []
}
export async function listLokiLabels(input: LokiAuthInput): Promise<LokiLabelsResult> {
  if (!isDesktop()) throw new Error('ListLokiLabels 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.ListLokiLabels(input as any) as Promise<LokiLabelsResult>
}
// query 可选(LogQL 选择器);用于"已选 namespace 后只拉该 namespace 下的 app"等场景
export async function listLokiLabelValues(input: LokiAuthInput, labelKey: string, query = ''): Promise<LokiLabelValuesResult> {
  if (!isDesktop()) throw new Error('ListLokiLabelValues 只在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.ListLokiLabelValues(input as any, labelKey, query) as Promise<LokiLabelValuesResult>
}

// ── OpenClaw 模型探测 ──────────────────────────────────────────────────
// 读本机 ~/.openclaw/openclaw.json(真实 schema,从真实 install 反推),
// 把 agents.defaults.model.primary / agents.defaults.models / agents.list[].model
// 聚合成"可用模型"清单给向导 OpenClaw 卡用。
// 三种状态:
//   installed=false      → 没装(openclaw.json 缺失),让用户装完再来 / 手选目录
//   installed_but_empty  → openclaw.json 存在但无任何 model 字段,让用户先装个 agent
//   ok + models          → 正常展示下拉
export interface OpenClawModelEntry {
  id: string               // "openai-codex/gpt-5.4"
  provider?: string        // "openai-codex"
  label?: string           // id 或 "id (默认)"
  source?: string          // 来自 openclaw.json 哪个字段
  primary?: boolean        // 是否 defaults.model.primary
}
export interface OpenClawDetectResult {
  ok: boolean
  installed: boolean
  installed_but_empty?: boolean
  install_dir?: string
  config_path?: string
  version?: string
  models?: OpenClawModelEntry[]
  auth_providers?: string[]
  err?: string
}
/** 探测 installDir 下的 OpenClaw 配置;installDir 为空 = 用 ~/.openclaw 默认路径 */
export async function detectOpenClawModels(installDir: string): Promise<OpenClawDetectResult> {
  if (!isDesktop()) return { ok: false, installed: false, err: '浏览器模式不支持' }
  return App.DetectOpenClawModels(installDir)
}

// ── Claude Code / Cursor 安装探测 ─────────────────────────────────────
// 给 wizard Step 2 的 claude-code / cursor 卡片显示"✓ 已装 vX.Y / ⚠ 未装"徽标用。
// 跟 openclaw 不一样:这俩 target 不从本地读模型,只做"装了没 + 版本"信息展示。
export interface AIToolResult {
  installed: boolean
  version?: string
  path?: string
  note?: string
}
export interface AIToolsDetectResult {
  claude_code: AIToolResult
  cursor: AIToolResult
}
export async function detectAITools(): Promise<AIToolsDetectResult> {
  if (!isDesktop()) {
    return {
      claude_code: { installed: false, note: '浏览器模式不支持' },
      cursor: { installed: false, note: '浏览器模式不支持' },
    }
  }
  // Wails 生成的类型把 nested 字段标成 optional,但 Go 侧永远返回非 nil;
  // 做一次防御性兜底,前端收到 undefined 时降级为 not-installed。
  const r = await App.DetectAITools()
  return {
    claude_code: (r.claude_code as AIToolResult) || { installed: false },
    cursor:      (r.cursor      as AIToolResult) || { installed: false },
  }
}

// embedded target 的对话走 chatSend/chatStop(原生 chat,直连 LLM API,见上面)。
// BotsChat.vue 用这套流式 API,前端无 iframe / 无 HTTP 中转。

/** 在 Finder / Explorer 里展示(不是打开)指定路径 */
export async function revealInFinder(path: string): Promise<void> {
  if (!isDesktop()) return
  return App.RevealInFinder(path)
}

/** exportYAML 弹原生保存对话框导出 yaml 到任意路径。
 *  桌面 app 走 Wails SaveFileDialog;浏览器走 Blob 下载。
 *  返回值:桌面 app 下为保存路径(或用户取消时空串);浏览器下为下载文件名。
 */
export async function exportYAML(defaultFilename: string, yamlText: string): Promise<string> {
  if (isDesktop()) return App.SaveYAML(defaultFilename, yamlText)
  // 浏览器回退:触发 blob 下载
  const blob = new Blob([yamlText], { type: 'text/yaml;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = defaultFilename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return defaultFilename
}
