// constants.ts —— 跨页面共享的字符串字面量收口处。Go 端有 internal/agent/target.go::IDETarget,
// 前端这里跟它对齐,避免 'openclaw'/'claude-code' 散落 InitPage / BotsPage / EditorPage 等多处。

/** 部署目标平台。openclaw 是自家产品 + 三家 IDE 平台。后端 cfg.generation.targets 取这套。 */
export const Target = {
  Openclaw: 'openclaw',
  ClaudeCode: 'claude-code',
  Cursor: 'cursor',
  Codex: 'codex',
} as const

export type TargetId = (typeof Target)[keyof typeof Target]

/** 全部后端支持的 generation.targets。新增 target 时这里和 Go validate 枚举必须同步。 */
export const TARGETS: TargetId[] = [Target.Openclaw, Target.ClaudeCode, Target.Cursor, Target.Codex]

/** IDE 三家(Claude Code / Cursor / Codex)—— 跟 OpenClaw 区分,装机走 install_native.go 而非 openclaw 那套。 */
export const IDE_TARGETS: TargetId[] = [Target.ClaudeCode, Target.Cursor, Target.Codex]

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
