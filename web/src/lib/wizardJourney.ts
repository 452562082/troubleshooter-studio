// Keep the persisted configuration section IDs stable; navigation is now four phases.
export const JOURNEY = [
  { step: 5, title: '选择项目', description: '选择仓库，自动识别项目信息' },
  { step: 3, title: '运行方式', description: '选择 AI 平台和目标环境' },
  { step: 6, title: '排障能力', description: '按需连接配置、数据和日志' },
  { step: 9, title: '确认并创建', description: '检查摘要，创建后开始排障' },
] as const

export function normalizeJourneyStep(step: number): number {
  if ([1, 2, 5].includes(step)) return 5
  if ([3, 4].includes(step)) return 3
  if ([6, 7, 8].includes(step)) return step
  if ([9, 10].includes(step)) return 9
  return 5
}
export function journeyPhase(step: number): number {
  const normalized = normalizeJourneyStep(step)
  return normalized === 5 ? 0 : normalized === 3 ? 1 : normalized === 9 ? 3 : 2
}
export function phaseSections(phase: number): number[] {
  return [[2, 5], [3, 4], [6, 7, 8], [2, 3, 4, 5, 6, 7, 8]][phase] || []
}
export function suggestedSystemID(repo: string, name: string): string {
  const slug = (value: string) => value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 32)
  const value = slug(repo) || slug(name)
  if (value) return value
  // Stable non-secret fallback for Chinese display names.
  let hash = 2166136261
  for (const character of name) hash = Math.imul(hash ^ character.codePointAt(0)!, 16777619)
  return name.trim() ? `project-${(hash >>> 0).toString(36)}` : ''
}

// Existing valid IDs are stable deployment identities, even after a display-name change.
export function resolveSystemID(existing: string, repo: string, name: string): string {
  if (/^[a-z0-9][a-z0-9-]*$/.test(existing)) return existing
  const normalized = suggestedSystemID(existing, '')
  return normalized || suggestedSystemID(repo, name)
}
