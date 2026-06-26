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
  /** 多条件版 showWhen;全部命中才显示。用于 provider + auth_mode 这类级联条件。 */
  showWhenAll?: Array<{ field: string; equals: string }>
  /** 按 sibling 字段值切换 label。 */
  labelBy?: { field: string; values: Record<string, string> }
  /** 按 sibling 字段值切换 placeholder。 */
  placeholderBy?: { field: string; values: Record<string, string> }
  /** 按 sibling 字段值切换部署环境变量名。 */
  envVarBy?: { field: string; values: Record<string, (env: string) => string> }
  /** uiOnly:UI 状态字段,不参与 yaml emit / install 凭证收集(如 auth_mode) */
  uiOnly?: boolean
}

function siblingValueWithDefault(field: string, getSibling: (key: string) => string): string {
  const value = getSibling(field)
  if (value) return value
  if (field === 'auth_mode') return 'access_key'
  if (field === 'provider') return 'kuboard'
  return ''
}

export function isCredFieldHidden(f: CredField, getSibling: (key: string) => string): boolean {
  const conditions = f.showWhenAll || (f.showWhen ? [f.showWhen] : [])
  if (conditions.length === 0) return false
  return conditions.some(cond => siblingValueWithDefault(cond.field, getSibling) !== cond.equals)
}

function resolveDynamicText(
  fallback: string | undefined,
  rule: { field: string; values: Record<string, string> } | undefined,
  getSibling: (key: string) => string,
): string | undefined {
  if (!rule) return fallback
  const siblingVal = siblingValueWithDefault(rule.field, getSibling)
  return rule.values[siblingVal] || fallback
}

export function resolveCredFieldDisplay(f: CredField, getSibling: (key: string) => string): CredField {
  const envVarBy = f.envVarBy
  const envVar = envVarBy
    ? (env: string) => {
        const siblingVal = siblingValueWithDefault(envVarBy.field, getSibling)
        return (envVarBy.values[siblingVal] || f.envVar)(env)
      }
    : f.envVar
  return {
    ...f,
    envVar,
    label: resolveDynamicText(f.label, f.labelBy, getSibling) || f.label,
    placeholder: resolveDynamicText(f.placeholder, f.placeholderBy, getSibling),
  }
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
