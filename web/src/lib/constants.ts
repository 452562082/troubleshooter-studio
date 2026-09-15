// constants.ts —— 跨页面共享的字符串字面量收口处。Go 端有 internal/agent/target.go::IDETarget,


export const Target = {
  ClaudeCode: 'claude-code',
  Cursor: 'cursor',
  Codex: 'codex',
  OpenCode: 'opencode',
} as const

export type TargetId = (typeof Target)[keyof typeof Target]

/** 全部后端支持的 generation.targets。新增 target 时这里和 Go validate 枚举必须同步。 */
export const TARGETS: TargetId[] = [Target.ClaudeCode, Target.Cursor, Target.Codex, Target.OpenCode]


export const IDE_TARGETS: TargetId[] = [Target.ClaudeCode, Target.Cursor, Target.Codex, Target.OpenCode]

/** 配置中心类型。后端 internal/config/types.go::ConfigCenter.Type 取这套。 */
export const ConfigCenterType = {
  Nacos: 'nacos',
  Apollo: 'apollo',
  Consul: 'consul',
  EnvVars: 'env-vars',
  Kuboard: 'kuboard',
  One2All: 'one2all',
  None: 'none',
} as const

export type ConfigCenterTypeId = (typeof ConfigCenterType)[keyof typeof ConfigCenterType]
