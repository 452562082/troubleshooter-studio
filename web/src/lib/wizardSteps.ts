export const DATA_STORE_STEP = 7
export const OBSERVABILITY_STEP = 8

export function shouldInitializeObservability(step: number): boolean {
  return step === OBSERVABILITY_STEP
}
