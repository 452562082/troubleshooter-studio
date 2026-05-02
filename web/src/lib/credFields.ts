// credFields.ts —— 配置中心 / 可观测性 / 数据源 凭证字段的通用 schema 形状。
// CC_FIELDS_BY_TYPE 等具体 spec 仍在 InitPage 里(它们依赖 reactive enabledDataStores
// 等步骤间状态),但 interface + KuboardResourceState 抽出来给子组件 import。

export interface CredField {
  /** keychain 子 key,例:"addr" "user" "pass" */
  key: string
  label: string
  /** secret 字段:input type=password + 眼睛按钮切显隐 */
  secret: boolean
  /** install.sh read_var 的变量名;envID 大写后作后缀 */
  envVar: (env: string) => string
  placeholder?: string
  optional?: boolean
  /** options 非空 → <select> 下拉(枚举字段,如鉴权方式 access_key / username_password) */
  options?: Array<{ value: string; label: string }>
  /** showWhen 非空 → 仅当同 env 下某 sibling 字段值匹配时才显示(条件表单) */
  showWhen?: { field: string; equals: string }
  /** uiOnly:UI 状态字段,不参与 yaml emit / install 凭证收集(如 auth_mode) */
  uiOnly?: boolean
}

export type KuboardClusterEntry = {
  name: string
  namespaces: { name: string; configmaps: string[] }[]
}

export type KuboardResourceState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ok'; clusters: KuboardClusterEntry[]; notes?: string[] }
  | { status: 'error'; error: string }
