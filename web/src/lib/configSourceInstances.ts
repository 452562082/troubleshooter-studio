export interface ConfigSourceInstance {
  id: string
  type: string
}

export function nextSourceInstanceID(type: string, instances: readonly ConfigSourceInstance[]): string {
  const used = new Set(instances.map(item => item.id))
  if (!used.has(type)) return type
  for (let n = 2; ; n++) {
    const candidate = `${type}-${n}`
    if (!used.has(candidate)) return candidate
  }
}

export function sourceStateKey(sourceID: string, envID: string, primarySourceID = ''): string {
  return !sourceID || sourceID === primarySourceID ? envID : `${sourceID}::${envID}`
}

export function sourceTypeFor(
  sourceID: string,
  instances: readonly ConfigSourceInstance[],
): string {
  return instances.find(item => item.id === sourceID)?.type || sourceID
}
