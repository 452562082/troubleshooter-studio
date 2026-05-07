// wizardStep.ts —— wizard step 相关的 pure helpers,单测覆盖。
//
// 当前只一个函数:migrateSavedStep。InitPage 启动时把 saved draft 里存的 currentStep
// 按 wizardSchema 迁移到当前 schema,行为跟原 inline 表达式严格等价(不引入额外
// 下限 clamp,避免改变 step=0/负数/NaN 等异常输入的渲染行为 —— 那些场景由 InitPage
// 自己的渲染兜底处理,本 helper 只管 schema 偏移)。
//
// schema 历史:
//   - schema=1(老):wizard step 1 是"系统基本信息"
//   - schema=2(当前):加了"欢迎页"占 step 1,之前所有 step 编号 +1

/** migrateSavedStep 把 saved draft 里的 currentStep 按 wizardSchema 偏移到新 schema。
 *
 *  跟原 InitPage inline 逻辑严格等价:
 *    saved?.currentStep != null
 *      ? Math.min(savedSchema >= 2 ? saved.currentStep : saved.currentStep + 1, totalSteps)
 *      : 1
 *
 *  特意**不**做下限 clamp(>=1)—— 保留原行为完全一致,以免阶段 1 那次重构里被怀疑
 *  的"白屏可能跟 currentStep 钳制差异有关"重演。下限保护留给 InitPage 自己的运行时
 *  兜底逻辑(clampCurrentStep / onErrorCaptured)。
 */
export function migrateSavedStep(
  savedStep: number | null | undefined,
  savedSchema: number | null | undefined,
  totalSteps: number,
): number {
  if (savedStep == null) return 1
  const schema = savedSchema ?? 1
  const migrated = schema >= 2 ? savedStep : savedStep + 1
  return Math.min(migrated, totalSteps)
}
